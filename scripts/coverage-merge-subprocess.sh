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
# coverage-merge-subprocess.sh — produce ONE legacy-format coverage profile
# that includes coverage from SUBPROCESS test executions, which `go test
# -coverprofile` cannot see on its own. Called by `make coverage` and `make
# coverage-baseline`.
#
#   coverage-merge-subprocess.sh --list                 print the packages this
#                                                        script special-cases,
#                                                        one per line
#   coverage-merge-subprocess.sh --run <out.profile>     run the merge, writing
#                                                        the combined legacy
#                                                        profile to the given path
#
# THE PROBLEM THIS SCRIPT EXISTS FOR (go-qrdb#114 review)
#
# A handful of tests must exercise a process-wide, one-shot, or destructive
# go-enclave lifecycle call (enclave.SetAllocator, enclave.WipeAll) that
# cannot run twice in one process without poisoning every later test in the
# same binary. Those tests re-execute the test binary itself as a
# SUBPROCESS (`exec.Command(os.Args[0], ...)`), so the risky call happens in
# a process of its own. `go test -coverprofile=X` only ever records what the
# PARENT process executed; the child's execution of, say, wrapAllocError's
# ErrBudgetExhausted branch is invisible to it, and this class of test can
# never move that line to "covered" no matter how correct it is.
#
# THE MECHANISM
#
# Go's newer coverage instrumentation (`-cover`) writes raw counter files to
# a directory named by GOCOVERDIR. `go tool covdata textfmt` then merges every
# counter file in that directory into ONE legacy-format profile,
# indistinguishable from what `-coverprofile` writes, which is what lets it
# feed the EXISTING coverratchet gate with no change to that tool.
#
# A CHILD MUST BE PASSED -test.gocoverdir EXPLICITLY. It is not enough for it
# to inherit GOCOVERDIR through its environment the way exec.Command(os.Args[0],
# ...) with `cmd.Env = append(os.Environ(), ...)` already does for every
# subprocess test in this tree — that was ASSUMED true here through round 3 of
# go-qrdb#114's review and was wrong, caught in round 4 by measurement, not
# argument: a minimal reproduction (parent re-execs itself with GOCOVERDIR
# inherited via os.Environ() but no -test.gocoverdir flag) wrote a
# covcounters file for the PARENT only; the child's own execution left zero
# files and the merged profile showed its exercised line at count 0. Passing
# -test.gocoverdir="$GOCOVERDIR" as an explicit ARGUMENT to the child, in
# addition to the inherited environment variable, is what produces a second
# covcounters file and a correct merged count — confirmed the same way. Every
# subprocess test this script depends on for coverage must therefore build
# its child's argv with that flag; see subprocessTestArgs in
# pkg/secure/allocation_failure_test.go for the shared helper most of them use,
# and pkg/logger/logger_additional_test.go / cmd/qrdbd/reset_signal_race_test.go
# for the two-line inline form where the shared helper is not reachable across
# a package boundary.
#
# os.Exit INSIDE the child does not break this — Go's coverage runtime
# registers an exit hook that still runs on a plain os.Exit, confirmed the
# same way (a child that calls os.Exit(1) after the flag was added still
# wrote its counter file). What DOES break it is a signal the child cannot
# catch or a kill it cannot see coming: SIGKILL
# (pkg/seal/encfs/pebble_sigkill_test.go's helper is deliberately killed with
# one, by design, to reproduce a real crash) and, plausibly, a `-test.timeout`
# deadline killing a permanently deadlocked goroutine
# (pkg/secure/borrowguard_test.go's "unguarded" hang case) — neither leaves
# the child any code path to run an exit hook from, and NEITHER IS FIXABLE
# from this side: there is no flag or environment variable that survives a
# kill the target process never gets to handle. Those two remain
# best-effort/absent from the merged profile; every other subprocess test
# this script discovers is expected to contribute real coverage once its
# child forwards the flag.
#
# WHAT DOES NOT WORK, MEASURED RATHER THAN ASSUMED: running `go test -cover
# ./pkg/... -args -test.gocoverdir=$DIR` with GOCOVERDIR=$DIR also set in the
# environment. `go test` manages its OWN internal coverage directory for the
# percentage it prints and does not forward an external GOCOVERDIR to the
# compiled test binary the way a plain `go build -cover` binary honors it —
# the child subprocess's counters never appeared in $DIR under that
# invocation. What DOES work, confirmed by inspecting the resulting
# covcounters files: pre-building the test binary with `go test -c -cover`
# and then running THAT binary directly with GOCOVERDIR set, so `go test`'s
# own wrapper is out of the loop entirely for the run step. This script does
# the latter for the special-cased packages only; every other package keeps
# using the ordinary, cheaper `go test -coverprofile` path via the bulk run
# the Makefile still performs.
#
# WHY CONCATENATION OF THE TWO PROFILES IS SAFE
#
# The bulk run and this script's per-package runs cover DISJOINT package
# sets — this script's packages are excluded from the bulk `go list` before
# it runs (see the Makefile's `coverage`/`coverage-baseline` targets). Two
# legacy-format profiles naming disjoint files can be concatenated with only
# the first "mode:" header kept; `go tool cover` and coverratchet both read
# the file as a flat list of `file:range numstmt count` lines and do not care
# which run contributed which line, unlike merging two profiles that BOTH
# describe the same file (which would need real merging, not concatenation).
#
# WHY THE PACKAGE LIST IS DISCOVERED, NOT HAND-WRITTEN (round 2 finding)
#
# The first version of this script hard-coded three packages. It missed two
# real ones on the FIRST pass: pkg/logger (logger_additional_test.go, two
# sites) and pkg/seal/encfs (pebble_sigkill_test.go) both re-exec
# os.Args[0] and were silently excluded from the treatment this script
# exists to provide — invisible until someone went looking, exactly the
# failure mode `check-tag-lockstep.sh`'s own history warns about for a
# hand-maintained list that drifts from the code. subprocess_packages()
# therefore GREPS the tree for the pattern every subprocess test in this
# repository shares (exec.Command(os.Args[0]) and resolves the containing
# packages, so a new subprocess test is covered automatically the next time
# this script runs, with no second place to remember to update.
#
# DISCOVERY_FLOOR guards the one way a grep-based list fails silently: if the
# pattern ever stops matching anything — a typo introduced here, `exec.Command`
# reformatted into a shape the pattern misses, `find` failing partway through
# — an empty list would make this script's job silently disappear, exactly
# the "an empty loop reports success" failure test/buildgates/action_pins_test.go
# is written against. A floor below the current real count (5 files across 4
# packages, kept lower here on purpose so a genuine future consolidation does
# not need to touch this number) turns that into a loud failure instead.
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
