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
# Regression tests for check-trivy-version.sh's version comparison.
#
# The image builds only ever exercise the ACCEPTING branch: the pinned digest
# reports the pinned version. The rejecting branches are the reason the check
# exists, so they are cases here. The check was once a substring (case-glob)
# test for `Version: ${TRIVY_VERSION#v}`, which accepts `0.75.0-rc1`, `0.75.0-dev`
# and `0.75.01` for a v0.75.0 pin; `--mutate` re-loosens the comparison in a
# COPY of the script to prove the near-miss cases still catch it.
#
# The accepting fake prints trivy's real output format: its own version on an
# unindented `Version: ` line, the databases' versions on indented ones.
#
# Usage: check-trivy-version_test.sh [--mutate]   (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subject="${SCRIPT_DIR}/check-trivy-version.sh"
mutate=""
[ "${1:-}" = "--mutate" ] && mutate=1

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

if [ -n "$mutate" ]; then
	# Reinstate the substring comparison the real script refuses to use.
	# shellcheck disable=SC2016  # a sed script: expansion here would be the bug
	sed -e 's|^if \[ "$got" != "${pin#v}" \]; then$|if ! (case "$report" in *"Version: ${pin#v}"*) true;; *) false;; esac); then|' \
		"$subject" > "$work/subject.sh"
	chmod +x "$work/subject.sh"
	subject="$work/subject.sh"
	# shellcheck disable=SC2016  # a literal pattern, not an expansion
	if ! grep -q 'case "$report" in' "$subject"; then
		echo "MUTATION DID NOT APPLY — the line it rewrites has changed." >&2
		echo "These tests cannot claim to have been verified. Fix the mutation." >&2
		exit 1
	fi
	echo "### MUTATED: comparison loosened back to a substring test"
fi

fake() { # fake <stdout...>
	{
		echo '#!/bin/sh'
		echo 'cat <<'"'"'EOF'"'"''
		printf '%s\n' "$@"
		echo 'EOF'
	} > "$work/bin/trivy"
	chmod +x "$work/bin/trivy"
}

# fake_db <version>: trivy's real layout, with indented database versions.
fake_db() {
	fake "Version: $1" 'Vulnerability DB:' '  Version: 2' \
		'  UpdatedAt: 2026-11-08 00:00:00 +0000 UTC' \
		'Java DB:' '  Version: 1'
}

pass=0
fail=0
expect() { # expect <accept|reject> <label> <pin>
	want="$1"
	label="$2"
	if "$subject" "$work/bin/trivy" "$3" >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	if [ "$got" = "$want" ]; then
		printf '  PASS  %-58s (%s)\n' "$label" "$got"
		pass=$((pass + 1))
	else
		printf '  FAIL  %-58s want %s, got %s\n' "$label" "$want" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
	fi
}

echo "== real output, matching pin: must ACCEPT =="
fake 'Version: 0.75.0'
expect accept "0.75.0 vs pin v0.75.0" v0.75.0
fake_db 0.75.0
expect accept "0.75.0 with DB version lines vs pin v0.75.0" v0.75.0
expect accept "0.75.0 vs bare pin 0.75.0" 0.75.0

echo "== NEAR MISS: the substring hole. Must REJECT =="
fake_db 0.75.0-rc1
expect reject "0.75.0-rc1 vs pin v0.75.0  (prerelease)" v0.75.0
fake_db 0.75.0-dev
expect reject "0.75.0-dev vs pin v0.75.0  (dev build)" v0.75.0
fake_db 0.75.01
expect reject "0.75.01 vs pin v0.75.0  (suffix digit)" v0.75.0
fake_db 10.75.0
expect reject "10.75.0 vs pin v0.75.0  (prefix digit)" v0.75.0

echo "== a different version: must REJECT =="
fake_db 0.74.0
expect reject "0.74.0 vs pin v0.75.0" v0.75.0

echo "== unparseable / unreadable: must REJECT =="
fake 'trivy, some format nobody documented'
expect reject "output with no Version: line" v0.75.0
fake 'Vulnerability DB:' '  Version: 0.75.0'
expect reject "only an indented (database) Version: line" v0.75.0
fake 'Version: 0.75.0' 'Version: 0.74.0'
expect reject "two unindented Version: lines" v0.75.0
{
	echo '#!/bin/sh'
	echo 'exit 3'
} > "$work/bin/trivy"
chmod +x "$work/bin/trivy"
expect reject "trivy --version exits non-zero" v0.75.0
rm -f "$work/bin/trivy"
expect reject "trivy binary absent" v0.75.0

echo ""
echo "passed ${pass}, failed ${fail}"

if [ -n "$mutate" ]; then
	if [ "$fail" -eq 0 ]; then
		echo "MUTATION SURVIVED: loosening the comparison to a substring broke" >&2
		echo "NOTHING. These tests do not test the property they claim to." >&2
		exit 1
	fi
	echo "mutation killed by ${fail} case(s) — the near-miss tests are real."
	exit 0
fi

[ "$fail" -eq 0 ] || exit 1
