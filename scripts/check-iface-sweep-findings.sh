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
# check-iface-sweep-findings.sh — the committed test/tools/ifacesweep/
# findings/*.tsv files must never carry an absolute, machine-specific path
# in their `file` column (go-qrdb#271 review).
#
# WHY THIS EXISTS
#
# ifacesweep prints the source file of every interface method it finds, and
# the first version of it printed token.Position.Filename VERBATIM. That is
# absolute — go/packages resolves it against the checkout, so the committed
# TSV read e.g. <repo-root>/cmd/qrdbd/cluster_migrate.go, <repo-root> being
# the absolute path of the reviewer's worktree, on 996 of 996 rows. Caught in
# review, not by a check: the publication-bound scanner's scope is docs/*/, so
# an absolute host path under test/tools/ was invisible to it (go-qrdb, filed
# separately). Two things were wrong with that output, and this script is
# against both:
#
#   1. It baked the reviewer's own workstation layout and worktree name into
#      version control — the same class of leak check-publication-bound.sh
#      exists to stop elsewhere in the tree.
#   2. It made the file NON-DIFFABLE. `make iface-sweep` regenerates it from
#      whatever absolute path the checkout happens to sit at, so every row
#      changes on every other machine and a real diff (a method that became
#      called, a new interface) is buried in checkout-path noise. The entire
#      reason to commit a stable reference measurement is defeated by the
#      one column that carries a location.
#
# main.go now relativizes every File against the module root before it is
# ever written (relFile), and refuses to emit anything that is not clean and
# inside that root. This script is the second, independent check on the
# ARTIFACT rather than the tool that wrote it — a bug in main.go's next
# revision is caught here even if nobody re-reads relFile's own reasoning at
# the time.
#
# DISCOVERY FLOOR. A missing file, an empty file, or a header-only file must
# FAIL, not pass by having nothing to check — that is exactly the failure
# mode #337's own findings record: a check with nothing in front of it
# reads as a clean bill of health. FLOOR below is deliberately far under the
# ~950-1000 rows measured at the time of writing: the finding count is
# expected to drift as the tree grows, and this script's job is to catch
# "the artifact is gone or truncated", not to pin an exact count that would
# make every `make iface-sweep` regeneration this script's problem too.
#
# Usage: check-iface-sweep-findings.sh [file...]
#   (defaults to both committed findings files, resolved from this script's
#   own location so it works from any invocation directory)

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
