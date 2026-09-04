# go-qrdb Build System
# Distributed key-value store powered by multi-group Raft with QUIC transport

.DEFAULT_GOAL := build

# Project metadata
MODULE        := github.com/jeremyhahn/go-qrdb
SERVER_BINARY := qrdbd
CLIENT_BINARY := qrdb
COVER_DIR     := coverage
# BUILD_VERSION, not VERSION: `make release-alpha VERSION=vX.Y.Z-alpha` takes the
# release version as an ARGUMENT, and a file-scope `VERSION` shadows it. A plain
# `make release-alpha` would then inherit this value, sail past the "state the
# version explicitly" guard with an unprefixed string, and die several steps
# later on a grammar error naming a version the operator never typed. The build
# stamp and the release argument are different facts; only one may be called
# VERSION, and the release recipe is the one whose name is part of its contract.
BUILD_VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.1-alpha")
GIT_COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS       := -X main.version=$(BUILD_VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

# Go tooling
GO            := go
# GOTEST_TIMEOUT is the PER-PACKAGE budget go test enforces, not a whole-suite
# one, and it is a runaway guard rather than a correctness gate: the CI job
# allows 90 minutes and the integration target sets its own 25m.
#
# 30m default, because go test defaults to 10m and this suite does not fit.
# pkg/seal alone measured 589.8s on a PASSING run -- 98% of the 10m default,
# under -race with Argon2id KDF work that is meant to be slow. Any load on a
# shared runner tips it over, and the whole job then dies as
# "panic: test timed out after 10m0s" naming whichever package was unlucky,
# which reads like a hang in that package rather than a budget that was always
# too small. Raising it removes a false red; it does not weaken any assertion.
#
# It is a variable because the FIPS leg needs a bigger one and nothing else
# does (go-qrdb#147). Measured on one 24-core box, pkg/seal under -race:
#
#            GODEBUG=fips140=only     default      factor
#   package        319.3s              121.9s       2.6x
#
# The KDF is the reason: pkg/seal/kdf selects PBKDF2-HMAC-SHA256 at 600,000
# iterations in FIPS mode and Argon2id otherwise, and the barrier tests
# re-derive on every seal/unseal/change-password. On a CI runner the same
# package takes 516.1s WITHOUT fips (4.2x this box), so the FIPS leg lands at
# roughly 1350s against a 1800s budget -- and on 2026-08-26, with two thirds
# of the runner fleet offline, it went past it and the package died at 30m
# having passed 17,787 of its tests. A margin that thin is not a budget.
#
# Do NOT raise the default to cover the FIPS leg. A 30m package that hangs on
# the push path should surface in 30m.
GOTEST_TIMEOUT ?= 30m
GOTEST        := $(GO) test -race -count=1 -v -timeout=$(GOTEST_TIMEOUT)
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
#   G703: path-traversal taint analysis. The single site is
#         pkg/system/openssl.go:106, and all three legs of the justification
#         were checked against that code path before excluding (#154):
#         (1) it is the taint analogue of G304, already excluded above for
#             this class, alongside its siblings G704 and G705;
#         (2) the taint source is os.Getenv("OPENSSL_CONF") -- the documented
#             OpenSSL operator mechanism. It is never a request parameter, so
#             setting it requires control of the process environment, which
#             already implies code execution as that user. This holds even
#             though LoadOpenSSLConfig is re-exported through
#             sdk/go/types_system.go: a downstream caller still reads ITS OWN
#             process environment, not caller-supplied input;
#         (3) the path is only os.Stat'd here and os.Open'd read-only in
#             parseOpenSSLConfig, then scanned. pkg/system/openssl.go contains
#             no Create/WriteFile/OpenFile/Remove/Rename/Chmod/Mkdir and no
#             exec.Command -- verified, not assumed.
#         If any of those three stops being true, this entry is wrong and the
#         finding is real: re-arm the rule rather than widening the exclusion.
GOSEC_EXCLUDE := G103,G104,G115,G118,G122,G304,G703,G704,G705

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

# REQUIRED_TAGS are the tags no WITH_* flag can turn off, so EVERY
# configuration this Makefile can produce contains them. They are named here
# rather than spelled out again in SUPPORTED_TAG_SETS because they are that
# declaration's `minimal` rung, and a floor with two definitions is a floor
# that drifts (#263).
REQUIRED_TAGS := codec_json raft

# Compute build tags from feature flags
BUILD_TAGS := $(REQUIRED_TAGS)
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
#
# check-publication-bound AND its self-test are both here. `make ci` is a name
# that claims a property -- "this is what CI runs" -- and it did not hold it:
# a developer could run the advertised entrypoint, get green, and be blocked by
# the pull-request job. Both steps run in the Lint job, so both run here.
#
# The self-test is included rather than left CI-only (which is where
# test-require-tool and test-tag-lockstep sit) because this guard is the one
# whose own defects are invisible by construction: it fails by staying silent,
# and silence is what success looks like. A second of shell is a cheap way to
# be told the guard still refuses what it claims to refuse.
_ci: fmt vet lint check-publication-bound test-publication-bound security build-server test
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
# The same join applied to the mandatory floor. Used by SUPPORTED_TAG_SETS.
MIN_TAGS   := $(subst $(space),$(comma),$(strip $(REQUIRED_TAGS)))

.PHONY: lint
lint:
	$(GOLINT) run --build-tags "$(LINT_TAGS)" ./...

# scripts/require-tool.sh is what stands between this repository and a lint or
# scan performed by an unknown analyser version (#179, #195). The pins in
# ci.yml match the runner image, so a healthy run exercises only its ACCEPTING
# branch — every rejecting branch, which is the whole reason it exists, would
# otherwise ship untested. Same reasoning as test-tag-lockstep below.
#
# `--mutate` re-loosens the version comparison to the substring test that fails
# open (a v1.7.0 pin accepting v11.7.0) and asserts the near-miss cases go red.
# A suite that passes both with and without the check proves nothing.
.PHONY: test-require-tool
test-require-tool:
	@./scripts/require-tool_test.sh

# AGENTS.md: a feature folder is publication-bound -- it ships to GitHub when
# the repo publishes -- so it must never carry an internal address, host name,
# workstation path, agent principal name or internal issue reference.
#
# Until this target existed the rule's only enforcement was a reviewer
# noticing, and the leak rate says what that was worth: five violations landed
# in one afternoon from an author who had read AGENTS.md, and the review caught
# two. That is not the reviewer's failing. A reviewer sees ONE DIFF, while the
# violation is a property of the TREE -- the same shape as vet-tags above, and
# the same reason a whole-repo scanner is the only thing that can see it.
#
# The failure is silent until it is public: nothing breaks, the docs read fine
# internally, and it surfaces on the publishing push, where a reader hits an
# internal issue number and finds nothing, or an unrelated public issue wearing
# it.
#
# Six rules: an internal issue reference, an internal issue LINK (the
# forge-relative `](../../issues/62)` form, where the reference is in the link
# target and no `repo#123` pattern can see it), an RFC 1918 address, an
# internal host name, a workstation path and an agent principal. Scope and
# pattern choices, including what each rule deliberately does NOT match and
# why, are documented at the top of the script.
.PHONY: check-publication-bound
check-publication-bound:
	@./scripts/check-publication-bound.sh

# The guard's own rejecting branches. This repository's feature folders are
# clean, so running the check here exercises only its ACCEPTING branch and
# every rejecting branch would otherwise ship untested -- same reasoning as
# test-require-tool and test-tag-lockstep.
#
# `--mutate` re-loosens internal-issue-ref to the naive `#[0-9]+` and asserts
# the near-miss cases go red: `PKCS#11`, a markdown anchor
# (`api.md#403-and-405-...`), `#1` as a back-reference to a numbered list, and
# a `#42` inside a fenced shell comment. All four are real text in this tree.
# A suite that passes both with and without the narrowing proves nothing.
.PHONY: test-publication-bound
test-publication-bound:
	@./scripts/check-publication-bound_test.sh

.PHONY: vet
vet:
	$(GOVET) $(TAG_FLAG) ./...

# ----------------------------------------------------------------------------
# THE SUPPORTED BUILD-TAG COMBINATIONS
# ----------------------------------------------------------------------------
#
# SUPPORTED_TAG_SETS below is THE declaration, and there is no other. Every
# consumer of it -- `make vet-tags`, scripts/vet-modules.sh, `make
# print-tag-sets` -- reads this variable rather than restating its contents.
#
# WHY A DECLARATION AT ALL (#263, owner-ruled 2026-09-03). A build constraint
# is a CLAIM that the file compiles under the tags it names, and for a build
# constraint compilation IS the check. Before this, the sweep ran exactly two
# combinations and BOTH contained every default tag, so every constraint naming
# a NARROWER set was unfalsifiable by construction -- the whole space between
# two tags and thirteen went unexercised. That is not a hypothetical gap: #267
# fixed a file declaring `codec_json && raft` that did not compile under only
# those tags, because it called a helper constrained to seven, and no leg of
# the gate could see it. The ruling was that an undeclared set is worse than
# either answer, so the set is written down here and `vet-tags` exercises
# exactly it.
#
# The sweep itself predates the declaration, and its own motivating measurement
# still stands: `go vet ./...` is scoped to the tag set it is given, so a
# package clean under one combination says NOTHING about the others.
# pkg/statemachine failed to typecheck under three of four combinations for
# weeks, on two undefined symbols, invisible because the only combination
# anyone ran was the fourth (#178). #263 is the same blindness one level up --
# not "which packages does the sweep reach" but "which combinations does it
# ask about".
#
# THE RUNGS, and why exactly these:
#
#   minimal              REQUIRED_TAGS -- the tags no WITH_* flag turns off, so
#                        the FLOOR of the configuration space and what
#                        `make build WITH_QRDB_TS=0 WITH_QRDB_VECTOR=0 ...`
#                        produces. It is also the EXACT constraint 15 files in
#                        this tree declare -- `//go:build codec_json && raft`
#                        and nothing more -- and this rung is the only thing
#                        that makes those 15 declarations checkable.
#   minimal+integration  the same floor with the integration suites compiled in.
#                        Not decoration: 102 files under test/ declare
#                        `//go:build integration` and NOTHING else, so this rung
#                        is the closest checkable approximation of the claim
#                        they actually make. Before it, every one of them was
#                        only ever compiled with all thirteen default tags
#                        present.
#   default              BUILD_TAGS as the WITH_* defaults compute it: what
#                        `make build`, `make test` and `make lint` use.
#   default+integration  what `make integration-test` runs.
#
# AND EVERY COMBINATION BETWEEN THEM IS DECLARED UNSUPPORTED. The WITH_* flags
# are individually settable, so the space they span is thousands of
# combinations; enumerating it is a combinatorial problem, not a budget one.
# Two ENDS crossed with the integration axis is the bounded claim this project
# is willing to keep true, and the consequence is stated rather than left to be
# discovered: a file constrained `codec_json && raft && qrdb_ts` is compiled
# only by the `default` rungs, so its narrower claim is NOT checked. That is a
# declaration, not an oversight. The honest reading of a green sweep is "every
# DECLARED combination typechecks", never "every combination does".
#
# AND WITHIN A RUNG, ONLY THE FILES THAT RUNG COMPILES ARE CHECKED. A rung does
# not verify "the minimal configuration"; it verifies the files that configuration
# admits. A test file whose own tag is outside the rung is not typechecked under
# it AND does not fail it -- it is absent, which is silence, not a check. That is
# the compile-time twin of `go test` printing `ok ... [no tests to run]` when a
# tag-short invocation matches nothing, and it is why the sweep now prints a
# PACKAGE COUNT beside every rung and fails closed on zero: a rung that checks
# nothing is the failure that looks most like success.
#
# Measured 2026-09-03 so the size of the gap is known rather than assumed. The
# `minimal` rung matches 121 of the root module's packages against `default`'s
# 129, and 1,174 test files against 1,361 -- substantive, not vacuous. Exactly
# ONE package sits in `minimal` while contributing zero test files to it:
# pkg/vector/hnsw, whose 19 test files are all behind qrdb_vector. Eight more
# packages (pkg/embedding, pkg/rag, pkg/rag/chunker, pkg/sql/mysql,
# pkg/sql/pgsql, pkg/vector/bloom, pkg/vector/quantization, pkg/vector/scalar)
# are outside `minimal` entirely. Those nine are covered by the `default` rungs
# and by nothing narrower, and that is the honest extent of the claim.
#
# OFF-BY-DEFAULT FEATURE TAGS ARE NOT RUNGS EITHER, AND ONE OF THEM IS ALREADY
# BROKEN. qrdb_fts, gpu, qrdbd_webui and desktop are WITH_*=0, so they appear in
# NEITHER rung and nothing in this repository compiles them -- not `make test`,
# not `make lint`, not `make vet`. Measured 2026-09-03 while choosing these
# rungs: `go vet -tags "codec_json,raft,qrdb_fts" ./pkg/fts/...` FAILS, because
# a test double stopped satisfying storage.Batch when Batch gained an embedded
# Reader, and no configuration anyone runs has compiled that package since
# (#279). That is the argument FOR declaring a qrdb_fts rung eventually and the
# argument AGAINST declaring one in the change that introduces the declaration:
# a rung red on arrival teaches people to ignore this target, which is how the
# blind spot got here. Add the rung in the change that fixes #279. gpu needs
# CUDA and desktop needs webkit2, so neither belongs in a target every developer
# is told to run before pushing.
#
# UNTAGGED IS DELIBERATELY NOT A RUNG. Whole packages legitimately fail to
# typecheck with no tags -- pkg/api/mcp's test mocks sit behind
# `codec_json && raft && qrdb_ts && ...` -- which is the same reason LINT_TAGS
# exists (see the comment above `lint`). A no-tag load is a different tool for
# a different question (#271), and its results are not comparable with these.
#
# ADDING OR REMOVING A RUNG IS A POLICY CHANGE WITH A RUNTIME PRICE, and both
# belong in the commit that makes it.
#
# The structural half of the price is exact and load-independent: this change
# took the tagged modules from 2 combinations to 4, and the whole-tree sweep
# from 6 `go vet` invocations to 10.
#
# THE WALL-CLOCK HALF IS DELIBERATELY NOT RECORDED HERE. Every window available
# while this landed had four or five concurrent `-race` suites on the host
# (load average 98-160 on 24 cores, CPU PSI 88 at the worst of it), and a
# before/after wall clock taken under that measures the CONTENTION, not the
# change -- in the direction that makes this gate look expensive. A number
# nobody can reproduce is worse than an admitted gap, because it gets quoted.
# The one figure worth knowing is the shape of the measurement: run BOTH sweeps
# back to back on the same cache in the same quiet window, and read the ratio,
# never an absolute second count copied out of a comment.
#
# The alternative that was priced and rejected: a per-feature ladder, `minimal`
# plus each optional tag on its own, which is what would make the in-between
# constraints TRUE rather than merely undeclared. Twelve more rungs -- an order
# of magnitude more work than the four here, on any host. The stronger claim is
# available and this declaration deliberately does not buy it.
#
# NEVER MERGE RESULTS FROM TWO RUNGS INTO ONE LIST. A symbol that exists only
# under a tag is ABSENT from a build without it, and absence is silence, not a
# finding -- so a union of runs hides exactly what the extra rungs were added
# to expose. The sweep prints the tag set beside every invocation and beside
# every failure for that reason; keep it that way.
SUPPORTED_TAG_SETS := $(MIN_TAGS) $(MIN_TAGS),integration $(LINT_TAGS) $(LINT_TAGS),integration

# vet-tags sweeps EVERY Go module in the tree, each with its own tag sets.
#
# The logic lives in scripts/vet-modules.sh, not here, and that is the point:
# as an inline recipe this target was wrong SIX times, always in the same
# direction -- narrower than its own claim, and silent about it. A guard fails
# by staying SILENT, so none of those was visible without deliberately breaking
# something, and an inline recipe has nowhere to put the test. The script's
# header lists all six; scripts/vet-modules_test.sh drives every rejecting
# branch and has a --mutate mode, exactly as require-tool.sh does.
#
# STILL NOT WIRED INTO CI, and #263 does not change that. The blocker has moved
# twice and is now a MODULE, not a tag set. #178 could not arm it because the
# `+integration` leg was red repo-wide; #203 cleared that. What blocks it now is
# `sdk/go/gorm/conformance`, one of the seven Go modules this target sweeps:
#
#     cd sdk/go/gorm/conformance && go vet -tags gormconformance ./...
#     go: updates to go.mod needed; to update it:
#             go mod tidy
#
# Pre-existing on `main` -- unnoticed precisely because nothing vetted that
# module until #221 made it visible -- and tracked in #233, alongside the rest
# of that module's work in #50 (lockstep tag namespace, unresolved
# `replace ... => ../../../..`). Arming before it is tidy makes a REQUIRED job
# red on `main` and blocks every PR behind it. Verify against the MERGED tree
# when arming, re-fetching first: #203's own merged-tree check went stale within
# the day. Run this by hand meanwhile.
#
# Do NOT add an exclusion list or a baseline file to get it armed sooner, and
# specifically do not skip the conformance module. The whole value of this
# target is that a green result means the tree typechecks under every DECLARED
# combination; a baseline would make it mean "except where it does not", which
# is what `make vet` alone already said.
.PHONY: vet-tags
vet-tags:
	@GOVET="$(GOVET)" GOLIST="$(GO) list" ./scripts/vet-modules.sh --vet "$(SUPPORTED_TAG_SETS)" "$(GORM_TAGS)"

# print-tag-sets prints the declared combinations, one per line, so that
# anything needing them -- a CI matrix, a doc, a one-off sweep -- READS the
# declaration instead of restating it. A second copy of this list is the defect
# #263 exists to close.
.PHONY: print-tag-sets
print-tag-sets:
	@printf '%s\n' $(SUPPORTED_TAG_SETS)

# The guard's own regression suite. Same reasoning as test-require-tool: a
# healthy sweep only exercises the accepting path, so every rejecting branch --
# the entire reason the script exists -- would otherwise ship untested.
.PHONY: test-vet-modules
test-vet-modules:
	@./scripts/vet-modules_test.sh

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

# release-versioning.md §2: VERSION, the newest CHANGELOG release heading and
# the git tag are three statements of one fact, and until now nothing compared
# them. The drift is not hypothetical — VERSION sat on 0.5.7-alpha through both
# the v0.5.8-alpha and v0.5.9-alpha tags while their notes stayed under
# [Unreleased] (go-qrdb#25).
#
# RELEASE_TAG is optional and only meaningful when publishing. Without it the
# tag half is skipped, loudly:
#
#     make check-version-reconciled
#     make check-version-reconciled RELEASE_TAG=v0.11.0-alpha
#
# Run both hygiene checks before publishing a release:
#
#     make check-tag-lockstep check-version-reconciled RELEASE_TAG=vX.Y.Z
# A TAG NAME IS UNTRUSTED INPUT. `release-lockstep.yml` passes github.ref_name
# straight in, and since this PR widened that workflow's trigger to `sdk/**/v*`
# it routes arbitrary DEEPER tag names here. Git accepts far more in a tag name
# than the version grammar this target reasons about, and the gap between what
# git accepts and what we audit was the vulnerability:
#
#   make check-version-reconciled RELEASE_TAG='sdk/go/.../v0.10.0-alpha;echo>X'
#
# expanded $(RELEASE_TAG) into recipe TEXT, so the shell split the command, ran
# the injected `echo`, and make exited 0 -- the step reported SUCCESS having run
# attacker-controlled shell. Measured, not theorised.
#
# BOTH halves, because either alone is insufficient in a different way. Quoting
# through the same $(value)/unexport/override sanitiser the release inputs use
# makes an injected value INERT -- the shell receives exactly one argument
# whatever it contains. The grammar check makes it REJECTED -- a tag that cannot
# be a release is not a tag this target should be reasoning about at all.
# Quoting closes this instance; the grammar closes the class, and closing the
# instance is not enough.
#
# `:=` not `?=`, so an exported RELEASE_TAG in the environment cannot decide
# what a CI step audits; the command line still can, which is how the workflow
# passes it.
RELEASE_TAG :=

unexport RELEASE_TAG REL_RELEASE_TAG

.PHONY: check-version-reconciled
check-version-reconciled: override export REL_RELEASE_TAG := $(value RELEASE_TAG)
check-version-reconciled:
	@num='(0|[1-9][0-9]*)'; \
	if [ -n "$$REL_RELEASE_TAG" ] && \
	   ! printf '%s' "$$REL_RELEASE_TAG" | grep -Eq "^([A-Za-z0-9][A-Za-z0-9._-]*/)*v$${num}\.$${num}\.$${num}(-(alpha|beta|rc))?$$"; then \
	  echo "ERROR: RELEASE_TAG is not a release tag: $$REL_RELEASE_TAG"; \
	  echo "  expected [<module-path>/]vX.Y.Z with an optional -alpha/-beta/-rc"; \
	  echo "  (release-versioning.md 1). A tag that cannot name a release is not"; \
	  echo "  one this check should reason about, and git accepts far more in a"; \
	  echo "  tag name than a shell treats as inert."; \
	  exit 1; \
	fi; \
	./scripts/check-version-reconciled.sh . "$$REL_RELEASE_TAG"

.PHONY: test-version-reconciled
test-version-reconciled:
	@./scripts/check-version-reconciled_test.sh

# release-alpha is orchestration whose failure mode is a PUBLISHED tag, and a
# published tag is immutable (release-versioning.md 3) — so it is the one target
# in this Makefile where "I ran it once and it worked" is not enough. The cases
# cover what a regression would actually cost: a guard that writes VERSION
# before it refuses, a half-created tag pair, the signing wrapper invoked with
# the wrong arguments, and a push that publishes refs nobody asked for.
#
# Hermetic: throwaway repositories, a local bare repo standing in for `dev`, and
# a fake AGENT_GIT wrapper with a throwaway signing key. No network, no Gitea,
# no dependence on the caller having a `dev` remote or a signing identity — a
# test that only passes on one host is a test that stops running.
.PHONY: test-release-alpha
test-release-alpha:
	@./scripts/check-release-alpha_test.sh

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
	docker build -t qrdbd:$(BUILD_VERSION) .

.PHONY: docker-build-integration
docker-build-integration:
	docker build -t go-qrdb-integration:$(DOCKER_TAG) -f .devcontainer/Dockerfile .

.PHONY: docker-run
docker-run:
	docker run --rm -p 63001:63001 -p 63002:63002 -p 63003:63003 qrdbd:$(BUILD_VERSION)

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
	@echo "  vet-tags               Run go vet over every DECLARED build-tag combination"
	@echo "  print-tag-sets         Print the declared build-tag combinations, one per line"
	@echo "  fmt                    Check gofmt formatting"
	@echo ""
	@echo "Release (see the release-alpha comment block for the full contract):"
	@echo "  release-alpha VERSION=vX.Y.Z-alpha   Cut and publish a signed release"
	@echo "                         Fetches tags from the release remote first (adds"
	@echo "                         refs, prunes nothing), then commits VERSION, cuts"
	@echo "                         signed tags for the root and every sdk/ module,"
	@echo "                         and pushes exactly those refs atomically."
	@echo "  check-tag-lockstep     Audit root and nested-module tags for lockstep"
	@echo "  check-version-reconciled [RELEASE_TAG=vX.Y.Z-alpha]"
	@echo "                         Reconcile VERSION, CHANGELOG and tag"
	@echo "  test-release-alpha     Regression tests for the release-alpha recipe"
	@echo "  test-tag-lockstep      Regression tests for the lockstep check"
	@echo "  test-version-reconciled  Regression tests for the reconciliation check"
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

# DERIVED, not declared, and `override` so nothing can reach it.
#
# Two problems, one answer. The declared list had already DRIFTED: go-qrdb#50
# was owner-ruled on 2026-08-20 to make the conformance module a third lockstep
# tag namespace, `sdk/go/gorm/conformance/go.mod` exists on main, and this line
# still said `sdk/go` alone. A hand-maintained list drifts from the module graph
# and the drift stays invisible until it strands something -- the same reasoning
# sdlc.md 8 records for deriving dispatch priority instead of listing it.
# Discovery makes #50's release-tooling half fall out of the definition rather
# than out of somebody remembering.
#
# THAT IS NOT THE SAME AS #50 BEING DONE, and nobody should read it that way.
# Deriving the set makes this recipe correct; it does not make the conformance
# module ready to publish. Measured 2026-09-02: `go mod tidy` in
# sdk/go/gorm/conformance SUCCEEDS but rewrites go.mod and go.sum, dropping five
# direct requirements -- the module's declared dependencies do not match its
# committed sources, because the upstream GORM suite it imports is materialised
# on demand by `make gorm-suite-sync` and is not in the tree. So the committed
# module is untidy, AND tidying it would strip the requirements the materialised
# suite needs: it is not a "run tidy and commit" fix. Until #50 resolves that,
# the third tag will point at a module a consumer cannot resolve standalone.
#
# And it was OVERRIDABLE, which was a reported escape: `SUBMODULE_TAGS=` on the
# command line cut a root tag with no lockstep leg. Deriving removes the knob
# instead of guarding it, which is the direction this repo prefers when the risk
# is the next engineer.
#
# THE DERIVATION LIVES IN scripts/lockstep-modules.sh, and check-tag-lockstep.sh
# calls the same script. That is the point rather than tidiness: the publisher
# and the auditor must read ONE derivation, not two that agree today. Two lists
# that happen to match are one edit away from not matching, and the failure is
# silent in the direction that matters -- an auditor narrower than the publisher
# passes green while a namespace goes unchecked. That gap was real here
# (reviewed on PR #212): discovery widened what release-alpha publishes while
# the audit still hard-coded sdk/go.
#
# `$(shell)` discards the script's exit status, so an empty result arrives as an
# empty list rather than an error. The recipe's non-empty assertion is what
# turns that back into a refusal; the script also fails loudly on its own for
# every other caller.
#
# SCOPED TO sdk/, which is the one judgement here, so it is worth stating.
# `find .` would also match test/performance/{qrdb,tikv,etcd-benchmark,
# cmd/kvcompare}/go.mod -- four local benchmark harnesses that must never be
# tagged -- and excluding them would need a hand-maintained exclusion list,
# which is the drift this change exists to remove wearing a different hat.
# sdk/ is the published consumer surface; a module outside it is not part of
# the release. The failure directions decide it: a module wrongly INCLUDED gets
# an irrevocable tag, a module wrongly EXCLUDED gets a missing one that
# check-tag-lockstep and the Release Lockstep workflow both report. Only one of
# those is recoverable.
#
# $(sort) is not decoration: it dedupes AND orders, so the published ref list is
# identical between runs. An unsorted find would make a release's own ref list
# depend on directory order.
override SUBMODULE_TAGS := $(shell ./scripts/lockstep-modules.sh 2>/dev/null)
# Builds never resolve through a workspace: modules come from Gitea tags
# (dependency-resolution.md §2). Immunizes against stray go.work files.
export GOWORK=off

# ---- dependency-resolution.md targets (2026-08-13 cutover) ----
# SUBMODULE_TAGS lists nested modules tagged in lockstep with the root
# (release-versioning.md P2). deps-latest runs before compiling in a
# consumer repo; release-alpha publishes this repo's next -alpha tag.
# deps-latest picks the highest tag INCLUDING -alpha prereleases — bare
# 'go get @latest' ignores prereleases and would downgrade to legacy v0.0.0.

.PHONY: release-alpha deps-latest

# release-alpha — company template software/templates/make/release-alpha.mk v2,
# adapted for this repository's root+sdk/go tag lockstep (go-qrdb#160, #116).
#
# The v1 recipe this replaces had six defects and none of them failed loudly:
#
#   1. It auto-incremented PATCH from `git describe`, so an API addition shipped
#      labelled a bugfix. The version is the only signal a pinning consumer
#      gets, and release-versioning.md §1 puts breakage in MINOR pre-1.0.
#      The version is now an explicit argument; the recipe refuses to guess.
#   2. Every git write used plain `git`, so the VERSION commit carried no SSH
#      signature and was authored as whoever ran make. main enforces signed
#      commits, so such a release is rejected at push time — after the tags
#      already exist locally, in a half-released state unpicked by hand.
#   3. `git tag $NEXT` creates a LIGHTWEIGHT tag. A lightweight tag is a ref to
#      a commit; it has no object of its own and therefore cannot carry a
#      signature at all, whatever tag.gpgsign says. Both the root tag and the
#      sdk/go lockstep tag were unsigned. Tags are now created with `-s`, which
#      makes them annotated tag objects AND signed, and each is verified after
#      creation rather than assumed.
#   4. `git push dev HEAD --tags` pushes EVERY tag in the local clone, not the
#      two just cut. It now names the exact refs and uses --atomic, so the
#      server applies the branch and both tags or none of them (a plain
#      multi-ref push may be accepted in part, which publishes the lockstep
#      skew that check-tag-lockstep exists to prevent).
#   5. `|| true` swallowed a failed VERSION commit and tagged anyway, producing
#      a tag whose VERSION file disagreed with its own name. There is no
#      `|| true` anywhere below; every step is checked, and HEAD is proved to
#      BE the release commit before any tag is created.
#   6. No CHANGELOG stamping. The cut is now refused unless CHANGELOG.md's
#      newest released section already names this version — checked by running
#      scripts/check-version-reconciled.sh, the same script the Release
#      Lockstep workflow runs, against the VERSION content about to be written.
#      A pre-flight that is not identical to the gate it clears for is not a
#      check; reusing the script is what makes it identical.
#
# Usage:
#
#     make release-alpha VERSION=v0.17.0-alpha
#
# Overridable inputs, all with sane defaults for this repository:
#
#   AGENT_GIT       the identity/signing wrapper, resolved from PATH. Not an
#                   absolute path: baking one publishes a developer's home
#                   directory and the internal tooling layout into a repo that
#                   mirrors publicly. If it is not on your PATH, pass
#                   AGENT_GIT=/path/to/agent-git.sh on the command line.
#   REPO_NAME       the LITERAL repo name, never $(notdir $(CURDIR)) — that
#                   derives the signing identity from the checkout directory,
#                   so a worktree named anything else signs as the wrong agent.
#   RELEASE_REMOTE  the Gitea remote. version-control.md §3 names it `dev` in
#                   every company repository and reserves `origin` for nothing
#                   at all, so a command given no remote has nothing to reach
#                   for. It stays a VARIABLE rather than a hardcoded word so a
#                   clone whose remote is named differently can say so; its
#                   existence is asserted before it is trusted, because the
#                   duplicate-tag check below cannot answer against a remote
#                   that does not resolve, and a check that cannot answer must
#                   not read as "no such tag".
#
# SECURITY — why the body touches no $(...) at all. A variable set on the make
# command line is expanded when the recipe's environment is built, even in a
# makefile that never mentions it, so `VERSION=v0.0.0-alpha$(shell ...)` runs
# before the first guard. `$(value ...)` stops the assignment expanding it and
# `unexport` stops the environment build expanding it; `override` is what keeps
# an operator from setting the internal REL_* name directly and outranking the
# sanitised value. Both name families must appear on both lines. Do not
# re-measure this with `make -n`: under -n no recipe runs, no environment is
# built, and an injectable makefile looks clean.
#
# Error messages below are SINGLE-quoted on purpose. A double-quoted echo is a
# live command-substitution context, and error strings are exactly what gets
# edited casually — a $(...) added to one gets executed at the moment a release
# is already failing.
#
# THESE ARE `:=`, NOT `?=`, AND THAT IS THE SECURITY-RELEVANT PART. An
# environment variable is a defined make variable, so `?=` DOES NOT OVERRIDE IT
# and `unexport` does not help -- unexport only keeps a name out of child
# environments. Measured on GNU Make 4.3:
#
#   makefile            environment          command line   REL_REMOTE becomes
#   ----------------    -----------------    ------------   ------------------
#   RELEASE_REMOTE ?=   RELEASE_REMOTE=      (none)         dev
#   RELEASE_REMOTE ?=   RELEASE_REMOTE=origin (none)        ORIGIN  <- wrong
#   RELEASE_REMOTE :=   RELEASE_REMOTE=origin (none)        dev
#   RELEASE_REMOTE :=   RELEASE_REMOTE=origin RELEASE_REMOTE=other  other
#
# With `?=`, any shell or CI job that happens to export RELEASE_REMOTE silently
# redirects where the release is PUSHED, and any shell that exports a
# well-formed VERSION lets a bare `make release-alpha` cut a release the
# operator never named -- the explicit-version guard passes on a value nobody
# typed. `:=` gives the order that is actually wanted: the Makefile's default
# beats the environment, and the command line beats the Makefile. VERSION is
# assigned EMPTY for exactly this reason; it has no sensible default and must
# come from the command line, so the file assignment exists only to neutralise
# an inherited one.
#
# The defaults also come BEFORE the unexport lines. `unexport FOO` DEFINES FOO
# as empty when it is not already set, which silently turned a `?=` default
# into a no-op here before these became `:=`. The order no longer matters for
# `:=`, and is kept because it is the order that reads correctly.
VERSION        :=
AGENT_GIT      := agent-git.sh
REPO_NAME      := go-qrdb
RELEASE_REMOTE := dev

unexport VERSION RELEASE_REMOTE REPO_NAME AGENT_GIT SUBMODULE_TAGS
unexport REL_VERSION REL_REMOTE REL_REPO REL_AGENT_GIT REL_SUBMODULE_TAGS
unexport REL_ORIGIN_VERSION REL_ORIGIN_REMOTE REL_ORIGIN_REPO
unexport REL_ORIGIN_AGENT REL_ORIGIN_SUBMOD

release-alpha: override export REL_VERSION        := $(value VERSION)
release-alpha: override export REL_REMOTE         := $(value RELEASE_REMOTE)
release-alpha: override export REL_REPO           := $(value REPO_NAME)
release-alpha: override export REL_AGENT_GIT      := $(value AGENT_GIT)
release-alpha: override export REL_SUBMODULE_TAGS := $(value SUBMODULE_TAGS)

# `:=` makes the Makefile's default beat the environment -- EXCEPT under
# `make -e`, where the environment beats the Makefile, and `-e` can arrive
# through an inherited MAKEFLAGS without anybody typing it. Measured on GNU
# Make 4.3: with MAKEFLAGS=-e in the environment, an exported VERSION reaches
# the recipe with origin 'environment override' and satisfies the
# explicit-version guard, and an exported RELEASE_REMOTE redirects the push.
#
# So the ORIGIN of each input is carried in beside its value and checked. This
# is the one place a plain $(...) belongs in the recipe: $(origin X) returns a
# word from a fixed vocabulary -- file, command line, environment, override,
# undefined -- and can never carry an operator's text, which is what makes
# expanding it safe where expanding the value would not be.
release-alpha: override export REL_ORIGIN_VERSION := $(origin VERSION)
release-alpha: override export REL_ORIGIN_REMOTE  := $(origin RELEASE_REMOTE)
release-alpha: override export REL_ORIGIN_REPO    := $(origin REPO_NAME)
release-alpha: override export REL_ORIGIN_AGENT   := $(origin AGENT_GIT)
release-alpha: override export REL_ORIGIN_SUBMOD  := $(origin SUBMODULE_TAGS)

# check-tag-lockstep as a prerequisite is the fast, offline pre-check: it fails
# on an obviously broken local tag set before the recipe reaches for the
# network. It is NOT the authoritative one -- it sees only this clone's tags AND
# only the default module, which is why the recipe fetches and audits again
# below over the DISCOVERED set. An audit that knows about fewer namespaces than
# the tagger cuts passes while one of them is broken. And it runs a third
# time after both new tags exist and before the push, which is the only moment
# at which a skewed release can still be prevented rather than reported
# (go-qrdb#13).
release-alpha: check-tag-lockstep
	@for o in "VERSION=$$REL_ORIGIN_VERSION" "RELEASE_REMOTE=$$REL_ORIGIN_REMOTE" "REPO_NAME=$$REL_ORIGIN_REPO" "AGENT_GIT=$$REL_ORIGIN_AGENT" "SUBMODULE_TAGS=$$REL_ORIGIN_SUBMOD"; do \
	  case "$${o#*=}" in \
	    environment|"environment override") echo "ERROR: $${o%%=*} is coming from the ENVIRONMENT, not from this Makefile or the command line. That happens under make -e, including an inherited MAKEFLAGS=-e that nobody typed, and it lets an exported value choose the version or the push destination for a release that cannot be retracted. Pass release inputs on the command line: make release-alpha VERSION=vX.Y.Z-alpha"; exit 1 ;; \
	  esac; \
	done
	@test -n "$$REL_VERSION" || { echo 'ERROR: state the version explicitly: make release-alpha VERSION=vX.Y.Z-alpha (release-versioning.md decides patch-vs-minor - the recipe refuses to guess)'; exit 1; }
	@printf '%s' "$$REL_VERSION" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-alpha$$' || { echo "ERROR: VERSION is not of the form vX.Y.Z-alpha (no leading zeroes): $$REL_VERSION"; exit 1; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "ERROR: releases are cut from main; you are on $$(git rev-parse --abbrev-ref HEAD). Pushing HEAD would publish this branch."; exit 1; }
# Before ANYTHING with a side effect, including the tag fetch further down.
# A refused invocation must not have touched the repository at all, and a fetch
# creates and updates local refs -- so a dirty tree has to be caught while the
# only thing that has happened is reading. Raised in review of PR #212.
	@test -z "$$(git status --porcelain)" || { echo 'ERROR: working tree not clean'; exit 1; }
	@test -n "$$REL_REPO" || { echo 'ERROR: REPO_NAME is empty - set it to the literal repo name, never the checkout directory name'; exit 1; }
	@printf '%s' "$$REL_REPO" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$$' || { echo "ERROR: REPO_NAME is not a plain repo name: $$REL_REPO"; exit 1; }
	@command -v "$$REL_AGENT_GIT" >/dev/null 2>&1 || { echo "ERROR: AGENT_GIT does not resolve to an executable: $$REL_AGENT_GIT. Every git write in this recipe signs through it, and a bad path must fail before VERSION is written. Put agent-git.sh on PATH or pass AGENT_GIT=/path/to/agent-git.sh"; exit 1; }
	@test -n "$$REL_REMOTE" || { echo 'ERROR: RELEASE_REMOTE is empty - set it to the Gitea remote'; exit 1; }
# The remote must resolve, and it must not be GitHub. Both halves are one recipe
# line because each line is its own shell and the URL is needed twice.
#
# A release tag is permanent and public the instant it lands, and AGENTS.md
# forbids pushing to github.com without explicit approval per push. Nothing
# below could take that back, so the refusal belongs here -- BEFORE the
# duplicate-tag check, which would otherwise reach out to github.com over the
# network just to ask a question the answer to which is "do not".
#
# EVERY url the remote has, fetch AND push, in BOTH the configured and the
# rewritten view. Four lists, and each one is there because leaving it out was a
# real defect.
#
#  - `git push` uses `remote.<name>.pushurl` when one is set, so a fetch-only
#    check let a remote with an innocuous fetch url and a github.com pushurl
#    publish anyway. `--all` because a remote may carry several of each.
#
#  - `git remote get-url` APPLIES url.<base>.insteadOf REWRITING. This fleet
#    rewrites `https://github.com/jeremyhahn/` to the internal Gitea host so Go
#    can resolve private modules, and under that config `get-url` returned the
#    Gitea URL for a remote configured as https://github.com/... -- no
#    "github.com" in it, and THE GUARD ACCEPTED A GITHUB REMOTE. It failed on CI
#    and passed locally for exactly that reason, and the ssh form still refused
#    because an https-scoped rewrite does not touch it. A guard against
#    publishing to GitHub, defeated by ambient git config. Measured 2026-09-02.
#
# So the CONFIGURED values are read with `git config --get-all`, which does not
# rewrite, and both views are tested. They answer different questions and both
# matter: the configured value is what the operator ASKED for, and a rewrite is
# ambient and may change; the rewritten value is where git would ACTUALLY
# connect, so a benign-looking remote rewritten INTO github.com must refuse too.
# Refusing on either is over-refusal in the safe direction.
#
# The split-remote comparison uses the CONFIGURED values, because the question
# there is whether this remote was set up to fetch and push to different places,
# not how a rewrite happens to land them today.
#
# The comparison is LOWERCASED first, under LC_ALL=C. DNS host names are
# case-insensitive, so git@GitHub.com:... is the same host and a case-sensitive
# match would have waved it through -- a one-character difference between
# refusing an irrevocable publication and performing it. LC_ALL=C because a
# Turkish locale folds I to a dotless i, which would leave GitHub.com unmatched
# in the one environment nobody tests in.
#
# The test is a substring match. It is deliberately conservative rather than
# clever: it refuses anything naming github.com, which can only ever
# over-refuse, and over-refusing an irrevocable publish is the safe direction.
# It is not a general "is this remote public" oracle -- a GitHub Enterprise host
# under another name would pass, and the approval requirement still applies to
# it. Reading the push urls FAILS CLOSED for the same reason the duplicate-tag
# check does: a remote this recipe cannot fully inspect is not a remote it may
# publish to.
	@EFF_URLS=$$(git remote get-url --all "$$REL_REMOTE" 2>/dev/null) || { echo "ERROR: no git remote named $$REL_REMOTE. version-control.md names the Gitea remote dev; if this clone calls it something else pass RELEASE_REMOTE=<name>. The duplicate-tag check cannot answer against a remote that does not resolve, and refusing here is the difference between a clear error and a release that half-happens"; exit 1; }; \
	EFF_PUSH=$$(git remote get-url --push --all "$$REL_REMOTE" 2>/dev/null) || { echo "ERROR: could not read the push URLs of $$REL_REMOTE. Refusing to publish to a remote this recipe cannot fully inspect"; exit 1; }; \
	CFG_URLS=$$(git config --get-all "remote.$$REL_REMOTE.url" 2>/dev/null || true); \
	CFG_PUSH=$$(git config --get-all "remote.$$REL_REMOTE.pushurl" 2>/dev/null || true); \
	test -n "$$CFG_PUSH" || CFG_PUSH="$$CFG_URLS"; \
	case "$$(printf '%s %s %s %s' "$$CFG_URLS" "$$CFG_PUSH" "$$EFF_URLS" "$$EFF_PUSH" | LC_ALL=C tr '[:upper:]' '[:lower:]')" in \
	  *github.com*) echo "ERROR: $$REL_REMOTE points at github.com. Configured: [$$CFG_URLS] push [$$CFG_PUSH]; after url.<base>.insteadOf rewriting: [$$EFF_URLS] push [$$EFF_PUSH]. BOTH views are checked - see the comment above. AGENTS.md requires explicit approval before any push to github.com, and a release tag is permanent and public the moment it lands - there is no retraction to fall back on. Releases are published to the Gitea remote; a GitHub publication is a separate, deliberate, approved step and never a side effect of RELEASE_REMOTE"; exit 1 ;; \
	esac; \
	test "$$CFG_URLS" = "$$CFG_PUSH" || { echo "ERROR: $$REL_REMOTE fetches from [$$CFG_URLS] but pushes to [$$CFG_PUSH]. When a push cannot be confirmed, this recipe asks the remote what it holds - and that query reads the FETCH url. On a split remote it would be interrogating one server about what it wrote to another, and could answer 'nothing was published' about a live release. Point both at the same place, or release through a remote that does"; exit 1; }
# FETCH BEFORE AUDITING. check-tag-lockstep.sh reads the tags present in THIS
# CLONE, and its own header says a checkout must fetch them first. The
# prerequisite above therefore answers a question about the operator's laptop,
# not about what is published: a clone that is merely stale -- not shallow, not
# tagless, so nothing complains -- can pass the audit while `dev` already
# carries a skewed pair somebody else pushed. Refusing a local skew and
# ignoring a published one is the wrong way round; the published one is the one
# consumers can already resolve. Reported as a P2 in review of PR #212.
#
# This runs after the github.com guard on purpose: fetching is the first thing
# here that talks to the remote, and a remote that must not be published to is
# not one to reach out to either. It also makes the duplicate-tag checks below
# stronger, since a tag that exists remotely is now present locally too.
#
# No --prune-tags. Deleting the operator's local tags to satisfy an audit is a
# larger and more destructive act than refusing to release, and a tag deleted
# on the remote is a separate incident (release-versioning.md 3 says published
# tags are immutable, so it should not happen at all).
	@echo "release-alpha: fetching tags from $$REL_REMOTE into this clone. This ADDS refs and prunes nothing; the lockstep audit below is only as good as the tag set this clone can see."
	@git fetch --quiet --tags "$$REL_REMOTE" || { echo "ERROR: could not fetch tags from $$REL_REMOTE. The lockstep audit is only as good as the tag set this clone can see, and a stale clone hides a skew that is already published"; exit 1; }
# The HISTORICAL audit runs at the script's default, deliberately, and this is
# the one place the discovered set is not used. A module added to the lockstep
# LATER can never satisfy a whole-history audit: every root tag cut before it
# existed has no counterpart, and tags are immutable, so there is no state the
# repository could reach that would pass. Measured here the moment discovery
# picked up sdk/go/gorm/conformance -- the pre-flight refused with MISSING
# against v0.1.0-alpha and no release would ever have been cuttable again.
#
# So history is audited for what has always been in lockstep, and THIS
# release's own tags are verified directly, against the release commit, over
# the whole discovered set (in the tag loop below). Reconciling the history of
# a newly-added namespace is a migration question, and it is go-qrdb#50's.
	@./scripts/check-tag-lockstep.sh || { echo "ERROR: the published tag set is not in lockstep. This is the audit that matters: the prerequisite ran before the fetch above and saw only this clone's tags. Fix the published skew before cutting a release on top of it"; exit 1; }
# The discovery runs when make PARSES the file; the tags are cut much later. So
# it is re-checked here against the filesystem it claimed to describe, rather
# than trusted -- reading a value at entry and acting on it later is exactly the
# gap that produced two of this branch's review findings.
#
# A discovery that finds NOTHING is the fail-open shape of the defect being
# fixed: it would publish a root tag with no lockstep leg at all and pass every
# other check, so it is refused explicitly rather than left to mean "there are
# no nested modules".
	@test -n "$$REL_SUBMODULE_TAGS" || { echo 'ERROR: no nested modules were discovered under sdk/. A release with no lockstep leg is exactly the skew this recipe exists to prevent, so an empty discovery is refused rather than read as "there are none"'; exit 1; }
	@case " $$REL_SUBMODULE_TAGS " in \
	  *" sdk/go "*) : ;; \
	  *) echo "ERROR: the discovered lockstep set does not contain sdk/go: $$REL_SUBMODULE_TAGS. sdk/go is this repository's published SDK and every release has carried its tag; a discovery that lost it is broken, not a repository that stopped having one"; exit 1 ;; \
	esac
	@for s in $$REL_SUBMODULE_TAGS; do \
	  printf '%s' "$$s" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._/-]*$$' || { echo "ERROR: discovered module path is not a plain path: $$s"; exit 1; }; \
	  test -f "$$s/go.mod" || { echo "ERROR: $$s was discovered as a lockstep module but has no go.mod now. Discovery runs at parse time; this is the check that it still describes the tree being tagged"; exit 1; }; \
	done
# EVERY DISCOVERED MODULE MUST BE DECLARED, DECLARED RELEASABLE, AND DECLARED
# AGAINST A VERSION THAT EITHER EXISTS OR IS THE ONE BEING CUT.
#
# The third clause is a state I designed without enumerating. Three states were
# intended -- a version, `unreleased`, no line at all -- but a version that does
# NOT exist and is NOT this release is a fourth, and it was reachable: the audit
# reports such a module `pending` and skips its history scan, forever, so every
# later release could omit its counterpart without failing anything.
#
# The hole came from a correct decision generalised too far. Permitting a
# baseline that names an untagged version is necessary -- it is how a module's
# FIRST release is cut, by setting the baseline to the version you are about to
# cut. But only that one instance of "does not exist yet" is legitimate, and I
# permitted the whole class. Requiring it to equal REL_VERSION preserves the
# bootstrap by construction and closes the rest. An enumeration bounds only the
# axis it enumerates, and this was the state axis. The audit
# is scoped by lockstep-baseline, so a module recorded as `unreleased` is out of
# scope for the history check -- which is correct there and would be a hole
# here: cutting a release that includes a module nobody has decided to release
# is exactly what the baseline exists to prevent. Undeclared is worse still.
#
# This is the release half of the same fact the audit reads. The audit reports;
# the release refuses.
	@for s in $$REL_SUBMODULE_TAGS; do \
	  b=$$(./scripts/lockstep-modules.sh --baseline "$$s" . 2>/dev/null) || { echo "ERROR: $$s is discovered as a lockstep module but is not declared in lockstep-baseline. An undeclared module would be tagged without anyone having decided it should be released. Add a line for it - a version, or 'unreleased'"; exit 1; }; \
	  test "$$b" != unreleased || { echo "ERROR: $$s is declared 'unreleased' in lockstep-baseline, so this release will not cut a tag for it - and a release that silently omits a discovered module is the skew this recipe exists to prevent. Decide: set its baseline to the version being cut, or remove the module from sdk/. For sdk/go/gorm/conformance that decision is go-qrdb#50's"; exit 1; }; \
	  test "$$b" = "$$REL_VERSION" || git rev-parse -q --verify "refs/tags/$$b" >/dev/null || { echo "ERROR: $$s has baseline $$b in lockstep-baseline, but no root tag $$b exists and it is not the version being cut ($$REL_VERSION). The audit would report that module 'pending' and skip its history scan FOREVER, so every later release could omit its counterpart without failing. A baseline may name a tag that does not exist ONLY when it is the release being cut - that is how a module's first release works"; exit 1; }; \
	done
# ASK THE MODULE, DO NOT ASSUME IT IS FINE. A lockstep tag is a permanent,
# consumer-resolvable reference; cutting one for a module whose go.mod does not
# match its own sources publishes a reference that cannot be resolved
# standalone -- the exact skew the lockstep tag exists to prevent, created by
# the mechanism meant to prevent it. Owner ruling, 2026-09-02.
#
# `go mod tidy -diff` is the check and it MUTATES NOTHING: it reports whether
# go.mod/go.sum would change, and exits nonzero if they would or if the module
# graph cannot be loaded at all. Both mean "not publishable". Measured on this
# tree: sdk/go passes, sdk/go/gorm/conformance does not.
#
# It is not the whole of #50's "a fresh clone resolves the tag" bullet, and
# should not be read as it -- that check needs the tag to exist first, which is
# a chicken and egg a pre-flight cannot resolve. This is the strongest question
# that CAN be asked before the irrevocable step.
#
# CONSEQUENCE, stated rather than discovered: while sdk/go/gorm/conformance is
# untidy, `make release-alpha` REFUSES. That is deliberate and there is no
# override -- an escape hatch here would be used the first time someone is in a
# hurry, which is exactly when it matters. A blocked release is recoverable; a
# published broken tag is not. Unblocking it is go-qrdb#50's conformance work.
#
# Literal `go`, not $(GO): a settable make variable in the recipe body is an
# injection surface, and this recipe deliberately takes no toolchain override.
	@for s in $$REL_SUBMODULE_TAGS; do \
	  ( cd "$$s" && go mod tidy -diff >/dev/null 2>&1 ) || { echo "ERROR: $$s does not resolve standalone: its go.mod does not match its own sources (go mod tidy -diff reports changes, or the module graph will not load). A lockstep tag is permanent and consumers resolve it, so this refuses rather than publish a reference that cannot be resolved. There is no override. If this is sdk/go/gorm/conformance, it is go-qrdb#50's remaining work"; exit 1; }; \
	done
	@TAGS="$$REL_VERSION"; for s in $$REL_SUBMODULE_TAGS; do TAGS="$$TAGS $$s/$$REL_VERSION"; done; \
	for t in $$TAGS; do \
	  ! git rev-parse -q --verify "refs/tags/$$t" >/dev/null || { echo "ERROR: tag $$t already exists locally"; exit 1; }; \
	  rc=0; git ls-remote --exit-code --tags "$$REL_REMOTE" "refs/tags/$$t" >/dev/null 2>&1 || rc=$$?; \
	  case "$$rc" in \
	    0) echo "ERROR: tag $$t already exists on $$REL_REMOTE (a local check alone would miss this and fail the push AFTER committing VERSION)"; exit 1 ;; \
	    2) : ;; \
	    *) echo "ERROR: could not reach $$REL_REMOTE to check whether tag $$t exists. Refusing to continue: an unreachable remote is not an absent tag"; exit 1 ;; \
	  esac; \
	done
	@d=$$(mktemp -d); test -n "$$d" || { echo 'ERROR: could not create a temporary directory for the CHANGELOG pre-flight'; exit 1; }; \
	printf '%s\n' "$${REL_VERSION#v}" > "$$d/VERSION"; \
	cp CHANGELOG.md "$$d/CHANGELOG.md"; \
	rc=0; ./scripts/check-version-reconciled.sh "$$d" "$$REL_VERSION" || rc=$$?; \
	rm -rf "$$d"; \
	test "$$rc" -eq 0 || { echo "ERROR: CHANGELOG.md does not carry $${REL_VERSION#v} as its newest released section. Write the release notes first; this runs the same script the Release Lockstep workflow runs, on a copy, so nothing in the working tree was touched"; exit 1; }
# EVERYTHING FROM HERE TO THE PUSH IS ONE SHELL, BECAUSE IT IS ONE TRANSACTION.
# Each recipe line is its own shell, so a cleanup handler cannot be shared
# across lines -- and while these were separate lines, three failure paths
# escaped cleanup: the two post-commit assertions, and the post-tag lockstep
# audit. The last one was real and reported (PR #212): `SUBMODULE_TAGS=` on the
# command line produced a release commit and a signed root tag with no sdk/go
# counterpart, nothing published, and a RETRY that then failed on the recipe's
# own debris -- at the lockstep PREREQUISITE, with a message that never
# mentioned the previous run. `override` on SUBMODULE_TAGS closes that one
# door; drawing the boundary here closes the corridor.
#
# The contract is now absolute: after this point the release either publishes,
# or the repository is exactly as it was. No tag, no release commit, no staged
# VERSION, no advice to run a two-step recovery by hand -- an earlier revision
# printed such advice and got it wrong, which is what the regression tests
# caught. Recovery is to fix the cause and run the target again.
#
# EACH TAG IS RECORDED IN THE CLEANUP SET BEFORE IT IS CREATED, not after. A
# wrapper that creates the tag and THEN exits nonzero -- post-command logic
# failing, or a lost success response -- would otherwise leave behind a tag the
# cleanup set never heard of, and the next attempt would fail the duplicate-tag
# preflight on this run's own debris. It is the same shape as trusting a push's
# exit code about what the remote did: the operation can succeed while the
# report of it fails, and the two must not be conflated.
#
# Recording it early is safe ONLY because the preflight has already established
# that none of these tags existed, locally or on the remote, after the fetch --
# so cleanup cannot delete a tag this invocation did not make. It deletes per
# tag and skips any that is absent, so a genuinely uncreated tag costs nothing
# while a real failure to delete still warns.
#
# cleanup resets to the commit recorded BEFORE anything was written, not to
# HEAD~1: it undoes exactly this invocation whether it got as far as committing
# or not. `reset --hard` is safe here and only here, because the clean-tree
# guard above has already established there is nothing else to lose. It removes
# LOCAL objects only -- anything that genuinely reached the remote is still
# there, and the duplicate-tag guard will refuse the next cut loudly rather
# than quietly re-cutting a published version.
#
# THREE OUTCOMES, NOT TWO. `--atomic` guarantees the remote applies every ref or
# none; it guarantees nothing about whether the CLIENT finds out. The server can
# commit the update and the connection drop before the success response arrives,
# so a nonzero exit is not evidence that nothing was published -- it is the
# absence of evidence either way.
#
# The old branch collapsed "I could not confirm" into "nothing happened", then
# deleted the local tags and told the operator nothing was published, while the
# tags might be live, immutable and already resolvable by a consumer. Deleting
# the only local copy of a release you cannot prove is absent is the worst
# outcome on the table. Present, absent and UNKNOWN are three answers.
#
# So the exit code is not trusted: the remote is asked what it holds, and the
# object each ref points at is compared with the object we meant to publish.
# THE PUSH NAMES THE COMMIT, NOT `HEAD`. `HEAD` is a moving reference resolved
# at push time; every assertion above was made about the commit as it stood when
# they ran. Pushing `HEAD` means the guards checked one object and the push
# published whatever the symbol pointed at by then -- the same
# read-a-proxy-for-the-thing shape as validating a fetch URL and then pushing to
# a pushurl, or reading SUBMODULE_TAGS at entry and tagging from it later. An
# explicit `<sha>:refs/heads/main` publishes exactly what was verified, and the
# verification below compares against that same recorded object.
#
# The question asked of each ref is "does the remote hold the OBJECT we meant to
# publish", not "does the ref exist". refs/heads/main always exists -- at the
# previous commit -- so an existence test could never see "nothing landed", and
# every failed push would look mixed. Comparing objects is what makes the three
# outcomes distinguishable.
#
# Every ref matches -> the release is live and the error was in the reporting;
# report success and roll back nothing. None matches -> nothing landed; clean up
# as usual. Anything in between, including a query that cannot be answered ->
# PRESERVE EVERYTHING and fail with the ref-by-ref detail, because a human has
# to reconcile it and needs the local objects to do it with.
	@BASE_COMMIT=$$(git rev-parse HEAD); \
	CREATED=""; \
	cleanup() { \
	  for t in $$CREATED; do \
	    git rev-parse -q --verify "refs/tags/$$t" >/dev/null || continue; \
	    git tag -d "$$t" >/dev/null || echo "WARNING: could not delete $$t; remove it by hand before retrying" >&2; \
	  done; \
	  git reset -q --hard "$$BASE_COMMIT" || echo 'WARNING: could not reset to the pre-release commit; check git status before retrying' >&2; \
	}; \
	fail() { \
	  cleanup; \
	  echo "$$1" >&2; \
	  echo '  Nothing was published. The release commit and any tag this run created have been removed and the tree is back where it started, so fixing the cause and re-running the target is the whole recovery.' >&2; \
	  exit 1; \
	}; \
	printf '%s\n' "$${REL_VERSION#v}" > VERSION; \
	"$$REL_AGENT_GIT" "$$REL_REPO" add VERSION || fail "ERROR: could not stage VERSION"; \
	"$$REL_AGENT_GIT" "$$REL_REPO" commit -q -m "chore(release): $$REL_VERSION" || fail 'ERROR: the release VERSION commit failed - v1 swallowed this with || true and tagged anyway, which is how a tag comes to disagree with its own VERSION file'; \
	test "$$(git show HEAD:VERSION)" = "$${REL_VERSION#v}" || fail "ERROR: HEAD is not the release commit for $$REL_VERSION - refusing to tag"; \
	test "$$(git cat-file commit HEAD | grep -c '^gpgsig')" -eq 1 || fail 'ERROR: the release commit carries no SSH signature. main enforces signed commits, so this would be rejected at push time - and without this check that rejection would arrive after the tags already existed'; \
	test "$$(git log -1 --format='%an %cn')" = "agent-$${REL_REPO#agent-} agent-$${REL_REPO#agent-}" || fail "ERROR: the release commit is authored/committed by $$(git log -1 --format='%an / %cn'), not the repository agent. AGENT_GIT is not the identity wrapper"; \
	REL_COMMIT=$$(git rev-parse HEAD); \
	TAGS="$$REL_VERSION"; \
	REFS="refs/tags/$$REL_VERSION"; \
	for s in $$REL_SUBMODULE_TAGS; do \
	  TAGS="$$TAGS $$s/$$REL_VERSION"; \
	  REFS="$$REFS refs/tags/$$s/$$REL_VERSION"; \
	done; \
	for t in $$TAGS; do \
	  CREATED="$$CREATED $$t"; \
	  "$$REL_AGENT_GIT" "$$REL_REPO" tag -s -m "$$t" "$$t" || fail "ERROR: could not create signed tag $$t"; \
	  test "$$(git cat-file -t "$$t")" = tag || fail "ERROR: $$t is a lightweight tag and can carry no signature"; \
	  git cat-file tag "$$t" | grep -q 'BEGIN SSH SIGNATURE' || fail "ERROR: $$t is annotated but UNSIGNED"; \
	  test "$$(git rev-parse "$$t^{commit}")" = "$$REL_COMMIT" || fail "ERROR: $$t does not point at the release commit. This is the lockstep property for THIS release, checked over every discovered module rather than left to an audit that only knows the default one"; \
	done; \
	./scripts/check-tag-lockstep.sh || fail "ERROR: the tags just created are not in lockstep with each other. Nothing was pushed - this is the one moment a skewed release can still be prevented rather than reported (go-qrdb#13)"; \
	if "$$REL_AGENT_GIT" "$$REL_REPO" push --atomic "$$REL_REMOTE" "$$REL_COMMIT:refs/heads/main" $$REFS; then \
	  echo "Released $$REL_VERSION on $$REL_REMOTE (signed commit; annotated signed tags:$$TAGS; atomic push of exactly those refs)"; \
	  exit 0; \
	fi; \
	echo 'WARNING: the push client reported failure. That does not say WHICH outcome occurred, so the remote is being asked what it actually holds before anything is touched.' >&2; \
	PROBE=$$(git ls-remote "$$REL_REMOTE" refs/heads/main $$REFS 2>/dev/null) || PROBE='@@unqueryable@@'; \
	if [ "$$PROBE" = '@@unqueryable@@' ]; then \
	  echo "ERROR: the push to $$REL_REMOTE failed AND the remote cannot be queried, so whether this release was published is UNKNOWN. Nothing has been rolled back on purpose: the release commit and the tags$$TAGS are intact locally, because deleting the only local copy of a release that may already be live and immutable is worse than leaving it. Restore access and compare: git ls-remote $$REL_REMOTE refs/heads/main$$REFS" >&2; \
	  exit 1; \
	fi; \
	CHECK="refs/heads/main=$$REL_COMMIT"; \
	for t in $$TAGS; do CHECK="$$CHECK refs/tags/$$t=$$(git rev-parse "refs/tags/$$t")"; done; \
	MATCH=0; TOTAL=0; DETAIL=""; \
	for c in $$CHECK; do \
	  r=$${c%%=*}; want=$${c#*=}; \
	  got=$$(printf '%s\n' "$$PROBE" | awk -v r="$$r" '$$2 == r { print $$1; exit }'); \
	  TOTAL=$$((TOTAL + 1)); \
	  if [ "$$got" = "$$want" ]; then MATCH=$$((MATCH + 1)); DETAIL="$$DETAIL\n    published      $$r"; \
	  elif [ -z "$$got" ]; then DETAIL="$$DETAIL\n    not published  $$r is absent"; \
	  else DETAIL="$$DETAIL\n    not published  $$r still holds $$got, not $$want"; fi; \
	done; \
	if [ "$$MATCH" -eq "$$TOTAL" ]; then \
	  echo "NOTE: the remote holds every intended ref at the expected object, so the release IS published and the client error was in reporting it, not in doing it. Nothing was rolled back." >&2; \
	  echo "Released $$REL_VERSION on $$REL_REMOTE (signed commit; annotated signed tags:$$TAGS; the push client errored after the remote had applied every ref)"; \
	  exit 0; \
	fi; \
	if [ "$$MATCH" -eq 0 ]; then \
	  fail "ERROR: the push to $$REL_REMOTE failed and the remote carries none of this release - established by asking it what it holds, not by reading the client's exit code"; \
	fi; \
	printf 'ERROR: the push to %s failed and the remote is in a MIXED state, which --atomic is meant to make impossible. NOTHING HAS BEEN ROLLED BACK: the release commit and the tags%s are intact locally, because part of this release may already be published and immutable. Reconcile by hand and do not re-run until you have.%b\n' "$$REL_REMOTE" "$$TAGS" "$$DETAIL" >&2; \
	exit 1

deps-latest:
	@GOWORK=off sh -c ' \
	SELF=$$(go list -m); \
	for m in $$(go list -m -f "{{if not .Indirect}}{{.Path}}{{end}}" all 2>/dev/null | grep "^github.com/jeremyhahn/" | grep -vx "$$SELF"); do \
	  case "$$(go list -m -f "{{if .Replace}}{{.Replace.Path}}{{end}}" "$$m" 2>/dev/null)" in .*|/*) continue;; esac; \
	  V=$$(go list -m -versions "$$m" 2>/dev/null | tr " " "\n" | tail -1); \
	  if [ -n "$$V" ] && [ "$$V" != "$$m" ]; then go get "$$m@$$V" || exit 1; fi; \
	done; go mod tidy'
