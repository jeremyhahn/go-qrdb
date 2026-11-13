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
# Regression tests for `make test-cross`: it compiles all three release test
# modules (the root, $(SDK_DIR) and test/clusterproof), plain and FIPS, with
# the tag sets of the targets that test them, for the requested GOOS/GOARCH
# with CGO_ENABLED=0 and -exec /bin/true; it refuses a missing or malformed
# PLATFORM before compiling anything; and a failed compilation fails make and
# stops every later compilation.
#
# GO is a recording stub: each call appends its directory (relative to the
# repository root), GOOS, GOARCH, GOFIPS140, CGO_ENABLED and argv to a log,
# and fails when its directory is $FAIL_DIR. Nothing is compiled.
#
# Usage: test-cross_test.sh    (no network, no go toolchain needed)

set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

export ROOT
export LOG="$work/log"
export FAIL_DIR=

# The stub records one line per call: dir|GOOS|GOARCH|GOFIPS140|CGO_ENABLED|argv,
# each argument in brackets so a space inside the -tags value stays visible.
cat >"$work/go" <<'EOF'
#!/bin/sh
dir=${PWD#"$ROOT"}
dir=${dir#/}
[ -n "$dir" ] || dir=.
line="$dir|${GOOS-}|${GOARCH-}|${GOFIPS140-}|${CGO_ENABLED-}|"
for a in "$@"; do line="$line[$a]"; done
printf '%s\n' "$line" >>"$LOG"
[ "$dir" != "$FAIL_DIR" ] || { echo "stub go: compile failed in $dir" >&2; exit 1; }
exit 0
EOF
chmod +x "$work/go"

# mkvar <name> prints the Makefile's value of a variable.
mkvar() {
	make -s --no-print-directory -C "$ROOT" -f Makefile --eval "print-var:;@echo \$($1)" print-var
}
fips=$(mkvar RELEASE_FIPS_MODULE)
release_tags=$(mkvar RELEASE_TAGS)
min_tags=$(mkvar MIN_TAGS)
sdk_dir=$(mkvar SDK_DIR)
[ -n "$fips" ] && [ "$fips" != off ] || { echo "RELEASE_FIPS_MODULE is '$fips'" >&2; exit 1; }
[ -n "$release_tags" ] && [ -n "$min_tags" ] && [ -n "$sdk_dir" ] ||
	{ echo "RELEASE_TAGS, MIN_TAGS or SDK_DIR is empty" >&2; exit 1; }

# call <dir> <fips> <tags> prints the line the stub records for one compilation.
call() {
	printf '%s|linux|arm64|%s|0|[test][-count=1][-vet=off][-exec][/bin/true][-tags][%s][./...]\n' "$1" "$2" "$3"
}
want="$work/want"
for f in off "$fips"; do
	call . "$f" "$release_tags"
	call . "$f" "$release_tags integration"
	call "$sdk_dir" "$f" "$release_tags"
	call "$sdk_dir" "$f" "$release_tags integration"
	call test/clusterproof "$f" "$min_tags,clusterproof"
done >"$want"

pass=0
fail=0

ok() {
	printf '  PASS  %s\n' "$1"
	pass=$((pass + 1))
}

bad() {
	printf '  FAIL  %s: %s\n' "$1" "$2"
	[ ! -s "$work/out" ] || sed 's/^/          /' "$work/out"
	[ ! -s "$LOG" ] || { echo '        stub calls:'; sed 's/^/          /' "$LOG"; }
	fail=$((fail + 1))
}

# run <fail-dir> <make-args>... runs make test-cross with the stub; sets rc.
run() {
	FAIL_DIR="$1"
	shift
	: >"$LOG"
	rc=0
	make -s --no-print-directory -C "$ROOT" test-cross GO="$work/go" "$@" >"$work/out" 2>&1 || rc=$?
}

label="PLATFORM=linux/arm64 compiles 3 modules x 2 tag sets x plain/FIPS"
run '' PLATFORM=linux/arm64
if [ "$rc" -ne 0 ]; then
	bad "$label" "make exited $rc"
elif ! diff -u "$want" "$LOG" >"$work/diff"; then
	cp "$work/diff" "$work/out"
	bad "$label" "stub calls differ from the expected $(wc -l <"$want") (diff -u want got)"
else
	ok "$label"
fi

for p in '' arm64; do
	label="PLATFORM='$p' is refused before any compilation"
	if [ -n "$p" ]; then run '' PLATFORM="$p"; else run ''; fi
	if [ "$rc" -ne 2 ]; then
		bad "$label" "make exited $rc, want 2"
	elif ! grep -qF 'test-cross: set PLATFORM=<goos>/<goarch>' "$work/out"; then
		bad "$label" "no 'set PLATFORM=' message"
	elif [ -s "$LOG" ]; then
		bad "$label" "the stub was called"
	else
		ok "$label"
	fi
done

# fails <dir> <n> asserts a stub failing in <dir> fails make after exactly the
# first <n> expected calls (the failing one last) and runs none after it.
fails() {
	local dir="$1" n="$2" label="a failed compilation in $1 fails make and stops"
	run "$dir" PLATFORM=linux/arm64
	if [ "$rc" -eq 0 ]; then
		bad "$label" "make exited 0"
	elif ! head -n "$n" "$want" | diff -u - "$LOG" >"$work/diff"; then
		cp "$work/diff" "$work/out"
		bad "$label" "stub calls are not the first $n expected (diff -u want got)"
	else
		ok "$label"
	fi
}
fails "$sdk_dir" 3
fails test/clusterproof 5

echo
echo "test-cross_test.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
