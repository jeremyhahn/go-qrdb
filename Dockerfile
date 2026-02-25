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
FROM golang:1.26-bookworm AS builder

WORKDIR /build

# Cache dependency downloads.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build.
COPY . .
RUN CGO_ENABLED=1 go build \
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

COPY --from=builder /build/bin/qrdbd /usr/local/bin/qrdbd
COPY --from=builder /build/configs/qrdb.yaml /etc/qrdb/qrdb.yaml

# Raft transport, REST admin, gRPC
EXPOSE 63001 63002 63003

ENTRYPOINT ["qrdbd"]
CMD ["start", "--config", "/etc/qrdb/qrdb.yaml"]
