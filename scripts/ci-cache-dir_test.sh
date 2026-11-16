#!/usr/bin/env bash
# Self-test for scripts/ci-cache-dir.sh (#1596): where each runner kind keeps
# the Go caches, the generations and leases that keep a mounted cache alive
# past the size cap, and cleanup of files a stage's uid owns. Runs on a temp
# CI_CACHE_ROOT; no Docker: CI_CACHE_RM_HELPER stands in for the privileged
# container. A file another uid owns is, to the runner's user, a file in a
# directory it cannot write: the tests make one with chmod a-w (as root, which
# writes anywhere, they chown to the stage uid instead).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-cache-dir.sh
D=$(mktemp -d)
trap 'chmod -R u+w "$D" 2>/dev/null; rm -rf "$D"' EXIT
fail=0
ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }
is_root=false
[ "$(id -u)" = 0 ] && is_root=true

# The privileged helper's stand-in: logs its arguments, then deletes them.
helper=$D/helper.sh
printf '#!/bin/sh\necho "$@" >>"%s/helper.log"\nchmod -R u+w "$@"\nrm -rf "$@"\n' "$D" >"$helper"
chmod +x "$helper"

# run VAR=value... [release]: the script under a clean environment; sets out,
# err, rc, and dir and lease (the CI_CACHE_DIR and CI_CACHE_LEASE it printed).
run() {
  local args=()
  rc=0
  if [ "${*: -1}" = release ]; then
    args=(release)
    set -- "${@:1:$#-1}"
  fi
  env -i PATH="$PATH" "$@" "$script" "${args[@]}" >"$D/out" 2>"$D/err" || rc=$?
  out=$(cat "$D/out")
  err=$(cat "$D/err")
  dir=$(printf '%s\n' "$out" | while IFS= read -r l; do case $l in CI_CACHE_DIR=*) echo "${l#CI_CACHE_DIR=}" ;; esac; done)
  lease=$(printf '%s\n' "$out" | while IFS= read -r l; do case $l in CI_CACHE_LEASE=*) echo "${l#CI_CACHE_LEASE=}" ;; esac; done)
}

reset() {
  chmod -R u+w "$D/root" "$D/temp" 2>/dev/null
  rm -rf "$D/root" "$D/temp" "$D/helper.log"
  mkdir -p "$D/root" "$D/temp"
}

# owned_by_stage DIR: fills DIR with a file the runner's user cannot delete.
owned_by_stage() {
  mkdir -p "$1/00"
  echo entry >"$1/00/a"
  if $is_root; then chown -R 10001:10001 "$1"; else chmod a-w "$1/00"; fi
}

SH=(RUNNER_ENVIRONMENT=self-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root")

# 1. A hosted runner keeps the cache in its temp dir, whatever the root holds,
# with no lease.
reset
mkdir -p "$D/root/gh-runner-cache"
run RUNNER_ENVIRONMENT=github-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
if [ "$rc" = 0 ] && [ "$dir" = "$D/temp/ci-cache" ] && [ -z "$lease" ] &&
  [ -z "$(ls -A "$D/root/gh-runner-cache")" ]; then
  ok "a hosted runner uses \$RUNNER_TEMP/ci-cache"
else bad "a hosted runner uses \$RUNNER_TEMP/ci-cache (rc=$rc out=$out)"; fi

# 2. A self-hosted runner with the shared runner-cache root leases a
# generation under it.
reset
mkdir -p "$D/root/gh-runner-cache"
run "${SH[@]}"
base=$D/root/gh-runner-cache/go-qrdb/ci-cache
if [ "$rc" = 0 ] && [[ $dir == "$base"/gen.* ]] && [ -d "$dir" ] &&
  [[ $lease == "$dir"/.leases/* ]] && [ -f "$lease" ] &&
  [ "$(cat "$base/current")" = "${dir##*/}" ]; then
  ok "a self-hosted runner leases a generation in gh-runner-cache/go-qrdb/ci-cache"
else bad "a self-hosted runner leases a generation in gh-runner-cache/go-qrdb/ci-cache (rc=$rc out=$out err=$err)"; fi

# 3. Without gh-runner-cache, a writable root holds go-qrdb-ci-cache. An unset
# RUNNER_ENVIRONMENT is a self-hosted runner too.
reset
run RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
if [ "$rc" = 0 ] && [[ $dir == "$D/root/go-qrdb-ci-cache"/gen.* ]] && [ -f "$lease" ]; then
  ok "a self-hosted runner without gh-runner-cache uses go-qrdb-ci-cache"
else bad "a self-hosted runner without gh-runner-cache uses go-qrdb-ci-cache (rc=$rc out=$out)"; fi

# 4. Without the root, the cache is the named volumes (empty), never the temp
# dir, and the step says so.
reset
run RUNNER_ENVIRONMENT=self-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/absent"
if [ "$rc" = 0 ] && [ "$out" = $'CI_CACHE_DIR=\nCI_CACHE_LEASE=' ] && [[ $err == *::notice::* ]] &&
  [ -z "$(ls -A "$D/temp")" ]; then
  ok "a self-hosted runner without the root falls back to the named volumes"
else bad "a self-hosted runner without the root falls back to the named volumes (rc=$rc out=$out)"; fi

# 5. A read-only root is no root (skipped as root, who writes anywhere).
if ! $is_root; then
  reset
  chmod a-w "$D/root"
  run "${SH[@]}"
  if [ "$rc" = 0 ] && [ -z "$dir" ] && [ -z "$lease" ]; then
    ok "a read-only root falls back to the named volumes"
  else bad "a read-only root falls back to the named volumes (rc=$rc out=$out)"; fi
fi

# 6. Under the cap a second job shares the first job's generation, and the
# cache is kept.
reset
run "${SH[@]}" CI_CACHE_MAX_GB=40
dir1=$dir lease1=$lease
mkdir -p "$dir1/gobuild/00" "$dir1/golangci"
echo entry >"$dir1/gobuild/00/a"
run "${SH[@]}" CI_CACHE_MAX_GB=40
lease2=$lease
if [ "$rc" = 0 ] && [ "$dir" = "$dir1" ] && [ "$lease2" != "$lease1" ] &&
  [ -f "$dir1/gobuild/00/a" ] && [[ $err != *::notice::* ]]; then
  ok "a cache under the cap is shared and kept"
else bad "a cache under the cap is shared and kept (rc=$rc out=$out err=$err)"; fi

# 7. Over the cap the next job gets a new, empty generation, with a notice,
# and the generation a running job has mounted is left whole: its directory
# (the bind mount's inode) and its files stay. du rounds each directory up to
# 1 GB, so a cap of 0 is exceeded by any cache.
run "${SH[@]}" CI_CACHE_MAX_GB=0
dir3=$dir lease3=$lease
if [ "$rc" = 0 ] && [[ $dir3 == "$D/root/go-qrdb-ci-cache"/gen.* ]] && [ "$dir3" != "$dir1" ] &&
  [ -z "$(find "$dir3" -mindepth 1 -maxdepth 1 ! -name .leases)" ] && [[ $err == *::notice::*rotated* ]] &&
  [ -f "$dir1/gobuild/00/a" ]; then
  ok "a cache over the cap rotates to a new generation, the leased one kept"
else bad "a cache over the cap rotates to a new generation, the leased one kept (rc=$rc out=$out err=$err)"; fi

# 8. The old generation outlives the release of one of its two leases, and is
# reclaimed with the last; the current generation is never reclaimed.
run "${SH[@]}" CI_CACHE_LEASE="$lease1" release
if [ "$rc" = 0 ] && [ ! -e "$lease1" ] && [ -f "$dir1/gobuild/00/a" ]; then
  ok "a generation with a live lease survives another job's release"
else bad "a generation with a live lease survives another job's release (rc=$rc err=$err)"; fi
run "${SH[@]}" CI_CACHE_LEASE="$lease2" release
if [ "$rc" = 0 ] && [ ! -e "$dir1" ] && [ -d "$dir3" ] &&
  [ -z "$(ls -d "$D/root/go-qrdb-ci-cache"/trash.* 2>/dev/null)" ]; then
  ok "a generation is reclaimed when its last lease is released"
else bad "a generation is reclaimed when its last lease is released (rc=$rc err=$err)"; fi
run "${SH[@]}" CI_CACHE_LEASE="$lease3" release
if [ "$rc" = 0 ] && [ -d "$dir3" ] && [ ! -e "$lease3" ]; then
  ok "the current generation is kept when its leases are released"
else bad "the current generation is kept when its leases are released (rc=$rc err=$err)"; fi

# 9. A lease older than CI_CACHE_LEASE_MAX_MIN belongs to a job that is gone:
# its generation is reclaimed by the next job.
reset
run "${SH[@]}"
old=$dir
mkdir -p "$old/gobuild"
echo entry >"$old/gobuild/a"
run "${SH[@]}" CI_CACHE_MAX_GB=0
touch -d '7 hours ago' "$old"/.leases/*
run "${SH[@]}" CI_CACHE_LEASE_MAX_MIN=360
if [ "$rc" = 0 ] && [ ! -e "$old" ]; then
  ok "a generation whose leases are stale is reclaimed"
else bad "a generation whose leases are stale is reclaimed (rc=$rc err=$err)"; fi

# 10. A generation holding files the stage's uid owns is deleted by the
# privileged helper once plain rm cannot.
reset
run "${SH[@]}"
old=$dir oldlease=$lease
owned_by_stage "$old/gobuild"
run "${SH[@]}" CI_CACHE_MAX_GB=0
run "${SH[@]}" CI_CACHE_LEASE="$oldlease" CI_CACHE_RM_HELPER="$helper" release
if [ "$rc" = 0 ] && [ ! -e "$old" ] &&
  [ -z "$(ls -d "$D/root/go-qrdb-ci-cache"/trash.* 2>/dev/null)" ] &&
  { $is_root || [ -s "$D/helper.log" ]; }; then
  ok "a stage-owned generation is deleted through the privileged helper"
else bad "a stage-owned generation is deleted through the privileged helper (rc=$rc err=$err)"; fi

# 11. When no helper can delete it, the job still succeeds, with a warning, and
# the next job retries (skipped as root, whose plain rm cannot fail here).
if ! $is_root; then
  reset
  run "${SH[@]}"
  old=$dir oldlease=$lease
  owned_by_stage "$old/gobuild"
  run "${SH[@]}" CI_CACHE_MAX_GB=0
  run "${SH[@]}" CI_CACHE_LEASE="$oldlease" CI_CACHE_RM_HELPER=false release
  t=$(ls -d "$D/root/go-qrdb-ci-cache"/trash.* 2>/dev/null)
  if [ "$rc" = 0 ] && [[ $err == *::warning::* ]] && [ -n "$t" ]; then
    ok "a failed privileged delete warns and keeps the job green"
  else bad "a failed privileged delete warns and keeps the job green (rc=$rc err=$err)"; fi
  run "${SH[@]}" CI_CACHE_RM_HELPER="$helper"
  if [ "$rc" = 0 ] && [ -n "$dir" ] && [ -z "$(ls -d "$D/root/go-qrdb-ci-cache"/trash.* 2>/dev/null)" ]; then
    ok "the next job deletes the leftover trash"
  else bad "the next job deletes the leftover trash (rc=$rc err=$err)"; fi
fi

# 12. Release removes the legacy $RUNNER_TEMP/ci-cache, stage-owned files
# included, without a lease.
reset
owned_by_stage "$D/temp/ci-cache/gobuild"
run "${SH[@]}" CI_CACHE_RM_HELPER="$helper" release
if [ "$rc" = 0 ] && [ ! -e "$D/temp/ci-cache" ] && { $is_root || [ -s "$D/helper.log" ]; }; then
  ok "release removes a stage-owned legacy \$RUNNER_TEMP/ci-cache"
else bad "release removes a stage-owned legacy \$RUNNER_TEMP/ci-cache (rc=$rc err=$err)"; fi

# 12b. Without a release step, the job's first step removes the stage-owned
# legacy $RUNNER_TEMP/ci-cache, so the workflow's final plain rm -rf
# succeeds.
reset
owned_by_stage "$D/temp/ci-cache/gobuild"
run "${SH[@]}" CI_CACHE_RM_HELPER="$helper"
if [ "$rc" = 0 ] && [ -n "$dir" ] && [ ! -e "$D/temp/ci-cache" ] &&
  rm -rf "$D/temp/ci-cache" && { $is_root || [ -s "$D/helper.log" ]; }; then
  ok "acquire removes a stage-owned legacy \$RUNNER_TEMP/ci-cache"
else bad "acquire removes a stage-owned legacy \$RUNNER_TEMP/ci-cache (rc=$rc err=$err)"; fi

# 13. A cache from before the generations (gobuild directly under the base) is
# reclaimed, and the job gets a generation.
reset
mkdir -p "$D/root/go-qrdb-ci-cache/gobuild/00"
echo entry >"$D/root/go-qrdb-ci-cache/gobuild/00/a"
run "${SH[@]}"
if [ "$rc" = 0 ] && [[ $dir == "$D/root/go-qrdb-ci-cache"/gen.* ]] &&
  [ ! -e "$D/root/go-qrdb-ci-cache/gobuild" ]; then
  ok "a pre-generation cache is reclaimed"
else bad "a pre-generation cache is reclaimed (rc=$rc out=$out err=$err)"; fi

# 14. A lease that is not under the base is refused with a warning; nothing
# is deleted.
reset
run "${SH[@]}"
keep=$lease
run "${SH[@]}" CI_CACHE_LEASE="$D/temp/elsewhere" release
if [ "$rc" = 0 ] && [[ $err == *::warning::* ]] && [ -f "$keep" ]; then
  ok "a lease outside the base is refused"
else bad "a lease outside the base is refused (rc=$rc err=$err)"; fi

# 15. Concurrent jobs: every one gets a leased generation that exists.
reset
for i in 1 2 3 4 5 6 7 8; do
  (env -i PATH="$PATH" "${SH[@]}" CI_CACHE_MAX_GB=0 "$script" >"$D/c$i.out" 2>/dev/null) &
done
wait
cok=true
for i in 1 2 3 4 5 6 7 8; do
  cd_=
  IFS= read -r cd_ <"$D/c$i.out" || true
  cd_=${cd_#CI_CACHE_DIR=}
  [[ $cd_ == "$D/root/go-qrdb-ci-cache"/gen.* ]] && [ -d "$cd_" ] || cok=false
done
if $cok; then
  ok "concurrent jobs each keep a leased generation"
else bad "concurrent jobs each keep a leased generation"; fi

# 16. A cap that is not a number is refused, and so is a zero lease age.
run "${SH[@]}" CI_CACHE_MAX_GB=4x
if [ "$rc" = 2 ] && [ -z "$out" ]; then
  ok "a non-numeric cap is refused"
else bad "a non-numeric cap is refused (rc=$rc out=$out)"; fi
run "${SH[@]}" CI_CACHE_LEASE_MAX_MIN=0
if [ "$rc" = 2 ] && [ -z "$out" ]; then
  ok "a zero lease age is refused"
else bad "a zero lease age is refused (rc=$rc out=$out)"; fi

# 17. A hosted runner without RUNNER_TEMP is refused, not pointed at /ci-cache.
run RUNNER_ENVIRONMENT=github-hosted CI_CACHE_ROOT="$D/root"
if [ "$rc" = 2 ] && [ -z "$out" ]; then
  ok "a hosted runner without RUNNER_TEMP is refused"
else bad "a hosted runner without RUNNER_TEMP is refused (rc=$rc out=$out)"; fi

if [ "$fail" != 0 ]; then
  echo "ci-cache-dir_test: FAILED"
  exit 1
fi
echo "ci-cache-dir_test: all passed"
