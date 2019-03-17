version := $(shell git describe --tags)
revision := $(shell git rev-parse HEAD)
release := $(shell git describe --tags | cut -d"-" -f 1,2)
build_date := $(shell date -Iseconds --utc)

GO_LDFLAGS := "-X main.Version=${version} -X main.Revision=${revision}"

all: build

.PHONY: install
install:
	cp zeromon.service /lib/systemd/system/zeromon.service
	cp zeromon /usr/local/bin/zeromon

.PHONY: uninstall
uninstall:
	rm /lib/systemd/system/zeromon.service
	rm /usr/local/bin/zeromon

.PHONY: deps
deps:
	go list -f '{{ join .Imports "\n" }}' | sort | uniq | grep "/" | grep -v "golang.org/x" | xargs go get -v

.PHONY: upgrade
upgrade:
	go list -f '{{ join .Imports "\n" }}' | sort | uniq | grep "/" | xargs go get -v -u

.PHONY: update
update:
	cp zeromon /usr/local/bin/zeromon

.PHONY: build
build:
	go build -ldflags $(GO_LDFLAGS) zeromon.go
