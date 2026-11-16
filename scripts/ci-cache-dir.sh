#!/usr/bin/env bash
# CUSTOMIZE (go-qrdb, #1596): where a CI job keeps its Go build and
# golangci-lint caches, and their cleanup.
#
#   ci-cache-dir.sh            prints CI_CACHE_DIR=<dir> and CI_CACHE_LEASE=<file>
#                              for "$GITHUB_ENV"; scripts/in-toolchain-image.sh
#                              bind-mounts <dir>/gobuild and <dir>/golangci, or,
#                              when <dir> is empty, uses its named Docker volumes.
#   ci-cache-dir.sh release    for a job's always() step on a self-hosted runner:
#                              drops the job's lease, reclaims generations no job
#                              uses, and removes the legacy $RUNNER_TEMP/ci-cache.
#                              Optional: an unreleased lease expires (below).
#
#   - A hosted runner (RUNNER_ENVIRONMENT=github-hosted) is thrown away after
#     the job: the cache is $RUNNER_TEMP/ci-cache, which actions/cache
#     restores and saves; there is no lease.
#   - A self-hosted pool host keeps its disk: $RUNNER_TEMP is on the root
#     filesystem and outlives the job, and the cache there grew to 168 GB
#     (#1596). The cache is a persistent base directory on the data array:
#       $CI_CACHE_ROOT/gh-runner-cache/go-qrdb/ci-cache  when
#           $CI_CACHE_ROOT/gh-runner-cache is a writable directory, else
#       $CI_CACHE_ROOT/go-qrdb-ci-cache                  when $CI_CACHE_ROOT
#           is a writable directory, else
#       none (CI_CACHE_DIR empty: the named volumes), never $RUNNER_TEMP.
#     CI_CACHE_ROOT defaults to /mnt/raid0.
#
# Generations. Every self-hosted job on a host shares the base, and a stage's
# bind mount pins the directory's inode: deleting a directory a running stage
# has mounted leaves that stage writing into an unlinked tree (ENOENT on the
# next mkdir). So the base holds generations, gen.<id>/{gobuild,golangci}, and
# a `current` file naming the one new jobs use. A job takes a lease,
# gen.<id>/.leases/<lease>, in the generation it is given; the lease lives
# until it is older than CI_CACHE_LEASE_MAX_MIN, longer than any job may run,
# or until the job runs `ci-cache-dir.sh release` (CI_CACHE_LEASE set), which
# frees it early. When the current generation's gobuild and golangci
# together exceed CI_CACHE_MAX_GB (default 40; du -s --block-size=1G, which
# rounds each up), a new, empty generation becomes current, with a ::notice::.
# A generation that is not current is reclaimed only when it holds no live
# lease; a lease older than CI_CACHE_LEASE_MAX_MIN minutes (default 360, the
# GitHub job limit; ci.yml's longest job has 180 minutes) belongs to a job that
# is gone. Leases, rotation and
# reclaim happen under flock(1) on <base>/.lock; a reclaimed generation is
# renamed to trash.<id> under the lock and deleted after it.
#
# Deletion. The stages give the cache's files to their non-root uid (10001,
# scripts/in-toolchain-image.sh), which the runner's user cannot delete. A
# plain rm runs first; what survives goes to a privileged helper: a --rm
# container from CI_CACHE_RM_IMAGE (default TOOLCHAIN_IMAGE) as root with only
# DAC_OVERRIDE and FOWNER, no network, no new privileges.
# CI_CACHE_RM_HELPER, a command, replaces the container (the self-test's
# stand-in); it is called with the paths to delete. A deletion that still
# fails is a ::warning:: and is retried by the next job: cleanup never fails
# a job.
#
# Notices go to stderr: stdout is the environment file.
set -euo pipefail

mode=${1:-acquire}
case $mode in
  acquire | release) ;;
  *) echo "usage: ci-cache-dir.sh [release]" >&2; exit 2 ;;
esac

# rm_tree PATH...: deletes each path, privileged when a plain rm cannot.
# Returns 1, after a ::warning::, when one survives.
rm_tree() {
  local p left=()
  for p in "$@"; do
    [ -e "$p" ] || [ -L "$p" ] || continue
    rm -rf -- "$p" 2>/dev/null || true
    if [ -e "$p" ] || [ -L "$p" ]; then left+=("$p"); fi
  done
  [ "${#left[@]}" -gt 0 ] || return 0
  if [ -n "${CI_CACHE_RM_HELPER:-}" ]; then
    # shellcheck disable=SC2086 # a command and its arguments
    ${CI_CACHE_RM_HELPER} "${left[@]}" >/dev/null 2>&1 || true
  else
    local image=${CI_CACHE_RM_IMAGE:-${TOOLCHAIN_IMAGE:-}}
    if [ -n "$image" ] && command -v docker >/dev/null 2>&1; then
      for p in "${left[@]}"; do
        docker run --rm --network none --cap-drop ALL \
          --cap-add DAC_OVERRIDE --cap-add FOWNER \
          --security-opt no-new-privileges:true --user 0:0 \
          -v "$(dirname -- "$p"):/p" --entrypoint rm "$image" \
          -rf -- "/p/$(basename -- "$p")" >/dev/null 2>&1 || true
      done
    fi
  fi
  local failed=0
  for p in "${left[@]}"; do
    if [ -e "$p" ] || [ -L "$p" ]; then
      echo "::warning::Could not delete $p (owned by a stage's uid; no privileged helper ran): the next job retries" >&2
      failed=1
    fi
  done
  return "$failed"
}

if [ "${RUNNER_ENVIRONMENT:-}" = github-hosted ]; then
  if [ -z "${RUNNER_TEMP:-}" ]; then
    echo "ci-cache-dir: RUNNER_TEMP is unset on a hosted runner" >&2
    exit 2
  fi
  [ "$mode" = release ] && exit 0
  echo "CI_CACHE_DIR=$RUNNER_TEMP/ci-cache"
  echo "CI_CACHE_LEASE="
  exit 0
fi

root=${CI_CACHE_ROOT:-/mnt/raid0}
cap=${CI_CACHE_MAX_GB:-40}
case $cap in
  '' | *[!0-9]*)
    echo "ci-cache-dir: CI_CACHE_MAX_GB must be a whole number of GB: $cap" >&2
    exit 2
    ;;
esac
lease_max=${CI_CACHE_LEASE_MAX_MIN:-360}
case $lease_max in
  '' | 0 | *[!0-9]*)
    echo "ci-cache-dir: CI_CACHE_LEASE_MAX_MIN must be a positive whole number of minutes: $lease_max" >&2
    exit 2
    ;;
esac

writable_dir() { [ -d "$1" ] && [ -w "$1" ]; }

base=
if writable_dir "$root/gh-runner-cache"; then
  base=$root/gh-runner-cache/go-qrdb/ci-cache
elif writable_dir "$root"; then
  base=$root/go-qrdb-ci-cache
fi

# live_leases GEN: prints the generation's leases younger than lease_max.
live_leases() {
  [ -d "$1/.leases" ] || return 0
  find "$1/.leases" -mindepth 1 -maxdepth 1 -type f -mmin "-$lease_max" -print 2>/dev/null
}

# reclaim_locked: under the lock, renames to trash.<id> every generation that
# is not current and has no live lease, and anything else that is not part of
# the layout (a cache from before the generations).
reclaim_locked() {
  local cur e name
  cur=$(cat "$base/current" 2>/dev/null || true)
  for e in "$base"/* "$base"/.[!.]*; do
    [ -e "$e" ] || [ -L "$e" ] || continue
    name=${e##*/}
    case $name in
      .lock | current | current.tmp.* | trash.*) continue ;;
      gen.*)
        [ "$name" = "$cur" ] && continue
        [ -z "$(live_leases "$e")" ] || continue
        ;;
    esac
    mv -- "$e" "$base/trash.$name.$$.$RANDOM" 2>/dev/null || true
  done
}

# reclaim_trash: deletes every trash.* entry, outside the lock.
reclaim_trash() {
  local t=()
  local e
  for e in "$base"/trash.*; do
    [ -e "$e" ] || [ -L "$e" ] || continue
    t+=("$e")
  done
  [ "${#t[@]}" -eq 0 ] || rm_tree "${t[@]}" || true
}

if [ "$mode" = release ]; then
  if [ -n "${CI_CACHE_LEASE:-}" ] && [ -n "$base" ] && [ -d "$base" ] &&
    command -v flock >/dev/null 2>&1; then
    case $CI_CACHE_LEASE in
      "$base"/gen.*/.leases/*)
        (
          flock 9
          rm -f -- "$CI_CACHE_LEASE"
          reclaim_locked
        ) 9>>"$base/.lock" || true
        reclaim_trash
        ;;
      *) echo "::warning::CI_CACHE_LEASE is not a lease under $base: $CI_CACHE_LEASE" >&2 ;;
    esac
  fi
  if [ -n "${RUNNER_TEMP:-}" ]; then
    rm_tree "$RUNNER_TEMP/ci-cache" || true
  fi
  exit 0
fi

named_volumes() {
  echo "::notice::$1: the Go caches are the stage's named Docker volumes" >&2
  echo "CI_CACHE_DIR="
  echo "CI_CACHE_LEASE="
  exit 0
}

# The legacy cache in the pool host's temp dir holds stage-owned files that
# the job's final plain `rm -rf "$RUNNER_TEMP/ci-cache"` step cannot delete:
# delete it here, through the helper, before any stage runs. No stage on a
# self-hosted runner writes there any more.
if [ -n "${RUNNER_TEMP:-}" ]; then
  rm_tree "$RUNNER_TEMP/ci-cache" || true
fi

if [ -z "$base" ] || ! mkdir -p "$base" 2>/dev/null || ! writable_dir "$base"; then
  named_volumes "No writable cache directory under $root"
fi
command -v flock >/dev/null 2>&1 || named_volumes "flock(1) is missing, so $base cannot be shared"

# size_gb GEN: gobuild plus golangci, in whole GB rounded up.
size_gb() {
  local used=0 sub gb
  for sub in gobuild golangci; do
    [ -e "$1/$sub" ] || continue
    gb=$(du -s --block-size=1G "$1/$sub" 2>/dev/null | cut -f1) || gb=
    case $gb in '' | *[!0-9]*) gb=0 ;; esac
    used=$((used + gb))
  done
  echo "$used"
}

# Under the lock: pick (or rotate) the current generation and lease it. The
# subshell's own errexit holds because it is not in an || list.
set +e
out=$(
  set -euo pipefail
  exec 9>>"$base/.lock"
  flock 9
  cur=$(cat "$base/current" 2>/dev/null || true)
  case $cur in gen.*) [ -d "$base/$cur" ] || cur= ;; *) cur= ;; esac
  rotate=
  if [ -z "$cur" ]; then
    rotate=new
  else
    used=$(size_gb "$base/$cur")
    if [ "$used" -gt "$cap" ]; then
      echo "::notice::The Go caches in $base/$cur hold ${used} GB, over the ${cap} GB cap: rotated to a new generation; the old one is reclaimed once no job uses it" >&2
      rotate=cap
    fi
  fi
  if [ -n "$rotate" ]; then
    cur=gen.$(date -u +%Y%m%dT%H%M%S).$$.$RANDOM
    mkdir -p "$base/$cur/.leases"
    printf '%s\n' "$cur" >"$base/current.tmp.$$"
    mv -f -- "$base/current.tmp.$$" "$base/current"
  fi
  mkdir -p "$base/$cur/.leases"
  lease=$base/$cur/.leases/${GITHUB_RUN_ID:-local}.${GITHUB_RUN_ATTEMPT:-0}.${GITHUB_JOB:-job}.$$.$RANDOM
  : >"$lease"
  reclaim_locked
  echo "CI_CACHE_DIR=$base/$cur"
  echo "CI_CACHE_LEASE=$lease"
)
rc=$?
set -e
[ "$rc" = 0 ] || named_volumes "Could not lease a generation in $base"

reclaim_trash
echo "$out"
