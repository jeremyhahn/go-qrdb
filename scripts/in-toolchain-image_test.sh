#!/usr/bin/env bash
# Tests for scripts/in-toolchain-image.sh (#1264): no container it starts can
# outlive its stage. A stub `docker` on PATH records every call; `docker rm`
# ends a running `docker logs -f`, as the daemon does when it removes the
# container, and STUB_BLOCK makes the stub stop at the configurator or the
# log stream and say so on a FIFO, so a test signals the script there without
# guessing at timing.
#
#   usage: scripts/in-toolchain-image_test.sh   (also: make test-in-toolchain-image)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
mkdir -p "$D/bin" "$D/src"
echo x > "$D/src/file"

cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -eu
s=$STUB_STATE
printf '%s\n' "$*" >> "$s/calls"
case $1 in
  create) printf '%s' "${*: -1}" > "$s/stage-cmd" ;;
  cp) cat > /dev/null ;;
  start) ;;
  run)
    case " $* " in
      *" --name "*)
        echo configured >> "$s/events"
        if [ "${STUB_BLOCK:-}" = config ]; then echo config > "$s/sync"; sleep 0.3; fi
        # STUB_CONFIG_FAILS=n: the first n configurator runs fail as a run
        # whose stage has exited does.
        if [ "$(grep -c configured "$s/events")" -le "${STUB_CONFIG_FAILS:-0}" ]; then exit 125; fi ;;
    esac ;;
  volume) [ "$2" = rm ] && echo "volume:$3" >> "$s/removed" ;;
  logs)
    if [ "${STUB_BLOCK:-}" = logs ]; then
      echo $$ > "$s/logs-pid"
      echo logs > "$s/sync"
      exec sleep 30
    fi
    echo "stage output" ;;
  wait) echo "${STUB_EXIT:-0}" ;;
  rm)
    shift
    for c in "$@"; do case $c in -*) ;; *) echo "$c" >> "$s/removed" ;; esac; done
    if [ -f "$s/logs-pid" ]; then kill "$(cat "$s/logs-pid")" 2>/dev/null || true; fi ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$D/bin/docker"

reset() {
  rm -rf "$D/state"
  mkdir -p "$D/state"
  mkfifo "$D/state/sync"
}
# run [env...]: the script in the foreground, its status in $rc.
run() {
  rc=0
  (cd "$D/src" && env PATH="$D/bin:$PATH" STUB_STATE="$D/state" CI_CACHE=off \
    IN_IMAGE_CPUS=2 CI_STACK_PREFIX=p- GITHUB_RUN_ID=77 GITHUB_JOB=image "$@" \
    "$here/in-toolchain-image.sh" img make test) > "$D/out" 2>&1 || rc=$?
}
# signalled <block> <signal>: the script stopped at <block> and sent <signal>;
# its status in $rc. Job control keeps SIGINT deliverable to a background job.
signalled() {
  set -m
  (cd "$D/src" && exec env PATH="$D/bin:$PATH" STUB_STATE="$D/state" CI_CACHE=off \
    IN_IMAGE_CPUS=2 CI_STACK_PREFIX=p- STUB_BLOCK="$1" \
    "$here/in-toolchain-image.sh" img make test) > "$D/out" 2>&1 &
  pid=$!
  set +m
  read -r _ < "$D/state/sync"
  kill -s "$2" "$pid"
  rc=0
  wait "$pid" || rc=$?
}
called() { grep -q -- "$1" "$D/state/calls"; }
removed() { grep -qx -- "$1" "$D/state/removed" 2>/dev/null; }
# both_removed: the stage and its configurator are both removed.
both_removed() {
  local stage
  stage=$(grep -m1 '^create ' "$D/state/calls" | grep -o -- '--name p-tc-[0-9]*-[0-9]*' | cut -d' ' -f2)
  [ -n "$stage" ] && removed "$stage" && removed "$stage-netns"
}

# 1. No container sleeps: the configurator is a one-shot --rm run that joins
# the stage's namespace after the stage has started, and nothing starts a
# holder of its own.
reset
run
if [ "$rc" = 0 ] && called '^run --rm --name p-tc-[0-9]*-[0-9]*-netns ' &&
  called ' --network container:p-tc-' && ! called 'sleep infinity' &&
  ! called '^run -d' && [ "$(grep -n '^start ' "$D/state/calls" | cut -d: -f1)" -lt \
  "$(grep -n -- '-netns ' "$D/state/calls" | cut -d: -f1)" ]; then
  ok "the configurator is a one-shot --rm run after the stage starts"
else bad "the configurator is a one-shot --rm run after the stage starts"; cat "$D/state/calls"; fi

# 2. Both containers carry the job label.
if [ "$(grep -c -- '--label qrdb.ci.job=77-image ' "$D/state/calls")" = 2 ] &&
  called '^create .*--network none '; then
  ok "the stage and the configurator carry qrdb.ci.job"
else bad "the stage and the configurator carry qrdb.ci.job"; cat "$D/state/calls"; fi

# 3. A normal exit removes both, and the stage's status is the script's.
reset
run STUB_EXIT=7
if [ "$rc" = 7 ] && both_removed && grep -q 'stage output' "$D/out"; then
  ok "a normal exit removes both and returns the stage's status"
else bad "a normal exit removes both and returns the stage's status (rc=$rc)"; cat "$D/out"; fi

# 4-6. Each signal on the log stream removes both, at once, with 128+n.
for t in HUP:129 INT:130 TERM:143; do
  reset
  signalled logs "${t%%:*}"
  if [ "$rc" = "${t##*:}" ] && both_removed; then
    ok "SIG${t%%:*} during the stage removes both"
  else bad "SIG${t%%:*} during the stage removes both (rc=$rc)"; cat "$D/out"; fi
done

# 7. A signal while the configurator runs removes both too.
reset
signalled config TERM
if [ "$rc" = 143 ] && both_removed; then
  ok "SIGTERM during the configurator removes both"
else bad "SIGTERM during the configurator removes both (rc=$rc)"; cat "$D/out"; fi

# 8. A bridge stage has its own network: no configurator, no multicast wait.
reset
run IN_IMAGE_NETWORK=bridge
if [ "$rc" = 0 ] && ! called '^run ' && ! grep -q 'ip route' "$D/state/stage-cmd"; then
  ok "a bridge stage starts no configurator"
else bad "a bridge stage starts no configurator"; cat "$D/state/calls"; fi

# 9-10. The stage's command waits for the configurator's markers and the
# route, then runs; without them it gives up MULTICAST_WAIT seconds after the
# configurator started. The gate runs here with the marker directory in $D/m
# and an `ip` that shows the route while $D/route exists. The replay sets the
# scratch directory the stage's guard checks, as the stage's container does,
# so it does not depend on the caller's TMPDIR.
mkdir -p "$D/ipbin" "$D/scratch"
printf '#!/bin/sh\n[ -e "%s/route" ] && echo "224.0.0.0/4 scope link"\nexit 0\n' "$D" > "$D/ipbin/ip"
chmod +x "$D/ipbin/ip"
reset
run MULTICAST_WAIT=1
gate=$(cat "$D/state/stage-cmd")
gate=${gate//\/run\/qrdb-mcast/$D/m}
gate=${gate%make test}
# markers <files...>: the marker directory holds exactly <files>.
markers() { rm -rf "$D/m" "$D/route"; mkdir -p "$D/m"; for f in "$@"; do : > "$D/$f"; done; }
markers m/started.1 m/ready.1 route
if (PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${gate}echo ran") | grep -qx ran; then
  ok "the stage runs once the configurator is ready and the route is there"
else bad "the stage runs once the configurator is ready and the route is there"; echo "$gate"; fi
# The retry has started too, so its MULTICAST_WAIT is the last bound.
markers m/started.1 m/started.2
grc=0
(PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${gate}echo ran") > "$D/gate" 2>&1 || grc=$?
if [ "$grc" = 1 ] && grep -q 'no multicast route 1 s after the configurator started' "$D/gate" &&
  ! grep -q ran "$D/gate"; then
  ok "the stage gives up without the route"
else bad "the stage gives up without the route (rc=$grc)"; cat "$D/gate"; fi

# 12. The route alone is not the signal: until the configurator writes
# `ready`, the stage waits, and gives up on the same bound (#1275).
markers m/started.1 m/started.2 route
grc=0
(PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${gate}echo ran") > "$D/gate" 2>&1 || grc=$?
if [ "$grc" = 1 ] && ! grep -q ran "$D/gate"; then
  ok "the stage waits for the ready marker, not only the route"
else bad "the stage waits for the ready marker, not only the route (rc=$grc)"; cat "$D/gate"; fi

# 13. The clock starts when the configurator does: a configurator that starts
# 2 s into the stage, past a 1 s MULTICAST_WAIT, still lets the stage run.
markers
(sleep 2; : > "$D/m/started.1"; : > "$D/route"; : > "$D/m/ready.1") &
helper=$!
if (PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${gate}echo ran") 2>&1 | grep -qx ran; then
  ok "the clock starts when the configurator starts"
else bad "the clock starts when the configurator starts"; fi
wait "$helper"

# 14. The configurator mounts the marker volume the stage mounts read-only and
# creates labelled, writes `started.1` first and `ready.1` after the route.
stage=$(grep -m1 '^create ' "$D/state/calls" | grep -o -- '--name p-tc-[0-9]*-[0-9]*' | cut -d' ' -f2)
if called "^create .*--mount type=volume,src=$stage-mcast,dst=/run/qrdb-mcast,readonly,volume-label=qrdb.ci.multicast-marker=1 " &&
  called "^run --rm --name $stage-netns .*-v $stage-mcast:/m img sh -c touch /m/started.1 && ip link .*ip route replace 224.0.0.0/4 dev lo && touch /m/ready.1$" &&
  removed "volume:$stage-mcast"; then
  ok "the configurator writes the markers on the stage's marker volume, removed after"
else bad "the configurator writes the markers on the stage's marker volume, removed after"; cat "$D/state/calls"; fi

# 15. A failed configurator is retried once, and the stage then runs.
reset
run STUB_CONFIG_FAILS=1
if [ "$rc" = 0 ] && [ "$(grep -c configured "$D/state/events")" = 2 ] &&
  grep -q 'multicast configurator failed; retrying once' "$D/out" &&
  called 'touch /m/started.1 .*touch /m/ready.1$' && called 'touch /m/started.2 .*touch /m/ready.2$'; then
  ok "a failed configurator is retried once"
else bad "a failed configurator is retried once (rc=$rc)"; cat "$D/out"; fi

# 16. A second failure fails the script, and both containers and the marker
# volume are removed.
reset
run STUB_CONFIG_FAILS=2
if [ "$rc" = 125 ] && [ "$(grep -c configured "$D/state/events")" = 2 ] && both_removed &&
  grep -q '^volume:p-tc-.*-mcast$' "$D/state/removed"; then
  ok "a configurator failing twice fails the script and removes everything"
else bad "a configurator failing twice fails the script and removes everything (rc=$rc)"; cat "$D/out"; fi

# 17. A bridge stage has no marker volume to remove.
reset
run IN_IMAGE_NETWORK=bridge
if ! called 'mcast'; then
  ok "a bridge stage has no marker volume"
else bad "a bridge stage has no marker volume"; cat "$D/state/calls"; fi

# 11. MULTICAST_WAIT is a positive integer.
reset
run MULTICAST_WAIT=0
if [ "$rc" = 2 ] && grep -q 'MULTICAST_WAIT must be' "$D/out"; then
  ok "a bad MULTICAST_WAIT is refused"
else bad "a bad MULTICAST_WAIT is refused (rc=$rc)"; cat "$D/out"; fi

# 18. A configurator that never starts (its launcher died first) ends the
# stage MULTICAST_START_WAIT seconds after the stage started, not never.
reset
run MULTICAST_START_WAIT=1
sgate=$(cat "$D/state/stage-cmd")
sgate=${sgate//\/run\/qrdb-mcast/$D/m}
sgate=${sgate%make test}
markers
grc=0
(PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${sgate}echo ran") > "$D/gate" 2>&1 || grc=$?
if [ "$grc" = 1 ] && grep -q 'in-toolchain-image: the configurator did not start within 1 s' "$D/gate" &&
  ! grep -q ran "$D/gate"; then
  ok "the stage gives up when the configurator never starts"
else bad "the stage gives up when the configurator never starts (rc=$grc)"; cat "$D/gate"; fi

# 19. `started` then `ready`, within the start bound, still runs the stage.
markers
(sleep 0.3; : > "$D/m/started.1"; : > "$D/route"; : > "$D/m/ready.1") &
helper=$!
if (PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${sgate}echo ran") 2>&1 | grep -qx ran; then
  ok "a configurator starting within MULTICAST_START_WAIT lets the stage run"
else bad "a configurator starting within MULTICAST_START_WAIT lets the stage run"; fi
wait "$helper"

# 21. A first attempt that wrote `started.1` and then failed does not hold the
# retry to its route clock: the stage waits MULTICAST_START_WAIT afresh for
# `started.2`, so a retry the daemon starts past the first attempt's
# MULTICAST_WAIT still lets the stage run (#1275, Codex review 8934).
reset
run MULTICAST_START_WAIT=3 MULTICAST_WAIT=1
rgate=$(cat "$D/state/stage-cmd")
rgate=${rgate//\/run\/qrdb-mcast/$D/m}
rgate=${rgate%make test}
markers m/started.1
(sleep 2; : > "$D/m/started.2"; : > "$D/route"; : > "$D/m/ready.2") &
helper=$!
if (PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${rgate}echo ran") 2>&1 | grep -qx ran; then
  ok "a retry started past the first attempt's MULTICAST_WAIT lets the stage run"
else bad "a retry started past the first attempt's MULTICAST_WAIT lets the stage run"; fi
wait "$helper"

# 20. MULTICAST_START_WAIT is a positive integer.
reset
run MULTICAST_START_WAIT=4m
if [ "$rc" = 2 ] && grep -q 'MULTICAST_START_WAIT must be a positive integer: 4m' "$D/out"; then
  ok "a bad MULTICAST_START_WAIT is refused"
else bad "a bad MULTICAST_START_WAIT is refused (rc=$rc)"; cat "$D/out"; fi

# 12-13. The stage's command checks its scratch directory before anything else
# runs: an unset or missing TMPDIR fails it by name with status 2.
reset
run
gate=$(cat "$D/state/stage-cmd")
# scratch_refused <what> <env argv...>: the stage's command, replayed under env
# with <env argv>, exits 2 naming the scratch directory and runs nothing.
scratch_refused() {
  local what=$1 grc=0
  shift
  (PATH="$D/ipbin:$PATH" ROUTE=1 env "$@" sh -c "${gate%make test}echo ran") > "$D/gate" 2>&1 || grc=$?
  if [ "$grc" = 2 ] && grep -q 'scratch directory .* is missing or not writable' "$D/gate" && ! grep -q ran "$D/gate"; then
    ok "a scratch directory that is $what fails the stage"
  else bad "a scratch directory that is $what fails the stage (rc=$grc)"; cat "$D/gate"; fi
}
scratch_refused unset -u TMPDIR
scratch_refused missing TMPDIR="$D/no-such-dir"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
