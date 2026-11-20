#!/usr/bin/env bash
# Tests for scripts/ci-image.sh: the docker-build job's hand-off of the CI
# images to every other job. A stub `docker` on PATH records every call and
# keeps a one-image "daemon" in a state directory, so save, load and push run
# end to end without a daemon or a registry.
#
#   usage: scripts/ci-image_test.sh   (also: make test-ci-image)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }

D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
# The registry's outage marker goes to the job's temporary directory: here
# the stub's state, so reset clears it between cases.
export RUNNER_TEMP="$D/state"
mkdir -p "$D/bin"

# The stub's daemon: $s/tags/<tag> holds the ID a tag names (line 1) and the
# tag (line 2); $s/ids/<hex> marks an image present; $s/created/<hex> and
# $s/tagtime/<hex> its build and last-tag times (RFC 3339); $s/running lists
# "<container> <ID>" of running containers. `docker save` writes the saved
# ref's ID and the ref into the "tar"; `docker load` puts them back
# (STUB_LOAD_ID overrides the ID). STUB_RETAG_ON=save|push|rm moves the tag
# STUB_RETAG_TAG to STUB_RETAG_ID just before that command runs: another run
# retagging on a shared daemon. STUB_NO_DIGEST makes a push report no repo
# digest. STUB_PUSH_FAILS makes the first that many pushes fail with a
# registry 500, counted in $s/push-tries. A push records the pushed repo and
# the ID it pushed. The login's password is read from stdin and recorded apart
# from the argv log, so a test can tell it never reached a command line.
cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -eu
s=$STUB_STATE
printf '%s\n' "$*" >> "$s/calls"
# STUB_PROBE_LOCK=<file>: each call records whether a process holds the image
# lock (an exclusive try on a fresh open fails while anyone holds it).
if [ -n "${STUB_PROBE_LOCK:-}" ]; then
  if flock -x -n "$STUB_PROBE_LOCK" true; then echo "free $*"; else echo "held $*"; fi >> "$s/lockprobe"
fi
key() { printf '%s' "${1//[^A-Za-z0-9._-]/_}"; }
resolve() {
  case $1 in
    sha256:*) [ -e "$s/ids/${1#sha256:}" ] && printf '%s\n' "$1" ;;
    *) [ -f "$s/tags/$(key "$1")" ] && sed -n 1p "$s/tags/$(key "$1")" ;;
  esac
}
settag() {
  mkdir -p "$s/tags" "$s/ids"
  printf '%s\n%s\n' "$2" "$1" > "$s/tags/$(key "$1")"
  : > "$s/ids/${2#sha256:}"
}
retag() { [ "${STUB_RETAG_ON:-}" != "$1" ] || settag "$STUB_RETAG_TAG" "$STUB_RETAG_ID"; }
case $1 in
  image)
    case $2 in
      ls) for f in "$s"/tags/*; do [ -f "$f" ] && printf '%s %s\n' "$(sed -n 1p "$f")" "$(sed -n 2p "$f")"; done; exit 0 ;;
      rm)
        # As the daemon without --force (daemon/images/image_delete.go,
        # isSingleReference): an ID in use by a running container, or named
        # by more than one tag reference (two tags of one repository count
        # as two), is refused; otherwise the image goes with its one tag.
        retag rm
        id=${*: -1}; [ -e "$s/ids/${id#sha256:}" ] || exit 1
        if [ -f "$s/running" ] && cut -d' ' -f2 "$s/running" | grep -qx "$id"; then exit 1; fi
        files=()
        for f in "$s"/tags/*; do
          [ -f "$f" ] && [ "$(sed -n 1p "$f")" = "$id" ] || continue
          files+=("$f")
        done
        # --force untags every reference that names the ID at this moment.
        case " $* " in *" --force "*) ;; *) [ "${#files[@]}" -le 1 ] || exit 1 ;; esac
        rm -f "${files[@]}" "$s/ids/${id#sha256:}"; exit 0 ;;
    esac
    ref=${*: -1}
    id=$(resolve "$ref") || exit 1
    case $* in
      *RepoDigests*)
        if [ -z "${STUB_NO_DIGEST:-}" ]; then
          printf '%s\n' "other.example/x@sha256:$(printf 'a%.0s' $(seq 64))"
          [ ! -f "$s/pushed" ] || printf '%s@sha256:%s\n' "$(cat "$s/pushed")" "$(printf 'b%.0s' $(seq 64))"
          [ ! -f "$s/pulled" ] || cat "$s/pulled"
        fi ;;
      *Created*)
        printf '%s %s\n' "$(cat "$s/created/${id#sha256:}" 2>/dev/null || echo 2000-01-01T00:00:00Z)" \
          "$(cat "$s/tagtime/${id#sha256:}" 2>/dev/null || echo 0001-01-01T00:00:00Z)" ;;
      *) printf '%s\n' "$id" ;;
    esac ;;
  ps) [ -f "$s/running" ] && cut -d' ' -f1 "$s/running"; exit 0 ;;
  inspect) while read -r c i; do [ "$c" != "${*: -1}" ] || echo "$i"; done < "$s/running" ;;
  save) retag save; id=$(resolve "$4") || exit 1; printf '%s\n%s\n' "$id" "$4" > "$3" ;;
  load)
    id=${STUB_LOAD_ID:-$(sed -n 1p "$3")}; ref=$(sed -n 2p "$3")
    settag "$ref" "$id"; echo "Loaded image: $ref"
    # Another run on the same daemon loads its own build under the same
    # content-hash tag the moment this load returns.
    [ -z "${STUB_RACE_ID:-}" ] || settag "${ref%-run-*}" "$STUB_RACE_ID" ;;
  login) cat > "$s/password"; printf '%s\n' "${DOCKER_CONFIG:-}" > "$s/config" ;;
  tag) id=$(resolve "$2") || exit 1; settag "$3" "$id" ;;
  rmi)
    retag rm
    # By name the daemon untags; the image goes with its last tag.
    f="$s/tags/$(key "$2")"; [ -f "$f" ] || exit 1
    id=$(sed -n 1p "$f")
    if [ -f "$s/running" ] && cut -d' ' -f2 "$s/running" | grep -qx "$id"; then exit 1; fi
    rm -f "$f"
    for f in "$s"/tags/*; do [ -f "$f" ] && [ "$(sed -n 1p "$f")" = "$id" ] && exit 0; done
    rm -f "$s/ids/${id#sha256:}" ;;
  pull)
    # STUB_PULL: hit pulls the digest asked for (STUB_PULL_SERVES: another
    # one), whose repo digest `image inspect` then reports; hang never answers
    # (a registry that accepts the connection and stalls); anything else is a
    # miss. -q (the LAN registry pull's) is dropped.
    [ "$2" != -q ] || set -- "$1" "${@:3}"
    case ${STUB_PULL:-miss} in
      hit)
        d=${STUB_PULL_SERVES:-${2#*@}}
        settag "$2" "${STUB_PULL_ID:-sha256:pulled}"
        printf '%s@%s\n' "${2%@*}" "$d" > "$s/pulled"
        # STUB_PULL_THEN_PRUNE: another runner's prune on this daemon runs
        # the moment the pull returns, before the caller checks or tags.
        [ -z "${STUB_PULL_THEN_PRUNE:-}" ] || "$STUB_CI_IMAGE" prune 24 > "$s/then.err" 2>&1 || true
        echo 'Status: Downloaded newer image'; echo "Digest: $d" ;;
      hang) exec sleep 30 ;;
      refused) echo "dial tcp registry.test:5000: connect: connection refused" >&2; exit 1 ;;
      *) echo 'manifest unknown' >&2; exit 1 ;;
    esac ;;
  push)
    [ -z "${STUB_PUSH_HANG:-}" ] || exec sleep 30
    n=$(($(cat "$s/push-tries" 2>/dev/null || echo 0) + 1))
    printf '%s\n' "$n" > "$s/push-tries"
    if [ "$n" -le "${STUB_PUSH_FAILS:-0}" ]; then
      echo 'received unexpected HTTP status: 500 Internal Server Error' >&2
      exit 1
    fi
    retag push
    resolve "$2" > "$s/pushed-id"
    printf '%s\n' "${2%:*}" > "$s/pushed" ;;
  buildx)
    # The make targets' build (DEVCONTAINER_BUILD): it loads the image, or
    # with STUB_BUILD_FAILS fails; STUB_BUILD_RACE loads it first, as a
    # concurrent build on the same daemon does.
    case $2 in
      version) ;;
      build)
        img="" iid=""
        shift 2
        while [ $# -gt 0 ]; do
          case $1 in
            -t) img=$2; shift ;;
            --iidfile) iid=$2; shift ;;
          esac
          shift
        done
        [ -z "${STUB_BUILD_RACE:-}" ] || settag "$img" sha256:raced
        [ -z "${STUB_BUILD_FAILS:-}" ] || exit 7
        settag "$img" sha256:built
        [ -z "$iid" ] || printf 'sha256:built\n' > "$iid" ;;
      *) exit 1 ;;
    esac ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$D/bin/docker"

id_a="sha256:$(printf '1%.0s' $(seq 64))"
id_b="sha256:$(printf '2%.0s' $(seq 64))"
tc=qrdb-toolchain:0123456789abcdef
# The per-run tags of run 77, attempt 2 (run() sets both).
rtc=$tc-run-77-2
ptc=ghcr.io/example-org/go-qrdb-toolchain:0123456789abcdef-main-77-2

# reset: a daemon holding $tc as $id_a, which its build wrote to $D/iid.
reset() {
  rm -rf "$D/state" "$D/art"
  mkdir -p "$D/state"
  tagimg "$tc" "$id_a"
  # From the registry cases on, the integration image is held too.
  [ -z "${it:-}" ] || tagimg "$it" "$id_a"
  printf '%s\n' "$id_a" > "$D/iid"
}
tagimg() {
  mkdir -p "$D/state/tags" "$D/state/ids"
  printf '%s\n%s\n' "$2" "$1" > "$D/state/tags/${1//[^A-Za-z0-9._-]/_}"
  : > "$D/state/ids/${2#sha256:}"
}
# fresh: the consumer's daemon, which holds no image until the load.
fresh() { rm -rf "$D/state/tags" "$D/state/ids"; }
# resolve <ref>: the ID the stub daemon launches for <ref>.
resolve() { env STUB_STATE="$D/state" "$D/bin/docker" image inspect -f '{{.Id}}' "$1" 2>/dev/null; }
# run <args...> [-- env...]: the script, its status in $rc, stdout in $D/out,
# stderr in $D/err.
run() {
  local args=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do args+=("$1"); shift; done
  [ $# -eq 0 ] || shift
  rc=0
  env PATH="$D/bin:$PATH" STUB_STATE="$D/state" GITHUB_REPOSITORY_OWNER=Example-Org CI_IMAGE_IDS="$D/no-ids" CI_IMAGE_LOCK="$D/image.lock" \
    GITHUB_RUN_ID=77 GITHUB_RUN_ATTEMPT=2 "$@" \
    "$here/ci-image.sh" "${args[@]}" > "$D/out" 2> "$D/err" || rc=$?
}
called() { grep -q -- "$1" "$D/state/calls" 2>/dev/null; }

# 1. save writes the tar and the ID beside it and prints the ID.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
if [ "$rc" = 0 ] && [ -s "$D/art/image.tar" ] && [ "$(cat "$D/art/image.id")" = "$id_a" ] &&
  [ "$(cat "$D/out")" = "$id_a" ] && called "^tag $id_a $rtc$" && called "^save -o $D/art/image.tar $rtc$" &&
  [ "$(sed -n 1p "$D/art/image.tar")" = "$id_a" ]; then
  ok "save writes the image and its ID"
else bad "save writes the image and its ID (rc=$rc)"; cat "$D/err"; fi

# 1a. #1597: another run moves the shared tag between save's resolve and its
# `docker save`: the archive still holds the ID save resolved and recorded.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art" -- STUB_RETAG_ON=save STUB_RETAG_TAG="$tc" STUB_RETAG_ID="$id_b"
if [ "$rc" = 0 ] && [ "$(sed -n 1p "$D/art/image.tar")" = "$id_a" ] && [ "$(cat "$D/art/image.id")" = "$id_a" ] &&
  ! called "^save -o $D/art/image.tar $tc$"; then
  ok "a tag moved during the save leaves the archive the recorded ID"
else bad "a tag moved during the save leaves the archive the recorded ID (rc=$rc)"; cat "$D/err"; fi
# 1b. The archive is checked: a per-run tag that no longer names the resolved
# ID after the save refuses the archive and records no ID.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art" -- STUB_RETAG_ON=save STUB_RETAG_TAG="$rtc" STUB_RETAG_ID="$id_b"
if [ "$rc" = 1 ] && grep -q "after the save, not $id_a" "$D/err" && [ ! -e "$D/art/image.id" ] && [ ! -s "$D/out" ]; then
  ok "save refuses an archive whose ID is not the resolved one"
else bad "save refuses an archive whose ID is not the resolved one (rc=$rc)"; cat "$D/err"; fi
# 1c. Codex P1: another run retags the shared name between the build (pin)
# and the save: the archive and the recorded ID stay the build's.
reset
run pin "$tc" "$D/iid"
pinned=$(cat "$D/out")
tagimg "$tc" "$id_b"
run save "$tc" "$D/art"
if [ "$pinned" = "$id_a" ] && [ "$rc" = 0 ] && [ "$(sed -n 1p "$D/art/image.tar")" = "$id_a" ] &&
  [ "$(cat "$D/art/image.id")" = "$id_a" ] && [ "$(cat "$D/out")" = "$id_a" ]; then
  ok "a retag between the build and the save leaves the archive the build's ID"
else bad "a retag between the build and the save leaves the archive the build's ID (rc=$rc)"; cat "$D/err"; fi
# 1c2. Codex P1 (lane-verify): another job retags the shared name between the
# build and the pin: pin records the ID the build wrote, not the tag's.
reset
tagimg "$tc" "$id_b"
run pin "$tc" "$D/iid"
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$id_a" ] &&
  [ "$(sed -n 1p "$D/state/tags/${rtc//[^A-Za-z0-9._-]/_}")" = "$id_a" ]; then
  ok "a retag between the build and the pin pins the build's own ID"
else bad "a retag between the build and the pin pins the build's own ID (rc=$rc, out=$(cat "$D/out"))"; cat "$D/err"; fi
# 1c3. pin refuses a missing or malformed ID file rather than resolve the tag.
reset
run pin "$tc" "$D/no-iid"
rc_missing=$rc
echo 'not-an-id' > "$D/iid"
run pin "$tc" "$D/iid"
if [ "$rc_missing" = 1 ] && [ "$rc" = 1 ] && grep -q 'not an image ID' "$D/err" && ! called '^tag '; then
  ok "pin refuses a missing or malformed build ID file"
else bad "pin refuses a missing or malformed build ID file (rc=$rc_missing/$rc)"; cat "$D/err"; fi
# 1d. save without a pin refuses rather than resolve the shared tag.
reset
run save "$tc" "$D/art"
if [ "$rc" = 1 ] && grep -q 'pin .* right after its build' "$D/err" && [ ! -e "$D/art/image.id" ]; then
  ok "save refuses an image the build did not pin"
else bad "save refuses an image the build did not pin (rc=$rc)"; cat "$D/err"; fi

# 2. save refuses an image that is not a content-hash tag of the CI families.
reset
run save qrdb-toolchain:latest "$D/art"
if [ "$rc" = 1 ] && grep -q 'is not a content-hash tag' "$D/err" && [ ! -e "$D/art" ]; then
  ok "save refuses an image that is not a content-hash tag"
else bad "save refuses an image that is not a content-hash tag (rc=$rc)"; fi

# 3. load accepts the image docker-build built and removes the tar.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
fresh
run load "$D/art" "$tc" "$id_a"
if [ "$rc" = 0 ] && [ ! -e "$D/art/image.tar" ] && grep -q "is $id_a, the image docker-build built" "$D/err" &&
  [ "$(cat "$D/out")" = "$id_a" ]; then
  ok "load accepts the image docker-build built"
else bad "load accepts the image docker-build built (rc=$rc)"; cat "$D/err"; fi

# 3a. #1597: load prints the verified ID and records it for the tag, where
# scripts/in-toolchain-image.sh runs the ID instead of the shared tag.
reset
rm -rf "$D/ids"
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
run load "$D/art" "$tc" "$id_a" -- CI_IMAGE_IDS="$D/ids"
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$id_a" ] &&
  [ "$(cat "$D/ids/${tc//[^A-Za-z0-9._-]/_}" 2>/dev/null)" = "$id_a" ]; then
  ok "load prints and records the verified ID"
else bad "load prints and records the verified ID (rc=$rc)"; cat "$D/err"; ls "$D/ids" 2>&1; fi
# A refused image records nothing.
reset
rm -rf "$D/ids"
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
run load "$D/art" "$tc" "$id_a" -- CI_IMAGE_IDS="$D/ids" STUB_LOAD_ID="$id_b"
if [ "$rc" = 1 ] && [ ! -e "$D/ids" ]; then
  ok "a refused image records no ID"
else bad "a refused image records no ID (rc=$rc)"; fi
# A tag replaced after a load (another job's image under the same tag on a
# shared daemon) leaves the verified record as it was: consumers read the ID.
reset
rm -rf "$D/ids"
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
run load "$D/art" "$tc" "$id_a" -- CI_IMAGE_IDS="$D/ids"
run load "$D/art" "$tc" "$id_a" -- CI_IMAGE_IDS="$D/ids" STUB_LOAD_ID="$id_b"
if [ "$rc" = 1 ] && [ "$(cat "$D/ids/${tc//[^A-Za-z0-9._-]/_}" 2>/dev/null)" = "$id_a" ]; then
  ok "a replaced tag leaves the verified ID record"
else bad "a replaced tag leaves the verified ID record (rc=$rc)"; fi
# 4. load refuses an image whose ID is not the one docker-build recorded.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
fresh
run load "$D/art" "$tc" "$id_a" -- STUB_LOAD_ID="$id_b"
if [ "$rc" = 1 ] && grep -q "loaded as $id_b, not $id_a" "$D/err"; then
  ok "load refuses a substituted image"
else bad "load refuses a substituted image (rc=$rc)"; cat "$D/err"; fi

# 5. load refuses a missing artifact before calling docker.
reset
run load "$D/art" "$tc" "$id_a"
if [ "$rc" = 1 ] && grep -q 'image.tar is missing or empty' "$D/err" && [ ! -e "$D/state/calls" ]; then
  ok "load refuses a missing artifact"
else bad "load refuses a missing artifact (rc=$rc)"; cat "$D/err"; fi

# 6. load refuses an ID that is not an image ID.
reset
run load "$D/art" "$tc" latest
if [ "$rc" = 1 ] && grep -q "'latest' is not an image ID" "$D/err"; then
  ok "load refuses a malformed ID"
else bad "load refuses a malformed ID (rc=$rc)"; fi

# 7. push logs in on stdin into a private DOCKER_CONFIG, pushes the per-run
# tag ghcr.io/<owner, lowercased>/<name>:<hash>-main-<run>-<attempt> created
# from the recorded ID and prints its digest.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=s3cr3t-token
want="ghcr.io/example-org/go-qrdb-toolchain@sha256:$(printf 'b%.0s' $(seq 64))"
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$want" ] &&
  called "^tag $id_a $ptc$" && called "^push $ptc$" && ! called '^push .*-main$' &&
  [ "$(cat "$D/state/pushed-id")" = "$id_a" ] &&
  called '^login ghcr.io -u bot --password-stdin$' && [ "$(cat "$D/state/password")" = s3cr3t-token ] &&
  ! grep -q s3cr3t-token "$D/state/calls" "$D/out" "$D/err" &&
  [ -n "$(cat "$D/state/config")" ] && [ ! -e "$(cat "$D/state/config")" ]; then
  ok "push logs in on stdin, pushes by content hash and prints the digest"
else bad "push logs in on stdin, pushes by content hash and prints the digest (rc=$rc)"; cat "$D/err" "$D/out"; fi

# 7a. #1597: another run moves the shared content-hash tag between the save
# and the push: the push still publishes the recorded ID.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_RETAG_ON=push STUB_RETAG_TAG="$tc" STUB_RETAG_ID="$id_b"
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$want" ] && [ "$(cat "$D/state/pushed-id")" = "$id_a" ]; then
  ok "a tag moved between the save and the push leaves the push the recorded ID"
else bad "a tag moved between the save and the push leaves the push the recorded ID (rc=$rc)"; cat "$D/err"; fi
# 7b. A pushed tag that no longer names the recorded ID is refused: no digest.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_RETAG_ON=push STUB_RETAG_TAG="$ptc" STUB_RETAG_ID="$id_b"
if [ "$rc" = 1 ] && grep -q "after the push, not $id_a" "$D/err" && [ ! -s "$D/out" ]; then
  ok "push refuses a pushed image whose ID is not the recorded one"
else bad "push refuses a pushed image whose ID is not the recorded one (rc=$rc)"; cat "$D/err"; fi
# 7c. push refuses an operand that is not an image ID before the login.
reset
run push "$tc" go-qrdb-toolchain "$tc" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t
if [ "$rc" = 1 ] && grep -q "is not an image ID" "$D/err" && ! called '^login '; then
  ok "push refuses a malformed ID"
else bad "push refuses a malformed ID (rc=$rc)"; cat "$D/err"; fi

# 8. push without the run's token fails naming the variable, not a value.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot
if [ "$rc" = 1 ] && grep -q 'CI_REGISTRY_TOKEN is empty' "$D/err" && ! called '^push '; then
  ok "push refuses an empty token"
else bad "push refuses an empty token (rc=$rc)"; cat "$D/err"; fi

# 9. A push the registry reports no digest for fails.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_NO_DIGEST=1
if [ "$rc" = 1 ] && grep -q 'reported no digest' "$D/err" && [ ! -s "$D/out" ]; then
  ok "a push with no digest fails"
else bad "a push with no digest fails (rc=$rc)"; cat "$D/err"; fi

# 10. A push the registry refuses once succeeds on the next attempt. No retry
# test sleeps: each sets CI_REGISTRY_BACKOFF=0.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_PUSH_FAILS=1 CI_REGISTRY_BACKOFF=0
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$want" ] &&
  [ "$(grep -c '^push ' "$D/state/calls")" = 2 ] && grep -q 'attempt 1 of 3' "$D/err"; then
  ok "a push the registry refuses once succeeds on the next attempt"
else bad "a push the registry refuses once succeeds on the next attempt (rc=$rc)"; cat "$D/err"; fi

# 11. A push refused on every attempt fails after the last.
reset
run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_PUSH_FAILS=9 CI_REGISTRY_BACKOFF=0
if [ "$rc" = 1 ] && grep -q 'GHCR refused the push of' "$D/err" &&
  [ "$(grep -c '^push ' "$D/state/calls")" = 3 ] && [ ! -s "$D/out" ]; then
  ok "a push refused on every attempt fails after the last"
else bad "a push refused on every attempt fails after the last (rc=$rc)"; cat "$D/err"; fi

# 12. A malformed attempt count or backoff is refused before the login and
# the push.
knobs=ok
for kv in CI_REGISTRY_ATTEMPTS=0 CI_REGISTRY_BACKOFF=soon; do
  reset
  run push "$tc" go-qrdb-toolchain "$id_a" -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t "$kv"
  if [ "$rc" != 1 ] || ! grep -q "${kv%%=*}" "$D/err" || called '^push ' || called '^login '; then
    knobs="$kv rc=$rc"
    cat "$D/err"
  fi
done
if [ "$knobs" = ok ]; then ok "a malformed attempt count or backoff is refused before the push"
else bad "a malformed attempt count or backoff is refused before the push ($knobs)"; fi

# 12a. #1597: prune, the self-hosted retention. Old CI images go; a recent
# one, one tagged recently, one a running container uses and any image that
# is not a CI image stay.
id_c="sha256:$(printf '3%.0s' $(seq 64))"
id_d="sha256:$(printf '4%.0s' $(seq 64))"
id_e="sha256:$(printf '5%.0s' $(seq 64))"
id_f="sha256:$(printf '6%.0s' $(seq 64))"
old=$(date -u -d '-3 days' +%Y-%m-%dT%H:%M:%S.000000000Z)
new=$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)
reset
mkdir -p "$D/state/created" "$D/state/tagtime"
tagimg qrdb-ci-it:1111111111111111 "$id_b"            # old: removed
id_g="sha256:$(printf '7%.0s' $(seq 64))"
tagimg ghcr.io/example-org/go-qrdb-ci-it:1111111111111111-main-1-1 "$id_g" # old, pushed: removed
tagimg qrdb-ci-it:2222222222222222 "$id_c"            # built an hour ago: kept
tagimg qrdb-ci-it:3333333333333333 "$id_d"            # old, tagged an hour ago: kept
tagimg qrdb-ci-it:4444444444444444 "$id_e"            # old, running: kept
tagimg alpine:3.20 "$id_f"                            # old, not a CI image: kept
for i in "$id_a" "$id_b" "$id_d" "$id_e" "$id_f" "$id_g"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
echo "$new" > "$D/state/created/${id_c#sha256:}"
echo "$new" > "$D/state/tagtime/${id_d#sha256:}"
echo "c0ffee $id_e" > "$D/state/running"
run prune 24
tags=$(ls "$D/state/tags" | tr '\n' ' ')
if [ "$rc" = 0 ] && [ ! -e "$D/state/tags/qrdb-ci-it_1111111111111111" ] &&
  [ ! -e "$D/state/tags/ghcr.io_example-org_go-qrdb-ci-it_1111111111111111-main-1-1" ] &&
  [ ! -e "$D/state/tags/${tc//[^A-Za-z0-9._-]/_}" ] &&
  [ -e "$D/state/tags/qrdb-ci-it_2222222222222222" ] && [ -e "$D/state/tags/qrdb-ci-it_3333333333333333" ] &&
  [ -e "$D/state/tags/qrdb-ci-it_4444444444444444" ] && [ -e "$D/state/tags/alpine_3.20" ] &&
  grep -q 'kept qrdb-ci-it:4444444444444444, a running container uses it' "$D/err"; then
  ok "prune removes old CI images and keeps recent, running and foreign ones"
else bad "prune removes old CI images and keeps recent, running and foreign ones (rc=$rc, left: $tags)"; cat "$D/err"; fi
# 12a2. Codex P1 (lane-verify): another job moves a shared tag to a fresh
# image between prune's check and its removal. prune removes the old image
# by its checked ID; the fresh image and the tag that now names it survive.
reset
rm -f "$D/state/tags/${tc//[^A-Za-z0-9._-]/_}"
mkdir -p "$D/state/created"
tagimg qrdb-ci-it:1111111111111111 "$id_b"
echo "$old" > "$D/state/created/${id_b#sha256:}"
echo "$new" > "$D/state/created/${id_c#sha256:}"
run prune 24 -- STUB_RETAG_ON=rm STUB_RETAG_TAG=qrdb-ci-it:1111111111111111 STUB_RETAG_ID="$id_c"
if [ "$rc" = 0 ] && [ "$(sed -n 1p "$D/state/tags/qrdb-ci-it_1111111111111111" 2>/dev/null)" = "$id_c" ] &&
  [ -e "$D/state/ids/${id_c#sha256:}" ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ] && ! called '^rmi '; then
  ok "prune removes the checked ID, not a tag another job moved to a fresh image"
else bad "prune removes the checked ID, not a tag another job moved to a fresh image (rc=$rc)"; cat "$D/err"; fi
# 12a3. An old CI image that also carries a foreign tag is left: prune never
# forces a removal. Codex P1 (mm1597): an old image two content-hash tags name
# (TOOLCHAIN_TAG changed, Docker reused the ID) is reclaimed: the daemon
# refuses a removal by ID while both name it, so prune untags all but one by
# name, each checked to still name the image, and removes it by ID.
reset
mkdir -p "$D/state/created"
tagimg qrdb-ci-it:5555555555555555 "$id_b"
tagimg mytool:keep "$id_b"
tagimg qrdb-ci-it:6666666666666666 "$id_c"
tagimg qrdb-toolchain:6666666666666666 "$id_c"
for i in "$id_a" "$id_b" "$id_c"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
run prune 24
if [ "$rc" = 0 ] && [ -e "$D/state/tags/mytool_keep" ] && [ -e "$D/state/tags/qrdb-ci-it_5555555555555555" ] &&
  [ ! -e "$D/state/tags/qrdb-ci-it_6666666666666666" ] && [ ! -e "$D/state/tags/qrdb-toolchain_6666666666666666" ] &&
  [ ! -e "$D/state/ids/${id_c#sha256:}" ] && [ ! -e "$D/state/tags/${tc//[^A-Za-z0-9._-]/_}" ] &&
  grep -q 'kept qrdb-ci-it:5555555555555555 .*not a CI image' "$D/err" && ! grep -q 'refused' "$D/err"; then
  ok "prune leaves an image with a foreign tag and reclaims one two content-hash tags name"
else bad "prune leaves an image with a foreign tag and reclaims one two content-hash tags name (rc=$rc)"; cat "$D/err"; fi
# 12a5. A content-hash tag another job moves to a fresh image while prune
# reclaims the old one is left alone, whichever of the old image's tags it
# is: the fresh image and the moved tag survive, the old image goes.
for moved in qrdb-ci-it:aaaaaaaaaaaaaaaa qrdb-toolchain:aaaaaaaaaaaaaaaa; do
  reset
  mkdir -p "$D/state/created"
  tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa "$id_b"
  tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa-run-1-1 "$id_b"
  tagimg qrdb-toolchain:aaaaaaaaaaaaaaaa "$id_b"
  for i in "$id_a" "$id_b"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
  echo "$new" > "$D/state/created/${id_c#sha256:}"
  run prune 24 -- STUB_RETAG_ON=rm STUB_RETAG_TAG="$moved" STUB_RETAG_ID="$id_c"
  if [ "$rc" = 0 ] && [ "$(sed -n 1p "$D/state/tags/${moved//[^A-Za-z0-9._-]/_}" 2>/dev/null)" = "$id_c" ] &&
    [ -e "$D/state/ids/${id_c#sha256:}" ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ] &&
    ! called "^rmi $moved\$" && [ "$(ls "$D/state/tags" | tr '\n' ' ')" = "${moved//[^A-Za-z0-9._-]/_} " ]; then
    ok "prune keeps the moved content-hash tag $moved and reclaims the old image"
  else bad "prune keeps the moved content-hash tag $moved and reclaims the old image (rc=$rc, left: $(ls "$D/state/tags" | tr '\n' ' '))"; cat "$D/err"; fi
done
# 12a6. Codex P1 (#1596): another job moves the second content-hash tag to a
# fresh image after prune inspected the old one, just before the removal
# (STUB_RETAG_ON=rm; the daemon holds no other image, so that removal is the
# old image's). Prune never removes a content-hash tag by name, so the fresh
# tag still names the fresh image, and the old image goes by its ID.
reset
fresh
mkdir -p "$D/state/created"
tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa "$id_b"
tagimg qrdb-toolchain:aaaaaaaaaaaaaaaa "$id_b"
for i in "$id_a" "$id_b"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
echo "$new" > "$D/state/created/${id_c#sha256:}"
run prune 24 -- STUB_RETAG_ON=rm STUB_RETAG_TAG=qrdb-toolchain:aaaaaaaaaaaaaaaa STUB_RETAG_ID="$id_c"
if [ "$rc" = 0 ] && [ "$(sed -n 1p "$D/state/tags/qrdb-toolchain_aaaaaaaaaaaaaaaa" 2>/dev/null)" = "$id_c" ] &&
  [ -e "$D/state/ids/${id_c#sha256:}" ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ] &&
  [ ! -e "$D/state/tags/qrdb-ci-it_aaaaaaaaaaaaaaaa" ] && ! called '^rmi qrdb-'; then
  ok "prune removes an expired image by ID and keeps a content-hash tag moved after its inspect"
else bad "prune removes an expired image by ID and keeps a content-hash tag moved after its inspect (rc=$rc, left: $(ls "$D/state/tags" | tr '\n' ' '))"; cat "$D/err"; fi
# 12a4. Codex P1 (fx1597g): load leaves the archive's per-run tag beside the
# content-hash tag, and a push can leave its run's pushed tag. Two tag
# references make the daemon refuse a removal by ID, so prune untags the
# run-unique references (each checked to still name the image) first, then
# removes the image by ID: no old image is kept for its per-run tags. An
# image whose only references are run-unique goes with its last untag.
reset
mkdir -p "$D/state/created"
tagimg qrdb-ci-it:7777777777777777 "$id_b"
tagimg qrdb-ci-it:7777777777777777-run-5-1 "$id_b"
tagimg qrdb-toolchain:8888888888888888 "$id_c"
tagimg qrdb-toolchain:8888888888888888-run-6-1 "$id_c"
tagimg ghcr.io/example-org/go-qrdb-toolchain:8888888888888888-main-6-1 "$id_c"
tagimg qrdb-ci-it:9999999999999999-run-8-1 "$id_d"
tagimg qrdb-ci-it:9999999999999999-run-9-1 "$id_d"
for i in "$id_a" "$id_b" "$id_c" "$id_d"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
run prune 24
tags=$(ls "$D/state/tags" 2>/dev/null | tr '\n' ' ')
if [ "$rc" = 0 ] && [ -z "$tags" ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ] &&
  [ ! -e "$D/state/ids/${id_c#sha256:}" ] && [ ! -e "$D/state/ids/${id_d#sha256:}" ] &&
  ! called '^rmi qrdb-ci-it:7777777777777777$' && ! called '^rmi qrdb-toolchain:8888888888888888$' &&
  ! grep -q 'refused' "$D/err"; then
  ok "prune reclaims an old image that also carries its per-run or pushed tags"
else bad "prune reclaims an old image that also carries its per-run or pushed tags (rc=$rc, left: $tags)"; cat "$D/err"; fi
# 12a7. Codex P1 (#1597 round 1): a concurrent producer reuses an image past
# the bound (the Make targets' already-built path) and inspects or pushes it.
# It holds the image lock shared while it does, so prune, which needs the
# lock exclusive, removes nothing; once the producer lets go, prune reclaims
# the image. Without flock(1) on this host the case cannot run.
if command -v flock >/dev/null 2>&1; then
  reset
  mkdir -p "$D/state/created"
  tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa "$id_b"
  echo "$old" > "$D/state/created/${id_b#sha256:}"
  exec 8>>"$D/image.lock"
  flock -s 8
  run prune 24 -- CI_IMAGE_LOCK_WAIT=0
  exec 8>&-
  if [ "$rc" = 0 ] && [ -e "$D/state/ids/${id_b#sha256:}" ] && [ -f "$D/state/tags/qrdb-ci-it_aaaaaaaaaaaaaaaa" ] &&
    ! called '^image rm ' && grep -q 'another job holds the image lock' "$D/err"; then
    ok "prune leaves an expired image while a concurrent producer holds the image lock"
  else bad "prune leaves an expired image while a concurrent producer holds the image lock (rc=$rc)"; cat "$D/err"; fi
  run prune 24 -- CI_IMAGE_LOCK_WAIT=0
  if [ "$rc" = 0 ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ]; then
    ok "prune reclaims the image once the producer releases the image lock"
  else bad "prune reclaims the image once the producer releases the image lock (rc=$rc)"; cat "$D/err"; fi
  # A producer step (load) waits for a prune that holds the lock exclusive,
  # and fails rather than run unprotected once the wait runs out.
  reset
  exec 8>>"$D/image.lock"
  flock -x 8
  run load "$D/art" "$tc" "$id_a" -- CI_IMAGE_LOCK_WAIT=0
  exec 8>&-
  if [ "$rc" = 1 ] && grep -q 'still held exclusive' "$D/err" && ! called '^load '; then
    ok "a producer step refuses to run while a prune holds the image lock"
  else bad "a producer step refuses to run while a prune holds the image lock (rc=$rc)"; cat "$D/err"; fi
  # 12a7b. Codex P1 (#1597 round 2): two pool runners are containers with
  # their own temp dirs that share one Docker daemon and the host's data
  # array. Runner 1 holds the default image lock shared; runner 2's prune
  # must find it held, so the default is on the data array, never the temp
  # dir. A pool runner with no data array and no CI_IMAGE_LOCK keeps every
  # image; a hosted runner keeps its temp dir.
  reset
  mkdir -p "$D/state/created" "$D/r1tmp" "$D/r2tmp" "$D/host/gh-runner-cache"
  tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa "$id_b"
  echo "$old" > "$D/state/created/${id_b#sha256:}"
  pool=(CI_IMAGE_LOCK= GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=self-hosted CI_CACHE_ROOT="$D/host")
  run lock-file -- "${pool[@]}" TMPDIR="$D/r1tmp"
  lock1=$(cat "$D/out")
  [ -n "$lock1" ] && exec 8>>"$lock1" && flock -s 8
  run prune 24 -- "${pool[@]}" TMPDIR="$D/r2tmp" CI_IMAGE_LOCK_WAIT=0
  exec 8>&-
  if [ "$rc" = 0 ] && [ "$lock1" = "$D/host/gh-runner-cache/qrdb-ci-image.lock" ] &&
    [ -e "$D/state/ids/${id_b#sha256:}" ] && ! called '^image rm ' && grep -q 'another job holds the image lock' "$D/err"; then
    ok "two pool runners with their own temp dirs share the image lock on the host's data array"
  else bad "two pool runners with their own temp dirs share the image lock on the host's data array (rc=$rc, lock=$lock1)"; cat "$D/err"; fi
  run prune 24 -- "${pool[@]}" CI_CACHE_ROOT="$D/nohost" TMPDIR="$D/r2tmp" CI_IMAGE_LOCK_WAIT=0
  if [ "$rc" = 0 ] && [ -e "$D/state/ids/${id_b#sha256:}" ] && ! called '^image rm ' && grep -q 'no host-shared image lock' "$D/err"; then
    ok "a pool runner with no host-shared image lock keeps every image"
  else bad "a pool runner with no host-shared image lock keeps every image (rc=$rc)"; cat "$D/err"; fi
  run lock-file -- "${pool[@]}" RUNNER_ENVIRONMENT=github-hosted TMPDIR="$D/r1tmp"
  if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$D/r1tmp/qrdb-ci-image.lock" ]; then
    ok "a hosted runner's image lock is in its temp dir"
  else bad "a hosted runner's image lock is in its temp dir (rc=$rc)"; cat "$D/out" "$D/err"; fi
  # 12a8. The toolchain image's flock is BusyBox's, which knows only -s -x
  # -u -n (#1597 Lint Gates: "flock: unrecognized option: w"). A shim that
  # refuses -w as BusyBox does stands in for it; the lock still works.
  real_flock=$(command -v flock)
  mkdir -p "$D/bbflock"
  cat > "$D/bbflock/flock" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do
  case \$a in -w|-E|--*) echo "flock: unrecognized option: \${a#-}" >&2; exit 1 ;; esac
done
exec "$real_flock" "\$@"
EOF
  chmod +x "$D/bbflock/flock"
  reset
  mkdir -p "$D/state/created"
  tagimg qrdb-ci-it:aaaaaaaaaaaaaaaa "$id_b"
  echo "$old" > "$D/state/created/${id_b#sha256:}"
  run prune 24 -- PATH="$D/bbflock:$D/bin:$PATH"
  if [ "$rc" = 0 ] && [ ! -e "$D/state/ids/${id_b#sha256:}" ] && ! grep -q 'kept every image' "$D/err" &&
    ! grep -q 'unrecognized option' "$D/err"; then
    ok "prune takes the image lock with a BusyBox flock that has no -w"
  else bad "prune takes the image lock with a BusyBox flock that has no -w (rc=$rc)"; cat "$D/err"; fi
  # Held exclusive past the wait, the BusyBox-flock producer reports the
  # timeout, after polling for the whole wait.
  reset
  exec 8>>"$D/image.lock"
  flock -x 8
  t0=$SECONDS
  run load "$D/art" "$tc" "$id_a" -- PATH="$D/bbflock:$D/bin:$PATH" CI_IMAGE_LOCK_WAIT=1
  t1=$SECONDS
  exec 8>&-
  if [ "$rc" = 1 ] && grep -q 'still held exclusive (a prune) after 1s' "$D/err" && ! grep -q 'unrecognized option' "$D/err" &&
    [ $((t1 - t0)) -ge 1 ] && ! called '^load '; then
    ok "a BusyBox-flock producer times out on a held image lock"
  else bad "a BusyBox-flock producer times out on a held image lock (rc=$rc)"; cat "$D/err"; fi
  # A flock that fails to run is an error that says so, not a held lock.
  mkdir -p "$D/badflock"
  printf '#!/usr/bin/env bash\necho "flock: unrecognized option: n" >&2\nexit 1\n' > "$D/badflock/flock"
  chmod +x "$D/badflock/flock"
  reset
  run load "$D/art" "$tc" "$id_a" -- PATH="$D/badflock:$D/bin:$PATH" CI_IMAGE_LOCK_WAIT=5
  if [ "$rc" = 1 ] && grep -q 'flock(1) cannot take the image lock .*unrecognized option: n' "$D/err" &&
    ! grep -q 'still held' "$D/err" && ! called '^load '; then
    ok "a flock that cannot run is reported as such, not as a held lock"
  else bad "a flock that cannot run is reported as such, not as a held lock (rc=$rc)"; cat "$D/err"; fi
else
  echo "SKIP: flock(1) is not installed, the image lock cases need it"
fi
# 12b. A malformed bound is refused before any removal.
reset
run prune 0
if [ "$rc" = 1 ] && grep -q 'is not a whole number of hours' "$D/err" && ! called '^rmi ' && ! called '^image rm '; then
  ok "prune refuses a malformed bound"
else bad "prune refuses a malformed bound (rc=$rc)"; cat "$D/err"; fi
# 12c. untag removes save's per-run tags, and nothing else.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
run untag "$tc"
if [ "$rc" = 0 ] && [ ! -e "$D/state/tags/${rtc//[^A-Za-z0-9._-]/_}" ] && [ -e "$D/state/tags/${tc//[^A-Za-z0-9._-]/_}" ]; then
  ok "untag removes the per-run tag only"
else bad "untag removes the per-run tag only (rc=$rc)"; cat "$D/err"; fi

# 13-15. check-ref accepts a digest reference under the owner in GHCR only.
hex=$(printf 'c%.0s' $(seq 64))
reset
run check-ref "ghcr.io/example-org/go-qrdb-toolchain@sha256:$hex"
if [ "$rc" = 0 ]; then ok "check-ref accepts the owner's digest reference"
else bad "check-ref accepts the owner's digest reference (rc=$rc)"; cat "$D/err"; fi
run check-ref "ghcr.io/someone-else/go-qrdb-toolchain@sha256:$hex"
if [ "$rc" = 1 ] && grep -q 'is not under ghcr.io/example-org' "$D/err"; then
  ok "check-ref refuses another owner's namespace"
else bad "check-ref refuses another owner's namespace (rc=$rc)"; fi
run check-ref "ghcr.io/example-org/go-qrdb-toolchain:0123456789abcdef-main"
if [ "$rc" = 1 ] && grep -q 'is not a digest reference' "$D/err"; then
  ok "check-ref refuses a tag reference"
else bad "check-ref refuses a tag reference (rc=$rc)"; fi

# 15a. Two interleaved hand-offs on one daemon, two builds with different IDs
# under the same content-hash tag: run B loads its image the moment run A's
# load returns, moving the tag. Each load still passes and hands its consumer
# its own producer's ID, which the daemon launches as that image while the
# tag names the other run's.
reset
run pin "$tc" "$D/iid"; run save "$tc" "$D/art"
mv "$D/art" "$D/art-a"
tagimg "$tc" "$id_b"
printf '%s\n' "$id_b" > "$D/iid"
run pin "$tc" "$D/iid" -- GITHUB_RUN_ID=78; run save "$tc" "$D/art" -- GITHUB_RUN_ID=78
mv "$D/art" "$D/art-b"
fresh
run load "$D/art-a" "$tc" "$id_a" -- STUB_RACE_ID="$id_b"
rc_a=$rc ref_a=$(cat "$D/out")
run load "$D/art-b" "$tc" "$id_b"
rc_b=$rc ref_b=$(cat "$D/out")
if [ "$rc_a" = 0 ] && [ "$rc_b" = 0 ] && [ "$ref_a" = "$id_a" ] && [ "$ref_b" = "$id_b" ] &&
  [ "$(resolve "$ref_a")" = "$id_a" ] && [ "$(resolve "$ref_b")" = "$id_b" ] &&
  [ "$(resolve "$tc")" = "$id_b" ]; then
  ok "interleaved hand-offs under one tag each launch their own producer's ID"
else bad "interleaved hand-offs under one tag each launch their own producer's ID (rc=$rc_a/$rc_b, refs $ref_a $ref_b)"; cat "$D/err"; fi

# 16-21. registry-pull and registry-push, the CI image registry path (#1609):
# docker-build-toolchain and docker-build-it try `registry-pull` before they
# build and `registry-push` after. The registry is a fake value; a real one is
# never written into a file.
reg=registry.test:5000
it=qrdb-ci-it:fedcba9876543210
dig="sha256:$(printf 'd%.0s' $(seq 64))"

# 16. Unset: no docker call, no output; a pull is a miss and a push a no-op.
reset
run registry-pull "$it"
pull_rc=$rc
run registry-push "$it"
if [ "$pull_rc" = 1 ] && [ "$rc" = 0 ] && [ ! -e "$D/state/calls" ] && [ ! -s "$D/out" ] && [ ! -s "$D/err" ]; then
  ok "registry: unset makes no docker call and logs nothing"
else bad "registry: unset makes no docker call and logs nothing (pull rc=$pull_rc, push rc=$rc)"; cat "$D/err"; fi

# The digest records (P1, Codex): a registry pull is trusted only by the digest
# the producer's push recorded under CI_IMAGE_DIGEST_DIR (a GitHub cache entry
# in CI), one file per content-hash tag, never by the tag alone.
itrepo=$reg/qrdb-ci-it
pushdig="sha256:$(printf 'b%.0s' $(seq 64))"
rec="$D/records"
records() { rm -rf "$rec"; mkdir -p "$rec"; [ $# -eq 0 ] || printf '%s\n' "$1" > "$rec/qrdb-ci-it_fedcba9876543210"; }

# 17. Hit with a record: pulled by digest, the pulled image's digest checked,
# tagged to the local name, one line.
reset
records "$dig"
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit
if [ "$rc" = 0 ] && called "^pull $itrepo@$dig$" && ! called "^pull $reg/$it$" &&
  called "^image inspect .*RepoDigests.* $itrepo@$dig$" && called "^tag $itrepo@$dig $it$" &&
  [ "$(cat "$D/err")" = "pulled $itrepo@$dig" ] && ! called '^push ' && ! called '^login '; then
  ok "registry: a hit with a record is pulled by digest, checked and tagged"
else bad "registry: a hit with a record is pulled by digest, checked and tagged (rc=$rc)"; cat "$D/err"; fi

# 17a. Hit without a record (no file, or no record directory at all): no pull,
# a silent miss, so the caller builds.
reset
records
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit
norec_rc=$rc norec_err=$(cat "$D/err")
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" STUB_PULL=hit
if [ "$norec_rc" = 1 ] && [ -z "$norec_err" ] && [ "$rc" = 1 ] && [ ! -s "$D/err" ] &&
  ! called '^pull ' && ! called '^tag '; then
  ok "registry: a hit without a record is not pulled"
else bad "registry: a hit without a record is not pulled (rc=$norec_rc/$rc)"; cat "$D/err"; fi

# 17b. A record that is not a sha256 digest is no record.
reset
records latest
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit
if [ "$rc" = 1 ] && ! called '^pull ' && [ ! -s "$D/err" ]; then
  ok "registry: a malformed record is not pulled"
else bad "registry: a malformed record is not pulled (rc=$rc)"; cat "$D/err"; fi

# 17c. Digest mismatch: the registry serves another digest; it is not tagged,
# one line says so, and the caller builds.
reset
records "$dig"
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit \
  STUB_PULL_SERVES="sha256:$(printf 'e%.0s' $(seq 64))"
if [ "$rc" = 1 ] && ! called "^tag .* $it$" &&
  [ "$(cat "$D/err")" = "miss, $itrepo@$dig does not match its record" ]; then
  ok "registry: a digest mismatch is a miss"
else bad "registry: a digest mismatch is a miss (rc=$rc)"; cat "$D/err"; fi

# 18. Miss: the pull fails silently (the caller builds), the push tags and
# pushes the build, records its repo digest and logs one line.
reset
records "$dig"
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=miss
pull_rc=$rc pull_err=$(cat "$D/err")
records
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec"
if [ "$pull_rc" = 1 ] && [ -z "$pull_err" ] && [ "$rc" = 0 ] && called "^tag $it $reg/$it$" &&
  called "^push $reg/$it$" && [ "$(cat "$D/err")" = "miss, built and pushed $itrepo@$pushdig" ] &&
  [ "$(cat "$rec/qrdb-ci-it_fedcba9876543210")" = "$pushdig" ] && ! called '^login '; then
  ok "registry: a miss pushes the build, records its digest and logs one line"
else bad "registry: a miss pushes the build, records its digest and logs one line (pull rc=$pull_rc, push rc=$rc)"; cat "$D/err"; fi

# 18a. A push the registry reports no digest for records nothing.
reset
records
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_NO_DIGEST=1
if [ "$rc" = 0 ] && [ ! -e "$rec/qrdb-ci-it_fedcba9876543210" ] &&
  [ "$(cat "$D/err")" = "miss, built and pushed $reg/$it, no digest to record" ]; then
  ok "registry: a push with no digest records nothing"
else bad "registry: a push with no digest records nothing (rc=$rc)"; cat "$D/err"; fi

# 19. Unreachable: a pull and a push that stall are cut off by the timeout;
# the pull is a miss, the push failure is logged, nothing is recorded and
# neither fails.
reset
records "$dig"
start=$SECONDS
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hang CI_IMAGE_REGISTRY_TIMEOUT=1
pull_rc=$rc
records
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PUSH_HANG=1 CI_IMAGE_REGISTRY_TIMEOUT=1
if [ "$pull_rc" = 1 ] && [ "$rc" = 0 ] && [ $((SECONDS - start)) -lt 20 ] && called "^pull $itrepo@$dig$" &&
  ! called '^push ' && [ "$(cat "$D/err")" = "registry unavailable, skipped $reg/$it" ] &&
  [ ! -e "$rec/qrdb-ci-it_fedcba9876543210" ]; then
  ok "registry: a stalled pull times out, falls back and the push skips"
else bad "registry: a stalled pull times out, falls back and the push skips (pull rc=$pull_rc, push rc=$rc)"; cat "$D/err"; fi

# 19a. One outage budget per job: the first stalled pull leaves one marker,
# named by the registry's hash, and the second pull and both pushes skip at
# once without calling docker, each with the status a failed transfer has.
reset
records "$dig"
start=$SECONDS
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hang CI_IMAGE_REGISTRY_TIMEOUT=2
r1=$rc
markers=$(cd "$D/state" && echo ci-image-registry-outage-*)
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hang CI_IMAGE_REGISTRY_TIMEOUT=2
r2=$rc e2=$(cat "$D/err")
records
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PUSH_HANG=1 CI_IMAGE_REGISTRY_TIMEOUT=2
r3=$rc e3=$(cat "$D/err")
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PUSH_HANG=1 CI_IMAGE_REGISTRY_TIMEOUT=2
if [ "$r1" = 1 ] && [ "$r2" = 1 ] && [ "$r3" = 0 ] && [ "$rc" = 0 ] && [ $((SECONDS - start)) -lt 10 ] &&
  [ "$(grep -c '^pull ' "$D/state/calls")" = 1 ] && ! called '^push ' && ! called '^tag ' &&
  [ "$e2" = "registry unavailable, skipped $itrepo@$dig" ] &&
  [ "$e3" = "registry unavailable, skipped $reg/$it" ] && [ "$(cat "$D/err")" = "$e3" ] &&
  [[ $markers =~ ^ci-image-registry-outage-[0-9a-f]{16}$ ]] && [[ $markers != *registry.test* ]]; then
  ok "registry: the first stalled transfer spends the job's outage budget, the rest skip"
else bad "registry: the first stalled transfer spends the job's outage budget, the rest skip ($r1 $r2 $r3 $rc, $markers)"; printf '%s\n%s\n' "$e2" "$e3"; fi

# 19b. A registry that cannot be reached at all spends the budget too; a fast
# not-found (test 18) and an HTTP error (test 20) do not.
reset
records "$dig"
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=refused
r1=$rc
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit
if [ "$r1" = 1 ] && [ "$rc" = 1 ] && [ "$(grep -c '^pull ' "$D/state/calls")" = 1 ] &&
  [ "$(cat "$D/err")" = "registry unavailable, skipped $itrepo@$dig" ]; then
  ok "registry: a refused connection spends the job's outage budget"
else bad "registry: a refused connection spends the job's outage budget (rc=$r1/$rc)"; cat "$D/err"; fi
reset
records "$dig"
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=miss
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" STUB_PUSH_FAILS=1
miss_left=$(cd "$D/state" && echo ci-image-registry-outage-*)
run registry-pull "$it" -- CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_PULL=hit
if [ "$miss_left" = 'ci-image-registry-outage-*' ] && [ "$rc" = 0 ] && [ "$(grep -c '^pull ' "$D/state/calls")" = 2 ]; then
  ok "registry: a not-found pull and an HTTP error leave no outage marker"
else bad "registry: a not-found pull and an HTTP error leave no outage marker (rc=$rc, $miss_left)"; cat "$D/err"; fi

# 19c. The per-call bound defaults to 300 s; a value that is not a count of
# seconds falls back to it.
t_default=$(unset CI_IMAGE_REGISTRY_TIMEOUT; . "$here/image-registry.sh"; ci_registry_timeout)
t_bad=$(CI_IMAGE_REGISTRY_TIMEOUT=abc; . "$here/image-registry.sh"; ci_registry_timeout)
t_set=$(CI_IMAGE_REGISTRY_TIMEOUT=42; . "$here/image-registry.sh"; ci_registry_timeout)
if [ "$t_default" = 300 ] && [ "$t_bad" = 300 ] && [ "$t_set" = 42 ]; then
  ok "registry: the per-call bound defaults to 300 s"
else bad "registry: the per-call bound defaults to 300 s (got $t_default, $t_bad, $t_set)"; fi

# 20. Unreachable: a refused push (closed port) is logged, never retried, never fatal.
reset
run registry-push "$it" -- CI_IMAGE_REGISTRY="$reg" STUB_PUSH_FAILS=9
if [ "$rc" = 0 ] && [ "$(grep -c '^push ' "$D/state/calls")" = 1 ] && [ "$(cat "$D/err")" = "miss, built, push failed" ]; then
  ok "registry: a refused push is logged once and does not fail"
else bad "registry: a refused push is logged once and does not fail (rc=$rc)"; cat "$D/err"; fi

# 21. The registry path takes only the CI content-hash tags (another tag is
# skipped, and its push never fails the build), and a value that is not
# host[:port] is never handed to docker.
reset
run registry-pull qrdb-toolchain:latest -- CI_IMAGE_REGISTRY="$reg" STUB_PULL=hit
bad_tag_rc=$rc bad_tag_err=$(cat "$D/err")
run registry-push qrdb-toolchain:latest -- CI_IMAGE_REGISTRY="$reg"
[ "$rc" = 0 ] || bad_tag_rc="push rc=$rc"
run registry-pull "$it" -- CI_IMAGE_REGISTRY='registry.test:5000/x y' STUB_PULL=hit
if [ "$bad_tag_rc" = 1 ] && [[ $bad_tag_err == *'is not a content-hash tag'* ]] && [ "$rc" = 1 ] && ! called '^pull '; then
  ok "registry: a non-CI tag or a malformed registry value is refused before docker"
else bad "registry: a non-CI tag or a malformed registry value is refused before docker (rc=$bad_tag_rc/$rc)"; cat "$D/err"; fi

# 21a. registry-push --unrecorded without CI_IMAGE_DIGEST_DIR pushes nothing
# (nothing could be recorded), and with the registry unset does nothing.
reset
run registry-push --unrecorded "$it" -- CI_IMAGE_REGISTRY="$reg"
nodir_rc=$rc
run registry-push --unrecorded "$it"
if [ "$nodir_rc" = 0 ] && [ "$rc" = 0 ] && [ ! -e "$D/state/calls" ] && [ ! -s "$D/err" ]; then
  ok "registry: --unrecorded with no record directory or no registry pushes nothing"
else bad "registry: --unrecorded with no record directory or no registry pushes nothing (rc=$nodir_rc/$rc)"; cat "$D/err"; fi

# 22-28. The Make targets themselves (P1, Codex): make docker-build-toolchain
# and docker-build-it run on the stub docker, whose buildx stands in for the
# build, through the real scripts/ci-image.sh. A target that stopped calling
# the registry fails these, where the cases above would still pass.
# mk <target> [env...]: the target, its status in $rc, stdout in $D/out,
# stderr in $D/err.
mk() {
  local target=$1
  shift
  rc=0
  env -u GIT_CONFIG_KEY_0 -u MODULE_ENV_FILE -u CI_IMAGE_REGISTRY -u CI_IMAGE_DIGEST_DIR -u MAKEFLAGS -u MAKELEVEL \
    PATH="$D/bin:$PATH" STUB_STATE="$D/state" CI_IMAGE_LOCK="$D/image.lock" "$@" \
    make -s --no-print-directory -C "$here/.." "$target" TOOLCHAIN_IMAGE="$tc" IT_IMAGE="$it" \
    > "$D/out" 2> "$D/err" || rc=$?
}
# before <a> <b>: the first call matching <a> precedes the first matching <b>.
before() {
  local a b
  a=$(grep -n -m1 -- "$1" "$D/state/calls" | cut -d: -f1)
  b=$(grep -n -m1 -- "$2" "$D/state/calls" | cut -d: -f1)
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]
}
# rec_for <image> [digest]: an empty record directory, or one holding <digest>
# for <image>.
rec_for() { rm -rf "$rec"; mkdir -p "$rec"; [ $# -lt 2 ] || printf '%s\n' "$2" > "$rec/${1//:/_}"; }

for target in docker-build-toolchain docker-build-it; do
  case $target in
  docker-build-toolchain) img=$tc stage=toolchain ;;
  *) img=$it stage=integration ;;
  esac
  repo=$reg/${img%:*}
  r=(CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec")

  # 22. A registry hit skips the build.
  reset; fresh; rec_for "$img" "$dig"
  mk "$target" "${r[@]}" STUB_PULL=hit
  if [ "$rc" = 0 ] && called "^pull $repo@$dig$" && ! called '^buildx build ' && ! called '^push '; then
    ok "$target: a registry hit skips the build"
  else bad "$target: a registry hit skips the build (rc=$rc)"; cat "$D/err"; fi

  # 23. A miss builds, then pushes and records the digest.
  reset; fresh; rec_for "$img"
  mk "$target" "${r[@]}" STUB_PULL=miss
  if [ "$rc" = 0 ] && called "^buildx build .*--target $stage -t $img " && before '^buildx build ' "^push $reg/$img$" &&
    [ "$(cat "$rec/${img//:/_}")" = "$pushdig" ] && grep -qx "miss, built and pushed $repo@$pushdig" "$D/err"; then
    ok "$target: a miss builds, then pushes"
  else bad "$target: a miss builds, then pushes (rc=$rc)"; cat "$D/err"; fi

  # 24. A failed push still exits 0.
  reset; fresh; rec_for "$img"
  mk "$target" "${r[@]}" STUB_PUSH_FAILS=9
  if [ "$rc" = 0 ] && called '^buildx build ' && called "^push $reg/$img$" && grep -qx 'miss, built, push failed' "$D/err"; then
    ok "$target: a failed push still exits 0"
  else bad "$target: a failed push still exits 0 (rc=$rc)"; cat "$D/err"; fi

  # 25. A failed build exits non-zero and pushes nothing.
  reset; fresh; rec_for "$img"
  mk "$target" "${r[@]}" STUB_BUILD_FAILS=1
  if [ "$rc" != 0 ] && called '^buildx build ' && ! called '^push '; then
    ok "$target: a failed build exits non-zero"
  else bad "$target: a failed build exits non-zero (rc=$rc)"; cat "$D/err"; fi

  # 26. P2: a held image with no usable record is published again, and the
  # record written; a failed retry still exits 0.
  reset; rec_for "$img"
  mk "$target" "${r[@]}"
  held_rc=$rc held_err=$(cat "$D/err")
  held_rec=$(cat "$rec/${img//:/_}" 2>/dev/null || :)
  held_build=$(grep -c '^buildx build ' "$D/state/calls" || :)
  reset; rec_for "$img"
  mk "$target" "${r[@]}" STUB_PUSH_FAILS=9
  if [ "$held_rc" = 0 ] && [ "$held_err" = "held, unrecorded and pushed $repo@$pushdig" ] &&
    [ "$held_rec" = "$pushdig" ] && [ "$held_build" = 0 ] &&
    [ "$rc" = 0 ] && called "^push $reg/$img$" && [ "$(cat "$D/err")" = "held, unrecorded, push failed" ]; then
    ok "$target: a held image with no record is published again"
  else bad "$target: a held image with no record is published again (rc=$held_rc/$rc)"; printf '%s\n' "$held_err"; cat "$D/err"; fi

  # 27. A held image with a record is not pushed (nor pulled, nor built).
  reset; rec_for "$img" "$dig"
  mk "$target" "${r[@]}"
  if [ "$rc" = 0 ] && ! called '^push ' && ! called '^pull ' && ! called '^buildx build ' && [ ! -s "$D/err" ] &&
    [ "$(grep '^tag ' "$D/state/calls")" = "tag $img $img" ]; then
    ok "$target: a held image with a record is not pushed, only its tag time refreshed"
  else bad "$target: a held image with a record is not pushed, only its tag time refreshed (rc=$rc)"; cat "$D/err"; fi

  # 27a. Codex P1 (#1597 round 1): the reuse runs under the image lock, held
  # shared from the check through the refresh and the push (the buildx probe
  # before it touches no image), so a concurrent
  # prune cannot remove the reused image (a prune cannot take it exclusive).
  if command -v flock >/dev/null 2>&1; then
    reset; rec_for "$img"
    mk "$target" "${r[@]}" STUB_PROBE_LOCK="$D/image.lock"
    if [ "$rc" = 0 ] && grep -q "^held image inspect -f {{.Id}} $img$" "$D/state/lockprobe" &&
      grep -q "^held tag $img $img$" "$D/state/lockprobe" && grep -q "^held push " "$D/state/lockprobe" &&
      [ "$(grep '^free ' "$D/state/lockprobe")" = "free buildx version" ]; then
      ok "$target: the reuse of a held image holds the image lock through its push"
    else bad "$target: the reuse of a held image holds the image lock through its push (rc=$rc)"; cat "$D/err" "$D/state/lockprobe"; fi
  fi

  # 28. Registry unset: a held image is today's one inspect and one line; a
  # missing one is built with no registry call.
  reset; rec_for "$img"
  mk "$target" CI_IMAGE_DIGEST_DIR="$rec"
  unset_out=$(cat "$D/out") unset_err=$(cat "$D/err") unset_calls=$(cat "$D/state/calls")
  reset; fresh
  mk "$target"
  if [ "$unset_out" = "==> $img is already built on this daemon" ] && [ -z "$unset_err" ] &&
    [ "$unset_calls" = "$(printf 'buildx version\nimage inspect -f {{.Id}} %s\ntag %s %s' "$img" "$img" "$img")" ] &&
    [ "$rc" = 0 ] && called '^buildx build ' && ! called '^pull ' && ! called '^push ' && ! called '^tag '; then
    ok "$target: an unset registry changes nothing"
  else bad "$target: an unset registry changes nothing (rc=$rc)"; printf '%s\n%s\n' "$unset_out" "$unset_calls"; cat "$D/err"; fi

  # 28a. #1597 CI: a self-hosted runner with no writable cache dir gets no
  # lock path from lock-file, and a lock path whose directory is missing
  # cannot be created. Either way the recipe shell (/bin/sh, dash in the
  # toolchain image, where a failed redirect on ':' exits the shell) holds
  # nothing and the target goes on.
  for lockenv in "CI_IMAGE_LOCK= GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=self-hosted CI_CACHE_ROOT=$D/nolockroot" \
    "CI_IMAGE_LOCK=$D/nolockdir/image.lock"; do
    reset; rec_for "$img"
    # shellcheck disable=SC2086 # word-split the env assignments on purpose
    mk "$target" CI_IMAGE_DIGEST_DIR="$rec" $lockenv
    if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "==> $img is already built on this daemon" ] && [ ! -s "$D/err" ]; then
      ok "$target: no usable lock path holds nothing and the target goes on ($lockenv)"
    else bad "$target: no usable lock path holds nothing and the target goes on ($lockenv, rc=$rc)"; cat "$D/err"; fi
  done
done

# 29. docker-build-it: a build that fails because a concurrent build loaded
# the image first succeeds, and the image is pushed.
reset; fresh; rec_for "$it"
mk docker-build-it CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_BUILD_FAILS=1 STUB_BUILD_RACE=1
if [ "$rc" = 0 ] && grep -q "a concurrent build loaded $it first" "$D/out" && called "^push $reg/$it$"; then
  ok "docker-build-it: a build a concurrent build beat still succeeds"
else bad "docker-build-it: a build a concurrent build beat still succeeds (rc=$rc)"; cat "$D/err"; fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
