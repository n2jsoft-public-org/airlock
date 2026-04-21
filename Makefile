SHELL := /bin/bash

BINARY := acme
BINDIR := bin
ENTRYPOINT := ./cmd/cli

.PHONY: help tidy fmt vet lint test build run clean

help:
	@echo "Targets:"
	@echo "  make tidy   - Run go mod tidy"
	@echo "  make fmt    - Format Go code"
	@echo "  make vet    - Run go vet"
	echo "  make lint   - Run golangci-lint"
	@echo "  make test   - Run tests"
	@echo "  make build  - Build $(BINARY) binary"
	@echo "  make run    - Run the CLI"
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

run:
	go run $(ENTRYPOINT)

clean:
	rm -rf $(BINDIR)
