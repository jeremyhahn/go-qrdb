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

# --------------------------------------------------------------------------
# Stage 1: Build
# --------------------------------------------------------------------------
FROM golang:1.26.1-bookworm AS builder

WORKDIR /build

# Cache dependency downloads.
# The go.mod has replace directives pointing to sibling repos (../go-quicraft,
# ../go-truststrap) and ./sdk/go. We stub the sibling repos and copy the SDK
# module manifest so go mod download can cache all remote dependencies.
COPY go.mod go.sum ./
COPY sdk/go/go.mod sdk/go/go.sum* sdk/go/
RUN mkdir -p /tmp/stubs/go-quicraft /tmp/stubs/go-truststrap && \
    printf 'module github.com/jeremyhahn/go-quicraft\n\ngo 1.26.1\n' > /tmp/stubs/go-quicraft/go.mod && \
    printf 'module github.com/jeremyhahn/go-truststrap\n\ngo 1.26.1\n' > /tmp/stubs/go-truststrap/go.mod && \
    cp go.mod go.mod.orig && \
    sed -i 's|=> \.\./go-quicraft|=> /tmp/stubs/go-quicraft|' go.mod && \
    sed -i 's|=> \.\./go-truststrap|=> /tmp/stubs/go-truststrap|' go.mod && \
    GONOSUMCHECK='github.com/jeremyhahn/*' GONOSUMDB='github.com/jeremyhahn/*' \
    GOFLAGS=-mod=mod go mod download 2>/dev/null || true && \
    mv go.mod.orig go.mod

# Copy source and build.
# NOTE: Standalone builds require vendored dependencies (go mod vendor)
# because sibling replace directives (../go-quicraft, ../go-truststrap)
# are outside the Docker build context.
COPY . .
ARG BUILD_TAGS="codec_json raft qrdb_ts qrdb_vector"
RUN CGO_ENABLED=1 go build \
    -tags "${BUILD_TAGS}" \
    -ldflags "-X main.version=$(cat VERSION) -X main.buildDate=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
    -o /build/bin/qrdbd \
    ./cmd/qrdbd

# --------------------------------------------------------------------------
# Stage 2: Runtime
# --------------------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/bin/qrdbd /app/qrdbd
COPY --from=builder /build/configs/qrdb.yaml /etc/qrdb/qrdb.yaml

# Raft transport, REST admin, gRPC, QUIC
EXPOSE 63001 63002 63003 63004

ENTRYPOINT ["/app/qrdbd"]
CMD ["start", "--config", "/etc/qrdb/qrdb.yaml"]
