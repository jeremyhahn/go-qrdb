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
# check-version-reconciled.sh: VERSION, the newest CHANGELOG release heading and the git tag must agree.

set -eu

cd "${1:-.}"
TAG="${2:-}"

if [ ! -f VERSION ]; then
	echo "MISSING  VERSION — nothing to reconcile against"
	exit 1
fi
if [ ! -f CHANGELOG.md ]; then
	echo "MISSING  CHANGELOG.md — nothing to reconcile against"
	exit 1
fi

VERSION=$(tr -d ' \t\n\r' < VERSION)
if [ -z "${VERSION}" ]; then
	echo "EMPTY    VERSION holds no version"
	exit 1
fi

# The newest RELEASED heading, which is the first `## [x]` that is not
# [Unreleased]. Keep-a-Changelog puts [Unreleased] first by construction, so
# skipping it by name is what makes "newest release" mean the newest release
# rather than the newest section.
CHANGELOG_VERSION=$(
	sed -n 's/^## \[\([^]]*\)\].*/\1/p' CHANGELOG.md |
		grep -v '^Unreleased$' |
		head -n 1
)
if [ -z "${CHANGELOG_VERSION}" ]; then
	echo "MISSING  CHANGELOG.md has no released section — only [Unreleased]"
	exit 1
fi

FAILED=0

if [ "${VERSION}" != "${CHANGELOG_VERSION}" ]; then
	echo "DRIFT    VERSION is ${VERSION} but the newest CHANGELOG release is ${CHANGELOG_VERSION}"
	echo "         either the release notes were never written, or VERSION was never bumped"
	FAILED=1
fi

if [ -n "${TAG}" ]; then
	# EVERY lockstep namespace names the same version: v0.10.0-alpha,
	# sdk/go/v0.10.0-alpha, sdk/go/gorm/conformance/v0.10.0-alpha. Take the LAST
	# path component rather than stripping a known prefix.
	#
	# It used to strip the literal `sdk/go/`, which was correct while that was
	# the only nested module. The lockstep set is DERIVED now
	# (scripts/lockstep-modules.sh), so the set of prefixes is not fixed, and
	# the Release Lockstep workflow triggers on `sdk/**/v*` — a deeper module's
	# tag reached this check and was compared as `gorm/conformance/v0.10.0-alpha`
	# against `0.10.0-alpha`, reporting DRIFT on a perfectly good release.
	# Measured on PR #212: the widened trigger and this unwidened normaliser
	# disagreed, which is the same publisher/auditor split this recipe has now
	# hit on three different pairs.
	TAG_VERSION=${TAG##*/}
	case "${TAG_VERSION}" in
	v*) ;;
	*)
		# Not a version tag at all. Reported as such rather than left to surface
		# as a confusing DRIFT between the tag and VERSION.
		echo "MALFORMED  ${TAG} does not end in a v-prefixed version component"
		exit 1
		;;
	esac
	TAG_VERSION=${TAG_VERSION#v}
	if [ "${TAG_VERSION}" != "${VERSION}" ]; then
		echo "DRIFT    tag ${TAG} names ${TAG_VERSION} but VERSION is ${VERSION}"
		FAILED=1
	fi
else
	echo "skipped  tag check — no tag supplied (this is not a tag push)"
fi

if [ "${FAILED}" -ne 0 ]; then
	echo "VERSION, the CHANGELOG heading and the tag do not agree (release-versioning.md §2)"
	exit 1
fi

if [ -n "${TAG}" ]; then
	echo "VERSION, the CHANGELOG heading and tag ${TAG} all name ${VERSION}"
else
	echo "VERSION and the newest CHANGELOG heading both name ${VERSION}"
fi
