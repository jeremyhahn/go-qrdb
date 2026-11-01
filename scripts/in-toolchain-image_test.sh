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
        if [ "${STUB_BLOCK:-}" = config ]; then echo config > "$s/sync"; sleep 0.3; fi ;;
    esac ;;
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

# 9-10. The stage's command waits for the route, then runs; without the route
# it gives up after MULTICAST_WAIT seconds. The replay sets the scratch
# directory the stage's guard checks, as the stage's container does, so it
# does not depend on the caller's TMPDIR.
mkdir -p "$D/ipbin" "$D/scratch"
# shellcheck disable=SC2016 # the stub expands ROUTE when it runs
printf '#!/bin/sh\n[ -n "${ROUTE:-}" ] && echo "224.0.0.0/4 scope link"\nexit 0\n' > "$D/ipbin/ip"
chmod +x "$D/ipbin/ip"
reset
run MULTICAST_WAIT=1
gate=$(cat "$D/state/stage-cmd")
if (PATH="$D/ipbin:$PATH" ROUTE=1 TMPDIR="$D/scratch" sh -c "${gate%make test}echo ran") | grep -qx ran; then
  ok "the stage runs once the route is there"
else bad "the stage runs once the route is there"; echo "$gate"; fi
grc=0
(PATH="$D/ipbin:$PATH" TMPDIR="$D/scratch" sh -c "${gate%make test}echo ran") > "$D/gate" 2>&1 || grc=$?
if [ "$grc" = 1 ] && grep -q 'no multicast route after 1 s' "$D/gate" && ! grep -q ran "$D/gate"; then
  ok "the stage gives up without the route"
else bad "the stage gives up without the route (rc=$grc)"; cat "$D/gate"; fi

# 11. MULTICAST_WAIT is a positive integer.
reset
run MULTICAST_WAIT=0
if [ "$rc" = 2 ] && grep -q 'MULTICAST_WAIT must be' "$D/out"; then
  ok "a bad MULTICAST_WAIT is refused"
else bad "a bad MULTICAST_WAIT is refused (rc=$rc)"; cat "$D/out"; fi

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
