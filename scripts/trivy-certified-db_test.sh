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
# Regression tests for trivy-certified-db.sh: cache first, upstream when the
# cache is stale, and closed when nothing is fresh.
#
# `trivy` is a fake whose database lives in a state file. A download with no
# --db-repository is served by the "cache" and a download naming a repository
# by that upstream; each source's UpdatedAt comes from an environment variable,
# or "fail" for an unreachable source. The real check-trivy-db.sh runs against
# the fake, so the age arithmetic is the production one.
#
# Usage: trivy-certified-db_test.sh    (no network, no trivy, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SUBJECT="${SCRIPT_DIR}/trivy-certified-db.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

PATH="$work/bin:/usr/bin:/bin"
export PATH
STATE="$work/db"
export STATE

cat > "$work/bin/trivy" <<'EOF'
#!/bin/sh
case "$1" in
version)
	if [ -s "$STATE" ]; then
		printf '{"Version":"0.74.0","VulnerabilityDB":{"Version":2,"NextUpdate":"2099-01-01T00:00:00Z","UpdatedAt":"%s"}}\n' "$(cat "$STATE")"
	else
		echo '{"Version":"0.74.0"}'
	fi
	;;
clean)
	: > "$STATE"
	;;
fs)
	src=cache
	prev=""
	for a in "$@"; do
		[ "$prev" = "--db-repository" ] && src="$a"
		prev="$a"
	done
	case "$src" in
	cache) at="$FAKE_CACHE" ;;
	mirror.gcr.io/*) at="$FAKE_MIRROR" ;;
	ghcr.io/*) at="$FAKE_GHCR" ;;
	esac
	[ "$at" = fail ] && exit 1
	printf '%s' "$at" > "$STATE"
	;;
esac
EOF
chmod +x "$work/bin/trivy"

NOW=$(date -u +%Y-%m-%dT%H:%M:%S.000000000Z)
OLD=2026-06-30T00:00:00.000000000Z

pass=0
fail=0

# run <label> <accept|reject> <want-substring> -- env FAKE_CACHE/MIRROR/GHCR set by caller
run() {
	label="$1"
	want="$2"
	text="$3"
	: > "$STATE"
	if "$SUBJECT" >"$work/out" 2>&1; then got=accept; else got=reject; fi
	if [ "$got" != "$want" ] || ! grep -q -- "$text" "$work/out"; then
		printf '  FAIL  %-58s want %s with "%s", got %s\n' "$label" "$want" "$text" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
		return
	fi
	printf '  PASS  %-58s (%s)\n' "$label" "$got"
	pass=$((pass + 1))
}

export FAKE_CACHE FAKE_MIRROR FAKE_GHCR

FAKE_CACHE=$NOW FAKE_MIRROR=$OLD FAKE_GHCR=$OLD
run "fresh cache is used and named" accept "source: configured cache"

FAKE_CACHE=$OLD FAKE_MIRROR=$NOW FAKE_GHCR=$OLD
run "stale cache, fresh upstream: passes and names upstream" accept "source: upstream mirror.gcr.io"

FAKE_CACHE=fail FAKE_MIRROR=$NOW FAKE_GHCR=$OLD
run "unreachable cache falls back to upstream" accept "source: upstream mirror.gcr.io"

FAKE_CACHE=$OLD FAKE_MIRROR=fail FAKE_GHCR=$NOW
run "second upstream is tried when the first fails" accept "source: upstream ghcr.io"

FAKE_CACHE=$OLD FAKE_MIRROR=$OLD FAKE_GHCR=$OLD
run "everything stale: fails closed" reject "no source served"

FAKE_CACHE=$OLD FAKE_MIRROR=fail FAKE_GHCR=fail
run "stale cache, upstream unreachable: fails closed" reject "no source served"

echo
echo "trivy-certified-db.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
