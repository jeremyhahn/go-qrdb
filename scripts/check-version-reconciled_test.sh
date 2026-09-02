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
# check-version-reconciled_test.sh — exercises the branches this repository
# cannot reach.
#
# This repository's VERSION and CHANGELOG are reconciled, so running the check
# against it only ever exercises the ACCEPTING branch. Each case below builds a
# throwaway tree in the shape the check is supposed to reject and asserts BOTH
# the exit status and the diagnostic, so a future edit cannot silently stop
# rejecting one of them.
#
# The first two fixtures are the drift that actually happened (go-qrdb#25):
# VERSION left behind a release, and a release whose notes were never moved out
# of [Unreleased].
#
# Usage: check-version-reconciled_test.sh    (no arguments; needs a temp dir)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-version-reconciled.sh"

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT INT TERM

FAILURES=0

# fixture NAME VERSION CHANGELOG_BODY
fixture() {
	DIR="${WORK}/$1"
	mkdir -p "${DIR}"
	printf '%s\n' "$2" > "${DIR}/VERSION"
	printf '%s\n' "$3" > "${DIR}/CHANGELOG.md"
	echo "${DIR}"
}

# assert NAME EXPECTED_STATUS EXPECTED_SUBSTRING REPO_DIR [TAG]
assert() {
	NAME="$1"
	WANT_STATUS="$2"
	WANT_TEXT="$3"
	DIR="$4"
	TAG="${5:-}"

	set +e
	OUTPUT=$("${CHECK}" "${DIR}" ${TAG:+"${TAG}"} 2>&1)
	GOT_STATUS=$?
	set -e

	if [ "${GOT_STATUS}" -ne "${WANT_STATUS}" ]; then
		echo "FAIL ${NAME}: exit ${GOT_STATUS}, want ${WANT_STATUS}"
		echo "${OUTPUT}" | sed 's/^/     /'
		FAILURES=$((FAILURES + 1))
		return
	fi
	case "${OUTPUT}" in
	*"${WANT_TEXT}"*) echo "ok   ${NAME}" ;;
	*)
		echo "FAIL ${NAME}: output does not mention '${WANT_TEXT}'"
		echo "${OUTPUT}" | sed 's/^/     /'
		FAILURES=$((FAILURES + 1))
		;;
	esac
}

RELEASED='# Changelog

## [Unreleased]

## [0.10.0-alpha] - 2026-08-20

### Fixed
- something

## [0.9.0-alpha] - 2026-08-20
'

# The exact go-qrdb#25 drift: two releases were tagged while VERSION stayed on
# an older one.
assert "VERSION behind the CHANGELOG" 1 \
	"VERSION is 0.9.0-alpha but the newest CHANGELOG release is 0.10.0-alpha" \
	"$(fixture stale-version '0.9.0-alpha' "${RELEASED}")"

# The other half of that drift: the version was bumped and the notes never left
# [Unreleased], so the newest heading is still the PREVIOUS release.
assert "release notes never written" 1 \
	"VERSION is 0.11.0-alpha but the newest CHANGELOG release is 0.10.0-alpha" \
	"$(fixture unreleased-notes '0.11.0-alpha' "${RELEASED}")"

# A CHANGELOG with nothing but [Unreleased] has no release to reconcile with,
# which is a failure and not a pass — the accepting branch must not be reachable
# by having no data.
assert "no released section at all" 1 \
	"has no released section" \
	"$(fixture only-unreleased '0.1.0-alpha' '# Changelog

## [Unreleased]
')"

# Invariant 2: the tag names a different version than VERSION.
assert "tag disagrees with VERSION" 1 \
	"tag v0.9.0-alpha names 0.9.0-alpha but VERSION is 0.10.0-alpha" \
	"$(fixture tag-skew '0.10.0-alpha' "${RELEASED}")" "v0.9.0-alpha"

# EVERY namespace is the same check: sdk/go/vX.Y.Z names X.Y.Z too.
assert "sdk tag disagrees with VERSION" 1 \
	"tag sdk/go/v0.9.0-alpha names 0.9.0-alpha but VERSION is 0.10.0-alpha" \
	"$(fixture sdk-tag-skew '0.10.0-alpha' "${RELEASED}")" "sdk/go/v0.9.0-alpha"

# A DEEPER lockstep namespace. The set of nested modules is derived now
# (scripts/lockstep-modules.sh) and the Release Lockstep workflow triggers on
# sdk/**/v*, so a tag with more than one path segment before the version reaches
# this check. Stripping a literal `sdk/go/` compared `gorm/conformance/v...`
# against the version and reported DRIFT on a perfectly good release.
assert "a deeper module's tag reconciles" 0 \
	"tag sdk/go/gorm/conformance/v0.10.0-alpha all name 0.10.0-alpha" \
	"$(fixture deep-tag-ok '0.10.0-alpha' "${RELEASED}")" \
	"sdk/go/gorm/conformance/v0.10.0-alpha"

# And it must still CATCH a skew at that depth, or the normalisation would have
# turned a real check into an unconditional pass.
assert "a deeper module's tag still catches a skew" 1 \
	"tag sdk/go/gorm/conformance/v0.9.0-alpha names 0.9.0-alpha but VERSION is 0.10.0-alpha" \
	"$(fixture deep-tag-skew '0.10.0-alpha' "${RELEASED}")" \
	"sdk/go/gorm/conformance/v0.9.0-alpha"

# A tag whose last component is not a version is reported as malformed rather
# than left to surface as a confusing DRIFT.
assert "a tag with no version component is refused" 1 \
	"MALFORMED  sdk/go/gorm/conformance does not end in a v-prefixed version component" \
	"$(fixture no-version-component '0.10.0-alpha' "${RELEASED}")" \
	"sdk/go/gorm/conformance"

# Missing inputs fail rather than passing vacuously.
MISSING=$(fixture missing-changelog '0.10.0-alpha' 'x')
rm "${MISSING}/CHANGELOG.md"
assert "no CHANGELOG" 1 "MISSING  CHANGELOG.md" "${MISSING}"

MISSING_V=$(fixture missing-version '0.10.0-alpha' "${RELEASED}")
rm "${MISSING_V}/VERSION"
assert "no VERSION" 1 "MISSING  VERSION" "${MISSING_V}"

EMPTY_V=$(fixture empty-version '' "${RELEASED}")
: > "${EMPTY_V}/VERSION"
assert "empty VERSION" 1 "EMPTY" "${EMPTY_V}"

# Accepting branches, so a check that rejects everything does not read as green.
assert "reconciled, no tag" 0 \
	"VERSION and the newest CHANGELOG heading both name 0.10.0-alpha" \
	"$(fixture good '0.10.0-alpha' "${RELEASED}")"

assert "reconciled, with tag" 0 \
	"all name 0.10.0-alpha" \
	"$(fixture good-tag '0.10.0-alpha' "${RELEASED}")" "v0.10.0-alpha"

# The skip must be LOUD: a tag check that quietly does nothing is
# indistinguishable from one that passed.
assert "tag check announces the skip" 0 \
	"skipped  tag check" \
	"$(fixture good-skip '0.10.0-alpha' "${RELEASED}")"

if [ "${FAILURES}" -ne 0 ]; then
	echo "${FAILURES} case(s) failed"
	exit 1
fi
echo "all cases passed"
