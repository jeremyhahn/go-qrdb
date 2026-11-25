#!/usr/bin/env bash
# ci-sweep-volumes.sh removes the Docker volumes go-qrdb CI runs leave
# behind on a self-hosted runner when a job's teardown never ran (runner
# killed, SIGKILL) or failed:
#
#   qrdb-ci-<run>-<attempt>-<shard|soak>_<vol>   integration compose projects
#   go-qrdb-ci-tc-<run>-<n>-mcast                 scripts/in-toolchain-image.sh
#
# Only dangling volumes with one of those names, created more than
# SWEEP_MIN_AGE ago (default 3h, past the 120-minute job limit, so a live
# run's volumes are never touched), are removed. The go-qrdb-ci-cache-*
# caches, anonymous volumes and other projects' volumes never match.
#
# With --own the current run's project ("$IT_PROJECT"_*) is removed as
# well, whatever its age. A volume still in use is kept and named. The
# sweep prints one line per removed or kept volume and always exits 0: a
# sweep never fails a job.
#
#   usage: scripts/ci-sweep-volumes.sh [--own]
set -u

own=0
for a in "$@"; do
  case "$a" in
  --own) own=1 ;;
  *) echo "ci-sweep-volumes: unknown argument: $a (ignored)" >&2 ;;
  esac
done

# age_seconds converts SWEEP_MIN_AGE (<n>[smhd], bare n = seconds).
age_seconds() {
  local v=$1 n unit
  if [[ $v =~ ^([0-9]+)([smhd]?)$ ]]; then
    n=${BASH_REMATCH[1]}
    unit=${BASH_REMATCH[2]}
  else
    echo "ci-sweep-volumes: bad SWEEP_MIN_AGE=$v, using 3h" >&2
    n=3
    unit=h
  fi
  case "$unit" in
  m) echo $((n * 60)) ;;
  h) echo $((n * 3600)) ;;
  d) echo $((n * 86400)) ;;
  *) echo "$n" ;;
  esac
}

min_age=$(age_seconds "${SWEEP_MIN_AGE:-3h}")
now=$(date +%s)
own_prefix=
if [ "$own" = 1 ] && [ -n "${IT_PROJECT:-}" ]; then
  own_prefix="${IT_PROJECT}_"
fi

stale_re='^(qrdb-ci-[0-9]+-[0-9]+-([0-9]+|soak)_[a-z0-9-]+|go-qrdb-ci-tc-[0-9]+-[0-9]+-mcast)$'

# remove <name> <why>: one rm, one line either way.
remove() {
  local err
  if err=$(docker volume rm "$1" 2>&1 >/dev/null); then
    echo "ci-sweep-volumes: removed $1 ($2)"
  else
    echo "ci-sweep-volumes: kept $1: ${err//$'\n'/ }"
  fi
}

if ! list=$(docker volume ls -q -f dangling=true 2>&1); then
  echo "ci-sweep-volumes: cannot list volumes: ${list//$'\n'/ }"
  exit 0
fi

while read -r name; do
  [ -n "$name" ] || continue
  if [ -n "$own_prefix" ] && [[ $name == "$own_prefix"* ]]; then
    remove "$name" "this run"
    continue
  fi
  [[ $name =~ $stale_re ]] || continue
  created=$(docker volume inspect -f '{{.CreatedAt}}' "$name" 2>/dev/null) || continue
  created_s=$(date -d "$created" +%s 2>/dev/null) || continue
  if [ $((now - created_s)) -ge "$min_age" ]; then
    remove "$name" "stale"
  fi
done <<< "$list"
exit 0
