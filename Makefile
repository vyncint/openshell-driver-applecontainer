MODULE  := github.com/vyncint/openshell-driver-applecontainer
BINARY  := openshell-driver-applecontainer
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test lint vet proto prep e2e soak clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

proto:
	buf generate

# Idempotent host preparation: vmnet network, default image, supervisor cache.
prep: build
	./e2e/prep.sh

e2e: build
	./e2e/smoke.sh

soak: build
	./e2e/soak.sh

clean:
	rm -rf bin dist coverage.out
