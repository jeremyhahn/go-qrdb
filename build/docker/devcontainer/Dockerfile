# go-qrdb CI image: one Dockerfile, the shipped stages built from base.

ARG GO_IMAGE
# The official Trivy image by digest (scripts/tool-pins.sh).
ARG TRIVY_IMAGE
# Build-time module and tool downloads go through this proxy.
ARG GO_BUILD_PROXY=https://proxy.golang.org

# ---------------------------------------------------------------------------
FROM ${TRIVY_IMAGE} AS trivy

# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS tools
ARG GO_BUILD_PROXY
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOPROXY=${GO_BUILD_PROXY} GOBIN=/out
COPY scripts/tool-pins.sh /tmp/tool-pins.sh
# gosec builds in its own module with XTOOLS_VERSION and its version stamped by -ldflags.
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
# trivy comes from its official image, pinned by digest.
COPY --from=trivy /usr/local/bin/trivy /out/trivy
COPY scripts/check-trivy-version.sh /tmp/check-trivy-version.sh
RUN set -eu; \
    . /tmp/tool-pins.sh; \
    sh /tmp/check-trivy-version.sh /out/trivy "${TRIVY_VERSION}"

# First-party modules resolve through git, so this stage has git.
FROM ${GO_IMAGE} AS modules
ARG GO_BUILD_PROXY
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOMODCACHE=/gomodcache GOPROXY=${GO_BUILD_PROXY}
RUN apk add --no-cache git
WORKDIR /src

# Every module in the tree at its own path, because the replace directives point between them.
COPY go.mod go.sum ./
COPY sdk/go/go.mod sdk/go/go.sum sdk/go/
COPY sdk/go/gorm/conformance/go.mod sdk/go/gorm/conformance/go.sum sdk/go/gorm/conformance/
COPY test/clusterproof/go.mod test/clusterproof/go.sum test/clusterproof/
COPY test/performance/cmd/kvcompare/go.mod test/performance/cmd/kvcompare/go.sum test/performance/cmd/kvcompare/
COPY test/performance/etcd-benchmark/go.mod test/performance/etcd-benchmark/go.sum test/performance/etcd-benchmark/
COPY test/performance/qrdb/go.mod test/performance/qrdb/go.sum test/performance/qrdb/
COPY test/performance/tikv/go.mod test/performance/tikv/go.sum test/performance/tikv/
COPY test/vanguardprobe/go.mod test/vanguardprobe/go.sum test/vanguardprobe/

# Download the module caches in their own layer.
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
# GOTOOLCHAIN=local: a go.mod asking for a newer Go fails instead of downloading one.
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
# Pre-warm the race-instrumented standard library, then open the build cache to any uid.
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
# The tag set the suites compile under, passed by `make docker-build-it`.
ARG IT_TAGS
# The Alpine package index checksum, passed by `make docker-build-it`.
ARG APK_INDEX_SUM
RUN apk upgrade --no-cache
WORKDIR /src
# The whole source, owned by root so the suite cannot rewrite it.
COPY --chown=root:root . ./
# Compile the suites here, so a broken build fails the image.
RUN set -eu; \
    test -n "${IT_TAGS}" || { echo "IT_TAGS is required (make docker-build-it passes it)" >&2; exit 1; }; \
    go vet -tags "${IT_TAGS}" ./test/integration; \
    chmod -R a+rwX /gocache
# HOME on /tmp because the root filesystem is read-only.
ENV HOME=/tmp
USER dev:dev

# Go 1.27.4 is required by the module graph.
FROM golang:1.27.4-bookworm AS dev

# Optional dev-cache proxies.
ARG DEVCACHE_APT_PROXY=""
ARG DEVCACHE_NPM_REGISTRY=""

# curl for the reachability probes below.
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install build, test, and debugging dependencies.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends \
    make \
    git \
    bc \
    jq \
    xxd \
    openssl \
    net-tools \
    iproute2 \
    iputils-ping \
    protobuf-compiler \
    build-essential \
    pkg-config \
    libgtk-3-dev \
    libwebkit2gtk-4.1-dev \
    gnupg \
    && rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache

# Install Node.js 20 from NodeSource.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache; \
    if [ -n "$DEVCACHE_NPM_REGISTRY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_NPM_REGISTRY"; then \
        npm config set registry "$DEVCACHE_NPM_REGISTRY" --global; \
    fi

# Install Playwright Chromium dependencies for E2E tests.
RUN set -eu; \
    if [ -n "$DEVCACHE_APT_PROXY" ] && curl -fsS --max-time 3 -o /dev/null "$DEVCACHE_APT_PROXY"; then \
        echo "Acquire::http::Proxy \"$DEVCACHE_APT_PROXY\";" > /etc/apt/apt.conf.d/01devcache; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends \
    libnss3 \
    libnspr4 \
    libdbus-1-3 \
    libatk1.0-0 \
    libatk-bridge2.0-0 \
    libcups2 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    libatspi2.0-0 \
    libxshmfence1 \
    && rm -rf /var/lib/apt/lists/*; \
    rm -f /etc/apt/apt.conf.d/01devcache

# Install CI and protobuf tooling.
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOSEC_VERSION=v2.29.0
ARG GOVULNCHECK_VERSION=v1.8.0
ARG TRIVY_VERSION=v0.75.0

# An explicitly empty version is an error.
RUN set -eu; \
    empty=''; \
    [ -n "${GOLANGCI_LINT_VERSION:-}" ] || empty="$empty GOLANGCI_LINT_VERSION"; \
    [ -n "${GOSEC_VERSION:-}" ]         || empty="$empty GOSEC_VERSION"; \
    [ -n "${GOVULNCHECK_VERSION:-}" ]   || empty="$empty GOVULNCHECK_VERSION"; \
    [ -n "${TRIVY_VERSION:-}" ]         || empty="$empty TRIVY_VERSION"; \
    [ -z "$empty" ] || { \
        echo "ERROR: this image pins its analysers and was passed an EMPTY version for:$empty" >&2; \
        echo "  These ARGs have defaults, so an empty value did not come from" >&2; \
        echo "  omitting them -- something passed the empty string explicitly." >&2; \
        echo "  The two ways that happens:" >&2; \
        echo "    * --build-arg GOSEC_VERSION=   with nothing after the '='" >&2; \
        echo "    * a compose build arg written GOSEC_VERSION: \${GOSEC_VERSION}" >&2; \
        echo "      while that variable is unset. Use the passthrough form" >&2; \
        echo "      'GOSEC_VERSION:' with no value, which omits the arg instead" >&2; \
        echo "      and lets the default apply." >&2; \
        echo "  The pins live in the Makefile, which exports them; ci.yml is the" >&2; \
        echo "  authority and #323 checks the two agree." >&2; \
        exit 1; \
    }

# gosec needs -ldflags for its version string.
RUN for i in 1 2 3; do \
        go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} && \
        go install -ldflags "-X main.Version=${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION} && \
        go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} && \
        go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
        go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
        exit 0; \
        echo "tool install attempt $i failed; retrying"; sleep 10; \
    done; \
    echo "tool install failed after 3 attempts"; exit 1
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Every scanner `make security` runs must be present at its pinned version.
COPY scripts/require-tool.sh /usr/local/bin/require-tool.sh
COPY scripts/check-trivy-version.sh /usr/local/bin/check-trivy-version.sh
RUN set -eu; \
    require-tool.sh golangci-lint "${GOLANGCI_LINT_VERSION}" github.com/golangci/golangci-lint/v2/cmd/golangci-lint; \
    require-tool.sh gosec "${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec; \
    require-tool.sh govulncheck "${GOVULNCHECK_VERSION}" golang.org/x/vuln/cmd/govulncheck; \
    command -v trivy >/dev/null 2>&1 || { \
        echo "ERROR: trivy is not on PATH after the tool install" >&2; exit 1; }; \
    check-trivy-version.sh "$(command -v trivy)" "${TRIVY_VERSION}"

WORKDIR /workspace/go-qrdb

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY build/docker/devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# The dev image has no source; the devcontainer bind mount supplies it.
CMD ["sleep", "infinity"]

# dev-integration: the same toolchain with the code under test baked in.
FROM dev AS dev-deps

WORKDIR /workspace/go-qrdb

# Pre-fetch the module graph so the image needs no credentials at run time.
COPY go.mod go.sum* ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN --mount=type=secret,id=gomodauth \
    set -eu; \
    if [ -s /run/secrets/gomodauth ]; then \
      . /run/secrets/gomodauth; \
      echo "==> go mod download: Gitea module credentials MOUNTED"; \
    else \
      echo "==> go mod download: NO Gitea module credentials mounted -- github.com/jeremyhahn/* can resolve from the public proxy ONLY (correct on GitHub; on an internal host this build cannot see unmirrored tags). See dependency-resolution.md section 6."; \
    fi; \
    go mod download; \
    (cd sdk/go && go mod download)


FROM dev-deps AS dev-integration
COPY . .

# QRDB_BUILD_TAGS defaults to the Makefile's BUILD_TAGS for builds that bypass make.
ARG QRDB_BUILD_TAGS="codec_json raft qrdb_ts qrdb_vector qrdb_vlog qrdb_sql qrdb_mysql qrdb_pgsql qrdb_etcd qrdb_embedded"

# Build the binaries the stack runs, at image build time.
RUN CGO_ENABLED=1 go build -buildvcs=false \
      -tags "${QRDB_BUILD_TAGS}" \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdbd ./cmd/qrdbd \
 && CGO_ENABLED=1 go build -buildvcs=false \
      -tags "${QRDB_BUILD_TAGS}" \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdb ./cmd/qrdb

# The suite drives the servers; it does not sit idle.
CMD ["make", "_integration-test"]

# Stage 1: download the Go distribution.
FROM ubuntu:24.04 AS gpu-go

ARG GO_VERSION=1.27.4
ARG TARGETARCH=amd64

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Resolve the architecture token used by go.dev tarballs (amd64 / arm64).
RUN ARCH="${TARGETARCH:-amd64}" && \
    curl -fsSL "https://dl.google.com/go/go${GO_VERSION}.linux-${ARCH}.tar.gz" \
        -o /tmp/go.tar.gz && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz

# Stage 2: CUDA devel base with Go and the project toolchain.
FROM nvidia/cuda:13.2.0-devel-ubuntu24.04 AS gpu

# NVIDIA Container Toolkit runtime hints.
ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility

# Prepend the CUDA toolkit binaries (nvcc, etc.) and Go to PATH.
ENV PATH=/usr/local/cuda/bin:/usr/local/go/bin:/root/go/bin:$PATH

# Make CUDA shared libraries discoverable by the dynamic linker.
ENV LD_LIBRARY_PATH=/usr/local/cuda/lib64:${LD_LIBRARY_PATH}

# Standard Go environment variables.
ENV GOPATH=/root/go
ENV GOROOT=/usr/local/go

# Bring the Go distribution from the download stage.
COPY --from=gpu-go /usr/local/go /usr/local/go

# Install build, test, and debugging dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    make \
    git \
    bc \
    curl \
    net-tools \
    iproute2 \
    iputils-ping \
    protobuf-compiler \
    build-essential \
    pkg-config \
    libgtk-3-dev \
    libwebkit2gtk-4.1-dev \
    ca-certificates \
    gnupg \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 20 from NodeSource (required for @tailwindcss/oxide).
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Playwright Chromium dependencies for E2E tests.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libnss3 \
    libnspr4 \
    libdbus-1-3 \
    libatk1.0-0t64 \
    libatk-bridge2.0-0t64 \
    libcups2t64 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2t64 \
    libatspi2.0-0t64 \
    libxshmfence1 \
    && rm -rf /var/lib/apt/lists/*

# Install CI and protobuf tooling.
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOSEC_VERSION=v2.29.0
ARG GOVULNCHECK_VERSION=v1.8.0

# Defaults equal the Makefile's pins; an explicitly empty value is an error.
RUN set -eu; \
    empty=''; \
    [ -n "${GOLANGCI_LINT_VERSION:-}" ] || empty="$empty GOLANGCI_LINT_VERSION"; \
    [ -n "${GOSEC_VERSION:-}" ]         || empty="$empty GOSEC_VERSION"; \
    [ -n "${GOVULNCHECK_VERSION:-}" ]   || empty="$empty GOVULNCHECK_VERSION"; \
    [ -z "$empty" ] || { \
        echo "ERROR: this image pins its analysers and was passed an EMPTY version for:$empty" >&2; \
        echo "  These ARGs have defaults, so something passed the empty string" >&2; \
        echo "  explicitly -- a bare '--build-arg X=', or a compose arg written" >&2; \
        echo "  X: \${X} while that variable is unset. See the dev stage." >&2; \
        exit 1; \
    }

# gosec needs -ldflags for its version string.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} && \
    go install -ldflags "-X main.Version=${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION} && \
    go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Assert what was installed, with the same script ci.yml uses.
COPY scripts/require-tool.sh /usr/local/bin/require-tool.sh
RUN set -eu; \
    require-tool.sh golangci-lint "${GOLANGCI_LINT_VERSION}" github.com/golangci/golangci-lint/v2/cmd/golangci-lint; \
    require-tool.sh gosec "${GOSEC_VERSION}" github.com/securego/gosec/v2/cmd/gosec; \
    require-tool.sh govulncheck "${GOVULNCHECK_VERSION}" golang.org/x/vuln/cmd/govulncheck

WORKDIR /workspace/go-qrdb

# Cache module downloads in a separate layer.
COPY go.mod go.sum* ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN --mount=type=secret,id=gomodauth \
    set -eu; \
    mkdir -p /tmp/stubs/go-quicraft /tmp/stubs/go-truststrap; \
    printf 'module github.com/jeremyhahn/go-quicraft\n\ngo 1.27.4\n' > /tmp/stubs/go-quicraft/go.mod; \
    printf 'module github.com/jeremyhahn/go-truststrap\n\ngo 1.27.4\n' > /tmp/stubs/go-truststrap/go.mod; \
    cp go.mod go.mod.orig; \
    sed -i 's|=> \.\./go-quicraft|=> /tmp/stubs/go-quicraft|' go.mod; \
    sed -i 's|=> \.\./go-truststrap|=> /tmp/stubs/go-truststrap|' go.mod; \
    if [ -s /run/secrets/gomodauth ]; then \
      . /run/secrets/gomodauth; \
      echo "==> go mod download: Gitea module credentials MOUNTED"; \
    else \
      echo "==> go mod download: NO Gitea module credentials mounted -- github.com/jeremyhahn/* can resolve from the public proxy ONLY (correct on GitHub; on an internal host this build cannot see unmirrored tags). See dependency-resolution.md section 6."; \
    fi; \
    GOFLAGS=-mod=mod go mod download; \
    mv go.mod.orig go.mod

# Copy the full project source tree.
COPY . .

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY build/docker/devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# Default command runs all integration tests.
CMD ["make", "_integration-test"]
