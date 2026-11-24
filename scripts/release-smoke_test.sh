#!/usr/bin/env bash
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
# Regression tests for scripts/release-smoke.sh (make release-smoke): it runs
# `<artifact> version` over every plain and -fips artifact of the host
# platform, the -fips ones under GODEBUG=fips140=only and the plain ones
# without GODEBUG; it reports every missing, non-executable, failing or
# wrongly versioned artifact before it fails; and it refuses a malformed or
# non-host platform before running anything.
#
# GO is a stub answering `env GOHOSTOS` / `env GOHOSTARCH` with $STUB_HOSTOS /
# $STUB_HOSTARCH. The artifacts are small shell scripts that log their name
# and GODEBUG, print "QuicRaft DB v<version>" and honour per-artifact knobs:
# <file>.exit (exit status) and <file>.ver (version printed). Nothing is built.
#
# Usage: release-smoke_test.sh    (no network, no go toolchain needed)

set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SMOKE="$SCRIPT_DIR/release-smoke.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

export LOG="$work/log"
export STUB_HOSTOS=linux STUB_HOSTARCH=amd64
ver=1.2.3-test
dist="$work/dist"

cat >"$work/go" <<'EOF'
#!/bin/sh
case "$*" in
"env GOHOSTOS") echo "$STUB_HOSTOS" ;;
"env GOHOSTARCH") echo "$STUB_HOSTARCH" ;;
*) echo "stub go: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/go"

# setup writes the good set of four stub artifacts for linux/amd64.
setup() {
	rm -rf "$dist"
	mkdir -p "$dist"
	: >"$LOG"
	for b in qrdbd qrdb; do
		for v in '' -fips; do
			f="$dist/$b-$ver-linux-amd64$v"
			cat >"$f" <<EOF
#!/bin/sh
printf '%s|%s\n' "\$(basename "\$0")" "\${GODEBUG-<unset>}" >>"\$LOG"
want=v$ver
[ ! -f "\$0.ver" ] || want=\$(cat "\$0.ver")
echo "boot line one"
echo "QuicRaft DB \$want"
[ ! -f "\$0.exit" ] || { echo "panic: stub failure" >&2; exit "\$(cat "\$0.exit")"; }
exit 0
EOF
			chmod +x "$f"
		done
	done
}

pass=0
fail=0

ok() {
	printf '  PASS  %s\n' "$1"
	pass=$((pass + 1))
}

bad() {
	printf '  FAIL  %s: %s\n' "$1" "$2"
	[ ! -s "$work/out" ] || sed 's/^/          /' "$work/out"
	[ ! -s "$LOG" ] || { echo '        artifact runs:'; sed 's/^/          /' "$LOG"; }
	fail=$((fail + 1))
}

# run <platform> runs release-smoke.sh on $dist; sets rc, output in $work/out.
run() {
	rc=0
	GODEBUG=inherited=1 RS_DIR="$dist" RS_VERSION="$ver" RS_PLATFORM="$1" \
		RS_BINARIES='qrdbd qrdb' GO="$work/go" "$SMOKE" >"$work/out" 2>&1 || rc=$?
}

# expect <label> <rc> <line>... asserts the exit status and each exact line.
expect() {
	local label="$1" want_rc="$2" line
	shift 2
	if [ "$rc" -ne "$want_rc" ]; then
		bad "$label" "exited $rc, want $want_rc"
		return
	fi
	for line in "$@"; do
		if ! grep -qxF -- "$line" "$work/out"; then
			bad "$label" "no line '$line'"
			return
		fi
	done
	ok "$label"
}

a=qrdbd-$ver-linux-amd64
c=qrdb-$ver-linux-amd64

setup
run linux/amd64
label="the good set runs, -fips under GODEBUG=fips140=only, plain without GODEBUG"
printf '%s\n' "$a|<unset>" "$a-fips|fips140=only" "$c|<unset>" "$c-fips|fips140=only" >"$work/want"
if ! diff -u "$work/want" "$LOG" >"$work/diff"; then
	cat "$work/diff" >>"$work/out"
	bad "$label" "artifact runs differ (diff -u want got)"
else
	expect "$label" 0 "release-smoke: OK linux/amd64: 4 artifacts ran"
fi

setup
rm "$dist/$c-fips"
run linux/amd64
expect "a missing artifact is a finding" 1 \
	"release-smoke: $c-fips: missing" "release-smoke: FAILED linux/amd64: 1 findings"

setup
chmod -x "$dist/$a"
run linux/amd64
expect "a non-executable artifact is a finding" 1 \
	"release-smoke: $a: not executable" "release-smoke: FAILED linux/amd64: 1 findings"

setup
echo 1 >"$dist/$a-fips.exit"
run linux/amd64
expect "an artifact exiting 1 is a finding with its output tail" 1 \
	"release-smoke: $a-fips: version exited 1; last output: boot line one|QuicRaft DB v$ver|panic: stub failure|" \
	"release-smoke: FAILED linux/amd64: 1 findings"

setup
echo v9.9.9 >"$dist/$c.ver"
run linux/amd64
expect "a wrong version is a finding" 1 \
	"release-smoke: $c: version printed 'QuicRaft DB v9.9.9', want v$ver" \
	"release-smoke: FAILED linux/amd64: 1 findings"

setup
rm "$dist/$a"
echo 3 >"$dist/$c-fips.exit"
run linux/amd64
label="two broken artifacts are both reported before it fails"
if [ "$(wc -l <"$LOG")" -ne 3 ]; then
	bad "$label" "$(wc -l <"$LOG") artifacts ran, want 3"
else
	expect "$label" 1 "release-smoke: $a: missing" \
		"release-smoke: $c-fips: version exited 3; last output: boot line one|QuicRaft DB v$ver|panic: stub failure|" \
		"release-smoke: FAILED linux/amd64: 2 findings"
fi

for p in '' linux; do
	setup
	run "$p"
	label="PLATFORM='$p' is refused before anything runs"
	if [ -s "$LOG" ]; then
		bad "$label" "an artifact ran"
	else
		expect "$label" 2 \
			"release-smoke: set PLATFORM=<goos>/<goarch>, e.g. make release-smoke PLATFORM=linux/arm64 (got '$p')"
	fi
done

setup
run linux/arm64
label="PLATFORM=linux/arm64 on an amd64 host is refused before anything runs"
if [ -s "$LOG" ]; then
	bad "$label" "an artifact ran"
else
	expect "$label" 2 \
		"release-smoke: linux/arm64 artifacts cannot run natively on this linux/amd64 host; run it on a linux/arm64 host"
fi

echo
echo "release-smoke_test.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
