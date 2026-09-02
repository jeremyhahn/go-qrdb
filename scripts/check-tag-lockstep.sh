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
# check-tag-lockstep.sh — release-versioning.md P2: the nested sdk/go module is
# tagged in lockstep with the root module.
#
# When a root vX.Y.Z tag ships without sdk/go/vX.Y.Z, `go get` at latest
# resolves the root to X.Y.Z and sdk/go to the previous release. Any symbol the
# release removed from a package sdk/go re-exports then breaks the consumer's
# build — go-qrdb#6: sdk/go v0.5.8-alpha still aliased
# statemachine.CachedDiskBackend, deleted in v0.5.9-alpha.
#
# Every root tag must have an sdk/go counterpart at the same commit, and no
# sdk/go tag may exist without its root. Publish a release by pushing both tags
# in ONE atomic push:
#
#     git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z
#
# --atomic is what makes it one transaction: the server applies both ref updates
# or neither. Listing two refs on a plain push is not enough — the server may
# accept one and reject the other, which publishes the skew.
#
# Running this script before that push is the only preventive step available
# today. Once a tag is pushed it is visible to `go get` immediately, so the
# Release Lockstep workflow reports a skewed release rather than stopping it;
# preventing one needs a server-side pre-receive policy or workflow-only
# tagging, which is release policy this repository does not set — go-qrdb#13.
#
# Usage: check-tag-lockstep.sh [repo-dir] [module ...]
#
#   repo-dir  defaults to the current directory
#   module    the nested module paths that must be tagged in lockstep with the
#             root. Defaults to `sdk/go`, which is what the Release Lockstep
#             workflow gets when it calls this with no arguments -- its
#             behaviour is unchanged by the argument existing.
#
# The list is a PARAMETER because release-alpha now DISCOVERS the lockstep set
# from the module tree rather than declaring it (go-qrdb#160), and an audit that
# knows about fewer namespaces than the tagger cuts is an audit that passes
# while one of them is broken. go-qrdb#50 added a third module,
# sdk/go/gorm/conformance; the release recipe passes its discovered set here so
# the two cannot disagree. Making the WORKFLOW audit that set as well is the
# remaining half of #50 and is not done here.
#
# Reads the tags present in the clone, so a shallow or tagless checkout must
# fetch them first; that case fails rather than reporting a vacuous green.
# Exit status: 0 when every pair is in lockstep, 1 otherwise.

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

cd "${1:-.}"

# `shift` is a POSIX SPECIAL BUILT-IN: when it fails, a non-interactive shell
# exits, and `|| true` does not save it -- the shell is gone before the operator
# is considered. With no arguments at all (the workflow's invocation) that made
# this script exit 2 silently. Test the count instead of catching the failure.
if [ $# -gt 1 ]; then
	shift
	MODULES=$*
else
	# THE SAME DERIVATION release-alpha PUBLISHES. Not a copy of it, and not a
	# hardcoded default that happens to agree: an auditor narrower than the
	# publisher passes green while a namespace goes unchecked, which is silent
	# in the one direction that matters.
	# NO PIPE. `x=$(cmd | tr ...)` yields tr's status, not cmd's, so a failing
	# discovery would arrive as an empty set and a green audit -- the same
	# defect this file's own comment warns about two functions down, which I
	# wrote and then committed anyway. The list is newline-separated and `for`
	# splits on newlines, so nothing needs converting.
	MODULES=$("${SCRIPT_DIR}/lockstep-modules.sh" .) || {
		echo "could not determine the lockstep module set"
		exit 1
	}
fi

ROOT_TAGS=$(git tag --list 'v*')
if [ -z "${ROOT_TAGS}" ]; then
	echo "no root version tags in this clone — fetch them before running this check"
	exit 1
fi

FAILED=0

for MODULE in ${MODULES}; do
	# The MISSING direction is scoped to releases cut AT OR AFTER the version at
	# which this module became REQUIRED, and that version is DECLARED in
	# lockstep-baseline rather than inferred. Inference was tried twice and both
	# forms are unsatisfiable on this repository -- all history, and the commit
	# that added the module's go.mod (27 root tags postdate it). Inferring from
	# the module's first tag is satisfiable but fail-open: a module with no tags
	# is scoped out entirely, so the release that should have cut its first tag
	# is the one nothing checks.
	BASELINE=$("${SCRIPT_DIR}/lockstep-modules.sh" --baseline "${MODULE}" .) || BASELINE=""

	if [ -z "${BASELINE}" ]; then
		echo "UNDECLARED  ${MODULE} — discovered as a lockstep module but absent from"
		echo "            lockstep-baseline. Declare it, with a version or 'unreleased'."
		FAILED=1
		continue
	fi

	if [ "${BASELINE}" = "unreleased" ]; then
		# `unreleased` is a claim about ONE DIRECTION of this audit: no root
		# release owes this module a counterpart yet. It is NOT a claim that
		# tags in the namespace are none of our business -- it says the
		# opposite, because a tag in a namespace declared unreleased contradicts
		# the declaration itself.
		#
		# This branch used to `continue` above the reverse scan, which disabled
		# the ORPHAN direction for the module -- the one direction that catches a
		# HAND-PUSHED tag, for the one module in the repository most likely to
		# receive a stray one, because it is the one nobody has released. A scope
		# argued for one direction, applied to both. Caught in review of PR #212.
		#
		# So the assertion here is STRONGER than orphan: ANY tag in the namespace
		# fails, whether or not a root tag matches it. Under the three-state
		# design this is exactly the state the declaration exists to make
		# impossible, and it gives go-qrdb#50 a tripwire -- tag conformance
		# without flipping its baseline line and the audit says which line to
		# edit.
		STRAY=""
		for CANDIDATE in $(git tag --list "${MODULE}/v*"); do
			case "${CANDIDATE#"${MODULE}"/}" in
			*/*) continue ;;
			esac
			STRAY="${STRAY} ${CANDIDATE}"
		done

		if [ -n "${STRAY}" ]; then
			for CANDIDATE in ${STRAY}; do
				echo "UNRELEASED_BUT_TAGGED  ${CANDIDATE}"
			done
			echo "           lockstep-baseline declares \"${MODULE} unreleased\", so no tag should"
			echo "           exist in that namespace at all. Either the tag was pushed by hand, or"
			echo "           the module has been released and that line is stale — edit it."
			FAILED=1
		else
			echo "declared    ${MODULE} — unreleased, and untagged as declared"
		fi
		continue
	fi

	# A baseline naming a tag that does not exist yet owes nothing yet: that is
	# how the first release including a module is cut.
	FIRST=$(git rev-parse -q --verify "refs/tags/${BASELINE}^{commit}") || FIRST=""
	if [ -z "${FIRST}" ]; then
		echo "pending     ${MODULE} — baseline ${BASELINE} is not tagged yet"
		continue
	fi

	MODULE_TAGS=""
	for CANDIDATE in $(git tag --list "${MODULE}/v*"); do
		case "${CANDIDATE#"${MODULE}"/}" in
		*/*) continue ;;
		esac
		MODULE_TAGS="${MODULE_TAGS} ${CANDIDATE}"
	done

	for TAG in ${ROOT_TAGS}; do
		if ! git merge-base --is-ancestor "${FIRST}" "$(git rev-parse "refs/tags/${TAG}^{commit}")"; then
			continue
		fi
		SDK_TAG="${MODULE}/${TAG}"
		if ! git rev-parse -q --verify "refs/tags/${SDK_TAG}^{commit}" >/dev/null; then
			echo "MISSING  ${SDK_TAG} — root tag ${TAG} has no ${MODULE} counterpart"
			FAILED=1
			continue
		fi
		ROOT_SHA=$(git rev-parse "refs/tags/${TAG}^{commit}")
		SDK_SHA=$(git rev-parse "refs/tags/${SDK_TAG}^{commit}")
		if [ "${ROOT_SHA}" != "${SDK_SHA}" ]; then
			echo "SKEW     ${TAG} is ${ROOT_SHA} but ${SDK_TAG} is ${SDK_SHA}"
			FAILED=1
		fi
	done

	# The reverse direction. A nested tag with no root counterpart is equally
	# unbuildable, and the pattern has to be anchored per module: `sdk/go/v*`
	# does not match sdk/go/gorm/conformance/v1.0.0, so auditing the modules
	# separately is what keeps a longer path from hiding inside a shorter one.
	# The ORPHAN direction is NOT scoped: a nested tag with no root counterpart
	# is unbuildable whenever it was cut, and it is the direction that catches a
	# tag pushed by hand outside this tooling.
	for SDK_TAG in ${MODULE_TAGS}; do
		TAG="${SDK_TAG#"${MODULE}"/}"
		if ! git rev-parse -q --verify "refs/tags/${TAG}^{commit}" >/dev/null; then
			echo "ORPHAN   ${SDK_TAG} — no matching root tag ${TAG}"
			FAILED=1
		fi
	done
done

if [ "${FAILED}" -ne 0 ]; then
	echo "root and nested-module tags are not in lockstep (release-versioning.md P2)"
	echo "publish a release with: git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z"
	exit 1
fi

# Unquoted on purpose: the shell collapses the newline-separated list into a
# single spaced line for the message.
# shellcheck disable=SC2086
echo "root and nested-module tags are in lockstep ($(echo ${MODULES}))"
