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
# vet-modules.sh — run `go vet` over EVERY Go module in the tree, each with its
# own build-tag sets. Called by `make vet-tags`.
#
#   vet-modules.sh --list                 print the discovered modules, one per line
#   vet-modules.sh --tags-for <module>    print that module's tag sets, one per line
#   vet-modules.sh --vet <TAG_SETS> <GORM_TAGS>    run the sweep
#
# TAG_SETS IS DATA THIS SCRIPT DOES NOT AUTHOR. The supported build-tag
# combinations are declared once, in the Makefile's SUPPORTED_TAG_SETS
# (go-qrdb#263), and arrive here as a space-separated list of comma-joined
# sets. Adding a combination here instead of there would put the declaration in
# two places, which is the defect #263 closed — the sweep's job is to exercise
# the declared set exactly, not to have opinions about what it contains.
#
# EVERY LINE THIS SCRIPT PRINTS CARRIES ITS TAG SET, and results from different
# sets are never merged into one list. A symbol declared only under a tag is
# ABSENT from a build without it, so a union of runs reports silence where the
# extra combinations were added to produce a finding (#271).
#
# WHY THIS IS A SCRIPT AND NOT A MAKEFILE RECIPE
#
# It began as an inline recipe and was wrong SIX times, every time in the same
# direction — narrower than its own claim, and silent about it:
#
#   1. never vetted sdk/go at all (a planted fmt.Printf produced no output)
#   2. after that was fixed, still missed sdk/go/gorm/conformance
#   3. `find -printf` is GNU-only: on BSD/macOS the list came back empty and the
#      target reported "clean across all 0 modules"
#   4. the guard added for (3) used `grep -qw '\.'`, which MATCHES the dot in
#      "./sdk/go" — so it accepted a rootless list while claiming to reject one
#   5. the success message claimed repository-wide coverage for modules pinned
#      to released dependencies
#   6. the discovery pipeline's exit status is `sort`'s, so `find` could fail
#      part-way through, emit a partial list, pass the root check, and sweep a
#      subset while reporting success
#
# A guard fails by STAYING SILENT, and silence is what success looks like. None
# of those six was visible without deliberately breaking something, and an
# inline recipe cannot be broken deliberately — there is nowhere to put the
# test. So the logic lives here, where vet-modules_test.sh can drive every
# rejecting branch, exactly as require-tool.sh does for its version comparison.
#
# The mapping is deliberately EXHAUSTIVE-OR-FAIL. Four of the seven modules
# match no packages without their own tags, so a wrong tag set prints
# "matched no packages" and looks identical to clean. Guessing is therefore
# worse than refusing, and adding a module forces a decision here rather than
# leaving a blind spot nobody sees until a gate is armed over it.
set -eu

ROOT="${VET_MODULES_ROOT:-.}"

# The vet command comes from Make, not from PATH. The inline recipe this
# replaced used $(GOVET), so hardcoding `go vet` here would silently ignore a
# toolchain override -- `make GO=go1.27.1 vet-tags`, or a CI wrapper setting
# GOVET -- and vet with a different Go than every other target in the build.
# A sweep that runs the wrong toolchain is a check reporting on something other
# than what ships, which is this script's whole subject.
GOVET="${GOVET:-go vet}"

# The package enumerator, overridable for the same reason GOVET is. It answers
# a question `go vet` cannot: HOW MANY packages a rung actually looked at.
GOLIST="${GOLIST:-go list}"

die() { printf '%s\n' "$@" >&2; exit 1; }

# discover_modules prints one module directory per line.
#
# Each stage's status is checked. A pipeline's exit status is its LAST stage's,
# so `find | sed | sort` reports success when find fails part-way — defect (6).
discover_modules() {
	_raw=$(find "$ROOT" -name go.mod -not -path '*/vendor/*' -not -path '*/.git/*' -print) || \
		die "vet-modules: module discovery FAILED while traversing the tree." \
		    "  Refusing to vet a partial list: a subset swept silently is the" \
		    "  failure this script exists to prevent."
	printf '%s\n' "$_raw" | sed 's#/go\.mod$##' | sort
}

# packages_under prints how many packages a module matches under a tag set, or
# "?" when the enumeration itself failed.
#
# WHY A COUNT IS PART OF THE RESULT. `go vet` exits 0 having looked at NOTHING
# just as readily as it exits 0 having looked at everything: a tag set that
# matches no packages prints "matched no packages" and is indistinguishable
# from clean. That hazard is named at the top of this file for MODULES, and it
# applies identically to a declared RUNG -- a rung that checks nothing is the
# failure that looks most like success, and it is exactly what the #263
# declaration would decay into if a tag were renamed underneath it.
#
# "?" and 0 are deliberately different answers. A `go list` that could not run
# -- an untidy go.mod, an unreachable module proxy -- is an ABSENCE OF
# MEASUREMENT, not a measurement of zero, and reporting it as zero would fail
# the sweep for a reason that has nothing to do with the tags.
packages_under() {
	if [ "$2" = "-" ]; then
		_out=$(cd "$1" && $GOLIST ./... 2>/dev/null) || { printf '?\n'; return 0; }
	else
		_out=$(cd "$1" && $GOLIST -tags "$2" ./... 2>/dev/null) || { printf '?\n'; return 0; }
	fi
	printf '%s\n' "$_out" | grep -c . || true
}

# tags_for prints the tag sets for a module, one per line. A lone "-" means
# "no -tags flag". An unmapped module is a hard failure.
tags_for() {
	case "$1" in
	"$ROOT"|"$ROOT"/sdk/go)
		# The two tagged modules carry the repository's own code, so they get
		# the FULL declared set — every combination in SUPPORTED_TAG_SETS,
		# verbatim and in order. This is the only place the declaration is
		# consumed for them; it is not filtered or deduplicated here.
		printf '%s\n' $TAG_SETS ;;
	"$ROOT"/sdk/go/gorm/conformance)
		printf '%s\n' "$GORM_TAGS" ;;
	"$ROOT"/test/performance/qrdb|"$ROOT"/test/performance/etcd-benchmark|"$ROOT"/test/performance/tikv)
		# These pin RELEASED go-qrdb/sdk versions with no `replace`, so vetting
		# them typechecks the benchmark code against those releases, NOT against
		# this checkout. Deliberate: they exist to benchmark released code, and
		# a replace would change what they measure. The honest reading of a
		# green run is "each module typechecks against its own declared
		# dependencies", not "the repository is consistent with itself".
		printf 'benchmark\n' ;;
	"$ROOT"/test/performance/cmd/kvcompare)
		printf -- '-\n' ;;
	*)
		die "vet-modules: UNMAPPED MODULE $1" \
		    "  A Go module was added and nothing says which build tags vet it." \
		    "  Add it to tags_for() in scripts/vet-modules.sh. Refusing to guess:" \
		    "  the wrong tag set reports 'matched no packages' and looks clean." ;;
	esac
}

assert_usable() {
	[ -n "$1" ] || die "vet-modules: discovery returned NOTHING — refusing to report success." \
	                   "  This repository always has at least the root module, so an empty" \
	                   "  list means the discovery failed, not that there is nothing to vet."
	for _m in $1; do [ "$_m" = "$ROOT" ] && return 0; done
	die "vet-modules: discovery returned no ROOT module — refusing to report success." \
	    "  Discovered: [$1]" \
	    "  A list without the root means the discovery malformed its output."
}

case "${1:---vet}" in
--list)
	mods=$(discover_modules); assert_usable "$mods"; printf '%s\n' "$mods" ;;
--tags-for)
	[ $# -ge 2 ] || die "vet-modules: --tags-for needs a module"
	TAG_SETS="${TAG_SETS:-}"; GORM_TAGS="${GORM_TAGS:-}"; tags_for "$2" ;;
--vet)
	TAG_SETS="${2:-${TAG_SETS:-}}"; GORM_TAGS="${3:-${GORM_TAGS:-}}"
	[ -n "$TAG_SETS" ] || die "vet-modules: TAG_SETS is empty; make must pass it"
	[ -n "$GORM_TAGS" ] || die "vet-modules: GORM_TAGS is empty; make must pass it"
	mods=$(discover_modules); assert_usable "$mods"
	# Echo the declared set BEFORE sweeping. A green run means "these
	# combinations typecheck" and nothing wider, so a log that does not say
	# which combinations were asked about records a verdict without its
	# question (#263).
	echo "vet-modules: declared combinations for the tagged modules:"
	printf '  %s\n' $TAG_SETS
	rc=0; n=0
	for m in $mods; do
		sets=$(tags_for "$m")   # `set -e` aborts the sweep if this is unmapped
		for t in $sets; do
			n=$((n + 1))
			pkgs=$(packages_under "$m" "$t")
			if [ "$t" = "-" ]; then
				echo "==> ($m) [$pkgs packages] $GOVET ./..."
				(cd "$m" && $GOVET ./...) || { rc=1; echo "    FAILED in $m"; }
			else
				echo "==> ($m) [$pkgs packages] $GOVET -tags \"$t\" ./..."
				(cd "$m" && $GOVET -tags "$t" ./...) || { rc=1; echo "    FAILED in $m under [$t]"; }
			fi
			if [ "$pkgs" = 0 ]; then
				rc=1
				echo "    MATCHED NO PACKAGES in $m under [$t] -- vet exited having looked at nothing"
			fi
		done
	done
	[ $rc -eq 0 ] || die "vet-modules: at least one module/tag combination does not typecheck, or matched no packages (#178, #221, #263)"
	echo "vet-modules: clean — $n sweeps across $(printf '%s\n' "$mods" | wc -l | tr -d ' ') modules, under the DECLARED combinations only" ;;
*)
	die "usage: vet-modules.sh [--list | --tags-for <module> | --vet <TAG_SETS> <GORM_TAGS>]" ;;
esac
