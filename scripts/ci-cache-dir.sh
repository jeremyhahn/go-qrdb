#!/usr/bin/env bash
# CUSTOMIZE (go-qrdb, #1596): where a CI job keeps its Go build and
# golangci-lint caches. Prints one line, CI_CACHE_DIR=<dir>, for "$GITHUB_ENV";
# scripts/in-toolchain-image.sh bind-mounts <dir>/gobuild and <dir>/golangci,
# or, when <dir> is empty, uses its named Docker volumes.
#
#   - A hosted runner (RUNNER_ENVIRONMENT=github-hosted) is thrown away after
#     the job: the cache is $RUNNER_TEMP/ci-cache, which actions/cache
#     restores and saves.
#   - A self-hosted pool host keeps its disk: $RUNNER_TEMP is on the root
#     filesystem and outlives the job, and the cache there grew to 168 GB
#     (#1596). The cache is a persistent directory on the data array instead:
#       $CI_CACHE_ROOT/gh-runner-cache/go-qrdb/ci-cache  when
#           $CI_CACHE_ROOT/gh-runner-cache is a writable directory, else
#       $CI_CACHE_ROOT/go-qrdb-ci-cache                  when $CI_CACHE_ROOT
#           is a writable directory, else
#       empty (the named volumes), never $RUNNER_TEMP.
#     CI_CACHE_ROOT defaults to /mnt/raid0. When gobuild and golangci together
#     exceed CI_CACHE_MAX_GB (default 40, du -s --block-size=1G, which rounds
#     each up), the directory is emptied first, with a ::notice::.
#
# Notices go to stderr: stdout is the environment file.
set -euo pipefail

if [ "${RUNNER_ENVIRONMENT:-}" = github-hosted ]; then
  if [ -z "${RUNNER_TEMP:-}" ]; then
    echo "ci-cache-dir: RUNNER_TEMP is unset on a hosted runner" >&2
    exit 2
  fi
  echo "CI_CACHE_DIR=$RUNNER_TEMP/ci-cache"
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

writable_dir() { [ -d "$1" ] && [ -w "$1" ]; }

dir=
if writable_dir "$root/gh-runner-cache"; then
  dir=$root/gh-runner-cache/go-qrdb/ci-cache
elif writable_dir "$root"; then
  dir=$root/go-qrdb-ci-cache
fi

if [ -z "$dir" ] || ! mkdir -p "$dir" 2>/dev/null || ! writable_dir "$dir"; then
  echo "::notice::No writable cache directory under $root: the Go caches are the stage's named Docker volumes" >&2
  echo "CI_CACHE_DIR="
  exit 0
fi

used=0
for sub in gobuild golangci; do
  [ -e "$dir/$sub" ] || continue
  gb=$(du -s --block-size=1G "$dir/$sub" 2>/dev/null | cut -f1) || gb=
  case $gb in '' | *[!0-9]*) gb=0 ;; esac
  used=$((used + gb))
done

if [ "$used" -gt "$cap" ]; then
  echo "::notice::The Go caches in $dir hold ${used} GB, over the ${cap} GB cap: cleared" >&2
  if ! find "$dir" -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null; then
    echo "::warning::Could not clear $dir: the Go caches are the stage's named Docker volumes" >&2
    echo "CI_CACHE_DIR="
    exit 0
  fi
fi

echo "CI_CACHE_DIR=$dir"
