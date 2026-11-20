# go-qrdb CI image: one Dockerfile, the shipped stages built FROM base
# (handbook devcontainer.md §1). Every CI stage runs in it (ci-standard.md §2).
#
#   trivy        build-only: the official Trivy image, pinned by digest
#   tools        build-only: the pinned tools, built into /out, and trivy
#                copied from the trivy stage
#   modules      build-only: every module's cache, fetched with the forge
#                credentials
#   base         the Go toolchain by digest, the C compiler -race needs, a
#                non-root user and the verified module cache; GOPROXY=off
#   toolchain    base + every tool the Makefile calls. `make build`, `make test`,
#                `make security` and `make deps-check` run here, in CI too.
#   integration  base + the source only. `make integration-test` runs it under
#                the hardened compose service (compose.yaml beside this file).
#
# .devcontainer/ is a different image: the developer's VS Code container and
# the Docker-based test/integration/api stack. It runs no CI stage of this
# file's; its analyser ARG defaults are held to the same pin file by
# `make check-tool-versions`.
#
# Build with `make docker-build`: it passes GO_IMAGE, TRIVY_IMAGE and every tool
# version from scripts/tool-pins.sh, the one pin file the Makefile also reads,
# and the forge credentials as the gomodauth BuildKit secret. GO_IMAGE and
# TRIVY_IMAGE have no default here, so a build that bypasses the pin file fails
# instead of drifting. Requires BuildKit.
#
# The build context is the repository root, filtered by the root .dockerignore.
# A CI runner reaches its Docker daemon through a socket, so a bind mount would
# resolve on the daemon's host, where the checkout does not exist: integration
# bakes the source in, and CI copies the checkout into a toolchain container
# (scripts/in-toolchain-image.sh) instead of mounting it.

ARG GO_IMAGE
# The official Trivy image by digest (scripts/tool-pins.sh). No default, like
# GO_IMAGE.
ARG TRIVY_IMAGE
# Build-time module and tool downloads go through this proxy (devcontainer.md
# §4). The caller's environment supplies another; no tracked file does. It has
# no direct entry (ci-template v1.34.1): a module the proxy cannot serve fails
# the build instead of being fetched from its origin.
ARG GO_BUILD_PROXY=https://proxy.golang.org

# ---------------------------------------------------------------------------
FROM ${TRIVY_IMAGE} AS trivy

# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS tools
ARG GO_BUILD_PROXY
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOPROXY=${GO_BUILD_PROXY} GOBIN=/out
COPY scripts/tool-pins.sh /tmp/tool-pins.sh
# gosec builds in a throwaway module so x/tools can be raised to XTOOLS_VERSION
# (see tool-pins.sh); the others build with the x/tools their go.mod names.
# gosec takes its version string from ldflags rather than the module path, so
# without -X it self-reports `dev` and scripts/require-tool.sh cannot attribute
# a scan to a scanner version.
RUN set -eu; \
    . /tmp/tool-pins.sh; \
    go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"; \
    go install "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}"; \
    ( d="$(mktemp -d)"; cd "$d"; \
      go mod init gosec-build; \
      go get "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}" "golang.org/x/tools@${XTOOLS_VERSION}"; \
      go install -ldflags "-X main.Version=${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec; \
      cd /; rm -rf "$d" ); \
    go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
# trivy comes from its official image, pinned by digest (TRIVY_IMAGE). The
# check fails the build unless the binary reports exactly TRIVY_VERSION, so a
# digest and version pair that disagree fails it.
COPY --from=trivy /usr/local/bin/trivy /out/trivy
COPY scripts/check-trivy-version.sh /tmp/check-trivy-version.sh
RUN set -eu; \
    . /tmp/tool-pins.sh; \
    sh /tmp/check-trivy-version.sh /out/trivy "${TRIVY_VERSION}"

# ---------------------------------------------------------------------------
# github.com/jeremyhahn/* modules resolve from the forge through git, so this
# stage has git and base does not.
FROM ${GO_IMAGE} AS modules
ARG GO_BUILD_PROXY
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOMODCACHE=/gomodcache GOPROXY=${GO_BUILD_PROXY}
RUN apk add --no-cache git
WORKDIR /src

# Every module in the tree, at its own path: the `replace` directives point
# between them (the root at ./sdk/go, sdk/go at ../.., the conformance and
# clusterproof modules at both), so the layout is part of the module graph.
# The list is the Makefile's DEVCONTAINER_MODULES, which also hashes these
# files into the toolchain tag.
COPY go.mod go.sum ./
COPY sdk/go/go.mod sdk/go/go.sum sdk/go/
COPY sdk/go/gorm/conformance/go.mod sdk/go/gorm/conformance/go.sum sdk/go/gorm/conformance/
COPY test/clusterproof/go.mod test/clusterproof/go.sum test/clusterproof/
COPY test/performance/cmd/kvcompare/go.mod test/performance/cmd/kvcompare/go.sum test/performance/cmd/kvcompare/
COPY test/performance/etcd-benchmark/go.mod test/performance/etcd-benchmark/go.sum test/performance/etcd-benchmark/
COPY test/performance/qrdb/go.mod test/performance/qrdb/go.sum test/performance/qrdb/
COPY test/performance/tikv/go.mod test/performance/tikv/go.sum test/performance/tikv/
COPY test/vanguardprobe/go.mod test/vanguardprobe/go.sum test/vanguardprobe/

# Download the module caches in their own layer. Credentials for the project
# forge arrive as the optional gomodauth BuildKit secret (the Makefile's
# $(stage-gomodauth) payload), mounted for this RUN only: sourced, never
# written to a layer or a command line. With no secret, modules resolve from
# the public proxy, so a module only the forge serves fails the build here,
# loudly. Requires BuildKit.
RUN --mount=type=secret,id=gomodauth \
    set -eu; \
    if [ -s /run/secrets/gomodauth ]; then \
      set -a; . /run/secrets/gomodauth; set +a; \
      echo "==> go mod download: Gitea module credentials mounted"; \
    else \
      echo "==> go mod download: no module credentials mounted; github.com/jeremyhahn/* resolves from the public proxy only"; \
    fi; \
    for m in . sdk/go sdk/go/gorm/conformance test/clusterproof \
        test/performance/cmd/kvcompare test/performance/etcd-benchmark \
        test/performance/qrdb test/performance/tikv test/vanguardprobe; do \
      echo "==> go mod download ($m)"; \
      (cd "$m" && go mod download); \
    done
RUN set -eu; \
    for m in . sdk/go sdk/go/gorm/conformance test/clusterproof \
        test/performance/cmd/kvcompare test/performance/etcd-benchmark \
        test/performance/qrdb test/performance/tikv test/vanguardprobe; do \
      (cd "$m" && go mod verify); \
    done

# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS base
# GOTOOLCHAIN=local: a go.mod asking for a newer Go fails instead of
# downloading one. CGO for -race, and for go-mysql-server's MySQL REGEXP
# engine (go-icu-regex): under the default build tags it is C++ linking the
# system ICU, so base carries g++ and icu-dev, which pulls in icu-libs for
# every stage that runs a cgo-built binary. Only the release build drops it,
# with gms_pure_go (Makefile, RELEASE_TAGS);
# test/buildgates/toolchain_image_cgo_test.go holds the two together. bash is
# here, not in toolchain alone, because the integration suites exec it (the
# completion tests' `bash -n`); test/buildgates/integration_image_binaries_test.go
# holds the integration stage to every binary its tests exec.
ENV GOTOOLCHAIN=local \
    CGO_ENABLED=1 \
    GOFLAGS=-mod=readonly \
    GOMODCACHE=/gomodcache \
    GOCACHE=/gocache
RUN set -eu; \
    apk upgrade --no-cache; \
    apk add --no-cache gcc g++ musl-dev icu-dev bash; \
    addgroup -g 10001 dev; \
    adduser -D -u 10001 -G dev -h /home/dev dev; \
    install -d -m 0755 -o 10001 -g 10001 /src
COPY --from=modules /gomodcache /gomodcache
# Pre-warm the race-instrumented standard library, then open the build cache to
# any uid (the toolchain image runs as the invoking user).
RUN set -eu; \
    chmod -R a+rX /gomodcache; \
    go build -race std; \
    chmod -R a+rwX /gocache
# Nothing fetches a module at run time (devcontainer.md §4).
ENV GOPROXY=off

# ---------------------------------------------------------------------------
FROM base AS toolchain
ENV HOME=/tmp
RUN apk add --no-cache make git curl jq shellcheck \
      coreutils grep sed gawk findutils diffutils tar xz openssh-keygen; \
    git config --system --add safe.directory '*'
COPY --from=tools /out/ /usr/local/bin/
WORKDIR /src
USER dev:dev

# ---------------------------------------------------------------------------
FROM base AS integration
# The tag set the suites compile under: the Makefile's INT_FEATURE_TAGS
# (BUILD_TAGS plus integration), passed by `make docker-build-it`. No default,
# so a build that bypasses make fails here instead of vetting a narrower set.
ARG IT_TAGS
# The Alpine package index checksum (scripts/apk-index-sum.sh, ci-template
# v1.29), passed by `make docker-build-it`. A new value misses the cache from
# here on, so the upgrade takes Alpine's latest packages, a CVE fix among
# them; base and toolchain do not declare it, so their cache does not move.
ARG APK_INDEX_SUM
RUN apk upgrade --no-cache
WORKDIR /src
# The whole source, filtered by .dockerignore, owned by root so the suite
# cannot rewrite it. Not pkg and test alone: the qrdbdharness suites build the
# qrdbd and qrdb binaries from ./cmd at test time.
COPY --chown=root:root . ./
# Compile the suites here, so a broken build fails the image, and leave their
# packages in the cache for the run.
RUN set -eu; \
    test -n "${IT_TAGS}" || { echo "IT_TAGS is required (make docker-build-it passes it)" >&2; exit 1; }; \
    go vet -tags "${IT_TAGS}" ./test/integration; \
    chmod -R a+rwX /gocache
# HOME on /tmp, the compose service's /tmp volume: its root filesystem is read-only, and
# the qrdb CLI the suites drive resolves its config directory from HOME.
ENV HOME=/tmp
USER dev:dev
