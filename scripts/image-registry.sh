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
# default 300), so a hung registry falls back to the build. One outage budget
# serves the whole job: the first transfer that times out or cannot connect
# leaves a marker under ${RUNNER_TEMP:-${TMPDIR:-/tmp}} (named by a hash of
# the registry value, never the value), and every later pull or push in the
# job that finds it skips at once, logging only `registry unavailable,
# skipped <ref>`, with the status a failed transfer has. A registry that
# answers, even "not found", leaves no marker. Otherwise each image logs
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
	local t=${CI_IMAGE_REGISTRY_TIMEOUT:-300}
	[[ $t =~ ^[1-9][0-9]{0,4}$ ]] || t=300
	printf '%s\n' "$t"
}

# ci_registry_outage_marker <registry> prints the path of this job's outage
# marker for <registry>: a file in the job's temporary directory named by the
# value's sha256, so the address never reaches a file name.
ci_registry_outage_marker() {
	local sum
	sum=$(printf '%s' "$1" | sha256sum 2>/dev/null) && [ -n "$sum" ] || return 1
	printf '%s/ci-image-registry-outage-%s\n' "${RUNNER_TEMP:-${TMPDIR:-/tmp}}" "${sum:0:16}"
}

# ci_registry_skip <registry> <ref> succeeds, logging `registry unavailable,
# skipped <ref>`, when an earlier transfer in this job left the outage marker
# for <registry>; it fails, silently, otherwise.
ci_registry_skip() {
	local marker
	marker=$(ci_registry_outage_marker "$1") && [ -e "$marker" ] || return 1
	echo "registry unavailable, skipped $2" >&2
}

# ci_registry_outage <registry> <status> [<stderr>] leaves the outage marker
# for <registry> when a transfer's <status> is timeout's expiry (124, or 137
# after -k) or its <stderr> says the registry could not be reached. Any other
# failure, a "manifest unknown" or an HTTP error, is the registry answering.
ci_registry_outage() {
	local marker
	case $2 in
	124 | 137) ;;
	*)
		case ${3:-} in
		*"connection refused"* | *"no such host"* | *"i/o timeout"* | *"no route to host"* | \
			*"network is unreachable"* | *"connection reset"* | *"TLS handshake timeout"* | \
			*"Client.Timeout exceeded"*) ;;
		*) return 0 ;;
		esac
		;;
	esac
	marker=$(ci_registry_outage_marker "$1") || return 0
	: >"$marker" 2>/dev/null || true
}

# ci_registry_transfer <docker args...> runs one bounded registry pull or push
# for CI_IMAGE_REGISTRY, output discarded, and leaves the outage marker when
# it timed out or could not connect. Its status is docker's (or timeout's).
ci_registry_transfer() {
	local err rc=0
	err=$(timeout -k 10 "$(ci_registry_timeout)" docker "$@" 2>&1 >/dev/null) || rc=$?
	[ "$rc" = 0 ] || ci_registry_outage "$CI_IMAGE_REGISTRY" "$rc" "$err"
	return "$rc"
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

# ci_registry_recorded <image> prints the sha256 digest CI_IMAGE_DIGEST_DIR
# records for <image>, or fails when there is no usable record: the directory
# unset, no file, or a file that does not hold sha256:<64 hex>. The pull trusts
# only this digest; a held image without it is published again
# (ci-image.sh registry-push --unrecorded).
ci_registry_recorded() {
	local record digest=""
	record=$(ci_registry_record "$1") || return 1
	[ -f "$record" ] || return 1
	IFS= read -r digest <"$record" || true
	[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
	printf '%s\n' "$digest"
}

# ci_registry_pull <image> pulls <registry>/<name>@<recorded digest>, checks
# it and tags it <image>. Status 0 when the image is now on the daemon; 1
# otherwise.
ci_registry_pull() {
	local image=$1 ref repo digest got
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 1
	ref=$(ci_registry_ref "$image") || return 1
	repo=${ref%:*}
	digest=$(ci_registry_recorded "$image") || return 1
	! ci_registry_skip "$CI_IMAGE_REGISTRY" "$repo@$digest" || return 1
	ci_registry_transfer pull "$repo@$digest" || return 1
	if ! got=$(ci_registry_digest "$repo" "$repo@$digest") || [ "$got" != "$digest" ]; then
		echo "miss, $repo@$digest does not match its record" >&2
		docker rmi "$repo@$digest" >/dev/null 2>&1 || true
		return 1
	fi
	docker tag "$repo@$digest" "$image" >/dev/null 2>&1 || return 1
	echo "pulled $repo@$digest" >&2
}

# ci_registry_push <image> [<how>] pushes the image just built as
# <registry>/<image> and records its repo digest. <how> opens its log line
# (default "miss, built"; a held image published again passes "held,
# unrecorded"). Status 0 always; a no-op when CI_IMAGE_REGISTRY is unset.
ci_registry_push() {
	local image=$1 how=${2:-miss, built} ref repo record digest
	[ -n "${CI_IMAGE_REGISTRY:-}" ] || return 0
	if ref=$(ci_registry_ref "$image") && ci_registry_skip "$CI_IMAGE_REGISTRY" "$ref"; then
		return 0
	fi
	if ! { [ -n "$ref" ] &&
		docker tag "$image" "$ref" >/dev/null 2>&1 &&
		ci_registry_transfer push "$ref"; }; then
		echo "$how, push failed" >&2
		return 0
	fi
	repo=${ref%:*}
	if ! digest=$(ci_registry_digest "$repo" "$image"); then
		echo "$how and pushed $ref, no digest to record" >&2
		return 0
	fi
	if record=$(ci_registry_record "$image") &&
		! { mkdir -p "${record%/*}" && printf '%s\n' "$digest" >"$record"; } 2>/dev/null; then
		echo "$how and pushed $repo@$digest, record not written" >&2
		return 0
	fi
	echo "$how and pushed $repo@$digest" >&2
	return 0
}
