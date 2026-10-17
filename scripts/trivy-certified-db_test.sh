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
CALLS="$work/calls"
export STATE CALLS

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
	insecure=no
	prev=""
	for a in "$@"; do
		[ "$prev" = "--db-repository" ] && src="$a"
		[ "$a" = "--insecure" ] && insecure=yes
		prev="$a"
	done
	echo "$src insecure=$insecure" >> "$CALLS"
	case "$src" in
	cache) at="$FAKE_CACHE" ;;
	# The bridge speaks plain HTTP only: without --insecure trivy cannot reach it.
	bridge.example.com/*) if [ "$insecure" = yes ]; then at="$FAKE_BRIDGE"; else at=fail; fi ;;
	mirror.gcr.io/*) at="$FAKE_MIRROR" ;;
	ghcr.io/*) at="$FAKE_GHCR" ;;
	*) at=fail ;;
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
	: > "$CALLS"
	if "$SUBJECT" >"$work/out" 2>&1; then got=accept; else got=reject; fi
	# --insecure is for the plain-HTTP bridge alone; a TLS upstream never sees it.
	if grep -q -e '^mirror.gcr.io/.* insecure=yes' -e '^ghcr.io/.* insecure=yes' "$CALLS"; then
		printf '  FAIL  %-58s --insecure reached a TLS upstream\n' "$label"
		sed 's/^/          /' "$CALLS"
		fail=$((fail + 1))
		return
	fi
	if [ "$got" != "$want" ] || ! grep -q -- "$text" "$work/out"; then
		printf '  FAIL  %-58s want %s with "%s", got %s\n' "$label" "$want" "$text" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
		return
	fi
	printf '  PASS  %-58s (%s)\n' "$label" "$got"
	pass=$((pass + 1))
}

FAKE_BRIDGE=fail
export FAKE_CACHE FAKE_MIRROR FAKE_GHCR FAKE_BRIDGE

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

# The forge's Trivy DB bridge, first in the list, from the one definition CI
# configures it with.
TRIVY_DB_UPSTREAMS=$("${SCRIPT_DIR}/trivy-db-upstreams.sh" https://bridge.example.com)
export TRIVY_DB_UPSTREAMS

FAKE_CACHE=$OLD FAKE_BRIDGE=$NOW FAKE_MIRROR=$NOW FAKE_GHCR=$NOW
run "bridge is certified first, scheme stripped" accept "source: upstream bridge.example.com/agent-infrastructure/trivy-db:2"

FAKE_CACHE=$OLD FAKE_BRIDGE=$OLD FAKE_MIRROR=$NOW FAKE_GHCR=$OLD
run "stale bridge falls back to the next upstream" accept "source: upstream mirror.gcr.io"

FAKE_CACHE=$OLD FAKE_BRIDGE=fail FAKE_MIRROR=fail FAKE_GHCR=$NOW
run "unreachable bridge falls back to the next upstream" accept "source: upstream ghcr.io"

FAKE_CACHE=$OLD FAKE_BRIDGE=$OLD FAKE_MIRROR=$OLD FAKE_GHCR=$OLD
run "bridge and upstreams stale: fails closed" reject "no source served"

# A bridge entry without the http:// prefix gets no --insecure and cannot be
# reached, so the prefix is what carries the option.
TRIVY_DB_UPSTREAMS="bridge.example.com/agent-infrastructure/trivy-db:2 ghcr.io/aquasecurity/trivy-db:2"
FAKE_CACHE=$OLD FAKE_BRIDGE=$NOW FAKE_GHCR=$NOW
run "bridge without http:// gets no --insecure" accept "source: upstream ghcr.io"

echo
echo "trivy-certified-db.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
