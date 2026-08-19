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
# Regression tests for check-tag-lockstep.sh.
#
# The production tag set is permanently valid, so running the check against
# this repository only ever exercises the accepting branch. Each case below
# builds an isolated throwaway repository with a tag layout the check is
# supposed to reject and asserts BOTH the exit status and the diagnostic, so a
# future edit cannot silently stop rejecting one of them.
#
# Usage: check-tag-lockstep_test.sh    (no arguments; needs git and a temp dir)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-tag-lockstep.sh"

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT INT TERM

FAILURES=0

# git in a fixture must not inherit the caller's identity, signing or hooks:
# CI signs every commit and tag, and an unavailable key would fail the fixture
# rather than the assertion.
fixture() {
	DIR="${WORK}/$1"
	mkdir -p "${DIR}"
	git -C "${DIR}" init -q -b main
	git -C "${DIR}" config user.name "lockstep-test"
	git -C "${DIR}" config user.email "lockstep-test@invalid"
	echo "${DIR}"
}

commit() {
	git -C "$1" commit -q --allow-empty --no-gpg-sign -m "$2"
}

tag() {
	git -C "$1" tag --no-sign "$2" ${3:+"$3"}
}

# assert NAME EXPECTED_STATUS EXPECTED_SUBSTRING REPO_DIR
assert() {
	NAME="$1"
	WANT_STATUS="$2"
	WANT_TEXT="$3"
	DIR="$4"

	set +e
	OUTPUT=$("${CHECK}" "${DIR}" 2>&1)
	GOT_STATUS=$?
	set -e

	if [ "${GOT_STATUS}" -ne "${WANT_STATUS}" ]; then
		echo "FAIL  ${NAME}: exit ${GOT_STATUS}, want ${WANT_STATUS}"
		echo "${OUTPUT}" | sed 's/^/        /'
		FAILURES=$((FAILURES + 1))
		return
	fi
	case "${OUTPUT}" in
	*"${WANT_TEXT}"*) ;;
	*)
		echo "FAIL  ${NAME}: output does not contain '${WANT_TEXT}'"
		echo "${OUTPUT}" | sed 's/^/        /'
		FAILURES=$((FAILURES + 1))
		return
		;;
	esac
	echo "ok    ${NAME}"
}

# A clone with no tags at all reports nothing to audit — the vacuous green this
# check must never produce.
DIR=$(fixture tagless)
assert "tagless clone is refused" 1 "no root version tags in this clone" "${DIR}"

# Matched pairs at identical commits, one annotated and one lightweight, to pin
# that both ref kinds peel to the same commit.
DIR=$(fixture valid)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
commit "${DIR}" two
git -C "${DIR}" tag --no-sign -a -m "release" v1.1.0
tag "${DIR}" sdk/go/v1.1.0
assert "matched pairs pass" 0 "root and sdk/go tags are in lockstep" "${DIR}"

# The go-qrdb#6 shape: a root tag published without its sdk/go counterpart.
DIR=$(fixture missing)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
commit "${DIR}" two
tag "${DIR}" v1.1.0
assert "missing sdk/go counterpart is rejected" 1 "MISSING  sdk/go/v1.1.0" "${DIR}"

# Both tags exist but were cut at different commits — the modules disagree on
# what the version contains, which the pair-existence check alone would miss.
DIR=$(fixture skew)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
commit "${DIR}" two
tag "${DIR}" v1.1.0
tag "${DIR}" sdk/go/v1.1.0 HEAD~1
assert "same version at different commits is rejected" 1 "SKEW     v1.1.0" "${DIR}"

# An sdk/go tag with no root counterpart: the reverse skew, equally unbuildable.
DIR=$(fixture orphan)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/v2.0.0
assert "orphaned sdk/go tag is rejected" 1 "ORPHAN   sdk/go/v2.0.0" "${DIR}"

# A branch named for the absent root tag must not satisfy the lookup — the
# reason every ref is resolved through refs/tags/ rather than by bare name.
DIR=$(fixture orphan-shadowed)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/v2.0.0
git -C "${DIR}" branch v2.0.0
assert "branch does not shadow a missing root tag" 1 "ORPHAN   sdk/go/v2.0.0" "${DIR}"

# The workflow's trigger list is the one part of this check that running the
# script cannot exercise: the audit rejects an orphaned sdk/go tag, but a
# root-only `v*` filter gives that branch no event to fire on, because `v*` does
# not match `sdk/go/v*`. Pin both patterns so a future edit of the filter cannot
# silently drop a namespace.
WORKFLOW="${SCRIPT_DIR}/../.github/workflows/release-lockstep.yml"
if [ ! -f "${WORKFLOW}" ]; then
	echo "FAIL  workflow triggers: ${WORKFLOW} not found"
	FAILURES=$((FAILURES + 1))
else
	TAGS_LINE=$(sed -n 's/^[[:space:]]*tags:[[:space:]]*//p' "${WORKFLOW}" | head -n 1)
	for PATTERN in '"v*"' '"sdk/go/v*"'; do
		case "${TAGS_LINE}" in
		*"${PATTERN}"*)
			echo "ok    workflow triggers on ${PATTERN} tag pushes"
			;;
		*)
			echo "FAIL  workflow tag filter does not include ${PATTERN}: ${TAGS_LINE}"
			FAILURES=$((FAILURES + 1))
			;;
		esac
	done
fi

if [ "${FAILURES}" -ne 0 ]; then
	echo "${FAILURES} case(s) failed"
	exit 1
fi

echo "all cases passed"
