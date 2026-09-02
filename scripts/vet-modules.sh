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
#   vet-modules.sh --vet <LINT_TAGS> <GORM_TAGS>   run the sweep
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
# toolchain override -- `make GO=go1.26.1 vet-tags`, or a CI wrapper setting
# GOVET -- and vet with a different Go than every other target in the build.
# A sweep that runs the wrong toolchain is a check reporting on something other
# than what ships, which is this script's whole subject.
GOVET="${GOVET:-go vet}"

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

# tags_for prints the tag sets for a module, one per line. A lone "-" means
# "no -tags flag". An unmapped module is a hard failure.
tags_for() {
	case "$1" in
	"$ROOT"|"$ROOT"/sdk/go)
		printf '%s\n%s,integration\n' "$LINT_TAGS" "$LINT_TAGS" ;;
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
	LINT_TAGS="${LINT_TAGS:-}"; GORM_TAGS="${GORM_TAGS:-}"; tags_for "$2" ;;
--vet)
	LINT_TAGS="${2:-${LINT_TAGS:-}}"; GORM_TAGS="${3:-${GORM_TAGS:-}}"
	[ -n "$LINT_TAGS" ] || die "vet-modules: LINT_TAGS is empty; make must pass it"
	[ -n "$GORM_TAGS" ] || die "vet-modules: GORM_TAGS is empty; make must pass it"
	mods=$(discover_modules); assert_usable "$mods"
	rc=0; n=0
	for m in $mods; do
		sets=$(tags_for "$m")   # `set -e` aborts the sweep if this is unmapped
		for t in $sets; do
			n=$((n + 1))
			if [ "$t" = "-" ]; then
				echo "==> ($m) $GOVET ./..."
				(cd "$m" && $GOVET ./...) || { rc=1; echo "    FAILED in $m"; }
			else
				echo "==> ($m) $GOVET -tags \"$t\" ./..."
				(cd "$m" && $GOVET -tags "$t" ./...) || { rc=1; echo "    FAILED in $m under [$t]"; }
			fi
		done
	done
	[ $rc -eq 0 ] || die "vet-modules: at least one module/tag combination does not typecheck (#178, #221)"
	echo "vet-modules: clean — $n sweeps across $(printf '%s\n' "$mods" | wc -l | tr -d ' ') modules" ;;
*)
	die "usage: vet-modules.sh [--list | --tags-for <module> | --vet <LINT_TAGS> <GORM_TAGS>]" ;;
esac
