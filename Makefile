version := $(shell git describe --tags)
revision := $(shell git rev-parse HEAD)
release := $(shell git describe --tags | cut -d"-" -f 1,2)
build_date := $(shell date -Iseconds --utc)

GO_LDFLAGS := "-X main.Version=${version} -X main.Revision=${revision}"

all: build

.PHONY: deps
deps:
	go list -f '{{ join .Imports "\n" }}' | xargs go get -vu

.PHONY: build
build:
	go build -ldflags $(GO_LDFLAGS) zeromon.go
