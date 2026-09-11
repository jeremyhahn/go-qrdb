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
# Regression tests for check-trivy-db.sh.
#
# On any host where the suite is meant to run, the database is fresh -- that is
# the point -- so running the check for real exercises only its ACCEPTING
# branch, and the rejecting branch it exists for would ship untested. Same
# reasoning as check-bundled-gosec_test.sh and check-tool-versions_test.sh, and
# it matters more here: the property under test is that a gate FAILS rather
# than warns, and an assertion nobody has seen fail is not that.
#
# THE CENTRAL CASE IS REAL MEASURED OUTPUT. `stale_and_unexpired` is the shape
# trivy v0.74.0 actually reported from a cache carrying a June database that
# declared NextUpdate in 2027: trivy scanned this tree with it, offline, and
# exited 0. The fixture is that state, and the check must refuse it.
#
# NO DATE ARITHMETIC HERE, deliberately. `date -d '25 hours ago'` is a GNU
# extension, and a test that needs one is a test that quietly stops running on
# a BusyBox image -- the same portability trap the subject avoids with awk. So
# the comparison is exercised in both directions by holding the timestamp fixed
# and MOVING THE LIMIT, which needs no clock at all. The one case that does
# involve the wall clock -- a database stamped `now` -- asserts the printed age
# is `0h`, which is an end-to-end check of the civil-days conversion: any error
# of an hour or more in it would print something else.
#
# `trivy` is faked on a REPLACED PATH rather than a prepended one, so the
# "scanner is absent" case can actually make it absent even on a host that has
# a real trivy installed.
#
# Usage: check-trivy-db_test.sh    (no network, no trivy, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-trivy-db.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

PATH="$work/bin:/usr/bin:/bin"
export PATH

pass=0
fail=0

# faketrivy <json> -- a trivy whose `version --format json` prints the given
# document. Any other invocation fails: the subject calls it exactly one way,
# and a fake that tolerates more would hide a subject that asks for more.
faketrivy() {
	{
		echo '#!/bin/sh'
		echo 'if [ "$1" = "version" ] && [ "$2" = "--format" ] && [ "$3" = "json" ]; then'
		echo "cat <<'EOF'"
		printf '%s\n' "$1"
		echo 'EOF'
		echo 'exit 0'
		echo 'fi'
		echo 'echo "unexpected trivy invocation: $*" >&2'
		echo 'exit 1'
	} > "$work/bin/trivy"
	chmod +x "$work/bin/trivy"
}

faketrivy_broken() {
	printf '#!/bin/sh\necho "trivy: cannot initialise cache" >&2\nexit 1\n' > "$work/bin/trivy"
	chmod +x "$work/bin/trivy"
}

# expect <accept|reject> <label> <want-substring> <check args...>
expect() {
	want="$1"
	label="$2"
	want_text="$3"
	shift 3
	if "$CHECK" "$@" >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	if [ "$got" != "$want" ]; then
		printf '  FAIL  %-62s want %s, got %s\n' "$label" "$want" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
		return
	fi
	if [ -n "$want_text" ]; then
		if ! grep -q -- "$want_text" "$work/out"; then
			printf '  FAIL  %-62s output did not mention: %s\n' "$label" "$want_text"
			sed 's/^/          /' "$work/out"
			fail=$((fail + 1))
			return
		fi
	fi
	printf '  PASS  %-62s (%s)\n' "$label" "$got"
	pass=$((pass + 1))
}

# The real document shape, captured from trivy v0.74.0. UpdatedAt carries
# nanoseconds there, so every fixture that stands in for a live database does
# too -- a parser that only handled whole seconds would pass a test written
# from memory and fail on the first real run.
report() {
	printf '{"Version":"0.74.0","VulnerabilityDB":{"Version":2,"NextUpdate":"%s","UpdatedAt":"%s","DownloadedAt":"%s"}}' \
		"$2" "$1" "$1"
}

NOW=$(date -u +%Y-%m-%dT%H:%M:%S.000000000Z)

echo "== the accepting branch: a database from just now =="
faketrivy "$(report "$NOW" 2099-01-01T00:00:00Z)"
expect accept "database stamped now, limit 24h" "vulnerability database is 0h old"

echo "== the measured false green: June data declaring it is not due until 2027 =="
# trivy scanned this tree with exactly this metadata, offline, and exited 0.
# Refusing it is the whole deliverable of go-qrdb#451.
stale_and_unexpired=$(report 2026-06-30T00:00:00.000000000Z 2027-01-01T00:00:00Z)
faketrivy "$stale_and_unexpired"
expect reject "stale database that trivy considers unexpired" "fails closed instead"
expect reject "the refusal names the refresh command" "--download-db-only"

echo "== the limit is the lever, not the fixture =="
# Same database, a limit wide enough to admit it. Both directions of the
# comparison are exercised without asking what time it is.
expect accept "same database under a limit that admits it" "vulnerability database is" 999999

echo "== the JavaDB has an UpdatedAt too, and it is not this gate's subject =="
faketrivy "$(printf '{"Version":"0.74.0","VulnerabilityDB":{"Version":2,"NextUpdate":"2027-01-01T00:00:00Z","UpdatedAt":"2026-06-30T00:00:00.000000000Z"},"JavaDB":{"Version":1,"NextUpdate":"2099-01-01T00:00:00Z","UpdatedAt":"%s"}}' "$NOW")"
expect reject "stale vulnerability DB behind a fresh JavaDB" "updated 2026-06-30"
faketrivy "$(printf '{"Version":"0.74.0","VulnerabilityDB":{"Version":2,"NextUpdate":"2099-01-01T00:00:00Z","UpdatedAt":"%s"},"JavaDB":{"Version":1,"NextUpdate":"2027-01-01T00:00:00Z","UpdatedAt":"2026-06-30T00:00:00.000000000Z"}}' "$NOW")"
expect accept "fresh vulnerability DB behind a stale JavaDB" "vulnerability database is 0h old"

echo "== the discovery floor: nothing to read is never a pass =="
faketrivy '{"Version":"0.74.0"}'
expect reject "empty cache, trivy prints only its own version" "reports no vulnerability database"
faketrivy '{"Version":"0.74.0","VulnerabilityDB":{"Version":2,"NextUpdate":"2099-01-01T00:00:00Z"}}'
expect reject "vulnerability DB object with no UpdatedAt" "no UpdatedAt"
faketrivy "$(report yesterday 2099-01-01T00:00:00Z)"
expect reject "UpdatedAt is not a timestamp" "not an RFC 3339 UTC timestamp"
faketrivy "$(report 2026-09-11T07:00:51+02:00 2099-01-01T00:00:00Z)"
expect reject "UpdatedAt carries an offset instead of Z" "not an RFC 3339 UTC timestamp"
faketrivy "$(report 2099-01-01T00:00:00.000000000Z 2099-06-01T00:00:00Z)"
expect reject "UpdatedAt is in the future" "in the future"

echo "== a scanner that cannot answer has not answered =="
faketrivy_broken
expect reject "trivy version exits non-zero" "did not answer"
rm -f "$work/bin/trivy"
expect reject "trivy absent" "is not installed"

echo "== the limit itself is validated, so a typo cannot widen the gate =="
faketrivy "$stale_and_unexpired"
expect reject "limit of zero hours" "positive whole number" 0
expect reject "non-numeric limit" "positive whole number" twenty-four
expect reject "negative limit" "positive whole number" -1

echo
echo "check-trivy-db.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
