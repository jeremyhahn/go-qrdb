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
# touched-features.sh: print the features whose integration tests a diff must run.

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
	paths=$(git diff --name-only --no-renames "$1" "$2")
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

# features_of <file>: the features the map names <file> under, one per line.
features_of() {
	local feature files f
	while read -r feature files; do
		for f in $files; do
			if [ "$f" = "$1" ]; then
				echo "$feature"
			fi
		done
	done <<<"$map"
}

# decls <file.go>: the top-level identifiers <file.go> declares (func, method,
# type, var, const, grouped or not), but its Test, Benchmark, Fuzz and
# Example functions, which nothing else calls.
decls() {
	awk '
	/^\)/ { block = 0; next }
	block && /^\t[A-Za-z_]/ { n = substr($0, 2); sub(/[^A-Za-z0-9_].*/, "", n); print n; next }
	/^(var|const|type) \(/ { block = 1; next }
	/^(var|const|type) [A-Za-z_]/ { n = $2; sub(/[^A-Za-z0-9_].*/, "", n); print n; next }
	/^func \(/ { n = $0; sub(/^func \([^)]*\) */, "", n); sub(/[^A-Za-z0-9_].*/, "", n); print n; next }
	/^func [A-Za-z_]/ { n = $2; sub(/[^A-Za-z0-9_].*/, "", n); print n; next }
	' "$1" | grep -vE '^((Test|Benchmark|Fuzz|Example)([A-Z_].*)?|_)?$' | sort -u || true
}

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
		fs=$(features_of "${p#test/integration/}")
		if [ -z "$fs" ]; then
			echo ALL
			exit 0
		fi
		# shellcheck disable=SC2206 # feature names hold no spaces or globs
		found+=($fs)
		# The file's helpers: every other test/integration file that names one
		# of its top-level identifiers adds its features, and an unmapped
		# (helper) file naming one means ALL. A deleted file declares nothing
		# its callers can still reach: they fail to compile in every feature.
		[ -f "$p" ] || continue
		ids=$(decls "$p" | paste -sd'|' -)
		[ -n "$ids" ] || continue
		while IFS= read -r user; do
			[ -n "$user" ] && [ "$user" != "$p" ] || continue
			fs=$(features_of "${user#test/integration/}")
			if [ -z "$fs" ]; then
				echo ALL
				exit 0
			fi
			# shellcheck disable=SC2206
			found+=($fs)
		done < <(grep -lwE "($ids)" test/integration/*.go || true)
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
