BINARY  := telegram-agent-cli
PKG     := github.com/AlexDobrushskiy/telegram-agent-cli/internal/cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: build test e2e lint fmt vet tidy clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

# Read-only checks against the real account in $$TGAC_HOME.
e2e:
	TGAC_E2E=1 go test -tags e2e -count=1 -v ./e2e/

vet:
	go vet ./...
	go vet -tags e2e ./e2e/
	go vet -tags demo ./cmd/telegram-agent-cli/

fmt:
	gofmt -w .

lint: vet
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v golangci-lint >/dev/null; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

tidy:
	go mod tidy

clean:
	rm -rf bin dist
