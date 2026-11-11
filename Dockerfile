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
FROM golang:1.27.4-bookworm AS builder

WORKDIR /build

# Cache dependency downloads.
#
# go.mod pins first-party modules -- go-quicraft, go-truststrap, go-objstore,
# go-codec -- published only to the internal Gitea, and a docker build inherits
# nothing from its caller's environment. The four variables from
# dependency-resolution.md section 6 therefore arrive as a BuildKit secret,
# never a build arg and never an ENV: an arg is recoverable from image metadata
# with `docker history`, while a secret is a tmpfs mount for this RUN alone. It
# is sourced rather than exported, so the token never reaches a command line a
# `ps` could show. The mount is optional by design -- on GitHub no secret is
# supplied and these modules resolve from the public proxy once tags are
# mirrored.
#
# This step used to end `go mod download 2>/dev/null || true`, which threw away
# the diagnostic AND the exit code (#305). A credential-less build therefore
# SUCCEEDED here with an empty module cache and died in the `go build` below,
# reporting a missing package rather than a missing credential -- cause and
# symptom separated by the one thing the `|| true` was there to avoid. The
# download now fails the build at the download, carrying its real stderr, and
# the line printed above it says which of the two worlds this build is in.
#
# Refusing on the not-mounted branch rather than reporting it would need a way
# to tell an internal build from a GitHub one. No such discriminator has been
# designed -- the four properties one would have to satisfy are recorded on
# #302 -- and choosing one is the owner's call, so this stops at saying what
# happened, exactly as .devcontainer/Dockerfile does.
#
# The stub modules stand in for any sibling `replace => ../go-quicraft`
# directive, whose path does not exist inside the build. Measured on this
# revision: go.mod carries no such directive (`grep -c '=> \.\./' go.mod` is
# 0), so both `sed` calls are no-ops today. They are kept because the
# standalone-build note below still describes that arrangement, and a sed that
# matches nothing costs nothing.
COPY go.mod go.sum ./
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

# Locked memory. qrdbd keeps cluster keys, credentials and PINs in mlock'd
# pages, admitted against a process-wide secure-memory budget. An image cannot
# grant itself the capability or the ulimit that needs; they are RUN-time
# settings, so run this image with both:
#
#   docker run --cap-add IPC_LOCK --ulimit memlock=-1:-1 ...
#
# (compose: `cap_add: [IPC_LOCK]` and `ulimits: {memlock: {soft: -1, hard: -1}}`).
# Without them an unconfigured node derives a smaller budget from Docker's
# default RLIMIT_MEMLOCK and logs it at startup, and an explicitly configured
# budget above that limit refuses to start. See docs/deployment/.
LABEL io.qrdb.requires.cap-add="IPC_LOCK" \
      io.qrdb.requires.ulimit="memlock=-1:-1"

ENTRYPOINT ["/app/qrdbd"]
CMD ["start", "--config", "/etc/qrdb/qrdb.yaml"]
