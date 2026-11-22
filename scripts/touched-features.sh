#!/usr/bin/env bash
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
# touched-features.sh — print the features whose
# `make integration-test-<feature>` a diff must run.
#
# Usage:
#   scripts/touched-features.sh <base> <tip>   paths from git diff --name-only
#   scripts/touched-features.sh --files        paths on stdin, one per line
#
# The ONE feature table is test/integration/features.mk; this script reads it
# only through `make -s print-integration-feature-map`, never a copy, so with
# <base> <tip> it refuses (exit 2) unless HEAD is <tip>: the table it reads is
# the work tree's.
#
# Per changed path:
#   - a docs-only path (ci.yml's DOCS_ONLY_PATTERN, read from ci.yml) is ignored;
#   - test/integration/<file> named by one or more features in the map counts
#     for those features;
#   - anything else (Makefile, test/integration/features.mk, any other
#     test/integration file, .github, scripts, go.mod/go.sum, any Go package,
#     sdk) prints the single line ALL. A package change means ALL because the
#     table maps features to test files only, and ruling 1171 forbids a second
#     list mapping packages to features.
# Otherwise the features print sorted, one per line; a docs-only or empty diff
# prints nothing. Wrong arguments print the usage on stderr and exit 2.

set -euo pipefail

usage() {
	echo "usage: touched-features.sh <base> <tip> | touched-features.sh --files (paths on stdin)" >&2
	exit 2
}

root=$(git rev-parse --show-toplevel)
cd "$root"

case $# in
1)
	[ "$1" = "--files" ] || usage
	paths=$(cat)
	;;
2)
	head=$(git rev-parse HEAD)
	tip=$(git rev-parse --verify "$2^{commit}") || usage
	git rev-parse --verify "$1^{commit}" >/dev/null || usage
	if [ "$head" != "$tip" ]; then
		echo "touched-features.sh: HEAD is not $2; the feature table is read from the work tree, so check out the tip first" >&2
		exit 2
	fi
	paths=$(git diff --name-only "$1" "$2")
	;;
*)
	usage
	;;
esac

docs=$(sed -n "s/^ *DOCS_ONLY_PATTERN: '\(.*\)' *$/\1/p" .github/workflows/ci.yml | head -n 1)
if [ -z "$docs" ]; then
	echo "touched-features.sh: DOCS_ONLY_PATTERN not found in .github/workflows/ci.yml" >&2
	exit 2
fi

map=$(make -s --no-print-directory print-integration-feature-map)
if [ -z "$map" ]; then
	echo "touched-features.sh: make print-integration-feature-map printed nothing" >&2
	exit 2
fi

found=()
while IFS= read -r p; do
	[ -n "$p" ] || continue
	if printf '%s\n' "$p" | grep -qE "$docs"; then
		continue
	fi
	case "$p" in
	test/integration/*/*)
		echo ALL
		exit 0
		;;
	test/integration/*)
		file=${p#test/integration/}
		hit=0
		while read -r feature files; do
			for f in $files; do
				if [ "$f" = "$file" ]; then
					found+=("$feature")
					hit=1
				fi
			done
		done <<<"$map"
		if [ "$hit" -eq 0 ]; then
			echo ALL
			exit 0
		fi
		;;
	*)
		echo ALL
		exit 0
		;;
	esac
done <<<"$paths"

if [ "${#found[@]}" -gt 0 ]; then
	printf '%s\n' "${found[@]}" | sort -u
fi
