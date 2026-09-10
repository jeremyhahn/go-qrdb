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
# check-compose-targets.sh — every `target:` a compose service names exists as
# a stage in the Dockerfile that service builds (go-qrdb#327).
#
# WHY THIS EXISTS. Compose merges overlays BY SERVICE NAME and merges keys
# individually: an overlay that overrides `dockerfile:` does NOT drop the
# `target:` sitting beside it in the base file. So
# `docker-compose.gpu.yml` redirected qrdbd and integration-tests at
# Dockerfile.gpu while the base file's `target: integration` survived the
# merge, and Dockerfile.gpu's final stage was unnamed. buildx was asked for a
# stage that did not exist and `make integration-test-gpu` could not build at
# all:
#
#     target stage "integration" could not be found
#
# WHY A SCRIPT AND NOT A TEST RUN. The GPU path is on no CI workflow, and a
# workstation without nvidia-smi never selects it (HAS_GPU=0, so
# integration-test-auto picks the CPU lane). Nothing exercised the broken join
# for as long as it was broken, and nothing would exercise the fix either. The
# stage graph is decidable BY READING, which is the whole reason this can be a
# scanner: it needs no GPU, no CUDA base image, and no build.
#
# THE DISCOVERY FLOOR. A pairing that finds no compose file, no service with a
# target, or a Dockerfile it cannot read is a FAILURE, not a pass. A scanner
# that silently checks nothing reports the same green as one that checked
# everything, and this one exists precisely because "nobody noticed" is how
# #327 survived.
#
# WHAT THIS DOES NOT CHECK, and why:
#   - That the image BUILDS. That needs the base images and the toolchain; this
#     asserts the stage graph only.
#   - Stages referenced by `COPY --from=` or `FROM <stage>`. Those fail the
#     build immediately and loudly on any path that builds the file at all;
#     the compose `target:` is the one that fails only on an unexercised lane.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

# Every compose file, and the overlays each one is legitimately combined with.
# Overlays are checked in combination because the defect is IN the merge: each
# file alone is consistent, and only the join produces the impossible pairing.
# .devcontainer/docker-compose.yml grew a `target:` on four services in
# go-qrdb#294 -- see that file for why -- so it is checked alone here, the
# same as any set with no legitimate overlay.
#
# devcontainer-gpu is in that file and names NO target, so it is not a pairing
# this scanner can check: with nothing named there is nothing to resolve. It is
# excluded from the reasoning above rather than from the scan -- the file IS
# scanned, and if that service ever grows a `target:` the scanner picks it up
# with no change here.
#
# An earlier version of this comment said Dockerfile.gpu "has exactly one
# stage, so there is no alternate target to name". That stopped being true in
# go-qrdb#327, which named its final stage `AS integration` precisely so the
# GPU integration lane could build -- the same commit that introduced this
# scanner. It now has two named stages, `go-downloader` and `integration`. The
# claim is corrected rather than deleted because a reader who checks it should
# find the file, not a confident sentence about it.
COMPOSE_SETS="
.devcontainer/docker-compose.yml
test/integration/api/docker-compose.yml
test/integration/api/docker-compose.yml:test/integration/api/docker-compose.gpu.yml
"

fail=0
pairs_checked=0
sets_checked=0

# stages_of DOCKERFILE — prints every named build stage, one per line.
# `FROM x AS name` in any case; buildx matches stage names case-insensitively,
# so the comparison below lowercases both sides.
stages_of() {
    sed -n 's/^[[:space:]]*[Ff][Rr][Oo][Mm][[:space:]].*[[:space:]][Aa][Ss][[:space:]]\{1,\}\([A-Za-z0-9._-]\{1,\}\).*$/\1/p' "$1" \
        | tr '[:upper:]' '[:lower:]'
}

for set in $COMPOSE_SETS; do
    [ -n "$set" ] || continue
    sets_checked=$((sets_checked + 1))

    args=""
    dir=""
    for f in $(echo "$set" | tr ':' ' '); do
        if [ ! -f "$f" ]; then
            echo "FAIL: compose file does not exist: $f" >&2
            echo "      (this list is in $0 and has gone stale)" >&2
            fail=1
            continue 2
        fi
        [ -n "$dir" ] || dir=$(dirname "$f")
        args="$args -f $(basename "$f")"
    done

    # `docker compose config` is what resolves the merge, so it is what gets
    # asked. Parsing the compose files by hand would re-implement the very
    # merge semantics that produced the defect, and would agree with itself
    # rather than with compose.
    #
    # --format json, NOT the default YAML: PyYAML is absent from the CI runner
    # image, and a scanner that needs a pip install to run is a scanner that
    # gets switched off. json is in the standard library everywhere.
    rendered=$( (cd "$dir" && docker compose $args config --format json 2>/dev/null) ) || {
        echo "FAIL: docker compose config failed for [$set]" >&2
        fail=1
        continue
    }
    if [ -z "$rendered" ]; then
        echo "FAIL: docker compose config produced nothing for [$set]" >&2
        fail=1
        continue
    fi

    # service<TAB>dockerfile<TAB>target, one line per service that names a target.
    pairs=$(printf '%s\n' "$rendered" | python3 -c '
import json, sys
doc = json.load(sys.stdin)
for name, svc in sorted((doc.get("services") or {}).items()):
    build = svc.get("build")
    if not isinstance(build, dict):
        continue
    target = build.get("target")
    if not target:
        continue
    print("%s\t%s\t%s" % (name, build.get("dockerfile") or "Dockerfile", target))
') || {
        echo "FAIL: could not parse the rendered config for [$set]" >&2
        fail=1
        continue
    }

    if [ -z "$pairs" ]; then
        echo "FAIL: no service in [$set] names a build target." >&2
        echo "      Either the compose files stopped using \`target:\` -- in which" >&2
        echo "      case delete this entry -- or the render is not what it was." >&2
        echo "      A scanner that checks nothing must not report success." >&2
        fail=1
        continue
    fi

    pairs_checked=$((pairs_checked + $(printf '%s\n' "$pairs" | wc -l)))

    while read -r service dockerfile target; do
        [ -n "$service" ] || continue

        # compose reports the dockerfile relative to the build context, and
        # every context in this repository is the repository root.
        path="$dockerfile"
        [ -f "$path" ] || path="$ROOT/$dockerfile"
        if [ ! -f "$path" ]; then
            echo "FAIL: $service names $dockerfile, which does not exist" >&2
            fail=1
            continue
        fi

        want=$(printf '%s' "$target" | tr '[:upper:]' '[:lower:]')
        found=$(stages_of "$path")
        if [ -z "$found" ]; then
            echo "FAIL: $dockerfile declares no named stage at all," >&2
            echo "      but $service asks for target \"$target\"." >&2
            fail=1
            continue
        fi
        if ! printf '%s\n' "$found" | grep -qx "$want"; then
            echo "FAIL: $service builds $dockerfile with target \"$target\"," >&2
            echo "      which is not a stage in that file." >&2
            echo "      Stages present: $(printf '%s ' $found)" >&2
            echo "      buildx reports this as:" >&2
            echo "        target stage \"$target\" could not be found" >&2
            fail=1
            continue
        fi
    done <<EOF
$pairs
EOF
done

if [ "$sets_checked" -eq 0 ]; then
    echo "FAIL: no compose sets were checked; the list in $0 is empty." >&2
    exit 1
fi

if [ "$fail" -ne 0 ]; then
    exit 1
fi

echo "ok  every compose build target resolves to a real stage ($sets_checked compose sets)"
