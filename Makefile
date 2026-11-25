# go-qrdb build system: a distributed key-value store on multi-group Raft over QUIC.

.DEFAULT_GOAL := build

# Release inputs are unexported before the first $(shell ...), which GNU Make 4.4 runs with exported variables.
unexport VERSION RELEASE_REMOTE REPO_NAME AGENT_GIT SUBMODULE_TAGS
unexport REL_VERSION REL_REMOTE REL_REPO REL_AGENT_GIT REL_SUBMODULE_TAGS
unexport REL_ORIGIN_VERSION REL_ORIGIN_REMOTE REL_ORIGIN_REPO
unexport REL_ORIGIN_AGENT REL_ORIGIN_SUBMOD
unexport RELEASE_TAG REL_RELEASE_TAG

# Project metadata
MODULE        := github.com/jeremyhahn/go-qrdb
SERVER_BINARY := qrdbd
CLIENT_BINARY := qrdb
COVER_DIR     := coverage
# The build stamp is BUILD_VERSION so that VERSION stays the release-alpha argument.
BUILD_VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.1-alpha")
GIT_COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS       := -X main.version=$(BUILD_VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

# Pinned tool versions, shared with the CI image's toolchain stage.
TOOL_PINS := scripts/tool-pins.sh
include $(TOOL_PINS)

# Go tooling
GO            := go
# Per-package go test timeout; pkg/seal under -race needs more than the 10m default, and the FIPS leg sets its own.
GOTEST_TIMEOUT ?= 30m
GOTEST        := $(GO) test -race -count=1 -v -timeout=$(GOTEST_TIMEOUT)
# COVER_GOTEST omits -count=1 so unchanged packages replay their cached result and coverage profile.
COVER_GOTEST  := $(GO) test -race -v -timeout=$(GOTEST_TIMEOUT)
# INT_GOTEST omits -race for the integration suites; INT_PARALLEL is the per-feature -parallel, one per transport.
INT_PARALLEL  ?= 5
INT_GOTEST    := $(GO) test -count=1 -v -timeout=$(GOTEST_TIMEOUT) -parallel $(INT_PARALLEL)
GOBUILD       := $(GO) build -ldflags "$(LDFLAGS)"
GOVET         := $(GO) vet
GOFMT         := gofmt
GOLINT        := golangci-lint
GOSEC         := gosec
GOVULNCHECK   := govulncheck

# gosec rule exclusions; each was measured by enabling one rule at a time, and inline #nosec covers the rest.
GOSEC_EXCLUDE := G115,G304,G704

# Directories the root-module gosec scan skips: separately pinned benchmark modules and analyzer fixtures gosec cannot typecheck.
GOSEC_EXCLUDE_DIRS := -exclude-dir=test/performance -exclude-dir=pkg/secure/analyzer/testdata

# Coverage
COVER_MIN     := $(or $(COVERAGE_THRESHOLD),91)

# Docker
DOCKER_IMAGE  := go-qrdb-dev
DOCKER_TAG    := latest
COMPOSE_FILE  := build/docker/devcontainer/compose.yaml

# Module credentials for every build of build/docker/devcontainer/Dockerfile, passed as the BuildKit secret GOMODAUTH from the environment.
MODULE_ENV_FILE ?=

define stage-gomodauth
set -eu; \
gomodauth_src="the job environment"; \
if [ -n '$(MODULE_ENV_FILE)' ]; then \
  [ -f '$(MODULE_ENV_FILE)' ] || { echo "ERROR: MODULE_ENV_FILE names no file" >&2; exit 1; }; \
  set -a; . '$(MODULE_ENV_FILE)'; set +a; \
  gomodauth_src="MODULE_ENV_FILE"; \
fi; \
if [ -n "$${GIT_CONFIG_KEY_0:-}" ]; then \
  GOMODAUTH="export GOPRIVATE='$${GOPRIVATE:-}'; export GIT_CONFIG_COUNT='$${GIT_CONFIG_COUNT:-}'; export GIT_CONFIG_KEY_0='$${GIT_CONFIG_KEY_0}'; export GIT_CONFIG_VALUE_0='$${GIT_CONFIG_VALUE_0:-}'"; \
  export GOMODAUTH; \
  echo "==> module credentials: from $$gomodauth_src"; \
else \
  echo "==> no module credentials set; resolving from the public proxy"; \
fi;
endef

# Benchmarks
KV_BENCHTIME  ?= 5s
QUICRAFT_ROOT ?= $(shell cd .. && pwd)/go-quicraft

# Optional feature flags (1 = enabled, 0 = disabled)
WITH_QRDB_TS     ?= 1
WITH_QRDB_VECTOR ?= 1
WITH_QRDB_VLOG   ?= 1
WITH_QRDB_SQL    ?= 1
WITH_QRDB_MYSQL  ?= 1
WITH_QRDB_PGSQL  ?= 1
WITH_QRDB_ETCD   ?= 1
WITH_QRDB_FTS    ?= 0
WITH_QRDB_GPU      ?= 0
WITH_QRDB_EMBEDDED ?= 1
WITH_QRDBD_WEBUI   ?= 0

# Object-storage cloud backends for the objstore backend and backup/restore; off by default, tag names match go-objstore.
WITH_AWS_S3        ?= 0
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

# WITH_QRDB_SQL=0 turns off both SQL wire protocols, which run on pkg/sql.
ifeq ($(WITH_QRDB_SQL),0)
  override WITH_QRDB_MYSQL := 0
  override WITH_QRDB_PGSQL := 0
endif

# Either SQL wire protocol turns on pkg/sql unless SQL was turned off above.
ifeq ($(WITH_QRDB_MYSQL),1)
  override WITH_QRDB_SQL := 1
endif
ifeq ($(WITH_QRDB_PGSQL),1)
  override WITH_QRDB_SQL := 1
endif

# REQUIRED_TAGS are the tags no WITH_* flag can turn off.
REQUIRED_TAGS := codec_json raft

# Compute build tags from feature flags
BUILD_TAGS := $(REQUIRED_TAGS)
ifeq ($(WITH_QRDB_TS),1)
  BUILD_TAGS += qrdb_ts
endif
ifeq ($(WITH_QRDB_VECTOR),1)
  BUILD_TAGS += qrdb_vector
endif
ifeq ($(WITH_QRDB_VLOG),1)
  BUILD_TAGS += qrdb_vlog
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
# Object-storage (go-objstore) cloud backends.
ifeq ($(WITH_AWS_S3),1)
  BUILD_TAGS += awss3
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
# GO_BUILD_TAGS, when set, replaces the composed tag set, except for the optional WITH_ flags re-added below.
ifneq ($(strip $(GO_BUILD_TAGS)),)
  BUILD_TAGS := $(strip $(GO_BUILD_TAGS))
endif
# A WITH_ flag named by print-optional-test-tags is added on top of GO_BUILD_TAGS.
ifeq ($(WITH_QRDB_FTS),1)
  BUILD_TAGS := $(strip $(filter-out qrdb_fts,$(BUILD_TAGS)) qrdb_fts)
endif
DESKTOP_TAGS := desktop production webkit2_41 $(BUILD_TAGS)

# Build tag flags for go commands
TAG_FLAG         := $(if $(strip $(BUILD_TAGS)),-tags "$(strip $(BUILD_TAGS))",)
DESKTOP_TAG_FLAG := -tags "$(strip $(DESKTOP_TAGS))"

# QRDB_BUILD_TAGS is exported for the integration compose build args.
export QRDB_BUILD_TAGS := $(strip $(BUILD_TAGS))

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
PKG_ENGINE         := ./pkg/engine/...
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
PKG_ETCD           := ./pkg/api/etcd/...
PKG_DATABASE       := ./pkg/database/...
PKG_QUIC           := ./pkg/api/quic/...
PKG_MCP            := ./pkg/api/mcp/...
PKG_FTS            := ./pkg/fts/...

# SDK (separate Go module)
SDK_DIR            := sdk/go

# SDK packages addressed individually (paths are relative to SDK_DIR)
SDK_PKG_QRDBDHARNESS := ./testutil/qrdbdharness/...

# Integration test paths
INT_TEST_DIR  := ./test/integration

# Per-feature integration test selection; see the header of test/integration/features.mk.
include test/integration/features.mk

# Primary targets

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
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) run --rm --build client-build

.PHONY: _build-client
_build-client: build-frontend
	CGO_ENABLED=1 $(GOBUILD) $(DESKTOP_TAG_FLAG) -o bin/$(CLIENT_BINARY) ./cmd/qrdb

# Release artifacts: qrdbd and the qrdb CLI per platform, plain and FIPS, static with CGO_ENABLED=0 and gms_pure_go.
RELEASE_DIR         := dist
RELEASE_PLATFORMS   ?= linux/amd64 linux/arm64
RELEASE_FIPS_MODULE := latest
RELEASE_TAGS        := $(strip $(BUILD_TAGS) gms_pure_go)

# release-build builds qrdbd and qrdb for $(1)=GOOS $(2)=GOARCH with suffix $(3) and GOFIPS140=$(4).
release-build = for cmd in $(SERVER_BINARY) $(CLIENT_BINARY); do \
	GOOS=$(1) GOARCH=$(2) GOFIPS140=$(4) CGO_ENABLED=0 \
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -tags "$(RELEASE_TAGS)" \
	-o $(RELEASE_DIR)/$$cmd-$(BUILD_VERSION)-$(1)-$(2)$(3) ./cmd/$$cmd || exit 1; \
	done

# release-platform expands to the plain and the FIPS build of $(1)=goos/goarch.
release-platform = $(call release-build,$(word 1,$(subst /, ,$(1))),$(word 2,$(subst /, ,$(1))),,off) && \
	$(call release-build,$(word 1,$(subst /, ,$(1))),$(word 2,$(subst /, ,$(1))),-fips,$(RELEASE_FIPS_MODULE))

# release-cross builds one platform, plain and FIPS: make release-cross PLATFORM=linux/arm64
# It ends with check-release-artifacts for $(PLATFORM) without SHA256SUMS.
.PHONY: release-cross
release-cross:
	@case "$(PLATFORM)" in */*) ;; *) echo "release-cross: set PLATFORM=<goos>/<goarch>, e.g. make release-cross PLATFORM=linux/arm64" >&2; exit 2 ;; esac
	@mkdir -p $(RELEASE_DIR)
	$(call release-platform,$(PLATFORM))
	@$(MAKE) --no-print-directory check-release-artifacts PLATFORMS='$(PLATFORM)' CRA_SUMS=0

# test-cross compiles the release test binaries of one platform, plain and FIPS, without running them.
.PHONY: test-cross
test-cross:
	@case "$(PLATFORM)" in */*) ;; *) echo "test-cross: set PLATFORM=<goos>/<goarch>, e.g. make test-cross PLATFORM=linux/arm64" >&2; exit 2 ;; esac
	@set -e; os=$(word 1,$(subst /, ,$(PLATFORM))); arch=$(word 2,$(subst /, ,$(PLATFORM))); \
	for fips in off $(RELEASE_FIPS_MODULE); do \
		for tags in '$(RELEASE_TAGS)' '$(RELEASE_TAGS) integration'; do \
			echo "==> test-cross $(PLATFORM) GOFIPS140=$$fips . tags=$$tags"; \
			GOOS=$$os GOARCH=$$arch GOFIPS140=$$fips CGO_ENABLED=0 \
				$(GO) test -count=1 -vet=off -exec /bin/true -tags "$$tags" ./...; \
		done; \
		for tags in '$(RELEASE_TAGS)' '$(RELEASE_TAGS) integration'; do \
			echo "==> test-cross $(PLATFORM) GOFIPS140=$$fips $(SDK_DIR) tags=$$tags"; \
			(cd $(SDK_DIR) && GOOS=$$os GOARCH=$$arch GOFIPS140=$$fips CGO_ENABLED=0 \
				$(GO) test -count=1 -vet=off -exec /bin/true -tags "$$tags" ./...); \
		done; \
		echo "==> test-cross $(PLATFORM) GOFIPS140=$$fips test/clusterproof tags=$(MIN_TAGS),clusterproof"; \
		(cd test/clusterproof && GOOS=$$os GOARCH=$$arch GOFIPS140=$$fips CGO_ENABLED=0 \
			$(GO) test -count=1 -vet=off -exec /bin/true -tags "$(MIN_TAGS),clusterproof" ./...); \
	done
	@echo "==> test-cross $(PLATFORM): root, $(SDK_DIR) and test/clusterproof test binaries compiled, plain and FIPS"

# test-test-cross is test-cross's regression test against a recording stub go.
.PHONY: test-test-cross
test-test-cross:
	@./scripts/test-cross_test.sh

# release-artifacts builds every RELEASE_PLATFORMS entry into a clean $(RELEASE_DIR), writes SHA256SUMS and checks the set with check-release-artifacts.
.PHONY: release-artifacts
release-artifacts:
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	$(foreach p,$(RELEASE_PLATFORMS),$(call release-platform,$(p)) && ) true
	cd $(RELEASE_DIR) && sha256sum $(SERVER_BINARY)-* $(CLIENT_BINARY)-* > SHA256SUMS
	@$(MAKE) --no-print-directory check-release-artifacts PLATFORMS='$(RELEASE_PLATFORMS)' CRA_SUMS=1

# check-release-artifacts checks each artifact's ELF machine, GOOS/GOARCH, release flags, tags, FIPS variant and, with CRA_SUMS=1, SHA256SUMS.
PLATFORMS ?= $(RELEASE_PLATFORMS)
CRA_SUMS  ?= 1
.PHONY: check-release-artifacts
check-release-artifacts:
	@CRA_DIR='$(RELEASE_DIR)' CRA_VERSION='$(BUILD_VERSION)' CRA_PLATFORMS='$(PLATFORMS)' \
		CRA_BINARIES='$(SERVER_BINARY) $(CLIENT_BINARY)' CRA_SUMS='$(CRA_SUMS)' \
		CRA_FIPS_MODULE='$(RELEASE_FIPS_MODULE)' CRA_TAGS='$(RELEASE_TAGS)' GO='$(GO)' \
		./scripts/check-release-artifacts.sh

# release-smoke runs every artifact of the host PLATFORM and checks its version, the -fips variant under GODEBUG=fips140=only.
.PHONY: release-smoke
release-smoke:
	@RS_DIR='$(RELEASE_DIR)' RS_VERSION='$(BUILD_VERSION)' RS_PLATFORM='$(PLATFORM)' \
		RS_BINARIES='$(SERVER_BINARY) $(CLIENT_BINARY)' GO='$(GO)' \
		./scripts/release-smoke.sh

# test-release-smoke tests release-smoke with stub artifacts and a stub GO.
.PHONY: test-release-smoke
test-release-smoke:
	@GO='$(GO)' ./scripts/release-smoke_test.sh

# test-check-release-artifacts tests check-release-artifacts against built fixtures and broken copies of them.
.PHONY: test-check-release-artifacts
test-check-release-artifacts:
	@GO='$(GO)' ./scripts/check-release-artifacts_test.sh

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

# test-typescript installs, typechecks, tests and builds the TypeScript SDK, then runs svelte-check and the mocked Playwright tier.
PLAYWRIGHT_INSTALL_FLAGS ?= --with-deps
.PHONY: test-typescript
test-typescript: ## TypeScript SDK typecheck, tests and build; desktop frontend svelte-check and mocked Playwright
	cd sdk/typescript && npm ci && npx tsc --noEmit -p . && npm test && npm run build
	cd cmd/qrdb/frontend && npm ci && npm run check && npm run build
	cd cmd/qrdb/frontend && npx playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium
	cd cmd/qrdb/frontend && CI=true npx playwright test --workers=2

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

# sdk/go/proto is the public proto root and precedes "." on the include path.
PROTO_ROOT := sdk/go/proto

.PHONY: proto
proto:
	protoc -I $(PROTO_ROOT) -I . \
		--go_out=. --go_opt=module=github.com/jeremyhahn/go-qrdb \
		--go-grpc_out=. --go-grpc_opt=module=github.com/jeremyhahn/go-qrdb \
		$(PROTO_ROOT)/qrdb/v1/permission.proto \
		pkg/api/grpc/proto/qrdbv1/qrdb.proto

# A .proto defined outside go-qrdb must compile with only $(PROTO_ROOT) on the include path.
PUBLIC_PROTO_FIXTURE := test/buildgates/testdata/publicproto

.PHONY: check-public-proto
check-public-proto:
	@out=$$(mktemp -d) && trap 'rm -rf "$$out"' EXIT && \
	protoc -I $(PROTO_ROOT) -I $(PUBLIC_PROTO_FIXTURE) \
		--descriptor_set_out="$$out/billing.pb" \
		--go_out="$$out" --go-grpc_out="$$out" \
		$(PUBLIC_PROTO_FIXTURE)/billing/v1/invoice.proto && \
	echo "check-public-proto: billing/v1/invoice.proto compiles against qrdb/v1/permission.proto"

# Unit tests

# ./test/buildgates holds gates over properties of the repository rather than of the program.
TEST_PKGS := ./pkg/... ./cmd/... ./test/buildgates/...

.PHONY: test
test: test-clusterproof
	$(GOTEST) $(TAG_FLAG) $(TEST_PKGS)

# Unit legs: CI splits the unit suites into parallel legs by measured wall time; `rest` and `sdk` take every package no leg names.
unit-skip = $(subst $(space),|,$(strip $(foreach l,$(1),$(UNIT_RUN_$(l)))))
UNIT_LEG_tenant-lifecycle := ./pkg/tenant
UNIT_RUN_tenant-lifecycle := ^Test(TenantService|CreateTenant|StoreResolver|DeleteTenant|UndoCreate)
UNIT_LEG_tenant        := ./pkg/tenant
UNIT_SKIP_tenant        = $(call unit-skip,tenant-lifecycle)
UNIT_LEG_seal-barrier  := ./pkg/seal
UNIT_RUN_seal-barrier  := ^Test(Barrier|StorageBarrier|ChangePassword|SealStore|TenantBarrier)
UNIT_LEG_seal          := ./pkg/seal
UNIT_SKIP_seal          = $(call unit-skip,seal-barrier)
UNIT_LEG_server-raft   := ./pkg/server
UNIT_RUN_server-raft   := ^Test(Raft|ReattachRaft)
UNIT_LEG_server        := ./pkg/server
UNIT_SKIP_server        = $(call unit-skip,server-raft)
UNIT_LEG_auth          := ./pkg/server/lifecycle/... ./pkg/auth/... ./cmd/...
UNIT_LEG_service       := ./pkg/service/... ./pkg/database/... ./pkg/statemachine/... ./pkg/engine/... \
                          ./pkg/dao/... ./pkg/store/...
UNIT_LEG_api-keys      := ./pkg/api/... ./pkg/seal/encfs/... ./pkg/seal/kdf/... ./pkg/seal/sealtest/... \
                          ./pkg/ceremony/... ./pkg/credentials/... ./pkg/firstboot/... ./pkg/pin/... \
                          ./pkg/clusterseal/... ./pkg/barrierinit/... ./pkg/authfactors/... \
                          ./pkg/keywrap/... ./pkg/dek/... ./pkg/sql/...
UNIT_LEG_sdk-transport := ./transport/...
UNIT_ROOT_LEGS := tenant-lifecycle tenant seal-barrier seal server-raft server auth service api-keys
UNIT_SDK_LEGS  := sdk-transport
UNIT_LEGS      := $(UNIT_ROOT_LEGS) rest $(UNIT_SDK_LEGS) sdk
FIPS_LEGS      := $(UNIT_LEGS)

# Helpers for the unit legs: module directory, leg check, test filter and taken packages.
unit-leg-dir = $(if $(filter $(1),$(UNIT_SDK_LEGS) sdk),$(SDK_DIR),.)
unit-leg-check = case " $(UNIT_LEGS) " in *" $(1) "*) ;; \
	*) echo "$(2) must be one of: $(UNIT_LEGS)" >&2; exit 2 ;; esac
unit-leg-filter = $(if $(UNIT_RUN_$(1)),-run '$(UNIT_RUN_$(1))') \
	$(if $(UNIT_SKIP_$(1)),-skip '$(UNIT_SKIP_$(1))')
unit-leg-taken = $(GO) list $(TAG_FLAG) $(foreach l,$(if $(filter $(1),sdk),$(UNIT_SDK_LEGS),$(UNIT_ROOT_LEGS)),$(UNIT_LEG_$(l)))

# print-fips-leg-filter prints the -run and -skip patterns FIPS_LEG passes to go test.
.PHONY: print-fips-leg-filter
print-fips-leg-filter:
	@$(call unit-leg-check,$(FIPS_LEG),FIPS_LEG)
	@printf 'run=%s\nskip=%s\n' '$(UNIT_RUN_$(FIPS_LEG))' '$(UNIT_SKIP_$(FIPS_LEG))'

.PHONY: print-fips-legs
print-fips-legs:
	@printf '%s\n' $(FIPS_LEGS)

# print-test-pkgs prints the package patterns `make test` runs.
.PHONY: print-test-pkgs
print-test-pkgs:
	@printf '%s\n' $(TEST_PKGS)

# print-unit-leg-packages prints the import paths UNIT_LEG runs.
.PHONY: print-unit-leg-packages
print-unit-leg-packages:
	@$(call unit-leg-check,$(UNIT_LEG),UNIT_LEG)
	@cd $(call unit-leg-dir,$(UNIT_LEG)) && case "$(UNIT_LEG)" in \
	rest) all=$$($(GO) list $(TAG_FLAG) $(TEST_PKGS)) && \
		taken=$$($(call unit-leg-taken,rest)) && \
		printf '%s\n' "$$all" | grep -vxF "$$taken" ;; \
	sdk) all=$$($(GO) list $(TAG_FLAG) ./...) && \
		taken=$$($(call unit-leg-taken,sdk)) && \
		printf '%s\n' "$$all" | grep -vxF "$$taken" ;; \
	*) $(GO) list $(TAG_FLAG) $(UNIT_LEG_$(UNIT_LEG)) ;; \
	esac

.PHONY: test-fips-leg
test-fips-leg:
	@$(call unit-leg-check,$(FIPS_LEG),FIPS_LEG)
	$(if $(filter rest,$(FIPS_LEG)),$(MAKE) --no-print-directory test-clusterproof)
	pkgs=$$($(MAKE) -s --no-print-directory print-unit-leg-packages UNIT_LEG=$(FIPS_LEG)) && \
		cd $(call unit-leg-dir,$(FIPS_LEG)) && $(GOTEST) $(TAG_FLAG) $(call unit-leg-filter,$(FIPS_LEG)) $$pkgs

# Unit tests of the cluster proof harness, a separate module under the clusterproof tag.
.PHONY: test-clusterproof
test-clusterproof:
	cd test/clusterproof && $(GOTEST) -tags "$(MIN_TAGS),clusterproof" ./...

# The vanguard blocker probe; run it at every vanguard release (docs/api-server/architecture.md).
.PHONY: check-vanguard-probe
check-vanguard-probe:
	cd test/vanguardprobe && $(GO) test -count=1 ./...

# make cluster-proof soaks a running cluster under faults; make cluster-proof-check re-derives a bundle's verdict. Exit 0 proven, 1 violated, 2 invalid.
CLUSTERPROOF_BIN := bin/clusterproof
export PROOF_NODES PROOF_DURATION PROOF_NEMESIS PROOF_BUNDLE PROOF_WORKLOAD PROOF_TARGETS PROOF_SEED PROOF_SEAL

.PHONY: cluster-proof-bin
cluster-proof-bin:
	cd test/clusterproof && $(GO) build -tags "$(MIN_TAGS),clusterproof" -ldflags "-X main.version=$(BUILD_VERSION) -X main.gitCommit=$(GIT_COMMIT)" -o $(CURDIR)/$(CLUSTERPROOF_BIN) ./soak/cmd/clusterproof

.PHONY: cluster-proof
cluster-proof: cluster-proof-bin
	$(CLUSTERPROOF_BIN) run

.PHONY: cluster-proof-check
cluster-proof-check: cluster-proof-bin
	$(CLUSTERPROOF_BIN) check

# BOOT-01 in container mode against PROOF_INVENTORY; PROOF_KEEP=1 leaves the cluster running.
export PROOF_INVENTORY
export PROOF_KEEP

.PHONY: cluster-proof-boot
cluster-proof-boot:
	cd test/clusterproof && $(GOTEST) -tags "$(MIN_TAGS),clusterproof,clusterproof_container" -run '^TestBOOT01$$' ./container/

# The cluster proof on one machine, as the nightly clusterproof-soak job runs it.
CLUSTERPROOF_CI_DIR ?= $(CURDIR)/build/clusterproof-ci
CLUSTERPROOF_CI_NODES := node-1=192.0.2.11:63004,node-2=192.0.2.12:63004,node-3=192.0.2.13:63004
CLUSTERPROOF_CI_NEMESIS := disk-full,kill,partition-bridge,partition-isolate-leader,partition-majority-minority,pause
CLUSTERPROOF_CI_PROOF = PROOF_NODES=$(CLUSTERPROOF_CI_NODES) \
	PROOF_TARGETS=$(CLUSTERPROOF_CI_DIR)/targets.json \
	PROOF_WORKLOAD=$(CLUSTERPROOF_CI_DIR)/workload.json \
	PROOF_SEAL=none \
	PROOF_BUNDLE=$(CLUSTERPROOF_CI_DIR)/bundle \
	PROOF_DURATION=$(or $(PROOF_DURATION),30m) \
	PROOF_NEMESIS=$(or $(PROOF_NEMESIS),$(CLUSTERPROOF_CI_NEMESIS))

# The image the CI cluster's fault commands run in.
.PHONY: cluster-proof-fault-image
cluster-proof-fault-image:
	docker build -f test/clusterproof/fault.Dockerfile -t clusterproof-fault:$(BUILD_VERSION) test/clusterproof

.PHONY: cluster-proof-ci
cluster-proof-ci:
	scripts/cluster-proof-ci.sh up $(CLUSTERPROOF_CI_DIR)
	$(MAKE) cluster-proof $(CLUSTERPROOF_CI_PROOF)
	$(MAKE) cluster-proof-check $(CLUSTERPROOF_CI_PROOF)

# Run one package's unit tests under the `make test` tags: make test-pkg PKG=./pkg/foo/...
.PHONY: test-pkg
test-pkg:
	@if [ -z "$(PKG)" ]; then \
		echo "usage: make test-pkg PKG=./pkg/foo/..."; \
		exit 1; \
	fi
	$(GOTEST) $(TAG_FLAG) $(PKG)

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

.PHONY: test-engine
test-engine:
	$(GOTEST) $(TAG_FLAG) $(PKG_ENGINE)

# Index maintenance on the Raft apply path, benchmarked against representative shard shapes.
.PHONY: bench-engine
bench-engine:
	$(GO) test $(TAG_FLAG) -run '^$$' -bench=. -benchmem $(PKG_ENGINE)

# File engine commit benchmarks; run them on a real disk (TMPDIR), not tmpfs.
.PHONY: bench-storage-file
bench-storage-file: ## Benchmark file-engine commits and the standalone write path on a real disk
	$(GO) test $(TAG_FLAG) -run '^$$' -bench=. -benchmem $(PKG_STORAGE_FILE) ./pkg/backend/

# The standalone backend's serialized write path; run with -cpu at or above the core count.
.PHONY: bench-database
bench-database:
	$(GO) test $(TAG_FLAG) -run '^$$' -bench='BenchmarkStandaloneBackend' -benchmem $(PKG_DATABASE)

# Primary-key point-statement latency over the MySQL wire against table size (test/performance/SQL_PK_LATENCY.md).
SQLPK_TAGS := codec_json raft qrdb_sql qrdb_mysql qrdb_pgsql
SQLPK_BIN  := $(CURDIR)/bin/sqlpk
SQLPK_ARGS ?=

.PHONY: bench-sqlpk
bench-sqlpk:
	$(GOBUILD) -tags "$(SQLPK_TAGS)" -o $(SQLPK_BIN)/qrdbd ./cmd/qrdbd
	$(GOBUILD) -tags "$(SQLPK_TAGS)" -o $(SQLPK_BIN)/qrdb ./cmd/qrdb
	$(GO) run -tags benchmark ./test/performance/sqlpk -qrdbd $(SQLPK_BIN)/qrdbd -qrdb $(SQLPK_BIN)/qrdb $(SQLPK_ARGS)

# bench-leasekeepalive measures LeaseKeepAlive round-trip latency against a 3-node cluster.
LEASEKEEPALIVE_ARGS ?=

.PHONY: bench-leasekeepalive
bench-leasekeepalive:
	$(GO) run -tags benchmark ./test/performance/leasekeepalive $(LEASEKEEPALIVE_ARGS)

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

# FTS_TAGS adds qrdb_fts so the real pkg/fts implementation runs under -race.
FTS_TAGS := $(strip $(BUILD_TAGS)) qrdb_fts

.PHONY: test-fts
test-fts:
	$(GOTEST) -tags "$(FTS_TAGS)" $(PKG_FTS)

# print-optional-test-tags prints `<tag> <WITH_flag>=1` for each supported default-off build tag.
.PHONY: print-optional-test-tags
print-optional-test-tags:
	@echo "qrdb_fts WITH_QRDB_FTS=1"

.PHONY: test-sql
test-sql: test-sql-provider test-sql-engine test-sql-mysql test-sql-pgsql test-sql-query test-sql-catalog test-sql-privilege test-sql-pgcatalog

# test-sdk needs TAG_FLAG because the SDK's DAO tests need a codec, and codecs are behind build tags.
.PHONY: test-sdk
test-sdk:
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) ./...

# test-sdk-leg runs one SDK_LEG of test-sdk; TEST_SDK_LEGS together are exactly `make test-sdk`.
TEST_SDK_LEGS := $(UNIT_SDK_LEGS) sdk

.PHONY: print-test-sdk-legs
print-test-sdk-legs:
	@printf '%s\n' $(TEST_SDK_LEGS)

.PHONY: test-sdk-leg
test-sdk-leg:
	@case " $(TEST_SDK_LEGS) " in *" $(SDK_LEG) "*) ;; \
		*) echo "SDK_LEG must be one of: $(TEST_SDK_LEGS)" >&2; exit 2 ;; esac
	pkgs=$$($(MAKE) -s --no-print-directory print-unit-leg-packages UNIT_LEG=$(SDK_LEG)) && \
		cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) $(call unit-leg-filter,$(SDK_LEG)) $$pkgs

# The qrdbd integration-test harness, run on its own.
.PHONY: test-testutil-qrdbdharness
test-testutil-qrdbdharness: ## Run unit tests for the shared qrdbd test harness
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) $(SDK_PKG_QRDBDHARNESS)

# E2E tests (Playwright, run in the devcontainer)

# Playwright tiers: mocked, real-backend and DOM; the last two run against a freshly built qrdbd.
QRDBD_E2E_BIN := $(CURDIR)/bin/$(SERVER_BINARY)
# The real-backend tier uses a headless build of ./cmd/qrdb.
QRDB_E2E_CLI := $(CURDIR)/bin/qrdb-cli

.PHONY: build-cli-headless
build-cli-headless:
	$(GOBUILD) -tags "qrdb_ts qrdb_vector" -o $(QRDB_E2E_CLI) ./cmd/qrdb

# Public target: build E2E container and run ALL Playwright tiers inside it.
.PHONY: test-e2e
test-e2e:
	@echo "==> Building E2E container..."
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) build --quiet e2e
	@echo "==> Running E2E tests (mocked + real-backend + DOM)..."
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e

# Internal target: executed inside the devcontainer. Not intended for host use.
# Builds qrdbd (for the real-backend + DOM tiers) and runs all three tiers.
.PHONY: _test-e2e
_test-e2e: build-server build-cli-headless build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	@echo "==> [e2e] mocked tier"
	cd cmd/qrdb/frontend && npx playwright test
	@echo "==> [e2e] real-backend tier"
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) QRDB_BIN=$(QRDB_E2E_CLI) npx playwright test --config=playwright.real.config.ts
	@echo "==> [e2e] DOM tier"
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.dom.config.ts

# Selective public target: real-backend tier only.
.PHONY: test-e2e-real
test-e2e-real:
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) build --quiet e2e
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e-real

.PHONY: _test-e2e-real
_test-e2e-real: build-server build-cli-headless build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) QRDB_BIN=$(QRDB_E2E_CLI) npx playwright test --config=playwright.real.config.ts

# Selective public target: DOM tier only.
.PHONY: test-e2e-dom
test-e2e-dom:
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) build --quiet e2e
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e-dom

.PHONY: _test-e2e-dom
_test-e2e-dom: build-server build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.dom.config.ts

.PHONY: test-playwright
test-playwright: test-e2e

# Integration tests (run in the devcontainer with qrdbd and every protocol)

# Compose project name for the integration harness, unique per run.
INT_GOAL   := $(firstword $(foreach g,integration-test-% integration-down-%,$(patsubst $(g),%,$(filter $(g),$(MAKECMDGOALS)))))
INT_JOB_ID := $(if $(GITHUB_RUN_ID),-$(GITHUB_RUN_ID)-$(or $(GITHUB_RUN_ATTEMPT),1)$(if $(INT_GOAL),-$(INT_GOAL)))
INT_PROJECT ?= $(shell printf '%s' "qrdb-int-$$(hostname | tr -cd '[:alnum:]' | tail -c 16)$(INT_JOB_ID)" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]_-')

# Integration tests run in a docker compose stack with every protocol enabled.
INT_COMPOSE := test/integration/api/docker-compose.yml

# The image a run builds and its stack runs, namespaced like INT_PROJECT.
INT_IMAGE     := go-qrdb-int:$(INT_PROJECT)
INT_GPU_IMAGE := go-qrdb-int-gpu:$(INT_PROJECT)
export QRDB_INT_IMAGE     := $(INT_IMAGE)
export QRDB_INT_GPU_IMAGE := $(INT_GPU_IMAGE)

# integration-image builds the image the stack runs, with the code under test baked in.
.PHONY: integration-image
integration-image:
	@$(stage-gomodauth) \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,env=GOMODAUTH \
	  $(SCANNER_BUILD_ARGS) \
	  --build-arg QRDB_BUILD_TAGS="$(strip $(BUILD_TAGS))" \
	  --target dev-integration \
	  -f build/docker/devcontainer/Dockerfile \
	  -t $(INT_IMAGE) .

# integration-test runs IT_CMD in the integration image under its hardened compose service and tears the project down.
IT_GOMAXPROCS ?=
IT_FEATURE ?=
IT_CMD     ?= $(if $(IT_FEATURE),$(call int_cmd,$(IT_FEATURE)),$(GO) test -count=1 -v -timeout=$(INTEGRATION_GO_TIMEOUT) -p 1 -parallel $(CLUSTER_PARALLEL) -tags "$(INT_FEATURE_TAGS)" $(INT_TEST_DIR))

.PHONY: integration-test
integration-test:
	@if [ -n "$(IT_FEATURE)" ] && [ -z "$(strip $(call int_files,$(IT_FEATURE)))" ]; then \
		echo "==> integration-test IT_FEATURE=$(IT_FEATURE): no non-Docker integration test is mapped to this feature yet (see test/integration/features.mk)"; \
		exit 0; \
	fi; \
	if [ -n "$(IT_FEATURE)" ] && [ -z '$(call int_names,$(IT_FEATURE))' ]; then \
		echo "==> integration-test IT_FEATURE=$(IT_FEATURE): mapped files contain no Test function"; \
		exit 0; \
	fi; \
	$(it-image-step); \
	echo "==> Running integration tests in $(IT_IMAGE) (project $(IT_PROJECT))..."; \
	status=0; \
	QRDB_IT_IMAGE='$(IT_IMAGE)' QRDB_IT_GOMAXPROCS='$(IT_GOMAXPROCS)' docker compose -f $(DEVCONTAINER_COMPOSE) -p '$(IT_PROJECT)' \
		run --rm integration $(IT_CMD) || status=$$?; \
	QRDB_IT_IMAGE='$(IT_IMAGE)' docker compose -f $(DEVCONTAINER_COMPOSE) -p '$(IT_PROJECT)' \
		down --volumes >/dev/null 2>&1 || true; \
	exit $$status

# integration-test-docker runs the standalone and cluster stages one after another so they never share CPU.
.PHONY: integration-test-docker
integration-test-docker: integration-test-standalone integration-test-cluster
	@echo "==> All integration stages passed (standalone + cluster)."

# Standalone stage: the multi-protocol CLI suites against the single-node docker servers.
.PHONY: integration-test-standalone
integration-test-standalone: integration-image
	@echo "==> Running STANDALONE integration tests (multi-protocol CLI vs single-node servers)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		INTEGRATION_TAGS='$(INT_FEATURE_TAGS)' \
		docker compose -p $(INT_PROJECT) up --abort-on-container-exit integration-tests; \
		rc=$$?; \
		if [ $$rc -ne 0 ]; then $(MAKE) -s -C $(CURDIR) _integration-server-logs INT_PROJECT=$(INT_PROJECT); fi; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

# Prints the server logs that --abort-on-container-exit does not show.
.PHONY: _integration-server-logs
_integration-server-logs:
	@cd test/integration/api && for svc in qrdbd qrdbd-shamir; do \
		echo "==> $$svc log (last 200 lines)"; \
		docker compose -p $(INT_PROJECT) logs --no-color --tail=200 "$$svc" || true; \
	done

# integration-down removes the run's stack and its per-run images.
.PHONY: integration-down
integration-down:
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v --remove-orphans || true
	@docker image rm $(INT_IMAGE) $(INT_GPU_IMAGE) >/dev/null 2>&1 || true

# integration-down-<feature> is integration-down for the names integration-test-<feature> derives.
.PHONY: integration-down-%
integration-down-%:
	@$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT)

# Cluster stage: the self-spawning single-node and 3-node Raft tests in ./test/integration.
INTEGRATION_RUN ?=
# The cluster stage's resource budget.
CLUSTER_CPUS ?= 8
CLUSTER_MEM_RESERVATION ?= 8g
CLUSTER_PARALLEL ?= 1
# INTEGRATION_GO_TIMEOUT is go test's -timeout for the package, read by the compose integration-tests service.
INTEGRATION_GO_TIMEOUT ?= 100m
.PHONY: integration-test-cluster
integration-test-cluster: integration-image
	@echo "==> Running CLUSTER integration tests (self-spawned single-node + 3-node clusters)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cpus=$$(./scripts/cluster-cpus.sh '$(CLUSTER_CPUS)') || exit 1; \
		cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/' \
		INTEGRATION_TAGS='$(INT_FEATURE_TAGS)' \
		INTEGRATION_RUN='$(INTEGRATION_RUN)' \
		INTEGRATION_GO_TIMEOUT='$(INTEGRATION_GO_TIMEOUT)' \
		INTEGRATION_CPUS="$$cpus" \
		INTEGRATION_MEM_RESERVATION='$(CLUSTER_MEM_RESERVATION)' \
		INTEGRATION_PARALLEL='$(CLUSTER_PARALLEL)' \
		docker compose -p $(INT_PROJECT) up --abort-on-container-exit integration-tests; \
		rc=$$?; \
		if [ $$rc -ne 0 ]; then $(MAKE) -s -C $(CURDIR) _integration-server-logs INT_PROJECT=$(INT_PROJECT); fi; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

# DAO stage: the real-binary GenericDAO suites.
DAO_RUN := ^TestDAO_
.PHONY: integration-test-dao
integration-test-dao:
	@$(MAKE) -s -C $(CURDIR) integration-test-cluster INT_PROJECT=$(INT_PROJECT) INTEGRATION_RUN='$(DAO_RUN)'

# Backward-compatible alias: the standalone multi-protocol suite.
.PHONY: integration-test-api
integration-test-api: integration-test-standalone

# GPU-accelerated integration tests.
INT_GPU_COMPOSE := test/integration/api/docker-compose.gpu.yml
HAS_GPU := $(shell command -v nvidia-smi >/dev/null 2>&1 && echo 1 || echo 0)

.PHONY: integration-test-gpu
# Builds the gpu stage, so it needs the same credential prelude as every image build.
integration-test-gpu:
	@echo "==> Running GPU integration tests (qrdbd + all protocols + NVIDIA GPU, build/docker/devcontainer/Dockerfile stage gpu)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
		docker compose -f docker-compose.yml -f docker-compose.gpu.yml -p $(INT_PROJECT) \
			up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

# Auto-detecting variant: uses GPU when nvidia-smi is in PATH, CPU otherwise.
.PHONY: integration-test-auto
integration-test-auto:
ifeq ($(HAS_GPU),1)
	@echo "==> NVIDIA GPU detected — running GPU-accelerated integration tests..."
	@$(MAKE) integration-test-gpu
else
	@echo "==> No NVIDIA GPU detected — falling back to CPU integration tests..."
	@$(MAKE) integration-test-docker
endif

# Run integration tests locally (requires running qrdbd server with all protocols).
.PHONY: integration-test-local
integration-test-local:
	$(GOTEST) -tags integration -timeout=25m ./test/integration/...

# Per-feature integration targets: make integration-test-<feature>, selected from test/integration/features.mk.
INT_FEATURE_TAGS = $(LINT_TAGS),integration

# INT_FEATURES is every feature whose features.mk entry maps at least one file.
INT_FEATURES = $(sort $(subst _,-,$(patsubst INTTEST_%,%,$(foreach v,$(filter INTTEST_%,$(.VARIABLES)),$(if $(strip $($(v))),$(v))))))

.PHONY: print-integration-features
print-integration-features:
	@printf '%s\n' $(INT_FEATURES)

# print-integration-feature-map prints each feature and the test/integration files it selects.
.PHONY: print-integration-feature-map
print-integration-feature-map:
	@$(foreach f,$(INT_FEATURES),printf '%s %s\n' '$(f)' '$(notdir $(call int_files,$(f)))';)

# integration-test-feature is the recipe behind every per-feature target: make integration-test-feature FEATURE=<docs folder name>
int_var   = $(subst -,_,$(1))
int_files = $(addprefix $(INT_TEST_DIR)/,$(strip $(INTTEST_$(call int_var,$(1)))))
int_dirs  = $(sort $(patsubst %/,%,$(dir $(call int_files,$(1)))))
INT_TEST_NAME_SED := s/^func \(Test[A-Za-z0-9_]*\)[(].*/\1/p
int_names = $(if $(call int_files,$(1)),$(shell sed -n '$(INT_TEST_NAME_SED)' $(call int_files,$(1)) | paste -sd '|' -))
int_tags  = $(INT_FEATURE_TAGS)$(if $(strip $(INTTAGS_$(call int_var,$(1)))),$(comma)$(strip $(INTTAGS_$(call int_var,$(1)))))
int_cmd   = $(INT_GOTEST) -tags "$(call int_tags,$(1))" -run '^($(call int_names,$(1)))$$' $(call int_dirs,$(1))

.PHONY: integration-test-feature
integration-test-feature:
	@test -n "$(FEATURE)" || { echo "integration-test-feature: FEATURE=<feature> is required"; exit 2; }
	@if [ -z "$(strip $(call int_files,$(FEATURE)))" ]; then \
		echo "==> integration-test-$(FEATURE): no non-Docker integration test is mapped to this feature yet (see test/integration/features.mk)"; \
		exit 0; \
	fi; \
	if [ -z '$(call int_names,$(FEATURE))' ]; then \
		echo "==> integration-test-$(FEATURE): mapped files contain no Test function"; \
		exit 0; \
	fi; \
	echo "==> integration-test-$(FEATURE): running $(subst |, ,$(call int_names,$(FEATURE)))"; \
	$(call int_cmd,$(FEATURE))

.PHONY: integration-test-%
integration-test-%:
	@$(MAKE) -s -C $(CURDIR) integration-test-feature FEATURE=$*

# integration-test-features runs every feature in IT_FEATURES and fails if any failed.
IT_FEATURES    ?=
IT_FEATURES_IN ?= host
it_features_goal.host  = integration-test-feature FEATURE
it_features_goal.image = integration-test IT_FEATURE

.PHONY: integration-test-features
integration-test-features:
	@test -n "$(strip $(IT_FEATURES))" || { echo "integration-test-features: IT_FEATURES=\"<feature> ...\" is required"; exit 2; }
	@test -n "$(it_features_goal.$(IT_FEATURES_IN))" || { echo "integration-test-features: IT_FEATURES_IN=$(IT_FEATURES_IN); want host or image"; exit 2; }
	@summary=''; failed=''; \
	for f in $(IT_FEATURES); do \
		start=$$(date +%s); \
		case ' $(INT_FEATURES) ' in \
		*" $$f "*) rc=0; $(MAKE) -s -C $(CURDIR) $(it_features_goal.$(IT_FEATURES_IN))=$$f || rc=$$?;; \
		*) echo "==> integration-test-features: $$f is not a feature with integration tests (make -s print-integration-features)"; rc=2;; \
		esac; \
		secs=$$(( $$(date +%s) - start )); \
		if [ $$rc -eq 0 ]; then res=PASS; else res=FAIL; failed="$$failed $$f"; fi; \
		echo "==> integration-test-features: $$res $$f $${secs}s"; \
		summary="$$summary  $$res $$f $${secs}s\n"; \
	done; \
	echo "==> integration-test-features summary ($(IT_FEATURES_IN)):"; \
	printf '%b' "$$summary"; \
	if [ -n "$$failed" ]; then \
		echo "==> integration-test-features: FAILED:$$failed"; \
		exit 1; \
	fi; \
	echo "==> integration-test-features: all $(words $(IT_FEATURES)) passed"

# Benchmarks (run in the devcontainer)

# Public target: build devcontainer image and run fair KV benchmarks inside it.
.PHONY: bench-qrdb-kv
bench-qrdb-kv:
	@$(stage-gomodauth) \
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

# Coverage

# check-coverage fails when a profile's total is below COVER_MIN or missing.
define check-coverage
	@COVERAGE=$$($(GO) tool cover -func=$(1) | awk '$$1 == "total:" { sub(/%$$/, "", $$NF); print $$NF }'); \
	if ! awk -v c="$${COVERAGE}" 'BEGIN { exit !(c ~ /^[0-9]+(\.[0-9]+)?$$/) }'; then \
		echo "FAIL: $(2) coverage profile $(1) has no total to compare"; \
		exit 1; \
	fi; \
	echo "$(2) coverage: $${COVERAGE}%"; \
	if awk -v c="$${COVERAGE}" -v m="$(COVER_MIN)" 'BEGIN { exit !(c + 0 < m + 0) }'; then \
		echo "FAIL: $(2) coverage $${COVERAGE}% is below minimum $(COVER_MIN)%"; \
		exit 1; \
	fi
endef

# The coverage gate: a repo-wide floor plus a per-package ratchet (test/tools/coverratchet).
COVER_BASELINE     := test/coverage-baseline.txt
COVER_TOLERANCE    := 1.0
COVER_EXCLUDE_PKGS := github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest,github.com/jeremyhahn/go-qrdb/pkg/index/indextest,github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest,github.com/jeremyhahn/go-qrdb/pkg/auth/authtest,github.com/jeremyhahn/go-qrdb/pkg/api/grpc/grpctest,github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest,github.com/jeremyhahn/go-qrdb/pkg/service/servicetest,github.com/jeremyhahn/go-qrdb/pkg/pin/pintest,github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest,github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest,github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest,github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest,github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest,github.com/jeremyhahn/go-qrdb/pkg/cert/certtest,github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest,github.com/jeremyhahn/go-qrdb/pkg/dek/dektest,github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest,github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest,github.com/jeremyhahn/go-qrdb/pkg/store/storetest,github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest,github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest,github.com/jeremyhahn/go-qrdb/pkg/database/databasetest,github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest,github.com/jeremyhahn/go-qrdb/sdk/go/transport/closedidletest
# The baseline is measured under BUILD_TAGS and records them in its `# build-tags:` line.
COVER_RATCHET       = $(GO) run ./test/tools/coverratchet -profile $(COVER_DIR)/total.out -baseline $(COVER_BASELINE) -exclude "$(COVER_EXCLUDE_PKGS)" -tags "$(strip $(BUILD_TAGS))"

# Coverage runs in legs, COVER_LEGS: make coverage-leg COVER_LEG=<leg> writes $(COVER_DIR)/leg-<leg>.out.
COVER_LEGS      := $(UNIT_LEGS)
COVER_ROOT_PKGS := ./pkg/... ./cmd/...

.PHONY: print-coverage-legs
print-coverage-legs:
	@printf '%s\n' $(COVER_LEGS)

# print-cover-root-pkgs prints the root module's package patterns the coverage suite measures.
.PHONY: print-cover-root-pkgs
print-cover-root-pkgs:
	@printf '%s\n' $(COVER_ROOT_PKGS)

# print-coverage-leg-packages prints the import paths COVER_LEG measures.
.PHONY: print-coverage-leg-packages
print-coverage-leg-packages:
	@$(call unit-leg-check,$(COVER_LEG),COVER_LEG)
	@set -e; \
	sub=$$(./scripts/coverage-merge-subprocess.sh --list); \
	cd $(call unit-leg-dir,$(COVER_LEG)); \
	case "$(COVER_LEG)" in \
	rest) all=$$($(GO) list $(TAG_FLAG) $(COVER_ROOT_PKGS)); taken=$$($(call unit-leg-taken,rest)) ;; \
	sdk) all=$$($(GO) list $(TAG_FLAG) ./...); taken=$$($(call unit-leg-taken,sdk)) ;; \
	*) all=$$($(GO) list $(TAG_FLAG) $(UNIT_LEG_$(COVER_LEG))); taken= ;; \
	esac; \
	printf '%s\n' "$$all" | grep -v -e '/proto/' -e '/sdk/go/examples/' | grep -vxF -e "$$sub" -e "$$taken" || true

# coverage-leg runs one leg under -race and writes its profile.
.PHONY: coverage-leg
coverage-leg: | _cover-dir
	@$(call unit-leg-check,$(COVER_LEG),COVER_LEG)
	@set -e; \
	pkgs=$$($(MAKE) -s --no-print-directory print-coverage-leg-packages COVER_LEG=$(COVER_LEG)); \
	if [ -z "$$pkgs" ]; then echo "coverage-leg: leg $(COVER_LEG) names no package" >&2; exit 1; fi; \
	out=$(CURDIR)/$(COVER_DIR)/leg-$(COVER_LEG).out; \
	rm -f "$$out" "$$out.part"; \
	sub_rc=0; \
	if [ "$(COVER_LEG)" = rest ]; then \
		COVERAGE_MERGE_TAGS="$(strip $(BUILD_TAGS))" COVERAGE_MERGE_TIMEOUT="$(GOTEST_TIMEOUT)" ./scripts/coverage-merge-subprocess.sh --run $(COVER_DIR)/subprocess.out > $(COVER_DIR)/subprocess.log 2>&1 & sub_pid=$$!; \
	fi; \
	rc=0; (cd $(call unit-leg-dir,$(COVER_LEG)) && $(COVER_GOTEST) $(TAG_FLAG) $(call unit-leg-filter,$(COVER_LEG)) -coverprofile="$$out.part" $$pkgs) || rc=$$?; \
	if [ "$(COVER_LEG)" = rest ]; then wait $$sub_pid || sub_rc=$$?; cat $(COVER_DIR)/subprocess.log; fi; \
	if [ $$rc -ne 0 ] || [ $$sub_rc -ne 0 ]; then echo "coverage-leg $(COVER_LEG): test run exit $$rc, subprocess run exit $$sub_rc" >&2; exit 1; fi; \
	if [ "$(COVER_LEG)" = rest ]; then tail -n +2 $(COVER_DIR)/subprocess.out >> "$$out.part"; fi; \
	mv "$$out.part" "$$out"

# COVERAGE_CONCAT joins the leg profiles into total.out after checking each.
define COVERAGE_CONCAT
	set -e; mode=; \
	rm -f $(COVER_DIR)/total.out $(COVER_DIR)/total.out.part; \
	for l in $(COVER_LEGS); do \
		f=$(COVER_DIR)/leg-$$l.out; \
		if [ ! -s "$$f" ]; then echo "coverage-merge: $$f is missing or empty: leg $$l did not pass" >&2; exit 1; fi; \
		m=$$(head -1 "$$f"); \
		case "$$m" in "mode: "*) ;; *) echo "coverage-merge: $$f has no mode header" >&2; exit 1 ;; esac; \
		if [ -z "$$mode" ]; then mode=$$m; printf '%s\n' "$$m" > $(COVER_DIR)/total.out.part; \
		elif [ "$$m" != "$$mode" ]; then echo "coverage-merge: $$f is '$$m', not '$$mode'" >&2; exit 1; fi; \
		tail -n +2 "$$f" >> $(COVER_DIR)/total.out.part; \
	done; \
	mv $(COVER_DIR)/total.out.part $(COVER_DIR)/total.out
endef

# coverage-merge gates the merged leg profiles.
.PHONY: coverage-merge
coverage-merge: | _cover-dir
	@$(COVERAGE_CONCAT)
	$(COVER_RATCHET) -min $(COVER_MIN) -tolerance $(COVER_TOLERANCE)

# _coverage-legs runs every leg and fails if any did.
.PHONY: _coverage-legs
_coverage-legs: | _cover-dir
	@rm -f $(COVER_DIR)/leg-*.out; \
	failed=; \
	for l in $(COVER_LEGS); do \
		$(MAKE) --no-print-directory coverage-leg COVER_LEG=$$l || failed="$$failed $$l"; \
	done; \
	if [ -n "$$failed" ]; then echo "coverage: failed legs:$$failed" >&2; exit 1; fi

# The package set is the one the baseline is generated from.
.PHONY: coverage
coverage: | _cover-dir
	@$(MAKE) --no-print-directory _coverage-legs
	@$(MAKE) --no-print-directory coverage-merge

# Regenerate the baseline from a fresh `make coverage` profile.
.PHONY: coverage-baseline
coverage-baseline: | _cover-dir
	@$(MAKE) --no-print-directory _coverage-legs
	@$(COVERAGE_CONCAT)
	$(COVER_RATCHET) -write

# coverage-fts measures pkg/fts with qrdb_fts against its own baseline.
FTS_COVER_BASELINE := test/coverage-baseline-fts.txt

.PHONY: coverage-fts
coverage-fts: | _cover-dir
	$(GOTEST) -tags "$(FTS_TAGS)" -coverprofile=$(COVER_DIR)/fts.out $(PKG_FTS)
	$(GO) run ./test/tools/coverratchet -profile $(COVER_DIR)/fts.out -baseline $(FTS_COVER_BASELINE) -tags "$(FTS_TAGS)" -min $(COVER_MIN) -discovery-floor 1

# Regenerate test/coverage-baseline-fts.txt after raising pkg/fts coverage.
.PHONY: coverage-fts-baseline
coverage-fts-baseline: | _cover-dir
	$(GOTEST) -tags "$(FTS_TAGS)" -coverprofile=$(COVER_DIR)/fts.out $(PKG_FTS)
	$(GO) run ./test/tools/coverratchet -profile $(COVER_DIR)/fts.out -baseline $(FTS_COVER_BASELINE) -tags "$(FTS_TAGS)" -discovery-floor 1 -write

# The coverage gate's own tests.
.PHONY: test-coverratchet
test-coverratchet:
	$(GOTEST) ./test/tools/coverratchet/...

# scripts/coverage-merge-subprocess.sh's own tests.
.PHONY: test-coverage-merge-subprocess
test-coverage-merge-subprocess:
	@GO="$(GO)" ./scripts/coverage-merge-subprocess_test.sh

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

# Gates the harness package's coverage on its own.
.PHONY: coverage-testutil-qrdbdharness
coverage-testutil-qrdbdharness: | _cover-dir ## Coverage for the shared qrdbd test harness
	cd $(SDK_DIR) && $(GOTEST) $(TAG_FLAG) -coverprofile=../../$(COVER_DIR)/testutil-qrdbdharness.out $(SDK_PKG_QRDBDHARNESS)
	$(call check-coverage,$(COVER_DIR)/testutil-qrdbdharness.out,testutil-qrdbdharness)

_cover-dir:
	@mkdir -p $(COVER_DIR)

# CI pipeline (runs in the devcontainer)

.PHONY: ci
ci:
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer make _ci

.PHONY: _ci
# _ci runs the gates, tests and the full security suite.
_ci: check-tool-versions test-tool-versions test-deps-check check-bundled-gosec test-bundled-gosec test-trivy-db check-compose-targets test-compose-targets test-cluster-cpus test-ci-image test-in-toolchain-image check-action-pins check-no-deprecated-credential-retrieve test-nightly-signal fmt vet vet-sdk vet-tags lint lint-sdk check-publication-bound test-publication-bound check-public-releasable test-public-releasable secure-analyzer test-secure-analyzer security build-server test
	@echo "CI pipeline passed"

# Linting and static analysis

# golangci-lint and go vet use the same build tags as the build.
comma      := ,
space      := $(subst ,, )
LINT_TAGS  := $(subst $(space),$(comma),$(strip $(BUILD_TAGS)))
# The same join applied to the mandatory floor. Used by SUPPORTED_TAG_SETS.
MIN_TAGS   := $(subst $(space),$(comma),$(strip $(REQUIRED_TAGS)))

# Reduced tag rungs, filtered from BUILD_TAGS.
NOMYSQL_TAGS      := $(subst $(space),$(comma),$(strip $(filter-out qrdb_mysql,$(BUILD_TAGS))))
NOPGSQL_TAGS      := $(subst $(space),$(comma),$(strip $(filter-out qrdb_pgsql,$(BUILD_TAGS))))
NOSQLFAMILY_TAGS  := $(subst $(space),$(comma),$(strip $(filter-out qrdb_mysql qrdb_pgsql qrdb_sql,$(BUILD_TAGS))))
NOETCD_TAGS       := $(subst $(space),$(comma),$(strip $(filter-out qrdb_etcd,$(BUILD_TAGS))))

# ACTIONLINT_VERSION comes from scripts/tool-pins.sh.
export ACTIONLINT_VERSION

# WORKFLOW_FILES lists the workflow files that exist.
WORKFLOW_FILES := $(wildcard .github/workflows/*.yml .github/workflows/*.yaml .gitea/workflows/*.yml .gitea/workflows/*.yaml)

# lint-workflows validates workflow syntax, expressions and runner labels.
.PHONY: lint-workflows
lint-workflows:
	@./scripts/require-tool.sh actionlint "$(ACTIONLINT_VERSION)" github.com/rhysd/actionlint/cmd/actionlint
	actionlint -oneline $(WORKFLOW_FILES)

.PHONY: lint
lint: lint-workflows
	$(GOLINT) run --output.text.print-issued-lines=false --build-tags "$(LINT_TAGS)" ./...

# ./... does not cross a module boundary, so sdk/go is linted separately.
.PHONY: lint-sdk
lint-sdk:
	cd $(SDK_DIR) && $(GOLINT) run --config $(CURDIR)/.golangci.yml --build-tags "$(LINT_TAGS)" ./...

.PHONY: vet-sdk
vet-sdk:
	cd $(SDK_DIR) && $(GOVET) $(TAG_FLAG) ./...

# lint-gates is every repository gate CI's lint job runs beside `make lint`.
LINT_GATES := test-require-tool check-bundled-gosec test-bundled-gosec \
	check-tool-versions test-tool-versions test-deps-check test-trivy-db \
	test-cluster-cpus test-ci-image test-in-toolchain-image \
	check-action-pins check-integration-feature-coverage \
	check-no-deprecated-credential-retrieve check-no-alias-language \
	check-lean-standalone \
	test-nightly-signal test-coverratchet test-coverage-merge-subprocess \
	check-publication-bound test-publication-bound test-public-releasable \
	test-touched-features test-touched-packages \
	check-iface-sweep-findings test-iface-sweep-findings check-artifact-paths \
	test-artifact-paths check-generated check-mod-tidy test-mod-tidy test-vet-modules \
	test-release-alpha test-test-cross test-check-release-artifacts test-release-smoke fmt vet test-secure-analyzer vet-sdk lint-sdk
LINT_SWEEPS := vet-tags secure-analyzer

.PHONY: lint-gates
lint-gates: $(LINT_GATES)

.PHONY: lint-sweeps
lint-sweeps: $(LINT_SWEEPS)

# Tests for scripts/require-tool.sh.
.PHONY: test-require-tool
test-require-tool:
	@./scripts/require-tool_test.sh
	@./scripts/check-trivy-version_test.sh
	@./scripts/check-trivy-version_test.sh --mutate

# secure-analyzer checks publication-bound files for internal addresses, host names and paths.
.PHONY: secure-analyzer
secure-analyzer:
	@GO="$(GO)" GOLIST="$(GO) list" ./scripts/secure-analyzer.sh --sweep "$(SUPPORTED_TAG_SETS)" "$(GORM_TAGS)"

# The analyzer's self-test against planted violations.
.PHONY: test-secure-analyzer
test-secure-analyzer:
	@GO="$(GO)" ./scripts/secure-analyzer_test.sh

.PHONY: check-publication-bound
check-publication-bound:
	@./scripts/check-publication-bound.sh

# The publication-bound guard's rejecting branches.
.PHONY: test-publication-bound
test-publication-bound:
	@./scripts/check-publication-bound_test.sh

# Tests for scripts/touched-features.sh and scripts/touched-packages.sh.
.PHONY: test-touched-features test-touched-packages
test-touched-features:
	@./scripts/touched-features_test.sh

test-touched-packages:
	@GO="$(GO)" ./scripts/touched-packages_test.sh

# check-public-releasable scans the tracked tree for internal addresses, host names and home paths.
.PHONY: check-public-releasable
check-public-releasable:
	@./scripts/check-public-releasable.sh

# Its rejecting and fail-closed branches, against throwaway repositories.
.PHONY: test-public-releasable
test-public-releasable:
	@./scripts/check-public-releasable_test.sh

# Every `uses:` in every workflow resolves to a 40-hex commit.
.PHONY: check-action-pins
check-action-pins:
	$(GOTEST) ./test/buildgates/...

# Gate over the features.mk mapping of docs folders to integration tests.
.PHONY: check-integration-feature-coverage
check-integration-feature-coverage:
	$(GOTEST) -run '^TestIntegrationFeatureCoverage' ./test/buildgates/...

# No Go file declares a []byte credential retrieval method or calls a removed store-level selector.
.PHONY: check-no-deprecated-credential-retrieve
check-no-deprecated-credential-retrieve:
	$(GOTEST) -run '^(TestNoInRepoCallerOfDeprecatedCredentialRetrieve|TestRetrieveSelectors_)' ./test/buildgates/...

# The one gate in the unit suite that reads docs/*.md.
.PHONY: check-no-alias-language
check-no-alias-language:
	$(GOTEST) -run '^TestNoLegacyOrDeprecatedAliasLanguage$$' ./test/buildgates/...

# A lean standalone embedder must not compile features it does not use.
.PHONY: check-lean-standalone
check-lean-standalone:
	$(GOTEST) -run '^TestLeanStandalone' ./test/buildgates/...

# Where the nightly's verdict is reported.
NIGHTLY_SIGNAL_SERVER ?= $(GITHUB_SERVER_URL)
NIGHTLY_SIGNAL_API    ?= $(if $(NIGHTLY_SIGNAL_SERVER),$(NIGHTLY_SIGNAL_SERVER)/api/v1,)
NIGHTLY_SIGNAL_REPO   ?= $(if $(GITHUB_REPOSITORY),$(GITHUB_REPOSITORY),jeremyhahn/go-qrdb)
NIGHTLY_SIGNAL_REF    ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
NIGHTLY_SIGNAL_TRIVY  ?= failure

# nightly-signal runs in report mode and files nothing.
.PHONY: nightly-signal
nightly-signal: ## Report what the nightly signal would open, update or close
	$(GO) run ./test/tools/nightlysignal \
		-mode report \
		-repo $(NIGHTLY_SIGNAL_REPO) \
		-api $(NIGHTLY_SIGNAL_API) \
		-ref $(NIGHTLY_SIGNAL_REF) \
		-self signal \
		-job "trivy=Trivy=$(NIGHTLY_SIGNAL_TRIVY)"

# Tests for the nightly signal's decision table, coverage floor and rendering.
.PHONY: test-nightly-signal
test-nightly-signal: ## Test the nightly signal and its workflow coverage floor
	$(GOTEST) ./test/tools/nightlysignal/...

# Every compose target names a real Dockerfile stage, checked by reading the files.
.PHONY: check-compose-targets
check-compose-targets: ## Every compose `target:` names a real Dockerfile stage
	@./scripts/check-compose-targets.sh

# Tests for check-compose-targets' rejecting branches.
.PHONY: test-compose-targets
test-compose-targets: ## Test the compose-target guard
	@./scripts/check-compose-targets_test.sh

# Tests for the cluster stage's CPU cap.
.PHONY: test-cluster-cpus
test-cluster-cpus: ## Test the cluster stage's host-bounded CPU cap
	@./scripts/cluster-cpus_test.sh

# Tests for scripts/ci-image.sh against a stub docker.
.PHONY: test-ci-image
test-ci-image: ## Test the CI image hand-off (save, load, GHCR push)
	@./scripts/ci-image_test.sh

# Tests for scripts/in-toolchain-image.sh.
.PHONY: test-in-toolchain-image
test-in-toolchain-image: ## Test that no stage container outlives its script
	@./scripts/in-toolchain-image_test.sh
	@./scripts/ci-cache-dir_test.sh

.PHONY: check-tool-versions
check-tool-versions:
	@./scripts/check-tool-versions.sh

# Tests for check-tool-versions' rejecting branches.
.PHONY: test-tool-versions
test-tool-versions:
	@./scripts/check-tool-versions_test.sh

# The gosec bundled in golangci-lint is read from the binary and compared with GOLANGCI_GOSEC_VERSION.
.PHONY: check-bundled-gosec
check-bundled-gosec: ## The gosec inside golangci-lint is the one the exclusions were measured against
	@./scripts/check-bundled-gosec.sh

# Tests for check-bundled-gosec.
.PHONY: test-bundled-gosec
test-bundled-gosec: ## Test the bundled-gosec guard
	@./scripts/check-bundled-gosec_test.sh
# Symbols no production path can reach, checked against a tracked baseline.
DEADCODE_REFLECT_ROOTS := go-qrdb/cmd/qrdb\.[A-Za-z]*Binding\.[A-Z]
DEADCODE_BASELINE      := test/tools/deadcode/baseline.txt

.PHONY: dead-code
dead-code:
	@go run ./test/tools/deadcode -mod . -name . -tags "$(LINT_TAGS)" \
		-reflect-roots '$(DEADCODE_REFLECT_ROOTS)' -baseline $(DEADCODE_BASELINE)
	@go run ./test/tools/deadcode -mod ./sdk/go -name sdk/go -library -tags "$(LINT_TAGS)" \
		-baseline $(DEADCODE_BASELINE)

# Regenerate the dead-code baseline; the commit must say why each accepted finding is legitimate.
.PHONY: dead-code-baseline
dead-code-baseline:
	@go run ./test/tools/deadcode -mod . -name . -tags "$(LINT_TAGS)" \
		-reflect-roots '$(DEADCODE_REFLECT_ROOTS)' -baseline $(DEADCODE_BASELINE) -write-baseline
	@go run ./test/tools/deadcode -mod ./sdk/go -name sdk/go -library -tags "$(LINT_TAGS)" \
		-baseline $(DEADCODE_BASELINE) -write-baseline

# Interface-method call sweep
IFACESWEEP_DIR := test/tools/ifacesweep/findings

.PHONY: iface-sweep
iface-sweep:
	@mkdir -p $(IFACESWEEP_DIR)
	@go run ./test/tools/ifacesweep -tags "$(MIN_TAGS)" -out $(IFACESWEEP_DIR)/min-tags.tsv
	@go run ./test/tools/ifacesweep -tags "$(LINT_TAGS)" -out $(IFACESWEEP_DIR)/full-tags.tsv

# The committed findings must contain only repo-relative paths.
.PHONY: check-iface-sweep-findings
check-iface-sweep-findings:
	@./scripts/check-iface-sweep-findings.sh

# Tests for check-iface-sweep-findings.
.PHONY: test-iface-sweep-findings
test-iface-sweep-findings:
	@./scripts/check-iface-sweep-findings_test.sh

# Committed generated artifacts must contain no absolute paths; add new artifacts to scripts/check-artifact-paths.sh.
.PHONY: check-artifact-paths
check-artifact-paths:
	@./scripts/check-artifact-paths.sh

# Tests for check-artifact-paths.
.PHONY: test-artifact-paths
test-artifact-paths:
	@./scripts/check-artifact-paths_test.sh

# Generated sources must match internal/scopegen's output.
.PHONY: check-generated
check-generated:
	cd sdk/go && GOWORK=off $(GO) run ./transport/embedded/internal/scopegen -check transport/embedded/database_scope_services.go -forward-check transport/embedded/replica_forward_methods.go

.PHONY: vet
vet:
	$(GOVET) $(TAG_FLAG) ./...

# Supported build-tag combinations
SUPPORTED_TAG_SETS := $(MIN_TAGS) $(MIN_TAGS),integration $(MIN_TAGS),clusterproof $(MIN_TAGS),clusterproof,clusterproof_container $(LINT_TAGS) $(LINT_TAGS),integration $(LINT_TAGS),qrdb_fts $(NOMYSQL_TAGS) $(NOMYSQL_TAGS),integration $(NOPGSQL_TAGS) $(NOPGSQL_TAGS),integration $(NOSQLFAMILY_TAGS) $(NOSQLFAMILY_TAGS),integration $(NOETCD_TAGS) $(NOETCD_TAGS),integration

# TEST_TAGS_SETS is the subset of SUPPORTED_TAG_SETS that `make test-tags` runs.
TEST_TAGS_SETS := $(LINT_TAGS) $(NOMYSQL_TAGS) $(NOPGSQL_TAGS) $(NOSQLFAMILY_TAGS) $(NOETCD_TAGS)

# TEST_TAGS_PKGS is the command surface `make test-tags` runs, plus pkg/service.
TEST_TAGS_PKGS := ./cmd/qrdbd/... ./cmd/qrdb/... ./pkg/service

# The CBOR codec compiles only under codec_cbor, so test-tags runs its packages once more with it.
CODEC_CBOR_TAGS := $(LINT_TAGS),codec_cbor
CODEC_CBOR_TEST_PKGS := ./pkg/dao ./pkg/service

# vet-tags vets every Go module in the tree under each of its tag sets.
.PHONY: vet-tags
vet-tags:
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	GOVET="$(GOVET)" GOLIST="$(GO) list" ./scripts/vet-modules.sh --vet "$(SUPPORTED_TAG_SETS)" "$(GORM_TAGS)"

# test-tags runs the daemon and CLI tests under every rung in TEST_TAGS_SETS.
.PHONY: test-tags
test-tags:
	GOTEST="$(GO) test" GOLIST="$(GO) list" ./scripts/test-tags.sh "$(TEST_TAGS_SETS)" "$(TEST_TAGS_PKGS)"
	$(GO) test -count=1 -tags=$(CODEC_CBOR_TAGS) $(CODEC_CBOR_TEST_PKGS)

# print-tag-sets prints the declared combinations, one per line.
.PHONY: print-tag-sets
print-tag-sets:
	@printf '%s\n' $(SUPPORTED_TAG_SETS)

# print-build-tags prints BUILD_TAGS, the tag set coverage is measured under.
.PHONY: print-build-tags
print-build-tags:
	@printf '%s\n' "$(strip $(BUILD_TAGS))"

# print-trivy-version prints TRIVY_VERSION for the CI trivy job.
.PHONY: print-trivy-version
print-trivy-version:
	@printf '%s\n' $(TRIVY_VERSION)

# Tests for scripts/vet-modules.sh.
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

# Release hygiene

# The nested modules are tagged in lockstep with the root module.
.PHONY: check-tag-lockstep
check-tag-lockstep:
	@./scripts/check-tag-lockstep.sh


.PHONY: test-tag-lockstep
test-tag-lockstep:
	@./scripts/check-tag-lockstep_test.sh

# VERSION, the newest CHANGELOG release heading and the git tag must agree.
RELEASE_TAG :=

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

# Tests for release-alpha; a published tag is immutable.
.PHONY: test-release-alpha
test-release-alpha:
	@./scripts/check-release-alpha_test.sh

# Security scanning

# Every scanner in `security` fails when its binary is absent.
define require-scanner
	@command -v $(1) >/dev/null 2>&1 || { \
		echo "ERROR: $(1) is not installed, and this suite does not skip scanners." >&2; \
		echo "  A scanner that did not run is not a scanner that passed (#299)." >&2; \
		echo "  Install: $(2)" >&2; \
		exit 1; \
	}
endef

# Install hints use the pinned versions from scripts/tool-pins.sh, never @latest.

# GOLANGCI_GOSEC_VERSION is the gosec bundled in golangci-lint, checked by check-bundled-gosec.
GOLANGCI_GOSEC_VERSION := v2.29.0

export GOLANGCI_LINT_VERSION
export GOSEC_VERSION
export GOVULNCHECK_VERSION
export TRIVY_VERSION
export TRIVY_IMAGE
# The compose dev services need GO_IMAGE and TRIVY_IMAGE for the Dockerfile's global FROMs.
export GO_IMAGE

# Direct docker builds pass the scanner pins explicitly.
SCANNER_BUILD_ARGS := \
  --build-arg GO_IMAGE=$(GO_IMAGE) \
  --build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
  --build-arg GOSEC_VERSION=$(GOSEC_VERSION) \
  --build-arg GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) \
  --build-arg TRIVY_VERSION=$(TRIVY_VERSION) \
  --build-arg TRIVY_IMAGE=$(TRIVY_IMAGE)

# gosec's install hint carries the same -ldflags as the images.
GOSEC_INSTALL       := go install -ldflags '-X main.Version=$(GOSEC_VERSION)' github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
GOVULNCHECK_INSTALL := go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
# trivy is copied from the official image pinned by digest, as the images install it.
TRIVY_INSTALL       := docker create --name trivy-pin $(TRIVY_IMAGE) && docker cp trivy-pin:/usr/local/bin/trivy /usr/local/bin/trivy && docker rm trivy-pin

# gosec scans with LINT_TAGS, comma-joined, over the root module; gosec-sdk scans sdk/go.
GOSEC_FLAGS := -concurrency 4 -tests -exclude-generated -tags "$(LINT_TAGS)" -exclude=$(GOSEC_EXCLUDE) -exclude-rules='_test\.go$$:G104' -severity=low -confidence=medium
.PHONY: gosec
gosec:
	$(call require-scanner,$(GOSEC),$(GOSEC_INSTALL))
	$(GOSEC) $(GOSEC_FLAGS) $(GOSEC_EXCLUDE_DIRS) ./...

# The SDK module, scanned with the same exclusions.
.PHONY: gosec-sdk
gosec-sdk:
	$(call require-scanner,$(GOSEC),$(GOSEC_INSTALL))
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	cd $(SDK_DIR) && $(GOSEC) $(GOSEC_FLAGS) ./...

# SARIF for GitHub code scanning.
.PHONY: gosec-sarif
gosec-sarif:
	$(GOSEC) $(GOSEC_FLAGS) -fmt=sarif -out=gosec.sarif $(GOSEC_EXCLUDE_DIRS) ./... || true

# govulncheck is blocking.
.PHONY: vuln
vuln:
	$(call require-scanner,$(GOVULNCHECK),$(GOVULNCHECK_INSTALL))
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	$(GOVULNCHECK) ./... && cd sdk/go && $(GOVULNCHECK) ./...

# vulncheck is an alias of vuln.
.PHONY: vulncheck
vulncheck: vuln

# Runs the Go toolchain, so no require-scanner guard.
.PHONY: mod-verify
mod-verify:
	$(GO) mod verify
	cd sdk/go && $(GO) mod verify

# Every tracked module's go.mod and go.sum match its sources.
.PHONY: check-mod-tidy
check-mod-tidy:
	@GO="$(GO)" ./scripts/check-mod-tidy.sh

.PHONY: test-mod-tidy
test-mod-tidy:
	@GO="$(GO)" ./scripts/check-mod-tidy_test.sh

# Directories excluded from the filesystem scan.
TRIVY_SKIP_DIRS := --skip-dirs test,cmd/qrdbd/data

# The oldest vulnerability database this leg certifies a tree with.
TRIVY_DB_MAX_AGE_HOURS := 24

# Download the vulnerability database, check its age, then scan with --skip-db-update.
define trivy-certified-db
	@./scripts/trivy-certified-db.sh $(TRIVY_DB_MAX_AGE_HOURS)
endef

# The blocking severity floor.
TRIVY_GATE_SEVERITY := CRITICAL,HIGH

# The scan includes dev dependencies.
TRIVY_SCOPE := --include-dev-deps

.PHONY: trivy
trivy:
	$(call require-scanner,trivy,$(TRIVY_INSTALL))
	$(trivy-certified-db)
	trivy fs --scanners vuln,secret $(TRIVY_SCOPE) --severity $(TRIVY_GATE_SEVERITY) --exit-code 1 --skip-db-update $(TRIVY_SKIP_DIRS) .

# The reporting floor
TRIVY_REPORT_SEVERITY := MEDIUM,LOW

.PHONY: trivy-report
trivy-report: ## Report sub-threshold (MEDIUM/LOW) advisories; advisory in nightly, blocks no merge
	$(call require-scanner,trivy,$(TRIVY_INSTALL))
	$(trivy-certified-db)
	trivy fs --scanners vuln,secret $(TRIVY_SCOPE) --severity $(TRIVY_REPORT_SEVERITY) --exit-code 1 --skip-db-update $(TRIVY_SKIP_DIRS) .
	@echo "== UNKNOWN band (advisory; no score assigned, often no fix available) =="
	trivy fs --scanners vuln $(TRIVY_SCOPE) --severity UNKNOWN --exit-code 0 --skip-db-update $(TRIVY_SKIP_DIRS) .

# Tests for the trivy database age check.
.PHONY: test-trivy-db
test-trivy-db: ## Test the trivy database-age guard
	@./scripts/check-trivy-db_test.sh
	@./scripts/trivy-certified-db_test.sh

# The security suite CI runs: gosec, govulncheck, go mod verify and trivy fs.
.PHONY: security
security: gosec gosec-sdk vuln mod-verify trivy
	@echo "Security suite passed: $^"

# Every direct requirement is at its latest release unless its go.mod line says why it is held.
.PHONY: deps-check
# deps-check and deps-bump use the shared depscheck tool.
DEPS_CHECK_MODULES ?= . sdk/go sdk/go/gorm/conformance test/clusterproof test/performance/cmd/kvcompare test/performance/etcd-benchmark test/performance/qrdb test/performance/tikv test/vanguardprobe
DEPS_CHECK_VULN_TAGS ?= gormconformance,clusterproof,benchmark

deps-check:
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	$(GO) run ./scripts/depscheck -go $(GO) $(if $(DEPS_CHECK_BASE),-base $(DEPS_CHECK_BASE)) -vuln -govulncheck $(GOVULNCHECK) $(if $(DEPS_CHECK_VULN_TAGS),-vuln-tags $(DEPS_CHECK_VULN_TAGS)) $(DEPS_CHECK_MODULES)

.PHONY: deps-bump
deps-bump:
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	$(GO) run ./scripts/depscheck -go $(GO) -bump $(DEPS_CHECK_MODULES)

.PHONY: test-deps-check
test-deps-check:
	$(GO) test -count=1 ./scripts/depscheck

# Docker

# docker-build-release builds the release image with the module-credential prelude.
.PHONY: docker-build-release
docker-build-release:
	@$(stage-gomodauth) \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,env=GOMODAUTH \
	  -t qrdbd:$(BUILD_VERSION) .

.PHONY: docker-run-release
docker-run-release:
	docker run --rm -p 63001:63001 -p 63002:63002 -p 63003:63003 qrdbd:$(BUILD_VERSION)

.PHONY: docker-run-devcontainer
docker-run-devcontainer:
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) run --rm --build devcontainer

# The CI image
DEVCONTAINER_DIR     := build/docker/devcontainer
DEVCONTAINER_COMPOSE := $(DEVCONTAINER_DIR)/compose.yaml
DEVCONTAINER_MODULES := . sdk/go sdk/go/gorm/conformance test/clusterproof \
	test/performance/cmd/kvcompare test/performance/etcd-benchmark \
	test/performance/qrdb test/performance/tikv test/vanguardprobe
TOOLCHAIN_TAG   = $(shell cat $(DEVCONTAINER_DIR)/Dockerfile $(TOOL_PINS) $(foreach m,$(DEVCONTAINER_MODULES),$(m)/go.mod $(m)/go.sum) | sha256sum | cut -c1-16)
TOOLCHAIN_IMAGE = qrdb-toolchain:$(TOOLCHAIN_TAG)

# The integration image and its compose project; the tag is a hash of the build inputs.
APK_INDEX_SUM ?=

# apk-index-sum prints the APK index checksum the CI jobs read.
.PHONY: apk-index-sum
apk-index-sum:
	@APK_BASE_IMAGES='$(GO_IMAGE)' scripts/apk-index-sum.sh
IT_TAG     = $(shell if git rev-parse -q --verify 'HEAD^{tree}' >/dev/null 2>&1; then \
	{ echo '$(GO_IMAGE) $(INT_FEATURE_TAGS) $(APK_INDEX_SUM)'; git rev-parse 'HEAD^{tree}'; git diff --no-ext-diff --binary HEAD; \
	  git ls-files -z -o --exclude-standard | xargs -0r sha256sum --; } | sha256sum | cut -c1-16; \
	else echo $(DOCKER_TAG); fi)
IT_IMAGE   ?= qrdb-ci-it:$(IT_TAG)
IT_PROJECT ?= qrdb-it-$(DOCKER_TAG)

# CI loads the prebuilt integration image (IT_PREBUILT=1) and never rebuilds it in a leg.
IT_PREBUILT ?=
it-require-prebuilt = docker image inspect '$(IT_IMAGE)' >/dev/null 2>&1 || { echo "ERROR: $(IT_IMAGE) is not on this daemon: a prebuilt image is loaded from docker-build's artifact (scripts/ci-image.sh load), never built" >&2; exit 1; }
# it-image-step is integration-test's image step.
it-image-step = $(if $(filter 1,$(IT_PREBUILT))$(findstring @sha256:,$(IT_IMAGE))$(filter sha256:%,$(IT_IMAGE)),$(it-require-prebuilt),$(MAKE) -s -C $(CURDIR) docker-build-it || exit $$?)

REQUIRE_BUILDX = @docker buildx version >/dev/null 2>&1 || { echo "docker buildx is required (the Dockerfile mounts the gomodauth BuildKit secret)" >&2; exit 1; }

# Image builds pass the module credentials as a BuildKit secret.
DEVCONTAINER_BUILD = DOCKER_BUILDKIT=1 docker buildx build --load \
	--secret id=gomodauth,env=GOMODAUTH \
	--build-arg GO_IMAGE=$(GO_IMAGE) \
	--build-arg TRIVY_IMAGE=$(TRIVY_IMAGE) \
	--build-arg IT_TAGS="$(INT_FEATURE_TAGS)" \
	$(if $(GO_BUILD_PROXY),--build-arg GO_BUILD_PROXY="$(GO_BUILD_PROXY)",$(if $(GOPROXY),--build-arg GO_BUILD_PROXY="$$GOPROXY")) \
	-f $(DEVCONTAINER_DIR)/Dockerfile

# concurrent-load tolerates a concurrent build of the same content-tagged image.
concurrent-load = || { status=$$?; id=$$(docker image inspect -f '{{.Id}}' '$(1)' 2>/dev/null) || exit $$status; echo "==> a concurrent build loaded $(1) first"; $(if $(2),printf '%s\n' "$$id" > '$(2)';) }

.PHONY: docker-build docker-build-toolchain docker-build-it toolchain-image docker-run
docker-build: docker-build-toolchain docker-build-it

# TOOLCHAIN_IIDFILE and IT_IIDFILE name the files each build writes its image ID to.
TOOLCHAIN_IIDFILE ?=
IT_IIDFILE ?=

# ci-image-shared-lock holds the image lock shared so a concurrent prune cannot remove the image.
ci-image-shared-lock = lock=$$(scripts/ci-image.sh lock-file 2>/dev/null) || lock=; \
	if [ -n "$$lock" ] && command -v flock >/dev/null 2>&1 && (: >>"$$lock") 2>/dev/null; then exec 9>>"$$lock" && flock -s 9; fi;

docker-build-toolchain:
	$(REQUIRE_BUILDX)
	@# ci-template v1.30: the tag is the content hash, so an image already on
	@# this daemon (built here, or loaded by scripts/ci-image.sh) is the
	@# image this tree would build. CUSTOMIZE (go-qrdb, #1609): with
	@# CI_IMAGE_REGISTRY set, the image is pulled from it before a build, by
	@# the digest recorded in CI_IMAGE_DIGEST_DIR only, and a build is pushed
	@# to it and its digest recorded. An image already held with no usable
	@# record (an earlier push failed) is pushed again (registry-push
	@# --unrecorded); no push ever fails the target. Unset, every ci-image.sh
	@# call does nothing.
	@$(ci-image-shared-lock) $(if $(TOOLCHAIN_IIDFILE),rm -f '$(TOOLCHAIN_IIDFILE)';) \
	if id=$$(docker image inspect -f '{{.Id}}' '$(TOOLCHAIN_IMAGE)' 2>/dev/null); then \
		echo "==> $(TOOLCHAIN_IMAGE) is already built on this daemon"; \
		docker tag '$(TOOLCHAIN_IMAGE)' '$(TOOLCHAIN_IMAGE)' >/dev/null 2>&1 || :; \
		$(if $(TOOLCHAIN_IIDFILE),printf '%s\n' "$$id" > '$(TOOLCHAIN_IIDFILE)';) \
		scripts/ci-image.sh registry-push --unrecorded '$(TOOLCHAIN_IMAGE)' || :; \
	elif scripts/ci-image.sh registry-pull '$(TOOLCHAIN_IMAGE)'; then \
		:; $(if $(TOOLCHAIN_IIDFILE),docker image inspect -f '{{.Id}}' '$(TOOLCHAIN_IMAGE)' > '$(TOOLCHAIN_IIDFILE)';) \
	else \
		$(stage-gomodauth) \
		$(DEVCONTAINER_BUILD) $(if $(TOOLCHAIN_IIDFILE),--iidfile '$(TOOLCHAIN_IIDFILE)') \
			--target toolchain -t $(TOOLCHAIN_IMAGE) . $(call concurrent-load,$(TOOLCHAIN_IMAGE),$(TOOLCHAIN_IIDFILE)); \
		scripts/ci-image.sh registry-push '$(TOOLCHAIN_IMAGE)'; \
	fi

# docker-build-it builds the integration image only when the daemon does not already hold IT_IMAGE.
docker-build-it:
	$(REQUIRE_BUILDX)
	@# CUSTOMIZE (go-qrdb): a digest reference names a pushed image; building one would mint a different image under its name (DS-3516)
	@case '$(IT_IMAGE)' in *@sha256:*) echo "ERROR: docker-build-it builds a tag, not $(IT_IMAGE): a digest reference names a pushed image" >&2; exit 1 ;; sha256:*) echo "ERROR: docker-build-it builds a tag, not $(IT_IMAGE): an image ID names a loaded image" >&2; exit 1 ;; esac
	@$(ci-image-shared-lock) $(if $(IT_IIDFILE),rm -f '$(IT_IIDFILE)';) \
	if id=$$(docker image inspect -f '{{.Id}}' '$(IT_IMAGE)' 2>/dev/null); then \
		echo "==> $(IT_IMAGE) is already built on this daemon"; \
		docker tag '$(IT_IMAGE)' '$(IT_IMAGE)' >/dev/null 2>&1 || :; \
		$(if $(IT_IIDFILE),printf '%s\n' "$$id" > '$(IT_IIDFILE)';) \
		scripts/ci-image.sh registry-push --unrecorded '$(IT_IMAGE)' || :; \
	elif scripts/ci-image.sh registry-pull '$(IT_IMAGE)'; then \
		:; $(if $(IT_IIDFILE),docker image inspect -f '{{.Id}}' '$(IT_IMAGE)' > '$(IT_IIDFILE)';) \
	else \
		$(stage-gomodauth) \
		$(DEVCONTAINER_BUILD) $(if $(IT_IIDFILE),--iidfile '$(IT_IIDFILE)') \
			--build-arg APK_INDEX_SUM='$(APK_INDEX_SUM)' \
			--target integration -t $(IT_IMAGE) . $(call concurrent-load,$(IT_IMAGE),$(IT_IIDFILE)); \
		scripts/ci-image.sh registry-push '$(IT_IMAGE)'; \
	fi

# docker-down-it removes a run's compose project but keeps its image.
.PHONY: docker-down-it
docker-down-it:
	@QRDB_IT_IMAGE='$(IT_IMAGE)' docker compose -f $(DEVCONTAINER_COMPOSE) -p '$(IT_PROJECT)' \
		down --volumes --remove-orphans >/dev/null 2>&1 || true

toolchain-image:
	@echo $(TOOLCHAIN_IMAGE)

.PHONY: it-image
it-image:
	@echo $(IT_IMAGE)

# docker-run runs CMD in the toolchain compose service.
CMD ?= make build
docker-run: docker-build-toolchain
	QRDB_TOOLCHAIN_TAG=$(TOOLCHAIN_TAG) QRDB_UID=$$(id -u) QRDB_GID=$$(id -g) \
		docker compose -f $(DEVCONTAINER_COMPOSE) -p qrdb-tc-$(DOCKER_TAG) run --rm toolchain sh -c '$(CMD)'

# trivy-image scans the integration image.
TRIVY_IMAGE_INPUT ?=
TRIVY_IMAGE_MODCACHE := /gomodcache
.PHONY: trivy-image
trivy-image:
	$(call require-scanner,trivy,$(TRIVY_INSTALL))
	$(trivy-certified-db)
	trivy image --severity $(TRIVY_GATE_SEVERITY) --exit-code 1 --skip-db-update --ignore-unfixed --skip-dirs $(TRIVY_IMAGE_MODCACHE) $(if $(TRIVY_IMAGE_INPUT),--input $(TRIVY_IMAGE_INPUT),$(IT_IMAGE))

# Dev Cluster (local 3-node cluster for testing)

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


# Vector GPU

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

# Embedding

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

# RAG

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

# Integration test targets for embedding, RAG and GPU

# Stacked compose files for the opt-in Ollama embedding overlay.
EMBEDDING_COMPOSE := -f docker-compose.yml -f docker-compose.embedding.yml

.PHONY: integration-test-embedding
integration-test-embedding: ## Run embedding e2e tests (opt-in Ollama overlay)
	@echo "==> Running embedding integration tests (Ollama overlay)..."
	@cd test/integration/api && docker compose $(EMBEDDING_COMPOSE) -p $(INT_PROJECT) down -v 2>/dev/null; true
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		INTEGRATION_RUN='Embedding' \
		docker compose $(EMBEDDING_COMPOSE) -p $(INT_PROJECT) up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

# Not named integration-test-rag, which would shadow the integration-test-% pattern rule.
.PHONY: integration-test-rag-ollama
integration-test-rag-ollama: ## Run RAG e2e tests (opt-in Ollama overlay)
	@echo "==> Running RAG integration tests (Ollama overlay)..."
	@cd test/integration/api && docker compose $(EMBEDDING_COMPOSE) -p $(INT_PROJECT) down -v 2>/dev/null; true
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
		INTEGRATION_TEST_PKGS='./test/integration/api/...' \
		INTEGRATION_RUN='RAG' \
		docker compose $(EMBEDDING_COMPOSE) -p $(INT_PROJECT) up --build --abort-on-container-exit integration-tests; \
		rc=$$?; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

# Combined new-package targets

.PHONY: test-all-new
test-all-new: test-embedding test-rag test-rag-chunker coverage-vector-gpu ## Run all new-package tests and GPU coverage

# Vector flat index

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


# Scalar index

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

# Vector quantization

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

# Shared k-means (pkg/vector/kmeans).

.PHONY: test-vector-kmeans
test-vector-kmeans: ## Run shared k-means unit tests
	$(GOTEST) $(TAG_FLAG) ./pkg/vector/kmeans/...

.PHONY: bench-vector-kmeans
bench-vector-kmeans: ## Run shared k-means benchmarks
	$(GO) test $(TAG_FLAG) -bench=. -benchmem -count=3 ./pkg/vector/kmeans/...

.PHONY: coverage-vector-kmeans
coverage-vector-kmeans: | _cover-dir ## Generate shared k-means coverage report
	$(GOTEST) $(TAG_FLAG) -coverprofile=$(COVER_DIR)/vector-kmeans.out ./pkg/vector/kmeans/...
	$(call check-coverage,$(COVER_DIR)/vector-kmeans.out,vector-kmeans)

# Bloom filter

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

# GORM conformance suite

GORM_DIR  := sdk/go/gorm/conformance
# GORM_TAGS is gormconformance plus the tags it needs to build.
GORM_TAGS := gormconformance,codec_json,raft,qrdb_sql,qrdb_embedded

.PHONY: test-gorm
test-gorm: ## Compile-check the GORM conformance harness (does not run the suite)
	cd $(GORM_DIR) && go vet -tags "$(GORM_TAGS)" ./...

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
conformance-test-gorm-node: gorm-suite-sync ## Run+reconcile the GORM suite with the stock MySQL dialector over a complete in-process node
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

# Help

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
	@echo "  WITH_QRDB_VLOG=0       Disable verifiable-log head signing"
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
	@echo "  integration-test           The CI stage: every self-spawned suite in the integration image (IT_FEATURE=<f> for one)"
	@echo "  integration-test-features  Each feature in IT_FEATURES=\"<f> ...\", past a failing one (IT_FEATURES_IN=host|image)"
	@echo "  integration-test-docker    The Docker api suites: auth, standalone and cluster stacks"
	@echo "  integration-test-api       The standalone multi-protocol suite (integration-test-standalone)"
	@echo "  integration-test-gpu       Run GPU-accelerated integration tests"
	@echo "  integration-test-embedding Run embedding integration tests"
	@echo "  integration-test-rag-ollama Run RAG e2e tests (Ollama overlay)"
	@echo "  integration-test-<feature> Run one feature's non-Docker tests (test/integration/features.mk)"
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
	@echo "                         refs, prunes nothing), then cuts signed tags for"
	@echo "                         the root and every sdk/ module (VERSION is never"
	@echo "                         written here; it must already be on HEAD), and"
	@echo "                         pushes exactly those refs atomically."
	@echo "  check-tag-lockstep     Audit root and nested-module tags for lockstep"
	@echo "  check-version-reconciled [RELEASE_TAG=vX.Y.Z-alpha]"
	@echo "                         Reconcile VERSION, CHANGELOG and tag"
	@echo "  test-release-alpha     Regression tests for the release-alpha recipe"
	@echo "  test-tag-lockstep      Regression tests for the lockstep check"
	@echo "  test-version-reconciled  Regression tests for the reconciliation check"
	@echo ""
	@echo "Security:"
	@echo "  security               Blocking suite: gosec, govulncheck, go mod verify, trivy"
	@echo "  deps-check             Fail on a direct dependency behind its latest release without a go.mod reason"
	@echo "  gosec                  Run gosec security scanner"
	@echo "  vuln                   Run govulncheck vulnerability scanner"
	@echo "  vulncheck              Alias for vuln (root + sdk/go)"
	@echo "  trivy                  Blocking trivy leg (CRITICAL/HIGH)"
	@echo "  trivy-report           Sub-threshold advisories (MEDIUM/LOW + UNKNOWN); blocks no merge"
	@echo ""
	@echo "Dev Cluster:"
	@echo "  dev-cluster              Start a local 3-node cluster"
	@echo "  dev-cluster-stop         Stop the local dev cluster"
	@echo "  dev-cluster-clean        Stop and remove dev cluster data"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build                 Build the CI toolchain and integration images (build/docker/devcontainer)"
	@echo "  docker-run CMD=...           Run CMD in the toolchain compose service (checkout bind-mounted)"
	@echo "  toolchain-image              Print the content-hashed toolchain image tag"
	@echo "  trivy-image                  Scan the integration image (IT_IMAGE) with trivy"
	@echo "  docker-down-it               Remove an integration-test run's stack, keeping its image (IT_PROJECT)"
	@echo "  it-image                     Print the integration image's content-addressed name (IT_IMAGE)"
	@echo "  docker-build-release         Build production image"
	@echo "  docker-run-release           Run production container"
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

# SUBMODULE_TAGS is discovered from the module graph, not declared.
override SUBMODULE_TAGS := $(shell ./scripts/lockstep-modules.sh 2>/dev/null)
# Builds never resolve through a workspace.
export GOWORK=off

# deps-latest picks the highest tag including -alpha prereleases; release-alpha publishes this repository's next -alpha tag.

.PHONY: release-alpha deps-latest

# release-alpha cuts the next -alpha tag for the root module and its lockstep submodules.
VERSION        :=
AGENT_GIT      := agent-git.sh
REPO_NAME      := go-qrdb
RELEASE_REMOTE := dev

release-alpha: override export REL_VERSION        := $(value VERSION)
release-alpha: override export REL_REMOTE         := $(value RELEASE_REMOTE)
release-alpha: override export REL_REPO           := $(value REPO_NAME)
release-alpha: override export REL_AGENT_GIT      := $(value AGENT_GIT)
release-alpha: override export REL_SUBMODULE_TAGS := $(value SUBMODULE_TAGS)

# Record each release input's origin so values from the environment or make -e are refused.
release-alpha: override export REL_ORIGIN_VERSION := $(origin VERSION)
release-alpha: override export REL_ORIGIN_REMOTE  := $(origin RELEASE_REMOTE)
release-alpha: override export REL_ORIGIN_REPO    := $(origin REPO_NAME)
release-alpha: override export REL_ORIGIN_AGENT   := $(origin AGENT_GIT)
release-alpha: override export REL_ORIGIN_SUBMOD  := $(origin SUBMODULE_TAGS)

# check-tag-lockstep is the offline pre-check before the network.
release-alpha: check-tag-lockstep
	@for o in "VERSION=$$REL_ORIGIN_VERSION" "RELEASE_REMOTE=$$REL_ORIGIN_REMOTE" "REPO_NAME=$$REL_ORIGIN_REPO" "AGENT_GIT=$$REL_ORIGIN_AGENT" "SUBMODULE_TAGS=$$REL_ORIGIN_SUBMOD"; do \
	  case "$${o#*=}" in \
	    environment|"environment override") echo "ERROR: $${o%%=*} is coming from the ENVIRONMENT, not from this Makefile or the command line. That happens under make -e, including an inherited MAKEFLAGS=-e that nobody typed, and it lets an exported value choose the version or the push destination for a release that cannot be retracted. Pass release inputs on the command line: make release-alpha VERSION=vX.Y.Z-alpha"; exit 1 ;; \
	  esac; \
	done
	@test -n "$$REL_VERSION" || { echo 'ERROR: state the version explicitly: make release-alpha VERSION=vX.Y.Z-alpha (release-versioning.md decides patch-vs-minor - the recipe refuses to guess)'; exit 1; }
	@printf '%s' "$$REL_VERSION" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-alpha$$' || { echo "ERROR: VERSION is not of the form vX.Y.Z-alpha (no leading zeroes): $$REL_VERSION"; exit 1; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "ERROR: releases are cut from main; you are on $$(git rev-parse --abbrev-ref HEAD). Pushing HEAD would publish this branch."; exit 1; }
# The working tree must be clean before anything with a side effect.
	@test -z "$$(git status --porcelain)" || { echo 'ERROR: working tree not clean'; exit 1; }
	@HEAD_VERSION=$$(git show HEAD:VERSION 2>/dev/null | tr -d '[:space:]'); \
	test "$$HEAD_VERSION" = "$${REL_VERSION#v}" || { echo "ERROR: VERSION on HEAD names $${HEAD_VERSION:-nothing}, not $${REL_VERSION#v}; merge the VERSION/CHANGELOG bump first. VERSION is authoritative: this recipe tags the commit that names the release and never writes VERSION itself"; exit 1; }
	@test "$$(git cat-file commit HEAD | grep -c '^gpgsig')" -eq 1 || { echo 'ERROR: HEAD names this release but carries no SSH signature. main enforces signed commits, so an unsigned HEAD means this clone is not what main holds'; exit 1; }
	@test -n "$$REL_REPO" || { echo 'ERROR: REPO_NAME is empty - set it to the literal repo name, never the checkout directory name'; exit 1; }
	@printf '%s' "$$REL_REPO" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$$' || { echo "ERROR: REPO_NAME is not a plain repo name: $$REL_REPO"; exit 1; }
	@command -v "$$REL_AGENT_GIT" >/dev/null 2>&1 || { echo "ERROR: AGENT_GIT does not resolve to an executable: $$REL_AGENT_GIT. Every git write in this recipe signs through it, and a bad path must fail before VERSION is written. Put agent-git.sh on PATH or pass AGENT_GIT=/path/to/agent-git.sh"; exit 1; }
	@test -n "$$REL_REMOTE" || { echo 'ERROR: RELEASE_REMOTE is empty - set it to the Gitea remote'; exit 1; }
# The release remote must resolve and must not be GitHub.
	@EFF_URLS=$$(git remote get-url --all "$$REL_REMOTE" 2>/dev/null) || { echo "ERROR: no git remote named $$REL_REMOTE. version-control.md names the Gitea remote dev; if this clone calls it something else pass RELEASE_REMOTE=<name>. The duplicate-tag check cannot answer against a remote that does not resolve, and refusing here is the difference between a clear error and a release that half-happens"; exit 1; }; \
	EFF_PUSH=$$(git remote get-url --push --all "$$REL_REMOTE" 2>/dev/null) || { echo "ERROR: could not read the push URLs of $$REL_REMOTE. Refusing to publish to a remote this recipe cannot fully inspect"; exit 1; }; \
	CFG_URLS=$$(git config --get-all "remote.$$REL_REMOTE.url" 2>/dev/null || true); \
	CFG_PUSH=$$(git config --get-all "remote.$$REL_REMOTE.pushurl" 2>/dev/null || true); \
	test -n "$$CFG_PUSH" || CFG_PUSH="$$CFG_URLS"; \
	case "$$(printf '%s %s %s %s' "$$CFG_URLS" "$$CFG_PUSH" "$$EFF_URLS" "$$EFF_PUSH" | LC_ALL=C tr '[:upper:]' '[:lower:]')" in \
	  *github.com*) echo "ERROR: $$REL_REMOTE points at github.com. Configured: [$$CFG_URLS] push [$$CFG_PUSH]; after url.<base>.insteadOf rewriting: [$$EFF_URLS] push [$$EFF_PUSH]. BOTH views are checked - see the comment above. AGENTS.md requires explicit approval before any push to github.com, and a release tag is permanent and public the moment it lands - there is no retraction to fall back on. Releases are published to the Gitea remote; a GitHub publication is a separate, deliberate, approved step and never a side effect of RELEASE_REMOTE"; exit 1 ;; \
	esac; \
	test "$$CFG_URLS" = "$$CFG_PUSH" || { echo "ERROR: $$REL_REMOTE fetches from [$$CFG_URLS] but pushes to [$$CFG_PUSH]. When a push cannot be confirmed, this recipe asks the remote what it holds - and that query reads the FETCH url. On a split remote it would be interrogating one server about what it wrote to another, and could answer 'nothing was published' about a live release. Point both at the same place, or release through a remote that does"; exit 1; }
# Fetch tags before auditing them.
	@echo "release-alpha: fetching tags from $$REL_REMOTE into this clone. This ADDS refs and prunes nothing; the lockstep audit below is only as good as the tag set this clone can see."
	@git fetch --quiet --tags "$$REL_REMOTE" || { echo "ERROR: could not fetch tags from $$REL_REMOTE. The lockstep audit is only as good as the tag set this clone can see, and a stale clone hides a skew that is already published"; exit 1; }
# The historical audit runs at the script's default.
	@./scripts/check-tag-lockstep.sh || { echo "ERROR: the published tag set is not in lockstep. This is the audit that matters: the prerequisite ran before the fetch above and saw only this clone's tags. Fix the published skew before cutting a release on top of it"; exit 1; }
	@REMOTE_MAIN=$$(git ls-remote "$$REL_REMOTE" refs/heads/main 2>/dev/null | awk '{print $$1}'); \
	test -n "$$REMOTE_MAIN" && test "$$REMOTE_MAIN" = "$$(git rev-parse HEAD)" || { echo "ERROR: HEAD $$(git rev-parse --short HEAD) is not what $$REL_REMOTE main holds ($${REMOTE_MAIN:-unreadable}). A release tags the reviewed VERSION/CHANGELOG commit once it is merged: merge the bump first, then release from that exact commit"; exit 1; }
# Re-check the discovered nested modules against the filesystem; an empty discovery is refused.
	@test -n "$$REL_SUBMODULE_TAGS" || { echo 'ERROR: no nested modules were discovered under sdk/. A release with no lockstep leg is exactly the skew this recipe exists to prevent, so an empty discovery is refused rather than read as "there are none"'; exit 1; }
	@case " $$REL_SUBMODULE_TAGS " in \
	  *" sdk/go "*) : ;; \
	  *) echo "ERROR: the discovered lockstep set does not contain sdk/go: $$REL_SUBMODULE_TAGS. sdk/go is this repository's published SDK and every release has carried its tag; a discovery that lost it is broken, not a repository that stopped having one"; exit 1 ;; \
	esac
	@for s in $$REL_SUBMODULE_TAGS; do \
	  printf '%s' "$$s" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._/-]*$$' || { echo "ERROR: discovered module path is not a plain path: $$s"; exit 1; }; \
	  test -f "$$s/go.mod" || { echo "ERROR: $$s was discovered as a lockstep module but has no go.mod now. Discovery runs at parse time; this is the check that it still describes the tree being tagged"; exit 1; }; \
	done
# Every discovered module must be declared releasable against an existing version or the one being cut.
	@for s in $$REL_SUBMODULE_TAGS; do \
	  b=$$(./scripts/lockstep-modules.sh --baseline "$$s" . 2>/dev/null) || { echo "ERROR: $$s is discovered as a lockstep module but is not declared in lockstep-baseline. An undeclared module would be tagged without anyone having decided it should be released. Add a line for it - a version, or 'unreleased'"; exit 1; }; \
	  test "$$b" != unreleased || { echo "ERROR: $$s is declared 'unreleased' in lockstep-baseline, so this release will not cut a tag for it - and a release that silently omits a discovered module is the skew this recipe exists to prevent. Decide: set its baseline to the version being cut, or remove the module from sdk/. For sdk/go/gorm/conformance that decision is go-qrdb#50's"; exit 1; }; \
	  test "$$b" = "$$REL_VERSION" || git rev-parse -q --verify "refs/tags/$$b" >/dev/null || { echo "ERROR: $$s has baseline $$b in lockstep-baseline, but no root tag $$b exists and it is not the version being cut ($$REL_VERSION). The audit would report that module 'pending' and skip its history scan FOREVER, so every later release could omit its counterpart without failing. A baseline may name a tag that does not exist ONLY when it is the release being cut - that is how a module's first release works"; exit 1; }; \
	done
# Each lockstep submodule must pass go mod tidy -diff before it is tagged.
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
# Everything from here to the push is one shell, so it runs as one transaction.
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
	  echo '  Nothing was published. Every tag this run created has been removed and the tree is back where it started, so fixing the cause and re-running the target is the whole recovery.' >&2; \
	  exit 1; \
	}; \
	test "$$(git show HEAD:VERSION | tr -d '[:space:]')" = "$${REL_VERSION#v}" || fail "ERROR: HEAD no longer names $$REL_VERSION - refusing to tag"; \
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
	if "$$REL_AGENT_GIT" "$$REL_REPO" push --atomic "$$REL_REMOTE" $$REFS; then \
	  echo "Released $$REL_VERSION on $$REL_REMOTE (merged release commit $$REL_COMMIT; annotated signed tags:$$TAGS; atomic push of exactly those tags)"; \
	  exit 0; \
	fi; \
	echo 'WARNING: the push client reported failure. That does not say WHICH outcome occurred, so the remote is being asked what it actually holds before anything is touched.' >&2; \
	PROBE=$$(git ls-remote "$$REL_REMOTE" $$REFS 2>/dev/null) || PROBE='@@unqueryable@@'; \
	if [ "$$PROBE" = '@@unqueryable@@' ]; then \
	  echo "ERROR: the push to $$REL_REMOTE failed AND the remote cannot be queried, so whether this release was published is UNKNOWN. Nothing has been rolled back on purpose: the tags$$TAGS are intact locally, because deleting the only local copy of a release that may already be live and immutable is worse than leaving it. Restore access and compare: git ls-remote $$REL_REMOTE$$REFS" >&2; \
	  exit 1; \
	fi; \
	CHECK=""; \
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
	  echo "Released $$REL_VERSION on $$REL_REMOTE (merged release commit $$REL_COMMIT; annotated signed tags:$$TAGS; the push client errored after the remote had applied every tag)"; \
	  exit 0; \
	fi; \
	if [ "$$MATCH" -eq 0 ]; then \
	  fail "ERROR: the push to $$REL_REMOTE failed and the remote carries none of this release - established by asking it what it holds, not by reading the client's exit code"; \
	fi; \
	printf 'ERROR: the push to %s failed and the remote is in a MIXED state, which --atomic is meant to make impossible. NOTHING HAS BEEN ROLLED BACK: the tags%s are intact locally, because part of this release may already be published and immutable. Reconcile by hand and do not re-run until you have.%b\n' "$$REL_REMOTE" "$$TAGS" "$$DETAIL" >&2; \
	exit 1

deps-latest:
	@GOWORK=off sh -c ' \
	SELF=$$(go list -m); \
	for m in $$(go list -m -f "{{if not .Indirect}}{{.Path}}{{end}}" all 2>/dev/null | grep "^github.com/jeremyhahn/" | grep -vx "$$SELF"); do \
	  case "$$(go list -m -f "{{if .Replace}}{{.Replace.Path}}{{end}}" "$$m" 2>/dev/null)" in .*|/*) continue;; esac; \
	  V=$$(go list -m -versions "$$m" 2>/dev/null | tr " " "\n" | tail -1); \
	  if [ -n "$$V" ] && [ "$$V" != "$$m" ]; then go get "$$m@$$V" || exit 1; fi; \
	done; go mod tidy'
