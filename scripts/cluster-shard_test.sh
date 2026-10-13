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
# cluster-shard_test.sh — the guard for cluster-shard.sh.
#
# On a healthy tree `check` only runs its accepting branch, so the rejecting
# branches -- a test no leg selects, a test two legs select, a list that is
# empty -- would ship untested. Same reasoning as test-compose-targets.
#
# Every case feeds a fixture list through CLUSTER_SHARD_LIST, so nothing here
# compiles the integration package. The rejecting cases run a copy of the
# script with one defect planted in it, the defect each check exists to catch.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT/scripts/cluster-shard.sh"

pass=0
fail=0

ok()   { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf 'FAIL %s\n' "$1" >&2; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT

# Seven names, unsorted, with TestA a prefix of TestAB: an unanchored
# expression for TestA would select TestAB too.
printf '%s\n' TestC TestAB TestA TestE TestB TestD TestF > "$D/list"
printf 'ok  \tgithub.com/x/y\t0.1s\n' >> "$D/list"

shard() {
    CLUSTER_SHARD_LIST="$D/list" CLUSTER_SHARD_MIN=1 "$@"
}

# planted FROM TO — a copy of the script with one line rewritten.
planted() {
    FROM="$1" TO="$2" awk '{
        from = ENVIRON["FROM"]; to = ENVIRON["TO"]
        i = index($0, from)
        if (i) $0 = substr($0, 1, i - 1) to substr($0, i + length(from))
        print
    }' "$SCRIPT" > "$D/planted.sh"
    chmod 0755 "$D/planted.sh"
    if cmp -s "$SCRIPT" "$D/planted.sh"; then
        bad "planting '$1' changed nothing: the case below would test the real script"
    fi
}

# ---------------------------------------------------------------------------
# 1. Accepting branch: the legs partition the list, for every leg count.
# ---------------------------------------------------------------------------
for n in 1 2 3 7; do
    if shard "$SCRIPT" check "$n" >/dev/null 2>&1; then
        ok "$n legs partition the list"
    else
        bad "$n legs did not partition the list"
    fi
done

# ---------------------------------------------------------------------------
# 2. The deal: sorted bytewise, round-robin, anchored.
# ---------------------------------------------------------------------------
got=$(shard "$SCRIPT" run 1/3)
if [ "$got" = '^(TestA|TestC|TestF)$' ]; then
    ok "leg 1/3 is every third name of the sorted list, anchored"
else
    bad "leg 1/3 is '$got'"
fi
got=$(shard "$SCRIPT" run 3/3)
if [ "$got" = '^(TestB|TestE)$' ]; then
    ok "leg 3/3 is the last of the round"
else
    bad "leg 3/3 is '$got'"
fi

# ---------------------------------------------------------------------------
# 3. A test dropped from one leg is selected by none: rejected, and named.
# ---------------------------------------------------------------------------
planted "'(NR - 1) % n == i - 1'" "'(NR - 1) % n == i - 1 && \$0 != \"TestD\"'"
out=$(shard "$D/planted.sh" check 3 2>&1 || true)
if printf '%s' "$out" | grep -q 'selected by none of 3 legs' && printf '%s' "$out" | grep -qx 'TestD'; then
    ok "a test no leg selects fails the check and is named"
else
    bad "a dropped test was not reported: $out"
fi

# ---------------------------------------------------------------------------
# 4. An unanchored expression selects TestAB in two legs: rejected.
# ---------------------------------------------------------------------------
planted "'^(%s)\$\\n'" "'(%s)\\n'"
out=$(shard "$D/planted.sh" check 3 2>&1 || true)
if printf '%s' "$out" | grep -q 'selected by more than one of 3 legs' && printf '%s' "$out" | grep -qx 'TestAB'; then
    ok "a test two legs select fails the check and is named"
else
    bad "a doubly selected test was not reported: $out"
fi

# ---------------------------------------------------------------------------
# 5. DISCOVERY FLOOR: a list under the floor fails rather than passing.
# ---------------------------------------------------------------------------
out=$(CLUSTER_SHARD_LIST="$D/list" CLUSTER_SHARD_MIN=8 "$SCRIPT" check 3 2>&1 || true)
if printf '%s' "$out" | grep -q 'under the floor of 8'; then
    ok "a list under the discovery floor fails the check"
else
    bad "the discovery floor did not fire: $out"
fi
out=$(CLUSTER_SHARD_LIST="$D/list" CLUSTER_SHARD_MIN=8 "$SCRIPT" run 1/3 2>&1 || true)
if printf '%s' "$out" | grep -q 'under the floor of 8'; then
    ok "a list under the discovery floor yields no expression"
else
    bad "run printed an expression under the floor: $out"
fi

# ---------------------------------------------------------------------------
# 6. A package that does not compile fails; it must not become an empty list.
# ---------------------------------------------------------------------------
printf '#!/bin/sh\necho "./x.go:1: syntax error"\nexit 1\n' > "$D/go"
chmod 0755 "$D/go"
out=$(GO="$D/go" CLUSTER_SHARD_MIN=1 "$SCRIPT" run 1/3 2>&1 || true)
if printf '%s' "$out" | grep -q 'go test -list .* failed' && printf '%s' "$out" | grep -q 'syntax error'; then
    ok "a failing go test -list fails the run and shows why"
else
    bad "a failing go test -list was not reported: $out"
fi

# ---------------------------------------------------------------------------
# 7. Malformed leg specs are refused.
# ---------------------------------------------------------------------------
for spec in 0/3 4/3 1/0 3 a/3 1/x 01/3; do
    if shard "$SCRIPT" run "$spec" >/dev/null 2>&1; then
        bad "leg spec '$spec' was accepted"
    else
        ok "leg spec '$spec' is refused"
    fi
done
if shard "$SCRIPT" run 8/8 >/dev/null 2>&1; then
    bad "an empty leg (leg 8 of 8, 7 tests) was accepted"
else
    ok "an empty leg is refused"
fi
if shard "$SCRIPT" check 8 >/dev/null 2>&1; then
    bad "more legs than tests passed the check"
else
    ok "more legs than tests fails the check"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
