# go-qrdb Build System
# Distributed key-value store powered by multi-group Raft with QUIC transport

.DEFAULT_GOAL := build

# Project metadata
MODULE        := github.com/jeremyhahn/go-qrdb
SERVER_BINARY := qrdbd
CLIENT_BINARY := qrdb
COVER_DIR     := coverage
VERSION       := $(shell cat VERSION 2>/dev/null || echo "0.0.1-alpha")
GIT_COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS       := -X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

# Go tooling
GO            := go
# -timeout=30m, because go test defaults to 10m and this suite does not fit.
# pkg/seal alone measured 589.8s on a PASSING run -- 98% of the 10m default,
# under -race with Argon2id KDF work that is meant to be slow. Any load on a
# shared runner tips it over, and the whole job then dies as
# "panic: test timed out after 10m0s" naming whichever package was unlucky,
# which reads like a hang in that package rather than a budget that was always
# too small. That is a runaway guard, not a correctness gate: the CI job itself
# allows 90 minutes, and the integration target already sets its own 25m.
# Raising it removes a false red; it does not weaken any assertion.
GOTEST        := $(GO) test -race -count=1 -v -timeout=30m
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
#   G118: context.Background in goroutine / cancel returned to caller (intentional patterns)
#   G122: filepath.Walk TOCTOU (internal storage directories only)
GOSEC_EXCLUDE := G103,G104,G115,G118,G122,G304,G704,G705

# Coverage
COVER_MIN     := 91

# Docker
DOCKER_IMAGE  := go-qrdb-dev
DOCKER_TAG    := latest
COMPOSE_FILE  := .devcontainer/docker-compose.yml

# Benchmarks
KV_BENCHTIME  ?= 5s
QUICRAFT_ROOT ?= $(shell cd .. && pwd)/go-quicraft

# Optional feature flags (1 = enabled, 0 = disabled)
WITH_QRDB_TS     ?= 1
WITH_QRDB_VECTOR ?= 1
WITH_QRDB_SQL    ?= 1
WITH_QRDB_MYSQL  ?= 1
WITH_QRDB_PGSQL  ?= 1
WITH_QRDB_ETCD   ?= 1
WITH_QRDB_FTS    ?= 0
WITH_QRDB_GPU      ?= 0
WITH_QRDB_EMBEDDED ?= 1
WITH_QRDBD_WEBUI   ?= 0

# Optional codec flags (1 = enabled, 0 = disabled; all default ON)
WITH_CODEC_MSGPACK  ?= 1
WITH_CODEC_YAML     ?= 1
WITH_CODEC_BSON     ?= 1
WITH_CODEC_PROTOBUF ?= 1

# Object-storage (go-objstore) cloud backends, used by the objstore storage
# backend and the backup/restore targets. These gate code in go-objstore (the
# SDKs live THERE, never in qrdb's module): qrdb stays cloud-SDK-free and simple
# by default. Default OFF — opt-in for deployments that want cloud backup
# destinations; downstream composers (xkms, quicbaas) enable what they need. Tag
# names match go-objstore exactly. The provider umbrellas enable the whole family.
WITH_AWS_S3        ?= 0
WITH_MINIO         ?= 0
WITH_GCP_STORAGE   ?= 0
WITH_AZURE_BLOB    ?= 0
WITH_GLACIER       ?= 0
WITH_AZURE_ARCHIVE ?= 0
WITH_AWS   ?= 0
WITH_GCP   ?= 0
WITH_AZURE ?= 0
ifeq ($(WITH_AWS),1)
  WITH_AWS_S3 := 1
  WITH_GLACIER := 1
endif
ifeq ($(WITH_GCP),1)
  WITH_GCP_STORAGE := 1
endif
ifeq ($(WITH_AZURE),1)
  WITH_AZURE_BLOB := 1
  WITH_AZURE_ARCHIVE := 1
endif

# The MySQL and PostgreSQL wire protocols are engines layered over the
# shared pkg/sql core (qrdb_sql), so enabling either implies qrdb_sql.
# "override" wins even when WITH_QRDB_SQL=0 is passed on the command line.
ifeq ($(WITH_QRDB_MYSQL),1)
  override WITH_QRDB_SQL := 1
endif
ifeq ($(WITH_QRDB_PGSQL),1)
  override WITH_QRDB_SQL := 1
endif

# Compute build tags from feature flags
BUILD_TAGS := codec_json raft
ifeq ($(WITH_QRDB_TS),1)
  BUILD_TAGS += qrdb_ts
endif
ifeq ($(WITH_QRDB_VECTOR),1)
  BUILD_TAGS += qrdb_vector
endif
ifeq ($(WITH_QRDB_SQL),1)
  BUILD_TAGS += qrdb_sql
endif
ifeq ($(WITH_QRDB_MYSQL),1)
  BUILD_TAGS += qrdb_mysql
endif
ifeq ($(WITH_QRDB_PGSQL),1)
  BUILD_TAGS += qrdb_pgsql
endif
ifeq ($(WITH_QRDB_ETCD),1)
  BUILD_TAGS += qrdb_etcd
endif
ifeq ($(WITH_QRDB_FTS),1)
  BUILD_TAGS += qrdb_fts
endif
ifeq ($(WITH_QRDB_GPU),1)
  BUILD_TAGS += gpu
endif
ifeq ($(WITH_QRDB_EMBEDDED),1)
  BUILD_TAGS += qrdb_embedded
endif
ifeq ($(WITH_QRDBD_WEBUI),1)
  BUILD_TAGS += qrdbd_webui
endif
ifeq ($(WITH_CODEC_MSGPACK),1)
  BUILD_TAGS += codec_msgpack
endif
ifeq ($(WITH_CODEC_YAML),1)
  BUILD_TAGS += codec_yaml
endif
ifeq ($(WITH_CODEC_BSON),1)
  BUILD_TAGS += codec_bson
endif
ifeq ($(WITH_CODEC_PROTOBUF),1)
  BUILD_TAGS += codec_protobuf
endif
# Object-storage (go-objstore) cloud backends.
ifeq ($(WITH_AWS_S3),1)
  BUILD_TAGS += awss3
endif
ifeq ($(WITH_MINIO),1)
  BUILD_TAGS += minio
endif
ifeq ($(WITH_GCP_STORAGE),1)
  BUILD_TAGS += gcpstorage
endif
ifeq ($(WITH_AZURE_BLOB),1)
  BUILD_TAGS += azureblob
endif
ifeq ($(WITH_GLACIER),1)
  BUILD_TAGS += glacier
endif
ifeq ($(WITH_AZURE_ARCHIVE),1)
  BUILD_TAGS += azurearchive
endif
DESKTOP_TAGS := desktop production webkit2_41 $(BUILD_TAGS)

# Build tag flags for go commands
TAG_FLAG         := $(if $(strip $(BUILD_TAGS)),-tags "$(strip $(BUILD_TAGS))",)
DESKTOP_TAG_FLAG := -tags "$(strip $(DESKTOP_TAGS))"

# Package paths
PKG_CONFIG         := ./pkg/config/...
PKG_DAO            := ./pkg/dao/...
PKG_SERVICE        := ./pkg/service/...
PKG_STORE          := ./pkg/store/...
PKG_REST           := ./pkg/api/rest/...
PKG_GRPC           := ./pkg/api/grpc/...
PKG_SERVER         := ./pkg/server/...
PKG_CMD            := ./cmd/qrdbd/...
PKG_PROTOCOL       := ./pkg/protocol/...
PKG_MVCC           := ./pkg/mvcc/...
PKG_WATCH          := ./pkg/watch/...
PKG_CONSISTENCY    := ./pkg/consistency/...
PKG_LEASE          := ./pkg/lease/...
PKG_INDEX          := ./pkg/index/...
PKG_TIMESERIES     := ./pkg/timeseries/...
PKG_WRITEMODE      := ./pkg/writemode/...
PKG_BATCH          := ./pkg/batch/...
PKG_CRYPTO_AESGCM  := ./pkg/crypto/aesgcm/...
PKG_CRYPTO_MEM     := ./pkg/crypto/mem/...
PKG_CRYPTO_SHAMIR  := ./pkg/crypto/shamir/...
PKG_STATEMACHINE   := ./pkg/statemachine/...
PKG_SEAL           := ./pkg/seal/...
PKG_SEAL_KDF       := ./pkg/seal/kdf/...
PKG_CEREMONY       := ./pkg/ceremony/...
PKG_INVITE         := ./pkg/invite/...
PKG_CREDENTIALS    := ./pkg/credentials/...
PKG_TENANT         := ./pkg/tenant/...
PKG_KVSTORE        := ./pkg/kvstore/...
PKG_STORAGE        := ./pkg/storage/...
PKG_STORAGE_FILE      := ./pkg/storage/file/...
PKG_STORAGE_PARTITION := ./pkg/storage/partition/...
PKG_SHARD          := ./pkg/shard/...
PKG_CAPABILITIES   := ./pkg/capabilities/...
PKG_PERMISSION     := ./pkg/permission/...
PKG_ROLE           := ./pkg/role/...
PKG_AUTH_RBAC      := ./pkg/auth/rbac/...
PKG_AUTH_MIDDLEWARE := ./pkg/auth/middleware/...
PKG_CERT           := ./pkg/cert/...
PKG_CLI            := ./cmd/qrdb/...
PKG_SQL_PROVIDER   := ./pkg/sql/provider/...
PKG_SQL_ENGINE     := ./pkg/sql/engine/...
PKG_SQL_MYSQL      := ./pkg/sql/mysql/...
PKG_SQL_PGSQL      := ./pkg/sql/pgsql/...
PKG_SQL_QUERY      := ./pkg/sql/query/...
PKG_SQL_CATALOG    := ./pkg/sql/catalog/...
PKG_SQL_PRIVILEGE  := ./pkg/sql/privilege/...
PKG_SQL_PGCATALOG  := ./pkg/sql/pgcatalog/...
PKG_CODEC          := ./pkg/codec/...
PKG_ETCD           := ./pkg/api/etcd/...
PKG_DATABASE       := ./pkg/database/...
PKG_QUIC           := ./pkg/api/quic/...
PKG_MCP            := ./pkg/api/mcp/...

# SDK (separate Go module)
SDK_DIR            := sdk/go

# SDK packages addressed individually (paths are relative to SDK_DIR)
SDK_PKG_QRDBDHARNESS := ./testutil/qrdbdharness/...

# Integration test paths
INT_TEST_DIR  := ./test/integration

# ============================================================================
# Primary targets
# ============================================================================

.PHONY: all
all: build test lint

.PHONY: build
build: build-server build-client

.PHONY: build-server
build-server:
	$(GOBUILD) $(TAG_FLAG) -o bin/$(SERVER_BINARY) ./cmd/qrdbd

# Build server with embedded web UI. Requires frontend to be built first.
.PHONY: build-server-webui
build-server-webui: build-frontend _stage-webui
	WITH_QRDBD_WEBUI=1 $(MAKE) build-server

# Copy frontend dist into cmd/qrdbd/webui_dist/ for go:embed.
.PHONY: _stage-webui
_stage-webui:
	@rm -rf cmd/qrdbd/webui_dist
	@cp -r cmd/qrdb/frontend/dist cmd/qrdbd/webui_dist

.PHONY: build-client
build-client:
	@echo "==> Building client binary in devcontainer..."
	docker compose -f $(COMPOSE_FILE) run --rm --build client-build

.PHONY: _build-client
_build-client: build-frontend
	CGO_ENABLED=1 $(GOBUILD) $(DESKTOP_TAG_FLAG) -o bin/$(CLIENT_BINARY) ./cmd/qrdb

# TypeScript SDK and frontend build targets
.PHONY: install-sdk
install-sdk:
	cd sdk/typescript && npm install

.PHONY: install-frontend
install-frontend: install-sdk
	cd cmd/qrdb/frontend && npm install

.PHONY: build-sdk
build-sdk: install-sdk
	cd sdk/typescript && npm run build

.PHONY: build-frontend
build-frontend: build-sdk install-frontend
	cd cmd/qrdb/frontend && npm run build

.PHONY: dev-gui
dev-gui:
	cd cmd/qrdb && wails dev $(DESKTOP_TAG_FLAG)

.PHONY: clean
clean:
	$(GO) clean ./...
	rm -rf bin/ $(COVER_DIR)/ *.log *.test *.out qrdb qrdbd
	rm -rf sdk/typescript/node_modules sdk/typescript/dist
	rm -rf cmd/qrdb/frontend/node_modules cmd/qrdb/frontend/dist
	rm -rf cmd/qrdb/frontend/test-results cmd/qrdb/frontend/playwright-report

.PHONY: proto
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/api/grpc/proto/qrdbv1/authz.proto \
		pkg/api/grpc/proto/qrdbv1/qrdb.proto

# ============================================================================
# Unit tests
# ============================================================================

.PHONY: test
test:
	$(GOTEST) $(TAG_FLAG) ./pkg/... ./cmd/...

.PHONY: test-stubs
test-stubs:
	$(GOTEST) ./cmd/qrdb/...

.PHONY: test-config
test-config:
	$(GOTEST) $(TAG_FLAG) $(PKG_CONFIG)

.PHONY: test-dao
test-dao:
	$(GOTEST) $(TAG_FLAG) $(PKG_DAO)

.PHONY: test-service
test-service:
	$(GOTEST) $(TAG_FLAG) $(PKG_SERVICE)

.PHONY: test-store
test-store:
	$(GOTEST) $(TAG_FLAG) $(PKG_STORE)

.PHONY: test-rest
test-rest:
	$(GOTEST) $(TAG_FLAG) $(PKG_REST)

.PHONY: test-grpc
test-grpc:
	$(GOTEST) $(TAG_FLAG) $(PKG_GRPC)

.PHONY: test-server
test-server:
	$(GOTEST) $(TAG_FLAG) $(PKG_SERVER)

.PHONY: test-cmd
test-cmd:
	$(GOTEST) $(TAG_FLAG) $(PKG_CMD)

.PHONY: test-protocol
test-protocol:
	$(GOTEST) $(TAG_FLAG) $(PKG_PROTOCOL)

.PHONY: test-mvcc
test-mvcc:
	$(GOTEST) $(TAG_FLAG) $(PKG_MVCC)

.PHONY: test-watch
test-watch:
	$(GOTEST) $(TAG_FLAG) $(PKG_WATCH)

.PHONY: test-consistency
test-consistency:
	$(GOTEST) $(TAG_FLAG) $(PKG_CONSISTENCY)

.PHONY: test-lease
test-lease:
	$(GOTEST) $(TAG_FLAG) $(PKG_LEASE)

.PHONY: test-index
test-index:
	$(GOTEST) $(TAG_FLAG) $(PKG_INDEX)

.PHONY: test-timeseries
test-timeseries:
	$(GOTEST) $(TAG_FLAG) $(PKG_TIMESERIES)

.PHONY: test-writemode
test-writemode:
	$(GOTEST) $(TAG_FLAG) $(PKG_WRITEMODE)

.PHONY: test-batch
test-batch:
	$(GOTEST) $(TAG_FLAG) $(PKG_BATCH)

.PHONY: test-crypto-aesgcm
test-crypto-aesgcm:
	$(GOTEST) $(TAG_FLAG) $(PKG_CRYPTO_AESGCM)

.PHONY: test-crypto-mem
test-crypto-mem:
	$(GOTEST) $(TAG_FLAG) $(PKG_CRYPTO_MEM)

.PHONY: test-crypto-shamir
test-crypto-shamir:
	$(GOTEST) $(TAG_FLAG) $(PKG_CRYPTO_SHAMIR)

.PHONY: test-statemachine
test-statemachine:
	$(GOTEST) $(TAG_FLAG) $(PKG_STATEMACHINE)

.PHONY: test-seal
test-seal:
	$(GOTEST) $(TAG_FLAG) $(PKG_SEAL)

.PHONY: test-seal-kdf
test-seal-kdf:
	$(GOTEST) $(TAG_FLAG) $(PKG_SEAL_KDF)

.PHONY: test-ceremony
test-ceremony:
	$(GOTEST) $(TAG_FLAG) $(PKG_CEREMONY)

.PHONY: test-invite
test-invite:
	$(GOTEST) $(TAG_FLAG) $(PKG_INVITE)

.PHONY: test-custodian
test-custodian:
	$(GOTEST) $(TAG_FLAG) ./pkg/custodian/...

.PHONY: test-auth
test-auth:
	$(GOTEST) $(TAG_FLAG) ./pkg/auth/...

.PHONY: test-credentials
test-credentials:
	$(GOTEST) $(TAG_FLAG) $(PKG_CREDENTIALS)

.PHONY: test-tenant
test-tenant:
	$(GOTEST) $(TAG_FLAG) $(PKG_TENANT)

.PHONY: test-kvstore
test-kvstore:
	$(GOTEST) $(TAG_FLAG) $(PKG_KVSTORE)

.PHONY: test-storage
test-storage:
	$(GOTEST) $(TAG_FLAG) $(PKG_STORAGE)

.PHONY: test-storage-file
test-storage-file:
	$(GOTEST) $(TAG_FLAG) $(PKG_STORAGE_FILE)

.PHONY: test-storage-partition
test-storage-partition:
	$(GOTEST) $(TAG_FLAG) $(PKG_STORAGE_PARTITION)

.PHONY: test-shard
test-shard:
	$(GOTEST) $(TAG_FLAG) $(PKG_SHARD)

.PHONY: test-capabilities
test-capabilities:
	$(GOTEST) $(TAG_FLAG) $(PKG_CAPABILITIES)

.PHONY: test-permission
test-permission:
	$(GOTEST) $(TAG_FLAG) $(PKG_PERMISSION)

.PHONY: test-role
test-role:
	$(GOTEST) $(TAG_FLAG) $(PKG_ROLE)

.PHONY: test-auth-rbac
test-auth-rbac:
	$(GOTEST) $(TAG_FLAG) $(PKG_AUTH_RBAC)

.PHONY: test-auth-middleware
test-auth-middleware:
	$(GOTEST) $(TAG_FLAG) $(PKG_AUTH_MIDDLEWARE)

.PHONY: test-cert
test-cert:
	$(GOTEST) $(TAG_FLAG) $(PKG_CERT)

.PHONY: test-cli
test-cli:
	$(GOTEST) $(TAG_FLAG) $(PKG_CLI)

.PHONY: test-sql-provider
test-sql-provider:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_PROVIDER)

.PHONY: test-sql-engine
test-sql-engine:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_ENGINE)

.PHONY: test-sql-mysql
test-sql-mysql:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_MYSQL)

.PHONY: test-sql-pgsql
test-sql-pgsql:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_PGSQL)

.PHONY: test-sql-query
test-sql-query:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_QUERY)

.PHONY: test-sql-catalog
test-sql-catalog:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_CATALOG)

.PHONY: test-sql-privilege
test-sql-privilege:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_PRIVILEGE)

.PHONY: test-sql-pgcatalog
test-sql-pgcatalog:
	$(GOTEST) $(TAG_FLAG) $(PKG_SQL_PGCATALOG)

.PHONY: test-codec
test-codec:
	$(GOTEST) $(TAG_FLAG) $(PKG_CODEC)

.PHONY: test-etcd
test-etcd:
	$(GOTEST) $(TAG_FLAG) $(PKG_ETCD)

.PHONY: test-database
test-database:
	$(GOTEST) $(TAG_FLAG) $(PKG_DATABASE)

.PHONY: test-quic
test-quic:
	$(GOTEST) $(TAG_FLAG) $(PKG_QUIC)

.PHONY: test-mcp
test-mcp:
	$(GOTEST) $(TAG_FLAG) $(PKG_MCP)

.PHONY: test-sql
test-sql: test-sql-provider test-sql-engine test-sql-mysql test-sql-pgsql test-sql-query test-sql-catalog test-sql-privilege test-sql-pgcatalog

# TAG_FLAG is required, not optional: the SDK's DAO consumer tests construct
# a DAO, which needs a codec, and the codecs are behind build tags — without
# it every such test fails with "codec \"json\" is not supported in this
# build". The root `test` target has always passed it; this one had not.
.PHONY: test-sdk
test-sdk:
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) ./...

# The qrdbd integration-test harness shared by test/integration and the
# GORM conformance module. It is part of
# the SDK module, so test-sdk already runs it via ./... ; this target exists to
# run it alone.
.PHONY: test-testutil-qrdbdharness
test-testutil-qrdbdharness: ## Run unit tests for the shared qrdbd test harness
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) $(SDK_PKG_QRDBDHARNESS)

# ============================================================================
# E2E tests (Playwright – always run in devcontainer)
# ============================================================================

# Three Playwright tiers:
#   - mocked       (playwright.config.ts)        — DOM vs page.route() mocks, no backend
#   - real-backend (playwright.real.config.ts)   — drives REST against a live qrdbd
#   - DOM          (playwright.dom.config.ts)     — drives the real UI (Vite) against a live qrdbd
# The real-backend + DOM tiers spawn a live daemon, so they build qrdbd and
# point QRDBD_BIN at the freshly built binary.
QRDBD_E2E_BIN := $(CURDIR)/bin/$(SERVER_BINARY)

# Public target: build E2E container and run ALL Playwright tiers inside it.
.PHONY: test-e2e
test-e2e:
	@echo "==> Building E2E container..."
	@docker compose -f $(COMPOSE_FILE) build --quiet e2e
	@echo "==> Running E2E tests (mocked + real-backend + DOM)..."
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e

# Internal target: executed inside the devcontainer. Not intended for host use.
# Builds qrdbd (for the real-backend + DOM tiers) and runs all three tiers.
.PHONY: _test-e2e
_test-e2e: build-server build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	@echo "==> [e2e] mocked tier"
	cd cmd/qrdb/frontend && npx playwright test
	@echo "==> [e2e] real-backend tier"
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.real.config.ts
	@echo "==> [e2e] DOM tier"
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.dom.config.ts

# Selective public target: real-backend tier only.
.PHONY: test-e2e-real
test-e2e-real:
	@docker compose -f $(COMPOSE_FILE) build --quiet e2e
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e-real

.PHONY: _test-e2e-real
_test-e2e-real: build-server build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.real.config.ts

# Selective public target: DOM tier only.
.PHONY: test-e2e-dom
test-e2e-dom:
	@docker compose -f $(COMPOSE_FILE) build --quiet e2e
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e-dom

.PHONY: _test-e2e-dom
_test-e2e-dom: build-server build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.dom.config.ts

.PHONY: test-playwright
test-playwright: test-e2e

# ============================================================================
# Integration tests (run in devcontainer with qrdbd server + all protocols)
# ============================================================================

# Compose project name for the integration harness. Everything compose creates
# is namespaced by it, so two runs on one host cannot collide. Without it the
# project name defaults to the compose file's directory, `api`, identical for
# every checkout.
#
# Keyed on hostname rather than a random value, deliberately: stable means the
# `down -v` below still clears a crashed run's leftovers instead of leaking a
# volume set per run, and each CI job container has its own hostname so
# concurrent jobs on one runner are isolated. It does NOT isolate two people on
# the SAME machine — pass INT_PROJECT=... for that.
INT_PROJECT ?= qrdb-int-$(shell hostname | tr -cd '[:alnum:]' | tail -c 16)

# All integration tests run in a docker compose stack with a qrdbd server
# that has ALL protocols enabled (REST, gRPC, QUIC, Unix, MCP).
# This is the single entry point for all integration tests.
INT_COMPOSE := test/integration/api/docker-compose.yml
INT_IMAGE   := go-qrdb-dev:latest

# integration-image builds the image the stack runs, with the code under test
# baked in. It is a separate target because the build needs credentials the
# containers must NOT keep: go.mod pins first-party modules published only to
# the internal Gitea, and a docker build inherits nothing from its caller's
# environment.
#
# The four variables from dependency-resolution.md section 6 go into a file
# mounted as a BuildKit secret for the single `go mod download`. Never a build
# arg -- an arg is recoverable from image metadata with `docker history`. The
# file is written under /tmp, deliberately OUTSIDE the build context, because
# the Dockerfile ends with `COPY . .`; .dockerignore lists gomodauth* as a
# second line of defence. It is removed on the way out, on success or failure.
#
# Two sources, in order: a CI job, where the runner injects the variables into
# the process environment; or a developer box, where the token lives in the
# gitignored .devcontainer/.env. With neither, the file is left empty and the
# build resolves from the public proxy -- which is exactly what happens on
# GitHub, where the Dockerfile's optional mount supplies nothing.
#
# The token is written by shell redirection rather than passed as an argument,
# so it never appears in a command line that `ps` could show.
.PHONY: integration-image
integration-image:
	@set -eu; \
	umask 077; \
	auth="$$(mktemp /tmp/qrdb-gomodauth.XXXXXX)"; \
	trap 'rm -f "$$auth"' EXIT INT TERM; \
	if [ -n "$${GIT_CONFIG_KEY_0:-}" ]; then \
	  { echo "export GOPRIVATE=$${GOPRIVATE:-}"; \
	    echo "export GIT_CONFIG_COUNT=$${GIT_CONFIG_COUNT:-}"; \
	    echo "export GIT_CONFIG_KEY_0=$${GIT_CONFIG_KEY_0}"; \
	    echo "export GIT_CONFIG_VALUE_0=$${GIT_CONFIG_VALUE_0:-}"; } > "$$auth"; \
	  echo "==> module credentials: from the job environment"; \
	elif [ -f .devcontainer/.env ]; then \
	  . ./.devcontainer/.env; \
	  { echo "export GOPRIVATE=github.com/jeremyhahn/*"; \
	    echo "export GIT_CONFIG_COUNT=1"; \
	    echo "export GIT_CONFIG_KEY_0=url.http://x:$${GITEA_GO_TOKEN}@198.51.100.91:3001/jeremyhahn/.insteadOf"; \
	    echo "export GIT_CONFIG_VALUE_0=https://github.com/jeremyhahn/"; } > "$$auth"; \
	  echo "==> module credentials: from .devcontainer/.env"; \
	else \
	  echo "==> no Gitea credentials here; resolving from the public proxy"; \
	fi; \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,src="$$auth" \
	  --target integration \
	  -f .devcontainer/Dockerfile \
	  -t $(INT_IMAGE) .

# integration-test runs BOTH suites in isolated stages so the standalone
# (multi-protocol CLI vs the single-node docker servers) and cluster
# (self-spawned single-node + 3-node clusters) tests never run concurrently
# and never contend for host CPU. Each stage builds, runs, and tears down its
# own stack. Fail-fast: a failing standalone stage stops before cluster.
.PHONY: integration-test
integration-test: integration-test-standalone integration-test-cluster
	@echo "==> All integration stages passed (standalone + cluster)."

# Standalone stage: the multi-protocol CLI suites in ./test/integration/api
# exercised against the single-node software + Shamir docker servers.
.PHONY: integration-test-standalone
integration-test-standalone: integration-image
	@echo "==> Running STANDALONE integration tests (multi-protocol CLI vs single-node servers)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		docker compose -p $(INT_PROJECT) up --abort-on-container-exit integration-tests; \
		rc=$$?; \
		if [ $$rc -ne 0 ]; then $(MAKE) -s _integration-server-logs INT_PROJECT=$(INT_PROJECT); fi; \
		docker compose -p $(INT_PROJECT) down -v; \
		exit $$rc

# --abort-on-container-exit attaches only to the tests container, so a server
# that fails to start reports "dependency failed to start" or "is unhealthy"
# and nothing else -- the reason stays inside a container nobody printed, and
# the teardown below then removes it. Dump the servers while they still exist.
.PHONY: _integration-server-logs
_integration-server-logs:
	@cd test/integration/api && for svc in qrdbd qrdbd-shamir; do \
		echo "==> $$svc log (last 200 lines)"; \
		docker compose -p $(INT_PROJECT) logs --no-color --tail=200 "$$svc" || true; \
	done

# Cluster stage: the self-spawning tests in ./test/integration (single-node +
# real 3-node Raft clusters, leader election, restart/durability, snapshots).
# These start their own qrdbd nodes and do not use the docker servers.
.PHONY: integration-test-cluster
integration-test-cluster: integration-image
	@echo "==> Running CLUSTER integration tests (self-spawned single-node + 3-node clusters)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/' \
		docker compose -p $(INT_PROJECT) up --abort-on-container-exit integration-tests; \
		rc=$$?; \
		docker compose -p $(INT_PROJECT) down -v; \
		exit $$rc

# Backward-compatible alias: the standalone multi-protocol suite.
.PHONY: integration-test-api
integration-test-api: integration-test-standalone

# GPU-accelerated integration tests.
# Auto-detects GPU: uses GPU compose overlay when nvidia-smi is present,
# otherwise falls back to the standard CPU-only test stack.
INT_GPU_COMPOSE := test/integration/api/docker-compose.gpu.yml
HAS_GPU := $(shell command -v nvidia-smi >/dev/null 2>&1 && echo 1 || echo 0)

.PHONY: integration-test-gpu
integration-test-gpu:
	@echo "==> Running GPU integration tests (qrdbd + all protocols + NVIDIA GPU)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cd test/integration/api && \
		docker compose -f docker-compose.yml -f docker-compose.gpu.yml \
			up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		docker compose -f docker-compose.yml -f docker-compose.gpu.yml down -v; \
		exit $$rc

# Auto-detecting variant: uses GPU when nvidia-smi is in PATH, CPU otherwise.
.PHONY: integration-test-auto
integration-test-auto:
ifeq ($(HAS_GPU),1)
	@echo "==> NVIDIA GPU detected — running GPU-accelerated integration tests..."
	@$(MAKE) integration-test-gpu
else
	@echo "==> No NVIDIA GPU detected — falling back to CPU integration tests..."
	@$(MAKE) integration-test
endif

# Run integration tests locally (requires running qrdbd server with all protocols).
.PHONY: integration-test-local
integration-test-local:
	$(GOTEST) -tags integration -timeout=25m ./test/integration/...

# ============================================================================
# Benchmarks (always run in devcontainer)
# ============================================================================

# Public target: build devcontainer image and run fair KV benchmarks inside it.
.PHONY: bench-qrdb-kv
bench-qrdb-kv:
	docker compose -f $(COMPOSE_FILE) run --rm --build -e KV_BENCHTIME=$(KV_BENCHTIME) benchmark make _bench-qrdb-kv-fair

# Internal target: executed inside the devcontainer. Not intended for host use.
.PHONY: _bench-qrdb-kv-fair
_bench-qrdb-kv-fair:
	$(GOTEST) -tags benchmark -bench=. -benchtime=$(KV_BENCHTIME) ./test/performance/qrdb/...

# Run native QUIC KV benchmarks only.
.PHONY: bench-qrdb-kv-quic
bench-qrdb-kv-quic: _kvcompare-build
	cd test/performance && QUICRAFT_ROOT="$(QUICRAFT_ROOT)" ./kvcompare --benchtime=$(KV_BENCHTIME) quic

# Build the kvcompare orchestrator from go-qrdb's own source tree.
.PHONY: _kvcompare-build
_kvcompare-build:
	cd "$(CURDIR)/test/performance/cmd/kvcompare" && $(GO) build -a -o "$(CURDIR)/test/performance/kvcompare" .

# Run QRDB etcd API vs real etcd comparison via kvcompare orchestrator.
.PHONY: bench-etcd-compare
bench-etcd-compare: _kvcompare-build
	cd test/performance && QUICRAFT_ROOT="$(QUICRAFT_ROOT)" ./kvcompare --benchtime=$(KV_BENCHTIME) etcd-compare

# Run QRDB etcd API benchmarks only (no real etcd server needed).
.PHONY: bench-etcd-qrdb
bench-etcd-qrdb: _kvcompare-build
	cd test/performance && QUICRAFT_ROOT="$(QUICRAFT_ROOT)" ./kvcompare --benchtime=$(KV_BENCHTIME) qrdb-etcd

# Run real etcd server benchmarks only.
.PHONY: bench-etcd-server
bench-etcd-server: _kvcompare-build
	cd test/performance && QUICRAFT_ROOT="$(QUICRAFT_ROOT)" ./kvcompare --benchtime=$(KV_BENCHTIME) etcd

# Run perf-docs: full fair comparison with SVG charts and markdown report, then update docs.
.PHONY: perf-docs
perf-docs: _kvcompare-build
	cd test/performance && QUICRAFT_ROOT="$(QUICRAFT_ROOT)" ./kvcompare \
		--benchtime=$(KV_BENCHTIME) --chart-dir charts --report RESULTS.md fair

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
coverage: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/total.out $$($(GO) list ./pkg/... ./cmd/... | grep -v '/proto/')
	$(call check-coverage,$(COVER_DIR)/total.out,total)

.PHONY: coverage-config
coverage-config: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/config.out $(PKG_CONFIG)
	$(call check-coverage,$(COVER_DIR)/config.out,config)

.PHONY: coverage-dao
coverage-dao: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/dao.out $(PKG_DAO)
	$(call check-coverage,$(COVER_DIR)/dao.out,dao)

.PHONY: coverage-service
coverage-service: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/service.out $(PKG_SERVICE)
	$(call check-coverage,$(COVER_DIR)/service.out,service)

.PHONY: coverage-store
coverage-store: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/store.out $(PKG_STORE)
	$(call check-coverage,$(COVER_DIR)/store.out,store)

.PHONY: coverage-rest
coverage-rest: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/rest.out $(PKG_REST)
	$(call check-coverage,$(COVER_DIR)/rest.out,rest)

.PHONY: coverage-grpc
coverage-grpc: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/grpc.out $$($(GO) list $(PKG_GRPC) | grep -v '/proto/')
	$(call check-coverage,$(COVER_DIR)/grpc.out,grpc)

.PHONY: coverage-server
coverage-server: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/server.out $(PKG_SERVER)
	$(call check-coverage,$(COVER_DIR)/server.out,server)

.PHONY: coverage-cmd
coverage-cmd: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/cmd.out $(PKG_CMD)
	$(call check-coverage,$(COVER_DIR)/cmd.out,cmd)

.PHONY: coverage-protocol
coverage-protocol: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/protocol.out $(PKG_PROTOCOL)
	$(call check-coverage,$(COVER_DIR)/protocol.out,protocol)

.PHONY: coverage-mvcc
coverage-mvcc: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/mvcc.out $(PKG_MVCC)
	$(call check-coverage,$(COVER_DIR)/mvcc.out,mvcc)

.PHONY: coverage-watch
coverage-watch: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/watch.out $(PKG_WATCH)
	$(call check-coverage,$(COVER_DIR)/watch.out,watch)

.PHONY: coverage-consistency
coverage-consistency: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/consistency.out $(PKG_CONSISTENCY)
	$(call check-coverage,$(COVER_DIR)/consistency.out,consistency)

.PHONY: coverage-lease
coverage-lease: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/lease.out $(PKG_LEASE)
	$(call check-coverage,$(COVER_DIR)/lease.out,lease)

.PHONY: coverage-index
coverage-index: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/index.out $(PKG_INDEX)
	$(call check-coverage,$(COVER_DIR)/index.out,index)

.PHONY: coverage-timeseries
coverage-timeseries: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/timeseries.out $(PKG_TIMESERIES)
	$(call check-coverage,$(COVER_DIR)/timeseries.out,timeseries)

.PHONY: coverage-writemode
coverage-writemode: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/writemode.out $(PKG_WRITEMODE)
	$(call check-coverage,$(COVER_DIR)/writemode.out,writemode)

.PHONY: coverage-batch
coverage-batch: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/batch.out $(PKG_BATCH)
	$(call check-coverage,$(COVER_DIR)/batch.out,batch)

.PHONY: coverage-crypto-aesgcm
coverage-crypto-aesgcm: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/crypto-aesgcm.out $(PKG_CRYPTO_AESGCM)
	$(call check-coverage,$(COVER_DIR)/crypto-aesgcm.out,crypto-aesgcm)

.PHONY: coverage-crypto-mem
coverage-crypto-mem: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/crypto-mem.out $(PKG_CRYPTO_MEM)
	$(call check-coverage,$(COVER_DIR)/crypto-mem.out,crypto-mem)

.PHONY: coverage-crypto-shamir
coverage-crypto-shamir: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/crypto-shamir.out $(PKG_CRYPTO_SHAMIR)
	$(call check-coverage,$(COVER_DIR)/crypto-shamir.out,crypto-shamir)

.PHONY: coverage-statemachine
coverage-statemachine: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/statemachine.out $(PKG_STATEMACHINE)
	$(call check-coverage,$(COVER_DIR)/statemachine.out,statemachine)

.PHONY: coverage-seal
coverage-seal: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/seal.out $(PKG_SEAL)
	$(call check-coverage,$(COVER_DIR)/seal.out,seal)

.PHONY: coverage-ceremony
coverage-ceremony: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/ceremony.out $(PKG_CEREMONY)
	$(call check-coverage,$(COVER_DIR)/ceremony.out,ceremony)

.PHONY: coverage-credentials
coverage-credentials: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/credentials.out $(PKG_CREDENTIALS)
	$(call check-coverage,$(COVER_DIR)/credentials.out,credentials)

.PHONY: coverage-tenant
coverage-tenant: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/tenant.out $(PKG_TENANT)
	$(call check-coverage,$(COVER_DIR)/tenant.out,tenant)

.PHONY: coverage-kvstore
coverage-kvstore: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/kvstore.out $(PKG_KVSTORE)
	$(call check-coverage,$(COVER_DIR)/kvstore.out,kvstore)

.PHONY: coverage-storage
coverage-storage: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/storage.out $(PKG_STORAGE)
	$(call check-coverage,$(COVER_DIR)/storage.out,storage)

.PHONY: coverage-storage-file
coverage-storage-file: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/storage-file.out $(PKG_STORAGE_FILE)
	$(call check-coverage,$(COVER_DIR)/storage-file.out,storage-file)

.PHONY: coverage-storage-partition
coverage-storage-partition: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/storage-partition.out $(PKG_STORAGE_PARTITION)
	$(call check-coverage,$(COVER_DIR)/storage-partition.out,storage-partition)

.PHONY: coverage-shard
coverage-shard: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/shard.out $(PKG_SHARD)
	$(call check-coverage,$(COVER_DIR)/shard.out,shard)

.PHONY: coverage-capabilities
coverage-capabilities: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/capabilities.out $(PKG_CAPABILITIES)
	$(call check-coverage,$(COVER_DIR)/capabilities.out,capabilities)

.PHONY: coverage-permission
coverage-permission: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/permission.out $(PKG_PERMISSION)
	$(call check-coverage,$(COVER_DIR)/permission.out,permission)

.PHONY: coverage-role
coverage-role: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/role.out $(PKG_ROLE)
	$(call check-coverage,$(COVER_DIR)/role.out,role)

.PHONY: coverage-auth-rbac
coverage-auth-rbac: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/auth-rbac.out $(PKG_AUTH_RBAC)
	$(call check-coverage,$(COVER_DIR)/auth-rbac.out,auth-rbac)

.PHONY: coverage-auth-middleware
coverage-auth-middleware: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/auth-middleware.out $(PKG_AUTH_MIDDLEWARE)
	$(call check-coverage,$(COVER_DIR)/auth-middleware.out,auth-middleware)

.PHONY: coverage-cert
coverage-cert: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/cert.out $(PKG_CERT)
	$(call check-coverage,$(COVER_DIR)/cert.out,cert)

.PHONY: coverage-cli
coverage-cli: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/cli.out $(PKG_CLI)
	$(call check-coverage,$(COVER_DIR)/cli.out,cli)

.PHONY: coverage-sql-provider
coverage-sql-provider: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-provider.out $(PKG_SQL_PROVIDER)
	$(call check-coverage,$(COVER_DIR)/sql-provider.out,sql-provider)

.PHONY: coverage-sql-engine
coverage-sql-engine: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-engine.out $(PKG_SQL_ENGINE)
	$(call check-coverage,$(COVER_DIR)/sql-engine.out,sql-engine)

.PHONY: coverage-sql-mysql
coverage-sql-mysql: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-mysql.out $(PKG_SQL_MYSQL)
	$(call check-coverage,$(COVER_DIR)/sql-mysql.out,sql-mysql)

.PHONY: coverage-sql-pgsql
coverage-sql-pgsql: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-pgsql.out $(PKG_SQL_PGSQL)
	$(call check-coverage,$(COVER_DIR)/sql-pgsql.out,sql-pgsql)

.PHONY: coverage-sql-query
coverage-sql-query: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-query.out $(PKG_SQL_QUERY)
	$(call check-coverage,$(COVER_DIR)/sql-query.out,sql-query)

.PHONY: coverage-sql-catalog
coverage-sql-catalog: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-catalog.out $(PKG_SQL_CATALOG)
	$(call check-coverage,$(COVER_DIR)/sql-catalog.out,sql-catalog)

.PHONY: coverage-sql-privilege
coverage-sql-privilege: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-privilege.out $(PKG_SQL_PRIVILEGE)
	$(call check-coverage,$(COVER_DIR)/sql-privilege.out,sql-privilege)

.PHONY: coverage-sql-pgcatalog
coverage-sql-pgcatalog: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/sql-pgcatalog.out $(PKG_SQL_PGCATALOG)
	$(call check-coverage,$(COVER_DIR)/sql-pgcatalog.out,sql-pgcatalog)

.PHONY: coverage-codec
coverage-codec: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/codec.out $(PKG_CODEC)
	$(call check-coverage,$(COVER_DIR)/codec.out,codec)

.PHONY: coverage-etcd
coverage-etcd: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/etcd.out $(PKG_ETCD)
	$(call check-coverage,$(COVER_DIR)/etcd.out,etcd)

.PHONY: coverage-database
coverage-database: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/database.out $(PKG_DATABASE)
	$(call check-coverage,$(COVER_DIR)/database.out,database)

.PHONY: coverage-quic
coverage-quic: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/quic.out $(PKG_QUIC)
	$(call check-coverage,$(COVER_DIR)/quic.out,quic)

.PHONY: coverage-mcp
coverage-mcp: | _cover-dir
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/mcp.out $(PKG_MCP)
	$(call check-coverage,$(COVER_DIR)/mcp.out,mcp)

.PHONY: coverage-sql
coverage-sql: coverage-sql-provider coverage-sql-engine coverage-sql-mysql coverage-sql-pgsql coverage-sql-query coverage-sql-catalog coverage-sql-privilege coverage-sql-pgcatalog

.PHONY: coverage-sdk
coverage-sdk: | _cover-dir
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) -coverprofile=../../$(COVER_DIR)/sdk.out ./...
	$(call check-coverage,$(COVER_DIR)/sdk.out,sdk)

# Included in coverage-sdk's ./... profile; this target gates the harness
# package on its own so a regression there is attributable.
.PHONY: coverage-testutil-qrdbdharness
coverage-testutil-qrdbdharness: | _cover-dir ## Coverage for the shared qrdbd test harness
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) -coverprofile=../../$(COVER_DIR)/testutil-qrdbdharness.out $(SDK_PKG_QRDBDHARNESS)
	$(call check-coverage,$(COVER_DIR)/testutil-qrdbdharness.out,testutil-qrdbdharness)

_cover-dir:
	@mkdir -p $(COVER_DIR)

# ============================================================================
# CI pipeline (runs in devcontainer with all tools pre-installed)
# ============================================================================

.PHONY: ci
ci:
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer make _ci

.PHONY: _ci
# `security` is the full §8 suite (gosec, govulncheck, go mod verify, trivy fs)
# and replaces the loose `gosec vuln` pair: devops.md §4 requires `make ci` to
# include it.
_ci: fmt vet lint security build-server test
	@echo "CI pipeline passed"

# ============================================================================
# Linting and static analysis
# ============================================================================

# golangci-lint and go vet need the same build tags the rest of the build uses.
# Untagged, whole packages fail to typecheck — pkg/api/mcp's test mocks sit
# behind `codec_json && raft && qrdb_ts && ...` — and a typecheck failure is
# reported as a lint issue, so the gate goes red for a reason that has nothing
# to do with the code. LINT_TAGS is BUILD_TAGS comma-joined, which is the form
# --build-tags takes.
#
# The time budget is NOT passed here. `run.timeout` in .golangci.yml is the one
# place it is declared; a --timeout flag would silently override that file and
# leave two values to keep in sync (#60).
comma      := ,
space      := $(subst ,, )
LINT_TAGS  := $(subst $(space),$(comma),$(strip $(BUILD_TAGS)))

.PHONY: lint
lint:
	$(GOLINT) run --build-tags "$(LINT_TAGS)" ./...

.PHONY: vet
vet:
	$(GOVET) $(TAG_FLAG) ./...

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
# Release hygiene
# ============================================================================

# release-versioning.md P2: the nested sdk/go module is tagged in lockstep with
# the root module. This is the ONLY preventive run — a tag is visible to
# consumers the moment it is pushed, so the Release Lockstep workflow can report
# a skewed release but not stop one (go-qrdb#13 tracks real enforcement). Run it
# before publishing, and publish both tags in one atomic push:
#
#     make check-tag-lockstep && git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z
#
# The rules and the failure modes are documented in the script; its rejection
# branches are covered by test-tag-lockstep, because this repository's own tag
# set is permanently valid and exercises only the accepting branch.
.PHONY: check-tag-lockstep
check-tag-lockstep:
	@./scripts/check-tag-lockstep.sh

.PHONY: test-tag-lockstep
test-tag-lockstep:
	@./scripts/check-tag-lockstep_test.sh

# ============================================================================
# Security scanning
# ============================================================================

.PHONY: gosec
gosec:
	$(GOSEC) -concurrency 4 -exclude=$(GOSEC_EXCLUDE) -exclude-dir=test/performance -severity=medium -confidence=medium ./...

# SARIF for GitHub code scanning. Same exclusions as `gosec` above so the
# dashboard and the gate never disagree; non-blocking because `gosec` is the
# gate.
.PHONY: gosec-sarif
gosec-sarif:
	$(GOSEC) -concurrency 4 -fmt=sarif -out=gosec.sarif -exclude=$(GOSEC_EXCLUDE) -exclude-dir=test/performance -severity=medium -confidence=medium ./... || true

# Blocking. govulncheck is the forcing function for toolchain currency
# (devops.md §9) — swallowing its exit code is what lets a known-vulnerable
# stdlib sit in a release for months, which is exactly the failure mode the
# section exists to prevent.
.PHONY: vuln
vuln:
	$(GOVULNCHECK) ./...
	cd sdk/go && $(GOVULNCHECK) ./...

.PHONY: mod-verify
mod-verify:
	$(GO) mod verify
	cd sdk/go && $(GO) mod verify

# Directories excluded from the filesystem scan, kept identical to the
# skip-dirs list in .github/workflows/ci.yml so `make trivy` and CI agree
# (devops.md §4). test/ holds benchmark harnesses that ship in no release.
# cmd/qrdbd/data/ is the daemon's runtime data directory: gitignored, created
# by running qrdbd locally, and full of the dev CA/node/admin keys it mints on
# first boot. Nothing there can reach history, so scanning it only makes the
# local gate fail for every developer who has ever started the daemon while
# CI -- which scans a fresh clone -- passes.
# Suppressions belong in a .trivyignore with a reason and a date, not here.
TRIVY_SKIP_DIRS := --skip-dirs test,cmd/qrdbd/data

.PHONY: trivy
trivy:
	@if command -v trivy >/dev/null 2>&1; then \
		trivy fs --scanners vuln,secret --severity CRITICAL,HIGH --exit-code 1 $(TRIVY_SKIP_DIRS) . ; \
	else \
		echo "trivy not found - skipping (install: curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sh -s -- -b \$$(go env GOPATH)/bin)"; \
	fi

# The contract CI calls (devops.md §8): gosec, govulncheck, go mod verify and
# trivy fs, in the same form a developer runs locally. trivy carries its own
# `command -v` guard so a developer without it installed still gets the Go half
# of the suite; CI installs the CLI, so nothing is skipped there.
.PHONY: security
security: gosec vuln mod-verify trivy
	@echo "Security suite passed"

# ============================================================================
# Docker
# ============================================================================

.PHONY: docker-build
docker-build:
	docker build -t qrdbd:$(VERSION) .

.PHONY: docker-build-integration
docker-build-integration:
	docker build -t go-qrdb-integration:$(DOCKER_TAG) -f .devcontainer/Dockerfile .

.PHONY: docker-run
docker-run:
	docker run --rm -p 63001:63001 -p 63002:63002 -p 63003:63003 qrdbd:$(VERSION)

.PHONY: docker-run-devcontainer
docker-run-devcontainer:
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer

# ============================================================================
# Dev Cluster (local 3-node cluster for testing)
# ============================================================================

DEV_CLUSTER_DATA := ./data

.PHONY: dev-cluster
dev-cluster: build-server
	@echo "==> Starting 3-node dev cluster..."
	@mkdir -p $(DEV_CLUSTER_DATA)
	@./bin/qrdbd start \
		--discovery static \
		--peers "1=127.0.0.1:63001,2=127.0.0.1:63011,3=127.0.0.1:63021" \
		--replica-id 1 --shard-id 1 \
		--listen 0.0.0.0:63001 --raft-address 127.0.0.1:63001 \
		--admin-port 63002 --grpc-port 63003 \
		--data-dir $(DEV_CLUSTER_DATA)/1 \
		--log-level info &
	@sleep 1
	@./bin/qrdbd start \
		--discovery static \
		--peers "1=127.0.0.1:63001,2=127.0.0.1:63011,3=127.0.0.1:63021" \
		--replica-id 2 --shard-id 1 \
		--listen 0.0.0.0:63011 --raft-address 127.0.0.1:63011 \
		--admin-port 63012 --grpc-port 63013 \
		--data-dir $(DEV_CLUSTER_DATA)/2 \
		--log-level info &
	@sleep 1
	@./bin/qrdbd start \
		--discovery static \
		--peers "1=127.0.0.1:63001,2=127.0.0.1:63011,3=127.0.0.1:63021" \
		--replica-id 3 --shard-id 1 \
		--listen 0.0.0.0:63021 --raft-address 127.0.0.1:63021 \
		--admin-port 63022 --grpc-port 63023 \
		--data-dir $(DEV_CLUSTER_DATA)/3 \
		--log-level info &
	@echo "Dev cluster started (PIDs: $$(pgrep -f 'qrdbd start' | tr '\n' ' '))"
	@echo "  Node 1: admin=:63002 grpc=:63003 raft=:63001"
	@echo "  Node 2: admin=:63012 grpc=:63013 raft=:63011"
	@echo "  Node 3: admin=:63022 grpc=:63023 raft=:63021"

.PHONY: dev-cluster-stop
dev-cluster-stop:
	@echo "==> Stopping dev cluster..."
	@pkill -f 'qrdbd start' 2>/dev/null || true
	@echo "Dev cluster stopped"

.PHONY: dev-cluster-clean
dev-cluster-clean: dev-cluster-stop
	@echo "==> Cleaning dev cluster data..."
	@rm -rf $(DEV_CLUSTER_DATA)/1 $(DEV_CLUSTER_DATA)/2 $(DEV_CLUSTER_DATA)/3
	@echo "Dev cluster data cleaned"



# ============================================================================
# Vector GPU
# ============================================================================

PKG_VECTOR_GPU     := ./pkg/vector/gpu/...
PKG_EMBEDDING      := ./pkg/embedding/...
PKG_RAG            := ./pkg/rag/...
PKG_RAG_CHUNKER    := ./pkg/rag/chunker/...

.PHONY: test-vector-gpu
test-vector-gpu: ## Run GPU stub/detection unit tests
	$(GOTEST) $(TAG_FLAG) $(PKG_VECTOR_GPU)

.PHONY: test-vector-gpu-real
test-vector-gpu-real: ## Run GPU tests against real NVIDIA hardware (requires gpu build tag)
	$(GOTEST) -tags "$(strip $(BUILD_TAGS)) gpu" $(PKG_VECTOR_GPU)

.PHONY: bench-vector-gpu
bench-vector-gpu: ## Run GPU index benchmarks (requires gpu build tag and NVIDIA hardware)
	$(GO) test -tags "$(strip $(BUILD_TAGS)) gpu" -bench=. -benchmem $(PKG_VECTOR_GPU)

.PHONY: coverage-vector-gpu
coverage-vector-gpu: | _cover-dir ## Generate GPU package coverage
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/vector-gpu.out $(PKG_VECTOR_GPU)
	$(call check-coverage,$(COVER_DIR)/vector-gpu.out,vector-gpu)

# ============================================================================
# Embedding
# ============================================================================

.PHONY: test-embedding
test-embedding: ## Run embedding package unit tests
	$(GOTEST) $(TAG_FLAG) $(PKG_EMBEDDING)

.PHONY: bench-embedding
bench-embedding: ## Run embedding package benchmarks
	$(GO) test $(TAG_FLAG) -bench=. -benchmem $(PKG_EMBEDDING)

.PHONY: coverage-embedding
coverage-embedding: | _cover-dir ## Generate embedding package coverage
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/embedding.out $(PKG_EMBEDDING)
	$(call check-coverage,$(COVER_DIR)/embedding.out,embedding)

# ============================================================================
# RAG
# ============================================================================

.PHONY: test-rag
test-rag: ## Run RAG package unit tests
	$(GOTEST) $(TAG_FLAG) $(PKG_RAG)

.PHONY: test-rag-chunker
test-rag-chunker: ## Run RAG chunker unit tests
	$(GOTEST) $(TAG_FLAG) $(PKG_RAG_CHUNKER)

.PHONY: bench-rag
bench-rag: ## Run RAG package benchmarks
	$(GO) test $(TAG_FLAG) -bench=. -benchmem $(PKG_RAG)

.PHONY: bench-rag-chunker
bench-rag-chunker: ## Run RAG chunker benchmarks
	$(GO) test $(TAG_FLAG) -bench=. -benchmem $(PKG_RAG_CHUNKER)

.PHONY: coverage-rag
coverage-rag: | _cover-dir ## Generate RAG package coverage
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/rag.out $(PKG_RAG)
	$(call check-coverage,$(COVER_DIR)/rag.out,rag)

.PHONY: coverage-rag-chunker
coverage-rag-chunker: | _cover-dir ## Generate RAG chunker coverage
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/rag-chunker.out $(PKG_RAG_CHUNKER)
	$(call check-coverage,$(COVER_DIR)/rag-chunker.out,rag-chunker)

# ============================================================================
# Integration test targets for embedding, RAG, and GPU
# ============================================================================
#
# Embedding/RAG e2e tests require the resource-heavy Ollama sidecar, which the
# docker-compose.embedding.yml overlay layers onto the base stack. These are
# kept OUT of `make integration-test` so the standalone and cluster suites run
# fast and light; run them explicitly when verifying embedding behaviour.
# INTEGRATION_RUN scopes `go test -run` to the embedding/RAG suites.

# Stacked compose files for the opt-in Ollama embedding overlay.
EMBEDDING_COMPOSE := -f docker-compose.yml -f docker-compose.embedding.yml

.PHONY: integration-test-embedding
integration-test-embedding: ## Run embedding e2e tests (opt-in Ollama overlay)
	@echo "==> Running embedding integration tests (Ollama overlay)..."
	@cd test/integration/api && docker compose $(EMBEDDING_COMPOSE) down -v 2>/dev/null; true
	@cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		INTEGRATION_RUN='Embedding' \
		docker compose $(EMBEDDING_COMPOSE) up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		docker compose $(EMBEDDING_COMPOSE) down -v; \
		exit $$rc

.PHONY: integration-test-rag
integration-test-rag: ## Run RAG e2e tests (opt-in Ollama overlay)
	@echo "==> Running RAG integration tests (Ollama overlay)..."
	@cd test/integration/api && docker compose $(EMBEDDING_COMPOSE) down -v 2>/dev/null; true
	@cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		INTEGRATION_RUN='RAG' \
		docker compose $(EMBEDDING_COMPOSE) up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		docker compose $(EMBEDDING_COMPOSE) down -v; \
		exit $$rc

# ============================================================================
# Combined new-package targets
# ============================================================================

.PHONY: test-all-new
test-all-new: test-embedding test-rag test-rag-chunker coverage-vector-gpu ## Run all new-package tests and GPU coverage

# ============================================================================
# Vector FLAT Index
# ============================================================================

.PHONY: test-vector-flat
test-vector-flat: ## Run FLAT index unit tests
	$(GOTEST) -tags qrdb_vector -count=1 ./pkg/vector/flat/...

.PHONY: bench-vector-flat
bench-vector-flat: ## Run FLAT index benchmarks
	go test -tags qrdb_vector -bench=. -benchmem -count=3 ./pkg/vector/flat/...

.PHONY: coverage-vector-flat
coverage-vector-flat: ## Generate FLAT index coverage
	go test -tags qrdb_vector -coverprofile=coverage-vector-flat.out -covermode=atomic ./pkg/vector/flat/...
	go tool cover -func=coverage-vector-flat.out


# ============================================================================
# Scalar Index
# ============================================================================

.PHONY: test-vector-scalar-index
test-vector-scalar-index: ## Run scalar index unit tests
	$(GOTEST) $(TAG_FLAG) ./pkg/vector/scalar/...

.PHONY: bench-vector-scalar-index
bench-vector-scalar-index: ## Run scalar index benchmarks
	$(GO) test $(TAG_FLAG) -bench='Benchmark.*' -benchmem -count=3 ./pkg/vector/scalar/...

.PHONY: coverage-vector-scalar-index
coverage-vector-scalar-index: | _cover-dir ## Generate scalar index coverage report
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/vector-scalar-index.out ./pkg/vector/scalar/...
	$(call check-coverage,$(COVER_DIR)/vector-scalar-index.out,vector-scalar-index)

# ============================================================================
# Vector Quantization
# ============================================================================

.PHONY: test-vector-quantization
test-vector-quantization: ## Run quantization unit tests
	$(GOTEST) $(TAG_FLAG) ./pkg/vector/quantization/...

.PHONY: bench-vector-quantization
bench-vector-quantization: ## Run quantization benchmarks
	$(GO) test $(TAG_FLAG) -bench=. -benchmem -count=3 ./pkg/vector/quantization/...

.PHONY: coverage-vector-quantization
coverage-vector-quantization: | _cover-dir ## Generate quantization coverage report
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/vector-quantization.out ./pkg/vector/quantization/...
	$(call check-coverage,$(COVER_DIR)/vector-quantization.out,vector-quantization)

# ============================================================================
# Bloom Filter
# ============================================================================

.PHONY: test-vector-bloom
test-vector-bloom: ## Run bloom filter unit tests
	$(GOTEST) $(TAG_FLAG) ./pkg/vector/bloom/...

.PHONY: bench-vector-bloom
bench-vector-bloom: ## Run bloom filter benchmarks
	$(GO) test $(TAG_FLAG) -bench="Benchmark.*" -benchmem -count=3 ./pkg/vector/bloom/...

.PHONY: coverage-vector-bloom
coverage-vector-bloom: | _cover-dir ## Generate bloom filter coverage report
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/vector-bloom.out ./pkg/vector/bloom/...
	$(call check-coverage,$(COVER_DIR)/vector-bloom.out,vector-bloom)

# ============================================================================
# GORM Conformance Suite
# ============================================================================
# sdk/go/gorm/conformance runs GORM's own upstream test suite against qrdb's
# GORM dialector (sdk/go/gorm) and, over gorm.io/driver/mysql and
# gorm.io/driver/postgres, qrdb's SQL wire protocol servers. It is a nested
# Go module (its go.mod pulls in gorm.io/driver/{mysql,postgres,gaussdb},
# lib/pq, and the upstream test fixtures — none of which belong in sdk/go's
# own dependency graph) with its own build-tag-gated, ratcheted allowlist;
# see sdk/go/gorm/conformance/README.md and GORM_SUITE.lock.
#
# This suite is INFRASTRUCTURE, not a gate: it is expected to fail a large,
# tracked fraction of its tests until qrdb's Migrator/DDL support lands (see
# sdk/go/gorm/migrator.go and pkg/sql/provider). conformance-test-gorm-* is
# therefore never wired into `make test`, `make integration-test`, or `make
# all` — a red run is the expected, tracked state rather than a
# transient one.

GORM_DIR  := sdk/go/gorm/conformance
GORM_TAGS := gormconformance

.PHONY: test-gorm
test-gorm: ## Compile-check the GORM conformance harness (does not run the suite)
	cd $(GORM_DIR) && go vet -tags $(GORM_TAGS) ./...

.PHONY: coverage-gorm
coverage-gorm: | _cover-dir ## Generate coverage for the node-target GORM conformance run (not COVER_MIN-gated: this suite is expected red)
	cd $(GORM_DIR) && GORM_QRDB_TARGET=node go test -tags $(GORM_TAGS) -count=1 -coverprofile=coverage.out ./... ; \
	go tool cover -func=coverage.out
	cp $(GORM_DIR)/coverage.out $(COVER_DIR)/gorm-node.out

.PHONY: gorm-suite-sync
gorm-suite-sync: ## Fetch/verify/materialize the upstream GORM test suite (see GORM_SUITE.lock)
	cd $(GORM_DIR) && go run sync.go

.PHONY: gorm-suite-clean
gorm-suite-clean: ## Remove the materialized upstream GORM test suite
	cd $(GORM_DIR) && go run sync.go -clean

.PHONY: conformance-test-gorm-node
conformance-test-gorm-node: gorm-suite-sync ## Run+reconcile the GORM suite against qrdb's native dialector over a complete in-process node
	cd $(GORM_DIR) && go run runner.go -target=node

.PHONY: conformance-test-gorm-mysql
conformance-test-gorm-mysql: build-server gorm-suite-sync ## Run+reconcile the GORM suite over qrdb's MySQL wire protocol server
	cd $(GORM_DIR) && go run runner.go -target=mysql

.PHONY: conformance-test-gorm-pgsql
conformance-test-gorm-pgsql: build-server gorm-suite-sync ## Run+reconcile the GORM suite over qrdb's PostgreSQL wire protocol server
	cd $(GORM_DIR) && go run runner.go -target=pgsql

.PHONY: conformance-test-gorm
conformance-test-gorm: conformance-test-gorm-node conformance-test-gorm-mysql conformance-test-gorm-pgsql ## Run all three GORM conformance targets

.PHONY: bench-gorm
bench-gorm: gorm-suite-sync ## Run GORM's own upstream benchmarks against the in-process node target
	cd $(GORM_DIR) && GORM_QRDB_TARGET=node go test -tags $(GORM_TAGS) -run '^$$' -bench=. -benchmem -count=1 ./...

# ============================================================================
# Help
# ============================================================================

.PHONY: help
help:
	@echo "go-qrdb Build System"
	@echo ""
	@echo "Primary:"
	@echo "  all                    Build, test, and lint (default)"
	@echo "  build                  Compile server and client binaries"
	@echo "  build-server           Compile daemon binary to bin/qrdbd"
	@echo "  build-client           Compile client binary in devcontainer (CLI + GUI)"
	@echo "  clean                  Remove build artifacts, coverage, and frontend"
	@echo "  proto                  Regenerate protobuf code"
	@echo ""
	@echo "Frontend & SDK:"
	@echo "  install-sdk            Install TypeScript SDK dependencies"
	@echo "  build-sdk              Build TypeScript SDK"
	@echo "  install-frontend       Install frontend dependencies"
	@echo "  build-frontend         Build frontend (Svelte + Vite)"
	@echo "  dev-gui                Start Wails dev server with hot reload"
	@echo ""
	@echo "Feature flags (default: enabled):"
	@echo "  WITH_QRDB_TS=0         Disable time-series support"
	@echo "  WITH_QRDB_VECTOR=0     Disable vector support"
	@echo "  WITH_QRDB_MYSQL=0      Disable MySQL wire protocol support"
	@echo "  WITH_QRDB_PGSQL=0      Disable PostgreSQL wire protocol support"
	@echo "  WITH_QRDB_ETCD=0       Disable etcd wire protocol support"
	@echo "  WITH_QRDB_GPU=1        Enable GPU acceleration (requires NVIDIA)"
	@echo ""
	@echo "Unit Tests:"
	@echo "  test                   Run all unit tests with race detector"
	@echo "  test-<pkg>             Run tests for a specific package"
	@echo "    packages: config, dao, service, store, rest, grpc, quic, mcp, server,"
	@echo "              cmd, database, protocol, mvcc, watch, consistency, lease, index,"
	@echo "              timeseries, writemode, batch, crypto-aesgcm, crypto-mem,"
	@echo "              crypto-shamir, statemachine, seal, tenant, kvstore, storage,"
	@echo "              storage-file, shard, sdk, embedding, rag, rag-chunker,"
	@echo "              vector-gpu, vector-gpu-real"
	@echo ""
	@echo "E2E Tests (Playwright):"
	@echo "  test-e2e               Install Playwright browsers and run all E2E tests"
	@echo "  test-playwright        Alias for test-e2e"
	@echo ""
	@echo "Integration Tests (devcontainer with qrdbd + all protocols):"
	@echo "  integration-test           Run ALL integration tests (docker compose)"
	@echo "  integration-test-api       Alias for integration-test"
	@echo "  integration-test-gpu       Run GPU-accelerated integration tests"
	@echo "  integration-test-embedding Run embedding integration tests"
	@echo "  integration-test-rag       Run RAG integration tests"
	@echo "  integration-test-auto      Auto-detect GPU, fallback to CPU"
	@echo "  integration-test-local     Run integration tests locally (requires running qrdbd)"
	@echo ""
	@echo "Benchmarks (run in devcontainer automatically):"
	@echo "  bench-qrdb-kv         Run fair KV benchmarks (KV_BENCHTIME=5s)"
	@echo "  bench-qrdb-kv-quic    Run native QUIC KV benchmarks"
	@echo "  bench-etcd-compare    Run QRDB etcd API vs real etcd comparison"
	@echo "  bench-etcd-qrdb       Run QRDB etcd API benchmarks only"
	@echo "  bench-etcd-server     Run real etcd server benchmarks only"
	@echo "  perf-docs             Run full fair comparison, generate charts + RESULTS.md"
	@echo ""
	@echo "Coverage (minimum $(COVER_MIN)%):"
	@echo "  coverage               Run total coverage report"
	@echo "  coverage-<pkg>         Run coverage for a specific package"
	@echo "    packages: config, dao, service, store, rest, grpc, quic, mcp, server,"
	@echo "              cmd, database, protocol, mvcc, watch, consistency, lease, index,"
	@echo "              timeseries, writemode, batch, crypto-aesgcm, crypto-mem,"
	@echo "              crypto-shamir, statemachine, seal, tenant, kvstore, storage,"
	@echo "              storage-file, shard, sdk, embedding, rag, rag-chunker, vector-gpu"
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
	@echo "Dev Cluster:"
	@echo "  dev-cluster              Start a local 3-node cluster"
	@echo "  dev-cluster-stop         Stop the local dev cluster"
	@echo "  dev-cluster-clean        Stop and remove dev cluster data"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build                 Build production image"
	@echo "  docker-build-integration     Build integration test image"
	@echo "  docker-run                   Run production container"
	@echo "  docker-run-devcontainer      Run devcontainer shell"
	@echo ""
	@echo "GORM Conformance (separate module at sdk/go/gorm/conformance; RED by design, see README):"
	@echo "  gorm-suite-sync                  Fetch/verify/materialize the upstream GORM test suite"
	@echo "  gorm-suite-clean                 Remove the materialized upstream GORM test suite"
	@echo "  test-gorm                        Compile-check the harness (go vet -tags gormconformance)"
	@echo "  conformance-test-gorm-node       Run+reconcile against an in-process qrdb node"
	@echo "  conformance-test-gorm-mysql      Run+reconcile over qrdb's MySQL wire protocol server"
	@echo "  conformance-test-gorm-pgsql      Run+reconcile over qrdb's PostgreSQL wire protocol server"
	@echo "  conformance-test-gorm            Run all three GORM conformance targets"
	@echo "  bench-gorm                       Run GORM's own upstream benchmarks (node target)"
	@echo "  coverage-gorm                    Generate coverage for the node-target run"

SUBMODULE_TAGS := sdk/go
# Builds never resolve through a workspace: modules come from Gitea tags
# (dependency-resolution.md §2). Immunizes against stray go.work files.
export GOWORK=off

# ---- dependency-resolution.md targets (2026-08-13 cutover) ----
# SUBMODULE_TAGS lists nested modules tagged in lockstep with the root
# (release-versioning.md P2). deps-latest runs before compiling in a
# consumer repo; release-alpha publishes this repo's next -alpha tag to dev.
# deps-latest picks the highest tag INCLUDING -alpha prereleases — bare
# 'go get @latest' ignores prereleases and would downgrade to legacy v0.0.0.

.PHONY: release-alpha deps-latest

release-alpha:
	@V=$$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || echo v0.0.0-alpha); \
	NEXT=$$(echo $$V | awk -F'[.-]' '{printf "v%s.%s.%d-alpha", substr($$1,2), $$2, $$3+1}'); \
	echo $${NEXT#v} > VERSION && git add VERSION && git commit -q -m "chore(release): $$NEXT" || true; \
	git tag $$NEXT; \
	for s in $(SUBMODULE_TAGS); do git tag "$$s/$$NEXT"; done; \
	git push dev HEAD --tags && echo "Tagged $$NEXT"

deps-latest:
	@GOWORK=off sh -c ' \
	SELF=$$(go list -m); \
	for m in $$(go list -m -f "{{if not .Indirect}}{{.Path}}{{end}}" all 2>/dev/null | grep "^github.com/jeremyhahn/" | grep -vx "$$SELF"); do \
	  case "$$(go list -m -f "{{if .Replace}}{{.Replace.Path}}{{end}}" "$$m" 2>/dev/null)" in .*|/*) continue;; esac; \
	  V=$$(go list -m -versions "$$m" 2>/dev/null | tr " " "\n" | tail -1); \
	  if [ -n "$$V" ] && [ "$$V" != "$$m" ]; then go get "$$m@$$V" || exit 1; fi; \
	done; go mod tidy'
