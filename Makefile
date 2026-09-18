# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
VERSION ?= 0.2.2
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X github.com/zyvorai/nodra/internal/version.Version=$(VERSION) -X github.com/zyvorai/nodra/internal/version.Commit=$(COMMIT) -X github.com/zyvorai/nodra/internal/version.BuildDate=$(BUILD_DATE)

# Configurable ports (also: --port on scripts, NODRA_PORT env, .deploy-last)
PORT ?= $(NODRA_PORT)
HOST ?= $(NODRA_HOST)

.PHONY: all test race vet fmt build clean release-check smoke smoke-relay-bridge demo-client demo-k8s test-all deploy qualify help ci status deploy-remote
all: test build ## Test, then build

fmt: ## Fail if any Go file needs gofmt
	@test -z "$$(gofmt -l .)" || (echo "Run gofmt on:"; gofmt -l .; exit 1)
vet: ## go vet
	go vet ./...
test: ## Unit tests
	go test ./...
race: ## Tests with the race detector
	go test -race ./...
build: ## Build server, agent, ctl, sim, and relay bridge
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodra-server ./cmd/nodra-server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodrad ./cmd/nodrad
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodractl ./cmd/nodractl
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodra-sim ./cmd/nodra-sim
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nodra-relay-bridge ./cmd/nodra-relay-bridge
qualify: build
	python3 scripts/qualify-matrix.py
smoke:
	./scripts/smoke.sh
smoke-relay-bridge:
	./scripts/smoke-relay-bridge.sh
demo-client:
	./scripts/demo-client.sh $(if $(PORT),--port $(PORT),)
demo-k8s:
	./scripts/demo-k8s.sh $(if $(PORT),--port $(PORT),)
test-all:
	./scripts/test-all.sh $(if $(HOST),--host $(HOST),) $(if $(PORT),--port $(PORT),)
deploy: ## Deploy using HOST (default lab) and USER
	./scripts/deploy-remote.sh $(or $(HOST),212.8.248.187) $(or $(USER),sus) $(if $(PORT),--port $(PORT),)
release-check:
	./scripts/release-check.sh
clean:
	rm -rf bin dist coverage.out

ci: fmt vet race build ## Local gate: gofmt, vet, race tests, build

status: build ## nodractl status (needs NODRA_ADMIN_TOKEN and a running server)
	./bin/nodractl status

deploy-remote: ## Deploy: make deploy-remote H=<host> [U=sus] [PORT=18447]
	@test -n "$(H)" || (echo "Usage: make deploy-remote H=<host> [U=user] [PORT=18447]"; exit 1)
	./scripts/deploy-remote.sh $(H) $(or $(U),sus) $(if $(PORT),--port $(PORT),) $(ARGS)

help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk -F':.*## ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
