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
# test-tags.sh: run go test over the command surface under every reduced-protocol tag set.
set -eu

ROOT="${TEST_TAGS_ROOT:-.}"

# Overridable for the same reason GOVET is overridable in vet-modules.sh: a
# toolchain override or CI wrapper must reach the actual test invocation,
# not a hardcoded `go test`.
GOTEST="${GOTEST:-go test}"
GOLIST="${GOLIST:-go list}"

# MIN_PACKAGES is the discovery floor. ./cmd/qrdbd, ./cmd/qrdb and
# pkg/service exist under every combination this script is ever called
# with -- none of the three is itself behind a build tag -- so a count
# below this is discovery silently matching nothing, the same hazard
# vet-modules.sh's packages_under names, not a measurement of a clean run.
MIN_PACKAGES=3

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
