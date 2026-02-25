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

# Increase UDP receive/send buffer limits for QUIC transport.
# quic-go requires at least 7168 KiB (~7.3 MB); we set 7500000 bytes
# to provide headroom. See:
# https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes
sysctl -w net.core.rmem_max=7500000 >/dev/null 2>&1 || true
sysctl -w net.core.wmem_max=7500000 >/dev/null 2>&1 || true

exec "$@"
