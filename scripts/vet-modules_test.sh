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
# Regression tests for vet-modules.sh's DISCOVERY and MAPPING.
#
# A healthy run of `make vet-tags` only exercises the accepting path. Every
# rejecting branch — which is the entire reason the script exists — would
# otherwise ship untested, and this target has already shipped six fail-opens
# for exactly that reason (listed at the top of vet-modules.sh).
#
#   ./scripts/vet-modules_test.sh            run the cases
#   ./scripts/vet-modules_test.sh --mutate   re-break each guard in a COPY and
#                                            prove the cases still catch it
#
# The fixtures are throwaway trees, so no case depends on this repository's
# current module layout — a test that only passes against today's tree would
# start passing vacuously the moment someone adds a module.
set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
UNDER_TEST="${VET_MODULES_SH:-$SCRIPT_DIR/vet-modules.sh}"
pass=0; fail=0

ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n     %s\n' "$1" "$2"; }

# fixture <dir> <module-relpath>... — a tree with a go.mod at each path
fixture() {
	_d=$1; shift
	rm -rf "$_d"; mkdir -p "$_d"
	for _m in "$@"; do
		mkdir -p "$_d/$_m"
		printf 'module example.com/%s\n\ngo 1.26\n' "$(echo "$_m" | tr / _)" > "$_d/$_m/go.mod"
	done
}

run() { VET_MODULES_ROOT="$1" LINT_TAGS=t1 GORM_TAGS=gt "$UNDER_TEST" "$2" ${3:+"$3"} 2>&1; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT INT TERM

# --- discovery -------------------------------------------------------------

fixture "$TMP/a" . sdk/go
out=$(run "$TMP/a" --list); rc=$?
case "$rc:$out" in
0:*"$TMP/a"*) ok "discovery lists the root and a nested module" ;;
*) bad "discovery lists the root and a nested module" "rc=$rc out=$out" ;;
esac

# A tree with NO root go.mod must be refused, not swept partially. This is the
# rootless case, and the guard that first covered it used `grep -qw '\.'`,
# which matches the dot inside "./sdk/go" and accepted the list.
fixture "$TMP/b" sdk/go
out=$(run "$TMP/b" --list); rc=$?
case "$rc:$out" in
0:*) bad "a rootless tree is refused" "accepted it: $out" ;;
*"no ROOT module"*) ok "a rootless tree is refused" ;;
*) bad "a rootless tree is refused" "rc=$rc out=$out" ;;
esac

# An empty tree must be refused rather than reported clean.
fixture "$TMP/c"
out=$(run "$TMP/c" --list); rc=$?
case "$rc:$out" in
0:*) bad "an empty tree is refused" "accepted it" ;;
*"discovery returned NOTHING"*) ok "an empty tree is refused" ;;
*) bad "an empty tree is refused" "rc=$rc out=$out" ;;
esac

# vendor/ and .git/ are excluded, so a vendored dependency's go.mod does not
# become an "unmapped module" that fails every sweep.
fixture "$TMP/d" . vendor/foo .git/x
out=$(run "$TMP/d" --list); rc=$?
case "$out" in
*vendor*|*.git*) bad "vendor and .git are excluded" "leaked: $out" ;;
*) ok "vendor and .git are excluded" ;;
esac

# --- mapping ---------------------------------------------------------------

fixture "$TMP/e" .
out=$(VET_MODULES_ROOT="$TMP/e" LINT_TAGS=t1 GORM_TAGS=gt "$UNDER_TEST" --tags-for "$TMP/e" 2>&1)
case "$out" in
"t1
t1,integration") ok "the root maps to LINT_TAGS and LINT_TAGS,integration" ;;
*) bad "the root maps to LINT_TAGS and LINT_TAGS,integration" "got: $out" ;;
esac

out=$(VET_MODULES_ROOT="$TMP/e" LINT_TAGS=t1 GORM_TAGS=gt "$UNDER_TEST" --tags-for "$TMP/e/sdk/go/gorm/conformance" 2>&1)
[ "$out" = "gt" ] && ok "the conformance module maps to GORM_TAGS" \
                  || bad "the conformance module maps to GORM_TAGS" "got: $out"

out=$(VET_MODULES_ROOT="$TMP/e" LINT_TAGS=t1 GORM_TAGS=gt "$UNDER_TEST" --tags-for "$TMP/e/test/performance/qrdb" 2>&1)
[ "$out" = "benchmark" ] && ok "a benchmark module maps to 'benchmark'" \
                         || bad "a benchmark module maps to 'benchmark'" "got: $out"

# THE ONE THAT MATTERS: an unrecognised module is refused, not guessed at. A
# wrong tag set prints "matched no packages", which is indistinguishable from
# clean, so guessing is worse than refusing.
out=$(VET_MODULES_ROOT="$TMP/e" LINT_TAGS=t1 GORM_TAGS=gt "$UNDER_TEST" --tags-for "$TMP/e/brand/new" 2>&1); rc=$?
case "$rc:$out" in
0:*) bad "an unmapped module is refused" "accepted it: $out" ;;
*"UNMAPPED MODULE"*) ok "an unmapped module is refused" ;;
*) bad "an unmapped module is refused" "rc=$rc out=$out" ;;
esac

# --- the --vet sweep itself ------------------------------------------------
#
# THE MOST IMPORTANT CASES IN THIS FILE, and they were missing from the first
# version: --list and --tags-for were tested and the sweep was not, so a
# regression that skipped sdk/go -- THE EXACT DEFECT THIS SCRIPT EXISTS TO FIX
# -- would have passed every test above.
#
# A PATH-injected fake `go` records its working directory and arguments, so the
# assertions are about which module/tag combinations were actually invoked
# rather than about the command's exit code, which is what a sweep can get
# wrong while still looking right.

mkfakego() { # <bindir> <log> [module-substring-that-fails]
	mkdir -p "$1"
	cat > "$1/go" <<FAKE
#!/bin/sh
printf '%s|%s\n' "\$(pwd)" "\$*" >> "$2"
case "\$(pwd)" in
*"${3:-__never__}"*) exit 1 ;;
esac
exit 0
FAKE
	chmod +x "$1/go"
}

fixture "$TMP/v" . sdk/go sdk/go/gorm/conformance test/performance/cmd/kvcompare
LOG="$TMP/v.log"; : > "$LOG"; mkfakego "$TMP/bin" "$LOG"
out=$(cd "$TMP/v" && PATH="$TMP/bin:$PATH" VET_MODULES_ROOT=. LINT_TAGS=t1 GORM_TAGS=gt \
      "$UNDER_TEST" --vet t1 gt 2>&1); rc=$?

[ $rc -eq 0 ] && ok "a clean sweep exits 0" || bad "a clean sweep exits 0" "rc=$rc out=$out"

# The root and sdk/go each get BOTH tag sets, conformance gets GORM_TAGS, and
# kvcompare gets no -tags: 2+2+1+1 = SIX invocations. The count matters as much
# as the contents -- a sweep that ran four of six would satisfy every
# "contains" assertion below on its own. (This assertion was written as 5 and
# the test caught the arithmetic, which is the point of asserting a total.)
n=$(wc -l < "$LOG" | tr -d ' ')
[ "$n" = 6 ] && ok "the sweep invokes go exactly 6 times" \
             || bad "the sweep invokes go exactly 6 times" "got $n: $(cat "$LOG")"

want_line() {
	if grep -qF "$1" "$LOG"; then ok "$2"; else bad "$2" "absent from: $(cat "$LOG")"; fi
}
want_line "|vet -tags t1 ./..."             "the root is vetted with LINT_TAGS"
want_line "|vet -tags t1,integration ./..." "the root is vetted with LINT_TAGS,integration"
want_line "/sdk/go|vet -tags t1 ./..."      "SDK/GO IS VETTED -- the defect this script exists to catch"
want_line "/conformance|vet -tags gt ./..." "the conformance module is vetted with GORM_TAGS"
want_line "/kvcompare|vet ./..."            "kvcompare is vetted with no -tags"

# The toolchain override must reach the sweep. Moving the logic out of the
# Makefile dropped $(GOVET) once already: `make GO=go1.26.1 vet-tags` silently
# vetted with the PATH `go` instead, so the sweep reported on a different
# toolchain than the one the rest of the build uses.
: > "$LOG"; mkdir -p "$TMP/bin3"
cat > "$TMP/bin3/othergo" <<FAKE
#!/bin/sh
printf 'OTHERGO|%s\n' "\$*" >> "$LOG"
exit 0
FAKE
chmod +x "$TMP/bin3/othergo"
out=$(cd "$TMP/v" && PATH="$TMP/bin:$TMP/bin3:$PATH" VET_MODULES_ROOT=. \
      GOVET="othergo vet" "$UNDER_TEST" --vet t1 gt 2>&1); rc=$?
# Every invocation must come from the override. The fake `go` logs "<pwd>|...",
# the override logs "OTHERGO|...", so a surviving PATH-`go` call shows as a
# line starting with "/". (Written first as a negative grep for the tag string,
# which the override's own lines also contain -- the assertion failed while the
# behaviour was correct.)
# `grep -c` PRINTS 0 and EXITS 1 on no match, so `|| echo 0` appends a second
# zero and the comparison silently fails against a correct result. `|| true`
# keeps the printed count and discards only the status.
_other=$(grep -c '^OTHERGO|' "$LOG" || true)
_path=$(grep -c '^/' "$LOG" || true)
if [ "$_other" = 6 ] && [ "$_path" = 0 ]; then
	ok "GOVET overrides the vet command for every module"
else
	bad "GOVET overrides the vet command for every module" \
	    "override=$_other path-go=$_path rc=$rc log: $(cat "$LOG")"
fi

# A single module's failure must fail the whole sweep. A sweep that reported
# success while one module was red would be the same class of false green.
: > "$LOG"; mkfakego "$TMP/bin2" "$LOG" "sdk/go"
out=$(cd "$TMP/v" && PATH="$TMP/bin2:$PATH" VET_MODULES_ROOT=. LINT_TAGS=t1 GORM_TAGS=gt \
      "$UNDER_TEST" --vet t1 gt 2>&1); rc=$?
case "$rc:$out" in
0:*) bad "one module failing fails the sweep" "exited 0: $out" ;;
*"does not typecheck"*) ok "one module failing fails the sweep" ;;
*) bad "one module failing fails the sweep" "rc=$rc out=$out" ;;
esac
# ...and the sweep must CONTINUE past it rather than aborting, or a second
# broken module hides behind the first.
n=$(wc -l < "$LOG" | tr -d ' ')
[ "$n" = 6 ] && ok "the sweep continues past a failure and reports all modules" \
             || bad "the sweep continues past a failure" "only $n invocations: $(cat "$LOG")"

printf '\n%s: %d passed, %d failed\n' "$(basename "$0")" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1

# --- mutation --------------------------------------------------------------
#
# Re-break each guard in a COPY and assert the cases go red. A suite that
# passes both with and without the guard proves nothing.
[ "${1:-}" = "--mutate" ] || exit 0
printf '\n--- mutation: re-breaking each guard in a copy ---\n'
mrc=0
mutate() { # <name> <sed-expr> <case-that-must-fail>
	cp "$UNDER_TEST" "$TMP/mut.sh"; chmod +x "$TMP/mut.sh"
	sed -i "$2" "$TMP/mut.sh"
	if VET_MODULES_SH="$TMP/mut.sh" "$0" >/dev/null 2>&1; then
		printf '  SURVIVED  %s — the suite passes with the guard broken\n' "$1"; mrc=1
	else
		printf '  killed    %s\n' "$1"
	fi
}
# (4): accept a rootless list, the exact hole the first guard had
mutate "rootless list accepted" 's#^	for _m in \$1; do \[ "\$_m" = "\$ROOT" \] && return 0; done#	return 0#'
# unmapped module silently swept with the root tag set
mutate "unmapped module guessed" 's#^	\*)#	*) printf "%s\\n" "$LINT_TAGS"; return 0 ;;\n	*unreachable)#'
# The regression this whole script exists to prevent: a sweep that silently
# skips sdk/go. Without the --vet cases above, this mutant SURVIVES.
mutate "sdk/go silently skipped" 's#^	for m in \$mods; do#	for m in $mods; do\n\t\tcase "$m" in */sdk/go) continue ;; esac#'

[ $mrc -eq 0 ] && printf '\nall mutants killed\n' || printf '\nSOME MUTANTS SURVIVED\n'
exit $mrc
