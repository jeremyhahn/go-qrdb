# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Go 1.26.5+ is required: the go-quicraft dependency declares `go 1.26.5`
# in its go.mod, and the official golang images pin GOTOOLCHAIN=local so an
# older base cannot satisfy that requirement (and cannot auto-upgrade).
# This must stay >= the highest `go` directive across the workspace modules.
FROM golang:1.26.6-bookworm AS toolchain

# Install build, test, and debugging dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    make \
    git \
    bc \
    curl \
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
    && rm -rf /var/lib/apt/lists/*

# Install CI and protobuf tooling.
#
# RETRIED (2026-08-20): same sum.golang.org flake class as the CI jobs' tool
# installs. This image build is a THIRD site for it, and the one that hurts
# most: `make integration-image` failing here fails the whole Integration job
# before a single test runs. Full sumdb verification is kept — GOSUMDB=off
# would trade a flake for a supply-chain hole. Bounded retry is inert on a
# healthy network: it only fires on failure, and a local build gets the same
# three attempts a runner does. Durable fix: bake these into the base image.
RUN for i in 1 2 3; do \
        go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.2 && \
        go install github.com/securego/gosec/v2/cmd/gosec@latest && \
        go install golang.org/x/vuln/cmd/govulncheck@latest && \
        go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
        go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
        exit 0; \
        echo "tool install attempt $i failed; retrying"; sleep 10; \
    done; \
    echo "tool install failed after 3 attempts"; exit 1

WORKDIR /workspace/go-qrdb

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY .devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# The image ends here for development: no source, so the devcontainer service's
# bind mount supplies it and a developer's edits are live without a rebuild.
CMD ["sleep", "infinity"]

# ---------------------------------------------------------------------------
# integration — the same toolchain with the code under test baked in.
# ---------------------------------------------------------------------------
#
# CI uses this stage and mounts no source. Bind-mounting the checkout cannot
# work from a Gitea job container: compose reaches the HOST daemon through the
# mounted socket, so a host path resolves on the host, and the job's checkout
# lives at /workspace/<owner>/<repo> inside the job container -- a path the
# runner host does not have. Compose declares create_host_path, so the daemon
# manufactures an empty directory and mounts that; nothing fails until the
# build inside the container cannot find the tree.
#
# This stage also replaces the Go workspace this image used to bake. That
# workspace `replace`d go-quicraft, go-truststrap and go-objstore to sibling
# checkouts supplied by compose bind mounts, which is the resolution model
# retired at the 2026-08 cutover and prohibited by
# conventions/dependency-resolution.md section 2. Those modules now resolve
# from their Gitea tags like every other dependency, which is why the
# entrypoints no longer need to avoid GOWORK=off.
FROM toolchain AS integration

WORKDIR /workspace/go-qrdb

# Pre-fetch the module graph so the finished image needs no credentials at run
# time, and a credential problem fails the BUILD with a clear message instead
# of surfacing halfway through a suite.
#
# The token arrives as a BuildKit secret, never a build arg or ENV: an arg is
# recoverable from image metadata with `docker history`, while a secret is a
# tmpfs mount for this RUN alone. It is sourced rather than exported so the
# value never reaches a command line either. The mount is optional by design --
# on GitHub no secret is supplied and these modules resolve from the public
# proxy once tags are mirrored (dependency-resolution.md section 6).
#
# The echo before the download states which of those two worlds this build is
# in. It changes no behaviour in either -- an empty or absent secret still
# falls through to the proxy exactly as before -- it only puts the one fact a
# reader needs at the top of the failure instead of leaving them to infer it
# from 80 lines of `unknown revision` further down (#302). Deciding to REFUSE
# here rather than report needs a way to tell an internal build from a GitHub
# one, and no such discriminator has been designed; that is the owner's call,
# so this stops at saying what happened.
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
    go mod download

COPY . .

# Build the binaries the stack runs, at image build time rather than at
# container start.
#
# The entrypoints used to compile qrdbd on every boot, into a volume shared
# with the healthcheck's grace period. That works on a workstation with a warm
# build cache and does not on a runner, where the cache is empty every time:
# the compile outruns start_period and compose reports only "container is
# unhealthy", naming neither the build nor the clock. Baking them makes
# container start a server start, so the health window measures what it is
# supposed to measure.
#
# The feature tags match what the compose entrypoints used, and CGO is
# required -- go-mysql-server's internal/regex has no pure-Go build.
RUN CGO_ENABLED=1 go build -buildvcs=false \
      -tags 'codec_json raft qrdb_ts qrdb_vector' \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdbd ./cmd/qrdbd \
 && CGO_ENABLED=1 go build -buildvcs=false \
      -tags 'qrdb_ts qrdb_vector' \
      -ldflags '-X main.version=test -X main.gitCommit=test -X main.buildDate=test' \
      -o /app/build/bin/qrdb ./cmd/qrdb

# The suite drives the servers; it does not sit idle.
CMD ["make", "_integration-test"]
