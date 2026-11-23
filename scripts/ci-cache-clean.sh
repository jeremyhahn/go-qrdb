#!/usr/bin/env bash
# CUSTOMIZE (go-qrdb, #1596): deletes CI Go cache trees whose files belong to
# the stages' non-root uid (10001, scripts/in-toolchain-image.sh), which the
# runner's own user cannot delete.
#
#   ci-cache-clean.sh PATH...
#
# Each PATH that exists is deleted. A directory's contents are deleted as
# root in a --rm container of the toolchain image (CI_CACHE_CLEAN_IMAGE,
# default TOOLCHAIN_IMAGE, the image the stages ran in): no network, every
# capability dropped but DAC_OVERRIDE and FOWNER, no new privileges, the
# directory alone mounted at /c, `find /c -mindepth 1 -delete`. The emptied
# directory itself, whose parent belongs to the runner, then goes with a
# plain rm. Only when docker or the image is absent is the whole PATH a
# plain rm -rf.
#
# Cleanup never fails a job: a PATH that survives is a ::warning:: on stderr
# (the next job retries), and the exit status is 0. scripts/ci-cache-dir.sh
# and ci.yml's "Remove the temp Go caches" steps call it.
set -uo pipefail

image=${CI_CACHE_CLEAN_IMAGE:-${TOOLCHAIN_IMAGE:-}}
# A tag is mutable: on a shared daemon another job can load other bytes under
# it after scripts/ci-image.sh load verified it. The helper runs the ID that
# load recorded for the tag (CI_IMAGE_IDS, else RUNNER_TEMP/ci-image-ids, as
# scripts/in-toolchain-image.sh reads it), never the tag; a record that is not
# an image ID disables the helper. A tag with no record (a local run) runs as
# given.
ids=${CI_IMAGE_IDS:-${RUNNER_TEMP:+$RUNNER_TEMP/ci-image-ids}}
if [ -n "$image" ] && [ -n "$ids" ] && [ -s "$ids/${image//[^A-Za-z0-9._-]/_}" ]; then
  verified=$(<"$ids/${image//[^A-Za-z0-9._-]/_}")
  case $verified in
    sha256:*) image=$verified ;;
    *)
      echo "::warning::ci-cache-clean: $ids records '$verified' for $image, not an image ID; no root helper" >&2
      image=
      ;;
  esac
fi
use_docker=
if [ -n "$image" ] && command -v docker >/dev/null 2>&1; then
  use_docker=1
fi

for p in "$@"; do
  [ -n "$p" ] || continue
  [ -e "$p" ] || [ -L "$p" ] || continue
  if [ -n "$use_docker" ] && [ -d "$p" ] && [ ! -L "$p" ]; then
    abs=$(cd -- "$p" 2>/dev/null && pwd -P) || abs=
    if [ -n "$abs" ]; then
      docker run --rm --network none --cap-drop ALL \
        --cap-add DAC_OVERRIDE --cap-add FOWNER \
        --security-opt no-new-privileges:true --user 0:0 \
        -v "$abs:/c" --entrypoint find "$image" \
        /c -mindepth 1 -delete >/dev/null 2>&1 ||
        echo "::warning::ci-cache-clean: the toolchain-image helper could not empty $p" >&2
    fi
  fi
  rm -rf -- "$p" 2>/dev/null || true
  if [ -e "$p" ] || [ -L "$p" ]; then
    if [ -n "$use_docker" ]; then
      echo "::warning::ci-cache-clean: could not delete $p; the next job retries" >&2
    else
      echo "::warning::ci-cache-clean: could not delete $p (stage-owned files; no docker or toolchain image for the root helper); the next job retries" >&2
    fi
  fi
done
exit 0
