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

# GOSEC EXCLUSIONS, EACH WITH WHAT IT IS WORTH. Re-measured 2026-09-10 against
# gosec v2.29.0 (GOSEC_VERSION) on both modules, with the corrected comma-joined
# -tags this file now passes (#295). The method is the one the issue asked for
# and anyone can repeat: arm ONE rule at a time and count.
#
#   gosec -tags "$(LINT_TAGS)" -include=<RULE> \
#         -exclude-dir=test/performance -severity=medium -confidence=medium ./...
#
# An entry that changes no count is INERT and was DELETED, not rescoped. An
# exclusion that fires on nothing is worse than an unexamined one: it reads as
# "the gate considered this class and cleared it", and re-running the tool
# cannot tell you otherwise, because a suppression and an inert rule look
# identical in the output. That is how the .golangci.yml G703 entry survived
# for months while suppressing nothing (#181).
#
#   rule   make gosec   make gosec-sdk   kept
#   ----   ----------   --------------   ----
#   G115      440            148          yes
#   G304       48              0          yes
#   G704        2              0          yes
#   G103        0              0          no -- 8 findings, all LOW
#   G104        0              0          no
#   G118        0              0          no -- 5 sites carry #nosec G118
#   G122        0              0          no -- 2 sites carry #nosec G122
#   G705        0              0          no -- 1 site carries #nosec G705
#
# THE THREE THAT ARE KEPT:
#
#   G115: integer overflow in conversions. Protocol-spec bounded shard/replica
#         IDs and length prefixes; 440 sites in the root module and 148 in
#         sdk/go, which is why this is a list entry and not 588 annotations.
#   G304: file path via variable. NOT a claim that every path is safe -- it is
#         the residue of a repo-wide audit (#181). THE PREDICATE it used, so
#         the next reader can re-derive rather than trust it:
#
#           a G304/G703 finding is a REAL containment defect iff a value an
#           unprivileged remote caller can choose -- one entering the process
#           through an API request (r.PathValue, r.URL.Path/Query, r.FormValue,
#           r.Header.Get, any field decoded from a REST/gRPC/QUIC/unix/MCP
#           request body, a KV key, or SQL text) rather than through
#           operator-controlled config, a CLI flag, or the process environment
#           -- can reach the path argument of a filesystem operation without a
#           separator-aware containment check against a fixed root.
#
#         48 sites were enumerated and classified against it and NONE satisfies
#         it: 30 in cmd/ (26 CLI flags and positional arguments, 4 TLS material
#         paths in cmd/qrdbd/tls.go), 9 more TLS material paths in
#         pkg/clustertls, 4 in pkg/database (catalog paths built from a name
#         that passed namePattern), 2 audit-log paths, and one each in
#         pkg/storage/file, pkg/vector/ivf and pkg/system. FOUR OF THEM ARE
#         TAG-GATED and this target could not see any of them until the -tags
#         fix below: cmd/qrdb/{bindings_server,timeseries_enabled,
#         vector_enabled}.go and pkg/vector/ivf/index.go.
#
#         THIS LEG IS STILL BLANKET FOR G304 AND THE LINT LEG IS NOT. gosec's
#         CLI takes no per-path rule scoping through -exclude, so narrowing
#         here would mean 48 inline `#nosec` annotations -- and a `#nosec` line
#         is the easiest thing in this repository to copy onto a new site.
#         .golangci.yml carries the narrowing instead: there G304 is suppressed
#         per-file at exactly these packages and stays ARMED over every
#         request-facing one, so a new file-open site in pkg/api goes red in
#         the Lint job.
#   G704: SSRF taint analysis. Two sites, both the leader-forwarding client in
#         pkg/api/rest/handlers.go, whose target URL comes from the internal
#         peer registry rather than from a request.
#
# THE FIVE THAT WERE DELETED, and what makes each zero -- because "it measured
# zero" is not the same claim as "it can never fire", and a reader deciding
# whether to re-add one needs to know which:
#
#   G103  8 findings exist (pkg/index/metadata.go, generated .pb.go) but every
#         one is LOW severity, and this leg runs at -severity=medium. The
#         exclusion was doing nothing the severity floor was not already doing.
#         Lower that floor and these appear -- which is the correct outcome:
#         they would then be audited rather than silently pre-cleared.
#   G104  no findings at all, at any severity. gosec reports unchecked errors
#         only in audit mode, which this leg does not enable.
#   G118  5 findings, every one already suppressed by an inline `#nosec G118`
#         carrying its reason at the site (pkg/auth/login.go, pkg/service,
#         pkg/api/{grpc,quic,mcp}). The list entry was a second suppression
#         over the same five lines.
#   G122  same shape: pkg/storage/file/iterator.go carries `#nosec G122` at
#         both WalkDir callbacks.
#   G705  same shape: pkg/api/rest/handlers.go:544 carries `#nosec G705`.
#
# Deleting the three annotation-shadowed entries is what MAKES the annotations
# load-bearing. Before, removing a `#nosec G118` changed nothing, because the
# list still covered it; now it reddens this leg. That is #181's rule -- the
# justification lives at the site -- turned from a convention into a gate.
#
# WHAT -tests WOULD DO TO THIS TABLE, measured 2026-09-10 so the next person to
# reach for the flag does not have to (#395). It is NOT on -- the `gosec` target
# below records why -- but the numbers are the reason the table above is still
# the whole truth about what this leg suppresses:
#
#   G115   440 -> 599 sites (161 more, all in root test files)
#   G304    48 ->  87 sites root, and 0 -> 6 in sdk/go
#   G704     2 ->   2
#
# THE SDK COLUMN IS THE ONE TO NOTICE. G304 measures 0 on sdk/go today, which
# is what makes this entry inert there; with -tests it is 6, all _test.go files
# opening a path under their own t.TempDir(). #395 asked for exactly this check
# -- an entry that was inert need not stay inert -- and this is the entry that
# moves. G122 moves the same way (0 -> 6) and is held at 0 by the inline
# annotations added at those six sites rather than by any list entry.
#
# Read a -tests total with the double-analysis in mind: gosec analyses a package
# that has tests TWICE, once as the package and once as its test variant, and
# reports a finding in a NON-TEST file once per analysis. The site counts above
# collapse duplicate (file, line, rule) tuples; the raw printed totals are
# roughly double for the rules that fire in non-test code (G115 prints 1044).
GOSEC_EXCLUDE := G115,G304,G704

# DIRECTORIES THE ROOT-MODULE SCAN DOES NOT ENTER. One variable, consumed by
# `gosec` AND `gosec-sarif`, so the gate and the dashboard cannot disagree
# about what was skipped -- the drift behind #293, #397 and #395 was a flag
# that reached one invocation and not the other, and two literals here is how
# that happens again.
#
#   test/performance        its own modules, pinned to RELEASED go-qrdb
#                           versions with no `replace`, so scanning them from
#                           here judges released code against this checkout's
#                           tags. Pre-existing.
#
#   pkg/secure/analyzer/testdata   analyzer FIXTURES (#429). Excluded because
#                           gosec CANNOT TYPECHECK THEM CORRECTLY, not because
#                           their findings are unwelcome.
#
# The fixture tree is GOPATH-shaped: `testdata/src/<import path>/...` holds
# SHIM packages standing in for real dependencies -- memguard, x/crypto/ssh,
# spf13/pflag. `analysistest` is the only reader that resolves them, because it
# sets GOPATH=testdata and GO111MODULE=off. Every module-mode typechecker,
# gosec included, resolves `github.com/spf13/pflag` to the REAL module and then
# reads the fixture against an API it was never written for. The result is a
# parse failure, and gosec exits NONZERO on one even with `Issues : 0` -- which
# is what turned a green security gate red on a commit that added no code to
# any binary.
#
# THIS IS LATENT FOR THE WHOLE FIXTURE TREE, not only the file that surfaced
# it. The older shims typecheck against the real modules by COINCIDENCE: their
# signatures happen to match. Any shim that narrows, widens or invents a symbol
# breaks this scan, so the durable fix is the directory, not that one file.
#
# NOTHING HERE IS COMPILED INTO A BINARY OR REACHABLE AT RUNTIME. The Go tool
# never matches a `testdata` element in `./...`, which is exactly why `go vet`,
# `vet-tags`, `lint` and `lint-sdk` were all green on the same commit: gosec is
# the only analyser in this file that walks the filesystem itself. That
# asymmetry is the whole explanation for why this surfaced in one gate.
#
# THE PATH IS THE NARROWEST ONE THAT WORKS: pkg/secure/analyzer/testdata, not
# pkg/secure/analyzer and certainly not pkg/secure. The analyzer's own source
# and its tests are still scanned. Measured: it is also the ONLY testdata
# directory in the repository, and sdk/go has none -- which is why gosec-sdk
# takes no -exclude-dir (see the comment there).
GOSEC_EXCLUDE_DIRS := -exclude-dir=test/performance -exclude-dir=pkg/secure/analyzer/testdata

# Coverage
COVER_MIN     := 91

# Docker
DOCKER_IMAGE  := go-qrdb-dev
DOCKER_TAG    := latest
COMPOSE_FILE  := .devcontainer/docker-compose.yml

# ---------------------------------------------------------------------------
# Module credentials for EVERY entrypoint that builds .devcontainer/Dockerfile
# ---------------------------------------------------------------------------
# The forge's address, in ONE place. It moved once already (2026-09-07,
# 198.51.100.91 -> 198.51.100.50) and broke every devcontainer target that
# resolves a first-party module, because the same URL was written out three
# times across this file and .devcontainer/docker-compose.yml. Compose reads the
# same name with a ":-" default, and that default is load-bearing -- #322
# measured that an interpolated compose value with nothing behind it expands to
# the EMPTY STRING and overrides the fallback it was meant to have. Override the
# variable; do not edit the literal.
GITEA_HOST ?= git.example.internal

# go.mod pins first-party modules published only to the internal Gitea, and a
# docker build inherits nothing from its caller's environment. The four
# variables from dependency-resolution.md section 6 therefore reach the single
# `go mod download` as a BuildKit secret.
#
# Never a build arg -- an arg is recoverable from image metadata with
# `docker history`, while a secret is a tmpfs mount for that one RUN.
#
# THE SECRET IS SOURCED FROM THE ENVIRONMENT, NOT FROM A FILE (#326).
#
# It used to be a `mktemp /tmp/qrdb-gomodauth.XXXXXX` removed by an EXIT trap.
# A make recipe shell does NOT run EXIT traps when it dies on SIGKILL -- OOM,
# `kill -9`, a session-scoped stop, or a harness tool timeout -- and all three
# have happened to gate wrappers on this host. A file dated 2026-09-04 20:43
# was found on 2026-09-05 09:20 holding the live token: mode 600, but twelve
# hours on a world-listable directory that every backup, sync and debugging
# `tar` picks up first, under a name that says exactly what it is.
#
# `--secret id=gomodauth,env=GOMODAUTH` removes the file, the trap and the
# question. Nothing is written to disk, so there is nothing for a SIGKILL to
# leave behind -- this is not a shorter window, it is no window. Compose reads
# the same variable through `secrets: gomodauth: environment: GOMODAUTH`.
#
# MEASURED before adopting, because the issue made it conditional on the
# BuildKit floor (option 2 "if the floor allows it, else 1"):
#   workstation      docker 29.1.3, compose 2.40.3
#   gpu-host (runner) docker 29.1.3, buildx 0.30.1
# and end to end on both shapes: set -> secret mounted; UNSET and EMPTY ->
# build succeeds with no secret, which is the fall-through #302 requires so a
# GitHub build resolves from the public proxy.
#
# The value is passed by EXPORTING a shell variable, never on a command line,
# so it stays out of `ps` exactly as the redirection did.
#
# GOMODAUTH is the one name both consumers read: `docker build --secret
# id=gomodauth,env=GOMODAUTH`, and the compose services' `build.secrets`
# through the top-level `secrets:` block in .devcontainer/docker-compose.yml
# and test/integration/api/docker-compose.yml. Sharing it is the whole point
# (#302): `make ci` built this image with no secret at all while
# `make integration-image` passed one, so two entrypoints to the same image
# disagreed about how to authenticate.
#
# Usage: open the recipe with $(stage-gomodauth) and let the docker invocation
# be the next continued line of that same recipe line. The prelude leaves the
# shell under `set -eu`, so a recipe that captures its own exit code and tears
# down after a failure must `set +e` before the command it means to survive --
# otherwise set -e exits at the failure and the teardown never runs.
# ONE LINE, `;`-separated, values single-quoted. The payload is consumed by
# `. /run/secrets/gomodauth`, and a sourceable script does not need newlines --
# which matters because make splits a recipe on them: an embedded newline ends
# the shell command mid-string ("Syntax error: Unterminated quoted string",
# measured before this form replaced it). Single quotes because the value is
# assembled from a URL and a token; neither may contain a single quote, which
# is true of a Gitea hex token and is the same constraint the previous
# one-value-per-line form carried.
define stage-gomodauth
set -eu; \
if [ -n "$${GIT_CONFIG_KEY_0:-}" ]; then \
  GOMODAUTH="export GOPRIVATE='$${GOPRIVATE:-}'; export GIT_CONFIG_COUNT='$${GIT_CONFIG_COUNT:-}'; export GIT_CONFIG_KEY_0='$${GIT_CONFIG_KEY_0}'; export GIT_CONFIG_VALUE_0='$${GIT_CONFIG_VALUE_0:-}'"; \
  export GOMODAUTH; \
  echo "==> module credentials: from the job environment"; \
elif [ -f $(CURDIR)/.devcontainer/.env ]; then \
  . $(CURDIR)/.devcontainer/.env; \
  GOMODAUTH="export GOPRIVATE='github.com/jeremyhahn/*'; export GIT_CONFIG_COUNT='1'; export GIT_CONFIG_KEY_0='url.http://x:$${GITEA_GO_TOKEN}@$(GITEA_HOST)/jeremyhahn/.insteadOf'; export GIT_CONFIG_VALUE_0='https://github.com/jeremyhahn/'"; \
  export GOMODAUTH; \
  echo "==> module credentials: from $(CURDIR)/.devcontainer/.env"; \
else \
  echo "==> no Gitea credentials here; resolving from the public proxy"; \
fi;
endef

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
	@$(stage-gomodauth) \
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

# ./test/buildgates holds gates over properties of the REPOSITORY rather than
# of the program (#367). They are listed here, not only in the Lint job, so a
# developer running the advertised unit-test entrypoint is told about a
# repository defect at the same moment CI would tell them -- the same reasoning
# that put check-publication-bound into `_ci`.
.PHONY: test
test:
	$(GOTEST) $(TAG_FLAG) ./pkg/... ./cmd/... ./test/buildgates/...

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

# Index maintenance on the Raft apply path, benchmarked against the shard
# shapes it runs in: no indexes, one covering index, and several databases'
# indexes of which one or none covers the write (go-qrdb#410).
.PHONY: bench-engine
bench-engine:
	$(GO) test $(TAG_FLAG) -run '^$$' -bench=. -benchmem $(PKG_ENGINE)

# The standalone backend's write path, which serializes every write so its
# guarded verbs can be atomic (go-qrdb#466). Run with -cpu at or above the
# host's core count: a write mutex is invisible single-threaded.
.PHONY: bench-database
bench-database:
	$(GO) test $(TAG_FLAG) -run '^$$' -bench='BenchmarkStandaloneBackend' -benchmem $(PKG_DATABASE)

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
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) build --quiet e2e
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
	@$(stage-gomodauth) \
	docker compose -f $(COMPOSE_FILE) build --quiet e2e
	docker compose -f $(COMPOSE_FILE) run --rm e2e make _test-e2e-real

.PHONY: _test-e2e-real
_test-e2e-real: build-server build-sdk install-frontend
	cd cmd/qrdb/frontend && npx playwright install chromium
	cd cmd/qrdb/frontend && QRDBD_BIN=$(QRDBD_E2E_BIN) npx playwright test --config=playwright.real.config.ts

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
# containers must NOT keep. The staging, and the reasoning behind it, is the
# shared $(stage-gomodauth) prelude near the top of this file -- every other
# entrypoint that builds this Dockerfile uses the same one, which is what
# stops the two from disagreeing (#302).
.PHONY: integration-image
integration-image:
	@$(stage-gomodauth) \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,env=GOMODAUTH \
	  $(SCANNER_BUILD_ARGS) \
	  --target integration \
	  -f .devcontainer/Dockerfile \
	  -t $(INT_IMAGE) .

# integration-test runs BOTH suites in isolated stages so the standalone
# (multi-protocol CLI vs the single-node docker servers) and cluster
# (self-spawned single-node + 3-node clusters) tests never run concurrently
# and never contend for host CPU. Each stage builds, runs, and tears down its
# own stack. Fail-fast: a failing standalone stage stops before cluster.
.PHONY: integration-test
integration-test: integration-test-auth integration-test-standalone integration-test-cluster
	@echo "==> All integration stages passed (auth + standalone + cluster)."

# test/integration/auth was named by no target and run by no workflow (#228),
# so its two tests -- the M14 auth-factor lifecycle and revoked-factor lookup --
# ran only when someone typed the package path by hand.
#
# Unlike the two stages below it needs no compose stack: it is in-memory and
# self-contained, so it runs as a plain `go test` in about two seconds. That is
# why it runs FIRST in the aggregate and first in the CI integration job: it is
# the cheapest integration failure to report.
#
# $(MIN_TAGS),integration is a DECLARED rung in SUPPORTED_TAG_SETS, so the
# combination this compiles under is one vet-tags already sweeps -- not a new,
# unverified tag set introduced by a test target.
.PHONY: integration-test-auth
integration-test-auth:
	@echo "==> Running AUTH integration tests (in-memory, no compose stack)..."
	$(GO) test -tags "$(MIN_TAGS),integration" -count=1 ./test/integration/auth/...

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
		if [ $$rc -ne 0 ]; then $(MAKE) -s -C $(CURDIR) _integration-server-logs INT_PROJECT=$(INT_PROJECT); fi; \
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
		if [ $$rc -ne 0 ]; then $(MAKE) -s -C $(CURDIR) _integration-server-logs INT_PROJECT=$(INT_PROJECT); fi; \
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
# `--build` here builds .devcontainer/Dockerfile.gpu, so this needs the same
# credential prelude as every other entrypoint that builds an image (#305); the
# overlay's services declare build.secrets and read $$GOMODAUTH from it.
# `set +e` because the prelude leaves the shell under `set -eu` and this recipe
# captures its own exit code to run `down -v` after a failure.
integration-test-gpu:
	@echo "==> Running GPU integration tests (qrdbd + all protocols + NVIDIA GPU)..."
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
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

# ============================================================================
# Coverage
# ============================================================================

# check-coverage: extract percentage from coverage profile and fail if below threshold
# Usage: $(call check-coverage,profile.out,package-name)
define check-coverage
	@COVERAGE=$$($(GO) tool cover -func=$(1) | grep '^total:' | awk '{print $$3}' | sed 's/%//'); \
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
	@$(stage-gomodauth) \
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
# secure-analyzer and its self-test are here for exactly that reason and no
# other (#429). They are Lint-job steps, so leaving them out would reintroduce
# the gap the sentence above describes.
#
# The self-test is included rather than left CI-only (which is where
# test-require-tool and test-tag-lockstep sit) because this guard is the one
# whose own defects are invisible by construction: it fails by staying silent,
# and silence is what success looks like. A second of shell is a cheap way to
# be told the guard still refuses what it claims to refuse.
#
# check-tool-versions and its self-test are here for the same two reasons.
# It runs FIRST, before anything installs or invokes an analyser: when the pins
# disagree, every later step in this pipeline is scanning with a tool whose
# version nobody can name, so a lint or security result produced after the drift
# is not evidence either way. Failing at the top says so in a second instead of
# after the suite.
#
# check-bundled-gosec follows it, at the top for the same reason and second
# because it needs an installed binary rather than a file. It answers the half
# check-tool-versions cannot: `lint` and `lint-sdk` run a gosec that no pin in
# this repository names, and every gosec entry in .golangci.yml means whatever
# THAT version says it means (#295).
#
# check-action-pins is named here AND reached again through `test`, which is
# not redundant book-keeping: `test` sits at the end of this list, behind the
# security suite and a server build, and an unpinned action is a supply-chain
# finding that should be the first thing a reader sees rather than the last.
# The gate costs hundredths of a second and needs neither network nor binary,
# so running it twice is cheaper than reading it late.
#
# test-trivy-db sits with the other scanner guards rather than beside
# `security`, and ahead of it: it needs no trivy and no network, and what it
# asserts is that `security`'s trivy leg refuses a database too old to know
# about a current advisory (#451). A guard for the suite belongs before the
# suite, so a broken guard reports as a broken guard.
_ci: check-tool-versions test-tool-versions check-bundled-gosec test-bundled-gosec test-trivy-db check-compose-targets test-compose-targets check-action-pins test-nightly-signal fmt vet vet-sdk vet-tags lint lint-sdk check-publication-bound test-publication-bound secure-analyzer test-secure-analyzer security build-server test
	@echo "CI pipeline passed"

# ============================================================================
# Linting and static analysis
# ============================================================================

# golangci-lint and go vet need the same build tags the rest of the build uses.
# Untagged, whole packages fail to typecheck — pkg/api/mcp's test mocks sit
# behind `codec_json && raft && qrdb_ts && ...` — and a typecheck failure is
# reported as a lint issue, so the gate goes red for a reason that has nothing
# to do with the code. LINT_TAGS is BUILD_TAGS comma-joined, which is the form
# --build-tags takes -- and, since #295, the form `gosec -tags` takes too, so
# this one list feeds every analyser in this file. It is not renamed to say so:
# the value is the same string either way, and a rename would cost every
# reference below for a comment's worth of clarity.
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

# `./...` DOES NOT CROSS A MODULE BOUNDARY, so the target above reaches none of
# sdk/go and neither does `vet` (#333). That left the one module downstream
# repos actually import -- go-xkms and go-quicbaas import sdk/go, not this one --
# as the only module in the tree with no linter at all, and it had five
# pre-existing findings nobody had ever been shown.
#
# sdk/go/gorm/conformance is a module of its own nested inside sdk/go, so
# `./...` here does not descend into it either and no exclusion is needed to
# keep it out. That is a fact, not a preference: its go.mod is untidy on main
# and `go vet` already refuses it (#233). When #233 lands, including it is a
# one-line change here.
#
# --config is passed explicitly rather than relying on golangci-lint's upward
# search from the working directory, so the SDK is linted by the SAME file as
# the root module and cannot drift onto a different one without the drift being
# visible in this recipe.
.PHONY: lint-sdk
lint-sdk:
	cd $(SDK_DIR) && $(GOLINT) run --config $(CURDIR)/.golangci.yml --build-tags "$(LINT_TAGS)" ./...

.PHONY: vet-sdk
vet-sdk:
	cd $(SDK_DIR) && $(GOVET) $(TAG_FLAG) ./...

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
# pkg/secure/analyzer's three rules, run over the tree they are about (#429).
#
# The analyzer was a complete, unit-tested go vet plugin that ran NOWHERE --
# no target here, no CI job, no .golangci.yml entry. Grepping this file, the
# workflows and scripts/ for `secure-analyzer`, `securecheck` and `vettool`
# found exactly one hit, and it was the doc comment on the tool's own main.go.
# So the rules were enforced by the analyzer's unit tests, which prove the
# ANALYZER works and say nothing whatever about this repository -- and
# pkg/secure's package doc claimed the memguard ban was "enforced by a custom
# go vet analyzer", which is the worse half: an agent reading it concludes the
# invariant holds. A raw memguard import sat in pkg/ceremony's share-store
# test, breaching the containment boundary the whole pkg/secure design rests
# on, for as long as both were true.
#
# WHY IT WAS NEVER ARMED, and what moved. Wiring it in as-written failed on 46
# findings, and most of them could not be fixed in the shape rule 3 demanded:
# generated .pb.go that protoc rewrites, transport DTOs the JSON decoder has
# already materialised, cobra flag targets that os.Args already holds. Leaving
# it unwired is how the rule-1 breach went unseen. The deadlock broke by
# SCOPING RULE 3 -- four structural clauses, each argued in
# pkg/secure/analyzer/scope.go, with the count each silenced written down --
# and fixing everything outside them. Rules 1 and 2 are not scoped.
#
# NOT A BASELINE FILE, on the same reasoning this file already records for
# vet-tags: a baseline makes a green result mean "clean except where it is
# not", which is what running nothing already said. Every clause is a property
# of the declaration, evaluated fresh; none names a file, a package or a line.
#
# THE SWEEP IS vet-modules.sh WITH GOVET OVERRIDDEN, not a second loop. That
# buys the module discovery, the exhaustive-or-fail module mapping and the
# per-rung PACKAGE COUNT that fails closed on zero -- the discovery floor that
# keeps a vacuous pass from looking like a clean one. It also buys the two
# things that cost a measurement each while this issue was being sized:
# `./...` does not cross a module boundary, and sdk/go held 14 findings of its
# own that two separate root-module runs never saw; and ONE root-module finding
# was visible only under the full tag set, so a single combination is not an
# answer about the tree.
.PHONY: secure-analyzer
secure-analyzer:
	@GO="$(GO)" GOLIST="$(GO) list" ./scripts/secure-analyzer.sh --sweep "$(SUPPORTED_TAG_SETS)" "$(GORM_TAGS)"

# The gate driven RED, deliberately, on violations planted for the purpose.
#
# A clean tree exercises only the accepting path, and a sweep whose analyzer
# was neutered, whose -vettool never started, or whose rule 3 was narrowed
# until nothing matched prints exactly what a clean one prints. The handbook's
# rule for auditing suppressions is this rule: the instrument must find a known
# instance FIRST, or its output is measuring something else.
#
# It asserts both directions. A raw memguard import and a holder field must be
# reported AND must make `go vet` exit nonzero -- a diagnostic that does not
# fail the command does not block a merge. A decoder-owned field, a generated
# declaration, a flag target and a test fixture must stay silent, through the
# real binary rather than under analysistest, because the unitchecker path is
# the one CI runs and it is a different code path.
.PHONY: test-secure-analyzer
test-secure-analyzer:
	@GO="$(GO)" ./scripts/secure-analyzer_test.sh

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

# Every `uses:` in every workflow resolves to a 40-hex commit (#367).
#
# A tag is a ref its owner can move, and the review job holds a WRITE-SCOPED
# token -- where auto-merge is armed, a moved tag runs attacker-controlled code
# with credentials that can merge. go-quicraft has caught the identical line
# three times, each an unnoticed template propagation whose real payload was
# something else; this repository had no gate at all, which made it unwatched
# rather than safe.
#
# A GO TEST rather than a scripts/check-*.sh guard like its neighbours, and the
# difference is not stylistic. The property is over PARSED YAML in both
# directions: a line-oriented pattern misses the spellings Actions accepts (a
# flow mapping, a quoted key, an anchor + alias) and passes over them while the
# floors stay satisfied -- and it also reports the word `uses:` in the 70 KB
# agent prompt that opencode-review.yml embedded while it existed, which is
# prose in a block scalar and not a step at all.
#
# Its own floors and rejecting branches ship with it in the same package, for
# the same reason every other guard here carries a self-test: this one fails by
# staying silent, and silence is what success looks like.
.PHONY: check-action-pins
check-action-pins:
	$(GOTEST) ./test/buildgates/...

# The nightly's verdict has to reach somebody (#478).
#
# `nightly.yml`'s signal job turns that workflow's result into a tracking issue
# on this repository: opened on the transition to red, REWRITTEN in place while
# the job stays red, closed on the first green run. The measurement that earned
# it is in test/tools/nightlysignal/main.go -- 22 retained runs of this
# workflow on 2026-09-12, all red, one of them carrying a HIGH advisory that
# reached this repository only because someone ran `make security` by hand.
#
# The knobs, all overridable from the environment. They default to what a
# workflow already sets, so inside a run the target needs none of them; on a
# workstation the forge address has to be given, and an unset one fails with a
# sentence naming the flag rather than by talking to nothing.
#
# The two RESULT knobs default to `failure` because "what would the signal do if
# this went red" is the question a developer has to ask, and the answer is
# uninteresting when everything is green.
NIGHTLY_SIGNAL_SERVER ?= $(GITHUB_SERVER_URL)
NIGHTLY_SIGNAL_API    ?= $(if $(NIGHTLY_SIGNAL_SERVER),$(NIGHTLY_SIGNAL_SERVER)/api/v1,)
NIGHTLY_SIGNAL_REPO   ?= $(if $(GITHUB_REPOSITORY),$(GITHUB_REPOSITORY),jeremyhahn/go-qrdb)
NIGHTLY_SIGNAL_REF    ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
NIGHTLY_SIGNAL_FIPS   ?= failure
NIGHTLY_SIGNAL_TRIVY  ?= failure

# REPORT MODE HERE, deliberately. devops.md §4 says CI runs the same target a
# developer runs, and this is that target -- but a developer running it must not
# file issues as a side effect of asking what the nightly thinks. `-mode apply`
# exists in exactly one place, the workflow step, where the writes are the
# point. Both halves need NIGHTLY_SIGNAL_TOKEN: every route is a read of a
# private repository, and an anonymous read answers 404, which would read as
# "no tracking issue exists".
.PHONY: nightly-signal
nightly-signal: ## Report what the nightly signal would open, update or close
	$(GO) run ./test/tools/nightlysignal \
		-mode report \
		-repo $(NIGHTLY_SIGNAL_REPO) \
		-api $(NIGHTLY_SIGNAL_API) \
		-ref $(NIGHTLY_SIGNAL_REF) \
		-self signal \
		-job "fips=FIPS 140-3=$(NIGHTLY_SIGNAL_FIPS)" \
		-job "trivy=Trivy=$(NIGHTLY_SIGNAL_TRIVY)"

# The decision table, the coverage floor and the rendering, over the REAL
# nightly.yml. Same reasoning as test-require-tool and test-trivy-db: on a
# healthy tree the mechanism only ever runs its accepting branches, so every
# refusal would otherwise ship untested -- and this one refuses by staying
# silent, which is what success looks like.
#
# It also carries the assertion that nightly.yml's `-job` list covers every job
# nightly.yml declares. That check exists at runtime too, but the runtime copy
# fires at 03:00 on a night nobody is watching, which is the same failure the
# tool exists to end. Here it fires in the Lint job on the push that forgets it.
#
# No network, no token, no Go build beyond this one package.
.PHONY: test-nightly-signal
test-nightly-signal: ## Test the nightly signal and its workflow coverage floor
	$(GOTEST) ./test/tools/nightlysignal/...

# The four analyser pins are written in four files, and a Dockerfile cannot read
# a make variable (#323). #322 first removed the duplication outright, by giving
# the ARGs no defaults; that broke VS Code "Reopen in Container" and a bare
# `docker compose build`, which #302 requires keep working, and the owner ruled
# on 2026-09-05 that a workflow regression is not a defect fix. The defaults are
# therefore a DELIBERATE second copy, and this check is the whole of what makes
# that safe -- without it the ruling reintroduces #154's defect one level down.
# #327: `make integration-test-gpu` could not build at all. The base compose
# file sets `target: integration` on two services; the GPU overlay overrides
# `dockerfile:` for those same services and compose merges keys individually,
# so the target survived onto Dockerfile.gpu, whose final stage was unnamed.
#
# It went unnoticed because nothing runs that lane: the GPU path is on no CI
# workflow, and a host without nvidia-smi never selects it (HAS_GPU=0). The
# stage graph is decidable by READING, which is why this is a scanner and not a
# build -- it needs no GPU, no CUDA image and no network.
.PHONY: check-compose-targets
check-compose-targets: ## Every compose `target:` names a real Dockerfile stage
	@./scripts/check-compose-targets.sh

# On a healthy tree the scanner only runs its accepting branch, so every
# rejecting branch would ship untested. Same reasoning as test-tool-versions.
.PHONY: test-compose-targets
test-compose-targets: ## Test the compose-target guard
	@./scripts/check-compose-targets_test.sh

.PHONY: check-tool-versions
check-tool-versions:
	@./scripts/check-tool-versions.sh

# This repository's pins agree permanently, so running the check here exercises
# only its accepting branch. The rejecting branches include the DISCOVERY FLOOR
# -- the cases where a pattern stops matching and the comparison would run over
# empty strings, which always agree. A gate that has quietly become a comparison
# of nothing against nothing is the exact defect this check was written to
# prevent, so it is the one that must not ship untested.
.PHONY: test-tool-versions
test-tool-versions:
	@./scripts/check-tool-versions_test.sh

# check-tool-versions above compares pins between FILES. This one reads a
# BINARY, and the version it reads is written in no file at all: the gosec that
# golangci-lint bundles, which is what `lint` and `lint-sdk` actually judge this
# tree with (#295). See GOLANGCI_GOSEC_VERSION for why it is recorded rather
# than pinned, and why it is not asserted equal to GOSEC_VERSION.
.PHONY: check-bundled-gosec
check-bundled-gosec: ## The gosec inside golangci-lint is the one the exclusions were measured against
	@./scripts/check-bundled-gosec.sh

# Accepting branch only, for as long as golangci-lint stays put -- which is the
# point of the check and the reason its rejecting branches would otherwise ship
# untested. Same reasoning as test-tool-versions, including the discovery floor.
.PHONY: test-bundled-gosec
test-bundled-gosec: ## Test the bundled-gosec guard
	@./scripts/check-bundled-gosec_test.sh
# Symbols no production path can reach (#337). #243's sweep measures
# CONSTRUCTION -- a type built anywhere outside a test counts as produced -- so a
# type constructed only inside a function nothing calls scored "produced" and
# never entered the set. Its 103 is a lower bound; this asks the larger question.
#
# TWO TIERS, and only one of them gates. A symbol with an unexported component
# cannot be named from another package, so unreachable-from-main means it is
# reached from tests or from nothing: that tier FAILS the build when it grows.
# An exported symbol may be called by go-xkms or go-quicbaas, which import
# go-qrdb/pkg/... directly and are invisible here, so those are recorded and
# counted but never fail. Failing on them would make every downstream-facing API
# addition a build error, and the gate would be off within a week.
#
# THE REFLECT-ROOTS PATTERN IS A DECLARATION. cmd/qrdb is a Wails application
# whose *Binding methods are invoked from JavaScript; `main` reaches the struct
# and never names a method on it. Without the declaration the report was 3296
# symbols and mostly that. The tool warns rather than passes if the pattern ever
# matches nothing, so a renamed binding type cannot silently widen the exemption.
DEADCODE_REFLECT_ROOTS := go-qrdb/cmd/qrdb\.[A-Za-z]*Binding\.[A-Z]
DEADCODE_BASELINE      := test/tools/deadcode/baseline.txt

.PHONY: dead-code
dead-code:
	@go run ./test/tools/deadcode -mod . -name . -tags "$(LINT_TAGS)" \
		-reflect-roots '$(DEADCODE_REFLECT_ROOTS)' -baseline $(DEADCODE_BASELINE)
	@go run ./test/tools/deadcode -mod ./sdk/go -name sdk/go -tags "$(LINT_TAGS)" \
		-baseline $(DEADCODE_BASELINE)

# Accepting findings is deliberate and reviewable: the baseline is a tracked
# file, so growing it shows up in the diff and the commit message has to say
# which of the four legitimate reasons applies. Shrinking it is the point and is
# never a failure.
.PHONY: dead-code-baseline
dead-code-baseline:
	@go run ./test/tools/deadcode -mod . -name . -tags "$(LINT_TAGS)" \
		-reflect-roots '$(DEADCODE_REFLECT_ROOTS)' -baseline $(DEADCODE_BASELINE) -write-baseline
	@go run ./test/tools/deadcode -mod ./sdk/go -name sdk/go -tags "$(LINT_TAGS)" \
		-baseline $(DEADCODE_BASELINE) -write-baseline

# ============================================================================
# Interface-method call sweep (go-qrdb#271)
# ============================================================================
#
# ANSWERS A DIFFERENT QUESTION THAN dead-code, ABOVE. dead-code reports
# unreachable FUNCTIONS via RTA/CHA, and by its own design treats every method
# of a runtime-instantiated type as reachable once the type is ever converted
# to ANY interface -- so a concrete type's method that is never actually
# invoked through the interface still reads as reachable there. This sweep
# asks the narrower, method-level question directly: does at least one
# non-test call site select THIS METHOD on THIS INTERFACE (identity =
# package + interface + method, resolved through go/types, never by name)?
# See test/tools/ifacesweep/main.go's package doc for the full rationale,
# including the pass-through false-positive class this method is known to
# produce (a narrow local interface passed to a function whose parameter is a
# structurally-identical sibling interface shows as "uncalled" even though
# the capability is used -- Go loses the "declared via THIS interface"
# identity at that conversion). Findings are NOT a delete list; they are the
# measurement, triaged by hand.
#
# TWO TAG CONFIGURATIONS, NEVER MERGED -- a call site behind a tag this run
# did not use is invisible to it, and the two reports can and do disagree in
# both directions. Comparing them is the reader's job.
IFACESWEEP_DIR := test/tools/ifacesweep/findings

.PHONY: iface-sweep
iface-sweep:
	@mkdir -p $(IFACESWEEP_DIR)
	@go run ./test/tools/ifacesweep -tags "$(MIN_TAGS)" -out $(IFACESWEEP_DIR)/min-tags.tsv
	@go run ./test/tools/ifacesweep -tags "$(LINT_TAGS)" -out $(IFACESWEEP_DIR)/full-tags.tsv

# The committed findings/*.tsv are a reference measurement, not scratch
# output -- their whole value is being stable and diffable across checkouts.
# main.go's relFile already refuses to emit anything but a clean,
# repo-relative path, but this is a SECOND, independent check on the
# committed ARTIFACT: a regression in relFile's next revision is caught here
# even by a reviewer who does not re-read main.go's reasoning, and even if
# nobody runs `make iface-sweep` again before merging. go-qrdb#271 shipped an
# absolute, workstation-specific path on 996 of 996 rows once already,
# uncaught by review and outside check-publication-bound.sh's docs/*/ scope
# -- this is that gap closed for this artifact specifically (the scope gap
# itself is tracked separately, not widened here).
.PHONY: check-iface-sweep-findings
check-iface-sweep-findings:
	@./scripts/check-iface-sweep-findings.sh

# The guard's rejecting branches, which the step above never reaches while
# the tree is clean -- same reasoning as test-publication-bound and every
# other *_test.sh guard in this file. No network, no Go build.
.PHONY: test-iface-sweep-findings
test-iface-sweep-findings:
	@./scripts/check-iface-sweep-findings_test.sh

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
# OFF-BY-DEFAULT FEATURE TAGS ARE NOT RUNGS BY DEFAULT. gpu, qrdbd_webui and
# desktop are WITH_*=0, so they appear in NO rung and nothing in this
# repository compiles them -- not `make test`, not `make lint`, not `make vet`.
# gpu needs CUDA and desktop needs webkit2, so neither belongs in a target every
# developer is told to run before pushing.
#
# qrdb_fts WAS in that set and is now a declared rung (#300, owner-ruled
# 2026-09-22). It is the one of the four with no exotic host requirement, so
# the argument that kept it out -- cost, not capability -- was the only one,
# and it is priced below.
#
# qrdb_fts WAS ALSO BROKEN. IT IS NOT ANY MORE -- corrected here because #279
# is what made the sentence false. Measured 2026-09-03 while choosing these
# rungs, `go vet -tags "codec_json,raft,qrdb_fts" ./pkg/fts/...` FAILED: a test
# double stopped satisfying storage.Batch when Batch gained an embedded Reader
# (2026-08-13), and no target had compiled that package's tests since. Fixed in
# #279 and re-measured on that branch, root module, read by exit code:
#
#     go vet -tags "codec_json,raft,qrdb_fts" ./...    EXIT=0
#     go vet -tags "<LINT_TAGS>,qrdb_fts" ./...        EXIT=0
#
# sdk/go has no qrdb_fts-gated files. So the argument AGAINST declaring the rung
# in the declaration's own commit -- a rung red on arrival teaches people to
# ignore this target, which is how the blind spot got here -- no longer applies
# to qrdb_fts on the evidence above.
#
# The rung IS declared now, and the 2026-09-03 figures above were re-measured
# on main at 63f80a6 before declaring it rather than trusted: `go vet` under
# both codec_json,raft,qrdb_fts and <LINT_TAGS>,qrdb_fts still exits 0 after
# three weeks of tree movement. A rung declared on a stale measurement arrives
# RED, which is the failure that teaches people to ignore this target -- and a
# stale measurement is precisely what #279 had to correct here.
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
# The structural half of the price is exact and load-independent: the original
# change took the tagged modules from 2 combinations to 4, and the whole-tree
# sweep from 6 `go vet` invocations to 10. Declaring the qrdb_fts rung (#300)
# took the sweep from 13 invocations to 15.
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
# Measured that way for #300, on main at 63f80a6, both sweeps WARM and back to
# back on one cache: 4 rungs 20.4s at load 6.94, 5 rungs 25.6s at load 5.73 --
# a ratio of 1.25x. Read the RATIO. The seconds are this host on that evening
# and will not reproduce elsewhere; they are recorded only so the ratio can be
# checked against them. The figure is conservative in the one direction that
# matters: the FIVE-rung run had the lower load, so contention did not inflate
# it. Both sides are warm deliberately -- the first run of a new combination
# pays a one-time cache population that a repeating gate does not.
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
SUPPORTED_TAG_SETS := $(MIN_TAGS) $(MIN_TAGS),integration $(LINT_TAGS) $(LINT_TAGS),integration $(LINT_TAGS),qrdb_fts

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
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	GOVET="$(GOVET)" GOLIST="$(GO) list" ./scripts/vet-modules.sh --vet "$(SUPPORTED_TAG_SETS)" "$(GORM_TAGS)"

# print-tag-sets prints the declared combinations, one per line, so that
# anything needing them -- a CI matrix, a doc, a one-off sweep -- READS the
# declaration instead of restating it. A second copy of this list is the defect
# #263 exists to close.
.PHONY: print-tag-sets
print-tag-sets:
	@printf '%s\n' $(SUPPORTED_TAG_SETS)

# print-trivy-version prints TRIVY_VERSION for the same reason: ci.yml's
# blocking trivy job installs the scanner PINNED to it, and a workflow that
# restated "v0.74.0" would be a second copy of the pin that drifts the first
# time someone bumps this one (#479). The workflow reads; this declares.
.PHONY: print-trivy-version
print-trivy-version:
	@printf '%s\n' $(TRIVY_VERSION)

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

# Every scanner in `security` refuses when its binary is absent, and none of
# them skips.
#
# `trivy` used to skip: `command -v trivy || echo "not found - skipping"`, which
# exits 0, so `security` went on to print "Security suite passed" having run
# three scanners out of four. Measured 2026-09-05 (#299): trivy was absent from
# the workstation AND from the devcontainer image every lane runs `make ci` in,
# so that is what every green Security leg on this host had meant, and the
# output was identical either way. A ruling to spend a 6.5 GB image rebuild
# (#294) to "get a trivy result" was made on the strength of a scan that does
# not run.
#
# The guard has to be here rather than in `security` because the question is
# "did this scanner run", and only the recipe that would have run it can answer
# it -- in the environment where it would have run. `make ci` executes `_ci`
# INSIDE the devcontainer (see `ci`), so a check made anywhere else reports on a
# PATH that decides nothing: trivy present on the host and absent from the image
# would report a scan that never happened, which is the defect wearing its own
# remedy. This is why there is no "which scanners ran" summary assembled outside
# the container.
#
# An install hint is part of the message: a gate that refuses without saying how
# to satisfy it is a gate people route around.
define require-scanner
	@command -v $(1) >/dev/null 2>&1 || { \
		echo "ERROR: $(1) is not installed, and this suite does not skip scanners." >&2; \
		echo "  A scanner that did not run is not a scanner that passed (#299)." >&2; \
		echo "  Install: $(2)" >&2; \
		exit 1; \
	}
endef

# Every hint installs the version this repo actually gates on, never `@latest`.
# gosec is pinned fleet-wide by owner ruling #154 for a measured reason: an
# `@latest` that self-updated mid-day introduced G703 and reddened the Security
# job on every branch, main included, on code nobody had touched since April.
# A hint that says `@latest` would hand a developer exactly that.
#
# THESE FOUR LITERALS ARE WHAT EVERY `make` ENTRYPOINT BUILDS THE IMAGE WITH
# (#322). They were three, they were hand-synced, and the Dockerfiles restated
# their own -- gosec and govulncheck at `@latest`, golangci-lint at v2.7.2
# against ci.yml's v2.13.2, so the gate image and CI could analyse one commit
# with different analysers and neither result would say so.
#
# The Dockerfiles now take these as build ARGs, and their ARG DEFAULTS ARE
# THESE SAME NUMBERS. That is a deliberate second copy: a build that goes
# around make -- VS Code "Reopen in Container", a bare `docker compose build`
# -- has to keep working (#302, ruled 2026-09-05), and it can only do that if
# the Dockerfile can supply a version on its own. The copy is guarded by a
# check rather than by its absence: #323 asserts the Makefile, ci.yml and the
# Dockerfile defaults agree. Changing a pin means changing it in all three.
#
# `export` rather than passing them per-recipe: every `docker compose` call in
# this file builds one of those Dockerfiles, directly or through a service that
# shares the `go-qrdb-dev:latest` tag, and compose reads these from its own
# environment. Exporting covers all of them, including the ones added after
# this comment -- naming the call sites instead would leave the next one to be
# found by a failed build. The compose files take them through the
# environment-PASSTHROUGH form (`GOSEC_VERSION:` with no value), so an unset
# variable omits the arg and the Dockerfile default applies; `${GOSEC_VERSION}`
# there would pass an empty string and defeat the default. Measured, and
# written up in .devcontainer/docker-compose.yml.
#
# THAT THESE AGREE WITH `.github/workflows/ci.yml` IS NOW ASSERTED, by
# `make check-tool-versions`, which runs first in `_ci` (#323). ci.yml's env
# block is the authority for the three Go analysers -- it is what the runner
# installs, so it is what actually judges a pull request -- and neither a
# Makefile nor a Dockerfile can read it. trivy is the exception: ci.yml carries
# no trivy pin, so for that one the value here is the authority and the CPU
# Dockerfile is held to it.
#
# Changing a pin still means changing it in ci.yml, here, and in both
# Dockerfiles. The difference is that forgetting one is now a red gate naming
# the file, rather than two images wearing one tag.
#
# WHAT IS STILL NOT ASSERTED BY THAT CHECK: that the version pinned is the
# version INSTALLED. A pin says which analyser should be in the image, not which
# one is; require-tool.sh answers that, inside both Dockerfiles at build time
# and again in ci.yml. The two halves together are the guarantee.
GOLANGCI_LINT_VERSION := v2.13.2
GOSEC_VERSION         := v2.29.0
GOVULNCHECK_VERSION   := v1.7.0
TRIVY_VERSION         := v0.74.0

# A FIFTH VERSION, and it is NOT A PIN (#295). golangci-lint bundles a gosec of
# its own, and that is the one `lint` and `lint-sdk` judge this tree with --
# a real analyser, chosen by golangci-lint's go.mod rather than by anything
# here, and until this line written down nowhere. It moves when
# GOLANGCI_LINT_VERSION moves, silently, and the meaning of every gosec entry
# in .golangci.yml moves with it.
#
# So this records WHICH gosec those entries were measured against, and
# `make check-bundled-gosec` reads the installed binary's build info and fails
# when it bundles a different one. It is deliberately NOT asserted equal to
# GOSEC_VERSION above: nothing in this repository chooses golangci-lint's
# dependency, so demanding they match would be a gate red by construction. The
# divergence is made VISIBLE instead -- the check prints both, every run.
#
# The two are not interchangeable, and the difference is soundness rather than
# strictness: v2.28.0's G703 accepts filepath.Clean as a path-traversal
# sanitizer and v2.29.0 does not, upstream having removed it with the note that
# Clean "evaluates '..' elements instead of rejecting them, so the traversal
# succeeds" (securego/gosec#1721). The lint leg therefore CLEARS a cleaned path
# that the gosec leg reports HIGH/HIGH.
#
# Bumping GOLANGCI_LINT_VERSION means re-measuring the exclusions in
# .golangci.yml and updating this line in the same change. Read it off the
# binary: `go version -m $$(command -v golangci-lint)`.
GOLANGCI_GOSEC_VERSION := v2.28.0

export GOLANGCI_LINT_VERSION
export GOSEC_VERSION
export GOVULNCHECK_VERSION
export TRIVY_VERSION

# The two `docker build` entrypoints get no compose interpolation, so they pass
# the same four explicitly. One variable rather than four flags at each site,
# for the same reason `export` beats per-recipe passing.
SCANNER_BUILD_ARGS := \
  --build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
  --build-arg GOSEC_VERSION=$(GOSEC_VERSION) \
  --build-arg GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) \
  --build-arg TRIVY_VERSION=$(TRIVY_VERSION)

# gosec's install hint carries the same -ldflags the images use. Without it a
# developer who follows the hint gets a binary that self-reports `dev`, which
# is not a version anything can check -- the hint would satisfy `command -v`
# and defeat the attribution the pin exists for.
#
# SINGLE quotes around the -X, and they are load-bearing. `require-scanner`
# renders this through `echo "  Install: $(2)"`, so double quotes inside it are
# consumed by that echo and the hint prints as `-ldflags -X main.Version=...`,
# which a developer who pastes it runs as an ldflags value of `-X` and a package
# path of `main.Version=v2.29.0`. Measured: it printed exactly that. A hint that
# does not survive being printed is worse than no hint, because it fails at the
# paste rather than at the read.
#
# There is no GOLANGCI_LINT_INSTALL hint, deliberately. `require-scanner` exists
# because trivy SKIPPED when absent and the suite still printed a pass (#299);
# `lint` has never skipped -- an absent golangci-lint fails the recipe outright
# -- so guarding it would add a hint for a failure mode that does not exist,
# and the variable would be the only thing keeping the guard alive.
GOSEC_INSTALL       := go install -ldflags '-X main.Version=$(GOSEC_VERSION)' github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
GOVULNCHECK_INSTALL := go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
# The release tarball, which is how devops.md 8 has both CI legs install it and
# how .devcontainer/Dockerfile bakes it into the image. The script is fetched at
# the tag too, so the hint and the image run the same fixed artifact.
TRIVY_INSTALL       := curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/$(TRIVY_VERSION)/contrib/install.sh | sh -s -- -b /usr/local/bin $(TRIVY_VERSION)

# -tags AND a second module, because this leg scanned neither (#293).
#
# NO TAGS was the larger gap. Every other analysis target composes tags --
# `lint` passes --build-tags, `test` composes TEST_TAGS -- for the reason the
# agent spec already states: an untagged run can be "clean" while a gated
# package does not even build.
#
# ONLY THE ROOT MODULE was the second. `./...` does not cross a nested module
# boundary, so sdk/go -- the module go-xkms, go-quicbaas and go-signal actually
# import -- was never scanned. Same shape as #333 found for the linter, and the
# same fix: a second invocation, because there is no pattern that spans modules.
#
# THE TAG LIST IS $(LINT_TAGS), COMMA-JOINED, AND THE SYNTAX IS THE WHOLE POINT
# (#295). #293 passed BUILD_TAGS space-joined, on the stated belief that "that
# is the form -tags takes". It is not. gosec's flag is documented "Comma
# separated list of build tags" and parsed `strings.Split(flag, ",")`, so a
# space-joined list arrives as ONE tag whose name contains spaces, matches no
# build constraint, and silently selects the UNTAGGED tree -- leaving the gap
# #293 measured exactly where it found it, under a flag that looked like the
# fix. Re-measured 2026-09-10 on gosec v2.29.0, whole tree, no excludes:
#
#   -tags "codec_json raft ..."   1044 files / 332,577 lines / 447 findings
#   -tags "codec_json,raft,..."   1147 files / 355,851 lines / 490 findings
#
# 103 files and 23,274 lines. The tell is in gosec's own log: with the space
# form it prints `Checking file: pkg/vector/ivf/stub.go` and nothing else for
# that package, because every real file there is behind `//go:build
# qrdb_vector`.
#
# There is deliberately no GOSEC_TAGS variable any more. golangci-lint and
# gosec take the identical syntax, so one list feeds both legs and there is
# nothing left to drift; a second name spelled a second way is what produced
# this defect.
#
# -tests IS DELIBERATELY ABSENT FROM ALL THREE INVOCATIONS BELOW, AND THAT IS A
# MEASURED DECISION, NOT AN OVERSIGHT (#395). Do not add it without reading
# this; #395 was filed to add it and the measurements say it cannot be, yet.
#
# WHY IT LOOKS LIKE IT SHOULD BE ON. gosec's default is -tests=false, and that
# is not merely "test files are skipped": the analyzer-backed rules run over a
# package's SSA, and the test variant BUILDS MORE OF THE PACKAGE, so omitting it
# reaches less of the NON-TEST code. Measured on gosec v2.29.0 with the tags
# above, no excludes, no severity floor, duplicate (file, line, rule) tuples
# collapsed:
#
#                unique findings in NON-TEST files
#                    root      sdk/go
#   no -tests          504         148
#   -tests             519         152
#
# The 15 and the 4 are all G602, and they are exactly the sets recorded against
# .golangci.yml's G602 entry -- that leg sets `run.tests: true`, so its gosec
# has always reached them.
#
# WHY IT IS STILL OFF. Two independent reasons, either one sufficient:
#
#   1. IT FAILS THE GATE. gosec loads a package by handing go/packages a FILE
#      LIST, which makes the loaded package `command-line-arguments`. With
#      -tests it adds XTestGoFiles to that list, and an external test package
#      (`package foo_test`) importing its own package by IMPORT PATH then binds
#      to the real, un-augmented package -- so every identifier an
#      `export_test.go` exposes is undefined. gosec exits 1 on loader errors
#      even with zero findings. This tree has two such packages, pkg/seal and
#      sdk/go, and both fail:
#
#        pkg/seal/barrier_registry_strategy_test.go:128: undefined:
#            seal.NewCaptureDistributor
#        sdk/go/membership_test.go:124: undefined:
#            sdk.TranslateMemberEventForTest        (+ 8 more in sdk/go)
#
#      `go vet` and `go test` typecheck both packages fine, so this is gosec's
#      loader, not this tree. Reproduced on 22 lines: a package, an
#      `export_test.go` exporting one unexported symbol, and an external test
#      using it -- `go vet` and `go test` pass, `gosec` passes, `gosec -tests`
#      exits 1 with "Issues: 0". `gosec@latest` resolved to v2.29.0 on
#      2026-09-10, so there is no newer release to take.
#
#      Working around it means deleting the `export_test.go` idiom from
#      pkg/seal and sdk/go -- restructuring tests in a security-review path to
#      suit a scanner bug. That is a change for an owner to approve, not a
#      side effect of a flag. Carried by #408 with both options written out.
#
#   2. IT WOULD NOT CHANGE WHAT THIS LEG REPORTS ANYWAY. This leg runs at
#      -severity=medium and gosec grades G602 LOW/HIGH, so all 19 of the sites
#      -tests restores sit under the floor. Measured the same day, unique
#      non-test sites at each floor:
#
#                              root   sdk/go
#        -severity=medium       488      148   <- what this leg reports
#        -severity=low          519      152
#          G602                  15        4   <- what -tests restores
#          G101                  11        0   <- LOW confidence too, so the
#                                                 -confidence floor holds these
#                                                 out at either severity
#          G103                   5        0   <- 3 of them in generated .pb.go
#
#      So -tests is necessary but NOT sufficient for the G602 blindness #395
#      names; the severity floor is the other half, and lowering it is its own
#      change (three of the sites are in generated protobuf output, which
#      cannot carry an inline justification because protoc rewrites it). #408.
#
# WHAT IS ALREADY PAID FOR. The 91 findings a -tests run admits in _test.go
# files -- the reason #395 called this a separate change -- are triaged and
# gone: directory and file modes tightened to 0750/0600 at 39 sites, and 48
# inline `#nosec <RULE> -- reason` annotations at the rest (G102, G120, G122,
# G202, G204, G302, G404, G407, G702, G703), each read individually. Both
# scanner versions honour those, so when the loader defect is resolved this
# becomes a one-word change to three lines with a green gate behind it.
#
# The three invocations are listed one after another ON PURPOSE. A flag that
# reaches two of them and not the third is how this class of defect survives:
# #293 gave `gosec` a tag list and left `gosec-sarif` bare, and #295 found the
# tag list was inert anyway. Whatever you change here, change all three.
.PHONY: gosec
gosec:
	$(call require-scanner,$(GOSEC),$(GOSEC_INSTALL))
	$(GOSEC) -concurrency 4 -tags "$(LINT_TAGS)" -exclude=$(GOSEC_EXCLUDE) $(GOSEC_EXCLUDE_DIRS) -severity=medium -confidence=medium ./...

# The SDK module, scanned with the same exclusions so the two legs cannot
# disagree about what is suppressed. sdk/go/gorm/conformance is a module nested
# inside it, so `./...` does not descend there and needs no exclusion -- its
# go.mod is untidy on main (#233) and `go vet` already refuses it.
#
# NO -exclude-dir, and that is measured rather than assumed: neither path in
# GOSEC_EXCLUDE_DIRS exists under sdk/go. `find . -type d -name testdata`
# returns exactly ONE directory in this repository, pkg/secure/analyzer/testdata,
# in the ROOT module, and test/performance is root-module too. Passing the flag
# here would be a claim about a path that is not there -- and the day sdk/go
# grows a testdata tree, this comment is where the decision gets made rather
# than silently inherited.
.PHONY: gosec-sdk
gosec-sdk:
	$(call require-scanner,$(GOSEC),$(GOSEC_INSTALL))
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	cd $(SDK_DIR) && $(GOSEC) -concurrency 4 -tags "$(LINT_TAGS)" -exclude=$(GOSEC_EXCLUDE) -severity=medium -confidence=medium ./...

# SARIF for GitHub code scanning. Same exclusions as `gosec` above so the
# dashboard and the gate never disagree; non-blocking because `gosec` is the
# gate.
.PHONY: gosec-sarif
gosec-sarif:
	$(GOSEC) -concurrency 4 -tags "$(LINT_TAGS)" -fmt=sarif -out=gosec.sarif -exclude=$(GOSEC_EXCLUDE) $(GOSEC_EXCLUDE_DIRS) -severity=medium -confidence=medium ./... || true

# Blocking. govulncheck is the forcing function for toolchain currency
# (devops.md §9) — swallowing its exit code is what lets a known-vulnerable
# stdlib sit in a release for months, which is exactly the failure mode the
# section exists to prevent.
#
# THIS LEG HAS NO DATABASE-AGE GUARD, and that is measured rather than an
# omission (#451 scope item 4). govulncheck carries no database: nothing in
# this repository sets GOVULNDB, so it fetches https://vuln.go.dev over the
# network on every run, and the images bake the binary only. There is no cache
# here that can go stale the way trivy's can.
#
# What it has instead is a COVERAGE gap, which no freshness check would close.
# Measured 2026-09-11: https://vuln.go.dev/index/modules.json lists four
# advisories for google.golang.org/grpc, the newest GO-2026-6061 (fixed
# 1.82.1). CVE-2026-84445 / GHSA-2v4p-qf9q-27wj — HIGH, fixed in 1.83.2 — has
# no Go-curated entry at all. So govulncheck being clean on v1.83.1 was not
# "the affected symbols are unreachable"; it was the advisory not being in the
# database it reads.
#
# That is precisely why this suite runs both and why neither substitutes for
# the other: govulncheck judges a call graph against a curated Go database,
# trivy judges package versions against the GHSA stream. A clean govulncheck
# is not evidence about anything trivy would report.
#
# A FRESHNESS FLOOR WAS RULED OUT (#480, 2026-09-22), and it was very nearly
# built. The case for it sounded identical to the trivy guard's -- "a green
# scan says a scan ran and nothing about what it knew" -- and it is not
# identical. trivy's guard exists because trivy holds a CACHED artifact that it
# never re-downloads while the artifact claims not to be expired. govulncheck
# holds nothing: measured 2026-09-22, no vuln database anywhere under ~/.cache
# or the module cache, GOVULNDB unset, `govulncheck -json` reporting
# db=https://vuln.go.dev. There is no local copy to go stale.
#
# What a copied guard WOULD have done is fail for the wrong reason. The same
# run reported db_last_modified=2026-09-16T18:00:43Z -- six days old, because
# the Go vulnerability team had simply not published anything that week. A
# 24-hour floor on that field would have been red with nothing a developer
# could do about it: there is no "refresh" for a database you do not hold. A
# guard that fires on a quiet upstream week and cannot be cleared by any change
# to this tree is as useless as one that cannot fire.
#
# The real exposure is the coverage gap above, and it is closed by running
# trivy BLOCKING on every push (ci.yml's `trivy` job, #479) rather than only
# overnight. Revisit only if GOVULNDB is ever pointed at a local or mirrored
# database, which is the one configuration where staleness becomes real.
.PHONY: vuln
vuln:
	$(call require-scanner,$(GOVULNCHECK),$(GOVULNCHECK_INSTALL))
	@$(stage-gomodauth) \
	eval "$${GOMODAUTH:-}"; \
	$(GOVULNCHECK) ./... && cd sdk/go && $(GOVULNCHECK) ./...

# No `require-scanner` guard: this one runs the Go toolchain, not a separately
# installed scanner. Every other target in this file needs `go` too, so its
# absence cannot produce the failure #299 is about -- a suite that reports a
# pass having silently run less than it says. Do not add a guard here for
# symmetry; it would assert something that is never in doubt.
.PHONY: mod-verify
mod-verify:
	$(GO) mod verify
	cd sdk/go && $(GO) mod verify

# Directories excluded from the filesystem scan. This is now the ONLY copy:
# nightly.yml's gate calls `make trivy` on both hosts, so the list cannot
# disagree with CI rather than being kept in agreement with it (devops.md §4,
# #451). The line above used to say it was "kept identical to the skip-dirs
# list in .github/workflows/ci.yml", which had not been true since #48 moved
# the job to nightly.yml -- a hand-sync against a file that no longer had
# anything to sync with.
# test/ holds benchmark harnesses that ship in no release.
# cmd/qrdbd/data/ is the daemon's runtime data directory: gitignored, created
# by running qrdbd locally, and full of the dev CA/node/admin keys it mints on
# first boot. Nothing there can reach history, so scanning it only makes the
# local gate fail for every developer who has ever started the daemon while
# CI -- which scans a fresh clone -- passes.
# Suppressions belong in a .trivyignore with a reason and a date, not here.
TRIVY_SKIP_DIRS := --skip-dirs test,cmd/qrdbd/data

# The oldest vulnerability database this leg will certify a tree with (#451).
#
# It is not a second expiry policy layered over trivy's. trivy-db is rebuilt
# upstream every six hours and its metadata sets NextUpdate = UpdatedAt + 24h,
# so 24 is trivy's own horizon; what the check adds is the comparison against
# the WALL CLOCK that trivy does not make. trivy decides whether to refresh by
# reading NextUpdate out of the artifact it already holds -- data inside that
# artifact rather than a test of how old it is. Measured on v0.74.0: a database
# stamped 2026-06-30 declaring NextUpdate 2027-01-01 scanned this tree offline
# and exited 0, with no warning and no age anywhere in its output.
#
# Raising this number widens exactly one thing: how far behind the advisory
# stream a green Security leg is allowed to be. The rationale, the measurement
# and the discovery floor are in scripts/check-trivy-db.sh.
TRIVY_DB_MAX_AGE_HOURS := 24

# THREE COMMANDS, AND THE ORDER IS THE POINT.
#
# The refresh is separated from the scan so a database can be certified BEFORE
# anything is judged with it, and the scan then runs `--skip-db-update` so the
# database the check ruled on is the database the scan rules with. Folding them
# back into one invocation returns to the state #451 describes: a verdict with
# no statement about what produced it.
#
# `--download-db-only` takes a scan target it does not read; the `.` is that
# argument, not a scope. The secret scanner needs no database, so the refresh
# asks only for vuln.
#
# The check is deliberately NOT its own phony target. It has exactly one
# correct call site -- ahead of every scan -- and a free-standing entry point
# would be a way to run it against a database no scan then uses, which is a
# report rather than a gate. It is a CANNED RECIPE rather than a target for
# that reason: `trivy` and `trivy-report` below each open with it, and neither
# can be invoked without it.
define trivy-certified-db
	trivy fs --scanners vuln --download-db-only .
	@./scripts/check-trivy-db.sh $(TRIVY_DB_MAX_AGE_HOURS)
endef

# The BLOCKING severity floor. It does not move, and #522 is the occasion on
# which it was asked to and did not: devops.md §8 sets the gate at
# CRITICAL/HIGH deliberately, and a gate that blocks a merge on a MEDIUM is one
# people learn to route around. What moves is the REPORTING floor, immediately
# below.
TRIVY_GATE_SEVERITY := CRITICAL,HIGH

# The SCOPE of the scan, which is a separate question from its severity floor
# and was never asked until #530. trivy's npm parser EXCLUDES a lockfile's
# dev dependencies unless told otherwise, so with the flag below absent the
# blocking gate was green on three HIGH advisories -- two in nanoid, one in
# postcss, all fixed upstream -- that `npm audit` reported and that sat squarely
# AT the blocking floor. A severity floor at least reports the band above it;
# a scope gap reports nothing about a whole dependency tree.
#
# Dev dependencies are IN scope here, on the blocking leg, by owner ruling
# (2026-09-16). "Dev dependencies do not ship" is the usual argument for
# leaving them out and it is weaker than usual for this tree: vite and esbuild
# PRODUCE the shipped bundle, and a compromised build tool is a supply-chain
# path into the artifact -- the class of risk this scanner exists to name. The
# flag applies to every language ecosystem trivy parses, not only npm; Go has
# no dev-dependency notion, so for the Go modules it changes nothing.
#
# Both legs carry it so the reporting floor below covers the same tree the
# blocking floor does. The three findings that made this visible were fixed in
# the same change that added the flag, so it landed green; a finding this
# surfaces later is fixed by bumping the dependency, not by removing the flag.
# test/buildgates asserts every scan line carries it.
TRIVY_SCOPE := --include-dev-deps

.PHONY: trivy
trivy:
	$(call require-scanner,trivy,$(TRIVY_INSTALL))
	$(trivy-certified-db)
	trivy fs --scanners vuln,secret $(TRIVY_SCOPE) --severity $(TRIVY_GATE_SEVERITY) --exit-code 1 --skip-db-update $(TRIVY_SKIP_DIRS) .

# ----------------------------------------------------------------------------
# The REPORTING floor (#522)
#
# THE DEFECT THIS CLOSES: with `CRITICAL,HIGH` the only severity list anywhere
# in the repository, no leg of the pipeline could report a MEDIUM, so a green
# Security signal could not distinguish "there are no MEDIUMs" from "nothing
# looked". Two real advisories -- an authentication bypass in go-pkcs12's
# Decode functions and an ECharts XSS -- sat under that floor for the whole of
# the repository's history and surfaced only because someone widened the list
# by hand for an unrelated demonstration. An advisory nobody has been shown is
# not an accepted one.
#
# WHY IT EXITS NONZERO, given that #522 asks for reporting rather than gating.
# "Reporting, not gating" is a statement about what blocks a MERGE, and this
# blocks none: it runs in nightly.yml, in the Trivy job, which is not in
# `ci-gate`'s `needs` while the repo is alpha (devops.md §8a). What it is not
# is `--exit-code 0`, because an exit-0 step in a nightly is indistinguishable
# from no step at all -- which is the status quo this exists to end. A signal
# that can transition to red is the only kind anyone reads (#478).
#
# WHY IT LANDS GREEN. Measured on main at the time of writing, the entire
# sub-threshold backlog was four findings and TWO distinct advisories -- the
# go-pkcs12 one in all three go.mod files, and echarts in the frontend
# lockfile. Both are fixed in the same change that adds this target, so the leg
# is green on the day it arrives. That is deliberate and it is the same
# reasoning scripts/check-publication-bound.sh records: a guard that is red on
# the day it lands is a guard nobody arms.
#
# WHY LOW IS IN AND UNKNOWN IS NOT. LOW measured ZERO on this tree, so
# including it costs nothing today and closes the band rather than leaving a
# second invisible one. UNKNOWN is trivy's "no CVSS score was assigned", and on
# this tree it is one finding -- golang.org/x/crypto/openpgp, deprecated
# upstream, `Fixed Version` EMPTY because the fix is not to use the package.
# There is no version to bump to, so a band that included it would be red from
# the first run with nothing anyone could do, which is the failure mode the
# paragraph above is about. It is not thereby invisible: the second invocation
# below PRINTS the UNKNOWN band advisorily in the same run. Unreported and
# non-blocking are different things, and the whole of #522 is that distinction.
#
# HOW A FINDING IS DISPOSITIONED, decided in advance rather than during the
# first noisy week:
#   1. A fix exists and the bump is not a major -- take it. The default.
#   2. The vulnerable SYMBOL is measurably unreached -- record the measurement
#      (the grep or the call graph, not the assertion) in the feature folder
#      documenting that surface, and take the bump anyway when it is free.
#      Reachability is argued per finding: govulncheck's silence is evidence
#      only when the affected symbols are actually in its database (#451).
#   3. Neither -- a .trivyignore entry with a written reason and a date
#      (devops.md §8). Note that trivy applies .trivyignore to EVERY leg, so an
#      entry added to quiet a MEDIUM also quiets that advisory in the blocking
#      gate if it is later rescored. Say so in the entry.
#
# NOT in `security` and NOT in `_ci`, deliberately. `make security` is the
# blocking contract and `make ci` claims to mirror the pull-request path; a
# MEDIUM-red target in either would make MEDIUM blocking by the back door,
# which is precisely what this must not do.
TRIVY_REPORT_SEVERITY := MEDIUM,LOW

.PHONY: trivy-report
trivy-report: ## Report sub-threshold (MEDIUM/LOW) advisories; advisory in nightly, blocks no merge
	$(call require-scanner,trivy,$(TRIVY_INSTALL))
	$(trivy-certified-db)
	trivy fs --scanners vuln,secret $(TRIVY_SCOPE) --severity $(TRIVY_REPORT_SEVERITY) --exit-code 1 --skip-db-update $(TRIVY_SKIP_DIRS) .
	@echo "== UNKNOWN band (advisory; no score assigned, often no fix available) =="
	trivy fs --scanners vuln $(TRIVY_SCOPE) --severity UNKNOWN --exit-code 0 --skip-db-update $(TRIVY_SKIP_DIRS) .

# The rejecting branches of that check, which the target above never reaches
# while the database is fresh -- and on any host where the suite is meant to
# run it is fresh, which is the point. Same reasoning as test-tool-versions and
# test-bundled-gosec, and it carries more weight here: the property is that the
# gate FAILS rather than warns, and an assertion nobody has watched fail is not
# that property. Needs no network, no trivy and no Go build, so it runs in the
# Lint job on every push rather than only where a scanner is installed.
.PHONY: test-trivy-db
test-trivy-db: ## Test the trivy database-age guard
	@./scripts/check-trivy-db_test.sh

# The contract CI calls (devops.md §8): gosec, govulncheck, go mod verify and
# trivy fs, in the same form a developer runs locally.
#
# The summary line names its prerequisites through `$^` rather than spelling
# them out, so it cannot drift from the list it is summarising -- a claim that
# four scanners passed is only worth reading if adding a fifth updates it. It is
# printed by the devcontainer, which is where they ran.
#
# What this line used to say: "trivy carries its own `command -v` guard so a
# developer without it installed still gets the Go half of the suite; CI
# installs the CLI, so nothing is skipped there." Both halves were false. The
# guard skipped for everyone, not only for developers, because the image had no
# trivy either; and `ci.yml` has not run trivy since ruling #48 moved it to
# `nightly.yml` (ci.yml:491 says so), so there was no CI install to make the
# local skip harmless. That is how a suite came to be missing a quarter of
# itself with nothing to read that would say so.
.PHONY: security
security: gosec gosec-sdk vuln mod-verify trivy
	@echo "Security suite passed: $^"

# ============================================================================
# Docker
# ============================================================================

# docker-build builds the release image from the root Dockerfile, whose
# `go mod download` needs the same internal Gitea credentials every other image
# build here does -- so it opens with the same $(stage-gomodauth) prelude and
# passes the same secret id. Before #305 it passed none and the Dockerfile
# swallowed the resulting failure, so this target produced an image-less build
# error naming a missing package instead of a missing token.
.PHONY: docker-build
docker-build:
	@$(stage-gomodauth) \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,env=GOMODAUTH \
	  -t qrdbd:$(BUILD_VERSION) .

.PHONY: docker-build-integration
docker-build-integration:
	@$(stage-gomodauth) \
	DOCKER_BUILDKIT=1 docker build \
	  --secret id=gomodauth,env=GOMODAUTH \
	  $(SCANNER_BUILD_ARGS) \
	  -t go-qrdb-integration:$(DOCKER_TAG) -f .devcontainer/Dockerfile .

.PHONY: docker-run
docker-run:
	docker run --rm -p 63001:63001 -p 63002:63002 -p 63003:63003 qrdbd:$(BUILD_VERSION)

.PHONY: docker-run-devcontainer
docker-run-devcontainer:
	@$(stage-gomodauth) \
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
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
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
	@$(stage-gomodauth) \
	set +e; \
	cd test/integration/api && \
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
# gormconformance ALONE IS NOT A BUILDABLE TAG SET. The harness imports
# sdk/go/driver, whose files are behind qrdb_sql and qrdb_embedded, so under the
# bare tag `go vet` reports
#
#     imports github.com/jeremyhahn/go-qrdb/sdk/go/driver:
#     build constraints exclude all Go files in .../sdk/go/driver
#
# which is a MISSING TAG, not a defect in this module. #233 recorded the blocker
# as "sdk/go/gorm/conformance's untidy go.mod"; the untidiness was real and is
# fixed in this same change, but it was never what made this leg red. Tidying
# alone leaves it red, which is how the misdiagnosis was found.
#
# codec_json and raft are the mandatory floor (REQUIRED_TAGS); qrdb_sql and
# qrdb_embedded are what sdk/go/driver itself needs. Adding a tag here is
# additive: a tag the harness does not use costs nothing, and a tag it does use
# and does not get costs the whole leg.
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
	@echo "  security               Blocking suite: gosec, govulncheck, go mod verify, trivy"
	@echo "  gosec                  Run gosec security scanner"
	@echo "  vuln                   Run govulncheck vulnerability scanner"
	@echo "  trivy                  Blocking trivy leg (CRITICAL/HIGH)"
	@echo "  trivy-report           Sub-threshold advisories (MEDIUM/LOW + UNKNOWN); blocks no merge"
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
