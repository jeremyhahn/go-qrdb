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
# trivy-certified-db.sh: download the vulnerability database and certify its age.

set -u

MAX_AGE_HOURS="${1:-24}"
BIN="${2:-trivy}"
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-trivy-db.sh"
UPSTREAMS="${TRIVY_DB_UPSTREAMS:-mirror.gcr.io/aquasec/trivy-db:2 ghcr.io/aquasecurity/trivy-db:2}"

if "$BIN" fs --scanners vuln --download-db-only . && "$CHECK" "$MAX_AGE_HOURS" "$BIN"; then
	echo "trivy-db source: configured cache (${TRIVY_DB_REPOSITORY:-trivy default})"
	exit 0
fi

echo "trivy-db: the configured cache did not yield a database within ${MAX_AGE_HOURS}h; discarding it and trying upstream." >&2

for repo in $UPSTREAMS; do
	"$BIN" clean --vuln-db || {
		echo "trivy-db: could not discard the cached database; refusing to certify." >&2
		exit 1
	}
	if "$BIN" fs --scanners vuln --download-db-only --db-repository "$repo" . &&
		"$CHECK" "$MAX_AGE_HOURS" "$BIN"; then
		echo "trivy-db source: upstream ${repo}"
		exit 0
	fi
	echo "trivy-db: ${repo} did not yield a database within ${MAX_AGE_HOURS}h." >&2
done

echo "FAIL: no source served a vulnerability database younger than ${MAX_AGE_HOURS}h (cache and upstream tried)." >&2
exit 1
