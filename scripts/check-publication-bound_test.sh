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
# Regression tests for check-publication-bound.sh.
#
# This repository's own feature folders are clean, so running the check here
# only ever exercises its ACCEPTING branch. Every rejecting branch — the entire
# reason it exists — would otherwise ship untested. Same reasoning as
# test-require-tool and test-tag-lockstep.
#
# TWO PROPERTIES ARE UNDER TEST, and each has a way of failing silently:
#
#   1. COVERAGE, WITHOUT ASSUMING A WALK ORDER. A scanner that stopped at the
#      first file, or the first rule, would still pass a naive test whose only
#      planted violation sat in the file it happened to open first. Planting in
#      "the last folder" does not fix that: neither POSIX nor GNU find
#      guarantees traversal order, so on another filesystem the walk could
#      reach that folder first and a broken scanner would pass anyway.
#
#      So the coverage proof enumerates the fixture's files and plants the SAME
#      violation in EACH of them in turn, one fresh tree per file, asserting
#      rejection every time. Whatever order the walk uses, every position in it
#      has been the only violating file. The multi-plant case then proves the
#      other half — that a scan does not stop at its first HIT — by asserting
#      all three planted paths are named in one run.
#
#   2. FAIL-CLOSED ON ITS OWN FAILURES. A guard that prints ok because it could
#      not look is worse than no guard, because the ok is the thing people act
#      on. There are cases below for a grep that exits 2, a grep that skips a
#      file silently, either walk failing, a scope that holds no files, and a
#      path the scan cannot represent.
#
#      NONE OF THEM DEPEND ON WHO RUNS THEM. The traversal case was once a
#      `chmod 000` directory; it passed locally and failed in CI, where the job
#      runs as root and root reads a mode-000 directory perfectly well. A test
#      whose premise is a permission check means something different depending
#      on the uid, so every failure here is induced by a shim on PATH, which
#      states the branch under test directly.
#
#   3. PRECISION. A check looser than the property it claims earns an
#      exclusion on its first false positive, and an excluded check is not a
#      check. The near-misses below are text that is in this repository today
#      and is CORRECT: `PKCS#11`, the markdown anchor
#      `api.md#403-and-405-are-different-claims`, `#4` as a back-reference to a
#      numbered list, a `#42` inside a fenced shell comment, `127.0.0.1`,
#      `db.example.com`, a relative link to a FILE, and a public
#      github.com issue URL. `--mutate` re-loosens the
#      issue-reference match to the naive `#[0-9]+` in a COPY of the script and
#      asserts those cases go red, because a suite that passes both with and
#      without the narrowing proves nothing about it.
#
# Usage: check-publication-bound_test.sh [--mutate]   (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subject="${SCRIPT_DIR}/check-publication-bound.sh"
mutate=""
[ "${1:-}" = "--mutate" ] && mutate=1

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

if [ -n "$mutate" ]; then
	# Reinstate the naive pattern: any `#` followed by digits, anywhere.
	sed -e "s|^ISSUE_REF=.*$|ISSUE_REF='#[0-9]+'|" "$subject" > "$work/subject.sh"
	chmod +x "$work/subject.sh"
	subject="$work/subject.sh"
	if ! grep -q "^ISSUE_REF='#\[0-9\]+'$" "$subject"; then
		echo "MUTATION DID NOT APPLY — the line it rewrites has changed." >&2
		echo "These tests cannot claim to have been verified. Fix the mutation." >&2
		exit 1
	fi
	echo "### MUTATED: internal-issue-ref loosened to the naive #[0-9]+"
fi

# fixture builds a repo-shaped tree whose feature folders are clean and, on
# purpose, contain the near-miss text this check must not flag. The folder
# names are alphabetical so a plant can be placed deliberately late in the
# walk; `alpha` and `beta` are always ahead of anything a test plants.
fixture() { # fixture <dir>
	root="$1"
	mkdir -p "$root/docs/alpha" "$root/docs/beta" "$root/docs/gamma" \
		"$root/docs/gamma/reference" "$root/docs/delta"

	# Legacy top-level documents. Deliberately OUT OF SCOPE: AGENTS.md writes
	# the publication-bound rule about folders and calls this layout debt to
	# migrate. They carry a violation of every class, and the check must stay
	# silent about all of them.
	cat > "$root/docs/01-architecture.md" <<-'EOF'
		Tracked as go-qrdb#94, pending a decision.
		The three nodes answer at 10.0.0.1, 10.0.0.2 and 10.0.0.3.
		Config lives in /home/operator/.config/qrdb.
		Filed by agent-go-qrdb against build02.internal.
	EOF
	cat > "$root/docs/run-modes.md" <<-'EOF'
		Owner ruling, from [go-qrdb#62](../../issues/62).
		Root-relative too: [62](/issues/62), and [a PR](../../pulls/62).
	EOF

	# Feature folders: clean, and stuffed with the near misses.
	cat > "$root/docs/alpha/architecture.md" <<-'EOF'
		Custodians sign with a hardware factor (PIV/FIDO2/PKCS#11).
		See [api.md](api.md#403-and-405-are-different-claims).
		The server binds 127.0.0.1:8443 by default, or 0.0.0.0 to listen wide.
		Point the client at db.example.com and install under <install-dir>.
		Relative links to FILES are fine: [the code](../../pkg/firstboot/firstboot.go).
		So is a PUBLIC issue: https://github.com/jeremyhahn/go-qrdb/issues/62,
		and so is the shorthand for one: golang/go#12345, kubernetes/kubernetes#1.
	EOF
	cat > "$root/docs/alpha/engineering.md" <<-'EOF'
		1. Isolate the tenant keyspace.
		2. Route the read.
		3. Rebalance.

		Implied by #1: the raft subsystem becomes central. See also #3.
		mDNS names such as qrdb.local are discussed here on purpose.
		A User-Agent header, and a user-agent-string, are not principals.
	EOF
	cat > "$root/docs/beta/api.md" <<-'EOF'
		A fenced example, which is NOT exempt from this check:

		```sh
		# retry the request, see step #42 of the runbook
		curl https://db.example.com/v1/kv/k
		```
	EOF
	cat > "$root/docs/gamma/architecture.md" <<-'EOF'
		Nothing to see here.
	EOF
	cat > "$root/docs/gamma/reference/deep.md" <<-'EOF'
		A companion reference, nested, and scanned all the same.
	EOF
	cat > "$root/docs/delta/user.md" <<-'EOF'
		The last folder in the walk.
	EOF

	# A benign symlink into the tree. The walk follows symlinks, so this is
	# scanned -- and it is clean, so a clean tree stays clean.
	printf '%s\n' 'A companion document that lives elsewhere and is linked in.' \
		> "$root/companion.md"
	ln -sf ../../companion.md "$root/docs/gamma/linked.md"
}

pass=0
fail=0
expect() { # expect <accept|reject> <label> <dir>
	want="$1"
	label="$2"
	dir="$3"
	if "$subject" "$dir" >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	if [ "$got" = "$want" ]; then
		printf '  PASS  %-62s (%s)\n' "$label" "$got"
		pass=$((pass + 1))
	else
		printf '  FAIL  %-62s want %s, got %s\n' "$label" "$want" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
	fi
}

# plant writes <text> into <relpath> of a FRESH fixture and returns its root.
plant() { # plant <name> <relpath> <text>
	dir="$work/$1"
	rm -rf "$dir"
	mkdir -p "$dir"
	fixture "$dir"
	printf '%s\n' "$3" >> "$dir/$2"
	echo "$dir"
}

echo "== a clean tree, near misses and all: must ACCEPT =="
clean="$(plant clean docs/delta/user.md 'Still clean.')"
expect accept "clean feature folders (PKCS#11, md anchors, #1, 127.0.0.1, .local)" "$clean"

echo "== legacy top-level docs are OUT OF SCOPE: must still ACCEPT =="
# The fixture's docs/01-architecture.md and docs/run-modes.md already carry an
# issue reference, three RFC 1918 addresses, a workstation path, an agent
# principal and an internal host name. A glob that had reached docs/** instead
# of docs/*/ would go red here.
expect accept "docs/*.md violations are not in scope (the deliberate boundary)" "$clean"

echo "== planted violations, each in a LATE file: must REJECT =="
# These pin the PATTERNS. Coverage of every file, independent of walk order, is
# proved by the enumeration further down; these deliberately do not claim it.
expect reject "planted go-qrdb#999 in a feature folder" \
	"$(plant issue docs/delta/user.md 'Tracked as go-qrdb#999.')"
expect reject "planted internal hostname in a feature folder" \
	"$(plant host docs/delta/user.md 'The forge answers at gitea.internal.')"
expect reject "planted workstation path in a feature folder" \
	"$(plant path docs/delta/user.md 'Config at /home/jhahn/.config/qrdb/qrdb.yaml.')"
expect reject "planted 192.168 address in a feature folder" \
	"$(plant addr docs/delta/user.md 'The forge answers at 198.51.100.91:3001.')"
expect reject "planted 172.16 address in a feature folder" \
	"$(plant addr172 docs/delta/user.md 'Peer at 172.20.4.7:63001.')"
expect reject "planted 10/8 address in a feature folder" \
	"$(plant addr10 docs/delta/user.md 'Peer at 10.4.2.9:63001.')"
expect reject "planted agent principal in a feature folder" \
	"$(plant agent docs/delta/user.md 'Filed by agent-go-qrdb on review.')"
# agent-<repo> for ANY repo, not only go-*. The go-* enumeration was the same
# fail-open shape the issue-reference pattern had, and it let this through.
expect reject "planted hyphenless principal agent-automatethethings" \
	"$(plant agentany docs/gamma/architecture.md 'Filed by agent-automatethethings during review.')"
expect reject "planted qualified ref jeremyhahn/go-truststrap#5 in a feature folder" \
	"$(plant qualified docs/delta/user.md 'Open as jeremyhahn/go-truststrap#5 upstream.')"
# The owner is what decides it. `jeremyhahn/go-truststrap#5` is as internal as
# `go-truststrap#5`; `golang/go#12345` is a public shorthand that resolves for
# the audience these folders are written for, and is in the clean fixture above
# -- blocking it would be the check over-enforcing its own rule.
# A HYPHENLESS internal repo. The first version of ISSUE_REF required a hyphen
# in the repository name, on the reasoning that every repo in this fleet is
# go-*, and AGENTS.md's own citation of the company repo `automatethethings`
# made that false on the day it was written: the reference passed the guard
# cleanly. A fail-open path in the enforcement mechanism is worse than the leak
# it exists to stop, because it also reports ok. Caught by the hosted review of
# this change.
expect reject "planted hyphenless internal ref automatethethings#5 in a feature folder" \
	"$(plant hyphenless docs/delta/user.md 'Recorded in automatethethings#5.')"
# The reference is in the link TARGET, not the link text, so no `repo#123`
# pattern can see it -- and it is the shape docs/run-modes.md:152 uses today.
expect reject "planted forge-relative issue link ../../issues/62 in a feature folder" \
	"$(plant relink docs/delta/user.md 'Owner ruling: [the decision](../../issues/62).')"
expect reject "planted root-relative issue link /issues/62 in a feature folder" \
	"$(plant rootlink docs/delta/user.md 'Owner ruling: [the decision](/issues/62).')"
expect reject "planted forge-relative PULL link ../../pulls/62 in a feature folder" \
	"$(plant pulllink docs/delta/user.md 'Discussed on [the PR](../../pulls/62).')"

echo "== a NESTED companion in a feature folder is scanned too =="
expect reject "planted go-qrdb#999 in docs/gamma/reference/deep.md" \
	"$(plant nested docs/gamma/reference/deep.md 'See go-qrdb#999.')"

echo "== INSIDE A FENCE is not exempt, and that is the point =="
fenced="$(plant fenced docs/beta/api.md '```sh')"
printf '%s\n' 'ssh operator@build02.internal' '```' >> "$fenced/docs/beta/api.md"
expect reject "internal hostname inside a fenced code block" "$fenced"

echo "== COVERAGE: EVERY file is scanned, whatever order the walk uses =="
# One fresh tree per file, each with the same single violation, each expected
# to reject. A scanner that stopped after its first file passes only if that
# file is the one planted -- and this plants every file in turn, so there is no
# such file. No traversal order is assumed, which is the point: find guarantees
# none.
cover="$work/cover-src"
rm -rf "$cover"
mkdir -p "$cover"
fixture "$cover"
covered=0
find "$cover" -type f | sed "s|^${cover}/||" | sort > "$work/coverfiles"
while IFS= read -r rel; do
	[ -n "$rel" ] || continue
	case "$rel" in
	docs/*/*) ;;
	*) continue ;;  # legacy top-level and out-of-tree companions are out of scope
	esac
	covered=$((covered + 1))
	expect reject "only violating file is ${rel}" \
		"$(plant "cover${covered}" "$rel" 'Tracked as go-qrdb#999.')"
done < "$work/coverfiles"
if [ "$covered" -lt 5 ]; then
	printf '  FAIL  %-62s only %s in-scope files\n' "the coverage fixture is too small to prove anything" "$covered"
	fail=$((fail + 1))
else
	printf '  PASS  %-62s (%s files)\n' "every in-scope fixture file was the sole violation in turn" "$covered"
	pass=$((pass + 1))
fi

echo "== COVERAGE: several plants at once are all reported =="
multi="$(plant multi docs/delta/user.md 'Tracked as go-qrdb#999.')"
printf '%s\n' 'Peer at 198.51.100.91.' >> "$multi/docs/gamma/architecture.md"
printf '%s\n' 'Config at /home/jhahn/x.' >> "$multi/docs/gamma/reference/deep.md"
expect reject "three plants across three files" "$multi"
if "$subject" "$multi" > "$work/multi.out" 2>&1; then :; else :; fi
missing=""
for w in 'docs/delta/user.md' 'docs/gamma/architecture.md' 'docs/gamma/reference/deep.md'; do
	grep -q "$w" "$work/multi.out" || missing="$missing $w"
done
if [ -z "$missing" ]; then
	printf '  PASS  %-62s (%s)\n' "all three plants named in one run" "reported"
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s missing:%s\n' "all three plants named in one run" "$missing"
	sed 's/^/          /' "$work/multi.out"
	fail=$((fail + 1))
fi

echo "== a SYMLINK publishes with the folder, so it is scanned =="
# `find -type f` skips symlinks, so docs/alpha/link.md -> ../../secret.md was
# scanned as nothing while it publishes as its target's contents: the guard
# could declare a tree clean without having read everything the tree ships.
# The target sits OUTSIDE docs/ where no other rule would ever look, so only
# following the link can find it.
symlinked="$(plant symlinked docs/delta/user.md 'Still clean.')"
printf '%s\n' 'Tracked as go-qrdb#999.' > "$symlinked/secret.md"
ln -sf ../../secret.md "$symlinked/docs/gamma/leak.md"
expect reject "a violating file reached only through a symlink" "$symlinked"

echo "== a BINARY-classified file is scanned, not silently skipped =="
# `grep -I` treats a binary-classified file as having no matches, silently. A
# document carrying an incidental NUL byte was therefore skipped while the
# folder's claim was that every file in it is scanned -- and `gitea.internal`
# is `gitea.internal` whatever else the file contains, so it publishes either
# way.
binary="$(plant binary docs/delta/user.md 'Still clean.')"
printf 'header\000\000 the forge answers at gitea.internal\n' \
	> "$binary/docs/gamma/architecture.md"
expect reject "a NUL-carrying file is scanned, not treated as no-match" "$binary"

echo "== ACCOUNTING: a file the scan did not READ is a failure, not a silence =="
# This is the structural answer to the class, rather than to any member of it.
# Six different mechanisms have made this check skip content and report clean --
# `|| true` over grep's exit 2, `-type f` past a symlink, `-I` past a NUL byte,
# a glob metacharacter, a walk outside the checkout, an enumeration one level
# too shallow. Predicting the seventh is not a strategy, so the scan counts the
# files grep actually opened against the files enumerated.
#
# The shim below models a SILENT skip specifically: it exits 0, prints no
# error, and simply omits one file from the accounting pass -- which is exactly
# what `grep -I` did with a binary file. No pattern, exit status or stderr
# reveals it. Only the count does.
mkdir -p "$work/skipbin"
cat > "$work/skipbin/grep" <<'SHIM'
#!/bin/sh
case " $* " in
*" -ac "*) /usr/bin/grep "$@" | sed '$d'; exit 0 ;;
esac
exec /usr/bin/grep "$@"
SHIM
chmod +x "$work/skipbin/grep"
if PATH="$work/skipbin:/usr/bin:/bin" "$subject" "$clean" > "$work/out" 2>&1; then
	printf '  FAIL  %-62s want %s, got %s\n' "a silently skipped file must not read as clean" reject accept
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
else
	printf '  PASS  %-62s (%s)\n' "a silently skipped file must not read as clean" reject
	pass=$((pass + 1))
fi
# ...and it must NAME the file, not merely fail: a count mismatch a reader
# cannot act on is only half an answer.
if grep -q "read [0-9]* of [0-9]* files" "$work/out"; then
	printf '  PASS  %-62s (%s)\n' "the accounting failure states both counts" reported
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s\n' "the accounting failure states both counts"
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
fi

# A clean run must state the counts too. A bare `ok` cannot distinguish
# "examined everything, found nothing" from "examined some of it, found
# nothing", and that indistinguishability is what let most of this check's own
# defects survive review.
if "$subject" "$clean" > "$work/out" 2>&1 &&
	grep -q "^ok  scanned [0-9]* of [0-9]* files" "$work/out"; then
	printf '  PASS  %-62s (%s)\n' "a clean run reports how many files it read" reported
	pass=$((pass + 1))
else
	printf '  FAIL  %-62s\n' "a clean run reports how many files it read"
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
fi

echo "== a TRAVERSAL error is a failure, not a silent pass =="
# A find that cannot complete is a different failure from grep's and takes a
# different branch. Both of the subject's walks get a case.
#
# THIS IS A SHIM, NOT A chmod 000 DIRECTORY, and the reason is worth keeping:
# it WAS a chmod, and it passed here and failed in CI, because CI runs as root
# and root reads a mode-000 directory perfectly well. The tree scanned clean
# and the case reported "want reject, got accept". A test whose premise is a
# permission check is a test that quietly means something different depending
# on who runs it -- and the branch under test is "find returned nonzero", which
# a shim states directly and identically for every uid.
mkdir -p "$work/failfind"
cat > "$work/failfind/find" <<'SHIM'
#!/bin/sh
echo "find: /docs/gamma/locked: Permission denied" >&2
exit 1
SHIM
chmod +x "$work/failfind/find"
if PATH="$work/failfind:/usr/bin:/bin" "$subject" "$clean" > "$work/out" 2>&1; then
	printf '  FAIL  %-62s want %s, got %s\n' "a failing entry walk must not read as clean" reject accept
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
else
	printf '  PASS  %-62s (%s)\n' "a failing entry walk must not read as clean" reject
	pass=$((pass + 1))
fi

# The SECOND walk, the one that collects the files to scan. This shim lets the
# entry walk succeed and fails only the `-type f` collection, so the first
# branch cannot be what caught it.
mkdir -p "$work/failfind2"
cat > "$work/failfind2/find" <<'SHIM'
#!/bin/sh
case " $* " in
*" -type f "*)
	echo "find: read error during traversal" >&2
	exit 1
	;;
esac
exec /usr/bin/find "$@"
SHIM
chmod +x "$work/failfind2/find"
if PATH="$work/failfind2:/usr/bin:/bin" "$subject" "$clean" > "$work/out" 2>&1; then
	printf '  FAIL  %-62s want %s, got %s\n' "a failing file walk must not read as clean" reject accept
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
else
	printf '  PASS  %-62s (%s)\n' "a failing file walk must not read as clean" reject
	pass=$((pass + 1))
fi

echo "== OPERATIONAL FAILURE must never read as a clean tree =="
# The scan used to be `find ... -exec grep ... + 2>/dev/null || true`, which
# reports 1 both when grep matched nothing and when grep could not read a file.
# `|| true` erased the distinction and `2>/dev/null` erased the message, so a
# grep that never ran left an empty result that read as "clean" and printed ok.
# A guard that prints ok because it could not look is worse than no guard: the
# ok is the thing people act on.
#
# A fake grep that exits 2 is exactly what an I/O error looks like to the
# caller. PATH is REPLACED for this one call, the way require-tool_test.sh does
# it, so the fake is unavoidable rather than merely first.
mkdir -p "$work/fakebin"
{
	echo '#!/bin/sh'
	echo 'exit 2'
} > "$work/fakebin/grep"
chmod +x "$work/fakebin/grep"
if PATH="$work/fakebin:/usr/bin:/bin" "$subject" "$clean" > "$work/out" 2>&1; then
	printf '  FAIL  %-62s want %s, got %s\n' "grep exiting 2 must not read as clean" reject accept
	sed 's/^/          /' "$work/out"
	fail=$((fail + 1))
else
	printf '  PASS  %-62s (%s)\n' "grep exiting 2 must not read as clean" reject
	pass=$((pass + 1))
fi

echo "== an EMPTY scope is a failure, not a silent pass =="
# docs/ exists but holds no feature folder, so the walk finds nothing. Scanning
# nothing and reporting ok is the failure mode this check exists to be, not the
# one it has.
mkdir -p "$work/nofolders/docs"
echo "legacy only" > "$work/nofolders/docs/01-architecture.md"
expect reject "docs/ with no feature folders at all" "$work/nofolders"

echo "== awkward FILENAMES are scanned, not expanded away or split =="
# $files is unquoted at the grep so one invocation gets the whole list. At the
# shell's defaults that is fail-open twice: the default IFS splits on spaces,
# and each word is then GLOBBED -- a violating `[a].md` sitting beside `a.md`
# expands to `a.md`, the violating file is never given to grep, and the script
# prints ok. `set -f` and IFS=newline are what make these two reject.
glob="$(plant glob docs/delta/user.md 'Still clean.')"
printf '%s\n' 'Tracked as go-qrdb#999.' > "$glob/docs/gamma/[a].md"
printf '%s\n' 'clean' > "$glob/docs/gamma/a.md"
expect reject "a glob-metacharacter filename is scanned, not expanded away" "$glob"

spacey="$(plant spacey docs/delta/user.md 'Still clean.')"
printf '%s\n' 'The forge answers at gitea.internal.' \
	> "$spacey/docs/gamma/a file with spaces.md"
expect reject "a filename with spaces is scanned, not split into two" "$spacey"

echo "== a path this check cannot scan safely is a failure =="
# A NEWLINE in a path is the one case the newline-split list cannot represent,
# and it is detected by counting NUL terminators against newline-converted
# lines -- an assertion a `-print` walk could not have made.
nlname="$(plant nlname docs/delta/user.md 'Still clean.')"
nl="$(printf 'a\nb')"
: > "$nlname/docs/gamma/${nl}.md"
expect reject "a newline in a path is refused, not silently unscanned" "$nlname"

echo "== a symlink OUT OF the repository is refused, not dereferenced =="
# `find -L` dereferences anything, including a link out of the checkout. Its
# target does not publish with the repository, so following it would make the
# verdict depend on the machine the check ran on and could echo host file
# contents into a CI log. It is also a workstation path written into a feature
# folder, which is the class this rule forbids in the first place.
escape="$(plant escape docs/delta/user.md 'Still clean.')"
printf '%s\n' 'clean, and none of this checks business' > "$work/outside.md"
ln -sf "$work/outside.md" "$escape/docs/gamma/external.md"
expect reject "a symlink pointing outside the repository" "$escape"

# The FEATURE FOLDER itself as the link. An earlier version inspected only
# symlinks at depth 2 and walked straight past this one, then traversed the
# external directory with `find -L` and reported on whatever was in it. The
# external directory here is CLEAN, so a check that merely scanned it would
# print ok -- the assertion is that the tree is refused by link name, not that
# a violation was found inside it.
folderlink="$(plant folderlink docs/delta/user.md 'Still clean.')"
mkdir -p "$work/outsidedir"
printf '%s\n' 'nothing of interest' > "$work/outsidedir/architecture.md"
rm -rf "$folderlink/docs/alpha"
ln -sf "$work/outsidedir" "$folderlink/docs/alpha"
expect reject "a FEATURE FOLDER that is a symlink out of the repository" "$folderlink"

# An in-repo symlinked folder is fine, and its contents are scanned through it.
inrepolink="$(plant inrepolink docs/delta/user.md 'Still clean.')"
mkdir -p "$inrepolink/elsewhere"
printf '%s\n' 'Tracked as go-qrdb#999.' > "$inrepolink/elsewhere/architecture.md"
rm -rf "$inrepolink/docs/alpha"
ln -sf ../elsewhere "$inrepolink/docs/alpha"
expect reject "an in-repo symlinked feature folder is traversed, not skipped" "$inrepolink"

echo "== a missing docs/ directory is a failure, not a silent pass =="
mkdir -p "$work/empty"
expect reject "no docs/ directory at all" "$work/empty"

echo ""
echo "passed ${pass}, failed ${fail}"

if [ -n "$mutate" ]; then
	if [ "$fail" -eq 0 ]; then
		echo "MUTATION SURVIVED: loosening internal-issue-ref to the naive" >&2
		echo "#[0-9]+ broke NOTHING. The near-miss cases do not test the" >&2
		echo "narrowing they claim to." >&2
		exit 1
	fi
	echo "mutation killed by ${fail} case(s) — the near-miss tests are real."
	exit 0
fi

[ "$fail" -eq 0 ] || exit 1
