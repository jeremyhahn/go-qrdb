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
# Three classes, each matched on the fleet's own values rather than on a shape:
#
#   lab-address   an address on one of the fleet's own subnets. NOT 192.168/16
#                 at large -- an opaque 192.168.1.10 in a test names nobody's
#                 network. Use RFC 5737 (192.0.2.0/24, 198.51.100.0/24,
#                 203.0.113.0/24) or a name under example.com.
#   lab-host      a fleet machine's own hostname. Name the role instead:
#                 gpu-host.example.com, ci-runner, db.example.com.
#   owner-path    a path under the owner's home directory or the lab's shared
#                 mount. NOT /home/<anyone> -- /home/user and /home/alice are
#                 placeholders. Use a repository-relative path, $HOME,
#                 $XDG_CONFIG_HOME/... or <install-dir>.
#
# And one class matched on the tracked PATH rather than its content:
#
#   internal-artefact   per-machine agent and editor state (.serena/, .claude/)
#                       and a local credentials file (any `.env` or
#                       `.env.<suffix>`). .gitignore
#                       already lists them, and they were tracked anyway --
#                       `git add -f`, or added before the ignore line existed --
#                       which is exactly the case an ignore rule cannot see. A
#                       documented template such as .env.example is not one.
#
# The fleet's values are written down in exactly one place in this repository:
# the patterns below. They are spelled so that no pattern matches its own
# source text (a one-letter bracket class, `godlik[e]`), which is what lets
# this script scan itself like any other tracked file instead of carrying a
# waiver for its own rule set.
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

# THE PATTERNS ARE BUILT FROM FRAGMENTS, one fleet fact per line, so adding a
# subnet or a machine is one line and a reader can check each against the
# fleet inventory (the handbook's infrastructure document) without parsing a
# regex. Where a literal fragment would match its own source text, one letter
# is written as a bracket class (`godlik[e]`).

# The fleet's subnets. The leading class stops a longer dotted quad such as
# 10.192.168.50.x from half-matching; the trailing class stops a /30's four
# addresses from matching a longer final octet (10.254.0.30).
NET_OCTET='[0-9]{1,3}'
NET_192='192\.168\.(50|101|102|103|104|105|110|111|112|113|114|200)\.'"$NET_OCTET"  # office, management, service VLANs, retired platform range
NET_172='172\.16\.200\.'"$NET_OCTET"                                            # the platform service range
NET_P2P='10\.254\.0\.[0-3]'                                                     # the router point-to-point /30
NET_LOOP='10\.255\.0\.'"$NET_OCTET"                                             # the router loopback range
LAB_ADDRESS='(^|[^0-9.])('"$NET_192"'|'"$NET_172"'|'"$NET_P2P"'|'"$NET_LOOP"')([^0-9]|$)'

# The fleet's machines, matched case-insensitively on boundaries built from
# explicit classes, so a longer word that merely starts with a fleet name
# (`mini-010`) does not match. check-public-releasable_test.sh holds the
# samples, assembled at run time so that file stays clean too.
#
# Numbered machines: <role>-NN.
HOST_NUMBERED='(vstation|dellemc|corertr|coreswt|rack[0-9]swt|houseswt|officeswt|fw|vpn|nat64|raritan|eaton|mini)-[0-9]{2}'
# A name no other text uses: matched wherever it stands as a word.
HOST_UNIQUE='godlik[e]'
# Names that are also ordinary words or identifiers (the Greek letter, a
# network interface): matched only in a HOST-LIKE context -- a service suffix
# (`-claude`), a domain (`.internal`), a port, or after `@` or `//`.
HOST_AMBIGUOUS='(omeg[a]|mon[0])'
HOST_CONTEXT_AFTER='(-[a-z0-9]+|\.[a-z][a-z0-9-]*|:[0-9]+)'
LAB_HOST='(^|[^a-z0-9-])('"$HOST_NUMBERED"'|'"$HOST_UNIQUE"'|'"$HOST_AMBIGUOUS$HOST_CONTEXT_AFTER"')([^a-z0-9-]|$)|(@|//)'"$HOST_AMBIGUOUS"'([^a-z0-9-]|$)'

# The owner's home directory on either OS, and the lab's storage and service
# paths: the shared mount, the fileserver's raid and data disks, and the forge's
# install root.
PATH_HOME='/(home|Users)/jhah[n]([^A-Za-z0-9._-]|$)'
PATH_LAB='/mnt/vstatio[n]|/mnt/rai[d]1|/mnt/dis[k]|/opt/gite[a]'
OWNER_PATH="$PATH_HOME|$PATH_LAB"

# Tracked paths that are per-machine state or local credentials: agent and
# editor state, and any `.env` or `.env.<suffix>`. The one exception, a
# documented template, is ENV_TEMPLATE.
INTERNAL_ARTEFACT='^\.serena/|^\.claude/|(^|/)\.env(\..+)?$'
ENV_TEMPLATE='(^|/)\.env\.example$'

# A waiver: the marker, a colon, and at least one non-blank character.
WAIVER='public-release-ok:[[:space:]]*[^[:space:]]'

list=$(mktemp)
hits=$(mktemp)
found=$(mktemp)
errs=$(mktemp)
links=$(mktemp)
trap 'rm -f "$list" "$hits" "$found" "$errs" "$links"' EXIT INT TERM
: > "$found"

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
scan() { # scan <class> <regex> <remedy> [grep flag]
	class="$1"
	regex="$2"
	remedy="$3"
	flag="${4:-}"
	rc=0
	# shellcheck disable=SC2086
	git -c core.quotePath=false grep --no-color -a -n -E $flag -e "$regex" > "$hits" 2> "$errs" || rc=$?
	if [ "$rc" -gt 1 ]; then
		echo "FAIL: git grep exited ${rc} scanning for ${class}." >&2
		sed 's/^/  /' "$errs" >&2
		exit 1
	fi
	# The symlink values, in the same path:line:text shape.
	rc=0
	# shellcheck disable=SC2086
	grep -a -E $flag -e "$regex" -- "$links" >> "$hits" 2> "$errs" || rc=$?
	if [ "$rc" -gt 1 ]; then
		echo "FAIL: grep exited ${rc} scanning symlink values for ${class}." >&2
		sed 's/^/  /' "$errs" >&2
		exit 1
	fi
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

echo "checking ${enumerated} tracked paths (${regular} files read, ${symlinks} symlink values, ${empty} empty, ${missing} deleted in the working tree) for fleet values"

scan lab-address "$LAB_ADDRESS" \
	"Use an RFC 5737 documentation address (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24) or a name under example.com. A value that must stay configurable belongs in the environment or a gitignored local file."
scan lab-host "$LAB_HOST" \
	"Name the role, not the machine: gpu-host.example.com, ci-runner, db.example.com." -i
scan owner-path "$OWNER_PATH" \
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
