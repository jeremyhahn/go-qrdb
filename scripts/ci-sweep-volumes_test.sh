#!/usr/bin/env bash
# Tests for scripts/ci-sweep-volumes.sh against a stub docker on PATH;
# run by `make test-in-toolchain-image`. No case reaches a docker daemon.
#
#   usage: scripts/ci-sweep-volumes_test.sh
set -u

here=$(cd "$(dirname "$0")" && pwd)
sweep="$here/ci-sweep-volumes.sh"
pass=0
fail=0

ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
mkdir -p "$D/bin"

# The stub serves volumes from $D/vols: one line per volume,
# "<name> <created-epoch> <dangling 0|1> <inuse 0|1>". It logs each rm
# to $D/rm.log and fails rm of an in-use volume the way docker does.
cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -u
vols="$STUB_DIR/vols"
case "$1 $2" in
"volume ls")
  dangling_only=0
  for a in "$@"; do [ "$a" = "dangling=true" ] && dangling_only=1; done
  while read -r n _ d _; do
    [ -n "$n" ] || continue
    if [ "$dangling_only" = 1 ] && [ "$d" != 1 ]; then continue; fi
    printf '%s\n' "$n"
  done < "$vols"
  ;;
"volume inspect")
  name=${!#}
  while read -r n c _ _; do
    if [ "$n" = "$name" ]; then
      date -u -d "@$c" '+%Y-%m-%dT%H:%M:%SZ'
      exit 0
    fi
  done < "$vols"
  echo "Error: No such volume: $name" >&2
  exit 1
  ;;
"volume rm")
  name=${!#}
  while read -r n _ _ u; do
    if [ "$n" = "$name" ]; then
      if [ "$u" = 1 ]; then
        echo "Error response from daemon: remove $name: volume is in use - [abc]" >&2
        exit 1
      fi
      printf '%s\n' "$name" >> "$STUB_DIR/rm.log"
      printf '%s\n' "$name"
      exit 0
    fi
  done < "$vols"
  echo "Error: No such volume: $name" >&2
  exit 1
  ;;
*)
  echo "stub docker: unexpected: $*" >&2
  exit 2
  ;;
esac
STUB
chmod +x "$D/bin/docker"

export STUB_DIR="$D"
export PATH="$D/bin:$PATH"

now=$(date +%s)
old=$((now - 4 * 3600))
young=$((now - 600))
hex=$(printf 'a%.0s' $(seq 1 64))

# run <args...>: runs the sweep on a fresh rm.log; leaves rc and output.
run() {
  : > "$D/rm.log"
  out=$("$sweep" "$@" 2>&1)
  rc=$?
}

removed() {
  local l
  while read -r l; do [ "$l" = "$1" ] && return 0; done < "$D/rm.log"
  return 1
}

if [ ! -x "$sweep" ]; then
  bad "scripts/ci-sweep-volumes.sh exists and is executable"
  printf '%d passed, %d failed\n' "$pass" "$fail"
  exit 1
fi

# (1)-(5): one listing covers every age, name and use case.
cat > "$D/vols" <<EOF
qrdb-ci-111-1-5_gocache $old 1 0
qrdb-ci-222-1-3_gocache $young 1 0
qrdb-ci-333-2-1_gocache $old 1 1
go-qrdb-ci-tc-111-2-mcast $old 1 0
go-qrdb-ci-tc-444-1-mcast $young 1 0
go-qrdb-ci-cache-gobuild $old 1 0
$hex $old 1 0
go-quicbaas-ci-9-1_x $old 1 0
qrdb-ci-555-1-soak_gocache $old 0 0
EOF
unset IT_PROJECT
run
if [ "$rc" = 0 ]; then ok "sweep exits 0"; else bad "sweep exits 0 (rc=$rc: $out)"; fi
if removed qrdb-ci-111-1-5_gocache; then ok "(1) stale shard volume removed"; else bad "(1) stale shard volume removed"; fi
if ! removed qrdb-ci-222-1-3_gocache; then ok "(2) young shard volume kept"; else bad "(2) young shard volume kept"; fi
if ! removed qrdb-ci-333-2-1_gocache && [[ $out == *qrdb-ci-333-2-1_gocache* ]]; then
  ok "(3) in-use volume kept and named"
else
  bad "(3) in-use volume kept and named ($out)"
fi
if removed go-qrdb-ci-tc-111-2-mcast; then ok "(4) stale mcast volume removed"; else bad "(4) stale mcast volume removed"; fi
if ! removed go-qrdb-ci-tc-444-1-mcast; then ok "(4) young mcast volume kept"; else bad "(4) young mcast volume kept"; fi
for keep in go-qrdb-ci-cache-gobuild "$hex" go-quicbaas-ci-9-1_x; do
  if ! removed "$keep"; then ok "(5) $keep never removed"; else bad "(5) $keep never removed"; fi
done
if ! removed qrdb-ci-555-1-soak_gocache; then ok "non-dangling volume kept"; else bad "non-dangling volume kept"; fi

# SWEEP_MIN_AGE moves the age bound.
SWEEP_MIN_AGE=5m run
if removed qrdb-ci-222-1-3_gocache; then ok "SWEEP_MIN_AGE=5m removes a 10 min volume"; else bad "SWEEP_MIN_AGE=5m removes a 10 min volume"; fi

# (6) --own removes the current run's project volumes regardless of age.
cat > "$D/vols" <<EOF
qrdb-ci-777-1-4_gocache $young 1 0
qrdb-ci-777-1-40_gocache $young 1 0
qrdb-ci-778-1-4_gocache $young 1 0
go-qrdb-ci-cache-gobuild $young 1 0
EOF
IT_PROJECT=qrdb-ci-777-1-4 run --own
if [ "$rc" = 0 ]; then ok "(6) --own exits 0"; else bad "(6) --own exits 0 (rc=$rc: $out)"; fi
if removed qrdb-ci-777-1-4_gocache; then ok "(6) own project volume removed"; else bad "(6) own project volume removed"; fi
if ! removed qrdb-ci-777-1-40_gocache && ! removed qrdb-ci-778-1-4_gocache; then
  ok "(6) other runs' young volumes kept"
else
  bad "(6) other runs' young volumes kept"
fi

# A daemon that cannot be reached never fails the job.
cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
echo "Cannot connect to the Docker daemon" >&2
exit 1
STUB
run
if [ "$rc" = 0 ]; then ok "unreachable daemon: exit 0"; else bad "unreachable daemon: exit 0 (rc=$rc)"; fi

printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ]
