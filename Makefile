VERSION ?= 0.2.0
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X github.com/zyvorai/nodra/internal/version.Version=$(VERSION) -X github.com/zyvorai/nodra/internal/version.Commit=$(COMMIT) -X github.com/zyvorai/nodra/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: all test race vet fmt build clean release-check smoke demo-client
all: test build
fmt:
	@test -z "$$(gofmt -l .)" || (echo "Run gofmt on:"; gofmt -l .; exit 1)
vet:
	go vet ./...
test:
	go test ./...
race:
	go test -race ./...
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodra-server ./cmd/nodra-server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodrad ./cmd/nodrad
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodractl ./cmd/nodractl
smoke:
	./scripts/smoke.sh
demo-client:
	./scripts/demo-client.sh
release-check:
	./scripts/release-check.sh
clean:
	rm -rf bin dist coverage.out
