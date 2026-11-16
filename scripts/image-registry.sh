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
# A plain-HTTP registry is not trusted by tag. The push records the pushed
# image's repo digest in CI_IMAGE_DIGEST_DIR, one file per content-hash tag
# (<name>_<tag>, holding sha256:<hex>); CI keeps that directory in a GitHub
# cache entry keyed by the tag, never in the repository. The pull asks for
# <registry>/<name>@<digest> only when that record exists, then checks the
# pulled image carries that repo digest. No record, a malformed one or a
# mismatch is a miss, so the caller builds.
#
# Neither function fails its caller's job: a pull or a push that fails is a
# miss, and each is bounded by `timeout` (CI_IMAGE_REGISTRY_TIMEOUT seconds,
# default 900), so a hung registry falls back to the build. Each image logs
# exactly one line on stderr: `pulled <repo>@<digest>` from ci_registry_pull,
# else `miss, built and pushed <repo>@<digest>` or `miss, built, push failed`
# from ci_registry_push; a mismatch logs `miss, <repo>@<digest> does not match
# its record`.

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

# ci_registry_record <image> prints the path of the image's digest record, or
# fails when CI_IMAGE_DIGEST_DIR is unset.
ci_registry_record() {
	[ -n "${CI_IMAGE_DIGEST_DIR:-}" ] || return 1
	printf '%s/%s\n' "$CI_IMAGE_DIGEST_DIR" "${1//:/_}"
}

# ci_registry_digest <repo> <ref> prints the sha256 digest <ref> carries under
# <repo>, or fails when it carries none.
ci_registry_digest() {
	local repo=$1 line
	while IFS= read -r line; do
		case $line in "$repo@sha256:"*)
			if [[ ${line#"$repo@"} =~ ^sha256:[0-9a-f]{64}$ ]]; then
				printf '%s\n' "${line#"$repo@"}"
				return 0
			fi ;;
		esac
	done < <(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$2" 2>/dev/null)
	return 1
}

# ci_registry_pull <image> pulls <registry>/<name>@<recorded digest>, checks
# it and tags it <image>. Status 0 when the image is now on the daemon; 1
# otherwise.
ci_registry_pull() {
	local image=$1 ref repo record digest="" got
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 1
	ref=$(ci_registry_ref "$image") || return 1
	repo=${ref%:*}
	record=$(ci_registry_record "$image") || return 1
	[ -f "$record" ] || return 1
	IFS= read -r digest <"$record" || true
	[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
	timeout -k 10 "$(ci_registry_timeout)" docker pull "$repo@$digest" >/dev/null 2>&1 || return 1
	if ! got=$(ci_registry_digest "$repo" "$repo@$digest") || [ "$got" != "$digest" ]; then
		echo "miss, $repo@$digest does not match its record" >&2
		docker rmi "$repo@$digest" >/dev/null 2>&1 || true
		return 1
	fi
	docker tag "$repo@$digest" "$image" >/dev/null 2>&1 || return 1
	echo "pulled $repo@$digest" >&2
}

# ci_registry_push <image> pushes the image just built as <registry>/<image>
# and records its repo digest. Status 0 always; a no-op when
# CI_IMAGE_REGISTRY is unset.
ci_registry_push() {
	local image=$1 ref repo record digest
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 0
	if ! { ref=$(ci_registry_ref "$image") &&
		docker tag "$image" "$ref" >/dev/null 2>&1 &&
		timeout -k 10 "$(ci_registry_timeout)" docker push "$ref" >/dev/null 2>&1; }; then
		echo "miss, built, push failed" >&2
		return 0
	fi
	repo=${ref%:*}
	if ! digest=$(ci_registry_digest "$repo" "$image"); then
		echo "miss, built and pushed $ref, no digest to record" >&2
		return 0
	fi
	if record=$(ci_registry_record "$image") &&
		! { mkdir -p "${record%/*}" && printf '%s\n' "$digest" >"$record"; } 2>/dev/null; then
		echo "miss, built and pushed $repo@$digest, record not written" >&2
		return 0
	fi
	echo "miss, built and pushed $repo@$digest" >&2
	return 0
}
