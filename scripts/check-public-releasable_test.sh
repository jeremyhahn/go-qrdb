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
# THE FLEET VALUES ARE ASSEMBLED AT RUN TIME from fragments, so this file never
# contains one and the gate scans it like any other tracked file. A sample
# written out literally here would need a waiver, and a waiver in the gate's
# own test file is the first entry of an allowlist.
#
# THREE PROPERTIES, each with a way of failing silently:
#
#   1. DETECTION: every class and every host pattern is planted on its own and
#      must be reported with its path, and several plants in one tree must all
#      be reported in one run.
#   2. PRECISION: text that is in this repository today and is correct --
#      an opaque 192.168.1.x test address, /home/user, a word that merely
#      starts with a fleet name -- must pass. A gate that fires on those earns
#      an allowlist on its first day.
#   3. FAIL-CLOSED: git failing, a file git grep did not read, and a listing
#      that is not this repository's each exit 1. Failures are induced by a git
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

# Fleet values, from fragments.
net="192.168."
lab_office="${net}101.7"
lab_mgmt="${net}50.21"
lab_retired="${net}200.9"
host_station="vstation""-04"
host_hyper="dellemc""-02"
host_gpu_shout="GOD""LIKE"
host_gpu_fqdn="god""like.internal"
host_svc="ome""ga-claude"
host_mini="mini""-03"
host_mon="mo""n0"
host_mon_port="${host_mon}:10050"
host_svc_fqdn="ome""ga.internal"
host_svc_at="ssh dev@ome""ga"
host_router="core""rtr-01"
host_switch="rack1""swt-01"
host_fw="fw""-02"
lab_vlan="${net}102.4"
lab_vlan_hi="${net}114.9"
lab_svc="172.16.""200.7"
lab_p2p="10.254.""0.2"
lab_loop="10.255.""0.9"
owner_home="/home/""jhahn/sources/x"
owner_mac="/Users/""jhahn/x"
owner_mount="/mnt/""vsta""tion-01/x"
owner_raid="/mnt/""raid1/backups"
owner_disk="/mnt/""disk2/x"
owner_forge="/opt/""gitea/data"

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

# run <dir> [PATH prefix]: runs the subject, capturing output and status.
run() {
	rc=0
	if [ -n "${2:-}" ]; then
		PATH="$2:$PATH" sh "$1/scripts/check-public-releasable.sh" "$1" > "$work/out" 2>&1 || rc=$?
	else
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
plant "$d" pkg/near.go '// RFC 5737: 192.0.2.1, 198.51.100.7, 203.0.113.9. Not a quad: 10.192.168.50.1'
plant "$d" pkg/near.go '// placeholders: /home/user/.config, /home/alice/x, /Users/dev/x'
plant "$d" pkg/near.go "// longer words: ${host_svc%-claude}conf, ${host_mini}0, ${host_gpu_fqdn%%.internal}ness, ${host_mon}x"
plant "$d" pkg/near.go "// ambiguous names outside a host context: the ${host_svc%-claude} constant, Ome""ga: done, bring ${host_mon} up"
plant "$d" pkg/near.go '// neighbouring ranges: 10.254.0.30, 10.254.0.4, 172.16.201.1, 192.168.106.3, 10.2.55.0'
plant "$d" pkg/near.go "// a longer user name: /home/""jhahnx/y"
plant "$d" pkg/near.go '// tracker references are allowed in code: go-qrdb#123, #45'
plant "$d" .devcontainer/.env.example 'GITEA_HOST=forge.example.com:3000'
expect_pass "near-misses in use in this repository pass" "$d"

d=$(fixture untracked)
printf '%s\n' "$lab_office" > "$d/pkg/notes.txt"
expect_pass "an untracked file does not publish and is not scanned" "$d"

d=$(fixture waived)
plant "$d" pkg/bench.md "measured on ${host_gpu_shout} <!-- public-release-ok: benchmark provenance -->"
expect_pass "a waiver with a reason accepts its line" "$d"

# --- detection, one plant per case --------------------------------------------

for sample in "$lab_office" "$lab_mgmt" "$lab_retired" "$lab_vlan" "$lab_vlan_hi" "$lab_svc" "$lab_p2p" "$lab_loop"; do
	d=$(fixture "addr$(printf '%s' "$sample" | tr -c '0-9' '_')")
	plant "$d" pkg/x.go "// dial ${sample}:9090"
	expect_reject "lab-address ${sample%.*}.x is rejected" "$d" "pkg/x.go:1:"
done

i=0
for sample in "$host_station" "$host_hyper" "$host_gpu_shout" "$host_gpu_fqdn" "$host_svc" "$host_mini" "$host_mon_port" "$host_svc_fqdn" "$host_svc_at" "$host_router" "$host_switch" "$host_fw"; do
	i=$((i + 1))
	d=$(fixture "host$i")
	plant "$d" Makefile "#   ${sample} (runner) docker 29"
	expect_reject "lab-host sample $i is rejected" "$d" "VIOLATION: lab-host"
done

i=0
for sample in "$owner_home" "$owner_mac" "$owner_mount" "$owner_raid" "$owner_disk" "$owner_forge"; do
	i=$((i + 1))
	d=$(fixture "path$i")
	plant "$d" scripts/fixture_test.sh "coverage \"${sample}\""
	expect_reject "owner-path sample $i is rejected" "$d" "VIOLATION: owner-path"
done

d=$(fixture emptywaiver)
plant "$d" pkg/x.go "// ${lab_office} // public-release-ok:"
expect_reject "a waiver with no reason is not a waiver" "$d" "VIOLATION: lab-address"

d=$(fixture unstaged)
printf '// %s\n' "$lab_office" >> "$d/pkg/a.go"
expect_reject "an edited, unstaged tracked file is scanned as it stands" "$d" "pkg/a.go:4:"

d=$(fixture binary)
printf 'head\000tail %s\n' "$owner_home" > "$d/pkg/blob.bin"
"$real_git" -C "$d" add pkg/blob.bin
expect_reject "a binary-classified file is read, not skipped" "$d" "pkg/blob.bin:1:"

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
plant "$d" docs/a.md "at ${lab_office}"
plant "$d" cmd/b.go "// ${host_station}"
plant "$d" test/c.sh "cd ${owner_home}"
run "$d"
if [ "$rc" -ne 0 ] && grep -q 'docs/a.md:1:' "$work/out" && grep -q 'cmd/b.go:1:' "$work/out" && grep -q 'test/c.sh:1:' "$work/out" && grep -q 'FAIL: 3 line' "$work/out"; then
	ok "every violation in the tree is reported in one run"
else
	bad "every violation in the tree is reported in one run" "exit $rc: $(tr '\n' ' ' < "$work/out")"
fi

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
plant "$d" pkg/x.go "// ${lab_office}"
bin=$(shim grepfailbin 'case "$*" in *" -n -E "*) echo "fatal: grep broke" >&2; exit 128;; esac')
expect_reject "git grep failing mid-scan is a failure, not a clean result" "$d" "git grep exited 128" "$bin"

d=$(fixture skipped)
bin=$(shim skippedbin 'case "$*" in *" -c -e "*) "'"$real_git"'" "$@" | head -n 1; exit 0;; esac')
expect_reject "a tracked file git grep did not read is a failure" "$d" "did not read is not" "$bin"

echo ""
echo "passed ${pass}, failed ${fail}"
[ "$fail" -eq 0 ]
