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
# release-bot-git.sh -- the AGENT_GIT wrapper the Release workflow hands to
# `make release-alpha` (#13).
#
# release-alpha makes every git write through AGENT_GIT, invoked as
#
#     release-bot-git.sh <repo> <git args...>
#
# so that each commit and tag carries one identity and one SSH signature. On a
# developer machine that wrapper is the operator's own tooling; in the workflow
# it is this script, and the identity is the release bot's: the only principal
# tag protection lets create a release tag.
#
# Everything comes from the environment the workflow sets, and every value is
# REQUIRED. A missing one is a refusal, never a fallback to whatever git
# identity or key the runner happens to have: a release tag is permanent, and
# one signed by the wrong key cannot be retracted.
#
#   RELEASE_BOT_NAME              the tagger name
#   RELEASE_BOT_EMAIL             the tagger email
#   RELEASE_BOT_SIGNING_KEY_FILE  a private SSH key file, mode 0600
#
# The <repo> argument is accepted for CLI compatibility and checked to be a
# plain name; the identity does not derive from it.

set -eu

if [ "$#" -lt 2 ]; then
	echo "release-bot-git: usage: release-bot-git.sh <repo> <git args...>" >&2
	exit 2
fi

REPO="$1"
shift
case "${REPO}" in
'' | *[!A-Za-z0-9._-]*)
	echo "release-bot-git: <repo> is not a plain repository name: ${REPO}" >&2
	exit 2
	;;
esac

for NAME in RELEASE_BOT_NAME RELEASE_BOT_EMAIL RELEASE_BOT_SIGNING_KEY_FILE; do
	eval "VALUE=\${${NAME}:-}"
	if [ -z "${VALUE}" ]; then
		echo "release-bot-git: ${NAME} is not set. The release bot signs with its own identity and key or not at all." >&2
		exit 1
	fi
done

if [ ! -f "${RELEASE_BOT_SIGNING_KEY_FILE}" ]; then
	echo "release-bot-git: RELEASE_BOT_SIGNING_KEY_FILE does not name a file: ${RELEASE_BOT_SIGNING_KEY_FILE}" >&2
	exit 1
fi

exec git \
	-c "user.name=${RELEASE_BOT_NAME}" \
	-c "user.email=${RELEASE_BOT_EMAIL}" \
	-c gpg.format=ssh \
	-c "user.signingkey=${RELEASE_BOT_SIGNING_KEY_FILE}" \
	-c commit.gpgsign=true \
	-c tag.gpgsign=true \
	"$@"
