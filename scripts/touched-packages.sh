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
# touched-packages.sh — print the root-module packages a diff changed plus
# their direct importers, as ./<dir> paths usable as
# `make test-pkg PKG=./<dir>`.
#
# Usage:
#   scripts/touched-packages.sh <base> <tip>   paths from git diff --name-only
#   scripts/touched-packages.sh --files        paths on stdin, one per line
#
# With <base> <tip> it refuses (exit 2) unless HEAD is <tip>: packages and
# imports are read from the work tree.
#
# Per changed path:
#   - a docs-only path (ci.yml's DOCS_ONLY_PATTERN, read from ci.yml) is ignored;
#   - a path inside a nested module (a directory below the root with its own
#     go.mod, from git ls-files) is ignored: those have their own suites;
#   - go.mod, go.sum or Makefile prints the single line ALL;
#   - otherwise the owning directory (a /testdata/... suffix stripped to its
#     package) counts when it holds a .go file in the work tree.
# A package prints when it changed or its Imports, TestImports or XTestImports
# (one `go list ./...` under `make -s print-build-tags`) name a changed
# package. Nothing changed in Go prints nothing. $GO overrides `go`.

set -euo pipefail

GO=${GO:-go}

usage() {
	echo "usage: touched-packages.sh <base> <tip> | touched-packages.sh --files (paths on stdin)" >&2
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
		echo "touched-packages.sh: HEAD is not $2; packages are read from the work tree, so check out the tip first" >&2
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
	echo "touched-packages.sh: DOCS_ONLY_PATTERN not found in .github/workflows/ci.yml" >&2
	exit 2
fi

nested=()
while IFS= read -r m; do
	[ "$m" = "go.mod" ] && continue
	nested+=("$(dirname "$m")")
done < <(git ls-files '*go.mod')

declare -A changed=()
while IFS= read -r p; do
	[ -n "$p" ] || continue
	if printf '%s\n' "$p" | grep -qE "$docs"; then
		continue
	fi
	skip=0
	for d in "${nested[@]}"; do
		case "$p" in
		"$d"/*)
			skip=1
			break
			;;
		esac
	done
	[ "$skip" -eq 0 ] || continue
	case "$p" in
	go.mod | go.sum | Makefile)
		echo ALL
		exit 0
		;;
	esac
	case "$p" in
	testdata/*) dir=. ;;
	*/testdata/*) dir=${p%%/testdata/*} ;;
	*/*) dir=${p%/*} ;;
	*) dir=. ;;
	esac
	if compgen -G "$dir/*.go" >/dev/null; then
		changed["$dir"]=1
	fi
done <<<"$paths"

[ "${#changed[@]}" -gt 0 ] || exit 0

mod=$("$GO" list -m)
tags=$(make -s --no-print-directory print-build-tags)

rel() {
	if [ "$1" = "$mod" ]; then
		echo .
	else
		echo "./${1#"$mod"/}"
	fi
}

declare -A want=()
for d in "${!changed[@]}"; do
	if [ "$d" = . ]; then
		want["$mod"]=1
	else
		want["$mod/$d"]=1
	fi
done

listing=$("$GO" list -tags "$tags" -f '{{.ImportPath}}{{"\t"}}{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./...)

{
	for d in "${!changed[@]}"; do
		if [ "$d" = . ]; then echo .; else echo "./$d"; fi
	done
	while IFS=$'\t' read -r pkg imports; do
		for i in $imports; do
			if [ -n "${want[$i]:-}" ]; then
				rel "$pkg"
				break
			fi
		done
	done <<<"$listing"
} | sort -u
