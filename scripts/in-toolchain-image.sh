#!/usr/bin/env bash
# Run a command in the toolchain image against a COPY of the checkout.
#
#   usage: scripts/in-toolchain-image.sh <image> <command...>
#   e.g.   scripts/in-toolchain-image.sh go-enclave-toolchain:abc make build
#
# in-toolchain-template: v4 (ci-template v1.29.5). Ships beside ci.yml; copy it to
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
# device". So with IN_IMAGE_NETWORK=none, once the stage has started, a
# one-shot container joins the stage's own namespace, given NET_ADMIN and
# nothing else, marks loopback multicast-capable, routes 224.0.0.0/4 over it
# and exits: the multicast a host offers, with nothing leaving the namespace.
# The stage's command waits for that route before it runs (MULTICAST_WAIT
# seconds at most). The namespace is the stage's, so no container holds it
# but the stage: a holder that slept until the script removed it outlived
# every job killed before its EXIT trap ran (#1264).
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
# IN_IMAGE_UID by a helper container holding CHOWN and nothing else, run only
# once the stage's container is created (DS-3998, below).
# scripts/trim-ci-caches.sh bounds their size. CI_CACHE=off mounts none (v1).
#
# v3 (ci-template v1.22.2, Company Agent m641 item 2 and m646 item 3): the
# stage runs in this sibling container, so the sibling holds the job slot's
# CPU bound. The job container's cgroup cpu.max gives it (rounded up; nproc
# when the job has no quota; IN_IMAGE_CPUS overrides), and the sibling gets
# --cpus N with GOMAXPROCS=N and GOFLAGS=-p=N. golangci-lint's default
# concurrency follows the container CPU quota, so lint takes N with no flag.
# A 4-CPU CI job slot gives 4; never a hardcoded number.
#
# The sibling holds the job slot's memory bound the same way (dept-infra #1337,
# the ruling 235/237 capacity answer; go-trusted-platform #1182 is the twin):
# the job container's cgroup memory.max (8g when the job has no limit;
# IN_IMAGE_MEMORY, a size such as 12g, overrides) becomes --memory, and
# --memory-swap is set to the same size, so the stage gets no swap: a stage
# that outgrows it is OOM-killed and fails, instead of swapping the shared
# runner host. Unbounded, the go-qrdb-ci-tc-* containers held up to 4.8 GiB
# each on one runner host. The 2 GiB /dev/shm is charged to the same limit. A
# 16g CI job slot gives 16g.
#
# CI_CGROUP_PARENT (default ci.slice) is the container's --cgroup-parent, so
# the runner host's ci.slice bounds every CI container together, wherever the
# daemon's own default parent points (go-trusted-platform 0216d8e, #1178). Set
# it empty to pass no --cgroup-parent (a developer host with no ci.slice).
#
# The container is named <CI_STACK_PREFIX>tc-<pid>-<random>, under the prefix
# scripts/reap-stale-ci-stacks.sh reaps (default <checkout dir>-ci-, which in CI
# is <repo>-ci-), and is removed on every exit, INT, TERM and HUP included.
# IN_IMAGE_UID is the non-root uid the command runs as (default 10001;
# devcontainer.md §5: any non-root uid).
#
# CUSTOMIZE (go-qrdb): every container the script starts carries the label
# qrdb.ci.job=<GITHUB_RUN_ID>-<GITHUB_JOB> (local-local off CI), so a teardown
# or a sweep tells the job a container belongs to without guessing by age.
# v4 (ci-template v1.29.4 and v1.29.5, Company Agent rulings 261(c) and
# 262(a)): Go's build and link scratch (TMPDIR,
# GOTMPDIR) is an anonymous volume on the image's /var/tmp (IN_IMAGE_SCRATCH
# overrides), on the daemon's data disk, removed with the container. A scratch directory that is missing or not writable fails
# the stage by name. A bridge stage's GOPROXY puts DEVCACHE_GOPROXY (Athens)
# first when the runner sets it: <DEVCACHE_GOPROXY>|https://proxy.golang.org|direct,
# so any error falls through to the public proxy; GOPRIVATE travels with it.
#
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
memory=${IN_IMAGE_MEMORY:-}
if [ -z "$memory" ]; then
  limit=max
  if [ -r /sys/fs/cgroup/memory.max ]; then read -r limit < /sys/fs/cgroup/memory.max; fi
  case $limit in
    max | "" | *[!0-9]*) memory=8g ;;
    *) memory=$limit ;;
  esac
fi
if ! [[ $memory =~ ^[1-9][0-9]*[mg]?$ ]]; then
  echo "IN_IMAGE_MEMORY must be a size in bytes, m or g, e.g. 16g: $memory" >&2
  exit 2
fi
scratch=${IN_IMAGE_SCRATCH:-/var/tmp}
case $scratch in
  /*) ;;
  *) echo "IN_IMAGE_SCRATCH must be an absolute path: $scratch" >&2; exit 2 ;;
esac
cgroup_parent=${CI_CGROUP_PARENT-ci.slice}
if [ -n "$cgroup_parent" ] && ! [[ $cgroup_parent =~ ^[A-Za-z0-9][A-Za-z0-9_.@:/-]*$ ]]; then
  echo "CI_CGROUP_PARENT must be a cgroup name such as ci.slice, or empty: $cgroup_parent" >&2
  exit 2
fi
# CUSTOMIZE (go-qrdb) begin: the multicast configurator and the job label.
netns="$name-netns"
job="${GITHUB_RUN_ID:-local}-${GITHUB_JOB:-local}"
# The stage's wait for the multicast route: the configurator's `docker run`
# on an image the daemon holds, never the stage's own run time.
mwait=${MULTICAST_WAIT:-60}
case $mwait in
  "" | 0 | *[!0-9]*) echo "MULTICAST_WAIT must be a positive integer: $mwait" >&2; exit 2 ;;
esac
# CUSTOMIZE (go-qrdb) end.
args=(
  --name "$name"
  --label "qrdb.ci.job=$job" # CUSTOMIZE (go-qrdb)
  --cpus "$cpus"
  --env GOMAXPROCS="$cpus"
  --env GOFLAGS="-p=$cpus"
  --init
  --cap-drop ALL
  --security-opt no-new-privileges:true
  --pids-limit 4096
  --memory "$memory"
  --memory-swap "$memory"
  --ulimit memlock=67108864:67108864
  --ulimit core=1048576:1048576
  --ulimit nofile=65536:65536
  # CUSTOMIZE (go-qrdb): no tmpfs on /tmp, a 2 GiB /dev/shm, and the network
  # chosen below (see the header).
  --shm-size 2g
  # Go's build and link scratch on its own volume (v4, above).
  -v "$scratch"
  --env TMPDIR="$scratch"
  --env GOTMPDIR="$scratch"
  --user "$uid:$uid"
  --workdir /src
  --env HOME=/tmp
  --env CI="${CI:-}"
)
if [ -n "$cgroup_parent" ]; then
  args+=(--cgroup-parent "$cgroup_parent")
fi
cmd="$*"
modcache=/home/dev/gomodcache # CUSTOMIZE (go-qrdb): the CI_CACHE=off copy (below)
if [ "$cache" = on ]; then
  vols="${prefix}cache-${class}"
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
  args+=(--network bridge --env "GOPROXY=${DEVCACHE_GOPROXY:+$DEVCACHE_GOPROXY|}https://proxy.golang.org|direct" --env "GOMODCACHE=$modcache")
  if [ "$cache" = off ]; then cmd="cp -a /gomodcache/. $modcache/ && $cmd"; fi
  # CUSTOMIZE (go-qrdb) end.
  for v in GOPRIVATE GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0; do
    if [ -n "${!v:-}" ]; then args+=(--env "$v"); fi
  done
else
  # CUSTOMIZE (go-qrdb) begin: the stage's own namespace, which the
  # configurator below makes multicast-capable once the stage has started.
  args+=(--network none)
  gate="i=0; until ip route show dev lo | grep -q '^224\\.0\\.0\\.0/4'; do"
  gate="$gate i=\$((i + 1)); if [ \$i -gt $((mwait * 10)) ]; then"
  gate="$gate echo 'in-toolchain-image: no multicast route after $mwait s' >&2; exit 1; fi;"
  cmd="$gate sleep 0.1; done; $cmd"
  # CUSTOMIZE (go-qrdb) end.
fi

# shellcheck disable=SC2329 # invoked by the EXIT trap below
cleanup() { docker rm -f -v "$name" "$netns" >/dev/null 2>&1 || true; }
# A signalled script cleans up too: bash runs no EXIT trap on a signal it does
# not trap, and the stage is a sibling on the host's daemon that would run on.
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# The scratch directory is checked in the container first (v4), so a missing
# or unwritable one fails the stage here by name, not later as ENOSPC or EACCES.
# shellcheck disable=SC2016 # expands in the container, not here
guard='[ -d "$TMPDIR" ] && [ -w "$TMPDIR" ] || { echo "in-toolchain-image: scratch directory $TMPDIR is missing or not writable" >&2; exit 2; }'
docker create "${args[@]}" "$image" sh -c "$guard; $cmd" >/dev/null
# DS-3998 (ruling 108): the volume roots go to the run's uid only after the
# create, while the stage's container holds the volumes. Another job's
# scripts/trim-ci-caches.sh removes a cache volume no container holds, so a
# chown done before the create can be lost: the create then makes a new,
# root-owned volume, and the stage cannot write it (run 68164, job 317602:
# "mkdir /cache/gomod/cache: permission denied" after job 317749 trimmed
# cache-main-gomod 1-2 s earlier). Docker refuses to remove a volume that a
# created container names, and the trim keeps it as in use.
if [ "$cache" = on ]; then
  docker run --rm --network none --cap-drop ALL --cap-add CHOWN \
    --security-opt no-new-privileges:true --user 0:0 \
    -v "$vols-gobuild:/c/gobuild" -v "$vols-golangci:/c/golangci" -v "$vols-gomod:/c/gomod" \
    "$image" sh -c "for d in /c/*; do [ \"\$(stat -c %u:%g \"\$d\")\" = $uid:$uid ] || chown $uid:$uid \"\$d\"; done" >/dev/null
fi
tar -C "$PWD" --owner="$uid" --group="$uid" --numeric-owner -cf - . |
  docker cp -a - "$name:/src"
docker start "$name" >/dev/null
# CUSTOMIZE (go-qrdb) begin: the multicast configurator. It joins the stage's
# namespace, root in it for the two `ip` commands with NET_ADMIN its only
# capability, and exits; --rm has the daemon remove it on that exit, so it
# outlives nothing, whatever kills this script. The stage never holds
# NET_ADMIN.
if [ "$network" = none ]; then
  docker run --rm --name "$netns" --label "qrdb.ci.job=$job" \
    --network "container:$name" --read-only --cap-drop ALL --cap-add NET_ADMIN \
    --security-opt no-new-privileges:true --pids-limit 16 --user 0:0 \
    "$image" sh -c 'ip link set lo multicast on && ip route add 224.0.0.0/4 dev lo' >/dev/null
fi
# CUSTOMIZE (go-qrdb) end.
# The stage's output from its start, its exit status after. The stream runs
# in the background so a signal reaches the traps at once: bash runs a trap
# only after a foreground command returns.
docker logs -f "$name" &
wait "$!" || true
code=$(docker wait "$name")
exit "$code"
