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

# The cluster proof's fault image (#1619): the tools the default nemesis
# commands run in a fault container. partition drop/heal: iptables and
# ip6tables (iptables); disk-full fill/free: df, dd, rm (coreutils), awk
# (mawk) and fallocate (util-linux). Built by `make cluster-proof-fault-image`.
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends iptables coreutils mawk util-linux && \
    rm -rf /var/lib/apt/lists/*
