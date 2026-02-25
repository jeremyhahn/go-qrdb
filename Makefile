# go-qrdb Build System
# Distributed key-value store powered by multi-group Raft with QUIC transport

.DEFAULT_GOAL := build

# Project metadata
MODULE        := github.com/jeremyhahn/go-qrdb
BINARY        := qrdbd
BUILD_DIR     := build
DOCKER_DIR    := $(BUILD_DIR)/docker
COVER_DIR     := $(BUILD_DIR)/coverage
VERSION       := $(shell cat VERSION 2>/dev/null || echo "0.0.1-alpha")
GIT_COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS       := -X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

# Go tooling
GO            := go
GOTEST        := $(GO) test -race -count=1 -v
GOBUILD       := $(GO) build -ldflags "$(LDFLAGS)"
GOVET         := $(GO) vet
GOFMT         := gofmt
GOLINT        := golangci-lint
GOSEC         := gosec
GOVULNCHECK   := govulncheck

# gosec exclusions (documented):
#   G103: unsafe calls (test reflect/unsafe access for unexported fields)
#   G104: unhandled errors (false positive on hash.Write)
#   G115: integer overflow conversions (protocol-spec bounded shard/replica IDs)
#   G304: file path via variable (config paths constructed internally)
#   G704: SSRF taint analysis (leader forwarding URL from internal peer registry, not user input)
#   G705: XSS taint analysis (KV GET returns application/octet-stream, not HTML)
GOSEC_EXCLUDE := G103,G104,G115,G304,G704,G705

# Coverage
COVER_MIN     := 91

# Docker
DOCKER_IMAGE  := go-qrdb-dev
DOCKER_TAG    := latest
COMPOSE_FILE  := $(DOCKER_DIR)/docker-compose.yml

# Benchmarks
KV_BENCHTIME  ?= 5s

# Package paths
PKG_CONFIG     := ./pkg/config/...
PKG_SERVICE    := ./pkg/service/...
PKG_STORE      := ./pkg/store/...
PKG_REST       := ./pkg/api/rest/...
PKG_GRPC       := ./pkg/api/grpc/...
PKG_SERVER     := ./pkg/server/...
PKG_CMD        := ./cmd/qrdbd/...
PKG_PROTOCOL   := ./pkg/protocol/...
PKG_MVCC       := ./pkg/mvcc/...
PKG_WATCH      := ./pkg/watch/...
PKG_CONSISTENCY := ./pkg/consistency/...
PKG_LEASE      := ./pkg/lease/...
PKG_INDEX      := ./pkg/index/...
PKG_TIMESERIES := ./pkg/timeseries/...
PKG_WRITEMODE  := ./pkg/writemode/...
PKG_BATCH      := ./pkg/batch/...

# Integration test paths
INT_TEST_DIR  := ./test/integration

# ============================================================================
# Primary targets
# ============================================================================

.PHONY: all
all: build test lint

.PHONY: build
build:
	$(GOBUILD) -o bin/$(BINARY) ./cmd/qrdbd

.PHONY: build-cli
build-cli:
	$(GOBUILD) -o bin/qrdb ./cmd/qrdb

.PHONY: clean
clean:
	$(GO) clean ./...
	rm -rf bin/ $(COVER_DIR) *.log *.test *.out qrdb qrdbd

.PHONY: proto
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/api/grpc/proto/qrdbv1/qrdb.proto

# ============================================================================
# Unit tests
# ============================================================================

.PHONY: test
test:
	$(GOTEST) ./pkg/... ./cmd/...

.PHONY: test-config
test-config:
	$(GOTEST) $(PKG_CONFIG)

.PHONY: test-service
test-service:
	$(GOTEST) $(PKG_SERVICE)

.PHONY: test-store
test-store:
	$(GOTEST) $(PKG_STORE)

.PHONY: test-rest
test-rest:
	$(GOTEST) $(PKG_REST)

.PHONY: test-grpc
test-grpc:
	$(GOTEST) $(PKG_GRPC)

.PHONY: test-server
test-server:
	$(GOTEST) $(PKG_SERVER)

.PHONY: test-cmd
test-cmd:
	$(GOTEST) $(PKG_CMD)

.PHONY: test-protocol
test-protocol:
	$(GOTEST) $(PKG_PROTOCOL)

.PHONY: test-mvcc
test-mvcc:
	$(GOTEST) $(PKG_MVCC)

.PHONY: test-watch
test-watch:
	$(GOTEST) $(PKG_WATCH)

.PHONY: test-consistency
test-consistency:
	$(GOTEST) $(PKG_CONSISTENCY)

.PHONY: test-lease
test-lease:
	$(GOTEST) $(PKG_LEASE)

.PHONY: test-index
test-index:
	$(GOTEST) $(PKG_INDEX)

.PHONY: test-timeseries
test-timeseries:
	$(GOTEST) $(PKG_TIMESERIES)

.PHONY: test-writemode
test-writemode:
	$(GOTEST) $(PKG_WRITEMODE)

.PHONY: test-batch
test-batch:
	$(GOTEST) $(PKG_BATCH)

# ============================================================================
# Integration tests (always run in devcontainer)
# ============================================================================

# Public target: build devcontainer image and run tests inside it.
.PHONY: integration-test
integration-test:
	@echo "==> Building integration container..."
	@docker compose -f $(COMPOSE_FILE) build --quiet integration
	@echo "==> Running integration tests..."
	docker compose -f $(COMPOSE_FILE) run --rm integration make _integration-test

# Internal target: executed inside the devcontainer. Not intended for host use.
# Note: runs only the top-level integration package. The API sub-suite
# (test/integration/api/) requires its own docker-compose stack and is
# exercised separately via the integration-test-api target.
.PHONY: _integration-test
_integration-test:
	$(GOTEST) -tags integration -timeout=300s $(INT_TEST_DIR)

# Multi-protocol API integration tests (docker compose with qrdbd server).
.PHONY: integration-test-api
integration-test-api:
	@echo "==> Running multi-protocol API integration tests..."
	cd test/integration/api && docker compose up --build --abort-on-container-exit integration-tests
	cd test/integration/api && docker compose down -v

# Run multi-protocol API tests locally (requires running qrdbd server).
.PHONY: integration-test-api-local
integration-test-api-local:
	$(GOTEST) -tags integration -timeout=15m ./test/integration/api/...

# ============================================================================
# Benchmarks (always run in devcontainer)
# ============================================================================

# Public target: build devcontainer image and run benchmarks inside it.
.PHONY: bench-qrdb-kv-fair
bench-qrdb-kv-fair:
	docker compose -f $(COMPOSE_FILE) run --rm --build -e KV_BENCHTIME=$(KV_BENCHTIME) benchmark make _bench-qrdb-kv-fair

# Internal target: executed inside the devcontainer. Not intended for host use.
.PHONY: _bench-qrdb-kv-fair
_bench-qrdb-kv-fair:
	$(GOTEST) -tags benchmark -bench=. -benchtime=$(KV_BENCHTIME) ./test/performance/qrdb/...

# ============================================================================
# Coverage
# ============================================================================

# check-coverage: extract percentage from coverage profile and fail if below threshold
# Usage: $(call check-coverage,profile.out,package-name)
define check-coverage
	@COVERAGE=$$($(GO) tool cover -func=$(1) | grep total | awk '{print $$3}' | sed 's/%//'); \
	echo "$(2) coverage: $${COVERAGE}%"; \
	if [ $$(echo "$${COVERAGE} < $(COVER_MIN)" | bc -l) -eq 1 ]; then \
		echo "FAIL: $(2) coverage $${COVERAGE}% is below minimum $(COVER_MIN)%"; \
		exit 1; \
	fi
endef

.PHONY: coverage
coverage: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/total.out $$($(GO) list ./pkg/... ./cmd/... | grep -v '/proto/')
	$(call check-coverage,$(COVER_DIR)/total.out,total)

.PHONY: coverage-config
coverage-config: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/config.out $(PKG_CONFIG)
	$(call check-coverage,$(COVER_DIR)/config.out,config)

.PHONY: coverage-service
coverage-service: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/service.out $(PKG_SERVICE)
	$(call check-coverage,$(COVER_DIR)/service.out,service)

.PHONY: coverage-store
coverage-store: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/store.out $(PKG_STORE)
	$(call check-coverage,$(COVER_DIR)/store.out,store)

.PHONY: coverage-rest
coverage-rest: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/rest.out $(PKG_REST)
	$(call check-coverage,$(COVER_DIR)/rest.out,rest)

.PHONY: coverage-grpc
coverage-grpc: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/grpc.out $(PKG_GRPC)
	$(call check-coverage,$(COVER_DIR)/grpc.out,grpc)

.PHONY: coverage-server
coverage-server: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/server.out $(PKG_SERVER)
	$(call check-coverage,$(COVER_DIR)/server.out,server)

.PHONY: coverage-cmd
coverage-cmd: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/cmd.out $(PKG_CMD)
	$(call check-coverage,$(COVER_DIR)/cmd.out,cmd)

.PHONY: coverage-protocol
coverage-protocol: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/protocol.out $(PKG_PROTOCOL)
	$(call check-coverage,$(COVER_DIR)/protocol.out,protocol)

.PHONY: coverage-mvcc
coverage-mvcc: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/mvcc.out $(PKG_MVCC)
	$(call check-coverage,$(COVER_DIR)/mvcc.out,mvcc)

.PHONY: coverage-watch
coverage-watch: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/watch.out $(PKG_WATCH)
	$(call check-coverage,$(COVER_DIR)/watch.out,watch)

.PHONY: coverage-consistency
coverage-consistency: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/consistency.out $(PKG_CONSISTENCY)
	$(call check-coverage,$(COVER_DIR)/consistency.out,consistency)

.PHONY: coverage-lease
coverage-lease: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/lease.out $(PKG_LEASE)
	$(call check-coverage,$(COVER_DIR)/lease.out,lease)

.PHONY: coverage-index
coverage-index: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/index.out $(PKG_INDEX)
	$(call check-coverage,$(COVER_DIR)/index.out,index)

.PHONY: coverage-timeseries
coverage-timeseries: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/timeseries.out $(PKG_TIMESERIES)
	$(call check-coverage,$(COVER_DIR)/timeseries.out,timeseries)

.PHONY: coverage-writemode
coverage-writemode: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/writemode.out $(PKG_WRITEMODE)
	$(call check-coverage,$(COVER_DIR)/writemode.out,writemode)

.PHONY: coverage-batch
coverage-batch: | $(COVER_DIR)
	$(GOTEST) -coverprofile=$(COVER_DIR)/batch.out $(PKG_BATCH)
	$(call check-coverage,$(COVER_DIR)/batch.out,batch)

$(COVER_DIR):
	mkdir -p $(COVER_DIR)

# ============================================================================
# CI pipeline (runs in devcontainer with all tools pre-installed)
# ============================================================================

.PHONY: ci
ci:
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer make _ci

.PHONY: _ci
_ci: fmt vet lint gosec vuln build test
	@echo "CI pipeline passed"

# ============================================================================
# Linting and static analysis
# ============================================================================

.PHONY: lint
lint:
	$(GOLINT) run --timeout=5m ./...

.PHONY: vet
vet:
	$(GOVET) ./...

.PHONY: fmt
fmt:
	@UNFORMATTED=$$($(GOFMT) -l .); \
	if [ -n "$${UNFORMATTED}" ]; then \
		echo "Files not formatted:"; \
		echo "$${UNFORMATTED}"; \
		exit 1; \
	fi; \
	echo "All files formatted correctly"

# ============================================================================
# Security scanning
# ============================================================================

.PHONY: gosec
gosec:
	$(GOSEC) -exclude=$(GOSEC_EXCLUDE) -exclude-dir=test/performance -severity=medium -confidence=medium ./...

.PHONY: vuln
vuln:
	$(GOVULNCHECK) ./... || echo "WARNING: govulncheck found stdlib vulnerabilities (upgrade Go to fix)"

# ============================================================================
# Docker
# ============================================================================

.PHONY: docker-build
docker-build:
	docker build -t qrdbd:$(VERSION) .

.PHONY: docker-build-integration
docker-build-integration:
	docker build -t go-qrdb-integration:$(DOCKER_TAG) -f $(DOCKER_DIR)/devcontainer/Dockerfile .

.PHONY: docker-run
docker-run:
	docker run --rm -p 63001:63001 -p 63002:63002 -p 63003:63003 qrdbd:$(VERSION)

.PHONY: docker-run-devcontainer
docker-run-devcontainer:
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer

# ============================================================================
# Help
# ============================================================================

.PHONY: help
help:
	@echo "go-qrdb Build System"
	@echo ""
	@echo "Primary:"
	@echo "  all                    Build, test, and lint (default)"
	@echo "  build                  Compile daemon binary to bin/qrdbd"
	@echo "  build-cli              Compile client CLI binary to bin/qrdb"
	@echo "  clean                  Remove build artifacts and coverage reports"
	@echo "  proto                  Regenerate protobuf code"
	@echo ""
	@echo "Unit Tests:"
	@echo "  test                   Run all unit tests with race detector"
	@echo "  test-<pkg>             Run tests for a specific package"
	@echo "    packages: config, service, store, rest, grpc, server, cmd,"
	@echo "              protocol, mvcc, watch, consistency, lease, index, timeseries,"
	@echo "              writemode, batch"
	@echo ""
	@echo "Integration Tests (run in devcontainer automatically):"
	@echo "  integration-test           Run all integration tests in devcontainer"
	@echo "  integration-test-api       Run multi-protocol API tests (docker compose)"
	@echo "  integration-test-api-local Run multi-protocol API tests locally"
	@echo ""
	@echo "Benchmarks (run in devcontainer automatically):"
	@echo "  bench-qrdb-kv-fair    Run KV fair benchmarks (KV_BENCHTIME=5s)"
	@echo ""
	@echo "Coverage (minimum $(COVER_MIN)%):"
	@echo "  coverage               Run total coverage report"
	@echo "  coverage-<pkg>         Run coverage for a specific package"
	@echo "    packages: config, service, store, rest, grpc, server, cmd,"
	@echo "              protocol, mvcc, watch, consistency, lease, index, timeseries,"
	@echo "              writemode, batch"
	@echo ""
	@echo "CI Pipeline (runs in devcontainer):"
	@echo "  ci                     Run full CI pipeline (fmt, vet, lint, gosec, vuln, build, test)"
	@echo ""
	@echo "Linting:"
	@echo "  lint                   Run golangci-lint"
	@echo "  vet                    Run go vet"
	@echo "  fmt                    Check gofmt formatting"
	@echo ""
	@echo "Security:"
	@echo "  gosec                  Run gosec security scanner"
	@echo "  vuln                   Run govulncheck vulnerability scanner"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build                 Build production image"
	@echo "  docker-build-integration     Build integration test image"
	@echo "  docker-run                   Run production container"
	@echo "  docker-run-devcontainer      Run devcontainer shell"
