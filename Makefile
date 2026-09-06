VERSION ?= 0.2.0
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X github.com/zyvorai/nodra/internal/version.Version=$(VERSION) -X github.com/zyvorai/nodra/internal/version.Commit=$(COMMIT) -X github.com/zyvorai/nodra/internal/version.BuildDate=$(BUILD_DATE)

# Configurable ports (also: --port on scripts, NODRA_PORT env, .deploy-last)
PORT ?= $(NODRA_PORT)
HOST ?= $(NODRA_HOST)

.PHONY: all test race vet fmt build clean release-check smoke demo-client demo-k8s test-all deploy
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
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodra-sim ./cmd/nodra-sim
smoke:
	./scripts/smoke.sh
demo-client:
	./scripts/demo-client.sh $(if $(PORT),--port $(PORT),)
demo-k8s:
	./scripts/demo-k8s.sh $(if $(PORT),--port $(PORT),)
test-all:
	./scripts/test-all.sh $(if $(HOST),--host $(HOST),) $(if $(PORT),--port $(PORT),)
deploy:
	./scripts/deploy-remote.sh $(or $(HOST),212.8.248.187) $(or $(USER),sus) $(if $(PORT),--port $(PORT),)
release-check:
	./scripts/release-check.sh
clean:
	rm -rf bin dist coverage.out
