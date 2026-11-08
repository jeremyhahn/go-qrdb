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
# Regression tests for check-iface-sweep-findings.sh.
#
# The committed findings/*.tsv are clean on a healthy tree, so running the
# checker against them only ever exercises its ACCEPTING branch -- exactly
# the gap #271's review noted about check-publication-bound.sh's own scope,
# and the same shape require-tool_test.sh exists to close for that script.
# Every REJECTING branch is the entire reason this checker exists (an
# absolute path is precisely what shipped once already, uncaught, in this
# tool's first revision) and is proven here against synthetic fixtures, not
# assumed from the real tree currently passing.
#
# Usage: check-iface-sweep-findings_test.sh   (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subject="${SCRIPT_DIR}/check-iface-sweep-findings.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

header='package	interface	method	exported	called_root	called_sdk	implementers_in_repo	file	line'

# row <file-column-value> -> one well-formed data row with that file value.
row() {
	printf 'github.com/example/x\tIface\tMethod\ttrue\ttrue\tfalse\t1\t%s\t42\n' "$1"
}

# write <name> <row-count> <file-column-template> -> a fixture TSV in $work
# with COUNT data rows, each using the given file column value (varied by
# index so rows are distinct, which does not matter to the checker but
# keeps the fixtures honest-looking).
write() {
	name="$1"
	count="$2"
	fcol="$3"
	{
		printf '%s\n' "$header"
		i=0
		while [ "$i" -lt "$count" ]; do
			row "$fcol"
			i=$((i + 1))
		done
	} > "$work/$name"
}

pass=0
fail=0
expect() { # expect <accept|reject> <label> <file...>
	want="$1"
	label="$2"
	shift 2
	if "$subject" "$@" >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	if [ "$got" = "$want" ]; then
		printf '  PASS  %-62s (%s)\n' "$label" "$got"
		pass=$((pass + 1))
	else
		printf '  FAIL  %-62s want %s, got %s\n' "$label" "$want" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
	fi
}

echo "== a clean, above-floor fixture: must ACCEPT =="
write clean.tsv 150 "cmd/qrdbd/start.go"
expect accept "150 clean repo-relative rows" "$work/clean.tsv"

echo "== THE DEFECT THIS SCRIPT EXISTS FOR: an absolute path row. Must REJECT =="
write absolute.tsv 150 "/home/dev/sources/go-qrdb/cmd/qrdbd/start.go"
expect reject "absolute POSIX path in the file column" "$work/absolute.tsv"

echo "== a Windows-style drive-absolute path. Must REJECT =="
write windrive.tsv 150 "C:\\Users\\dev\\go-qrdb\\cmd\\qrdbd\\start.go"
expect reject "drive-absolute path in the file column" "$work/windrive.tsv"

echo "== an upward-escaping relative path. Must REJECT =="
write escape.tsv 150 "../outside/start.go"
expect reject "path escapes the module root via ../" "$work/escape.tsv"

write escapeonly.tsv 150 ".."
expect reject "path is exactly .." "$work/escapeonly.tsv"

echo "== DISCOVERY FLOOR: must REJECT rather than pass vacuously =="
printf '' > "$work/empty.tsv"
expect reject "empty file (no header)" "$work/empty.tsv"

printf '%s\n' "$header" > "$work/headeronly.tsv"
expect reject "header-only file, zero data rows" "$work/headeronly.tsv"

write toofew.tsv 3 "cmd/qrdbd/start.go"
expect reject "3 data rows, below the floor" "$work/toofew.tsv"

expect reject "file does not exist at all" "$work/does-not-exist.tsv"

echo "== malformed header: must REJECT, not silently check the wrong column =="
{
	printf 'package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\twrongname\tline\n'
	i=0
	while [ "$i" -lt 150 ]; do
		row "cmd/qrdbd/start.go"
		i=$((i + 1))
	done
} > "$work/nocol.tsv"
expect reject "header has no 'file' column" "$work/nocol.tsv"

echo "== column reordered: the check must follow the NAME, not a hardcoded index =="
{
	printf 'file\tpackage\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\tline\n'
	i=0
	while [ "$i" -lt 150 ]; do
		printf 'cmd/qrdbd/start.go\tgithub.com/example/x\tIface\tMethod\ttrue\ttrue\tfalse\t1\t42\n'
		i=$((i + 1))
	done
} > "$work/reordered.tsv"
expect accept "file column moved to index 1, still found by name" "$work/reordered.tsv"

echo "== multiple files given together: one bad file must fail the whole run =="
expect reject "clean + absolute together" "$work/clean.tsv" "$work/absolute.tsv"

echo "== empty argument: must REJECT rather than silently check zero files =="
expect reject "empty string argument resolves to no files" ""

echo ""
echo "passed ${pass}, failed ${fail}"
[ "$fail" -eq 0 ]
