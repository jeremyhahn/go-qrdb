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
# test-tags.sh — run `go test` over the command surface, under every
# reduced-protocol build-tag combination. Called by `make test-tags`.
#
#   test-tags.sh <TAG_SETS> <PACKAGES>
#
# WHY THIS EXISTS AND WHY IT IS NARROWER THAN vet-modules.sh
#
# `make vet-tags` proves the tree TYPECHECKS under every declared
# combination (scripts/vet-modules.sh). It does not, and cannot, prove a
# test that compiles under a reduced build also PASSES there: a test can
# reference a subcommand or flag that a tag removed, assert on an error
# type a capability gate changed, or hard-code a count that varies by tag,
# and `go vet` has no opinion about any of that -- only `go test` running
# the test body does. go-qrdb's own history has three real examples of this
# exact gap: a hard-coded subcommand count, and two tests that asserted
# success on a subcommand a reduced build does not register.
#
# TAG_SETS is passed in from the Makefile's SUPPORTED_TAG_SETS (via
# TEST_TAGS_SETS, a filtered subset), not authored here, for the same reason
# scripts/vet-modules.sh takes TAG_SETS as data rather than declaring its
# own: one declaration (go-qrdb#263).
#
# SCOPE: only ./cmd/qrdbd/... and ./cmd/qrdb/... (PACKAGES), not a
# repository-wide sweep. Running the full suite under every combination
# multiplies an already-long test run by the combination count; the command
# surface is where a build tag changes observable BEHAVIOR a test can get
# wrong (a flag or subcommand appearing or vanishing), so that is what this
# gate exists to catch. pkg/sql, pkg/api/etcd and the other tag-gated
# packages are already covered for THEIR OWN reduced builds by
# `make vet-tags` (they typecheck or vanish entirely) and by
# `test/buildgates` (their dependency graph is absent) -- this script does
# not re-check that; it checks the command surface that is ALWAYS present
# but whose registered flags and subcommands vary underneath it.
#
# The +integration rungs are deliberately excluded by the caller (Make):
# running them here would need the real infrastructure integration tests
# assume, which is what `make integration-test` (test/integration/) is for,
# not this gate.
set -eu

ROOT="${TEST_TAGS_ROOT:-.}"

# Overridable for the same reason GOVET is overridable in vet-modules.sh: a
# toolchain override or CI wrapper must reach the actual test invocation,
# not a hardcoded `go test`.
GOTEST="${GOTEST:-go test}"
GOLIST="${GOLIST:-go list}"

# MIN_PACKAGES is the discovery floor. ./cmd/qrdbd and ./cmd/qrdb exist
# under every combination this script is ever called with -- they are the
# two daemon/CLI main packages, never themselves behind a build tag -- so a
# count below this is discovery silently matching nothing, the same hazard
# vet-modules.sh's packages_under names, not a measurement of a clean run.
MIN_PACKAGES=2

die() { printf '%s\n' "$@" >&2; exit 1; }

packages_under() {
	_out=$(cd "$ROOT" && $GOLIST -tags "$1" $PACKAGES 2>/dev/null) || { printf '?\n'; return 0; }
	printf '%s\n' "$_out" | grep -c . || true
}

[ $# -ge 2 ] || die "usage: test-tags.sh <TAG_SETS> <PACKAGES>"
TAG_SETS="$1"
PACKAGES="$2"
[ -n "$TAG_SETS" ] || die "test-tags: TAG_SETS is empty; make must pass it"
[ -n "$PACKAGES" ] || die "test-tags: PACKAGES is empty; make must pass it"

echo "test-tags: declared combinations:"
printf '  %s\n' $TAG_SETS

rc=0
n=0
for t in $TAG_SETS; do
	n=$((n + 1))
	pkgs=$(packages_under "$t")
	if [ "$pkgs" = "?" ]; then
		rc=1
		echo "    DISCOVERY FAILED under [$t] -- go list itself errored, not a measurement of zero"
		continue
	fi
	if [ "$pkgs" -lt "$MIN_PACKAGES" ]; then
		rc=1
		echo "    MATCHED $pkgs PACKAGE(S) under [$t], want at least $MIN_PACKAGES -- discovery silently shrank"
		continue
	fi
	echo "==> [$pkgs packages] $GOTEST -tags \"$t\" $PACKAGES"
	(cd "$ROOT" && $GOTEST -tags "$t" $PACKAGES) || { rc=1; echo "    FAILED under [$t]"; }
done

[ $rc -eq 0 ] || die "test-tags: at least one tag combination failed its tests, or matched too few packages"
echo "test-tags: clean — $n combinations run over $PACKAGES"
