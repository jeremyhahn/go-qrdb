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
# check-compose-targets_test.sh — the guard for check-compose-targets.sh.
#
# On a healthy tree the scanner only ever runs its ACCEPTING branch, so every
# rejecting branch -- the whole reason it exists -- would otherwise ship
# untested. Same reasoning as test-tool-versions and test-publication-bound.
#
# Each case builds a throwaway tree with its own compose file and Dockerfile
# and runs the scanner's own logic against it, so no case can disturb the real
# repository.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCANNER="$ROOT/scripts/check-compose-targets.sh"

pass=0
fail=0

ok()   { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf 'FAIL %s\n' "$1" >&2; }

# scanner_with SETS DEST — copy the scanner with its COMPOSE_SETS list REPLACED
# by the given sets. Replacing rather than prepending matters: an earlier draft
# of this file prepended, left the repository's real entries in place, and two
# cases failed against files that were simply absent from the fixture tree.
scanner_with() {
    awk -v sets="$1" '
        /^COMPOSE_SETS="$/ { print; print sets; inblock = 1; next }
        inblock && /^"$/   { print; inblock = 0; next }
        inblock            { next }
                           { print }
    ' "$SCANNER" > "$2"
    chmod 0755 "$2"
}

# work — a scratch tree carrying a scanner that looks only at the fixture, so
# the real compose files are never involved.
work() {
    d=$(mktemp -d)
    mkdir -p "$d/scripts" "$d/stack"
    scanner_with "stack/docker-compose.yml" "$d/scripts/check-compose-targets.sh"
    printf '%s' "$d"
}

# stub_docker DIR — write DIR/bin/docker, a stand-in docker (see its comment).
stub_docker() {
    mkdir -p "$1/bin"
    cat > "$1/bin/docker" <<'EOF'
#!/bin/sh
# A docker stand-in for the cases that are about the scanner's own reporting,
# not about compose's merge. STUB_COMPOSE=missing answers `docker compose
# version` the way a docker without the compose plugin does; otherwise that
# succeeds, and every other call fails the way a broken `compose config` does:
# a long error on stderr, nothing on stdout.
if [ "${1:-} ${2:-}" = "compose version" ]; then
    if [ "${STUB_COMPOSE:-ok}" = missing ]; then
        echo "docker: 'compose' is not a docker command." >&2
        echo "See 'docker --help'" >&2
        exit 1
    fi
    echo "Docker Compose version v2.0.0-stub"
    exit 0
fi
i=1
while [ "$i" -le 30 ]; do
    echo "stub compose error line $i" >&2
    i=$((i + 1))
done
exit 15
EOF
    chmod 0755 "$1/bin/docker"
}

# scan_stubbed DIR — run the fixture's scanner with the docker stub first on
# PATH; prints its combined output and then its exit status on the last line.
scan_stubbed() {
    (
        cd "$1"
        rc=0
        PATH="$1/bin:$PATH" ./scripts/check-compose-targets.sh 2>&1 || rc=$?
        echo "rc=$rc"
    )
}

# fixture_ok DIR — a Dockerfile and compose file that real compose accepts.
fixture_ok() {
    printf 'FROM busybox AS integration\n' > "$1/Dockerfile"
    printf 'services:\n  app:\n    build:\n      context: ..\n      dockerfile: Dockerfile\n      target: integration\n' \
        > "$1/stack/docker-compose.yml"
}

# ---------------------------------------------------------------------------
# S1. docker compose itself is unusable on the host (#1609: a new runner
#     failed all three sets with no cause). One FAIL line that names compose's
#     own error, a non-zero exit, and no per-set FAILs blaming the files.
# ---------------------------------------------------------------------------
d=$(work)
fixture_ok "$d"
stub_docker "$d"
out=$(STUB_COMPOSE=missing scan_stubbed "$d")
if printf '%s\n' "$out" | grep -qx "FAIL: docker compose is not usable on this host: docker: 'compose' is not a docker command."; then
    ok "an unusable docker compose is reported once, with its own error"
else
    bad "an unusable docker compose was not named: $out"
fi
if [ "$(printf '%s\n' "$out" | grep -c '^FAIL')" -eq 1 ] && ! printf '%s\n' "$out" | grep -q 'config failed for'; then
    ok "an unusable docker compose produces no per-set FAILs"
else
    bad "an unusable docker compose still blamed the compose sets: $out"
fi
if printf '%s\n' "$out" | tail -n 1 | grep -qx 'rc=0'; then
    bad "an unusable docker compose passed the scan"
else
    ok "an unusable docker compose fails the scan"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# S2. A set whose `docker compose config` fails shows compose's stderr under
#     the FAIL line, indented and capped at 20 lines, and still fails.
# ---------------------------------------------------------------------------
d=$(work)
fixture_ok "$d"
stub_docker "$d"
out=$(scan_stubbed "$d")
if [ "$(printf '%s\n' "$out" | grep -A1 '^FAIL: docker compose config failed for \[stack/docker-compose.yml\]$' | tail -n 1)" = "      stub compose error line 1" ]; then
    ok "a failed compose config shows compose's stderr under the FAIL line"
else
    bad "a failed compose config hid compose's stderr: $out"
fi
if printf '%s\n' "$out" | grep -qx '      stub compose error line 20' && ! printf '%s\n' "$out" | grep -q 'stub compose error line 21$'; then
    ok "compose's stderr is capped at 20 lines"
else
    bad "compose's stderr was not capped at 20 lines: $out"
fi
if printf '%s\n' "$out" | tail -n 1 | grep -qx 'rc=0'; then
    bad "a failed compose config passed the scan"
else
    ok "a failed compose config fails the scan"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# S3. DISCOVERY FLOOR: a listed compose file that no longer exists.
# ---------------------------------------------------------------------------
d=$(work)
printf 'FROM busybox AS integration\n' > "$d/Dockerfile"
stub_docker "$d"
out=$(scan_stubbed "$d")
if printf '%s' "$out" | grep -q 'compose file does not exist'; then
    ok "a moved or deleted compose file fails the scan"
else
    bad "a missing compose file did not fail: $out"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# The cases below ask real `docker compose` to resolve the merge, which is the
# point of them, so they need a usable docker compose. Without one they are
# skipped on a workstation and FAIL under CI (CI is set), where the scanner
# itself must run.
# ---------------------------------------------------------------------------
if ! docker compose version >/dev/null 2>&1; then
    if [ -n "${CI:-}" ]; then
        bad "docker compose is not usable, and CI is set: the compose-merge cases cannot run"
    else
        printf 'skip the compose-merge cases: docker compose is not usable here\n'
    fi
    printf '\n%d passed, %d failed\n' "$pass" "$fail"
    [ "$fail" -eq 0 ]
    exit
fi

# ---------------------------------------------------------------------------
# 1. Accepting branch: the target names a real stage.
# ---------------------------------------------------------------------------
d=$(work)
cat > "$d/Dockerfile" <<'EOF'
FROM busybox AS builder
FROM busybox AS integration
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
      target: integration
EOF
if (cd "$d" && ./scripts/check-compose-targets.sh >/dev/null 2>&1); then
    ok "a target naming a real stage is accepted"
else
    bad "a target naming a real stage was rejected"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# 2. THE #327 DEFECT: unnamed final stage, target survives the overlay.
# ---------------------------------------------------------------------------
d=$(work)
cat > "$d/Dockerfile" <<'EOF'
FROM busybox AS builder
FROM busybox
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
      target: integration
EOF
out=$( (cd "$d" && ./scripts/check-compose-targets.sh 2>&1) || true )
if printf '%s' "$out" | grep -q 'not a stage in that file'; then
    ok "a target with no matching stage is rejected (#327)"
else
    bad "the #327 shape was not rejected: $out"
fi
if printf '%s' "$out" | grep -q 'target stage "integration" could not be found'; then
    ok "the message quotes what buildx would say"
else
    bad "the failure does not quote buildx's own error"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# 3. Overlay merge: the defect only exists in the JOIN, so it must be checked
#    there. Each file alone is consistent.
# ---------------------------------------------------------------------------
d=$(mktemp -d)
mkdir -p "$d/scripts" "$d/stack"
scanner_with "stack/docker-compose.yml:stack/docker-compose.gpu.yml" "$d/scripts/check-compose-targets.sh"
cat > "$d/Dockerfile" <<'EOF'
FROM busybox AS integration
EOF
cat > "$d/Dockerfile.gpu" <<'EOF'
FROM busybox AS go-downloader
FROM busybox
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
      target: integration
EOF
cat > "$d/stack/docker-compose.gpu.yml" <<'EOF'
services:
  app:
    build:
      dockerfile: Dockerfile.gpu
EOF
out=$( (cd "$d" && ./scripts/check-compose-targets.sh 2>&1) || true )
if printf '%s' "$out" | grep -q 'not a stage in that file'; then
    ok "an overlay that redirects dockerfile but inherits target is rejected"
else
    bad "the overlay join was not checked: $out"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# 4. DISCOVERY FLOOR: a set whose services name no target must FAIL, not pass.
#    This is the case that caught a stale entry on the scanner's first run.
# ---------------------------------------------------------------------------
d=$(work)
cat > "$d/Dockerfile" <<'EOF'
FROM busybox AS integration
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
EOF
out=$( (cd "$d" && ./scripts/check-compose-targets.sh 2>&1) || true )
if printf '%s' "$out" | grep -q 'no service in'; then
    ok "a compose set with no targets fails rather than passing vacuously"
else
    bad "the discovery floor did not fire: $out"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# 6. A Dockerfile that exists but declares no named stage at all.
# ---------------------------------------------------------------------------
d=$(work)
cat > "$d/Dockerfile" <<'EOF'
FROM busybox
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
      target: integration
EOF
out=$( (cd "$d" && ./scripts/check-compose-targets.sh 2>&1) || true )
if printf '%s' "$out" | grep -q 'declares no named stage at all'; then
    ok "a Dockerfile with no named stage is reported distinctly"
else
    bad "the no-stage case was not distinguished: $out"
fi
rm -rf "$d"

# ---------------------------------------------------------------------------
# 7. buildx matches stage names case-insensitively; so must the scanner.
# ---------------------------------------------------------------------------
d=$(work)
cat > "$d/Dockerfile" <<'EOF'
FROM busybox AS Integration
EOF
cat > "$d/stack/docker-compose.yml" <<'EOF'
services:
  app:
    build:
      context: ..
      dockerfile: Dockerfile
      target: integration
EOF
if (cd "$d" && ./scripts/check-compose-targets.sh >/dev/null 2>&1); then
    ok "stage names match case-insensitively, as buildx does"
else
    bad "a case difference was treated as a missing stage"
fi
rm -rf "$d"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
