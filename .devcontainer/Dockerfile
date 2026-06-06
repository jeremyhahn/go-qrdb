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

# Go 1.26.4+ is required: the go-objstore dependency declares `go 1.26.4`
# in its go.mod, and the official golang images pin GOTOOLCHAIN=local so a
# 1.26.3 base cannot satisfy that requirement (and cannot auto-upgrade).
# This must stay >= the highest `go` directive across the workspace modules.
FROM golang:1.26.4-bookworm

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
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.2 && \
    go install github.com/securego/gosec/v2/cmd/gosec@latest && \
    go install golang.org/x/vuln/cmd/govulncheck@latest && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

WORKDIR /workspace/go-qrdb

# Cache third-party module downloads in a separate layer to speed up
# rebuilds. go.mod carries NO local replace directives (it builds against
# upstream-published versions); the sibling repos (go-quicraft, go-truststrap,
# go-objstore) are unpublished placeholders (v0.0.0) resolved by a Go
# workspace at runtime. They are not in the build context here, so to build a
# module graph that lets `go mod download` fetch the third-party deps we
# create empty STUB modules for the siblings and a TEMPORARY go.work that
# `use`s them. The workspace and stubs are discarded after caching; the real
# siblings are mounted and resolved by /workspace/go.work at runtime.
COPY go.mod go.sum* ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN mkdir -p /tmp/stubs/go-quicraft /tmp/stubs/go-truststrap /tmp/stubs/go-objstore && \
    printf 'module github.com/jeremyhahn/go-quicraft\n\ngo 1.26.4\n' > /tmp/stubs/go-quicraft/go.mod && \
    printf 'module github.com/jeremyhahn/go-truststrap\n\ngo 1.26.4\n' > /tmp/stubs/go-truststrap/go.mod && \
    printf 'module github.com/jeremyhahn/go-objstore\n\ngo 1.26.4\n' > /tmp/stubs/go-objstore/go.mod && \
    printf 'go 1.26.4\n\nuse (\n\t.\n\t./sdk/go\n)\n\nreplace github.com/jeremyhahn/go-qrdb v0.0.0 => .\nreplace github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0 => ./sdk/go\nreplace github.com/jeremyhahn/go-quicraft => /tmp/stubs/go-quicraft\nreplace github.com/jeremyhahn/go-truststrap => /tmp/stubs/go-truststrap\nreplace github.com/jeremyhahn/go-objstore => /tmp/stubs/go-objstore\n' > go.work && \
    GONOSUMCHECK='github.com/jeremyhahn/*' GONOSUMDB='github.com/jeremyhahn/*' \
    GOFLAGS=-mod=mod go mod download 2>/dev/null || true; \
    rm -f go.work go.work.sum

# Bake the runtime Go workspace that resolves the locally-mounted modules.
# go.mod carries no local replace directives, so this workspace is what wires
# the in-tree sibling repos (go-quicraft, go-truststrap, go-objstore) and the
# sdk/go submodule during integration/client builds. The referenced paths are
# populated by the docker-compose volume mounts at runtime; every go build/test
# runs from /workspace/go-qrdb and walks up to auto-discover this /workspace/go.work
# (so do NOT pass GOWORK=off in the entrypoints).
RUN printf 'go 1.26.4\n\nuse (\n\t./go-qrdb\n\t./go-qrdb/sdk/go\n)\n\nreplace github.com/jeremyhahn/go-qrdb v0.0.0 => ./go-qrdb\nreplace github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0 => ./go-qrdb/sdk/go\nreplace github.com/jeremyhahn/go-quicraft => ./go-quicraft\nreplace github.com/jeremyhahn/go-truststrap => ./go-truststrap\nreplace github.com/jeremyhahn/go-objstore => ./go-objstore\n' > /workspace/go.work

# Copy the full project source tree.
COPY . .

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY .devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# Default command runs all integration tests.
CMD ["make", "_integration-test"]
