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
# accept one and reject the other, which publishes the skew this check exists to
# prevent, and no post-publication gate can retract a tag consumers have already
# resolved.
#
# Usage: check-tag-lockstep.sh [repo-dir]   (default: the current directory)
#
# Reads the tags present in the clone, so a shallow or tagless checkout must
# fetch them first; that case fails rather than reporting a vacuous green.
# Exit status: 0 when every pair is in lockstep, 1 otherwise.

set -eu

cd "${1:-.}"

ROOT_TAGS=$(git tag --list 'v*')
if [ -z "${ROOT_TAGS}" ]; then
	echo "no root version tags in this clone — fetch them before running this check"
	exit 1
fi

FAILED=0

for TAG in ${ROOT_TAGS}; do
	SDK_TAG="sdk/go/${TAG}"
	if ! git rev-parse -q --verify "refs/tags/${SDK_TAG}^{commit}" >/dev/null; then
		echo "MISSING  ${SDK_TAG} — root tag ${TAG} has no sdk/go counterpart"
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

for SDK_TAG in $(git tag --list 'sdk/go/v*'); do
	TAG="${SDK_TAG#sdk/go/}"
	if ! git rev-parse -q --verify "refs/tags/${TAG}^{commit}" >/dev/null; then
		echo "ORPHAN   ${SDK_TAG} — no matching root tag ${TAG}"
		FAILED=1
	fi
done

if [ "${FAILED}" -ne 0 ]; then
	echo "root and sdk/go tags are not in lockstep (release-versioning.md P2)"
	echo "publish a release with: git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z"
	exit 1
fi

echo "root and sdk/go tags are in lockstep"
