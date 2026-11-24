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
# check-iface-sweep-findings.sh: the committed ifacesweep findings must not contain absolute paths.

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
FLOOR=100

if [ "$#" -gt 0 ]; then
	files="$*"
else
	files="test/tools/ifacesweep/findings/min-tags.tsv test/tools/ifacesweep/findings/full-tags.tsv"
fi

fail=0
total_files=0

for rel in $files; do
	total_files=$((total_files + 1))
	# Absolute arguments (the self-test's synthetic fixtures) are used
	# as-is; the default, repo-relative paths are resolved against
	# REPO_ROOT so the check works from any invocation directory.
	case "$rel" in
	/*) path="$rel" ;;
	*) path="$REPO_ROOT/$rel" ;;
	esac

	if [ ! -f "$path" ]; then
		echo "FAIL: $rel does not exist" >&2
		fail=1
		continue
	fi

	header=$(head -n 1 -- "$path")
	if [ -z "$header" ]; then
		echo "FAIL: $rel is empty (no header row) -- the discovery floor caught a missing artifact" >&2
		fail=1
		continue
	fi

	# Find the "file" column by NAME, not a hardcoded index -- a reordered
	# or added column must not silently start checking the wrong field.
	col=$(printf '%s' "$header" | awk -F'\t' '{for (i=1;i<=NF;i++) if ($i=="file") {print i; exit}}')
	if [ -z "$col" ]; then
		echo "FAIL: $rel header has no 'file' column: $header" >&2
		fail=1
		continue
	fi

	rows=$(($(wc -l < "$path") - 1))
	if [ "$rows" -lt "$FLOOR" ]; then
		echo "FAIL: $rel has $rows data row(s), below the $FLOOR floor -- the discovery floor caught a truncated or vacuous artifact" >&2
		fail=1
		continue
	fi

	# awk does the per-row check in one pass rather than a shell loop over
	# every row, which for ~1000 rows per file is the difference between
	# this running in a fraction of a second and visibly slowing the job.
	bad=$(awk -F'\t' -v col="$col" '
		NR==1 { next }
		{
			f = $col
			if (f ~ /^\// || f == ".." || f ~ /^\.\.\// || f ~ /^[A-Za-z]:[\\\/]/) {
				print NR": "f
			}
		}
	' "$path")

	if [ -n "$bad" ]; then
		echo "FAIL: $rel has row(s) whose file column is absolute or escapes the module root:" >&2
		echo "$bad" >&2
		fail=1
		continue
	fi

	echo "ok  $rel: $rows data row(s), all file paths clean and repo-relative"
done

if [ "$total_files" -eq 0 ]; then
	echo "FAIL: no files given and no default resolved -- this check would have been vacuous" >&2
	exit 1
fi

exit "$fail"
