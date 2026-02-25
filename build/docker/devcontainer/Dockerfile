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

FROM golang:1.26-bookworm

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
    && rm -rf /var/lib/apt/lists/*

# Install CI and protobuf tooling.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.2 && \
    go install github.com/securego/gosec/v2/cmd/gosec@latest && \
    go install golang.org/x/vuln/cmd/govulncheck@latest && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

WORKDIR /workspace/go-qrdb

# Cache module downloads in a separate layer to speed up rebuilds.
# The replace directive for go-quicraft will fail during build (the
# sibling repo isn't in the build context), but || true allows the
# remaining modules to cache successfully.
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# Copy the full project source tree.
COPY . .

# Entrypoint configures QUIC UDP buffer sizes at container startup.
COPY build/docker/devcontainer/entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# Default command runs all integration tests.
CMD ["make", "_integration-test"]
