SHELL := /bin/bash

BINARY := airlock
BINDIR := bin
ENTRYPOINT := ./cmd/airlock

GOBIN := $(shell go env GOBIN)
ifeq ($(strip $(GOBIN)),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: help tidy fmt vet lint test build dist run install uninstall clean

help:
	@echo "Targets:"
	@echo "  make tidy   - Run go mod tidy"
	@echo "  make fmt    - Format Go code"
	@echo "  make vet    - Run go vet"
	@echo "  make lint   - Run golangci-lint"
	@echo "  make test   - Run tests"
	@echo "  make build  - Build $(BINARY) binary"
	@echo "  make dist   - Build $(BINARY) for every platform into dist/ (GoReleaser)"
	@echo "  make run    - Run the CLI"
	@echo "  make install   - Install $(BINARY) to $(GOBIN)"
	@echo "  make uninstall - Remove $(BINARY) from $(GOBIN)"
	@echo "  make clean  - Remove build artifacts"

tidy:
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run --config .golangci.yml ./...

test:
	go test ./...

build:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/$(BINARY) $(ENTRYPOINT)

dist:
	goreleaser release --snapshot --clean

run:
	go run $(ENTRYPOINT)

install:
	mkdir -p $(GOBIN)
	go build -o $(GOBIN)/$(BINARY) $(ENTRYPOINT)
	@echo "Installed $(BINARY) to $(GOBIN)/$(BINARY)"

uninstall:
	rm -f $(GOBIN)/$(BINARY)
	@echo "Removed $(GOBIN)/$(BINARY)"

clean:
	rm -rf $(BINDIR) dist
