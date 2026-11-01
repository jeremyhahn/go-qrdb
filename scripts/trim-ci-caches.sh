#!/usr/bin/env bash
# Bound the size of this repo's CI cache volumes on the runner host.
#
#   usage: scripts/trim-ci-caches.sh <prefix> <image> [max-gib]
#   e.g.   scripts/trim-ci-caches.sh go-enclave-ci- "$TOOLCHAIN_IMAGE"
#
# v1 (ci-template v1.22, Company Agent m616 item 2). in-toolchain-image.sh v2
# keeps the build, golangci-lint and module caches in named volumes,
# <prefix>cache-<class>-{gobuild,golangci,gomod}, on the Docker host the job
# talks to (ci-standard.md §6). Go trims its build cache of entries unused for
# five days, but nothing trims the module cache, so the volumes would grow
# without bound. This measures them and, while their total is over <max-gib>,
# first deletes the build and golangci-lint cache entries unused longest (see
# evict), then removes the largest volume no container is using. An evicted
# entry or a removed volume costs a rebuild, never a wrong one.
#
# Sizes are measured by `du`, and entries evicted by `find`, in <image> (the
# toolchain image the job just built: it has both, and it is already on the
# host), run as IN_IMAGE_UID (the volumes' owner, default 10001) with no
# network and no capability; `du` mounts the volume read-only. A volume any
# container (running or stopped) mounts is never evicted from or removed; `docker volume rm` refuses one in use in any
# case.
#
# <max-gib> defaults to CI_CACHE_MAX_GIB, else 32: the bound for this repo's
# volumes, both trust classes together, on one host.
#
# A failed measurement or removal is reported and skipped: this never fails
# the job. REAP_DRY_RUN=1 prints what would be removed and removes nothing.
set -u

prefix=${1:-}
image=${2:-}
max_gib=${3:-${CI_CACHE_MAX_GIB:-32}}
case $prefix in
  '' | *[!A-Za-z0-9_.-]*) echo "usage: $0 <prefix [A-Za-z0-9_.-]> <image> [max-gib]" >&2; exit 2 ;;
esac
if [ -z "$image" ]; then
  echo "usage: $0 <prefix> <image> [max-gib]" >&2
  exit 2
fi
case $max_gib in
  '' | *[!0-9]*) echo "max-gib must be a number: $max_gib" >&2; exit 2 ;;
esac
max_kib=$((max_gib * 1024 * 1024))
uid=${IN_IMAGE_UID:-10001}
case $uid in
  "" | 0 | *[!0-9]*) echo "IN_IMAGE_UID must be a non-root numeric uid: $uid" >&2; exit 2 ;;
esac

# CUSTOMIZE (go-qrdb, #1275) begin: one trim at a time per host. Every job
# trims after its stages, and each measure below walks the daemon's volumes and
# their snapshots; N jobs walking them at once stalled a loaded daemon long
# enough that a stage's multicast configurator could not start (#1275). The
# lock file is on the runner's tool cache, the one directory every job on a
# runner host shares (CI_CACHE_LOCK overrides it). A job that cannot take it
# within CI_CACHE_LOCK_WAIT seconds skips its trim: the holder is trimming the
# same volumes, so the skipped trim is redundant, never a failure.
lock=${CI_CACHE_LOCK:-${RUNNER_TOOL_CACHE:-${TMPDIR:-/tmp}}/ci-cache-trim.lock}
lock_wait=${CI_CACHE_LOCK_WAIT:-120}
case $lock_wait in
  '' | *[!0-9]*) echo "CI_CACHE_LOCK_WAIT must be a number: $lock_wait" >&2; exit 2 ;;
esac
# lock_fd9 takes the lock on fd 9 within $lock_wait seconds. It polls
# `flock -n` once a second rather than calling `flock -w`: the toolchain
# image's BusyBox flock takes only -s, -x, -u and -n, and rejects -w.
lock_fd9() {
  lock_deadline=$((SECONDS + lock_wait))
  until flock -n 9; do
    [ "$SECONDS" -lt "$lock_deadline" ] || return 1
    sleep 1
  done
}
if ! command -v flock >/dev/null 2>&1; then
  echo "warn  flock not found: trimming without the host lock"
elif ! { exec 9>>"$lock"; } 2>/dev/null; then
  echo "warn  $lock: cannot open, trimming without the host lock"
elif ! lock_fd9; then
  echo "trim: skipped, another job held $lock for ${lock_wait}s"
  exit 0
fi
# CUSTOMIZE (go-qrdb, #1275) end.

# size <volume>: the volume's size in KiB, or nothing when it cannot be read.
size() {
  docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges:true \
    --user "$uid:$uid" -v "$1:/c:ro" --entrypoint du "$image" -sk /c 2>/dev/null | awk '{print $1}'
}

list=$(mktemp)
trap 'rm -f "$list"' EXIT

# measure: writes "<KiB> <volume>" per cache volume of the prefix to $list and
# sets total to their sum.
measure() {
  : > "$list"
  total=0
  for vol in $(docker volume ls -q --filter "name=${prefix}cache-"); do
    case $vol in "${prefix}cache-"*) ;; *) continue ;; esac
    kib=$(size "$vol")
    case $kib in
      '' | *[!0-9]*) echo "warn  $vol: size not read"; continue ;;
    esac
    echo "$kib $vol" >> "$list"
    total=$((total + kib))
  done
}

# CUSTOMIZE (go-qrdb, #1046) begin: evict by age before removing a volume.
# evict <minutes>: delete every entry of the build and golangci-lint caches,
# in the volumes no container mounts, last used more than <minutes> ago. Both
# are Go's cache format, whose own trim deletes entries one at a time by
# mtime, and Go sets an entry's mtime when it uses it (at most hourly), so an
# evicted entry is a cache miss, never a wrong build. The module cache is not
# evicted this way: a module directory with files missing is a broken module,
# not a miss.
evict() {
  local mounts=() _ vol
  while read -r _ vol; do
    case $vol in *-gobuild | *-golangci) ;; *) continue ;; esac
    [ -z "$(docker ps -aq --filter "volume=$vol")" ] || continue
    mounts+=(-v "$vol:/c/$vol")
  done < "$list"
  [ "${#mounts[@]}" -gt 0 ] || return 0
  if [ "${REAP_DRY_RUN:-0}" = 1 ]; then
    echo "dry   evict cache entries unused for over $1 min"
    return 0
  fi
  docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges:true \
    --user "$uid:$uid" "${mounts[@]}" --entrypoint find "$image" \
    /c -type f -mmin "+$1" -delete >/dev/null 2>&1 ||
    echo "warn  eviction of entries unused for over $1 min did not finish"
}
# CUSTOMIZE (go-qrdb, #1046) end.

measure
echo "caches: $((total / 1024 / 1024)) GiB of ${max_gib} GiB for prefix '$prefix'"

# CUSTOMIZE (go-qrdb, #1046) begin: while the total is over the bound, evict
# entries unused for three days, then one day, 12 h, 6 h and 3 h, and measure
# again after each. What the last jobs on this host used is what the next one
# reads, so it goes last. Removing a whole volume, below, is the fallback: done
# first, it emptied the build cache whenever the bound was reached, and every
# stage after it compiled the tree from nothing (#1046).
for age in 4320 1440 720 360 180; do
  [ "$total" -gt "$max_kib" ] || break
  evict "$age"
  [ "${REAP_DRY_RUN:-0}" = 1 ] && continue
  measure
  echo "evict entries unused for over $age min: $((total / 1024 / 1024)) GiB left"
done
# CUSTOMIZE (go-qrdb, #1046) end.

removed=0
while read -r kib vol; do
  [ "$total" -gt "$max_kib" ] || break
  if [ -n "$(docker ps -aq --filter "volume=$vol")" ]; then
    echo "keep  $vol ($((kib / 1024)) MiB, in use)"
    continue
  fi
  if [ "${REAP_DRY_RUN:-0}" = 1 ]; then
    echo "dry   docker volume rm $vol ($((kib / 1024)) MiB)"
  elif docker volume rm "$vol" >/dev/null 2>&1; then
    echo "trim  $vol ($((kib / 1024)) MiB)"
  else
    echo "warn  $vol not removed"
    continue
  fi
  total=$((total - kib))
  removed=$((removed + 1))
done < <(sort -rn "$list")

echo "trim: $removed volume(s), $((total / 1024 / 1024)) GiB left for prefix '$prefix'"
exit 0
