MODULE  := github.com/vyncint/openshell-driver-applecontainer
BINARY  := openshell-driver-applecontainer
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

PREFIX ?= /opt/homebrew

.PHONY: build install test lint vet sec proto prep e2e soak clean

# Gosec exclusions and rationale are documented in .github/workflows/ci.yml.
GOSEC_EXCLUDE := G101,G204,G304,G703

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

# Install the driver binary; follow with `$(BINARY) setup`.
install: build
	install -d "$(PREFIX)/bin"
	install -m 0755 bin/$(BINARY) "$(PREFIX)/bin/$(BINARY)"

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

# Run the same security scanners as CI. Installs pinned tools on demand.
sec:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -exclude=$(GOSEC_EXCLUDE) -exclude-dir=internal/gen -quiet ./...
	@command -v gitleaks >/dev/null 2>&1 && gitleaks git --no-banner --redact . || echo "sec: gitleaks not installed (brew install gitleaks) — skipping secret scan"

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
