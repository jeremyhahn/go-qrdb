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
# Regression tests for git-extraheader.sh, the helper that gives ONE Release
# workflow step the forge credential instead of persisting it in .git/config.
# Each case sources the helper in a subshell and asks git itself what it sees,
# so the assertion is on git's view of the header, not on variable names.
# Nothing here reads or writes any git config file: HOME and
# GIT_CONFIG_GLOBAL point at an empty temp directory and system config is off.
#
# Usage: git-extraheader_test.sh   (no network)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
HELPER="${SCRIPT_DIR}/git-extraheader.sh"

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT INT TERM
export HOME="${WORK}" GIT_CONFIG_GLOBAL="${WORK}/gitconfig" GIT_CONFIG_NOSYSTEM=1
: > "${WORK}/gitconfig"

FAILURES=0
ok() { echo "ok    $1"; }
bad() {
	echo "FAIL  $1: $2"
	FAILURES=$((FAILURES + 1))
}

SERVER=https://forge.example.com
WANT="AUTHORIZATION: basic $(printf 'x-access-token:%s' s3cret | base64 | tr -d '\n')"

NAME="git sees the header for the forge, from the environment only"
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
GOT=$(env -u GIT_CONFIG_COUNT GITHUB_SERVER_URL="${SERVER}" FORGE_TOKEN=s3cret sh -c '. "$1" >/dev/null && git config --get "http.$2/.extraheader"' _ "${HELPER}" "${SERVER}") || GOT=''
if [ "${GOT}" = "${WANT}" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "git saw '${GOT}'"
fi

NAME="an existing GIT_CONFIG_* entry is kept, and the header is appended after it"
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
OUT=$(GITHUB_SERVER_URL="${SERVER}" FORGE_TOKEN=s3cret GIT_CONFIG_COUNT=1 \
	GIT_CONFIG_KEY_0='url.https://mirror.example.com/.insteadOf' \
	GIT_CONFIG_VALUE_0='https://github.com/' \
	sh -c '. "$1" >/dev/null && echo "$GIT_CONFIG_COUNT" && git config --get url.https://mirror.example.com/.insteadOf && git config --get "http.$2/.extraheader"' _ "${HELPER}" "${SERVER}") || OUT=''
EXPECTED=$(printf '2\nhttps://github.com/\n%s' "${WANT}")
if [ "${OUT}" = "${EXPECTED}" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "got: $(printf '%s' "${OUT}" | tr '\n' '|')"
fi

NAME="the encoded credential is registered as a mask"
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
OUT=$(env -u GIT_CONFIG_COUNT GITHUB_SERVER_URL="${SERVER}" FORGE_TOKEN=s3cret sh -c '. "$1"' _ "${HELPER}") || OUT=''
case "${OUT}" in
"::add-mask::${WANT#AUTHORIZATION: basic }") ok "${NAME}" ;;
*) bad "${NAME}" "output was '${OUT}'" ;;
esac

NAME="nothing is written to a git config file"
if [ -s "${WORK}/gitconfig" ]; then
	bad "${NAME}" "the global config was written"
else
	ok "${NAME}"
fi

NAME="the token does not outlive the helper"
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
OUT=$(env -u GIT_CONFIG_COUNT GITHUB_SERVER_URL="${SERVER}" FORGE_TOKEN=s3cret sh -c '. "$1" >/dev/null; echo "[${FORGE_TOKEN:-}]"' _ "${HELPER}") || OUT=''
if [ "${OUT}" = "[]" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "FORGE_TOKEN is still set: ${OUT}"
fi

NAME="an empty token is refused, and the caller stops"
RC=0
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
OUT=$(env -u GIT_CONFIG_COUNT -u FORGE_TOKEN GITHUB_SERVER_URL="${SERVER}" sh -c 'set -e; . "$1"; echo "reached"' _ "${HELPER}" 2>&1) || RC=$?
case "${RC}:${OUT}" in
0:*) bad "${NAME}" "exit 0" ;;
*"reached"*) bad "${NAME}" "the caller continued past the refusal" ;;
*"FORGE_TOKEN is empty"*) ok "${NAME}" ;;
*) bad "${NAME}" "wrong diagnostic: ${OUT}" ;;
esac

NAME="a missing server URL is refused"
RC=0
# shellcheck disable=SC2016 # the inner sh expands its own positional parameters
OUT=$(env -u GIT_CONFIG_COUNT -u GITHUB_SERVER_URL FORGE_TOKEN=s3cret sh -c 'set -e; . "$1"; echo "reached"' _ "${HELPER}" 2>&1) || RC=$?
case "${RC}:${OUT}" in
0:*) bad "${NAME}" "exit 0" ;;
*"GITHUB_SERVER_URL is not set"*) ok "${NAME}" ;;
*) bad "${NAME}" "wrong diagnostic: ${OUT}" ;;
esac

if [ "${FAILURES}" -ne 0 ]; then
	echo "${FAILURES} case(s) failed"
	exit 1
fi
echo "all cases passed"
