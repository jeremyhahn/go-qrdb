#!/bin/sh
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

# A module-forge token without the forge's address is a half-configured
# .devcontainer/.env. The Makefile's stage-gomodauth refuses it; compose cannot
# express the same condition, so it hands the two facts in and this refuses it
# at start (see the client-build service in docker-compose.yml). Without the
# refusal git would silently skip the rewrite and resolve from the public
# proxy, which is not what a machine holding a token asked for.
if [ -n "${QRDB_FORGE_TOKEN_SET:-}" ] && [ -z "${QRDB_FORGE_HOST:-}" ]; then
    echo "ERROR: .devcontainer/.env sets GITEA_GO_TOKEN but not GITEA_HOST." >&2
    echo "Add GITEA_HOST=<host:port> of the module forge (see .devcontainer/.env.example)." >&2
    exit 1
fi

# Increase UDP receive/send buffer limits for QUIC transport.
# quic-go requires at least 7168 KiB (~7.3 MB); we set 7500000 bytes
# to provide headroom. See:
# https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes
sysctl -w net.core.rmem_max=7500000 >/dev/null 2>&1 || true
sysctl -w net.core.wmem_max=7500000 >/dev/null 2>&1 || true

# Mark the workspace as a safe git directory so that Go tooling and
# golangci-lint can read VCS metadata when the repo is volume-mounted
# from a host user with a different UID.
git config --global --add safe.directory /workspace/go-qrdb 2>/dev/null || true
git config --global --add safe.directory /workspace/go-quicraft 2>/dev/null || true
git config --global --add safe.directory /workspace/go-truststrap 2>/dev/null || true

# Probe for NVIDIA GPU and log status for diagnostics.
if command -v nvidia-smi >/dev/null 2>&1 && [ -e /dev/nvidia0 ]; then
    echo "GPU: NVIDIA device detected"
    nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader 2>/dev/null || true
else
    echo "GPU: not detected (CPU-only mode)"
fi

exec "$@"
