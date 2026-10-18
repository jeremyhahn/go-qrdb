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
# Regression tests for check-public-releasable.sh.
#
# The repository is clean, so running the check on it exercises only its
# accepting branch. Every rejecting branch is proven here, against throwaway
# git repositories under mktemp -- nothing outside them is touched.
#
# NO REAL FLEET VALUE APPEARS HERE. The gate loads its rules from an untracked
# pattern file (owner ruling 2026-10-01), so these cases run it against a
# FIXTURE pattern file describing an invented fleet: the 10.123.45.0/24
# network, machines named labhost-NN and zeta-node, and an owner called
# fixtureowner. Every sample below is one of those invented values.
# XDG_CONFIG_HOME is pointed at an empty directory for every run, so a pattern
# file on the machine running these tests can never decide a result.
#
# FOUR PROPERTIES, each with a way of failing silently:
#
#   0. THE RULE SOURCE: no pattern file, an unknown class, an empty or
#      non-compiling rule, and a class with no rules each fail closed; each of
#      the three sources is found in its documented order.
#
#   1. DETECTION: every class and every host pattern is planted on its own and
#      must be reported with its path, and several plants in one tree must all
#      be reported in one run.
#   2. PRECISION: text that is in this repository today and is correct --
#      an opaque 192.168.1.x test address, /home/user, a word that merely
#      starts with a fleet name -- must pass. A gate that fires on those earns
#      an allowlist on its first day.
#   3. FAIL-CLOSED: git failing, a file git grep did not read, a header od
#      could not read, and a listing that is not this repository's each exit 1. Failures are induced by a git
#      shim on PATH, never by permissions, so the result does not depend on the
#      uid (CI runs as root).
#
# Usage: check-public-releasable_test.sh   (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subject="${SCRIPT_DIR}/check-public-releasable.sh"
real_git=$(command -v git)

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1: $2"; }

# The invented fleet. Each rule is written so it cannot match its own text.
patterns="$work/patterns"
cat > "$patterns" <<'PATTERNS'
# A fixture fleet for check-public-releasable_test.sh. Nothing here is real.
lab-address	(^|[^0-9.])10\.123\.45\.[0-9]{1,3}([^0-9]|$)
lab-host/i	(^|[^a-z0-9-])labhost-[0-9]{2}([^a-z0-9-]|$)
lab-host	(^|[^a-z0-9-])zeta-nod[e]([^a-z0-9-]|$)
owner-path	/home/fixtureowne[r]([^A-Za-z0-9._-]|$)|/mnt/labstor[e]
PATTERNS
empty_xdg="$work/xdg-empty"
mkdir -p "$empty_xdg"

lab_addr="10.123.45.7"
host_numbered="labhost-04"
host_shout="LABHOST-11"
host_exact="zeta-node"
owner_home="/home/fixtureowner/sources/x"
owner_mount="/mnt/labstore/backups"

# fixture <name>: a fresh repository holding the subject and one clean file,
# staged. Nothing is committed -- the gate reads the index, so staging is what
# makes a file "tracked", and no identity or signing key is needed.
fixture() {
	d="$work/$1"
	mkdir -p "$d/scripts" "$d/pkg"
	"$real_git" -C "$d" init -q .
	cp "$subject" "$d/scripts/check-public-releasable.sh"
	printf 'package pkg\n\n// Endpoint is e.g. "gpu-host.example.com:9090".\n' > "$d/pkg/a.go"
	"$real_git" -C "$d" add -A
	printf '%s\n' "$d"
}

# run <dir> [PATH prefix]: runs the subject against the fixture pattern file
# (or $PATTERNS_OVERRIDE, which may name no file at all), with the XDG source
# pointed at an empty directory.
run() {
	rc=0
	pf="${PATTERNS_OVERRIDE-$patterns}"
	if [ -n "${2:-}" ]; then
		PATH="$2:$PATH" XDG_CONFIG_HOME="${XDG_OVERRIDE:-$empty_xdg}" PUBLIC_RELEASE_PATTERNS_FILE="$pf" \
			sh "$1/scripts/check-public-releasable.sh" "$1" > "$work/out" 2>&1 || rc=$?
	elif [ -z "$pf" ]; then
		env -u PUBLIC_RELEASE_PATTERNS_FILE XDG_CONFIG_HOME="${XDG_OVERRIDE:-$empty_xdg}" \
			sh "$1/scripts/check-public-releasable.sh" "$1" > "$work/out" 2>&1 || rc=$?
	else
		XDG_CONFIG_HOME="${XDG_OVERRIDE:-$empty_xdg}" PUBLIC_RELEASE_PATTERNS_FILE="$pf" \
			sh "$1/scripts/check-public-releasable.sh" "$1" > "$work/out" 2>&1 || rc=$?
	fi
	return 0
}

expect_pass() { # expect_pass <name> <dir> [path prefix]
	run "$2" "${3:-}"
	if [ "$rc" -eq 0 ] && grep -q '^ok ' "$work/out"; then
		ok "$1"
	else
		bad "$1" "want exit 0 and an ok line, got exit $rc: $(tr '\n' ' ' < "$work/out")"
	fi
}

expect_reject() { # expect_reject <name> <dir> <text that must appear> [path prefix]
	run "$2" "${4:-}"
	if [ "$rc" -ne 0 ] && grep -qF -- "$3" "$work/out"; then
		ok "$1"
	else
		bad "$1" "want nonzero exit naming '$3', got exit $rc: $(tr '\n' ' ' < "$work/out")"
	fi
}

plant() { # plant <dir> <path> <line>; stages it
	mkdir -p "$(dirname -- "$1/$2")"
	printf '%s\n' "$3" >> "$1/$2"
	"$real_git" -C "$1" add -- "$2"
}

# --- accepting ---------------------------------------------------------------

d=$(fixture clean)
expect_pass "a clean tree passes" "$d"
if grep -q 'read 2 of 2 tracked files' "$work/out"; then
	ok "the pass line states how many files were read"
else
	bad "the pass line states how many files were read" "$(cat "$work/out")"
fi

d=$(fixture nearmiss)
plant "$d" pkg/near.go '// 192.168.1.10 and 192.168.5.5 are opaque test addresses; so is 10.0.0.1.'
plant "$d" pkg/near.go '// RFC 5737: 192.0.2.1, 198.51.100.7, 203.0.113.9.'
plant "$d" pkg/near.go '// neighbours of the fixture network: 110.123.45.7, 10.123.46.7, 10.123.45'
plant "$d" pkg/near.go '// placeholders: /home/user/.config, /home/alice/x, /Users/dev/x, /home/fixtureownerx/y'
plant "$d" pkg/near.go '// longer names: labhost-004, mylabhost-04, zeta-nodes, ZETA-NODE (that rule is case-sensitive)'
plant "$d" pkg/near.go '// tracker references are allowed in code: go-qrdb#123, #45'
plant "$d" .devcontainer/.env.example 'GITEA_HOST=forge.example.com:3000'
expect_pass "near-misses pass" "$d"

d=$(fixture untracked)
printf '%s\n' "$lab_addr" > "$d/pkg/notes.txt"
expect_pass "an untracked file does not publish and is not scanned" "$d"

d=$(fixture waived)
plant "$d" pkg/bench.md "measured on ${host_numbered} <!-- public-release-ok: benchmark provenance -->"
expect_pass "a waiver with a reason accepts its line" "$d"

# --- detection, one plant per case --------------------------------------------

d=$(fixture addr)
plant "$d" pkg/x.go "// dial ${lab_addr}:9090"
expect_reject "a lab-address rule rejects its value" "$d" "pkg/x.go:1: lab-address"
if grep -qF -- "$lab_addr" "$work/out"; then
	bad "a finding never prints the matched text" "the value appears in the output: $(tr '\n' ' ' < "$work/out")"
else
	ok "a finding never prints the matched text"
fi

i=0
for sample in "$host_numbered" "$host_shout" "$host_exact"; do
	i=$((i + 1))
	d=$(fixture "host$i")
	plant "$d" Makefile "#   ${sample} (runner) docker 29"
	expect_reject "lab-host sample $i is rejected" "$d" "VIOLATION: lab-host"
done

i=0
for sample in "$owner_home" "$owner_mount"; do
	i=$((i + 1))
	d=$(fixture "path$i")
	plant "$d" scripts/fixture_test.sh "coverage \"${sample}\""
	expect_reject "owner-path sample $i is rejected" "$d" "VIOLATION: owner-path"
done

d=$(fixture onehit)
plant "$d" pkg/x.go "// ${host_shout} and ${host_numbered} on one line"
run "$d"
if [ "$rc" -ne 0 ] && grep -q 'FAIL: 1 line' "$work/out"; then
	ok "a line two rules match is one violation"
else
	bad "a line two rules match is one violation" "exit $rc: $(tr '\n' ' ' < "$work/out")"
fi

d=$(fixture emptywaiver)
plant "$d" pkg/x.go "// ${lab_addr} // public-release-ok:"
expect_reject "a waiver with no reason is not a waiver" "$d" "VIOLATION: lab-address"

d=$(fixture unstaged)
printf '// %s\n' "$lab_addr" >> "$d/pkg/a.go"
expect_reject "an edited, unstaged tracked file is scanned as it stands" "$d" "pkg/a.go:4:"

d=$(fixture binary)
printf 'head\000tail %s\n' "$owner_home" > "$d/pkg/blob.bin"
"$real_git" -C "$d" add pkg/blob.bin
expect_reject "a binary-classified file is read, not skipped" "$d" "pkg/blob.bin:1:"

# --- executables, by leading bytes ------------------------------------------------

# plant_bytes <dir> <path> <printf format>: writes the bytes and stages them.
# Octal escapes only, so every byte is the same under every printf.
plant_bytes() {
	mkdir -p "$(dirname -- "$1/$2")"
	# shellcheck disable=SC2059 # the format IS the fixture
	printf "$3" > "$1/$2"
	"$real_git" -C "$1" add -- "$2"
}
# 58 zero bytes take an `MZ` stub to e_lfanew at 0x3c.
mz_pad=$(i=0; while [ "$i" -lt 58 ]; do printf '\\000'; i=$((i + 1)); done)
pe="MZ${mz_pad}\\100\\000\\000\\000PE\\000\\000\\114\\001"
elf='\177ELF\002\001\001\000\000\000\000\000\000\000\000\000\002\000\076\000'
macho_be32='\376\355\372\316\000\000\000\007'
macho_le64='\317\372\355\376\007\000\000\001'
macho_fat='\312\376\272\276\000\000\000\002\001\000\000\007'
macho_fat64='\312\376\272\277\000\000\000\001\001\000\000\007'

i=0
for spec in "elf|${elf}" "macho-be32|${macho_be32}" "macho-le64|${macho_le64}" "macho-fat|${macho_fat}" "macho-fat64|${macho_fat64}" "pe|${pe}"; do
	i=$((i + 1))
	kind=${spec%%|*}
	d=$(fixture "exec$i")
	plant_bytes "$d" "cmd/tool/${kind}" "${spec#*|}"
	expect_reject "a tracked ${kind} executable is rejected on its own" "$d" "    cmd/tool/${kind}"
	if ! grep -q 'VIOLATION: executable' "$work/out"; then
		bad "the ${kind} rejection names the executable class" "$(tr '\n' ' ' < "$work/out")"
	fi
done

# Binaries that are not executables, and must pass: a PNG; a Java class file,
# which shares the fat Mach-O magic but carries a version (52) where the
# architecture count would be; a text that merely starts `MZ`; and an `MZ` stub
# whose e_lfanew points past the end of the file.
d=$(fixture notexec)
plant_bytes "$d" assets/logo.png '\211PNG\015\012\032\012\000\000\000\015IHDR\000\000\000\001'
plant_bytes "$d" testdata/A.class '\312\376\272\276\000\000\000\064\000\017\012\000'
plant "$d" docs/mz.txt 'MZ is a monogram, and this line is long enough to reach offset 0x3c and past it.'
plant_bytes "$d" testdata/stub.bin "MZ${mz_pad}\\377\\377\\000\\000"
expect_pass "non-executable binaries pass, including the magic-byte near misses" "$d"

# The exemption list: an exact path listed in EXECUTABLE_EXEMPT passes; an
# executable at any other path still fails.
d=$(fixture exempt)
sed 's|^EXECUTABLE_EXEMPT=.$|EXECUTABLE_EXEMPT='"'"'\
# fixture: a vendored test binary\
testdata/vendored.elf|' "$d/scripts/check-public-releasable.sh" > "$d/scripts/x" && mv "$d/scripts/x" "$d/scripts/check-public-releasable.sh"
"$real_git" -C "$d" add scripts/check-public-releasable.sh
plant_bytes "$d" testdata/vendored.elf "$elf"
expect_pass "an executable at an exempted path passes" "$d"
plant_bytes "$d" testdata/vendored.elf.bak "$elf"
expect_reject "an exemption is the exact path, not a prefix" "$d" "    testdata/vendored.elf.bak"

# An od that cannot read a header is an unknown, never a pass.
mkdir -p "$work/odfailbin"
printf '#!/bin/sh\necho "od: read error" >&2\nexit 1\n' > "$work/odfailbin/od"
chmod +x "$work/odfailbin/od"
d=$(fixture odfail)
expect_reject "a header that cannot be read is a failure" "$d" "whether" "$work/odfailbin"

d=$(fixture symlink)
ln -s "$owner_home" "$d/pkg/link"
"$real_git" -C "$d" add pkg/link
expect_reject "a tracked symlink is scanned as its link value" "$d" "pkg/link:0:"

for p in .serena/project.yml .claude/skills/x/SKILL.md deploy/.env .env.production deploy/.env.local; do
	d=$(fixture "artefact$(printf '%s' "$p" | tr -c '[:lower:]' '_')")
	plant "$d" "$p" 'k: v'
	expect_reject "internal artefact ${p} is rejected" "$d" "    ${p}"
done

d=$(fixture multi)
plant "$d" docs/a.md "at ${lab_addr}"
plant "$d" cmd/b.go "// ${host_exact}"
plant "$d" test/c.sh "cd ${owner_home}"
run "$d"
if [ "$rc" -ne 0 ] && grep -q 'docs/a.md:1:' "$work/out" && grep -q 'cmd/b.go:1:' "$work/out" && grep -q 'test/c.sh:1:' "$work/out" && grep -q 'FAIL: 3 line' "$work/out"; then
	ok "every violation in the tree is reported in one run"
else
	bad "every violation in the tree is reported in one run" "exit $rc: $(tr '\n' ' ' < "$work/out")"
fi

# --- the rule source ------------------------------------------------------------

d=$(fixture nosource)
PATTERNS_OVERRIDE=''
expect_reject "no pattern file anywhere is a failure, naming all three sources" "$d" "no fleet pattern file"
PATTERNS_OVERRIDE='/nonexistent/patterns'
expect_reject "an env var naming a missing file is a failure" "$d" "no fleet pattern file"
unset PATTERNS_OVERRIDE

xdg="$work/xdg"
mkdir -p "$xdg/go-qrdb"
cp "$patterns" "$xdg/go-qrdb/public-release-patterns"
d=$(fixture fromxdg)
plant "$d" pkg/x.go "// ${lab_addr}"
PATTERNS_OVERRIDE='' XDG_OVERRIDE="$xdg"
expect_reject "the XDG source is used when nothing else is set" "$d" "VIOLATION: lab-address"
unset PATTERNS_OVERRIDE XDG_OVERRIDE

# The handbook checkout beside the repository wins over XDG. The fixture repo
# sits under $work/hb, so its sibling handbook is $work/hb/handbook.
mkdir -p "$work/hb/handbook/software/conventions"
cp "$patterns" "$work/hb/handbook/software/conventions/public-release-patterns"
d="$work/hb/repo"
mkdir -p "$d/scripts" "$d/pkg"
"$real_git" -C "$d" init -q .
cp "$subject" "$d/scripts/check-public-releasable.sh"
"$real_git" -C "$d" add scripts/check-public-releasable.sh
plant "$d" pkg/x.go "// ${host_exact}"
PATTERNS_OVERRIDE=''
run "$d"
if [ "$rc" -ne 0 ] && grep -q 'from the handbook checkout' "$work/out" && grep -q 'VIOLATION: lab-host' "$work/out"; then
	ok "the handbook checkout beside the repository is used"
else
	bad "the handbook checkout beside the repository is used" "exit $rc: $(tr '\n' ' ' < "$work/out")"
fi
unset PATTERNS_OVERRIDE

bad_patterns() { # bad_patterns <name> <file body> <expected text>
	printf '%s\n' "$2" > "$work/bad-patterns"
	d=$(fixture "src$(printf '%s' "$1" | tr -c '[:lower:]' '_')")
	PATTERNS_OVERRIDE="$work/bad-patterns"
	expect_reject "$1" "$d" "$3"
	unset PATTERNS_OVERRIDE
}
all_classes='lab-address	x1
lab-host	x2
owner-path	x3'
bad_patterns "an unknown class is refused" "$all_classes
lab-hots	x4" "unknown class 'lab-hots'"
bad_patterns "a rule with no pattern is refused" "$all_classes
owner-path" "has no pattern"
bad_patterns "a pattern that does not compile is refused" "$all_classes
lab-address	(unclosed" "does not compile"
bad_patterns "a class with no rules is refused" "lab-address	x1
lab-host	x2" "declares no owner-path rule"
bad_patterns "an empty pattern file is refused" "# only a comment" "declares no lab-address rule"

# --- fail-closed ----------------------------------------------------------------

d="$work/notgit"
mkdir -p "$d/scripts"
cp "$subject" "$d/scripts/check-public-releasable.sh"
expect_reject "outside a git work tree it fails" "$d" "is not inside a git work tree"

d=$(fixture nofloor)
"$real_git" -C "$d" rm -q --cached scripts/check-public-releasable.sh
expect_reject "a listing that does not contain the script is refused" "$d" "does not contain this script"

# shim <name> <case body>: a git that misbehaves for one subcommand shape and
# delegates everything else to the real one.
shim() {
	mkdir -p "$work/$1"
	{
		printf '#!/bin/sh\n'
		printf '%s\n' "$2"
		printf 'exec "%s" "$@"\n' "$real_git"
	} > "$work/$1/git"
	chmod +x "$work/$1/git"
	printf '%s\n' "$work/$1"
}

d=$(fixture lsfail)
bin=$(shim lsfailbin 'case "$*" in *ls-files*) echo "fatal: index file corrupt" >&2; exit 128;; esac')
expect_reject "git ls-files failing is a failure" "$d" "git ls-files failed" "$bin"

d=$(fixture grepfail)
plant "$d" pkg/x.go "// ${lab_addr}"
bin=$(shim grepfailbin 'case "$*" in *" -n -E "*) echo "fatal: grep broke" >&2; exit 128;; esac')
expect_reject "git grep failing mid-scan is a failure, not a clean result" "$d" "git grep exited 128" "$bin"

d=$(fixture skipped)
bin=$(shim skippedbin 'case "$*" in *" -c -e "*) "'"$real_git"'" "$@" | head -n 1; exit 0;; esac')
expect_reject "a tracked file git grep did not read is a failure" "$d" "did not read is not" "$bin"

echo ""
echo "passed ${pass}, failed ${fail}"
[ "$fail" -eq 0 ]
