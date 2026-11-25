#!/usr/bin/env bash
# CUSTOMIZE: where a CI job keeps its Go build and golangci-lint caches.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
clean() { bash "$here/ci-cache-clean.sh" "$@" || true; }

named_volumes() {
  echo "::notice::$1: the Go caches are the stage's named Docker volumes" >&2
  echo "CI_CACHE_DIR="
  exit 0
}

if [ "${RUNNER_ENVIRONMENT:-}" = github-hosted ]; then
  if [ -z "${RUNNER_TEMP:-}" ]; then
    echo "ci-cache-dir: RUNNER_TEMP is unset on a hosted runner" >&2
    exit 2
  fi
  echo "CI_CACHE_DIR=$RUNNER_TEMP/ci-cache"
  exit 0
fi

# A self-hosted pool runner is a container sharing only the Docker daemon:
# its $RUNNER_TEMP is private to it, while the stages bind-mount the same path
# on the daemon's host. No root is ever derived from RUNNER_TEMP here.
if [ -z "${CI_CACHE_ROOT:-}" ]; then
  named_volumes "Self-hosted runner with no CI_CACHE_ROOT"
fi
root=$CI_CACHE_ROOT
legacy=${RUNNER_TEMP:-}
cap=${CI_CACHE_MAX_GB:-40}
case $cap in
  '' | *[!0-9]*)
    echo "ci-cache-dir: CI_CACHE_MAX_GB must be a whole number of GB: $cap" >&2
    exit 2
    ;;
esac
hours=${CI_CACHE_RECLAIM_HOURS:-7}
case $hours in
  '' | 0 | *[!0-9]*)
    echo "ci-cache-dir: CI_CACHE_RECLAIM_HOURS must be a positive whole number of hours: $hours" >&2
    exit 2
    ;;
esac
reclaim_min=$((hours * 60))

# The legacy cache in the pool host's temp dir holds stage-owned files: delete
# it before any stage runs, since CI_CACHE_ROOT puts the cache elsewhere.
if [ -n "$legacy" ]; then
  clean "$legacy/ci-cache"
fi

writable_dir() { [ -d "$1" ] && [ -w "$1" ]; }

base=
if writable_dir "$root/gh-runner-cache"; then
  base=$root/gh-runner-cache/go-qrdb/ci-cache
elif writable_dir "$root"; then
  base=$root/go-qrdb-ci-cache
fi
if [ -z "$base" ] || ! mkdir -p "$base" 2>/dev/null || ! writable_dir "$base"; then
  named_volumes "No writable cache directory under $root"
fi

# size_gb GEN: gobuild plus golangci, in whole GB rounded up.
size_gb() {
  local used=0 sub gb
  for sub in gobuild golangci; do
    [ -e "$1/$sub" ] || continue
    # du exits nonzero when an entry vanishes or cannot be read, but still
    # prints the total of what it read: use that number. With no number, the
    # size is unknown: fail, never read it as 0 GB.
    gb=$(du -s --block-size=1G "$1/$sub" 2>/dev/null | cut -f1) || true
    case $gb in '' | *[!0-9]*) return 1 ;; esac
    used=$((used + gb))
  done
  echo "$used"
}

# stale PATH: true when PATH was last used more than reclaim_min minutes ago
# (its .last-use marker for a generation, else its own mtime).
stale() {
  local mark=$1
  [ -e "$1/.last-use" ] && mark=$1/.last-use
  [ -n "$(find "$mark" -maxdepth 0 -mmin "+$reclaim_min" -print 2>/dev/null)" ]
}

# select_locked: prints the generation this job uses, rotating past the cap,
# touches its marker, and moves stale generations to trash-*.
select_locked() {
  local cur used e name
  cur=$(cat "$base/current" 2>/dev/null || true)
  case $cur in gen-*) [ -d "$base/$cur" ] || cur= ;; *) cur= ;; esac
  if [ -n "$cur" ]; then
    if ! used=$(size_gb "$base/$cur"); then
      echo "::warning::du could not measure the Go caches in $base/$cur: counted as over the ${cap} GB cap, a new generation is current; the old one is deleted once unused for ${hours} h" >&2
      cur=
    elif [ "$used" -gt "$cap" ]; then
      echo "::notice::The Go caches in $base/$cur hold ${used} GB, over the ${cap} GB cap: a new generation is current; the old one is deleted once unused for ${hours} h" >&2
      cur=
    fi
  fi
  if [ -z "$cur" ]; then
    cur=gen-$(date -u +%Y%m%dT%H%M%SZ)-$$
    mkdir "$base/$cur"
    printf '%s\n' "$cur" >"$base/current.tmp.$$"
    mv -f -- "$base/current.tmp.$$" "$base/current"
  fi
  touch "$base/$cur/.last-use"
  # The stages bind-mount these two at /cache/gobuild and /cache/golangci
  # (scripts/in-toolchain-image.sh) and GOCACHE/GOLANGCI_LINT_CACHE point
  # there: the generation has them before any stage runs, whatever the root.
  mkdir -p "$base/$cur/gobuild" "$base/$cur/golangci"
  for e in "$base"/* "$base"/.[!.]*; do
    [ -e "$e" ] || [ -L "$e" ] || continue
    name=${e##*/}
    case $name in
      .lock | current | trash-*) continue ;;
      "$cur") continue ;;
      current.tmp.*) ;;
    esac
    stale "$e" || continue
    mv -- "$e" "$base/trash-$name.$$" 2>/dev/null || true
  done
  echo "$cur"
}

set +e
if command -v flock >/dev/null 2>&1; then
  gen=$(
    set -euo pipefail
    exec 9>>"$base/.lock"
    flock 9
    select_locked
  )
else
  gen=$(
    set -euo pipefail
    select_locked
  )
fi
rc=$?
set -e
[ "$rc" = 0 ] && [ -n "$gen" ] || named_volumes "Could not select a generation in $base"

trash=()
for e in "$base"/trash-*; do
  [ -e "$e" ] || [ -L "$e" ] || continue
  trash+=("$e")
done
[ "${#trash[@]}" -eq 0 ] || clean "${trash[@]}"

echo "CI_CACHE_DIR=$base/$gen"
