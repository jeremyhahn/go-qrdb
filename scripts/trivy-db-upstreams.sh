#!/bin/sh
#
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
#
# trivy-db-upstreams.sh — print the TRIVY_DB_UPSTREAMS list for a CI server.
#
# The ONE definition of where CI looks for a vulnerability database after the
# runner's cache. Every job that certifies a database (ci.yml's Trivy and
# Devcontainer image jobs, nightly.yml's Trivy job) writes this script's output
# to $GITHUB_ENV, and scripts/trivy-certified-db.sh reads it.
#
# On the project's own forge the list starts with the forge's Trivy DB bridge,
# the trivy-db:2 package its infrastructure team publishes in the forge's
# container registry (CA m450), then trivy's documented pair as the fallback.
# The bridge is served over plain HTTP, so its entry is written `http://...`,
# which tells trivy-certified-db.sh to pass trivy's --insecure for that entry
# alone. The registry address is derived from the server URL at run time and
# never written into the tree: the tracked tree is publicly releasable. On
# github.com, or with no server URL, the list is the documented pair alone.
#
# Usage: trivy-db-upstreams.sh [server-url]   (default: $GITHUB_SERVER_URL)

set -eu

DOCUMENTED="mirror.gcr.io/aquasec/trivy-db:2 ghcr.io/aquasecurity/trivy-db:2"
BRIDGE_REPOSITORY="agent-infrastructure/trivy-db:2"

server="${1-${GITHUB_SERVER_URL:-}}"
server="${server%/}"

case "$server" in
"" | https://github.com)
	echo "$DOCUMENTED"
	exit 0
	;;
http://* | https://*) ;;
*)
	echo "trivy-db-upstreams: server URL must be http:// or https://: '${server}'" >&2
	exit 2
	;;
esac

registry="${server#*://}"
case "$registry" in
"" | */* | *[[:space:]]*)
	echo "trivy-db-upstreams: server URL must be a bare origin: '${server}'" >&2
	exit 2
	;;
esac

echo "http://${registry}/${BRIDGE_REPOSITORY} ${DOCUMENTED}"
