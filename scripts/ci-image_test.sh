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

# The stub's daemon: $s/id is the ID the image under test has once loaded or
# built ($STUB_ID), and $s/loaded-id the ID `docker load` gives it
# ($STUB_LOAD_ID, default the saved one). STUB_NO_DIGEST makes a push report
# no repo digest. STUB_PUSH_FAILS makes the first that many pushes fail with a
# registry 500, counted in $s/push-tries. The login's password is read from stdin and recorded apart
# from the argv log, so a test can tell it never reached a command line.
cat > "$D/bin/docker" <<'STUB'
#!/usr/bin/env bash
set -eu
s=$STUB_STATE
printf '%s\n' "$*" >> "$s/calls"
case $1 in
  image)
    [ -f "$s/id" ] || exit 1
    case $* in
      *RepoDigests*)
        [ -n "${STUB_NO_DIGEST:-}" ] || printf '%s\n' "other.example/x@sha256:$(printf 'a%.0s' $(seq 64))" "$(cat "$s/pushed")@sha256:$(printf 'b%.0s' $(seq 64))" ;;
      *) cat "$s/id" ;;
    esac ;;
  save) printf 'tar-of-%s' "$(cat "$s/id")" > "$3" ;;
  load) printf '%s\n' "${STUB_LOAD_ID:-$(sed 's/^tar-of-//' "$3")}" > "$s/id"; echo "Loaded image" ;;
  login) cat > "$s/password"; printf '%s\n' "${DOCKER_CONFIG:-}" > "$s/config" ;;
  tag) ;;
  push)
    n=$(($(cat "$s/push-tries" 2>/dev/null || echo 0) + 1))
    printf '%s\n' "$n" > "$s/push-tries"
    if [ "$n" -le "${STUB_PUSH_FAILS:-0}" ]; then
      echo 'received unexpected HTTP status: 500 Internal Server Error' >&2
      exit 1
    fi
    printf '%s\n' "${2%:*}" > "$s/pushed" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$D/bin/docker"

id_a="sha256:$(printf '1%.0s' $(seq 64))"
id_b="sha256:$(printf '2%.0s' $(seq 64))"
tc=qrdb-toolchain:0123456789abcdef

reset() {
  rm -rf "$D/state" "$D/art"
  mkdir -p "$D/state"
  printf '%s\n' "$id_a" > "$D/state/id"
}
# run <args...> [-- env...]: the script, its status in $rc, stdout in $D/out,
# stderr in $D/err.
run() {
  local args=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do args+=("$1"); shift; done
  [ $# -eq 0 ] || shift
  rc=0
  env PATH="$D/bin:$PATH" STUB_STATE="$D/state" GITHUB_REPOSITORY_OWNER=Example-Org "$@" \
    "$here/ci-image.sh" "${args[@]}" > "$D/out" 2> "$D/err" || rc=$?
}
called() { grep -q -- "$1" "$D/state/calls" 2>/dev/null; }

# 1. save writes the tar and the ID beside it and prints the ID.
reset
run save "$tc" "$D/art"
if [ "$rc" = 0 ] && [ -s "$D/art/image.tar" ] && [ "$(cat "$D/art/image.id")" = "$id_a" ] &&
  [ "$(cat "$D/out")" = "$id_a" ] && called "^save -o $D/art/image.tar $tc$"; then
  ok "save writes the image and its ID"
else bad "save writes the image and its ID (rc=$rc)"; cat "$D/err"; fi

# 2. save refuses an image that is not a content-hash tag of the CI families.
reset
run save qrdb-toolchain:latest "$D/art"
if [ "$rc" = 1 ] && grep -q 'is not a content-hash tag' "$D/err" && [ ! -e "$D/art" ]; then
  ok "save refuses an image that is not a content-hash tag"
else bad "save refuses an image that is not a content-hash tag (rc=$rc)"; fi

# 3. load accepts the image docker-build built and removes the tar.
reset
run save "$tc" "$D/art"
rm -f "$D/state/id"
run load "$D/art" "$tc" "$id_a"
if [ "$rc" = 0 ] && [ ! -e "$D/art/image.tar" ] && grep -q "is $id_a, the image docker-build built" "$D/err"; then
  ok "load accepts the image docker-build built"
else bad "load accepts the image docker-build built (rc=$rc)"; cat "$D/err"; fi

# 4. load refuses an image whose ID is not the one docker-build recorded.
reset
run save "$tc" "$D/art"
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

# 7. push logs in on stdin into a private DOCKER_CONFIG, pushes
# ghcr.io/<owner, lowercased>/<name>:<hash>-main and prints its digest.
reset
run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=s3cr3t-token
want="ghcr.io/example-org/go-qrdb-toolchain@sha256:$(printf 'b%.0s' $(seq 64))"
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$want" ] &&
  called "^tag $tc ghcr.io/example-org/go-qrdb-toolchain:0123456789abcdef-main$" &&
  called '^login ghcr.io -u bot --password-stdin$' && [ "$(cat "$D/state/password")" = s3cr3t-token ] &&
  ! grep -q s3cr3t-token "$D/state/calls" "$D/out" "$D/err" &&
  [ -n "$(cat "$D/state/config")" ] && [ ! -e "$(cat "$D/state/config")" ]; then
  ok "push logs in on stdin, pushes by content hash and prints the digest"
else bad "push logs in on stdin, pushes by content hash and prints the digest (rc=$rc)"; cat "$D/err" "$D/out"; fi

# 8. push without the run's token fails naming the variable, not a value.
reset
run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot
if [ "$rc" = 1 ] && grep -q 'CI_REGISTRY_TOKEN is empty' "$D/err" && ! called '^push '; then
  ok "push refuses an empty token"
else bad "push refuses an empty token (rc=$rc)"; cat "$D/err"; fi

# 9. A push the registry reports no digest for fails.
reset
run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_NO_DIGEST=1
if [ "$rc" = 1 ] && grep -q 'reported no digest' "$D/err" && [ ! -s "$D/out" ]; then
  ok "a push with no digest fails"
else bad "a push with no digest fails (rc=$rc)"; cat "$D/err"; fi

# 10. A push the registry refuses once succeeds on the next attempt. No retry
# test sleeps: each sets CI_REGISTRY_BACKOFF=0.
reset
run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_PUSH_FAILS=1 CI_REGISTRY_BACKOFF=0
if [ "$rc" = 0 ] && [ "$(cat "$D/out")" = "$want" ] &&
  [ "$(grep -c '^push ' "$D/state/calls")" = 2 ] && grep -q 'attempt 1 of 3' "$D/err"; then
  ok "a push the registry refuses once succeeds on the next attempt"
else bad "a push the registry refuses once succeeds on the next attempt (rc=$rc)"; cat "$D/err"; fi

# 11. A push refused on every attempt fails after the last.
reset
run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t STUB_PUSH_FAILS=9 CI_REGISTRY_BACKOFF=0
if [ "$rc" = 1 ] && grep -q 'GHCR refused the push of' "$D/err" &&
  [ "$(grep -c '^push ' "$D/state/calls")" = 3 ] && [ ! -s "$D/out" ]; then
  ok "a push refused on every attempt fails after the last"
else bad "a push refused on every attempt fails after the last (rc=$rc)"; cat "$D/err"; fi

# 12. A malformed attempt count or backoff is refused before the login and
# the push.
knobs=ok
for kv in CI_REGISTRY_ATTEMPTS=0 CI_REGISTRY_BACKOFF=soon; do
  reset
  run push "$tc" go-qrdb-toolchain -- CI_REGISTRY_USER=bot CI_REGISTRY_TOKEN=t "$kv"
  if [ "$rc" != 1 ] || ! grep -q "${kv%%=*}" "$D/err" || called '^push ' || called '^login '; then
    knobs="$kv rc=$rc"
    cat "$D/err"
  fi
done
if [ "$knobs" = ok ]; then ok "a malformed attempt count or backoff is refused before the push"
else bad "a malformed attempt count or backoff is refused before the push ($knobs)"; fi

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
