#!/usr/bin/env bash
# Self-test for scripts/ci-cache-dir.sh (#1596): where each runner kind keeps
# the Go caches, and the size cap. Runs on a temp CI_CACHE_ROOT; no Docker.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-cache-dir.sh
D=$(mktemp -d)
trap 'chmod -R u+w "$D" 2>/dev/null; rm -rf "$D"' EXIT
fail=0
ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }

# run VAR=value...: the script under a clean environment; sets out, err, rc.
run() {
  rc=0
  env -i PATH="$PATH" "$@" "$script" >"$D/out" 2>"$D/err" || rc=$?
  out=$(cat "$D/out")
  err=$(cat "$D/err")
}

reset() {
  chmod -R u+w "$D/root" 2>/dev/null
  rm -rf "$D/root" "$D/temp"
  mkdir -p "$D/root" "$D/temp"
}

# 1. A hosted runner keeps the cache in its temp dir, whatever the root holds.
reset
mkdir -p "$D/root/gh-runner-cache"
run RUNNER_ENVIRONMENT=github-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=$D/temp/ci-cache" ]; then
  ok "a hosted runner uses \$RUNNER_TEMP/ci-cache"
else bad "a hosted runner uses \$RUNNER_TEMP/ci-cache (rc=$rc out=$out)"; fi

# 2. A self-hosted runner with the shared runner-cache root uses it.
reset
mkdir -p "$D/root/gh-runner-cache"
run RUNNER_ENVIRONMENT=self-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
want=$D/root/gh-runner-cache/go-qrdb/ci-cache
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=$want" ] && [ -d "$want" ]; then
  ok "a self-hosted runner uses gh-runner-cache/go-qrdb/ci-cache"
else bad "a self-hosted runner uses gh-runner-cache/go-qrdb/ci-cache (rc=$rc out=$out)"; fi

# 3. Without gh-runner-cache, a writable root holds go-qrdb-ci-cache. An unset
# RUNNER_ENVIRONMENT is a self-hosted runner too.
reset
run RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
want=$D/root/go-qrdb-ci-cache
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=$want" ] && [ -d "$want" ]; then
  ok "a self-hosted runner without gh-runner-cache uses go-qrdb-ci-cache"
else bad "a self-hosted runner without gh-runner-cache uses go-qrdb-ci-cache (rc=$rc out=$out)"; fi

# 4. Without the root, the cache is the named volumes (empty), never the temp
# dir, and the step says so.
reset
run RUNNER_ENVIRONMENT=self-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/absent"
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=" ] && [[ $err == *::notice::* ]] &&
  [ -z "$(ls -A "$D/temp")" ]; then
  ok "a self-hosted runner without the root falls back to the named volumes"
else bad "a self-hosted runner without the root falls back to the named volumes (rc=$rc out=$out)"; fi

# 5. A read-only root is no root (skipped as root, who writes anywhere).
if [ "$(id -u)" != 0 ]; then
  reset
  chmod a-w "$D/root"
  run RUNNER_ENVIRONMENT=self-hosted RUNNER_TEMP="$D/temp" CI_CACHE_ROOT="$D/root"
  if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=" ]; then
    ok "a read-only root falls back to the named volumes"
  else bad "a read-only root falls back to the named volumes (rc=$rc out=$out)"; fi
fi

# 6. Under the cap the cache is kept.
reset
want=$D/root/go-qrdb-ci-cache
mkdir -p "$want/gobuild/00" "$want/golangci"
echo entry >"$want/gobuild/00/a"
run RUNNER_ENVIRONMENT=self-hosted CI_CACHE_ROOT="$D/root" CI_CACHE_MAX_GB=40
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=$want" ] && [ -f "$want/gobuild/00/a" ] &&
  [[ $err != *::notice::* ]]; then
  ok "a cache under the cap is kept"
else bad "a cache under the cap is kept (rc=$rc out=$out err=$err)"; fi

# 7. Over the cap the cache is emptied, with a notice. du rounds each
# directory up to 1 GB, so a cap of 0 is exceeded by any cache.
run RUNNER_ENVIRONMENT=self-hosted CI_CACHE_ROOT="$D/root" CI_CACHE_MAX_GB=0
if [ "$rc" = 0 ] && [ "$out" = "CI_CACHE_DIR=$want" ] && [ -d "$want" ] &&
  [ -z "$(ls -A "$want")" ] && [[ $err == *::notice::*cleared* ]]; then
  ok "a cache over the cap is cleared with a notice"
else bad "a cache over the cap is cleared with a notice (rc=$rc out=$out err=$err)"; fi

# 8. A cap that is not a number is refused.
run RUNNER_ENVIRONMENT=self-hosted CI_CACHE_ROOT="$D/root" CI_CACHE_MAX_GB=4x
if [ "$rc" = 2 ] && [ -z "$out" ]; then
  ok "a non-numeric cap is refused"
else bad "a non-numeric cap is refused (rc=$rc out=$out)"; fi

# 9. A hosted runner without RUNNER_TEMP is refused, not pointed at /ci-cache.
run RUNNER_ENVIRONMENT=github-hosted CI_CACHE_ROOT="$D/root"
if [ "$rc" = 2 ] && [ -z "$out" ]; then
  ok "a hosted runner without RUNNER_TEMP is refused"
else bad "a hosted runner without RUNNER_TEMP is refused (rc=$rc out=$out)"; fi

if [ "$fail" != 0 ]; then
  echo "ci-cache-dir_test: FAILED"
  exit 1
fi
echo "ci-cache-dir_test: all passed"
