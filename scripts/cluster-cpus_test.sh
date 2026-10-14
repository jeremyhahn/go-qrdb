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
# cluster-cpus_test.sh — the guard for cluster-cpus.sh.
#
# Every case runs the script against a stub docker whose `info` reports a
# fixed NCPU, so nothing here needs a daemon. The runner this exists for has
# 4 CPUs under a cap of 8 (#673); case 1 is that host.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT/scripts/cluster-cpus.sh"

pass=0
fail=0

ok()   { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf 'FAIL %s\n' "$1" >&2; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT

# stub NCPU — a docker whose `info --format {{.NCPU}}` prints NCPU, and which
# records every call so a case can tell whether the daemon was asked.
stub() {
    printf '#!/bin/sh\necho "$*" >> "%s/calls"\n[ "$*" = "info --format {{.NCPU}}" ] || exit 2\necho "%s"\n' "$D" "$1" > "$D/docker"
    chmod 0755 "$D/docker"
    : > "$D/calls"
}

cap() {
    DOCKER="$D/docker" "$SCRIPT" "$@"
}

# ---------------------------------------------------------------------------
# 1. A host with fewer CPUs than the cap: the cap is the host's count, logged.
# ---------------------------------------------------------------------------
stub 4
out=$(cap 8 2> "$D/err")
if [ "$out" = 4 ]; then
    ok "CLUSTER_CPUS=8 on a 4-CPU host caps at 4"
else
    bad "CLUSTER_CPUS=8 on a 4-CPU host printed '$out'"
fi
if grep -q 'at 4 CPUs (CLUSTER_CPUS=8, docker host has 4)' "$D/err"; then
    ok "the decision names both figures on stderr"
else
    bad "the decision was not logged: $(cat "$D/err")"
fi

# ---------------------------------------------------------------------------
# 2. A host with more CPUs, or exactly as many: the cap is unchanged.
# ---------------------------------------------------------------------------
for host in 80 8; do
    stub "$host"
    out=$(cap 8 2>/dev/null)
    if [ "$out" = 8 ]; then
        ok "CLUSTER_CPUS=8 on a $host-CPU host stays 8"
    else
        bad "CLUSTER_CPUS=8 on a $host-CPU host printed '$out'"
    fi
done

# ---------------------------------------------------------------------------
# 3. CLUSTER_CPUS=0, docker's "unset", passes through without the daemon.
# ---------------------------------------------------------------------------
stub 4
out=$(cap 0 2>/dev/null)
if [ "$out" = 0 ] && [ ! -s "$D/calls" ]; then
    ok "CLUSTER_CPUS=0 is no cap and does not ask the daemon"
else
    bad "CLUSTER_CPUS=0 printed '$out', calls: $(cat "$D/calls")"
fi

# ---------------------------------------------------------------------------
# 4. A daemon that cannot be read fails the run; it must not become no cap.
# ---------------------------------------------------------------------------
printf '#!/bin/sh\necho "Cannot connect to the Docker daemon" >&2\nexit 1\n' > "$D/docker"
if out=$(cap 8 2>&1); then
    bad "an unreadable daemon was accepted: $out"
elif printf '%s' "$out" | grep -q 'docker info failed' && printf '%s' "$out" | grep -q 'Cannot connect'; then
    ok "an unreadable daemon fails the run and shows why"
else
    bad "an unreadable daemon was not reported: $out"
fi
for ncpu in '' abc 0 2.5; do
    stub "$ncpu"
    if cap 8 >/dev/null 2>&1; then
        bad "a daemon reporting '$ncpu' CPUs was accepted"
    else
        ok "a daemon reporting '$ncpu' CPUs is refused"
    fi
done

# ---------------------------------------------------------------------------
# 5. Malformed caps are refused.
# ---------------------------------------------------------------------------
stub 4
for want in '' -1 8.5 abc 08; do
    if cap "$want" >/dev/null 2>&1; then
        bad "CLUSTER_CPUS='$want' was accepted"
    else
        ok "CLUSTER_CPUS='$want' is refused"
    fi
done
if cap >/dev/null 2>&1; then
    bad "no argument was accepted"
else
    ok "no argument is refused"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
