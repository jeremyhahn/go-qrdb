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
# coverage-merge-subprocess.sh: one coverage profile that includes subprocess test executions.
set -eu

ROOT="${COVERAGE_MERGE_ROOT:-.}"
GO="${GO:-go}"
TAGS="${COVERAGE_MERGE_TAGS:-}"
TIMEOUT="${COVERAGE_MERGE_TIMEOUT:-30m}"
DISCOVERY_FLOOR="${COVERAGE_MERGE_DISCOVERY_FLOOR:-3}"

die() { printf '%s\n' "$@" >&2; exit 1; }

# subprocess_packages discovers, rather than hard-codes, every package with a
# test that re-execs the test binary itself. See the file header for why.
#
# The walk is scoped to ./pkg and ./cmd -- exactly the package universe
# `make coverage`'s bulk run sweeps (`go list ./pkg/... ./cmd/...`) -- and
# NOT the whole repository tree. sdk/go and sdk/go/gorm/conformance are
# SEPARATE Go modules with their own test target (`make test-sdk`) and are
# out of this gate's scope entirely; `go list` run from the root module
# cannot resolve a directory inside them anyway; the first version of this
# function found this out by having `go list ./sdk/go/node` fail head-on
# (round 2 review's own end-to-end run) and, because that failure happened
# inside a subshell in a pipeline, silently truncated the rest of the
# discovered list rather than reporting anything -- a `set -e` inside a
# pipeline stage does not propagate the way a flat script's does. Scoping the
# walk removes the cross-module case entirely rather than papering over the
# subshell/set -e interaction with a differently-fragile workaround.
subprocess_packages() {
	files=$(find ./pkg ./cmd -name '*_test.go' -type f | xargs grep -l 'exec\.Command(os\.Args\[0\]' 2>/dev/null || true)
	if [ -z "$files" ]; then
		die "subprocess_packages: no _test.go file under ./pkg or ./cmd matched exec.Command(os.Args[0] -- either every subprocess test was removed, or the discovery pattern broke"
	fi
	dirs=$(printf '%s\n' "$files" | xargs -n1 dirname | sort -u)
	# Accumulated with a plain shell variable and a `for` loop that runs
	# directly in THIS shell, not piped into anything -- piping a loop's
	# output (`for ... done | sort -u`) forks the loop into a subshell in
	# POSIX sh and bash alike, and `die`'s `exit 1` inside that subshell would
	# only end the subshell, silently truncating the rest of the list instead
	# of failing the script. That is exactly the failure round 2's review
	# surfaced when `go list ./sdk/go/node` failed partway through the old
	# piped form: the error printed, and the run continued as if it had not.
	pkgs=""
	for dir in $dirs; do
		pkg=$("$GO" list "$dir" 2>/dev/null) || die "subprocess_packages: 'go list $dir' failed for a file the discovery grep matched"
		pkgs="$pkgs
$pkg"
	done
	printf '%s\n' "$pkgs" | sed '/^$/d' | sort -u
}

cmd="${1:-}"
case "$cmd" in
--list)
	cd "$ROOT"
	subprocess_packages
	exit 0
	;;
--run)
	out="${2:-}"
	[ -n "$out" ] || die "coverage-merge-subprocess.sh --run requires an output path"
	# Anchor a relative output path to the CALLER's directory before the cd
	# below: resolved after it, `--run cover/x.out` would silently land under
	# COVERAGE_MERGE_ROOT instead of where the caller asked for it.
	case "$out" in
	/*) ;;
	*) out="$(pwd)/$out" ;;
	esac
	;;
*)
	die "usage: coverage-merge-subprocess.sh --list | --run <out.profile>"
	;;
esac

# Both modes discover and build relative to COVERAGE_MERGE_ROOT, so --list
# (which the Makefile uses to EXCLUDE these packages from the bulk run) and
# --run (which covers them) always describe the same package set, whatever
# directory the script is invoked from.
cd "$ROOT"

packages="$(subprocess_packages)"
found=$(printf '%s\n' "$packages" | sed '/^$/d' | wc -l | tr -d ' ')
if [ "$found" -lt "$DISCOVERY_FLOOR" ]; then
	die "subprocess_packages found only $found package(s), below the floor of $DISCOVERY_FLOOR -- discovery likely broke; see DISCOVERY_FLOOR in this script's header before lowering it"
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT INT TERM

rawdir="$workdir/rawcounters"
mkdir -p "$rawdir"

i=0
for pkg in $packages; do
	i=$((i + 1))
	bin="$workdir/pkg$i.test"
	# -c compiles the test binary without running it, exactly as `go test`
	# would for this package, WITH coverage instrumentation. Running the
	# resulting binary directly (below) is what lets GOCOVERDIR reach it —
	# see the file header for why the `go test -cover ... -args
	# -test.gocoverdir=` form does not.
	#
	# TAGS is quoted HERE, as one shell argument, regardless of how many
	# space-separated words it contains -- this is the one place that can be
	# correct. A pre-quoted string (`-tags "a b c"`) handed through a Make
	# variable and an environment variable carries literal quote BYTES, not
	# shell quoting, and round-tripping it that way is exactly what broke the
	# tag list in round 2's review (see the Makefile's coverage-leg
	# comment for the measured failure). Building the flag only when TAGS is
	# non-empty, rather than always passing `-tags ""`, keeps an empty tag set
	# from being a no-op flag that differs from omitting it entirely.
	if [ -n "$TAGS" ]; then
		if ! "$GO" test -c -cover -race -tags "$TAGS" -o "$bin" "$pkg"; then
			die "building the coverage-instrumented test binary for $pkg failed"
		fi
	else
		if ! "$GO" test -c -cover -race -o "$bin" "$pkg"; then
			die "building the coverage-instrumented test binary for $pkg failed"
		fi
	fi
	# GOCOVERDIR is exported, not just passed via -test.gocoverdir, because a
	# subprocess this binary spawns (exec.Command(os.Args[0], ...)) reads it
	# back out of its OWN inherited environment to decide whether to forward
	# -test.gocoverdir to ITS child in turn — see
	# pkg/secure/allocation_failure_test.go's subprocessTestArgs.
	#
	# -test.timeout is explicit rather than relied on as a default: this
	# binary is run directly, bypassing the `go test` wrapper that
	# $(GOTEST)'s own -timeout=$(GOTEST_TIMEOUT) flag would normally reach, so
	# without this a wedged subprocess test hangs the coverage gate rather
	# than failing it within a bounded time. Matches $(GOTEST_TIMEOUT)'s
	# default via COVERAGE_MERGE_TIMEOUT.
	#
	# The binary runs from its PACKAGE's directory, as `go test` runs it: tests
	# that open testdata/ or other relative paths resolve them against the
	# working directory, and from the repository root they would read the
	# wrong files or none. bin and rawdir are absolute, so the cd does not
	# move them.
	pkgdir=$("$GO" list -f '{{.Dir}}' "$pkg") || die "resolving the directory of $pkg failed"
	export GOCOVERDIR="$rawdir"
	if ! (cd "$pkgdir" && "$bin" -test.timeout="$TIMEOUT" -test.gocoverdir="$rawdir"); then
		die "coverage-instrumented test run for $pkg failed (see output above)"
	fi
	unset GOCOVERDIR
done

fragment="$workdir/fragment.out"
if ! "$GO" tool covdata textfmt -i="$rawdir" -o="$fragment"; then
	die "go tool covdata textfmt failed to merge $rawdir"
fi

if [ ! -s "$fragment" ]; then
	die "the merged subprocess-package fragment is empty; expected coverage data for: $(printf '%s' "$packages" | tr '\n' ' ')"
fi

cp "$fragment" "$out"

echo "coverage-merge-subprocess: merged $found package(s) into $out"
