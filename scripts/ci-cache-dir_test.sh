#!/usr/bin/env bash
# Self-test for scripts/ci-cache-dir.sh and scripts/ci-cache-clean.sh (#1596),
# run by `make test-in-toolchain-image`. Every case works on a temp
# CI_CACHE_ROOT. Stage-owned files are stood in for by read-only directories
# (a non-root runner cannot delete their contents) and the privileged helper
# by a fake `docker` on PATH; the real helper runs on a uid-10001 fixture only
# when docker and a local image are available, and is skipped with a message
# otherwise.
# shellcheck disable=SC2016,SC2034 # check() evals its single-quoted condition
set -uo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SH=$here/ci-cache-dir.sh
CLEAN=$here/ci-cache-clean.sh
fail=0
ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

tmp=$(mktemp -d)
cleanup() { chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"; }
trap cleanup EXIT

# run VAR=VAL... -- CMD...: runs CMD with a clean cache environment, leaving
# stdout in $out, stderr in $err and the status in $rc.
run() {
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  out=$(env -u RUNNER_ENVIRONMENT -u RUNNER_TEMP -u CI_CACHE_ROOT \
    -u CI_CACHE_MAX_GB -u CI_CACHE_RECLAIM_HOURS -u TOOLCHAIN_IMAGE \
    -u CI_CACHE_CLEAN_IMAGE -u CI_IMAGE_IDS "${envs[@]}" "$@" 2>"$tmp/err")
  rc=$?
  err=$(cat "$tmp/err")
}

age() { touch -d "@$(($(date +%s) - $1 * 3600))" "$2"; }

# stage_owned DIR: a tree the runner's (non-root) user cannot delete.
stage_owned() {
  mkdir -p "$1/sub"
  echo x >"$1/sub/f"
  chmod 0555 "$1/sub"
}

# The fake privileged helper: a `docker` that empties the -v source as the
# real one does as root, or fails when FAKE_DOCKER_FAIL is set.
fake=$tmp/fakebin
mkdir -p "$fake"
cat >"$fake/docker" <<'EOF'
#!/usr/bin/env bash
[ -z "${FAKE_DOCKER_FAIL:-}" ] || exit 1
src= image=
while [ $# -gt 0 ]; do
  case $1 in
    -v) src=${2%%:*}; shift ;;
    --entrypoint) image=${3:-}; shift ;;
  esac
  shift
done
[ -n "$src" ] || exit 2
echo "$src" >>"${FAKE_DOCKER_LOG:-/dev/null}"
echo "$image" >>"${FAKE_DOCKER_IMAGE_LOG:-/dev/null}"
chmod -R u+w "$src" && find "$src" -mindepth 1 -delete
EOF
chmod +x "$fake/docker"

# --- hosted runner -----------------------------------------------------------
run RUNNER_ENVIRONMENT=github-hosted RUNNER_TEMP="$tmp/rt" -- bash "$SH"
check "hosted: \$RUNNER_TEMP/ci-cache" '[ $rc = 0 ] && [ "$out" = "CI_CACHE_DIR=$tmp/rt/ci-cache" ]'
run RUNNER_ENVIRONMENT=github-hosted -- bash "$SH"
check "hosted: no RUNNER_TEMP exits 2" '[ $rc = 2 ]'

# --- argument checks ---------------------------------------------------------
run CI_CACHE_ROOT="$tmp" CI_CACHE_MAX_GB=x -- bash "$SH"
check "bad CI_CACHE_MAX_GB exits 2" '[ $rc = 2 ]'
run CI_CACHE_ROOT="$tmp" CI_CACHE_RECLAIM_HOURS=0 -- bash "$SH"
check "bad CI_CACHE_RECLAIM_HOURS exits 2" '[ $rc = 2 ]'

# --- base selection ----------------------------------------------------------
run CI_CACHE_ROOT="$tmp/missing" -- bash "$SH"
check "no writable root: named volumes" '[ $rc = 0 ] && [ "$out" = "CI_CACHE_DIR=" ]'

r1=$tmp/r1
mkdir -p "$r1"
run CI_CACHE_ROOT="$r1" -- bash "$SH"
base=$r1/go-qrdb-ci-cache
g1=$(cat "$base/current" 2>/dev/null)
check "root: base go-qrdb-ci-cache, a gen- generation" \
  '[ $rc = 0 ] && case $g1 in gen-*) true ;; *) false ;; esac && [ "$out" = "CI_CACHE_DIR=$base/$g1" ]'
check "the selected generation has a last-use marker" '[ -f "$base/$g1/.last-use" ]'

r2=$tmp/r2
mkdir -p "$r2/gh-runner-cache"
run CI_CACHE_ROOT="$r2" -- bash "$SH"
check "gh-runner-cache: base under it" \
  '[ $rc = 0 ] && case $out in "CI_CACHE_DIR=$r2/gh-runner-cache/go-qrdb/ci-cache/gen-"*) true ;; *) false ;; esac'

# --- reuse, rotation, reclaim ------------------------------------------------
age 1 "$base/$g1/.last-use"
run CI_CACHE_ROOT="$r1" -- bash "$SH"
check "under the cap: the same generation" '[ "$out" = "CI_CACHE_DIR=$base/$g1" ]'
check "selection refreshes the marker" '[ -z "$(find "$base/$g1/.last-use" -mmin +5)" ]'

mkdir -p "$base/$g1/gobuild"
echo x >"$base/$g1/gobuild/f"
run CI_CACHE_ROOT="$r1" CI_CACHE_MAX_GB=0 -- bash "$SH"
g2=$(cat "$base/current" 2>/dev/null)
check "over the cap: a new generation is current" \
  '[ $rc = 0 ] && [ "$g2" != "$g1" ] && [ "$out" = "CI_CACHE_DIR=$base/$g2" ] && [[ $err == *"over the 0 GB cap"* ]]'
check "over the cap: the old generation is kept while recent" '[ -f "$base/$g1/gobuild/f" ]'
check "no temp current file is left" '! ls "$base"/current.tmp.* >/dev/null 2>&1'

age 6 "$base/$g1/.last-use"
run CI_CACHE_ROOT="$r1" -- bash "$SH"
check "reclaim: kept under CI_CACHE_RECLAIM_HOURS" '[ -d "$base/$g1" ] && [ "$out" = "CI_CACHE_DIR=$base/$g2" ]'

age 8 "$base/$g1/.last-use"
run CI_CACHE_ROOT="$r1" -- bash "$SH"
check "reclaim: deleted past CI_CACHE_RECLAIM_HOURS" '[ ! -e "$base/$g1" ] && ! ls -d "$base"/trash-* >/dev/null 2>&1'

age 8 "$base/$g2/.last-use"
run CI_CACHE_ROOT="$r1" CI_CACHE_RECLAIM_HOURS=1 -- bash "$SH"
check "the current generation is never deleted, however old" \
  '[ -d "$base/$g2" ] && [ "$out" = "CI_CACHE_DIR=$base/$g2" ] && [ -z "$(find "$base/$g2/.last-use" -mmin +5)" ]'

# --- pre-generation layout ---------------------------------------------------
mkdir -p "$base/gobuild" "$base/golangci"
age 8 "$base/golangci"
run CI_CACHE_ROOT="$r1" -- bash "$SH"
check "pre-generation dirs: old deleted, recent kept" '[ ! -e "$base/golangci" ] && [ -d "$base/gobuild" ]'

# --- du failures --------------------------------------------------------------
# A du that exits nonzero (pipefail, a partial read, an entry vanishing under
# it) must never read as 0 GB: a number it printed is used, and no number at
# all counts as over the cap.
fakedu=$tmp/fakedu
mkdir -p "$fakedu"
cat >"$fakedu/du" <<'EOF'
#!/usr/bin/env bash
for a; do p=$a; done
[ -z "${FAKE_DU_GB:-}" ] || printf '%s\t%s\n' "$FAKE_DU_GB" "$p"
exit 1
EOF
chmod +x "$fakedu/du"
r4=$tmp/r4
mkdir -p "$r4"
run CI_CACHE_ROOT="$r4" -- bash "$SH"
b4=$r4/go-qrdb-ci-cache
h1=$(cat "$b4/current" 2>/dev/null)
mkdir -p "$b4/$h1/gobuild"
run CI_CACHE_ROOT="$r4" CI_CACHE_MAX_GB=5 PATH="$fakedu:$PATH" -- bash "$SH"
h2=$(cat "$b4/current" 2>/dev/null)
check "du fails, prints nothing: warns, a new generation is current" \
  '[ $rc = 0 ] && [ "$h2" != "$h1" ] && [ "$out" = "CI_CACHE_DIR=$b4/$h2" ] && [[ $err == *"::warning::"* ]]'
mkdir -p "$b4/$h2/gobuild"
run CI_CACHE_ROOT="$r4" CI_CACHE_MAX_GB=5 FAKE_DU_GB=9 PATH="$fakedu:$PATH" -- bash "$SH"
h3=$(cat "$b4/current" 2>/dev/null)
check "du fails, prints a size over the cap: a new generation is current" \
  '[ $rc = 0 ] && [ "$h3" != "$h2" ] && [ "$out" = "CI_CACHE_DIR=$b4/$h3" ] && [[ $err == *"9 GB, over the 5 GB cap"* ]]'
mkdir -p "$b4/$h3/gobuild"
run CI_CACHE_ROOT="$r4" CI_CACHE_MAX_GB=5 FAKE_DU_GB=2 PATH="$fakedu:$PATH" -- bash "$SH"
check "du fails, prints a size under the cap: the same generation" \
  '[ $rc = 0 ] && [ "$(cat "$b4/current")" = "$h3" ] && [ "$out" = "CI_CACHE_DIR=$b4/$h3" ]'

# --- legacy temp cache, stage-owned files -------------------------------------
if [ "$(id -u)" = 0 ]; then
  echo "skip stage-owned stand-ins: running as root, read-only dirs do not stop rm"
else
  rt=$tmp/rt2
  stage_owned "$rt/ci-cache"
  run CI_CACHE_ROOT="$r1" RUNNER_TEMP="$rt" -- bash "$SH"
  check "legacy temp cache, no helper: warns, job not failed" \
    '[ $rc = 0 ] && [ -e "$rt/ci-cache" ] && [[ $err == *"::warning::"* ]] && [ "$out" = "CI_CACHE_DIR=$base/$g2" ]'
  run CI_CACHE_ROOT="$r1" RUNNER_TEMP="$rt" CI_CACHE_CLEAN_IMAGE=img PATH="$fake:$PATH" -- bash "$SH"
  check "legacy temp cache: deleted through the helper" '[ $rc = 0 ] && [ ! -e "$rt/ci-cache" ]'

  stage_owned "$base/$g2/gobuild"
  run CI_CACHE_ROOT="$r1" CI_CACHE_MAX_GB=0 -- bash "$SH"
  g3=$(cat "$base/current")
  age 8 "$base/$g2/.last-use"
  run CI_CACHE_ROOT="$r1" CI_CACHE_CLEAN_IMAGE=img PATH="$fake:$PATH" -- bash "$SH"
  check "a stage-owned old generation is reclaimed through the helper" \
    '[ $rc = 0 ] && [ ! -e "$base/$g2" ] && [ "$out" = "CI_CACHE_DIR=$base/$g3" ]'

  # The helper alone, as ci.yml's cleanup steps call it.
  d=$tmp/clean1
  stage_owned "$d"
  run -- bash "$CLEAN" "$d" "$tmp/absent"
  check "clean: no docker image, plain rm, warns, exit 0" '[ $rc = 0 ] && [ -e "$d" ] && [[ $err == *"::warning::"* ]]'
  run TOOLCHAIN_IMAGE=img PATH="$fake:$PATH" FAKE_DOCKER_LOG="$tmp/dlog" -- bash "$CLEAN" "$d"
  check "clean: TOOLCHAIN_IMAGE helper deletes, mounts the dir" \
    '[ $rc = 0 ] && [ ! -e "$d" ] && [ -z "$err" ] && [ "$(cat "$tmp/dlog")" = "$d" ]'
  stage_owned "$d"
  run TOOLCHAIN_IMAGE=img PATH="$fake:$PATH" FAKE_DOCKER_FAIL=1 -- bash "$CLEAN" "$d"
  check "clean: helper failure warns, exit 0" '[ $rc = 0 ] && [ -e "$d" ] && [[ $err == *"::warning::"* ]]'
fi

# --- the helper runs the verified image ID, never the tag ----------------------
# scripts/ci-image.sh load records the ID it verified for each tag; another job
# on a shared daemon can retag the tag afterwards. The helper must run the
# recorded ID, so a retag (here: anything the tag now names) changes nothing.
id_rec=sha256:$(printf '%064d' 7)
ids=$tmp/ids
mkdir -p "$ids"
printf '%s\n' "$id_rec" >"$ids/img"
d=$tmp/clean-id
mkdir -p "$d/sub"
: >"$tmp/ilog"
run TOOLCHAIN_IMAGE=img CI_IMAGE_IDS="$ids" PATH="$fake:$PATH" FAKE_DOCKER_IMAGE_LOG="$tmp/ilog" -- bash "$CLEAN" "$d"
check "clean: a retagged tag is not run, the recorded ID is" \
  '[ $rc = 0 ] && [ ! -e "$d" ] && [ "$(cat "$tmp/ilog")" = "$id_rec" ]'
mkdir -p "$d/sub" "$tmp/rt3/ci-image-ids"
printf '%s\n' "$id_rec" >"$tmp/rt3/ci-image-ids/img"
: >"$tmp/ilog"
run CI_CACHE_CLEAN_IMAGE=img RUNNER_TEMP="$tmp/rt3" PATH="$fake:$PATH" FAKE_DOCKER_IMAGE_LOG="$tmp/ilog" -- bash "$CLEAN" "$d"
check "clean: RUNNER_TEMP/ci-image-ids record runs the ID" \
  '[ $rc = 0 ] && [ ! -e "$d" ] && [ "$(cat "$tmp/ilog")" = "$id_rec" ]'
mkdir -p "$d/sub"
printf 'img\n' >"$ids/img"
: >"$tmp/ilog"
run TOOLCHAIN_IMAGE=img CI_IMAGE_IDS="$ids" PATH="$fake:$PATH" FAKE_DOCKER_IMAGE_LOG="$tmp/ilog" -- bash "$CLEAN" "$d"
check "clean: a record that is not an image ID is refused, no helper, exit 0" \
  '[ $rc = 0 ] && [ ! -s "$tmp/ilog" ] && [[ $err == *"not an image ID"* ]]'

# --- the real helper on a root-owned fixture ----------------------------------
img=${CI_CACHE_TEST_IMAGE:-${TOOLCHAIN_IMAGE:-}}
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "skip real helper: docker is not available"
elif [ -z "$img" ] || ! docker image inspect "$img" >/dev/null 2>&1; then
  echo "skip real helper: no local image (set CI_CACHE_TEST_IMAGE or TOOLCHAIN_IMAGE)"
else
  d=$tmp/real
  mkdir -p "$d"
  docker run --rm --user 0:0 -v "$d:/c" --entrypoint sh "$img" -c \
    'mkdir -p /c/a/b && echo x >/c/a/b/f && chmod 0555 /c/a/b && chown -R 10001:10001 /c/a' >/dev/null 2>&1
  if [ ! -e "$d/a/b/f" ]; then
    bad "real helper: could not build the uid-10001 fixture"
  else
    run TOOLCHAIN_IMAGE="$img" -- bash "$CLEAN" "$d"
    check "real helper: uid-10001 tree deleted" '[ $rc = 0 ] && [ ! -e "$d" ]'
  fi
fi

if [ "$fail" != 0 ]; then
  echo "ci-cache-dir_test: FAIL"
  exit 1
fi
echo "ci-cache-dir_test: ok"
