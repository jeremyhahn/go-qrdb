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
# check-artifact-paths.sh — no committed, machine-generated reference artifact
# may carry an absolute, workstation-specific path (go-qrdb#403).
#
# WHY THIS EXISTS
#
# check-iface-sweep-findings.sh (go-qrdb#271) closed this for exactly two
# files -- test/tools/ifacesweep/findings/{min,full}-tags.tsv -- after the
# first revision of that tool printed token.FileSet.Position(...).Filename
# verbatim and baked a reviewer's own checkout path into 996 of 996 rows,
# uncaught by review. This script is the repo-wide generalization: it covers
# every KNOWN generated artifact, and the next one is added to ARTIFACTS
# (and taught to artifact_shape) the day it is committed, rather than
# rediscovering the convention in review.
#
# ALLOWLIST, NOT DENYLIST -- THE SECOND DESIGN, NOT THE FIRST
#
# The first revision of this script denied a growing list of absolute-path
# SHAPES: /home/<user>, /Users/<user>, a Windows drive letter, a UNC share
# (both slash directions), then a Windows extended-length path
# (`\\?\C:\...`, `\\?\UNC\server\share\...`). Each review round found a
# shape it did not cover, because a denylist can only be as complete as the
# list of attacks someone has thought of.
#
# So this is an ALLOWLIST: for each artifact, this script knows the exact
# SHAPE every legitimate value in its path-bearing column takes -- a Go
# import path rooted at this module, or a path relative to the repository
# root under one of its real top-level directories -- and rejects a value
# in that column that is not one of those shapes, WHATEVER shape it is
# instead. `.` and `..` path segments are excluded from both shapes for the
# same reason: `pkg/../../home/alice/x.go` is exactly as much an escape from
# the asserted root as an absolute path is, and a segment made entirely of
# dots is never a legitimate package or file name.
#
# EXACT DISPATCH, NOT A GLOB OR A BASENAME. Matching `*.tsv` or a bare
# `baseline.txt` against ANY file with that shape is itself a hole: a sixth
# artifact that happens to end in `.tsv`, or an unrelated file that happens
# to be named `baseline.txt`, would silently inherit a validator built for a
# different file's contract. artifact_shape below matches the full
# REPO-RELATIVE IDENTITY of each of the five known artifacts (as a path
# suffix, so a self-test fixture under a temp directory still matches by
# ending in that same identity) and refuses anything else, including a
# same-named file that lives somewhere else in the tree.
#
# THE DISCOVERY FLOOR IS ON VALIDATED RECORDS, NOT PHYSICAL LINES. A
# header-only or all-comments artifact clears any physical-line floor
# trivially while validating zero path-bearing values -- the exact
# vacuous-success shape #337 named for a missing artifact, reached instead
# through an artifact that exists but says nothing. MIN_RECORDS counts what
# was actually run through the allowlist: comments and blanks skipped for
# the line-oriented formats, the header excluded for the TSV ones.
#
# Usage: check-artifact-paths.sh [file...]
#   (defaults to every known generated artifact, resolved from this script's
#   own location so it works from any invocation directory; a file argument
#   is matched against a known artifact by the REPO-RELATIVE IDENTITY it
#   ends in, which is how the self-test exercises each validator against
#   synthetic fixtures placed under a matching subpath)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
MIN_RECORDS=1

# THE ENUMERATED SET OF GENERATED ARTIFACTS. Add a new one here, AND teach
# artifact_shape below its exact identity, the day it is committed -- that is
# the convention scope item 4 of #403 asks be written down, and this comment
# plus artifact_shape's are that documentation.
ARTIFACTS="test/coverage-baseline.txt test/coverage-baseline-fts.txt test/tools/deadcode/baseline.txt test/tools/ifacesweep/findings/min-tags.tsv test/tools/ifacesweep/findings/full-tags.tsv lockstep-baseline"

if [ "$#" -gt 0 ]; then
	files="$*"
else
	files="$ARTIFACTS"
fi

# SEG -- one path segment that is not composed ENTIRELY of `.` characters, so
# it can never be exactly `.` or `..`. Composed as "optional dots/word chars,
# then at least one character that is not a dot, then optional dots/word
# chars" rather than a negative match, because POSIX ERE (what grep -E and
# awk's `~` both use here) has no negative lookahead to say "not these two
# strings" directly.
SEG='[A-Za-z0-9._-]*[A-Za-z0-9_-][A-Za-z0-9._-]*'

# IMPORT_PATH -- a Go import path rooted at this module, with every segment a
# SEG. Used where the column holds a full package path: test/coverage-baseline.txt's
# first field, the ifacesweep findings' `package` column.
IMPORT_PATH="^github\\.com/jeremyhahn/go-qrdb(/${SEG})*\$"

# SYM -- the `.Name` (or `.Type.Method`) suffix of a SYMBOL, held to the same
# not-all-dots rule as SEG so `..Foo` or a bare `.` cannot stand in for a real
# declaration name. Go identifiers never contain `-`, unlike a path SEG.
SYM='[A-Za-z0-9_.]*[A-Za-z0-9_][A-Za-z0-9_.]*'

# SYMBOL -- an IMPORT_PATH followed by SYM. Used for
# test/tools/deadcode/baseline.txt's `symbol` column, which is an import path
# with the declaration appended.
SYMBOL="^github\\.com/jeremyhahn/go-qrdb(/${SEG})*\\.${SYM}\$"

# REPO_RELATIVE -- a path relative to the repository root, rooted at one of
# its real top-level directories, every segment a SEG. Used where the column
# holds a directory or file location rather than an import path: the
# ifacesweep findings' `file` column, lockstep-baseline's module column, and
# (with the `.` alternative added at its call site)
# test/tools/deadcode/baseline.txt's `module` column, whose root-module rows
# are literally `.`.
REPO_RELATIVE="^(cmd|pkg|sdk|test|scripts)(/${SEG})+\$"

fail=0
total_files=0

# violations <path> <sep> <field> <pattern> -- prints "LINE: VALUE" for every
# non-comment, non-blank line whose $field (1-indexed, split on $sep) does not
# match $pattern, and nothing if every line's field matches. $sep is an awk
# field separator: a single character, or an awk regex like '\t' for tab.
violations() {
	path="$1"
	sep="$2"
	field="$3"
	pattern="$4"
	awk -F "$sep" -v f="$field" -v pat="$pattern" '
		/^[[:space:]]*#/ { next }
		/^[[:space:]]*$/ { next }
		$f !~ pat { print NR": "$f }
	' "$path"
}

# record_count <path> -- the number of non-comment, non-blank lines: what
# violations() above actually ran the allowlist against, for the line-oriented
# formats (coverage-baseline.txt, deadcode's baseline.txt, lockstep-baseline).
record_count() {
	awk '
		/^[[:space:]]*#/ { next }
		/^[[:space:]]*$/ { next }
		{ n++ }
		END { print n + 0 }
	' "$1"
}

# violations_tsv_by_header <path> <column-name> <pattern> -- same as
# violations, but for a header-led TSV: the column is located by NAME in the
# first line (so a reordered or renamed column is still found, or reported
# missing, rather than silently checking the wrong one), and every row after
# the header is checked.
violations_tsv_by_header() {
	path="$1"
	name="$2"
	pattern="$3"
	header=$(head -n 1 -- "$path")
	col=$(printf '%s' "$header" | awk -F'\t' -v name="$name" '{for (i=1;i<=NF;i++) if ($i==name) {print i; exit}}')
	if [ -z "$col" ]; then
		echo "NO SUCH COLUMN: $name"
		return
	fi
	awk -F'\t' -v f="$col" -v pat="$pattern" 'NR==1 { next } $f !~ pat { print NR": "$f }' "$path"
}

# record_count_tsv <path> -- the number of rows after the header: what
# violations_tsv_by_header() above actually ran the allowlist against.
record_count_tsv() {
	awk 'NR==1 { next } { n++ } END { print n + 0 }' "$1"
}

# artifact_shape <path> -- validates the path-bearing column(s) for the
# artifact whose known REPO-RELATIVE IDENTITY $path ends in, per the
# ALLOWLIST commentary above, and requires at least MIN_RECORDS validated
# records. Prints nothing and returns 0 when every value is legitimate and
# the floor is cleared; otherwise prints the violating lines (or the floor
# failure) and returns 1. A path matching none of the five known identities
# is the "unknown artifact" case above: it FAILS rather than silently
# accepting a format it has no shape for, however plausible its name looks.
artifact_shape() {
	path="$1"
	bad=""
	count=0
	case "$path" in
	*/test/coverage-baseline.txt | test/coverage-baseline.txt | \
		*/test/coverage-baseline-fts.txt | test/coverage-baseline-fts.txt)
		# "<import path> <percent>", space-separated. coverage-baseline-fts.txt
		# is the same shape, regenerated by `make coverage-fts-baseline`
		# instead of `make coverage-baseline` (go-qrdb#610).
		count=$(record_count "$path")
		bad=$(violations "$path" ' ' 1 "$IMPORT_PATH")
		;;
	*/test/tools/deadcode/baseline.txt | test/tools/deadcode/baseline.txt)
		# "<module>\t<scope>\t<kind>\t<symbol>". module is `.` for the root
		# module or REPO_RELATIVE for a nested one; symbol is always a SYMBOL.
		count=$(record_count "$path")
		bad=$(violations "$path" '\t' 1 "^\\.\$|$REPO_RELATIVE")
		more=$(violations "$path" '\t' 4 "$SYMBOL")
		if [ -n "$more" ]; then
			bad=$(printf '%s\n%s' "$bad" "$more")
		fi
		;;
	*/test/tools/ifacesweep/findings/min-tags.tsv | test/tools/ifacesweep/findings/min-tags.tsv | \
		*/test/tools/ifacesweep/findings/full-tags.tsv | test/tools/ifacesweep/findings/full-tags.tsv)
		# ifacesweep findings: header-led TSV, `package` (IMPORT_PATH) and
		# `file` (REPO_RELATIVE) columns located by name.
		count=$(record_count_tsv "$path")
		bad=$(violations_tsv_by_header "$path" package "$IMPORT_PATH")
		more=$(violations_tsv_by_header "$path" file "$REPO_RELATIVE")
		if [ -n "$more" ]; then
			bad=$(printf '%s\n%s' "$bad" "$more")
		fi
		;;
	*/lockstep-baseline | lockstep-baseline)
		# "<module> <version>", space-separated; module is REPO_RELATIVE
		# (lockstep-modules.sh scopes discovery to sdk/, but any real
		# top-level directory is accepted here rather than hard-coding that
		# scope a second time).
		count=$(record_count "$path")
		bad=$(violations "$path" ' ' 1 "$REPO_RELATIVE")
		;;
	*)
		echo "NO KNOWN SHAPE for $(basename -- "$path") -- its full path does not end in any of the five known artifact identities. Teach artifact_shape() in check-artifact-paths.sh what a new artifact's path-bearing column looks like before adding it to ARTIFACTS"
		return 1
		;;
	esac
	bad=$(printf '%s' "$bad" | sed '/^$/d')
	if [ "$count" -lt "$MIN_RECORDS" ]; then
		bad=$(printf '%s\n%s validated record(s), below the %s floor -- a header-only, all-comment, or truncated artifact validates nothing and must not pass' "$bad" "$count" "$MIN_RECORDS")
		bad=$(printf '%s' "$bad" | sed '/^$/d')
	fi
	if [ -n "$bad" ]; then
		printf '%s\n' "$bad"
		return 1
	fi
	return 0
}

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

	if ! hits=$(artifact_shape "$path"); then
		echo "FAIL: $rel:" >&2
		printf '%s\n' "$hits" | sed 's/^/  /' >&2
		fail=1
		continue
	fi

	echo "ok  $rel: every path-bearing column matches its allowlist"
done

if [ "$total_files" -eq 0 ]; then
	echo "FAIL: no files given and no default resolved -- this check would have been vacuous" >&2
	exit 1
fi

exit "$fail"
