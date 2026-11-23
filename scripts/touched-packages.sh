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
#     go.mod, from git ls-files) is ignored: those have their own suites
#     (a go.mod under a testdata/ directory is a fixture, not a module);
#   - go.mod, go.sum or Makefile prints the single line ALL;
#   - otherwise the owning directory (a /testdata/... suffix stripped to its
#     package) counts when it holds a .go file in the work tree;
#   - a .go path whose directory holds no .go file (a deleted package) prints
#     ALL: its importers cannot be read from the work tree.
# A renamed file counts at both ends (git diff --no-renames).
# A package is selected when it changed or its Imports, TestImports or
# XTestImports name a changed package in any listing: one `go list ./...`
# under `make -s print-build-tags`, plus one per line `<tag> <WITH_flag>=1` of
# `make -s print-optional-test-tags` under those tags and <tag> (qrdb_fts:
# pkg/fts imports pkg/storage only under it). A selected package prints
# ./<dir> when the default listing has it; it also prints
# `./<dir> <WITH_flag>=1` when its file set (GoFiles, TestGoFiles,
# XTestGoFiles) under <tag> differs from the default one or only exists under
# <tag>. `make test-pkg PKG=$line`, unquoted, turns that second word into the
# make variable that adds <tag> to BUILD_TAGS. Nothing changed in Go prints
# nothing. The listings run with -mod=readonly, so go.sum is never written.
# $GO overrides `go`.

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
	# --no-renames: a moved file prints both its source and its destination,
	# so the package it left counts too.
	paths=$(git diff --name-only --no-renames "$1" "$2")
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
done < <(git ls-files -- 'go.mod' '*/go.mod' ':(exclude,glob)testdata/**' ':(exclude,glob)**/testdata/**')

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
	elif [ "$dir" = "${p%/*}" ] && [ "${p%.go}" != "$p" ]; then
		# A .go file whose directory holds no .go file now: the package was
		# deleted (or moved away whole), and its importers are not in the
		# work tree's graph to find.
		echo ALL
		exit 0
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

# One line per package: import path, its Imports/TestImports/XTestImports, and
# its file set (GoFiles/TestGoFiles/XTestGoFiles). -mod=readonly: reading the
# graph never writes go.sum, whatever GOFLAGS says.
fmt='{{.ImportPath}}{{"\t"}}{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}{{"\t"}}{{join .GoFiles " "}} {{join .TestGoFiles " "}} {{join .XTestGoFiles " "}}'
list() {
	"$GO" list -mod=readonly -tags "$1" -f "$fmt" ./...
}

# hit <pkg> <imports>: true when <pkg> changed or imports a changed package.
hit() {
	[ -z "${want[$1]:-}" ] || return 0
	local i
	for i in $2; do
		[ -z "${want[$i]:-}" ] || return 0
	done
	return 1
}

default=$(list "$tags")
optional=$(make -s --no-print-directory print-optional-test-tags)

{
	declare -A files=() sel=() listed=()
	while IFS=$'\t' read -r pkg imports fs; do
		[ -n "$pkg" ] || continue
		files["$pkg"]=$fs
		listed["$pkg"]=1
		if hit "$pkg" "$imports"; then sel["$pkg"]=1; fi
	done <<<"$default"
	# Each supported default-off tag: a package selected under it whose file
	# set differs from the default one (or that only exists under the tag)
	# also prints `./<dir> <WITH_flag>=1`, so `make test-pkg PKG=$line`
	# compiles it with the tag.
	while read -r tag flag; do
		[ -n "$tag" ] || continue
		while IFS=$'\t' read -r pkg imports fs; do
			[ -n "$pkg" ] || continue
			listed["$pkg"]=1
			hit "$pkg" "$imports" || continue
			sel["$pkg"]=1
			if [ -z "${files[$pkg]+x}" ] || [ "${files[$pkg]}" != "$fs" ]; then
				echo "$(rel "$pkg") $flag"
			fi
		done <<<"$(list "$tags $tag")"
	done <<<"$optional"
	# A changed directory no listing shows still prints, as before.
	for p in "${!want[@]}"; do
		[ -n "${listed[$p]:-}" ] || rel "$p"
	done
	for p in "${!sel[@]}"; do
		[ -z "${files[$p]+x}" ] || rel "$p"
	done
} | sort -u
