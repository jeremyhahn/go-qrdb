#!/usr/bin/env bash
# Tests for scripts/trim-ci-caches.sh (#1046). A stub `docker` on PATH keeps
# each volume as a directory under $D/vols and runs the script's `du` and
# `find` on it; a file's content is its size in KiB, which the stub's `du`
# adds up, so a test sets a size in GiB without writing one.
#
#   usage: scripts/trim-ci-caches_test.sh   (also: make test-trim-ci-caches)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
mkdir -p "$D/bin"

cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -eu
vols=$STUB_VOLS
case $1 in
  volume)
    case $2 in
      ls) for v in "$vols"/*; do [ -d "$v" ] && basename "$v"; done ;;
      rm) [ -e "$STUB_STATE/inuse-$3" ] && exit 1; rm -rf "${vols:?}/$3" ;;
    esac
    exit 0 ;;
  ps)
    # docker ps -aq --filter volume=<v>
    v=${4#volume=}
    [ -e "$STUB_STATE/inuse-$v" ] && echo c0ffee
    exit 0 ;;
  run) shift ;;
  *) exit 1 ;;
esac
entry='' maps=()
while [ $# -gt 0 ]; do
  case $1 in
    -v) maps+=("$2"); shift 2 ;;
    --entrypoint) entry=$2; shift 2 ;;
    --user | --network | --cap-drop | --security-opt) shift 2 ;;
    --rm) shift ;;
    *) break ;;
  esac
done
shift # the image
echo "$entry" >> "$STUB_STATE/runs"
case $entry in
  du)
    v=${maps[0]%%:*}
    [ -d "$vols/$v" ] || exit 1
    n=0
    for f in $(find "$vols/$v" -type f); do n=$((n + $(cat "$f"))); done
    echo "$n	/c" ;;
  find)
    [ "${STUB_FIND_FAILS:-0}" = 1 ] && exit 1
    shift # /c
    for m in "${maps[@]}"; do
      v=${m%%:*}
      echo "$v" >> "$STUB_STATE/evicted-from"
      find "$vols/$v" "$@"
    done ;;
esac
STUB
chmod +x "$D/bin/docker"

GIB=$((1024 * 1024))
# entry <volume> <file> <GiB> <minutes ago>
entry() {
  mkdir -p "$D/vols/$1"
  echo $(($3 * GIB)) > "$D/vols/$1/$2"
  touch -d "@$(($(date +%s) - $4 * 60))" "$D/vols/$1/$2"
}
reset() {
  rm -rf "$D/vols" "$D/state"
  mkdir -p "$D/vols" "$D/state"
}
trim() {
  PATH="$D/bin:$PATH" STUB_VOLS="$D/vols" STUB_STATE="$D/state" \
    CI_CACHE_LOCK="${CI_CACHE_LOCK:-$D/lock}" \
    "$here/trim-ci-caches.sh" p- img "$@" > "$D/out" 2>&1
}
has() { [ -e "$D/vols/$1/$2" ]; }

P=p-cache-main

# 1. Under the bound: nothing is evicted or removed, and no find runs.
reset
entry $P-gobuild old 4 10000
entry $P-gomod m 4 10000
trim 32
if has $P-gobuild old && ! grep -qx find "$D/state/runs"; then
  ok "under the bound nothing is touched"
else bad "under the bound nothing is touched"; fi

# 2. Over the bound: the entries unused longest go first, and eviction stops
# once the total is under it; the volume stays.
reset
entry $P-gobuild d5 10 7200   # five days
entry $P-gobuild d2 10 2880   # two days
entry $P-gobuild h1 10 60     # one hour
entry $P-golangci d4 4 5760   # four days
trim 25
if ! has $P-gobuild d5 && ! has $P-golangci d4 && has $P-gobuild d2 &&
  has $P-gobuild h1 && grep -q '^trim: 0 volume' "$D/out"; then
  ok "eviction by age stops under the bound and keeps the volumes"
else bad "eviction by age stops under the bound and keeps the volumes"; cat "$D/out"; fi

# 3. Steps continue down to three hours, never further.
reset
entry $P-gobuild h4 10 240
entry $P-gobuild h2 10 120
trim 5
if ! has $P-gobuild h4 && [ "$(grep -c '^evict entries' "$D/out")" = 5 ]; then
  ok "the steps go down to three hours"
else bad "the steps go down to three hours"; cat "$D/out"; fi

# 4. ... and when that is not enough the largest unused volume goes, as before.
if [ ! -d "$D/vols/$P-gobuild" ] && grep -q "^trim  $P-gobuild" "$D/out"; then
  ok "whole-volume removal is the fallback"
else bad "whole-volume removal is the fallback"; cat "$D/out"; fi

# 5. The module cache is never evicted from, only removed whole.
reset
entry $P-gomod old 20 10000
entry $P-gobuild new 1 10
trim 10
if ! grep -q gomod "$D/state/evicted-from" 2>/dev/null &&
  grep -q "^trim  $P-gomod" "$D/out"; then
  ok "the module cache is not evicted from"
else bad "the module cache is not evicted from"; cat "$D/out"; fi

# 6. A volume a container mounts is neither evicted from nor removed.
reset
entry $P-gobuild old 20 10000
: > "$D/state/inuse-$P-gobuild"
trim 10
if has $P-gobuild old && grep -q "^keep  $P-gobuild" "$D/out"; then
  ok "an in-use volume is left alone"
else bad "an in-use volume is left alone"; cat "$D/out"; fi

# 7. REAP_DRY_RUN=1 deletes nothing.
reset
entry $P-gobuild old 20 10000
REAP_DRY_RUN=1 trim 10
if has $P-gobuild old && grep -q '^dry   evict' "$D/out" &&
  grep -q "^dry   docker volume rm $P-gobuild" "$D/out"; then
  ok "a dry run deletes nothing"
else bad "a dry run deletes nothing"; cat "$D/out"; fi

# 8. A failed eviction is reported, the fallback still runs, and the job passes.
reset
entry $P-gobuild old 20 10000
if STUB_FIND_FAILS=1 trim 10 && grep -q '^warn  eviction' "$D/out" &&
  [ ! -d "$D/vols/$P-gobuild" ]; then
  ok "a failed eviction falls back and exits 0"
else bad "a failed eviction falls back and exits 0"; cat "$D/out"; fi

# 9. A trim takes the host lock: one that ran left its lock file behind.
if [ -e "$D/lock" ]; then
  ok "a trim takes the host lock"
else bad "a trim takes the host lock"; fi

# 10. While another job holds the lock past CI_CACHE_LOCK_WAIT, the trim is
# skipped with one line, touches nothing and exits 0 (#1275).
reset
entry $P-gobuild old 20 10000
exec 8>>"$D/lock"
flock 8
if CI_CACHE_LOCK_WAIT=0 trim 10 && grep -q '^trim: skipped, another job held' "$D/out" &&
  has $P-gobuild old && [ ! -e "$D/state/runs" ]; then
  ok "a held lock skips the trim and exits 0"
else bad "a held lock skips the trim and exits 0"; cat "$D/out"; fi

# 11. Once the holder lets go, the next trim runs.
exec 8>&-
if CI_CACHE_LOCK_WAIT=0 trim 10 && ! has $P-gobuild old; then
  ok "a released lock lets the trim run"
else bad "a released lock lets the trim run"; cat "$D/out"; fi

# 12. A lock wait that is not a number is refused.
if ! CI_CACHE_LOCK_WAIT=2m trim 10 && grep -q 'CI_CACHE_LOCK_WAIT must be a number' "$D/out"; then
  ok "a bad lock wait is refused"
else bad "a bad lock wait is refused"; cat "$D/out"; fi

# 13. The toolchain image's BusyBox flock rejects -w ("flock: unrecognized
# option: w"); with it on PATH a free lock is still taken and the trim runs
# (#1275, run 75927).
reset
entry $P-gobuild old 20 10000
mkdir -p "$D/busybox"
cat > "$D/busybox/flock" <<STUB
#!/usr/bin/env bash
for a; do
  case \$a in -w*) echo 'flock: unrecognized option: w' >&2; exit 1 ;; esac
done
exec $(command -v flock) "\$@"
STUB
chmod +x "$D/busybox/flock"
if PATH="$D/busybox:$PATH" CI_CACHE_LOCK_WAIT=5 trim 10 && ! has $P-gobuild old; then
  ok "a flock without -w still takes a free lock"
else bad "a flock without -w still takes a free lock"; cat "$D/out"; fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
