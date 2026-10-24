#!/usr/bin/env bash
# ci-registry.sh moves the CI images between this job and the Gitea registry
# (DS-3516). docker-build builds each image once per run and pushes it; every
# other job pulls that exact image by digest. Nothing here builds an image: a
# consumer that cannot pull fails, loudly, rather than run its stages on an
# image docker-build did not push.
#
#   ci-registry.sh push <local-image> <name> <trust>   prints <registry>/<owner>/<name>@sha256:<hex>
#   ci-registry.sh pull <ref> [hold]                   pulls <ref>; with [hold], holds it
#   ci-registry.sh release <hold>...                   removes the holds
#   ci-registry.sh check-ref <ref>...                  validates digest references
#
# The registry is the forge the run belongs to, derived from GITHUB_SERVER_URL;
# no address is written here or in ci.yml. The owner is GITHUB_REPOSITORY_OWNER.
# push and pull log in with CI_REGISTRY_USER and CI_REGISTRY_TOKEN, the latter
# read from the environment and fed to docker on stdin only. The login lives in
# a private DOCKER_CONFIG that is deleted on exit, so the credential outlives
# neither this process nor reaches another job on the same Docker host.
#
# A pushed image is tagged <content-hash>-<trust>: the content hash is the tag
# the Makefile built it under, and trust is main (a push to main) or pr. One
# tag per content hash and trust class; the registry's cleanup rule removes
# superseded versions (docs/release/engineering.md, "The CI image").
#
# A hold is a container created from the image and never started: `docker
# image rm` refuses an image a container uses, so the reaper and the cache trim
# of a concurrent job on the same host cannot remove the image between this
# pull and the job's last stage. The job's teardown releases it.
#
# CI_REGISTRY_BACKOFF is the seconds between pull attempts, times the attempt
# number (default 10, so at most 10 s + 20 s of waiting across 3 attempts).
set -euo pipefail

readonly attempts=3
readonly ref_re='^[^@ ]+@sha256:[0-9a-f]{64}$'

die() {
	echo "::error::ci-registry: $*" >&2
	exit 1
}

# registry_host prints host[:port] of GITHUB_SERVER_URL.
registry_host() {
	local url=${GITHUB_SERVER_URL:-}
	[ -n "$url" ] || die "GITHUB_SERVER_URL is empty: the registry is derived from it, never written down"
	local host=${url#*://}
	host=${host%%/*}
	[[ $host =~ ^[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || die "GITHUB_SERVER_URL does not name a registry host"
	printf '%s\n' "${host,,}"
}

registry_owner() {
	local owner=${GITHUB_REPOSITORY_OWNER:-}
	owner=${owner,,}
	[[ $owner =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "GITHUB_REPOSITORY_OWNER does not name a registry owner"
	printf '%s\n' "$owner"
}

check_ref() {
	[[ $1 =~ $ref_re ]] || die "'$1' is not a digest reference (<registry>/<owner>/<name>@sha256:<64 hex>)"
}

login() {
	local host=$1
	[ -n "${CI_REGISTRY_USER:-}" ] || die "CI_REGISTRY_USER is empty"
	[ -n "${CI_REGISTRY_TOKEN:-}" ] || die "CI_REGISTRY_TOKEN is empty: the job needs secrets.GITHUB_TOKEN with the packages permission"
	DOCKER_CONFIG=$(mktemp -d)
	export DOCKER_CONFIG
	trap 'rm -rf "$DOCKER_CONFIG"' EXIT
	if ! printf '%s' "$CI_REGISTRY_TOKEN" | docker login "$host" -u "$CI_REGISTRY_USER" --password-stdin >/dev/null; then
		die "the Gitea registry refused the login: the run's token cannot use $(registry_owner)'s container packages"
	fi
}

push() {
	[ $# -eq 3 ] || die "usage: push <local-image> <name> <trust>"
	local image=$1 name=$2 trust=$3
	case $trust in main | pr) ;; *) die "trust '$trust' is neither main nor pr" ;; esac
	[[ $name =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "image name '$name' is not a registry repository name"
	local hash=${image##*:}
	[ "$hash" != "$image" ] && [[ $hash =~ ^[A-Za-z0-9_.-]+$ ]] || die "'$image' carries no content-hash tag"
	local host owner
	host=$(registry_host)
	owner=$(registry_owner)
	local repo=$host/$owner/$name
	local target=$repo:$hash-$trust
	login "$host"
	docker tag "$image" "$target" || die "cannot tag $image as $target"
	docker push "$target" >&2 || die "the Gitea registry refused the push of $target"
	local digest="" d
	while IFS= read -r d; do
		case $d in "$repo@"*) digest=$d ;; esac
	done < <(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$target")
	[ -n "$digest" ] || die "the push of $target reported no digest"
	check_ref "$digest"
	printf '%s\n' "$digest"
}

pull() {
	[ $# -ge 1 ] && [ $# -le 2 ] || die "usage: pull <ref> [hold]"
	local ref=$1 hold=${2:-}
	check_ref "$ref"
	local host
	host=$(registry_host)
	[ "${ref%%/*}" = "$host" ] || die "'$ref' is not in this run's registry"
	login "$host"
	local backoff=${CI_REGISTRY_BACKOFF:-10} n
	for ((n = 1; n <= attempts; n++)); do
		if docker pull --quiet "$ref" >/dev/null; then
			[ -n "$hold" ] || return 0
			docker rm -f "$hold" >/dev/null 2>&1 || true
			# A concurrent reap can remove the image between the pull and the
			# hold; the next attempt pulls it again.
			if docker create --pull never --name "$hold" "$ref" true >/dev/null; then
				echo "==> $hold holds $ref until the job's teardown"
				return 0
			fi
		fi
		[ "$n" -lt "$attempts" ] || break
		echo "==> pull of $ref failed (attempt $n of $attempts); retrying" >&2
		sleep $((n * backoff))
	done
	die "cannot pull $ref after $attempts attempts; a consumer never rebuilds the image docker-build pushed"
}

release() {
	local hold
	for hold in "$@"; do
		[ -z "$hold" ] || docker rm -f "$hold" >/dev/null 2>&1 || true
	done
}

main() {
	[ $# -ge 1 ] || die "usage: ci-registry.sh push|pull|release|check-ref ..."
	local cmd=$1
	shift
	case $cmd in
	push) push "$@" ;;
	pull) pull "$@" ;;
	release) release "$@" ;;
	check-ref)
		local r
		for r in "$@"; do check_ref "$r"; done
		;;
	*) die "unknown command '$cmd'" ;;
	esac
}

main "$@"
