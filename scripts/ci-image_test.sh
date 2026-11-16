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
        [ "${#files[@]}" -le 1 ] || exit 1
        rm -f "${files[@]}" "$s/ids/${id#sha256:}"; exit 0 ;;
    esac
    ref=${*: -1}
    id=$(resolve "$ref") || exit 1
    case $* in
      *RepoDigests*)
        [ -n "${STUB_NO_DIGEST:-}" ] || printf '%s\n' "other.example/x@sha256:$(printf 'a%.0s' $(seq 64))" "$(cat "$s/pushed")@sha256:$(printf 'b%.0s' $(seq 64))" ;;
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
    settag "$ref" "$id"; echo "Loaded image: $ref" ;;
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
  push)
    n=$(($(cat "$s/push-tries" 2>/dev/null || echo 0) + 1))
    printf '%s\n' "$n" > "$s/push-tries"
    if [ "$n" -le "${STUB_PUSH_FAILS:-0}" ]; then
      echo 'received unexpected HTTP status: 500 Internal Server Error' >&2
      exit 1
    fi
    retag push
    resolve "$2" > "$s/pushed-id"
    printf '%s\n' "${2%:*}" > "$s/pushed" ;;
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
  printf '%s\n' "$id_a" > "$D/iid"
}
tagimg() {
  mkdir -p "$D/state/tags" "$D/state/ids"
  printf '%s\n%s\n' "$2" "$1" > "$D/state/tags/${1//[^A-Za-z0-9._-]/_}"
  : > "$D/state/ids/${2#sha256:}"
}
# run <args...> [-- env...]: the script, its status in $rc, stdout in $D/out,
# stderr in $D/err.
run() {
  local args=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do args+=("$1"); shift; done
  [ $# -eq 0 ] || shift
  rc=0
  env PATH="$D/bin:$PATH" STUB_STATE="$D/state" GITHUB_REPOSITORY_OWNER=Example-Org CI_IMAGE_IDS="$D/no-ids" \
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
rm -rf "$D/state/tags" "$D/state/ids"
run load "$D/art" "$tc" "$id_a"
if [ "$rc" = 0 ] && [ ! -e "$D/art/image.tar" ] && grep -q "is $id_a, the image docker-build built" "$D/err"; then
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
# 12a3. An old CI image that also carries a foreign tag, or two shared
# content-hash tags, is left: prune never forces a removal, and never
# untags a shared tag by name (another job can move it at any moment).
reset
mkdir -p "$D/state/created"
tagimg qrdb-ci-it:5555555555555555 "$id_b"
tagimg mytool:keep "$id_b"
tagimg qrdb-ci-it:6666666666666666 "$id_c"
tagimg qrdb-toolchain:6666666666666666 "$id_c"
for i in "$id_a" "$id_b" "$id_c"; do echo "$old" > "$D/state/created/${i#sha256:}"; done
run prune 24
if [ "$rc" = 0 ] && [ -e "$D/state/tags/mytool_keep" ] && [ -e "$D/state/tags/qrdb-ci-it_5555555555555555" ] &&
  [ -e "$D/state/tags/qrdb-ci-it_6666666666666666" ] && [ ! -e "$D/state/tags/${tc//[^A-Za-z0-9._-]/_}" ] &&
  grep -q 'kept qrdb-ci-it:5555555555555555 .*not a CI image' "$D/err" && grep -q 'the daemon refused its removal' "$D/err"; then
  ok "prune leaves an image with a foreign tag or two shared tags"
else bad "prune leaves an image with a foreign tag or two shared tags (rc=$rc)"; cat "$D/err"; fi
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

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
