#!/usr/bin/env bash
# Run a command in the toolchain image against a COPY of the checkout.
#
#   usage: scripts/in-toolchain-image.sh <image> <command...>
#   e.g.   scripts/in-toolchain-image.sh go-enclave-toolchain:abc make build
#
# in-toolchain-template: v5 (ci-template v1.34.1). Ships beside ci.yml; copy it to
# scripts/ with the workflow. From go-quicraft's scripts/in-toolchain-image.sh
# (a141c3c), with the container prefix and uid taken from the environment.
#
# How CI runs a stage in the devcontainer image (handbook ci-standard.md §2).
# This creates the container, streams the checkout into it (`.git` included:
# the releasability tests read it) owned by the image's non-root user, then
# starts it and returns the command's exit status. A copy rather than a bind
# mount of the checkout keeps the stage's writes off the runner's tree and
# works the same against a daemon on another host, reached through a socket;
# on a workstation `make docker-run CMD=...` (bind mount) is the shorter path.
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
# The stage's command waits for that route before it runs. The configurator
# says so itself, on a marker volume (<stage>-mcast) the stage mounts
# read-only, labelled qrdb.ci.multicast-marker=1, so a sweep of a long-lived
# daemon tells one a killed launcher left from any other volume. Each
# attempt n (the run, then its one retry) writes `started.<n>` when it runs and
# `ready.<n>` once the route is in place. The stage's MULTICAST_WAIT clock
# starts at `started.<n>`, never at its own start: a loaded daemon can take minutes to start the configurator, and a
# clock that counted that wait ran out before the route existed (#1275). Until
# `started.1` the stage runs a clock of its own, MULTICAST_START_WAIT, which
# starts with the stage and is sized for those minutes; this script removes
# the stage sooner if the configurator fails twice, and the clock ends the
# stage if this script dies first. So each attempt has two clocks:
# MULTICAST_START_WAIT to `started.<n>`, then MULTICAST_WAIT from it to a
# `ready` marker and the route. A `started` of the next attempt ends the
# current one's clocks and starts its MULTICAST_WAIT; an attempt whose
# MULTICAST_WAIT runs out before the last one restarts MULTICAST_START_WAIT
# for the retry, so a retry the daemon starts late is not judged on the clock
# of the attempt that failed. The namespace is the stage's, so no container holds it
# but the stage: a holder that slept until the script removed it outlived
# every job killed before its EXIT trap ran (#1264).
#
# A networked stage writes its module cache: to the gomod volume with the
# named-volume cache, which keeps what it fetched across runs; otherwise (a
# CI_CACHE_DIR cache, or CI_CACHE=off) to a writable copy of the baked module
# cache at /home/dev/gomodcache rather than an empty one under /tmp (see the
# bridge branch below).
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
# v2 (ci-template v1.22, Company Agent m616 item 2): the caches live outside
# the container's writable layer and its /tmp: GOCACHE at /cache/gobuild and
# GOLANGCI_LINT_CACHE at /cache/golangci for every stage (a no-network stage
# keeps the image's own read-only module cache, so it still proves the image
# works offline). Where they live is the caller's choice:
#
#   - CI_CACHE_DIR, an absolute directory on the daemon's host, is the hosted
#     runner's: its gobuild and golangci subdirectories are bind-mounted, and
#     the workflow restores and saves that directory with actions/cache
#     (docs/release/engineering.md, "The CI image"), so a runner VM that lives
#     for one job starts from main's last cache. The daemon runs on the runner
#     VM, so the path resolves. A bridge stage takes the copy of the baked
#     module cache (above): what it fetches is not worth an entry in the
#     repository's bounded cache storage.
#   - Unset, the caches are named volumes on the daemon's host,
#     <CI_STACK_PREFIX>cache-{gobuild,golangci,gomod}, kept across runs: a
#     developer's long-lived daemon. A bridge stage mounts the gomod volume at
#     /cache/gomod as its GOMODCACHE.
#
# Either way the cache's files are given to IN_IMAGE_UID by a helper container
# holding CHOWN and nothing else, run only once the stage's container is
# created (DS-3998, below). The files stay world-readable, so the runner's own
# user can save the directory after the stage. CI_CACHE=off mounts none (v1).
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
# the job container's cgroup memory.max (when the job has no limit, as on a
# hosted runner VM, the host's MemTotal less 2 GiB for the runner and the
# daemon, or 8g if that cannot be read; IN_IMAGE_MEMORY, a size such as 12g,
# overrides) becomes --memory, and
# --memory-swap is set to the same size, so the stage gets no swap: a stage
# that outgrows it is OOM-killed and fails, instead of swapping the shared
# runner host. The 2 GiB /dev/shm is charged to the same limit. A 16 GB hosted
# runner gives about 13g.
#
# CI_CGROUP_PARENT (default empty: none) is the container's --cgroup-parent,
# for a shared host whose slice must bound every CI container together,
# wherever the daemon's own default parent points (go-trusted-platform
# 0216d8e, #1178). A hosted runner VM is the job's alone and sets none.
#
# IN_IMAGE_COPY_OUT, a path relative to the checkout, is copied out of the
# stage's container to the same path in the runner's checkout once the stage
# has exited, whatever its status: the copy-in has no way back, and a stage
# whose product a later job reads (a coverage leg's profile) names it here.
# An absolute path or one with a `.` or `..` component is refused up front.
#
# The container is named <CI_STACK_PREFIX>tc-<pid>-<random> (default prefix
# <checkout dir>-ci-), and is removed on every exit, INT, TERM and HUP
# included.
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
# first when the runner sets it: <DEVCACHE_GOPROXY>|https://proxy.golang.org,
# so any error falls through to the public proxy; GOPRIVATE travels with it.
# v5 (ci-template v1.34.1, dependency-resolution.md §7a): the list has no
# direct entry, so a module the proxy cannot serve fails the stage instead of
# being fetched from its origin.
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
cache_dir=${CI_CACHE_DIR:-}
case $cache_dir in
  "" | /*) ;;
  *) echo "CI_CACHE_DIR must be an absolute path: $cache_dir" >&2; exit 2 ;;
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
    max | "" | *[!0-9]*)
      # No cgroup bound: the host's memory less 2 GiB for the runner and the
      # daemon (a hosted runner VM), 8g when /proc/meminfo cannot say.
      total=$(awk '/^MemTotal:/ { print int($2 / 1024); exit }' /proc/meminfo 2>/dev/null || true)
      case $total in
        "" | *[!0-9]*) memory=8g ;;
        *) if [ "$total" -gt 4096 ]; then memory="$((total - 2048))m"; else memory=8g; fi ;;
      esac ;;
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
copy_out=${IN_IMAGE_COPY_OUT:-}
case /$copy_out/ in
  //) ;;
  /[/.]* | *//* | */../* | */./*) echo "IN_IMAGE_COPY_OUT must be a relative path inside the checkout: $copy_out" >&2; exit 2 ;;
esac
cgroup_parent=${CI_CGROUP_PARENT:-}
if [ -n "$cgroup_parent" ] && ! [[ $cgroup_parent =~ ^[A-Za-z0-9][A-Za-z0-9_.@:/-]*$ ]]; then
  echo "CI_CGROUP_PARENT must be a cgroup name such as ci.slice, or empty: $cgroup_parent" >&2
  exit 2
fi
# CUSTOMIZE (go-qrdb) begin: the multicast configurator and the job label.
netns="$name-netns"
mcast="$name-mcast" # CUSTOMIZE (go-qrdb): the configurator's marker volume
job="${GITHUB_RUN_ID:-local}-${GITHUB_JOB:-local}"
# The stage's wait for the multicast route: the configurator's `docker run`
# on an image the daemon holds, never the stage's own run time.
mwait=${MULTICAST_WAIT:-60}
case $mwait in
  "" | 0 | *[!0-9]*) echo "MULTICAST_WAIT must be a positive integer: $mwait" >&2; exit 2 ;;
esac
# The stage's wait for the configurator to start, so a stage whose launcher
# died before the configurator ran does not wait forever. 240 s: in the run
# #1275 records (70401) a loaded shared daemon started no container for over a
# minute, so each of the configurator's two runs (one retry) is allowed two
# minutes to start: 2 x 120 s.
tries=2 # the configurator's run and its one retry (below)
swait=${MULTICAST_START_WAIT:-240}
case $swait in
  "" | 0 | *[!0-9]*) echo "MULTICAST_START_WAIT must be a positive integer: $swait" >&2; exit 2 ;;
esac
# CUSTOMIZE (go-qrdb) end.
# The daemon refuses --cpus above its own CPU count, and the job container can
# see more CPUs than the daemon that runs the stage (a pooled runner host), so
# the bound is capped at the daemon's NCPU when the daemon reports one. Asked
# only after every input is validated, so a refused input makes no docker call.
if ! daemon_cpus=$(docker info --format '{{.NCPU}}' 2>/dev/null); then daemon_cpus=; fi
case $daemon_cpus in
  "" | 0 | *[!0-9]*) ;;
  *) if [ "$cpus" -gt "$daemon_cpus" ]; then cpus=$daemon_cpus; fi ;;
esac
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
# The cache sources, by mount point: the CI_CACHE_DIR subdirectories or the
# named volumes (v2, above). The chown helper below mounts the same sources.
cache_mounts=()
if [ "$cache" = on ]; then
  if [ -n "$cache_dir" ]; then
    mkdir -p "$cache_dir/gobuild" "$cache_dir/golangci"
    cache_mounts=("$cache_dir/gobuild:/cache/gobuild" "$cache_dir/golangci:/cache/golangci")
  else
    cache_mounts=("${prefix}cache-gobuild:/cache/gobuild" "${prefix}cache-golangci:/cache/golangci")
    if [ "$network" = bridge ]; then
      cache_mounts+=("${prefix}cache-gomod:/cache/gomod")
      modcache=/cache/gomod
    fi
  fi
  for m in "${cache_mounts[@]}"; do args+=(-v "$m"); done
  args+=(--env GOCACHE=/cache/gobuild --env GOLANGCI_LINT_CACHE=/cache/golangci)
fi
if [ "$network" = bridge ]; then
  # CUSTOMIZE (go-qrdb) begin: the baked module cache is read-only to the run's
  # user, and a networked stage writes to its cache: deps-check records the
  # newer versions it finds, govulncheck and trivy fetch what they scan. With
  # the named-volume cache that is the gomod volume, kept across runs;
  # otherwise the run takes a writable copy of the baked cache on the
  # container's own layer, so nothing baked is downloaded twice.
  args+=(--network bridge --env "GOPROXY=${DEVCACHE_GOPROXY:+$DEVCACHE_GOPROXY|}https://proxy.golang.org" --env "GOMODCACHE=$modcache")
  if [ "$modcache" = /home/dev/gomodcache ]; then cmd="cp -a /gomodcache/. $modcache/ && $cmd"; fi
  # CUSTOMIZE (go-qrdb) end.
  # ci-template v1.36: the Trivy database repositories the runner sets (the
  # dev-cache ghcr mirror on the LAN first, the public ones after) travel
  # with the stage, so `make security` and `trivy image` fetch the vulnerability
  # and Java databases from the LAN instead of the internet.
  for v in GOPRIVATE GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 \
    TRIVY_DB_REPOSITORY TRIVY_JAVA_DB_REPOSITORY TRIVY_INSECURE; do
    if [ -n "${!v:-}" ]; then args+=(--env "$v"); fi
  done
else
  # CUSTOMIZE (go-qrdb) begin: the stage's own namespace, which the
  # configurator below makes multicast-capable once the stage has started.
  # The marker volume is created with the stage, labelled, so a sweep of a
  # long-lived daemon can tell one a killed launcher left.
  args+=(--network none --mount "type=volume,src=$mcast,dst=/run/qrdb-mcast,readonly,volume-label=qrdb.ci.multicast-marker=1")
  # The gate below, in the stage: n is the configurator attempt it waits for
  # and s its phase, `start` (on MULTICAST_START_WAIT) or `route` (on
  # MULTICAST_WAIT); i counts 0.1 s ticks in the phase (see the header).
  # shellcheck disable=SC2016 # expands in the stage, not here
  gate='m=/run/qrdb-mcast; n=1; s=start; i=0; mready() { for r in "$m"/ready.*; do [ -e "$r" ] && return 0; done; return 1; };'
  gate="$gate"' until mready && ip route show dev lo | grep -q "^224\.0\.0\.0/4"; do'
  gate="$gate"' if [ $n -lt '"$tries"' ] && [ -e "$m/started.$((n + 1))" ]; then n=$((n + 1)); s=route; i=0;'
  gate="$gate"' elif [ $s = start ] && [ -e "$m/started.$n" ]; then s=route; i=0; fi; i=$((i + 1));'
  gate="$gate"' if [ $s = start ] && [ $i -gt '"$((swait * 10))"' ]; then'
  gate="$gate"' echo "in-toolchain-image: the configurator did not start within '"$swait"' s (attempt $n)" >&2; exit 1; fi;'
  gate="$gate"' if [ $s = route ] && [ $i -gt '"$((mwait * 10))"' ]; then if [ $n -ge '"$tries"' ]; then'
  gate="$gate"' echo "in-toolchain-image: no multicast route '"$mwait"' s after the configurator started (attempt $n)" >&2; exit 1; fi;'
  gate="$gate"' n=$((n + 1)); s=start; i=0; fi;'
  cmd="$gate sleep 0.1; done; $cmd"
  # CUSTOMIZE (go-qrdb) end.
fi

# shellcheck disable=SC2329 # invoked by the EXIT trap below
cleanup() {
  docker rm -f -v "$name" "$netns" >/dev/null 2>&1 || true
  if [ "$network" = none ]; then docker volume rm "$mcast" >/dev/null 2>&1 || true; fi
}
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
# DS-3998 (ruling 108): the cache goes to the run's uid only after the create,
# while the stage's container holds the volumes, so a volume removed between a
# chown and the create cannot come back root-owned (run 68164, job 317602:
# "mkdir /cache/gomod/cache: permission denied"). Every entry not already the
# run's is given to it: a CI_CACHE_DIR cache comes back from actions/cache
# owned by the runner's user, and the stage writes into it.
if [ "${#cache_mounts[@]}" -gt 0 ]; then
  helper=(docker run --rm --network none --cap-drop ALL --cap-add CHOWN
    --security-opt no-new-privileges:true --user 0:0)
  for m in "${cache_mounts[@]}"; do helper+=(-v "${m%%:*}:/c/${m##*/}"); done
  "${helper[@]}" "$image" find /c -mindepth 1 ! -user "$uid" -exec chown -h "$uid:$uid" {} + >/dev/null
fi
tar -C "$PWD" --owner="$uid" --group="$uid" --numeric-owner -cf - . |
  docker cp -a - "$name:/src"
docker start "$name" >/dev/null
# CUSTOMIZE (go-qrdb) begin: the multicast configurator. It joins the stage's
# namespace, root in it for the two `ip` commands with NET_ADMIN its only
# capability, and exits; --rm has the daemon remove it on that exit, so it
# outlives nothing, whatever kills this script. The stage never holds
# NET_ADMIN. It writes the stage's markers (see the header) on the marker
# volume, numbered by attempt (configure <n>); `ip route replace` makes the
# one retry safe after a run that got as far as the route.
configure() {
  docker run --rm --name "$netns" --label "qrdb.ci.job=$job" \
    --network "container:$name" --read-only --cap-drop ALL --cap-add NET_ADMIN \
    --security-opt no-new-privileges:true --pids-limit 16 --user 0:0 \
    -v "$mcast:/m" "$image" \
    sh -c "touch /m/started.$1 && ip link set lo multicast on && ip route replace 224.0.0.0/4 dev lo && touch /m/ready.$1" >/dev/null
}
if [ "$network" = none ] && ! configure 1; then
  echo "in-toolchain-image: the multicast configurator failed; retrying once" >&2
  docker rm -f "$netns" >/dev/null 2>&1 || true
  configure 2
fi
# CUSTOMIZE (go-qrdb) end.
# The stage's output from its start, its exit status after. The stream runs
# in the background so a signal reaches the traps at once: bash runs a trap
# only after a foreground command returns.
docker logs -f "$name" &
wait "$!" || true
code=$(docker wait "$name")
# The stage's product, copied out whatever its status, before the cleanup
# removes the container. A stage that wrote none says so here and its own
# status says why; the caller's next step finds the file missing.
if [ -n "$copy_out" ]; then
  mkdir -p "$(dirname "$PWD/$copy_out")"
  docker cp "$name:/src/$copy_out" "$PWD/$copy_out" >/dev/null 2>&1 ||
    echo "in-toolchain-image: the stage left no $copy_out" >&2
fi
exit "$code"
