.PHONY: fmt fmt-check vet test test-unit test-local test-pi test-ssh test-e2e test-race build run clean check

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
BINARY := bin/agentflow
PARALLEL ?= 8

fmt:
	gofmt -w cmd internal tests

fmt-check:
	@gofmt -l cmd internal tests

vet:
	go vet ./...

test:
	go test -count=1 -p $(PARALLEL) -parallel $(PARALLEL) ./tests/...

test-unit:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/unit/...

test-local:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/integration/local/...

test-pi:
	go test -count=1 -v ./tests/integration/pi/...

test-ssh:
	go test -count=1 -v ./tests/integration/ssh/...

test-e2e:
	go test -count=1 -v ./tests/e2e/...

test-race:
	go test -race -count=1 ./tests/unit/...

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

run:
	go run ./cmd/agentflow

clean:
	rm -rf bin

check: fmt-check vet build
