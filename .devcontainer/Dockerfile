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

FROM golang:1.26.1-bookworm

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

# Cache module downloads in a separate layer to speed up rebuilds.
# Replace directives point to sibling repos (../go-quicraft, etc.)
# that aren't in the build context. We temporarily swap them to
# stub directories so `go mod download` can resolve the module
# graph and cache all remote dependencies.
COPY go.mod go.sum* ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN mkdir -p /tmp/stubs/go-quicraft /tmp/stubs/go-truststrap && \
    printf 'module github.com/jeremyhahn/go-quicraft\n\ngo 1.26.1\n' > /tmp/stubs/go-quicraft/go.mod && \
    printf 'module github.com/jeremyhahn/go-truststrap\n\ngo 1.26.1\n' > /tmp/stubs/go-truststrap/go.mod && \
    cp go.mod go.mod.orig && \
    sed -i 's|=> \.\./go-quicraft|=> /tmp/stubs/go-quicraft|' go.mod && \
    sed -i 's|=> \.\./go-truststrap|=> /tmp/stubs/go-truststrap|' go.mod && \
    GONOSUMCHECK='github.com/jeremyhahn/*' GONOSUMDB='github.com/jeremyhahn/*' \
    GOFLAGS=-mod=mod go mod download 2>/dev/null || true; \
    mv go.mod.orig go.mod

# Copy the full project source tree.
COPY . .

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY .devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# Default command runs all integration tests.
CMD ["make", "_integration-test"]
