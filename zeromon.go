package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/pkg/flagutil"
	sensor "github.com/d2r2/go-dht"
	device "github.com/d2r2/go-hd44780"
	i2c "github.com/d2r2/go-i2c"
	logger "github.com/d2r2/go-logger"
	humanize "github.com/dustin/go-humanize"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tabalt/pidfile"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

/*
	AUX1 - GPIO 4 (Pin 7)
	AUX2 - GPIO 23 (Pin 16)
	AUX3 - GPIO 17 (Pin 11)
		   GPIO 27 (Pin 13)
		   GPIO 22 (Pin 15)
*/

// CmdLineOpts structure for the command line options
type CmdLineOpts struct {
	room     string
	aiokey   string
	apiURL   string
	aiouser  string
	port     int
	version  bool
	intLevel int
	pidfile  string
}

var opts CmdLineOpts

var lg = logger.NewPackageLogger("main", logger.NotifyLevel)
var m sync.Mutex
var lcd *device.Lcd
var client MQTT.Client
var token MQTT.Token

// Environment structure of temperature/humidity
type Environment struct {
	temperature float32
	humidity    float32
	timestamp   time.Time
	sync.RWMutex
}

func (o *Environment) GetTemperature() float32 {
	o.RLock()         // lock for reading, blocks until the Mutex is ready
	defer o.RUnlock() // make SURE you do this, else it will be locked permanently
	return o.temperature
}

func (o *Environment) GetHumidity() float32 {
	o.RLock()         // lock for reading, blocks until the Mutex is ready
	defer o.RUnlock() // make SURE you do this, else it will be locked permanently
	return o.humidity
}

func (o *Environment) GetTimestamp() time.Time {
	o.RLock()         // lock for reading, blocks until the Mutex is ready
	defer o.RUnlock() // make SURE you do this, else it will be locked permanently
	return o.timestamp
}

func (o *Environment) PutTemperature(value float32) {
	o.Lock()         // lock for writing, blocks until the Mutex is ready
	defer o.Unlock() // again, make SURE you do this, else it will be locked permanently
	o.temperature = value
}

func (o *Environment) PutHumidity(value float32) {
	o.Lock()         // lock for writing, blocks until the Mutex is ready
	defer o.Unlock() // again, make SURE you do this, else it will be locked permanently
	o.humidity = value
}

func (o *Environment) PutTimestamp(value time.Time) {
	o.Lock()         // lock for reading, blocks until the Mutex is ready
	defer o.Unlock() // make SURE you do this, else it will be locked permanently
	o.timestamp = value
}

var env Environment

var (
	promTemp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "temperature",
		Help: "Current temperature value of the sensor.",
	}, []string{"room"})
	promHum = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "humidity",
		Help: "Current humidity value of the sensor.",
	}, []string{"room"})
	promTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "time",
		Help: "UnixTime the sensor was last checked.",
	}, []string{"room"})
)

var (
	// Version supplied by the linker
	Version = "v0.0.0"
	// Revision supplied by the linker
	Revision = "00000000"
	// GoVersion supplied by the runtime
	GoVersion = runtime.Version()
)

func buildInfo() string {
	return fmt.Sprintf("zeromon version %s git revision %s go version %s", Version, Revision, GoVersion)
}

func main() {
	defer logger.FinalizeLogger()

	// I HATE THIS HAS TO BE IN MAIN! Why am I not good enough yet??
	i2c, err := i2c.NewI2C(0x27, 1)
	if err != nil {
		lg.Fatalf("i2c.NewI2C: %s", err)
	}
	// Free I2C connection on exit
	defer i2c.Close()
	// Construct lcd-device connected via I2C connection
	lcd, err = device.NewLcd(i2c, device.LCD_16x2)
	if err != nil {
		lg.Fatalf("device.NewLcd: %s", err)
	}

	BacklightOff(lcd)
	go WriteMessage(lcd, "ZeroMon", device.SHOW_LINE_1)
	go WriteMessage(lcd, fmt.Sprintf("%s", Version), device.SHOW_LINE_2)

	//* Metrics Handler *//
	go func() {
		for {
			go funcWithChanResult()

			temp := env.GetTemperature()
			hum := env.GetHumidity()
			timestamp := env.GetTimestamp()

			if timestamp.Unix() > 0 {
				lg.Infof("Updated: Temperature = %.1f°F, Humidity = %.1f%%, Last Checked = %s, Unix = %d",
					temp, hum, humanize.Time(timestamp), timestamp.Unix())
				promTemp.With(prometheus.Labels{"room": opts.room}).Set(float64(temp))
				promHum.With(prometheus.Labels{"room": opts.room}).Set(float64(hum))
				promTime.With(prometheus.Labels{"room": opts.room}).Set(float64(timestamp.Unix()))
				go WriteMessage(lcd, fmt.Sprintf("Temp: %.1fF     ", temp), device.SHOW_LINE_1)
				go WriteMessage(lcd, fmt.Sprintf("Hum : %.1f%%     ", hum), device.SHOW_LINE_2)
			}
			time.Sleep(5000 * time.Millisecond)
		}
	}()

	go func() {
		for {
			go funcWithChanResult()

			timestamp := env.GetTimestamp()

			if timestamp.Unix() > 0 {
				temp := env.GetTemperature()
				hum := env.GetHumidity()
				go publishData("temperature", fmt.Sprintf("%.1f", temp))
				go publishData("humidity", fmt.Sprintf("%.1f", hum))
			}
			time.Sleep(30 * time.Second)
		}
	}()

	BacklightOn(lcd)
	//* HTTP Handler *//
	// go func() {
	// The Handler function provides a default handler to expose metrics
	// via an HTTP server. "/metrics" is the usual endpoint for that.
	http.Handle("/metrics", promhttp.Handler())
	// https://github.com/prometheus/prometheus/wiki/Default-port-allocations
	err = http.ListenAndServe(":9204", nil)
	if err != nil {
		lg.Errorf("http err: %s", err)
	}
	// }()

}

func init() {
	fmt.Println(buildInfo())
	logger.ChangePackageLogLevel("dht", logger.ErrorLevel)
	logger.ChangePackageLogLevel("i2c", logger.ErrorLevel)

	flag.StringVar(&opts.aiokey, "aiokey", "", "io.adafruit.com API Key (AIO)")
	flag.StringVar(&opts.aiouser, "aiouser", "", "io.adafruit.com Username")
	flag.StringVar(&opts.room, "room", "", "room name")
	flag.StringVar(&opts.pidfile, "pidfile", "/var/run/zeromon.pid", "pidfile")
	flag.IntVar(&opts.port, "port", 9204, "prometheus metrics port")
	flag.IntVar(&opts.intLevel, "loglevel", 4, "log level (0=emerg through 6=debug)")
	flag.BoolVar(&opts.version, "version", false, "print version and exit")
	flagutil.SetFlagsFromEnv(flag.CommandLine, "ZEROMON")

	if opts.version {
		// already printed version
		os.Exit(0)
	}

	pid, _ := pidfile.Create(opts.pidfile)

	logger.ChangePackageLogLevel("main", LogLevel(opts.intLevel))

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		BacklightOff(lcd)
		_ = pid.Clear()
		client.Disconnect(250)
		os.Exit(0)
	}()

	prometheus.MustRegister(promTemp)
	prometheus.MustRegister(promHum)
	prometheus.MustRegister(promTime)

	if opts.aiouser != "" && opts.aiokey != "" && opts.room != "" {
		mqtt := MQTT.NewClientOptions()
		mqtt.AddBroker("tcp://io.adafruit.com:1883")
		mqtt.SetClientID("github.com/jnovack/zeromon")
		mqtt.SetUsername(opts.aiouser)
		mqtt.SetPassword(opts.aiokey)
		mqtt.SetCleanSession(false)
		client = MQTT.NewClient(mqtt)
		if token = client.Connect(); token.Wait() && token.Error() != nil {
			panic(token.Error())
		}
		lg.Infof("Publishing to io.adafruit.com:1883/%s", opts.aiouser)
	} else {
		lg.Warnf("Not publishing statistics.  Username: %s, Key: %s", opts.aiouser, opts.aiokey)
	}
}

func readSensor(done chan bool) {
	temp, hum, _, err := sensor.ReadDHTxxWithRetry(sensor.DHT22, 4, false, 10)
	if err == nil {
		lg.Debugf("readSensor(): Temperature = %2.2f°C, Humidity = %2.2f%%", temp, hum)
		env.PutTemperature(float32(temp)*1.8 + 32)
		env.PutHumidity(float32(hum))
		env.PutTimestamp(time.Now())
	}
	done <- true
}

func funcWithChanResult() {
	done := make(chan bool, 1)
	go readSensor(done)
	<-done
	return
}

//* These should probably be in their own file or use the package ones.

// WriteMessage writes a message to the LCD at the defined line, char 0
func WriteMessage(lcd *device.Lcd, str string, line device.ShowOptions) {
	m.Lock()
	err := lcd.ShowMessage(str, line)
	m.Unlock()
	if err != nil {
		lg.Fatalf("WriteMessage: %s", err)
	}
	return
}

// BacklightOn turns the backlight on
func BacklightOn(lcd *device.Lcd) {
	m.Lock()
	err := lcd.BacklightOn()
	m.Unlock()
	if err != nil {
		lg.Fatalf("BacklightOn: %s", err)
	}
	return
}

// BacklightOff turns the backlight off
func BacklightOff(lcd *device.Lcd) {
	m.Lock()
	err := lcd.BacklightOff()
	m.Unlock()
	if err != nil {
		lg.Fatalf("BacklightOff: %s", err)
	}
	return
}

// Clear clears the LCD display
func Clear(lcd *device.Lcd) error {
	m.Lock()
	err := lcd.Clear()
	m.Unlock()
	return err
}

// Home moves the cursor back to 0,0
func Home(lcd *device.Lcd) error {
	m.Lock()
	err := lcd.Home()
	m.Unlock()
	return err
}

func publishData(key string, value string) {
	if opts.aiouser != "" && opts.aiokey != "" && opts.room != "" {
		topic := fmt.Sprintf("%s/feeds/%s-%s", opts.aiouser, opts.room, key)
		lg.Debugf("Publishing '%s' to %s", value, topic)
		token := client.Publish(topic, byte(0), false, value)
		token.Wait()
	}
}

func LogLevel(i int) logger.LogLevel {
	switch i {
	case 0:
		return logger.FatalLevel
	case 1:
		return logger.PanicLevel
	case 2:
		return logger.ErrorLevel
	case 3:
		return logger.WarnLevel
	case 4:
		return logger.NotifyLevel
	case 5:
		return logger.InfoLevel
	case 6:
		return logger.DebugLevel
	default:
		return logger.InfoLevel
	}
}
