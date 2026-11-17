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
    # A pulled image is on the daemon too.
    [ -f "$s/id" ] || [ -f "$s/pulled" ] || exit 1
    case $* in
      *RepoDigests*)
        if [ -z "${STUB_NO_DIGEST:-}" ]; then
          printf '%s\n' "other.example/x@sha256:$(printf 'a%.0s' $(seq 64))"
          [ ! -f "$s/pushed" ] || printf '%s@sha256:%s\n' "$(cat "$s/pushed")" "$(printf 'b%.0s' $(seq 64))"
          [ ! -f "$s/pulled" ] || cat "$s/pulled"
        fi ;;
      *) cat "$s/id" ;;
    esac ;;
  save) printf 'tar-of-%s' "$(cat "$s/id")" > "$3" ;;
  load) printf '%s\n' "${STUB_LOAD_ID:-$(sed 's/^tar-of-//' "$3")}" > "$s/id"; echo "Loaded image" ;;
  login) cat > "$s/password"; printf '%s\n' "${DOCKER_CONFIG:-}" > "$s/config" ;;
  tag) ;;
  pull)
    # STUB_PULL: hit pulls the digest asked for (STUB_PULL_SERVES: another
    # one), whose repo digest `image inspect` then reports; hang never answers
    # (a registry that accepts the connection and stalls); anything else is a
    # miss.
    case ${STUB_PULL:-miss} in
      hit)
        d=${STUB_PULL_SERVES:-${2#*@}}
        printf '%s@%s\n' "${2%@*}" "$d" > "$s/pulled"
        echo 'Status: Downloaded newer image'; echo "Digest: $d" ;;
      hang) exec sleep 30 ;;
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
    printf '%s\n' "${2%:*}" > "$s/pushed" ;;
  buildx)
    # The make targets' build (DEVCONTAINER_BUILD): it loads the image, or
    # with STUB_BUILD_FAILS fails; STUB_BUILD_RACE loads it first, as a
    # concurrent build on the same daemon does.
    case $2 in
      version) ;;
      build)
        [ -z "${STUB_BUILD_RACE:-}" ] || printf 'sha256:raced\n' > "$s/id"
        [ -z "${STUB_BUILD_FAILS:-}" ] || exit 7
        printf 'sha256:built\n' > "$s/id" ;;
      *) exit 1 ;;
    esac ;;
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
  [ "$(cat "$D/err")" = "miss, built, push failed" ] && [ ! -e "$rec/qrdb-ci-it_fedcba9876543210" ]; then
  ok "registry: a stalled pull and push time out, fall back and log the push failure"
else bad "registry: a stalled pull and push time out, fall back and log the push failure (pull rc=$pull_rc, push rc=$rc)"; cat "$D/err"; fi

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
    PATH="$D/bin:$PATH" STUB_STATE="$D/state" "$@" \
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
  reset; rm -f "$D/state/id"; rec_for "$img" "$dig"
  mk "$target" "${r[@]}" STUB_PULL=hit
  if [ "$rc" = 0 ] && called "^pull $repo@$dig$" && ! called '^buildx build ' && ! called '^push '; then
    ok "$target: a registry hit skips the build"
  else bad "$target: a registry hit skips the build (rc=$rc)"; cat "$D/err"; fi

  # 23. A miss builds, then pushes and records the digest.
  reset; rm -f "$D/state/id"; rec_for "$img"
  mk "$target" "${r[@]}" STUB_PULL=miss
  if [ "$rc" = 0 ] && called "^buildx build .*--target $stage -t $img " && before '^buildx build ' "^push $reg/$img$" &&
    [ "$(cat "$rec/${img//:/_}")" = "$pushdig" ] && grep -qx "miss, built and pushed $repo@$pushdig" "$D/err"; then
    ok "$target: a miss builds, then pushes"
  else bad "$target: a miss builds, then pushes (rc=$rc)"; cat "$D/err"; fi

  # 24. A failed push still exits 0.
  reset; rm -f "$D/state/id"; rec_for "$img"
  mk "$target" "${r[@]}" STUB_PUSH_FAILS=9
  if [ "$rc" = 0 ] && called '^buildx build ' && called "^push $reg/$img$" && grep -qx 'miss, built, push failed' "$D/err"; then
    ok "$target: a failed push still exits 0"
  else bad "$target: a failed push still exits 0 (rc=$rc)"; cat "$D/err"; fi

  # 25. A failed build exits non-zero and pushes nothing.
  reset; rm -f "$D/state/id"; rec_for "$img"
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
  if [ "$rc" = 0 ] && ! called '^push ' && ! called '^pull ' && ! called '^tag ' && ! called '^buildx build ' && [ ! -s "$D/err" ]; then
    ok "$target: a held image with a record is not pushed"
  else bad "$target: a held image with a record is not pushed (rc=$rc)"; cat "$D/err"; fi

  # 28. Registry unset: a held image is today's one inspect and one line; a
  # missing one is built with no registry call.
  reset; rec_for "$img"
  mk "$target" CI_IMAGE_DIGEST_DIR="$rec"
  unset_out=$(cat "$D/out") unset_err=$(cat "$D/err") unset_calls=$(cat "$D/state/calls")
  reset; rm -f "$D/state/id"
  mk "$target"
  if [ "$unset_out" = "==> $img is already built on this daemon" ] && [ -z "$unset_err" ] &&
    [ "$unset_calls" = "$(printf 'buildx version\nimage inspect %s' "$img")" ] &&
    [ "$rc" = 0 ] && called '^buildx build ' && ! called '^pull ' && ! called '^push ' && ! called '^tag '; then
    ok "$target: an unset registry changes nothing"
  else bad "$target: an unset registry changes nothing (rc=$rc)"; printf '%s\n%s\n' "$unset_out" "$unset_calls"; cat "$D/err"; fi
done

# 29. docker-build-it: a build that fails because a concurrent build loaded
# the image first succeeds, and the image is pushed.
reset; rm -f "$D/state/id"; rec_for "$it"
mk docker-build-it CI_IMAGE_REGISTRY="$reg" CI_IMAGE_DIGEST_DIR="$rec" STUB_BUILD_FAILS=1 STUB_BUILD_RACE=1
if [ "$rc" = 0 ] && grep -q "a concurrent build loaded $it first" "$D/out" && called "^push $reg/$it$"; then
  ok "docker-build-it: a build a concurrent build beat still succeeds"
else bad "docker-build-it: a build a concurrent build beat still succeeds (rc=$rc)"; cat "$D/err"; fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
