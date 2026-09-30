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
	# The default module set is DISCOVERED from the tree by
	# lockstep-modules.sh, the same derivation release-alpha publishes, so a
	# fixture needs a real module layout for the default path to mean anything.
	mkdir -p "${DIR}/sdk/go"
	printf 'module github.com/jeremyhahn/go-qrdb/sdk/go\n\ngo 1.26\n' > "${DIR}/sdk/go/go.mod"
	# The audit is scoped by a DECLARED baseline, not an inferred one, so a
	# fixture needs the declaration. v1.0.0 is the first release in these
	# fixtures' history.
	printf '%s\n' 'sdk/go v1.0.0' > "${DIR}/lockstep-baseline"
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
assert "matched pairs pass" 0 "root and nested-module tags are in lockstep (sdk/go)" "${DIR}"

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
# The module list is a PARAMETER now, because release-alpha discovers its
# lockstep set rather than declaring it (go-qrdb#160) and an audit that knows
# about fewer namespaces than the tagger cuts passes while one is broken. The
# default is unchanged, which is what keeps the workflow's behaviour identical;
# these cases cover the form the release recipe actually calls.
#
# assert_args NAME EXPECTED_STATUS EXPECTED_SUBSTRING REPO_DIR MODULES...
assert_args() {
	NAME="$1"
	WANT_STATUS="$2"
	WANT_TEXT="$3"
	DIR="$4"
	shift 4

	set +e
	OUTPUT=$("${CHECK}" "${DIR}" "$@" 2>&1)
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

# Two modules, both in lockstep with the root.
DIR=$(fixture multi-ok)
printf '%s\n' 'sdk/go/gorm/conformance v1.0.0' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v1.0.0
assert_args "every named module is audited when all are in lockstep" 0 \
	"in lockstep (sdk/go sdk/go/gorm/conformance)" "${DIR}" sdk/go sdk/go/gorm/conformance

# A module that has JOINED the lockstep and was then left out of a later
# release. This is the shape the scoping must still catch: conformance is
# tagged at v1.0.0, so v2.0.0 owes it a counterpart and does not have one.
DIR=$(fixture multi-missing)
printf '%s\n' 'sdk/go/gorm/conformance v1.0.0' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v1.0.0
commit "${DIR}" two
tag "${DIR}" v2.0.0
tag "${DIR}" sdk/go/v2.0.0
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
assert "the DEFAULT audit now covers a deeper module, because it derives the set" 1 \
	"MISSING  sdk/go/gorm/conformance/v2.0.0" "${DIR}"
assert_args "the same, when the set is passed explicitly" 1 \
	"MISSING  sdk/go/gorm/conformance/v2.0.0" "${DIR}" sdk/go sdk/go/gorm/conformance

# All three tags of a release exist, but the DEEPEST module's leg sits on a
# different commit. This is the skewed set the Release workflow's atomic push
# must never publish (#13): the audit that release-alpha runs on the tags it
# just created, before pushing, has to see a skew in any module, not only
# sdk/go.
DIR=$(fixture multi-skew)
printf '%s\n' 'sdk/go/gorm/conformance v1.0.0' >> "${DIR}/lockstep-baseline"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v1.0.0
commit "${DIR}" two
tag "${DIR}" v2.0.0
tag "${DIR}" sdk/go/v2.0.0
tag "${DIR}" sdk/go/gorm/conformance/v2.0.0 HEAD~1
assert "a skewed three-module tag set is refused" 1 "SKEW" "${DIR}"

# A module DECLARED unreleased is out of scope for the history audit -- and it is
# out of scope because somebody wrote it down, not because the audit inferred it
# from an absence. That distinction is the whole of this file's design: an
# inferred skip is a fail-open (the release that should cut the first tag is the
# one nothing checks), a declared one is a reviewable decision.
DIR=$(fixture declared-unreleased)
printf '%s\n' 'sdk/go/gorm/conformance unreleased' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
assert "a module declared unreleased is out of scope, and says so" 0 \
	"declared    sdk/go/gorm/conformance" "${DIR}"

# `unreleased` scopes out ONE direction -- no root release owes a counterpart --
# and must not scope out the reverse scan. A tag in a namespace declared
# unreleased contradicts the declaration, so ANY tag there fails, whether or not
# a root tag matches it. This is the case that would have caught the regression:
# a `continue` above the reverse scan made a stray conformance tag invisible,
# disabling the one direction that catches a HAND-PUSHED tag for the one module
# most likely to receive one.
DIR=$(fixture unreleased-but-tagged)
printf '%s\n' 'sdk/go/gorm/conformance unreleased' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v1.0.0
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
assert "a tag in a namespace declared unreleased is a failure" 1 \
	"UNRELEASED_BUT_TAGGED  sdk/go/gorm/conformance/v1.0.0" "${DIR}"
assert "and the diagnostic names the baseline line it contradicts" 1 \
	'declares "sdk/go/gorm/conformance unreleased"' "${DIR}"

# Note the tag above MATCHES a root tag, so an orphan-only check would have
# passed it. The failure is the declaration being contradicted, not the absence
# of a counterpart.

# An UNDECLARED module fails. Discovered but never decided on is worse than
# declared-unreleased, because nobody chose it.
DIR=$(fixture undeclared)
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
assert "an undeclared discovered module fails the audit" 1 \
	"UNDECLARED  sdk/go/gorm/conformance" "${DIR}"

# A baseline naming a tag that does not exist yet owes nothing yet: that is how
# the first release including a module is cut.
DIR=$(fixture baseline-pending)
printf '%s\n' 'sdk/go/gorm/conformance v2.0.0' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
assert "a baseline that is not tagged yet owes nothing yet" 0 \
	"pending     sdk/go/gorm/conformance" "${DIR}"

# A nested module whose path is a PREFIX of another must not swallow it:
# `git tag --list 'sdk/go/v*'` does not match sdk/go/gorm/conformance/v1.0.0,
# and the orphan scan has to be anchored per module or a longer path hides
# inside a shorter one.
DIR=$(fixture multi-orphan)
printf '%s\n' 'sdk/go/gorm/conformance v1.0.0' >> "${DIR}/lockstep-baseline"
commit "${DIR}" one
tag "${DIR}" v1.0.0
tag "${DIR}" sdk/go/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v1.0.0
tag "${DIR}" sdk/go/gorm/conformance/v2.0.0
assert_args "an orphan in a deeper module is rejected" 1 \
	"ORPHAN   sdk/go/gorm/conformance/v2.0.0" "${DIR}" sdk/go sdk/go/gorm/conformance

WORKFLOW="${SCRIPT_DIR}/../.github/workflows/release-lockstep.yml"
if [ ! -f "${WORKFLOW}" ]; then
	echo "FAIL  workflow triggers: ${WORKFLOW} not found"
	FAILURES=$((FAILURES + 1))
else
	TAGS_LINE=$(sed -n 's/^[[:space:]]*tags:[[:space:]]*//p' "${WORKFLOW}" | head -n 1)
	for PATTERN in '"v*"' '"sdk/go/v*"' '"sdk/**/v*"'; do
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
