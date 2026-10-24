#!/usr/bin/env bash
# Run a command in the toolchain image against a COPY of the checkout.
#
#   usage: scripts/in-toolchain-image.sh <image> <command...>
#   e.g.   scripts/in-toolchain-image.sh go-enclave-toolchain:abc make build
#
# in-toolchain-template: v3 (ci-template v1.22.2). Ships beside ci.yml; copy it to
# scripts/ with the workflow. From go-quicraft's scripts/in-toolchain-image.sh
# (a141c3c), with the container prefix and uid taken from the environment.
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
# CUSTOMIZE (go-qrdb) begin: where the stage's files go, and multicast.
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
# A networked stage writes its module cache to the gomod volume, which keeps
# what it fetched across jobs. With CI_CACHE=off it takes a writable copy of
# the baked module cache at /home/dev/gomodcache rather than an empty one
# under /tmp (see the bridge branch below).
# CUSTOMIZE (go-qrdb) end.
#
# IN_IMAGE_NETWORK=bridge gives the container a network, for the stages that
# must reach one: `make deps-check` asks the module proxy for the newest
# releases, and `make security` fetches the vulnerability databases. Only then
# are GOPROXY reopened, a writable module cache mounted (below; /tmp/gomodcache
# with CI_CACHE=off), and the forge credentials passed through, by NAME from
# this environment (the four variables dependency-resolution.md §6 has the
# runner inject), so no token ever appears on a command line.
#
# v2 (ci-template v1.22, Company Agent m616 item 2): the caches live in named
# volumes on the daemon's host (ci-standard.md §6), never in the container's
# writable layer or its 3 GB /tmp: GOCACHE at /cache/gobuild and
# GOLANGCI_LINT_CACHE at /cache/golangci for every stage, and, for a bridge
# stage, GOMODCACHE at /cache/gomod (a no-network stage keeps the image's own
# read-only module cache, so it still proves the image works offline). The
# volumes are <CI_STACK_PREFIX>cache-<class>-{gobuild,golangci,gomod}, one set
# per repo, trust class and daemon host, kept across jobs. CI_CACHE_CLASS
# (default pr) is `main` only for a push to main, so a pull request never
# writes a cache that main's build reads. A new volume's root is given to
# IN_IMAGE_UID by a helper container holding CHOWN and nothing else.
# scripts/trim-ci-caches.sh bounds their size. CI_CACHE=off mounts none (v1).
#
# v3 (ci-template v1.22.2, Company Agent m641 item 2 and m646 item 3): the
# stage runs in this sibling container, so the sibling holds the job slot's
# CPU bound. The job container's cgroup cpu.max gives it (rounded up; nproc
# when the job has no quota; IN_IMAGE_CPUS overrides), and the sibling gets
# --cpus N with GOMAXPROCS=N and GOFLAGS=-p=N. golangci-lint's default
# concurrency follows the container CPU quota, so lint takes N with no flag.
# A 4-CPU slot on the Dells gives 4; never a hardcoded number.
#
# The container is named <CI_STACK_PREFIX>tc-<pid>-<random>, under the prefix
# scripts/reap-stale-ci-stacks.sh reaps (default <checkout dir>-ci-, which in CI
# is <repo>-ci-), and is removed on every exit. IN_IMAGE_UID is the non-root
# uid the command runs as (default 10001; devcontainer.md §5: any non-root uid).
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

uid=${IN_IMAGE_UID:-10001}
case $uid in
  "" | 0 | *[!0-9]*) echo "IN_IMAGE_UID must be a non-root numeric uid: $uid" >&2; exit 2 ;;
esac
prefix=${CI_STACK_PREFIX:-$(basename "$PWD")-ci-}
name="${prefix}tc-$$-${RANDOM}"
cache=${CI_CACHE:-on}
case $cache in
  on | off) ;;
  *) echo "CI_CACHE must be on or off: $cache" >&2; exit 2 ;;
esac
class=${CI_CACHE_CLASS:-pr}
case $class in
  pr | main) ;;
  *) echo "CI_CACHE_CLASS must be pr or main: $class" >&2; exit 2 ;;
esac
cpus=${IN_IMAGE_CPUS:-}
if [ -z "$cpus" ]; then
  quota=max period=100000
  if [ -r /sys/fs/cgroup/cpu.max ]; then read -r quota period < /sys/fs/cgroup/cpu.max; fi
  case $quota in
    max | "" | *[!0-9]*) cpus=$(nproc) ;;
    *) cpus=$(((quota + period - 1) / period)) ;;
  esac
fi
case $cpus in
  "" | 0 | *[!0-9]*) echo "IN_IMAGE_CPUS must be a positive integer: $cpus" >&2; exit 2 ;;
esac
netns="$name-netns" # CUSTOMIZE (go-qrdb): the multicast namespace holder
args=(
  --name "$name"
  --cpus "$cpus"
  --env GOMAXPROCS="$cpus"
  --env GOFLAGS="-p=$cpus"
  --init
  --cap-drop ALL
  --security-opt no-new-privileges:true
  --pids-limit 4096
  --ulimit memlock=67108864:67108864
  --ulimit core=1048576:1048576
  --ulimit nofile=65536:65536
  # CUSTOMIZE (go-qrdb): no tmpfs on /tmp, a 2 GiB /dev/shm, and the network
  # chosen below (see the header).
  --shm-size 2g
  --user "$uid:$uid"
  --workdir /src
  --env HOME=/tmp
  --env CI="${CI:-}"
)
cmd="$*"
modcache=/home/dev/gomodcache # CUSTOMIZE (go-qrdb): the CI_CACHE=off copy (below)
if [ "$cache" = on ]; then
  vols="${prefix}cache-${class}"
  docker run --rm --network none --cap-drop ALL --cap-add CHOWN \
    --security-opt no-new-privileges:true --user 0:0 \
    -v "$vols-gobuild:/c/gobuild" -v "$vols-golangci:/c/golangci" -v "$vols-gomod:/c/gomod" \
    "$image" sh -c "for d in /c/*; do [ \"\$(stat -c %u:%g \"\$d\")\" = $uid:$uid ] || chown $uid:$uid \"\$d\"; done" >/dev/null
  args+=(
    -v "$vols-gobuild:/cache/gobuild" --env GOCACHE=/cache/gobuild
    -v "$vols-golangci:/cache/golangci" --env GOLANGCI_LINT_CACHE=/cache/golangci
  )
  if [ "$network" = bridge ]; then
    args+=(-v "$vols-gomod:/cache/gomod")
    modcache=/cache/gomod
  fi
fi
if [ "$network" = bridge ]; then
  # CUSTOMIZE (go-qrdb) begin: the baked module cache is read-only to the run's
  # user, and a networked stage writes to its cache: deps-check records the
  # newer versions it finds, govulncheck and trivy fetch what they scan. With
  # the cache on that is the gomod volume, kept across jobs; with CI_CACHE=off
  # the run takes a writable copy of the baked cache on the container's own
  # layer, so nothing baked is downloaded twice.
  args+=(--network bridge --env "GOPROXY=https://proxy.golang.org,direct" --env "GOMODCACHE=$modcache")
  if [ "$cache" = off ]; then cmd="cp -a /gomodcache/. $modcache/ && $cmd"; fi
  # CUSTOMIZE (go-qrdb) end.
  for v in GOPRIVATE GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0; do
    if [ -n "${!v:-}" ]; then args+=(--env "$v"); fi
  done
else
  args+=(--network "container:$netns") # CUSTOMIZE (go-qrdb): the holder's namespace
fi

cleanup() { docker rm -f -v "$name" "$netns" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# CUSTOMIZE (go-qrdb) begin: the multicast namespace holder. It owns the
# namespace; root in it for the two `ip` commands, with NET_ADMIN its only
# capability. The stage's container never holds it.
if [ "$network" = none ]; then
  docker run -d --name "$netns" --init --network none --read-only \
    --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges:true \
    --pids-limit 16 --user 0:0 "$image" sleep infinity >/dev/null
  docker exec "$netns" sh -c 'ip link set lo multicast on && ip route add 224.0.0.0/4 dev lo'
fi
# CUSTOMIZE (go-qrdb) end.

docker create "${args[@]}" "$image" sh -c "$cmd" >/dev/null
tar -C "$PWD" --owner="$uid" --group="$uid" --numeric-owner -cf - . |
  docker cp -a - "$name:/src"
docker start -a "$name"
