.PHONY: fmt fmt-check vet test build run clean check

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
BINARY := bin/agentflow

fmt:
	gofmt -w cmd internal

fmt-check:
	@gofmt -l cmd internal

vet:
	go vet ./...

test:
	go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

run:
	go run ./cmd/agentflow

clean:
	rm -rf bin

check: fmt vet test build
