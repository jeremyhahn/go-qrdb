#!/usr/bin/env bash
# Run a command in the toolchain image against a COPY of the checkout.
#
#   usage: scripts/in-toolchain-image.sh <image> <command...>
#   e.g.   scripts/in-toolchain-image.sh qrdb-toolchain:abc make build
#
# How CI runs a stage in the devcontainer image (handbook ci-standard.md §2). A
# CI runner reaches its Docker daemon through a socket, so a bind mount would
# resolve on the daemon's host, where the checkout does not exist. This
# creates the container, streams the checkout into it (`.git` included: the
# releasability tests read it) owned by the image's non-root user, then starts
# it and returns the command's exit status. It works the same against a local
# daemon; on a workstation `make docker-run CMD=...` (bind mount) is the
# shorter path.
#
# The container keeps the integration service's posture where a copy-in allows
# it: every capability dropped, no-new-privileges, the same ulimits and pid
# bound, a non-root user, no network. The root filesystem is writable only
# because `docker cp` cannot write into a read-only one.
#
# Where the stage's files go is the native run's. /tmp (HOME, go's $WORK and
# every t.TempDir) is on the container's own layer, on the daemon's disk, as on
# a host and as in the compose services' anonymous /tmp volume (their root
# filesystem is read-only, so /tmp cannot be the layer): a full `make test` links one
# race-built test binary per package in parallel, and a size-capped tmpfs fills
# under it ("no space left on device" in compile, link and the tests' own
# writes). /dev/shm, where pkg/testutil/memdir puts the file engine's test
# directories, is 2 GiB rather than Docker's 64 MiB; a tmpfs holds only what
# is written to it.
#
# "No network" is a network namespace holding only loopback, and on its own
# that namespace has no route for multicast: a socket joining a group on the
# default interface, as pkg/discovery's multicast tests do, fails with "no such
# device". So with IN_IMAGE_NETWORK=none the stage joins the namespace of a
# holder container, given NET_ADMIN and nothing else, that marks loopback
# multicast-capable and routes 224.0.0.0/4 over it: the multicast a host
# offers, with nothing leaving the namespace.
#
# IN_IMAGE_NETWORK=bridge gives the container a network, for the stages that
# must reach one: `make deps-check` asks the module proxy for the newest
# releases, and `make security` fetches the vulnerability databases. Only then
# are GOPROXY reopened, a writable copy of the module cache used, and the forge
# credentials passed through, by NAME from
# this environment (the four variables dependency-resolution.md §6 has the
# runner inject), so no token ever appears on a command line. TRIVY_DB_UPSTREAMS,
# the vulnerability database sources CI derives with
# scripts/trivy-db-upstreams.sh, is passed the same way, for trivy's download.
#
# The container is named qrdb-ci-tc-<pid>-<random>, under the prefix
# scripts/reap-stale-ci-stacks.sh reaps, and is removed on every exit.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <image> <command...>" >&2
  exit 2
fi
image=$1
shift
network=${IN_IMAGE_NETWORK:-none}
case $network in
  none | bridge) ;;
  *) echo "IN_IMAGE_NETWORK must be none or bridge: $network" >&2; exit 2 ;;
esac

uid=10001
name="qrdb-ci-tc-$$-${RANDOM}"
netns="$name-netns"
args=(
  --name "$name"
  --init
  --cap-drop ALL
  --security-opt no-new-privileges:true
  --pids-limit 4096
  --ulimit memlock=67108864:67108864
  --ulimit core=1048576:1048576
  --ulimit nofile=65536:65536
  --shm-size 2g
  --user "$uid:$uid"
  --workdir /src
  --env HOME=/tmp
  --env CI="${CI:-}"
)
cmd="$*"
if [ "$network" = bridge ]; then
  # The baked module cache is read-only to the run's user, and a networked
  # stage writes to its cache: deps-check records the newer versions it finds,
  # govulncheck and trivy fetch what they scan. The run takes a writable copy
  # on the container's own layer, so nothing baked is downloaded twice.
  args+=(--network bridge --env "GOPROXY=https://proxy.golang.org,direct" --env GOMODCACHE=/home/dev/gomodcache)
  cmd="cp -a /gomodcache/. /home/dev/gomodcache/ && $cmd"
  for v in GOPRIVATE GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 TRIVY_DB_UPSTREAMS; do
    if [ -n "${!v:-}" ]; then args+=(--env "$v"); fi
  done
else
  args+=(--network "container:$netns")
fi

cleanup() { docker rm -f -v "$name" "$netns" >/dev/null 2>&1 || true; }
trap cleanup EXIT

if [ "$network" = none ]; then
  # The holder owns the namespace; root in it for the two `ip` commands, with
  # NET_ADMIN its only capability. The stage's container never holds it.
  docker run -d --name "$netns" --init --network none --read-only \
    --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges:true \
    --pids-limit 16 --user 0:0 "$image" sleep infinity >/dev/null
  docker exec "$netns" sh -c 'ip link set lo multicast on && ip route add 224.0.0.0/4 dev lo'
fi

docker create "${args[@]}" "$image" sh -c "$cmd" >/dev/null
tar -C "$PWD" --owner="$uid" --group="$uid" --numeric-owner -cf - . |
  docker cp -a - "$name:/src"
docker start -a "$name"
