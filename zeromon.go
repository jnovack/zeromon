package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	sensor "github.com/d2r2/go-dht"
	device "github.com/d2r2/go-hd44780"
	i2c "github.com/d2r2/go-i2c"
	logger "github.com/d2r2/go-logger"
	humanize "github.com/dustin/go-humanize"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var lg = logger.NewPackageLogger("main", logger.NotifyLevel)
var m sync.Mutex
var lcd *device.Lcd

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
	Version string
	// Revision supplied by the linker
	Revision string
	// GoVersion supplied by the runtime
	GoVersion = runtime.Version()
)

func buildInfo() string {
	return fmt.Sprintf("zeromon version %s git revision %s go version %s", Version, Revision, GoVersion)
}

func main() {
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
	//* Metrics Handler *//
	go func() {
		for {
			go funcWithChanResult()

			temp := env.GetTemperature()
			hum := env.GetHumidity()
			timestamp := env.GetTimestamp()

			if timestamp.Unix() > 0 {
				lg.Notifyf("Updated: Temperature = %.1f°F, Humidity = %.1f%%, Last Checked = %s, Unix = %d",
					temp, hum, humanize.Time(timestamp), timestamp.Unix())
				promTemp.With(prometheus.Labels{"room": "office"}).Set(float64(temp))
				promHum.With(prometheus.Labels{"room": "office"}).Set(float64(hum))
				promTime.With(prometheus.Labels{"room": "office"}).Set(float64(timestamp.Unix()))
				go WriteMessage(lcd, fmt.Sprintf("Temp: %.1fF", temp), device.SHOW_LINE_1)
				go WriteMessage(lcd, fmt.Sprintf("Hum : %.1f%%", hum), device.SHOW_LINE_2)
			}
			time.Sleep(5000 * time.Millisecond)
		}
	}()

	BacklightOn(lcd)

	//* HTTP Handler *//
	// go func() {
	// The Handler function provides a default handler to expose metrics
	// via an HTTP server. "/metrics" is the usual endpoint for that.
	http.Handle("/metrics", promhttp.Handler())
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		lg.Errorf("http err: %s", err)
	}
	// }()

}

func init() {
	buildInfo()
	logger.ChangePackageLogLevel("dht", logger.ErrorLevel)
	logger.ChangePackageLogLevel("i2c", logger.InfoLevel)

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		BacklightOff(lcd)
		// Run Cleanup
		os.Exit(0)
	}()

	prometheus.MustRegister(promTemp)
	prometheus.MustRegister(promHum)
	prometheus.MustRegister(promTime)

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

func WriteMessage(lcd *device.Lcd, str string, line device.ShowOptions) {
	m.Lock()
	err := lcd.ShowMessage(str, line)
	m.Unlock()
	if err != nil {
		lg.Fatalf("WriteMessage: %s", err)
	}
	return
}

func BacklightOn(lcd *device.Lcd) {
	m.Lock()
	err := lcd.BacklightOn()
	m.Unlock()
	if err != nil {
		lg.Fatalf("BacklightOn: %s", err)
	}
	return
}

func BacklightOff(lcd *device.Lcd) {
	m.Lock()
	err := lcd.BacklightOff()
	m.Unlock()
	if err != nil {
		lg.Fatalf("BacklightOff: %s", err)
	}
	return
}

func Clear(lcd *device.Lcd) error {
	m.Lock()
	err := lcd.Clear()
	m.Unlock()
	return err
}

func Home(lcd *device.Lcd) error {
	m.Lock()
	err := lcd.Home()
	m.Unlock()
	return err
}
