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
# git-extraheader.sh -- SOURCED, not executed, by a Release workflow step,
# with the token in FORGE_TOKEN (the step's env):
#
#     . ./scripts/git-extraheader.sh
#
# The token is read from the environment, not from an argument: POSIX `.` takes
# no arguments (dash ignores them and leaves the caller's $1 in place), so an
# argument would silently become whatever $1 the calling shell happened to have.
# FORGE_TOKEN is unset once the header is built.
#
# It authenticates git for the REST OF THAT ONE STEP and nowhere else. The
# Release workflow checks out with persist-credentials: false, so no token is
# written into .git/config where every later step -- and every process those
# steps start -- could read it. A step that needs the forge passes its token
# here, and the header lives only in that step shell's environment:
#
#   http.<server>/.extraheader = AUTHORIZATION: basic base64(x-access-token:TOKEN)
#
# set through GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n, the same
# header actions/checkout would have persisted. The pair is APPENDED after any
# GIT_CONFIG_* entries already present: a runner that injects its own git
# rewrites for module fetching (dependency-resolution) keeps them.
#
# The encoded value is registered as a mask, so a log line that echoes it
# prints *** instead.

if [ -z "${FORGE_TOKEN:-}" ]; then
	echo "::error::git-extraheader.sh: FORGE_TOKEN is empty; this step has no forge credentials" >&2
	return 1
fi
if [ -z "${GITHUB_SERVER_URL:-}" ]; then
	echo "::error::git-extraheader.sh: GITHUB_SERVER_URL is not set; cannot scope the header to the forge" >&2
	return 1
fi

git_extraheader_auth=$(printf 'x-access-token:%s' "${FORGE_TOKEN}" | base64 | tr -d '\n')
unset FORGE_TOKEN
echo "::add-mask::${git_extraheader_auth}"
git_extraheader_n=${GIT_CONFIG_COUNT:-0}
export "GIT_CONFIG_KEY_${git_extraheader_n}=http.${GITHUB_SERVER_URL%/}/.extraheader"
export "GIT_CONFIG_VALUE_${git_extraheader_n}=AUTHORIZATION: basic ${git_extraheader_auth}"
export GIT_CONFIG_COUNT=$((git_extraheader_n + 1))
unset git_extraheader_auth git_extraheader_n
