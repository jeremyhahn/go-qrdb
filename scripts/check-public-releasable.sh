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
# check-public-releasable.sh — no TRACKED file names the private development
# fleet.
#
# WHY THIS EXISTS, AND WHY IT IS NOT check-publication-bound.sh
#
# Everything git tracks here publishes: the module zip a consumer's `go get`
# downloads is the tracked tree, and the public mirror is a squash commit whose
# tree is byte-identical to the internal release tag. Anything that must not
# appear publicly is therefore scrubbed at the source, before tagging --
# rewriting at publication time would break content identity.
#
# check-publication-bound.sh guards the FEATURE FOLDERS, with a wide rule set
# (every RFC 1918 literal, every repo#N reference, every agent principal) that
# is right for prose written for an outside reader and wrong for a source tree:
# tests across this repository use 192.168.1.x and 10.0.0.x as opaque
# addresses that are never dialed, and code comments cite the tracker. Widening
# that script to the whole tree would bury the real findings under hundreds of
# correct lines, and a guard that is red on the day it lands is a guard nobody
# arms. This one is the other half: a NARROW rule set over the WHOLE tree.
#
# THE RULE: no tracked file carries a value that identifies the private fleet.
# Three content classes, each matched on the fleet's OWN values rather than on a
# shape:
#
#   lab-address   an address on one of the fleet's own networks. NOT RFC 1918
#                 at large -- an opaque 192.168.1.10 in a test names nobody's
#                 network. Use RFC 5737 (192.0.2.0/24, 198.51.100.0/24,
#                 203.0.113.0/24) or a name under example.com.
#   lab-host      a fleet machine's own hostname. Name the role instead:
#                 gpu-host.example.com, ci-runner, db.example.com.
#   owner-path    a path under the owner's home directory or on the lab's
#                 storage. NOT /home/<anyone> -- /home/user and /home/alice are
#                 placeholders. Use a repository-relative path, $HOME,
#                 $XDG_CONFIG_HOME/... or <install-dir>.
#
# And one class matched on the tracked PATH rather than its content, which is
# generic and therefore lives here:
#
#   internal-artefact   per-machine agent and editor state (.serena/, .claude/)
#                       and a local credentials file (any `.env` or
#                       `.env.<suffix>`). .gitignore already lists them, and
#                       they were tracked anyway -- `git add -f`, or added
#                       before the ignore line existed -- which is exactly the
#                       case an ignore rule cannot see. A documented template
#                       such as .env.example is not one.
#
# THE FLEET'S VALUES ARE NOT IN THIS REPOSITORY (owner ruling 2026-10-01). A
# rule set that names the fleet's networks, hosts and paths discloses exactly
# what the rule exists to keep private, so the content patterns are loaded at
# run time from an UNTRACKED pattern file, found in this order:
#
#   1. $PUBLIC_RELEASE_PATTERNS_FILE, when set (CI writes the pattern secret to
#      a temp file and points this at it);
#   2. ../handbook/software/conventions/public-release-patterns, in the
#      handbook checkout beside this repository;
#   3. ${XDG_CONFIG_HOME:-$HOME/.config}/go-qrdb/public-release-patterns.
#
# No source is a FAILURE that names all three, never a pass: a scan with no
# rules checks nothing. The file is line-oriented; blank lines and `#` lines
# are ignored, and every other line is
#
#   <class>[/i] <tab-or-spaces> <POSIX ERE>
#
# where <class> is lab-address, lab-host or owner-path and `/i` matches
# case-insensitively. Every class must have at least one rule, and every rule
# must compile; anything else fails closed. Write each ERE so it cannot match
# its own text (a one-letter bracket class does it) -- the file is never
# scanned, but a copy pasted into a tracked note would be.
#
# NOT A VIOLATION, and deliberately not matched:
#
#   - A reference to an internal tracker issue in a code comment (`go-qrdb#123`,
#     `#123`). It names no address, host or path, and the feature folders --
#     the documents written for an outside reader -- are held to the stricter
#     rule by check-publication-bound.sh. The go-quicbaas gate this mirrors
#     draws the same line.
#   - The `.internal` TLD and the product domains, which the owner ruled are
#     published deliberately (handbook feature-docs.md section 10).
#
# ACCEPTING A LINE: an inline waiver on the offending line, in the file's own
# comment syntax, WITH a reason:
#
#     measured on <host> at 2026-09-12   # public-release-ok: benchmark provenance
#
# `public-release-ok:` with nothing after it is not a waiver and does not
# suppress anything. There is no path allowlist: a waiver sits next to the
# value it explains, where the next reader sees both.
#
# WHAT IS SCANNED: every path in the git index, read from the working tree --
# what a commit of this checkout would publish, including a tracked file you
# have edited and not yet staged. An untracked file does not publish and is
# not scanned. A tracked SYMLINK publishes as its link value, which git grep
# does not read, so link values are scanned separately.
#
# THE INVARIANT, shared with every guard in scripts/:
#
#   THIS SCRIPT EXITS NONZERO UNLESS IT HAS POSITIVELY ESTABLISHED THAT THE
#   TREE IS CLEAN. AN UNKNOWN RESULT IS A FAILURE, NEVER A PASS.
#
# git failing, a grep that cannot answer, a path it cannot represent, and a
# tracked file it did not read are each an unknown, and each exits 1. The
# accounting near the end is what turns "did not read" into a number.
#
# Usage: check-public-releasable.sh [repo-dir]   (defaults to .)
# Exit status: 0 when the tracked tree is publicly releasable, 1 otherwise.

set -eu

cd "${1:-.}"

if ! top=$(git rev-parse --show-toplevel 2>/dev/null); then
	echo "FAIL: $(pwd) is not inside a git work tree." >&2
	echo "This check scans what git tracks; with no index there is nothing it" >&2
	echo "may call clean." >&2
	exit 1
fi
cd "$top"

# LOAD THE CONTENT PATTERNS, before anything is enumerated, and fail closed.
HANDBOOK_PATTERNS="$top/../handbook/software/conventions/public-release-patterns"
XDG_PATTERNS="${XDG_CONFIG_HOME:-${HOME:-/nonexistent}/.config}/go-qrdb/public-release-patterns"
if [ -n "${PUBLIC_RELEASE_PATTERNS_FILE:-}" ]; then
	patterns_file="$PUBLIC_RELEASE_PATTERNS_FILE"
	patterns_origin="PUBLIC_RELEASE_PATTERNS_FILE"
elif [ -f "$HANDBOOK_PATTERNS" ]; then
	patterns_file="$HANDBOOK_PATTERNS"
	patterns_origin="the handbook checkout"
else
	patterns_file="$XDG_PATTERNS"
	patterns_origin="XDG_CONFIG_HOME"
fi
if [ ! -f "$patterns_file" ] || [ ! -r "$patterns_file" ]; then
	echo "FAIL: no fleet pattern file. This gate loads the private fleet's values at" >&2
	echo "run time and none of these is a readable file:" >&2
	echo "  \$PUBLIC_RELEASE_PATTERNS_FILE  ${PUBLIC_RELEASE_PATTERNS_FILE:-(unset)}" >&2
	echo "  handbook checkout             $HANDBOOK_PATTERNS" >&2
	echo "  XDG config                    $XDG_PATTERNS" >&2
	echo "A scan with no rules checks nothing, so this is a failure, not a pass." >&2
	echo "docs/release/user.md says where the file comes from." >&2
	exit 1
fi

pattern_dir=$(mktemp -d)
trap 'rm -rf "$pattern_dir"' EXIT INT TERM
for c in lab-address lab-host owner-path; do
	: > "$pattern_dir/$c"
	: > "$pattern_dir/$c.i"
done
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
	lineno=$((lineno + 1))
	case "$line" in
	'' | '#'*) continue ;;
	esac
	key=${line%%[[:space:]]*}
	regex=$(printf '%s' "${line#"$key"}" | sed 's/^[[:space:]]*//')
	case "$key" in
	lab-address | lab-host | owner-path) dest="$pattern_dir/$key" ;;
	lab-address/i | lab-host/i | owner-path/i) dest="$pattern_dir/${key%/i}.i" ;;
	*)
		echo "FAIL: ${patterns_file}:${lineno}: unknown class '${key}'; want lab-address, lab-host or owner-path, optionally with /i." >&2
		exit 1
		;;
	esac
	if [ -z "$regex" ]; then
		echo "FAIL: ${patterns_file}:${lineno}: class '${key}' has no pattern." >&2
		exit 1
	fi
	rc=0
	printf '' | grep -E -e "$regex" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -gt 1 ]; then
		echo "FAIL: ${patterns_file}:${lineno}: the pattern does not compile as a POSIX ERE." >&2
		exit 1
	fi
	printf '%s\n' "$regex" >> "$dest"
done < "$patterns_file"
pattern_count=0
for c in lab-address lab-host owner-path; do
	n=$(cat "$pattern_dir/$c" "$pattern_dir/$c.i" | wc -l)
	if [ "$n" -eq 0 ]; then
		echo "FAIL: ${patterns_file} declares no ${c} rule. A class with no rules is a" >&2
		echo "class this gate cannot check, so the file is refused as incomplete." >&2
		exit 1
	fi
	pattern_count=$((pattern_count + n))
done

# Tracked paths that are per-machine state or local credentials: agent and
# editor state, and any `.env` or `.env.<suffix>`. The one exception, a
# documented template, is ENV_TEMPLATE.
INTERNAL_ARTEFACT='^\.serena/|^\.claude/|(^|/)\.env(\..+)?$'
ENV_TEMPLATE='(^|/)\.env\.example$'

# A waiver: the marker, a colon, and at least one non-blank character.
WAIVER='public-release-ok:[[:space:]]*[^[:space:]]'

list="$pattern_dir/list"
hits="$pattern_dir/hits"
found="$pattern_dir/found"
errs="$pattern_dir/errs"
links="$pattern_dir/links"
: > "$found"
: > "$links"

# ENUMERATE ONCE, NUL-delimited, and fail if git cannot.
if ! git ls-files -z > "$list" 2> "$errs"; then
	echo "FAIL: git ls-files failed." >&2
	sed 's/^/  /' "$errs" >&2
	exit 1
fi

# A path containing a newline cannot be accounted for below, where every
# per-file count is one line. Refuse it rather than guess.
if [ "$(tr -dc '\000' < "$list" | wc -c)" -ne "$(tr '\000' '\n' < "$list" | wc -l)" ]; then
	echo "FAIL: a tracked path contains a newline. Rename it; this check cannot" >&2
	echo "account for it and will not pretend to." >&2
	exit 1
fi

enumerated=$(tr -dc '\000' < "$list" | wc -c)

# THE DISCOVERY FLOOR. Every other assertion here is about what the scan found,
# and an empty or truncated enumeration finds nothing. This script is itself a
# tracked file, so a listing that does not contain it did not list the tree.
if ! tr '\000' '\n' < "$list" | grep -qx 'scripts/check-public-releasable.sh'; then
	echo "FAIL: the tracked-file listing does not contain this script" >&2
	echo "(scripts/check-public-releasable.sh), so it is not a listing of this" >&2
	echo "repository. ${enumerated} path(s) were listed." >&2
	exit 1
fi

# CLASSIFY every tracked path: a symlink publishes as its link value; a regular
# file with content is what git grep must read; an empty file has nothing to
# match; a path missing from the working tree is a pending deletion.
#
# The loop runs under xargs -0 so every path arrives verbatim, whatever it
# contains. Its output is one tag per path plus the link values.
# shellcheck disable=SC2016 # the inner script expands its own variables
counts=$(xargs -0 sh -c '
	for f do
		if [ -L "$f" ]; then
			printf "L\n"
			printf "%s:0:%s\n" "$f" "$(readlink -- "$f")" >> "$0"
		elif [ -f "$f" ] && [ -s "$f" ]; then
			printf "R\n"
		elif [ -f "$f" ]; then
			printf "E\n"
		elif [ ! -e "$f" ]; then
			printf "M\n"
		else
			printf "X %s\n" "$f"
		fi
	done
' "$links" < "$list") || {
	echo "FAIL: could not classify the tracked paths." >&2
	exit 1
}
regular=$(printf '%s\n' "$counts" | grep -c '^R$' || true)
symlinks=$(printf '%s\n' "$counts" | grep -c '^L$' || true)
empty=$(printf '%s\n' "$counts" | grep -c '^E$' || true)
missing=$(printf '%s\n' "$counts" | grep -c '^M$' || true)
if printf '%s\n' "$counts" | grep -q '^X '; then
	echo "FAIL: tracked path(s) that are neither a file nor a symlink:" >&2
	printf '%s\n' "$counts" | sed -n 's/^X /  /p' >&2
	exit 1
fi

# ACCOUNTING: git grep must have READ every regular, non-empty tracked file.
# `-c ''` prints one line per file it read, and a file it skipped -- for any
# reason, including one nobody here has thought of -- becomes a mismatch rather
# than a silence that reads as clean. `-a` because a binary-classified file
# publishes a fleet value exactly as loudly as prose does.
rc=0
git -c core.quotePath=false grep --no-color -a -c -e '' > "$hits" 2> "$errs" || rc=$?
if [ "$rc" -gt 1 ]; then
	echo "FAIL: git grep exited ${rc} while counting the tracked files." >&2
	sed 's/^/  /' "$errs" >&2
	exit 1
fi
read_count=$(wc -l < "$hits")
if [ "$read_count" -ne "$regular" ]; then
	echo "FAIL: git grep read ${read_count} tracked file(s); ${regular} regular," >&2
	echo "non-empty tracked file(s) exist. A file this check did not read is not" >&2
	echo "a file it may call clean." >&2
	exit 1
fi

# EVERY rule over EVERY file, failures reported together.
scan() { # scan <class> <remedy>
	class="$1"
	remedy="$2"
	: > "$hits"
	for variant in "" ".i"; do
		file="$pattern_dir/$class$variant"
		[ -s "$file" ] || continue
		flag=''
		[ "$variant" = ".i" ] && flag='-i'
		rc=0
		# shellcheck disable=SC2086
		git -c core.quotePath=false grep --no-color -a -n -E $flag -f "$file" >> "$hits" 2> "$errs" || rc=$?
		if [ "$rc" -gt 1 ]; then
			echo "FAIL: git grep exited ${rc} scanning for ${class}." >&2
			sed 's/^/  /' "$errs" >&2
			exit 1
		fi
		# The symlink values, in the same path:line:text shape.
		rc=0
		# shellcheck disable=SC2086
		grep -a -E $flag -f "$file" -- "$links" >> "$hits" 2> "$errs" || rc=$?
		if [ "$rc" -gt 1 ]; then
			echo "FAIL: grep exited ${rc} scanning symlink values for ${class}." >&2
			sed 's/^/  /' "$errs" >&2
			exit 1
		fi
	done
	# A line both rule variants match is one violation, not two.
	sort -u "$hits" -o "$hits"
	# Waived lines drop out here. `|| true`: grep -v exits 1 when EVERY hit
	# was waived, which is the clean outcome, not an error.
	grep -a -v -E -e "$WAIVER" "$hits" > "$hits.kept" || true
	mv "$hits.kept" "$hits"
	[ -s "$hits" ] || return 0
	echo ""
	echo "VIOLATION: ${class}"
	echo "  ${remedy}"
	LC_ALL=C tr '\000-\010\013\014\016-\037\177' '?' < "$hits" | sed 's/^/    /'
	wc -l < "$hits" >> "$found"
}

echo "checking ${enumerated} tracked paths (${regular} files read, ${symlinks} symlink values, ${empty} empty, ${missing} deleted in the working tree) against ${pattern_count} fleet rule(s) from ${patterns_origin}"

scan lab-address \
	"Use an RFC 5737 documentation address (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24) or a name under example.com. A value that must stay configurable belongs in the environment or a gitignored local file."
scan lab-host \
	"Name the role, not the machine: gpu-host.example.com, ci-runner, db.example.com."
scan owner-path \
	"Use a repository-relative path, \$HOME, \$XDG_CONFIG_HOME/... or <install-dir>."

# PATHS, not content. The listing is proven newline-free above, so one path per
# line is exact.
tr '\000' '\n' < "$list" | grep -E -e "$INTERNAL_ARTEFACT" | grep -v -E -e "$ENV_TEMPLATE" > "$hits" || true
if [ -s "$hits" ]; then
	echo ""
	echo "VIOLATION: internal-artefact"
	echo "  Untrack it with 'git rm --cached <path>'; .gitignore already keeps it out of the next commit."
	sed 's/^/    /' "$hits"
	wc -l < "$hits" >> "$found"
fi

total=0
while read -r n; do
	total=$((total + n))
done < "$found"

if [ "$total" -ne 0 ]; then
	echo ""
	echo "FAIL: ${total} line(s) name the private fleet in tracked files." >&2
	echo "Every tracked file publishes. Scrub the value, or waive the line with" >&2
	echo "'public-release-ok: <reason>' when naming it is the line's meaning." >&2
	exit 1
fi

echo "ok  read ${read_count} of ${regular} tracked files and ${symlinks} symlink value(s); publicly releasable"
