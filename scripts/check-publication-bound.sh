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
# check-publication-bound.sh — AGENTS.md: "The feature folder is
# publication-bound. Never write an internal address, host name, workstation
# path, agent principal name or internal issue reference into one."
#
# WHY THIS EXISTS
#
# Until now the rule's only enforcement was a reviewer noticing, and the
# measured leak rate says what that is worth. Five violations landed in a
# single afternoon, from an author who had read AGENTS.md, against a review
# process that caught two of them. That is not the reviewer's failing: a
# reviewer sees ONE DIFF, and this violation is a property of the TREE. Three
# of the five reached main and were corrected after the fact; four older ones
# had been sitting in folders nobody touched that day.
#
# It is the same shape as `make vet-tags`: a whole-repo property no per-diff
# check can see. And it fails silently in the worst way — nothing breaks, the
# docs read fine internally, and it surfaces only when the repo publishes to
# GitHub, where a reader hits an issue number and finds either nothing or, far
# worse, an unrelated public issue wearing it.
#
# SCOPE: docs/*/ — files inside a feature folder, recursively, at any depth.
#
# The legacy top-level documents (docs/01-architecture.md, docs/run-modes.md,
# docs/12-configuration.md) are DELIBERATELY out of scope, not missed by a
# sloppy glob. AGENTS.md writes the publication-bound rule about FOLDERS, and
# calls the top-level layout "legacy layouts, debt to migrate, never
# precedent". Measured on this tree at the time of writing, widening to
# docs/** would light up three internal issue references
# (docs/01-architecture.md, docs/run-modes.md — the latter also linking
# ../../issues/62) and ten lines of 10.0.0.x example addresses in
# docs/12-configuration.md. None of those is a feature folder, all are on the
# migration list, and a guard that is red on the day it lands is a guard
# nobody arms. Widen the scope in the change that migrates them, not before.
#
# EVERY FILE in a feature folder is scanned, not just the five canonical
# documents; symlinks are FOLLOWED and binary-classified files are read as
# text. A companion reference living in the folder, a link to a document
# living elsewhere, and a file carrying an incidental NUL byte all publish on
# exactly the same push, and `gitea.internal` is `gitea.internal` whatever
# else the file it sits in contains.
#
# FENCED CODE BLOCKS ARE NOT EXEMPT, and that is a decision, not an oversight.
# The usual reason to exempt them is the bare `#123` pattern, which collides
# with a shell comment in a `curl` example or a config sample — so the first
# false positive teaches everyone to add exclusions. This check does not match
# bare `#123` (see internal-issue-ref below), so it never has that collision,
# and it therefore keeps the stronger property: an internal hostname inside a
# worked example publishes just as loudly as one in prose.
#
# THE PATTERNS ARE DELIBERATELY NARROWER THAN THE RULE. A check looser than
# the property it claims earns exclusions, and an excluded check is not a
# check. What each rule does NOT match is written next to it, and the residue
# is a human's job, not a wider regex's.
#
# THE INVARIANT, because the next person adding a scan needs to know which way
# the default runs before they write `|| true` out of habit:
#
#   THIS SCRIPT EXITS NONZERO UNLESS IT HAS POSITIVELY ESTABLISHED THAT THE
#   TREE IS CLEAN. AN UNKNOWN RESULT IS A FAILURE, NEVER A PASS.
#
# A walk that fails, a grep that cannot answer, a path it cannot represent, a
# link that leaves the checkout, a file it did not open -- every one of those
# is an unknown, and every one exits 1. The accounting further down is what
# turns "did not open" from a silence into a number.
#
# Usage: check-publication-bound.sh [repo-dir]   (defaults to .)
# Exit status: 0 when the feature folders are publishable, 1 otherwise.

set -eu

cd "${1:-.}"

SCOPE=docs

if [ ! -d "$SCOPE" ]; then
	echo "FAIL: no ${SCOPE}/ directory here." >&2
	echo "Run this from the repository root, or pass one as the first argument." >&2
	exit 1
fi

# An internal issue reference, in the qualified forms this fleet writes:
# `go-qrdb#44`, `go-quicbaas#270`, `jeremyhahn/go-truststrap#5`,
# `automatethethings#5`. The repository half must be lowercase, and must not be
# preceded by a `.`.
#
# It deliberately does NOT require a hyphen in the repository name. It did, on
# the reasoning that every repo in this fleet is `go-*` — and that was already
# false when it was written: AGENTS.md itself cites `automatethethings`, the
# company repo, which is hyphenless and would have passed this guard cleanly.
# A fail-open path in the enforcement mechanism is worse than the leak it was
# added to stop, because it also reports `ok`.
#
# NOT MATCHED, on purpose: a bare `#44`. It is genuinely ambiguous in prose —
# a document can use `#1` and `#4` as
# back-references to its own numbered list, and that is not a violation of
# anything. Flagging it would mean rewriting correct prose on the guard's first
# run, which is how a check acquires its first exclusion and its reputation.
# The bare form is a real residue and a real hazard — GitHub auto-links `#44`
# to the PUBLISHED repo's issue 44, a different issue entirely — but resolving
# each instance needs a human reading the sentence, not a pattern.
#
# Three things keep near-misses out, and all three are real text in this tree
# or its neighbourhood. LOWERCASE excludes `PKCS#11`. NOT PRECEDED BY A DOT
# excludes the markdown anchor `api.md#403-and-405-are-different-claims`, whose
# token before the `#` is the file extension `md`. NOT PRECEDED BY A SLASH
# excludes the repo half of an owner-qualified reference, which is what makes
# the second alternative below meaningful: a PUBLIC shorthand like
# `golang/go#12345` is not an internal reference, it resolves for exactly the
# audience these folders are written for, and blocking it would be the check
# over-enforcing its own rule.
#
# The internal namespace is enumerated because it is short and knowable: this
# forge has one org. `jeremyhahn/go-truststrap#5` is as internal as
# `go-truststrap#5` and is rejected the same way. ADD TO IT if the forge gains
# another org -- a second internal namespace would otherwise pass in its
# owner-qualified form, and that is the one gap this shape has.
ISSUE_REF='(^|[^A-Za-z0-9./-])[a-z][a-z0-9-]*#[0-9]+([^0-9A-Za-z-]|$)|(^|[^A-Za-z0-9.-])jeremyhahn/[a-z0-9][a-z0-9-]*#[0-9]+'

# An internal issue LINK: the forge-relative form a markdown link takes when it
# points at this Gitea's own issue pages -- `[..](../../issues/62)`, and the
# root-relative `](/issues/62)`. It is the same violation as `go-qrdb#62` and
# usually sits right beside one, but no `repo#123` pattern can see it: the
# reference is in the link TARGET, not the link text.
#
# docs/run-modes.md:152 carries exactly this shape today. That file is a legacy
# top-level document and out of scope, so this rule is green on the tree -- but
# a feature folder written by the same hand would not have been, and the first
# version of this script would have reported ok.
#
# NOT MATCHED: an absolute https://github.com/<owner>/<repo>/issues/<n> URL. A
# link to a PUBLIC issue on a public forge is not an internal reference, and it
# resolves for the audience these folders are written for.
ISSUE_LINK='((\.\.?/)+|\]\(/)(issues|pulls)/[0-9]+'

# An internal address: an RFC 1918 literal. A published document has no
# business naming one — the forge this repo is developed on answers at a
# 192.168 address, and that address is one paste away from any doc. All three
# private ranges are flagged, 10/8 included: a reader cannot tell a leaked
# 10.0.0.4 from an illustrative one, so the illustrative one belongs in
# RFC 5737's documentation range (192.0.2.0/24) or, better, behind the
# placeholder AGENTS.md already prescribes — db.example.com.
#
# NOT MATCHED: 127.0.0.1, 0.0.0.0 and localhost. A loopback or wildcard bind
# address is a documented configuration value, not somebody's network.
INTERNAL_ADDR='(^|[^0-9A-Za-z.])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3})'

# An internal host name. .internal and .home.arpa are reserved for exactly the
# private use this rule is about, and .lan is the fleet's habit.
#
# NOT MATCHED: .local. mDNS is a legitimate subject a document may discuss by
# name, and this check does not get to decide it may not.
INTERNAL_HOST='[A-Za-z0-9][A-Za-z0-9-]*\.(internal|lan|home\.arpa)([^A-Za-z0-9-]|$)'

# A workstation path. AGENTS.md prescribes the replacements by name:
# <install-dir>, $XDG_CONFIG_HOME/..., $HOME.
WORKSTATION_PATH='/(home|Users)/[A-Za-z0-9._-]+'

# An agent principal: agent-<repo>, for ANY repo, not only go-*.
#
# It was `agent-go-[a-z0-9-]+` on the reasoning that every repo in this fleet
# is go-*, which is the same reasoning that made the issue-reference pattern
# fail open on `automatethethings#5` — and it failed the same way, letting
# `agent-automatethethings` through. An enumerated prefix in an enforcement
# mechanism is a list of the leaks it does not stop.
#
# This does hit ordinary prose: "agent-based", "agent-driven". That is the
# deliberate trade and the costs are not symmetric — a false positive costs one
# word ("agent based"), a false negative publishes a principal name — and it is
# free today: there is not one occurrence of `agent-` anything under docs/*/.
#
# The leading boundary keeps the compound out: `user-agent-string` does not
# match, because the `agent` there is preceded by a hyphen. `User-Agent` does
# not match either, being uppercase.
AGENT_PRINCIPAL='(^|[^a-z0-9-])agent-[a-z0-9]+(-[a-z0-9]+)*'

hits=$(mktemp)
found=$(mktemp)
errs=$(mktemp)
list=$(mktemp)
nlist=$(mktemp)
trap 'rm -f "$hits" "$found" "$errs" "$list" "$nlist"' EXIT INT TERM
: > "$found"

# THE WALK IS DONE ONCE, AND ITS FAILURE IS A FAILURE.
#
# This used to be `find ... -exec grep ... + 2>/dev/null || true` inside the
# scan loop, and that arrangement is fail-open in the enforcement mechanism
# itself: `find -exec ... +` reports 1 both when grep matched nothing and when
# grep could not read a file, `|| true` erases the distinction, `2>/dev/null`
# erases the message, and an empty $hits then reads as "clean". A guard that
# prints ok because it could not look is worse than no guard, because the ok is
# the thing people act on.
#
# So: walk once, fail if the walk fails, and hold the file list for the scans.

# FIRST, WHERE EVERYTHING RESOLVES TO.
#
# The scan walk below uses `find -L`, because a plain `-type f` walk skips
# symlinks and `docs/alpha/link.md -> ../../secret.md` was therefore scanned as
# nothing while it reaches the folder as its target's contents.
#
# But dereferencing is only correct INSIDE the repository. A path that resolves
# out of the checkout has no target that publishes -- what ships is the link
# value, not somebody's `/etc/passwd` -- so scanning it would make this check's
# verdict depend on runner-local state and could echo host file contents into a
# CI log. It is also, on its own terms, a workstation path written into a
# feature folder: precisely the class this rule forbids.
#
# So EVERY entry the walk can see is resolved and checked first, and one that
# escapes is refused by name. Checking entries rather than symlinks is what
# makes this complete: an earlier version inspected only symlinks at depth 2
# and missed `docs/alpha -> /tmp/outside`, where the FEATURE FOLDER itself is
# the link, and would have missed a link nested inside a symlinked directory
# too. Resolving what will actually be read has no such corners.
root=$(pwd -P)
if ! find -L "$SCOPE" -mindepth 1 -print0 > "$list" 2> "$errs"; then
	echo "FAIL: could not walk ${SCOPE}/." >&2
	sed 's/^/  /' "$errs" >&2
	echo "A tree this check could not read is not a tree it may call clean." >&2
	exit 1
fi
assert_no_newline_in_paths() { # assert_no_newline_in_paths <nul-delimited-file>
	# The scan splits its list on newlines, so a path CONTAINING one would
	# become two unreadable paths -- an unscanned file reported as clean.
	# Counting NUL terminators against newline-converted lines detects that
	# exactly, which a `-print` walk could not have done. Spaces, tabs,
	# brackets and backslashes are all fine and are scanned normally.
	if [ "$(tr -dc '\000' < "$1" | wc -c)" -ne "$(tr '\000' '\n' < "$1" | wc -l)" ]; then
		echo "FAIL: a path under ${SCOPE}/ contains a newline." >&2
		echo "Rename it. This check cannot scan it safely and will not" >&2
		echo "pretend to." >&2
		exit 1
	fi
}
assert_no_newline_in_paths "$list"
tr '\000' '\n' < "$list" > "$nlist"
escaped=0
while IFS= read -r entry; do
	[ -n "$entry" ] || continue
	target=$(readlink -f -- "$entry" 2>/dev/null) || target=""
	case "$target" in
	"$root" | "$root"/*) continue ;;
	esac
	if [ "$escaped" -eq 0 ]; then
		echo "FAIL: a path under ${SCOPE}/ resolves outside the repository:" >&2
	fi
	escaped=$((escaped + 1))
	echo "  ${entry} -> ${target:-<unresolvable>}" >&2
done < "$nlist"
if [ "$escaped" -ne 0 ]; then
	echo "What publishes there is the link, not its target, and following it" >&2
	echo "would make this check's verdict depend on the machine it ran on." >&2
	echo "Point it at a file in the repository, or remove it." >&2
	exit 1
fi

# NOW the files to scan, following symlinks, none of which can leave the
# checkout. A symlinked DIRECTORY is traversed rather than handed to grep as a
# directory, and a filesystem loop makes find exit nonzero, which lands in the
# failure branch with find's own message: an incomplete walk is not a clean
# tree.
#
# `-print0`, not `-print`: it is the only walk output that can represent every
# filename, which is what makes the newline assertion above an assertion rather
# than a guess.
if ! find -L "$SCOPE" -mindepth 2 -type f -print0 > "$list" 2> "$errs"; then
	echo "FAIL: could not walk ${SCOPE}/*/." >&2
	sed 's/^/  /' "$errs" >&2
	echo "A tree this check could not read is not a tree it may call clean." >&2
	exit 1
fi

if [ ! -s "$list" ]; then
	echo "FAIL: no files found under ${SCOPE}/*/." >&2
	echo "Scanning nothing and reporting ok is the failure mode this check is" >&2
	echo "supposed to be, not the one it has." >&2
	exit 1
fi

assert_no_newline_in_paths "$list"
files=$(tr '\000' '\n' < "$list")

# PATHNAME EXPANSION OFF, WORD SPLITTING ON NEWLINES ONLY.
#
# $files is deliberately unquoted at the grep below, which is the only way to
# hand a shell-built list to one grep invocation. Left at the defaults that is
# fail-open twice over: the default IFS splits on spaces and tabs, and the
# shell then GLOBS each word -- a violating `docs/alpha/[a].md` sitting beside
# `docs/alpha/a.md` expands to `a.md`, the violating file is never given to
# grep, and the script prints ok. `set -f` removes the expansion and IFS
# removes the splitting, so every path reaches grep verbatim.
set -f
IFS='
'


# EVERY rule is run over EVERY file, and the failures are reported together.
# Stopping at the first match would report one line of a tree-wide property and
# invite a fix-one-rerun loop; the whole point of scanning the tree is to see
# all of it at once.
scan() { # scan <rule> <regex> <remedy>
	rule="$1"
	regex="$2"
	remedy="$3"
	rc=0
	# Unquoted on purpose: the list is newline-separated and proven free of
	# whitespace above. grep's statuses are distinguished rather than collapsed
	# -- 0 matched, 1 clean, anything else is an operational failure.
	#
	# `-a`, NOT `-I`. `-I` treats a binary-classified file as having no matches,
	# silently: a document carrying an incidental NUL byte, or a genuine binary
	# asset, was skipped while the folder's claim was that every file in it is
	# scanned. And a prohibited string inside such a file publishes exactly like
	# one in prose -- `gitea.internal` is `gitea.internal` whatever else the file
	# contains -- so skipping it is a fail-open path with no error to reveal it.
	# shellcheck disable=SC2086
	grep -aHnE -e "$regex" -- $files > "$hits" 2> "$errs" || rc=$?
	if [ "$rc" -gt 1 ]; then
		echo "FAIL: grep exited ${rc} scanning for ${rule}." >&2
		sed 's/^/  /' "$errs" >&2
		echo "The scan did not complete, so this tree has not been checked." >&2
		exit 1
	fi
	[ -s "$hits" ] || return 0
	echo ""
	echo "VIOLATION: ${rule}"
	echo "  ${remedy}"
	# CONTROL characters are replaced before display, and only those. `-a` means
	# a hit may come from a file that is mostly not text, and a CI log is not
	# improved by the rest of that line arriving raw -- but `tr -c '[:print:]'`
	# was too wide: in the C locale it mangles every multi-byte character, so an
	# em dash in an ordinary markdown line came back as `???` and the check's own
	# output stopped being quotable. The path and line number are unaffected
	# either way; this keeps the sentence readable too.
	LC_ALL=C tr '\000-\010\013\014\016-\037\177' '?' < "$hits" | sed 's/^/    /' 
	wc -l < "$hits" >> "$found"
}

# ACCOUNTING: PROVE THE SCAN READ EVERY FILE IT ENUMERATED.
#
# This check has had a long run of defects with ONE sentence in common: the
# scan did not reach some content, and reported clean. Six different mechanisms
# produced it -- `|| true` collapsing grep's exit 2, `-type f` skipping
# symlinks, `-I` skipping a file with a NUL byte, a glob metacharacter
# expanding a path away, a walk that read outside the checkout, an enumeration
# that stopped one directory level too shallow. Not one was a wrong pattern.
#
# Predicting the seventh is not a strategy. So the scan is made to ACCOUNT for
# itself instead: grep is asked to report one line per file it actually opened,
# and that number must equal the number enumerated. A file that is skipped for
# any reason -- including a reason nobody here has thought of -- becomes a
# count mismatch and a failure, rather than a silence that reads as clean.
#
# `grep -ac ''` prints `path:<lines>` for every file it reads and nothing for a
# file it cannot. It is one extra pass, and it is the only claim in this script
# that is checked rather than assumed.
scanned=0
grep -ac '' -- $files > "$hits" 2> "$errs" || true
scanned=$(wc -l < "$hits")
enumerated=$(printf '%s\n' "$files" | wc -l)
if [ "$scanned" -ne "$enumerated" ]; then
	echo "FAIL: read ${scanned} of ${enumerated} files under ${SCOPE}/*/." >&2
	sed 's/^/  /' "$errs" >&2
	echo "The files enumerated but not read are:" >&2
	sed 's/:[0-9]*$//' "$hits" | sort > "$nlist.read"
	printf '%s\n' "$files" | sort | comm -23 - "$nlist.read" | sed 's/^/  /' >&2
	rm -f "$nlist.read"
	echo "A file this check did not read is not a file it may call clean." >&2
	exit 1
fi

echo "checking ${scanned} files in feature folders under ${SCOPE}/*/ for publication-bound violations"

scan internal-issue-ref "$ISSUE_REF" \
	"Keep the measurement, drop the tracker reference: a reader outside this forge cannot resolve it, while the fact it is attached to is exactly what they need. Where the reference is load-bearing, say what the issue established."
scan internal-issue-link "$ISSUE_LINK" \
	"A forge-relative link resolves only inside this Gitea. Name what the issue established, or link something a public reader can open."
scan internal-address "$INTERNAL_ADDR" \
	"Use db.example.com, or RFC 5737's 192.0.2.0/24 if a literal is unavoidable."
scan internal-host "$INTERNAL_HOST" \
	"Use db.example.com."
scan workstation-path "$WORKSTATION_PATH" \
	"Use <install-dir>, \$XDG_CONFIG_HOME/... or \$HOME."
scan agent-principal "$AGENT_PRINCIPAL" \
	"Name the role, not the principal."

total=0
while read -r n; do
	total=$((total + n))
done < "$found"

if [ "$total" -ne 0 ]; then
	echo ""
	echo "FAIL: ${total} publication-bound violation(s) in feature folders." >&2
	echo "AGENTS.md: the feature folder ships to GitHub when the repo publishes." >&2
	echo "Nothing breaks until it does, which is why this is checked rather than" >&2
	echo "reviewed: the violation is a property of the tree, and a reviewer sees" >&2
	echo "one diff." >&2
	exit 1
fi

# The count is printed on SUCCESS as well as failure. A bare `ok` cannot
# distinguish "I examined everything and found nothing" from "I examined some
# of it and found nothing", and that indistinguishability is what let most of
# this check's own defects survive review.
echo "ok  scanned ${scanned} of ${enumerated} files under ${SCOPE}/*/; all publishable"
