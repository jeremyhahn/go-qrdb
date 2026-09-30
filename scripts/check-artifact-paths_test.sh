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
# Regression tests for check-artifact-paths.sh.
#
# The committed generated artifacts are clean on a healthy tree, so running
# the checker against the real tree only ever exercises its ACCEPTING branch
# -- the same gap check-iface-sweep-findings_test.sh and
# check-publication-bound_test.sh both exist to close for their own checkers.
# Every REJECTING branch is proven here against synthetic fixtures.
#
# FIXTURES LIVE AT THE REAL REPO-RELATIVE IDENTITY, under $work: e.g.
# $work/test/coverage-baseline.txt, $work/test/tools/deadcode/baseline.txt,
# $work/test/tools/ifacesweep/findings/min-tags.tsv, $work/lockstep-baseline.
# check-artifact-paths.sh dispatches its per-artifact ALLOWLIST by matching
# the full path a fixture is given at against one of the five known
# identities as a SUFFIX, precisely so that basename or extension alone
# cannot alias an unrelated file onto the wrong validator -- which is also
# why this file's "unknown artifact" section places fixtures OUTSIDE those
# exact identities on purpose.
#
# Every absolute-path SHAPE this guard's review history has produced is
# exercised once, in the coverage-baseline.txt fixture, to pin the shared
# IMPORT_PATH/REPO_RELATIVE/SYMBOL patterns themselves. The other three
# artifact shapes then get a lighter pass -- one accept, a couple of rejects
# -- to prove the per-format DISPATCH and field-location logic, since the
# patterns doing the actual work are already pinned.
#
# Usage: check-artifact-paths_test.sh   (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subject="${SCRIPT_DIR}/check-artifact-paths.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/test/tools/deadcode" "$work/test/tools/ifacesweep/findings" "$work/pkg/somewhere"

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

echo "== the real tree, as CI will run it: must ACCEPT with no arguments =="
if "$subject" >"$work/out" 2>&1; then
	got=accept
else
	got=reject
fi
if [ "$got" = accept ]; then
	printf '  PASS  %-62s (accept)\n' "the committed artifacts, with no arguments"
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s want accept\n' "the committed artifacts, with no arguments"
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
fi

# ---------------------------------------------------------------------------
# test/coverage-baseline.txt shape: "<import path> <percent>". Every
# absolute-path shape this guard has ever needed to catch is exercised here,
# plus the traversal and vacuous-floor cases go-qrdb#403's review added.
# ---------------------------------------------------------------------------

COVERAGE="$work/test/coverage-baseline.txt"

# coverage <field1> -> COVERAGE with 20 clean rows and one row whose
# import-path field is $1.
coverage() {
	field1="$1"
	{
		i=0
		while [ "$i" -lt 20 ]; do
			printf 'github.com/jeremyhahn/go-qrdb/pkg/example%d 90.00\n' "$i"
			i=$((i + 1))
		done
		printf '%s 50.00\n' "$field1"
	} >"$COVERAGE"
}

echo "== coverage-baseline.txt: a legitimate import path must ACCEPT =="
coverage "github.com/jeremyhahn/go-qrdb/pkg/legit"
expect accept "legitimate import path" "$COVERAGE"

echo "== coverage-baseline.txt: every absolute-path shape seen so far must REJECT =="
coverage "/home/dev/sources/go-qrdb/pkg/x"
expect reject "POSIX home-directory path" "$COVERAGE"

coverage "/Users/dev/sources/go-qrdb/pkg/x"
expect reject "macOS /Users path" "$COVERAGE"

coverage "/workspace/go-qrdb/pkg/x"
expect reject "/workspace path (the agent container's mount point)" "$COVERAGE"

coverage "/root/go-qrdb/pkg/x"
expect reject "/root path" "$COVERAGE"

coverage "/tmp/go-qrdb/pkg/x"
expect reject "/tmp path" "$COVERAGE"

coverage "/opt/go-qrdb/pkg/x"
expect reject "/opt path" "$COVERAGE"

coverage "/var/go-qrdb/pkg/x"
expect reject "/var path -- an unenumerated prefix a denylist would still be missing" "$COVERAGE"

coverage 'C:\Users\dev\go-qrdb\pkg\x'
expect reject "Windows drive-absolute path" "$COVERAGE"

coverage '\\server\share\go-qrdb\pkg\x'
expect reject "Windows UNC path, backslash form" "$COVERAGE"

coverage '//server/share/go-qrdb/pkg/x'
expect reject "Windows UNC path, forward-slash form" "$COVERAGE"

coverage '\\?\C:\Users\dev\go-qrdb\pkg\x'
expect reject "Windows extended-length drive path" "$COVERAGE"

coverage '\\?\UNC\server\share\go-qrdb\pkg\x'
expect reject "Windows extended-length UNC path" "$COVERAGE"

coverage "path=/home/dev/sources/go-qrdb"
expect reject "an absolute path is still rejected wrapped in key=value" "$COVERAGE"

echo "== coverage-baseline.txt: a URI in the import-path field must REJECT too -- it is not an import path either =="
coverage "https://example.com/tool"
expect reject "https:// is not an import path" "$COVERAGE"

coverage "credential://db-key"
expect reject "credential:// is not an import path" "$COVERAGE"

echo "== coverage-baseline.txt: a real import path rooted at a DIFFERENT module must REJECT =="
coverage "github.com/someoneelse/other-repo/pkg/x"
expect reject "a github.com path outside this module" "$COVERAGE"

echo "== coverage-baseline.txt: a '.' or '..' path segment must REJECT -- it escapes the asserted root =="
coverage "pkg/../../home/alice/x.go"
expect reject "REPO_RELATIVE-shaped traversal in the import-path column" "$COVERAGE"

coverage "github.com/jeremyhahn/go-qrdb/pkg/../../other"
expect reject "traversal inside an otherwise-legitimate import path" "$COVERAGE"

coverage "github.com/jeremyhahn/go-qrdb/."
expect reject "a bare '.' as the final segment" "$COVERAGE"

coverage "github.com/jeremyhahn/go-qrdb/.."
expect reject "a bare '..' as the final segment" "$COVERAGE"

echo "== coverage-baseline.txt: an all-comment file validates zero records and must REJECT, not pass vacuously =="
{
	i=0
	while [ "$i" -lt 30 ]; do
		printf '# comment line %d, well above any physical-line floor\n' "$i"
		i=$((i + 1))
	done
} >"$COVERAGE"
expect reject "30 comment lines, 0 validated records" "$COVERAGE"

printf '' >"$COVERAGE"
expect reject "empty file" "$COVERAGE"

# ---------------------------------------------------------------------------
# test/tools/deadcode/baseline.txt shape:
# "<module>\t<scope>\t<kind>\t<symbol>". module is "." or REPO_RELATIVE;
# symbol is an import path plus ".Name".
# ---------------------------------------------------------------------------

DEADCODE="$work/test/tools/deadcode/baseline.txt"

# deadcode <module> <symbol> -> DEADCODE with 20 clean rows and one row using
# the given module and symbol.
deadcode() {
	module="$1"
	symbol="$2"
	{
		i=0
		while [ "$i" -lt 20 ]; do
			printf '.\texported\tfunc\tgithub.com/jeremyhahn/go-qrdb/pkg/example%d.Fn\n' "$i"
			i=$((i + 1))
		done
		printf '%s\texported\tfunc\t%s\n' "$module" "$symbol"
	} >"$DEADCODE"
}

echo "== baseline.txt (deadcode): root module '.' and a nested module must ACCEPT =="
deadcode "." "github.com/jeremyhahn/go-qrdb/pkg/legit.Fn"
expect accept "module '.' with a legitimate symbol" "$DEADCODE"

deadcode "sdk/go" "github.com/jeremyhahn/go-qrdb/pkg/legit.Fn"
expect accept "a nested module (sdk/go)" "$DEADCODE"

echo "== baseline.txt (deadcode): an absolute path or traversal in either column must REJECT =="
deadcode "/home/dev/x" "github.com/jeremyhahn/go-qrdb/pkg/legit.Fn"
expect reject "absolute path in the module column" "$DEADCODE"

deadcode "." '\\server\share\x.Fn'
expect reject "UNC path in the symbol column" "$DEADCODE"

deadcode "." '\\?\C:\Users\dev\go-qrdb\pkg\x.Fn'
expect reject "extended-length path in the symbol column" "$DEADCODE"

deadcode "sdk/../../etc" "github.com/jeremyhahn/go-qrdb/pkg/legit.Fn"
expect reject "traversal in the module column" "$DEADCODE"

echo "== baseline.txt (deadcode): all-comment file must REJECT (vacuous floor) =="
{
	i=0
	while [ "$i" -lt 30 ]; do
		printf '# comment line %d\n' "$i"
		i=$((i + 1))
	done
} >"$DEADCODE"
expect reject "30 comment lines, 0 validated records" "$DEADCODE"

# ---------------------------------------------------------------------------
# The ifacesweep findings TSV shape: header-led, `package` (import path) and
# `file` (REPO_RELATIVE) columns located by name.
# ---------------------------------------------------------------------------

TSV="$work/test/tools/ifacesweep/findings/min-tags.tsv"

# ifacesweep <package> <file> -> TSV with a header, 150 clean rows, and one
# row using the given package and file.
ifacesweep() {
	package="$1"
	file="$2"
	{
		printf 'package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\tfile\tline\n'
		i=0
		while [ "$i" -lt 150 ]; do
			printf 'github.com/jeremyhahn/go-qrdb/pkg/example\tIface\tMethod\ttrue\ttrue\tfalse\t1\tcmd/qrdbd/start.go\t1\n'
			i=$((i + 1))
		done
		printf '%s\tIface\tMethod\ttrue\ttrue\tfalse\t1\t%s\t42\n' "$package" "$file"
	} >"$TSV"
}

echo "== *.tsv (ifacesweep): a legitimate package and file must ACCEPT =="
ifacesweep "github.com/jeremyhahn/go-qrdb/pkg/legit" "cmd/qrdbd/start.go"
expect accept "legitimate package and file" "$TSV"

echo "== *.tsv (ifacesweep): an absolute, foreign, or traversing value in either column must REJECT =="
ifacesweep "github.com/jeremyhahn/go-qrdb/pkg/legit" "/home/dev/sources/go-qrdb/cmd/qrdbd/start.go"
expect reject "absolute path in the file column" "$TSV"

ifacesweep "github.com/jeremyhahn/go-qrdb/pkg/legit" '\\?\UNC\server\share\go-qrdb\cmd\qrdbd\start.go'
expect reject "extended-length UNC path in the file column" "$TSV"

ifacesweep "not-a-github-path" "cmd/qrdbd/start.go"
expect reject "a package value that is not an import path" "$TSV"

ifacesweep "github.com/jeremyhahn/go-qrdb/pkg/legit" "cmd/../../etc/passwd"
expect reject "traversal in the file column" "$TSV"

echo "== *.tsv (ifacesweep): a header with no 'file' column must REJECT, not silently pass =="
{
	printf 'package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\twrongname\tline\n'
	i=0
	while [ "$i" -lt 150 ]; do
		printf 'github.com/jeremyhahn/go-qrdb/pkg/example\tIface\tMethod\ttrue\ttrue\tfalse\t1\tcmd/qrdbd/start.go\t1\n'
		i=$((i + 1))
	done
} >"$TSV"
expect reject "header has no 'file' column" "$TSV"

echo "== *.tsv (ifacesweep): a header-only file (0 data rows) must REJECT (vacuous floor) =="
printf 'package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\tfile\tline\n' >"$TSV"
expect reject "header row only, 0 validated records" "$TSV"

# ---------------------------------------------------------------------------
# lockstep-baseline shape: "<module> <version>", comments and blanks skipped.
# ---------------------------------------------------------------------------

LOCKSTEP="$work/lockstep-baseline"

# lockstep <module> -> LOCKSTEP with 20 comment lines (one of which mentions a
# URI, to prove comments are truly skipped), a clean data row, and one row
# using the given module.
lockstep() {
	module="$1"
	{
		i=0
		while [ "$i" -lt 20 ]; do
			printf '# comment line %d, mentions https://example.com/doc for good measure\n' "$i"
			i=$((i + 1))
		done
		printf 'sdk/go v0.1.0-alpha\n'
		printf '%s v9.9.9\n' "$module"
	} >"$LOCKSTEP"
}

echo "== lockstep-baseline: a legitimate module must ACCEPT, and a URI in a COMMENT is not even examined =="
lockstep "sdk/go/gorm/conformance"
expect accept "a nested module, with a comment-line URI ignored" "$LOCKSTEP"

echo "== lockstep-baseline: an absolute path or traversal in the module column must REJECT =="
lockstep "/home/dev/go-qrdb"
expect reject "absolute path as the module" "$LOCKSTEP"

lockstep '\\?\C:\Users\dev\go-qrdb'
expect reject "extended-length path as the module" "$LOCKSTEP"

lockstep "sdk/../../etc"
expect reject "traversal as the module" "$LOCKSTEP"

echo "== lockstep-baseline: an all-comment file must REJECT (vacuous floor) =="
{
	i=0
	while [ "$i" -lt 30 ]; do
		printf '# comment line %d\n' "$i"
		i=$((i + 1))
	done
} >"$LOCKSTEP"
expect reject "30 comment lines, 0 validated records" "$LOCKSTEP"

# ---------------------------------------------------------------------------
# Exact-identity dispatch: a file that merely LOOKS like a known artifact --
# same extension, same basename -- but does not live at that artifact's
# identity must be treated as unknown, not aliased onto that artifact's
# validator.
# ---------------------------------------------------------------------------

echo "== an unknown .tsv (same extension, different identity) must REJECT as UNKNOWN, not as ifacesweep =="
OTHER_TSV="$work/test/tools/ifacesweep/findings/other-tags.tsv"
{
	printf 'package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\tfile\tline\n'
	i=0
	while [ "$i" -lt 20 ]; do
		printf 'github.com/jeremyhahn/go-qrdb/pkg/example\tIface\tMethod\ttrue\ttrue\tfalse\t1\tcmd/qrdbd/start.go\t1\n'
		i=$((i + 1))
	done
} >"$OTHER_TSV"
expect reject "a well-formed TSV under a name other than min-tags.tsv/full-tags.tsv" "$OTHER_TSV"
if "$subject" "$OTHER_TSV" 2>&1 | grep -q "NO KNOWN SHAPE"; then
	printf '  PASS  %-62s (unknown, not aliased)\n' "unrecognised .tsv reports NO KNOWN SHAPE"
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s did not report NO KNOWN SHAPE\n' "unrecognised .tsv"
	fail=$((fail + 1))
fi

echo "== an unrelated file named baseline.txt (wrong directory) must REJECT as UNKNOWN, not as deadcode =="
OTHER_BASELINE="$work/pkg/somewhere/baseline.txt"
{
	i=0
	while [ "$i" -lt 20 ]; do
		printf '.\texported\tfunc\tgithub.com/jeremyhahn/go-qrdb/pkg/example%d.Fn\n' "$i"
		i=$((i + 1))
	done
} >"$OTHER_BASELINE"
expect reject "a well-formed deadcode-shaped file outside test/tools/deadcode/" "$OTHER_BASELINE"
if "$subject" "$OTHER_BASELINE" 2>&1 | grep -q "NO KNOWN SHAPE"; then
	printf '  PASS  %-62s (unknown, not aliased)\n' "unrelated baseline.txt reports NO KNOWN SHAPE"
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s did not report NO KNOWN SHAPE\n' "unrelated baseline.txt"
	fail=$((fail + 1))
fi

echo "== an artifact basename this script does not recognise at all must REJECT, not silently pass =="
UNKNOWN="$work/some-unknown-artifact.dat"
{
	i=0
	while [ "$i" -lt 20 ]; do
		printf 'whatever %d\n' "$i"
		i=$((i + 1))
	done
} >"$UNKNOWN"
expect reject "unrecognised artifact basename and extension" "$UNKNOWN"

# ---------------------------------------------------------------------------
# Cross-cutting: file existence, multi-file, empty argument.
# ---------------------------------------------------------------------------

echo "== a missing file must REJECT =="
expect reject "file does not exist at all" "$work/does-not-exist.txt"

echo "== multiple files given together: one bad file must fail the whole run =="
# Both fixtures keep the identity test/coverage-baseline.txt -- the dispatch
# in check-artifact-paths.sh matches by that full identity as a path suffix,
# so a differently-placed copy would hit the "no known shape" branch instead
# of exercising this artifact's validator, which is not what this case tests.
mkdir -p "$work/clean-dir/test" "$work/bad-dir/test"
coverage "github.com/jeremyhahn/go-qrdb/pkg/legit"
cp "$COVERAGE" "$work/clean-dir/test/coverage-baseline.txt"
coverage "/home/dev/sources/go-qrdb/pkg/x"
cp "$COVERAGE" "$work/bad-dir/test/coverage-baseline.txt"
expect reject "clean + absolute together" "$work/clean-dir/test/coverage-baseline.txt" "$work/bad-dir/test/coverage-baseline.txt"

echo "== empty argument: must REJECT rather than silently check zero files =="
expect reject "empty string argument resolves to no files" ""

echo ""
echo "passed ${pass}, failed ${fail}"
[ "$fail" -eq 0 ]
