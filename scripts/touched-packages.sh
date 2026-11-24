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
# touched-packages.sh: print the packages a diff changed plus their direct importers, as ./<dir> paths.

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

optional=$(make -s --no-print-directory print-optional-test-tags)

# Every listing runs here, before the selection block: an unreadable listing
# exits 2 and prints nothing, never a partial (default-only) selection.
if ! default=$(list "$tags"); then
	echo "touched-packages.sh: go list under the default tags failed; the importer graph is unreadable" >&2
	exit 2
fi
opt_flags=() opt_lists=()
while read -r tag flag; do
	[ -n "$tag" ] || continue
	if ! listing=$(list "$tags $tag"); then
		echo "touched-packages.sh: go list under the default tags plus $tag failed; the importer graph under $tag is unreadable" >&2
		exit 2
	fi
	opt_flags+=("$flag")
	opt_lists+=("$listing")
done <<<"$optional"

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
	for i in "${!opt_lists[@]}"; do
		flag=${opt_flags[$i]}
		while IFS=$'\t' read -r pkg imports fs; do
			[ -n "$pkg" ] || continue
			listed["$pkg"]=1
			hit "$pkg" "$imports" || continue
			sel["$pkg"]=1
			if [ -z "${files[$pkg]+x}" ] || [ "${files[$pkg]}" != "$fs" ]; then
				echo "$(rel "$pkg") $flag"
			fi
		done <<<"${opt_lists[$i]}"
	done
	# A changed directory no listing shows still prints, as before.
	for p in "${!want[@]}"; do
		[ -n "${listed[$p]:-}" ] || rel "$p"
	done
	for p in "${!sel[@]}"; do
		[ -z "${files[$p]+x}" ] || rel "$p"
	done
} | sort -u
