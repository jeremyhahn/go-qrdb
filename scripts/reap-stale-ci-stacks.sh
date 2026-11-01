#!/usr/bin/env bash
# Remove this repo's stopped CI leftovers on the runner host.
#
#   usage: scripts/reap-stale-ci-stacks.sh <prefix> [min-age-seconds]
#
# v1 (ci-template v1.18, 2026-10-12); v2 and v3 below. An integration job's
# `if: always()` teardown runs only while the job process lives. When the host
# or its Docker daemon dies mid-job, the job is gone and so is its teardown: the
# compose stack stays behind, exited, with its networks and volumes (11 stacks
# on two runner hosts after the 2026-10-11/12 restarts). Nothing else
# removes them, so the first step of every integration job runs this.
#
# What it removes, on the Docker host the job talks to:
#   - a compose project (label com.docker.compose.project) whose name starts
#     with <prefix>, when EVERY container in it is stale; then the project's
#     networks and volumes (same label);
#   - a container outside compose whose name starts with <prefix>, when stale,
#     with its anonymous volumes.
# v2 (ci-template v1.23, Company Agent m674 item 3, 2026-10-18; the go-qrdb
# #814 pattern): images too. The integration image is named by its content,
# <prefix>it:<hash> (`make -s it-image`, ci-standard.md §9), and every job and
# run of that tree on this runner reuses it, so no job removes it. This
# removes a <prefix>it image created at least REAP_IMAGE_MIN_AGE seconds ago,
# default 7200 (2 h: the longest full-tier run plus headroom), so an image a
# run still needs is not old enough. An image a container (running or stopped)
# uses is skipped before any of its names is removed (in_use): `docker image
# rm` refuses only the LAST name of such an image and untags any other, so its
# refusal alone does not keep a live stack's reference. Age alone does not protect a reused image older than that,
# so every job that needs the image holds a lease on it: a created, never
# started container named <prefix>lease-<run>-..., removed at its Tear down
# (ci.yml). A lease a dead job leaves is stale like any other container
# outside compose, and goes after <min-age-seconds>; its image goes on the
# reap after that.
# v3 (ci-template v1.23.1, 2026-10-19): an image's age runs
# from the later of its `.Created` and its last tag time
# (`.Metadata.LastTagTime`). A rebuild from a full cache keeps the old
# `.Created` but re-tags the image, so v2 saw a just-rebuilt image as stale and
# a parallel job's reaper removed it between its build and its lease
# (go-quicraft main runs 57389 and 57484).
#
# CUSTOMIZE (go-qrdb, #813, run 57540): the lease is the job's IT_HOLD
# container, <prefix>hold-<run>-<attempt>-<job>, created from the image by
# `make docker-build-it` before the job uses it and removed by
# `make docker-release-it` in its teardown; a dead job's hold goes with the
# stale containers above. The template's `<prefix>lease-` steps are not in
# ci.yml, so a job holds the image once, not twice. The image job reaps
# under qrdb-ci- as well as <repo>-ci-, because the hold and the image are
# named under the Makefile's qrdb-ci- prefix.
# CUSTOMIZE (go-qrdb) end.
#
# Stale: exited or dead and finished at least <min-age-seconds> ago, or
# created and never started at least that long ago. A project with any
# container running, paused, restarting or removing is never touched.
#
# <min-age-seconds> defaults to 11100: the runners' 3h job timeout plus 5 min,
# so no job that could still own the stack is alive. Pass the job's own
# timeout-minutes * 60 plus a margin only if you know every runner's timeout.
#
# The prefix is the repo's own (`qrdb-int`, `recountly-ci-`): the name the job
# gives its compose project, without the run id. Never a prefix another repo's
# projects could start with.
#
# A failed removal is reported and skipped: the reaper never fails the job.
# REAP_DRY_RUN=1 prints what would be removed and removes nothing.
set -u

prefix=${1:-}
min_age=${2:-11100}
case $prefix in
  '' | *[!A-Za-z0-9_.-]*) echo "usage: $0 <prefix [A-Za-z0-9_.-]> [min-age-seconds]" >&2; exit 2 ;;
esac
case $min_age in
  '' | *[!0-9]*) echo "min-age-seconds must be a number: $min_age" >&2; exit 2 ;;
esac
image_min_age=${REAP_IMAGE_MIN_AGE:-7200}
case $image_min_age in
  '' | *[!0-9]*) echo "REAP_IMAGE_MIN_AGE must be a number: $image_min_age" >&2; exit 2 ;;
esac

now=$(date +%s)

# run <command...>: the command, or only its echo under REAP_DRY_RUN=1.
run() {
  if [ "${REAP_DRY_RUN:-0}" = 1 ]; then echo "dry   $*"; else "$@" >/dev/null; fi
}

# stale <container>: 0 when the container is stale (as above), 1 otherwise.
stale() {
  local state finished created t
  read -r state finished created < <(docker inspect -f '{{.State.Status}} {{.State.FinishedAt}} {{.Created}}' "$1" 2>/dev/null) || return 1
  case $state in
    exited | dead) t=$finished ;;
    created) t=$created ;;
    *) return 1 ;;
  esac
  t=$(date -d "$t" +%s 2>/dev/null) || return 1
  [ $((now - t)) -ge "$min_age" ]
}

# in_use succeeds, and says so, when a container (running or stopped: a hold
# is created and never started) uses the image $1 names, or when that cannot
# be known: an image is kept rather than reaped on a failed lookup.
in_use() {
  local users
  if ! users=$(docker ps -aq --filter "ancestor=$1" 2>/dev/null); then
    echo "keep  image $1 (cannot list the containers that use it)"
    return 0
  fi
  [ -n "$users" ] || return 1
  echo "keep  image $1 (used by a container)"
}

reaped=0

# Compose projects.
for project in $(docker ps -a --filter label=com.docker.compose.project \
    --format '{{.Label "com.docker.compose.project"}}' | sort -u); do
  case $project in "$prefix"*) ;; *) continue ;; esac
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
  keep=0
  for id in $ids; do stale "$id" || { keep=1; break; }; done
  if [ "$keep" = 1 ]; then
    echo "keep  $project (a container is live or stopped within ${min_age}s)"
    continue
  fi
  # shellcheck disable=SC2086 # one id per word
  set -- $ids
  echo "reap  $project: $# container(s)"
  run docker rm -v "$@" || echo "warn  $project: container removal failed"
  for net in $(docker network ls -q --filter "label=com.docker.compose.project=$project"); do
    run docker network rm "$net" || echo "warn  $project: network $net not removed"
  done
  for vol in $(docker volume ls -q --filter "label=com.docker.compose.project=$project"); do
    run docker volume rm "$vol" || echo "warn  $project: volume $vol not removed"
  done
  reaped=$((reaped + 1))
done

# Containers outside compose.
docker ps -a --filter "name=^$prefix" \
    --format '{{.ID}} {{.Names}} {{.Label "com.docker.compose.project"}}' |
  while read -r id name project; do
    [ -z "$project" ] || continue
    case $name in "$prefix"*) ;; *) continue ;; esac
    if stale "$id"; then
      echo "reap  container $name"
      run docker rm -v "$id" || echo "warn  container $name not removed"
    fi
  done

# CUSTOMIZE (go-qrdb, DS-4701): scripts/ci-registry.sh tags the image a
# toolchain hold holds <prefix>hold:<hold>, so a rebuild that moves the
# content-hash tag cannot drop the image the job's stages run by ID. A hold
# tag whose hold container is gone belongs to a job that died before its
# teardown (its stale hold went above, or a teardown untagged its own first):
# it is removed, never forced, so the image goes only when no other name or
# container keeps it. The hold is created before its tag and removed before
# it, so a live hold's tag always has its container.
holdtags=0
for ref in $(docker images --filter "reference=${prefix}hold" --format '{{.Repository}}:{{.Tag}}'); do
  case $ref in "${prefix}hold:"*) ;; *) continue ;; esac
  name=${ref#"${prefix}hold:"}
  if docker container inspect "$name" >/dev/null 2>&1; then
    echo "keep  hold tag $ref (hold $name exists)"
    continue
  fi
  echo "reap  hold tag $ref (hold $name is gone)"
  if run docker image rm "$ref"; then holdtags=$((holdtags + 1)); else echo "warn  hold tag $ref not removed"; fi
done
# CUSTOMIZE (go-qrdb) end.

# Content-addressed integration images (v2, above).
images=0
for ref in $(docker images --filter "reference=${prefix}it" --format '{{.Repository}}:{{.Tag}}'); do
  case $ref in "${prefix}it:"*) ;; *) continue ;; esac
  read -r created tagged < <(docker image inspect -f '{{.Created}} {{.Metadata.LastTagTime.Unix}}' "$ref" 2>/dev/null) || continue
  t=$(date -d "$created" +%s 2>/dev/null) || continue
  # A never-tagged image reports the zero time, a large negative number.
  case $tagged in ''|*[!0-9]*) ;; *) [ "$tagged" -le "$t" ] || t=$tagged ;; esac
  if [ $((now - t)) -lt "$image_min_age" ]; then
    echo "keep  image $ref (created or tagged within ${image_min_age}s)"
    continue
  fi
  in_use "$ref" && continue
  echo "reap  image $ref"
  if run docker image rm "$ref"; then images=$((images + 1)); else echo "warn  image $ref not removed (in use?)"; fi
done

# CUSTOMIZE (go-qrdb, DS-3516): the CI images pulled from the Gitea registry
# are <registry>/<owner>/<prefix>it and <registry>/<owner>/<prefix>toolchain,
# named by tag on the host that pushed them and by digest alone where a job
# pulled them. The same age rule applies to each name, and an image a job's
# hold (a created, never-started container) uses keeps every name: in_use is
# asked before any of them is removed, because `docker image rm` of one of
# several names only untags it, in use or not, and the name it drops can be
# the digest reference the holding job's next `docker create` runs. A
# pull by digest records no tag time, so an unheld pulled image ages from its
# build; a job holds what it pulls, and its pull retries if a reap wins the
# race between the two.
while read -r repo tag digest; do
  case $repo in */"${prefix}it" | */"${prefix}toolchain") ;; *) continue ;; esac
  refs=()
  [ "$tag" = '<none>' ] || refs+=("$repo:$tag")
  [ "$digest" = '<none>' ] || refs+=("$repo@$digest")
  [ "${#refs[@]}" -gt 0 ] || continue
  read -r created tagged < <(docker image inspect -f '{{.Created}} {{.Metadata.LastTagTime.Unix}}' "${refs[0]}" 2>/dev/null) || continue
  t=$(date -d "$created" +%s 2>/dev/null) || continue
  case $tagged in ''|*[!0-9]*) ;; *) [ "$tagged" -le "$t" ] || t=$tagged ;; esac
  if [ $((now - t)) -lt "$image_min_age" ]; then
    echo "keep  image ${refs[0]} (created or tagged within ${image_min_age}s)"
    continue
  fi
  in_use "${refs[0]}" && continue
  for ref in "${refs[@]}"; do
    # Removing the tag can remove the image, and its digest name with it.
    docker image inspect "$ref" >/dev/null 2>&1 || continue
    echo "reap  image $ref"
    if run docker image rm "$ref"; then images=$((images + 1)); else echo "warn  image $ref not removed (in use?)"; fi
  done
done < <(docker images --digests --format '{{.Repository}} {{.Tag}} {{.Digest}}')

if [ "${REAP_DRY_RUN:-0}" = 1 ]; then done_as="would be removed"; else done_as=removed; fi
echo "reaper: $reaped compose project(s), $images image(s) and $holdtags hold tag(s) $done_as for prefix '$prefix'"
exit 0
