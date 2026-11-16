# shellcheck shell=bash
# image-registry.sh is sourced, never run: the pull-or-build path for the CI
# images through the CI image registry (#1609, docs/release/engineering.md,
# "The CI image"). scripts/ci-image.sh (registry-pull, registry-push) and
# scripts/in-toolchain-image.sh call it; make docker-build-toolchain and
# docker-build-it reach it through ci-image.sh.
#
# CI_IMAGE_REGISTRY is the runner's environment, host[:port], plain HTTP and
# no credential: nothing here logs in. Unset, both functions make no docker
# call and print nothing, so the caller does exactly what it did without
# them. A value that is not host[:port] is treated as an unreachable
# registry. The registry's image is the local content-hash tag under the
# registry: <registry>/<name>:<tag>.
#
# Neither function fails its caller's job: a pull or a push that fails is a
# miss, and each is bounded by `timeout` (CI_IMAGE_REGISTRY_TIMEOUT seconds,
# default 900), so a hung registry falls back to the build. Each image logs
# exactly one line on stderr: `pulled <ref> <digest>` from ci_registry_pull,
# else `miss, built and pushed <ref>` or `miss, built, push failed` from
# ci_registry_push.

# ci_registry_ref prints <registry>/<image>, or fails when CI_IMAGE_REGISTRY
# is unset or not host[:port].
ci_registry_ref() {
	local reg=${CI_IMAGE_REGISTRY:-}
	[[ $reg =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]] || return 1
	printf '%s/%s\n' "$reg" "$1"
}

# ci_registry_timeout prints the per-call bound in seconds.
ci_registry_timeout() {
	local t=${CI_IMAGE_REGISTRY_TIMEOUT:-900}
	[[ $t =~ ^[1-9][0-9]{0,4}$ ]] || t=900
	printf '%s\n' "$t"
}

# ci_registry_pull <image> pulls <registry>/<image> and tags it <image>.
# Status 0 when the image is now on the daemon; 1, silently, otherwise.
ci_registry_pull() {
	local image=$1 ref out line digest=""
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 1
	ref=$(ci_registry_ref "$image") || return 1
	out=$(timeout -k 10 "$(ci_registry_timeout)" docker pull "$ref" 2>/dev/null) || return 1
	docker tag "$ref" "$image" >/dev/null 2>&1 || return 1
	while IFS= read -r line; do
		case $line in "Digest: "*) digest=${line#Digest: } ;; esac
	done <<<"$out"
	echo "pulled $ref ${digest:-unknown}" >&2
}

# ci_registry_push <image> pushes the image just built as <registry>/<image>.
# Status 0 always; a no-op when CI_IMAGE_REGISTRY is unset.
ci_registry_push() {
	local image=$1 ref
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 0
	if ref=$(ci_registry_ref "$image") &&
		docker tag "$image" "$ref" >/dev/null 2>&1 &&
		timeout -k 10 "$(ci_registry_timeout)" docker push "$ref" >/dev/null 2>&1; then
		echo "miss, built and pushed $ref" >&2
	else
		echo "miss, built, push failed" >&2
	fi
	return 0
}
