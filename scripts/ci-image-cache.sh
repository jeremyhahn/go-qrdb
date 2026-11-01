#!/usr/bin/env bash
# ci-image-cache.sh v1 (ci-template v1.30, fix 1 of the 72-hour CI evaluation):
# the toolchain image's registry cache. It ships beside ci.yml as
# scripts/ci-image-cache.sh.
#
#   ci-image-cache.sh pull    before a local toolchain build: when the daemon
#                             lacks the toolchain tag, pull it from the forge
#                             registry and tag it locally
#
# CUSTOMIZE (go-qrdb, #1331): this repository already publishes the toolchain
# image. docker-build, on the release runner on a push to main, pushes it with
# `scripts/ci-registry.sh push <tag> qrdb-ci-toolchain main` (DS-3516, rulings
# 169 and 173) to <registry>/<release user>/qrdb-ci-toolchain:<hash>-main, so
# the template's publish command, a second publisher on the release runner, is
# not shipped here, and pull reads that same repository. <hash> is the
# content hash of the local tag `make -s toolchain-image` prints,
# qrdb-toolchain:<hash>. The registry is the forge's own, host[:port] from
# GITHUB_SERVER_URL, as in scripts/ci-registry.sh; no address is written here.
# The namespace is CI_IMAGE_NAMESPACE, else RELEASE_REGISTRY_USER, the release
# credential's user the images are pushed under, which is public for anonymous
# pull; with neither set there is nothing to pull from. On github.com pull does
# nothing.
#
# pull exits 0 when the daemon holds the tag after it, and 1 otherwise; the
# caller runs `scripts/ci-image-cache.sh pull || true`, so a miss, an
# unreachable registry or a failed pull falls back to the local build, never to
# a red job. A pulled image keeps its digest in the log. A pull logs in with
# nothing, through an empty DOCKER_CONFIG.
set -euo pipefail

die() { echo "ci-image-cache: $*" >&2; exit 1; }

[ "${1:-}" = pull ] || die "usage: ci-image-cache.sh pull"

case ${GITHUB_SERVER_URL:-} in
  '' | https://github.com) echo "ci-image-cache: no forge registry on this host; nothing to do"; exit 0 ;;
esac
host=${GITHUB_SERVER_URL#*://}
host=${host%%/*}
host=${host,,}
ns=${CI_IMAGE_NAMESPACE:-${RELEASE_REGISTRY_USER:-}}
ns=${ns,,}
if [ -z "$ns" ]; then
  echo "ci-image-cache: neither CI_IMAGE_NAMESPACE nor RELEASE_REGISTRY_USER names the namespace; the job builds it"
  exit 1
fi
[[ $ns =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "'$ns' does not name a registry owner"

local_tag=$(env -u TOOLCHAIN_IMAGE make -s --no-print-directory toolchain-image) || die "make -s toolchain-image failed"
[[ $local_tag =~ ^qrdb-toolchain:[0-9a-f]{16}$ ]] || die "make -s toolchain-image printed '$local_tag', not qrdb-toolchain:<hash>"
remote="$host/$ns/qrdb-ci-toolchain:${local_tag##*:}-main"

if docker image inspect "$local_tag" >/dev/null 2>&1; then
  echo "ci-image-cache: the daemon holds $local_tag; no pull"
  exit 0
fi

cfg=$(mktemp -d)
cleanup() { rm -f "$cfg/config.json"; rmdir "$cfg" 2>/dev/null || true; }
trap cleanup EXIT

if ! DOCKER_CONFIG=$cfg docker pull -q "$remote" >/dev/null 2>&1; then
  echo "ci-image-cache: $remote is not in the registry (or the pull failed); the job builds it"
  exit 1
fi
digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$remote" 2>/dev/null || echo unknown)
docker tag "$remote" "$local_tag"
docker image rm "$remote" >/dev/null 2>&1 || true   # untags only; $local_tag keeps the image
echo "ci-image-cache: pulled $digest as $local_tag"
