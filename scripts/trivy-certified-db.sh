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
# trivy-certified-db.sh — download the vulnerability database and certify its
# age, preferring the cache and falling back to upstream (#451).
#
# The runner points trivy at a pull-through mirror through the environment
# (TRIVY_DB_REPOSITORY). A mirror can serve a database older than the gate
# allows, and then every scan is red for a reason no commit can fix. The rule is
# that a container which cannot get what it needs from the cache downloads it,
# so:
#
#   1. download through whatever the environment configures (the cache), and
#      certify it with check-trivy-db.sh;
#   2. if that download fails or the database is too old, discard it
#      (`trivy clean --vuln-db`) and download from each upstream repository in
#      turn, certifying after each;
#   3. if no source yields a certified database, fail closed.
#
# The age limit is passed through unchanged: a fallback widens where the
# database may come from, never how old it may be. The line that names the
# source is the one a reader of a green job needs.
#
# Usage: trivy-certified-db.sh [max-age-hours] [trivy-binary]
#
# TRIVY_DB_UPSTREAMS overrides the upstream list (space separated, tried in
# order); the default is trivy's documented pair. An entry written
# `http://<registry>/<repository>:<tag>` names a registry served over plain
# HTTP: the scheme is stripped and trivy's `--insecure` is passed for that
# entry ALONE, so it never reaches a TLS upstream. CI puts the forge's own
# Trivy DB bridge first this way, with the list scripts/trivy-db-upstreams.sh
# prints (docs/release/architecture.md, "CI tiers").

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

for entry in $UPSTREAMS; do
	"$BIN" clean --vuln-db || {
		echo "trivy-db: could not discard the cached database; refusing to certify." >&2
		exit 1
	}
	case "$entry" in
	http://*)
		repo="${entry#http://}"
		set -- --insecure
		;;
	*)
		repo="$entry"
		set --
		;;
	esac
	if "$BIN" fs --scanners vuln --download-db-only "$@" --db-repository "$repo" . &&
		"$CHECK" "$MAX_AGE_HOURS" "$BIN"; then
		echo "trivy-db source: upstream ${repo}"
		exit 0
	fi
	echo "trivy-db: ${repo} did not yield a database within ${MAX_AGE_HOURS}h." >&2
done

echo "FAIL: no source served a vulnerability database younger than ${MAX_AGE_HOURS}h (cache and upstream tried)." >&2
exit 1
