#!/usr/bin/env bash
# ci-registry.sh hands the CI images from docker-build to every other job
# (DS-3516), on one of two paths chosen by the event (claude-agents#329):
#
#   - A push to main: docker-build pushes each image to the forge's container
#     registry, the Gitea registry or GHCR on the public mirror, and every
#     other job pulls that exact image by digest. A consumer that cannot pull
#     fails, loudly, rather than run its stages on an image docker-build did
#     not push.
#   - Every other event (a pull request, a dispatch): nothing logs in and
#     nothing is pushed, because the run's token cannot log in to the registry
#     there. Each job takes the image from its own runner's daemon under the
#     content-hash family tag (handbook ci-standard.md §9), building it when
#     the daemon does not hold it yet, and holds it by its local image ID: no
#     digest exists without a push.
#
#   ci-registry.sh push <local-image> <name> <trust>   prints <registry>/<owner>/<name>@sha256:<hex>
#   ci-registry.sh pull <ref> [hold]                   pulls <ref>; with [hold], holds it
#   ci-registry.sh local toolchain|it <hold>           builds if missing, holds, prints the reference to run
#   ci-registry.sh release <hold>...                   removes the holds
#   ci-registry.sh check-ref <ref>...                  validates digest references
#
# The registry is the forge the run belongs to, derived from GITHUB_SERVER_URL:
# ghcr.io on GitHub, the forge's own host[:port] on Gitea (handbook devops.md
# §2); no internal address is written here or in ci.yml. The owner is GITHUB_REPOSITORY_OWNER.
#
# A push to the Gitea registry runs on the release runner alone (ruling 169)
# and reads that runner's credential contract and nothing else (ruling 173,
# claude-agents#329): the three names its runner env file exports,
#
#   RELEASE_REGISTRY         the registry's host[:port]; the push goes there and
#                            the digest reference it prints names it
#   RELEASE_REGISTRY_USER    the login's user
#   RELEASE_REGISTRY_TOKEN   the login's password, fed to docker on stdin
#
# A name change to the contract is an Infrastructure PR to claude-agents,
# announced on claude-agents#329 before it lands. A pull, and a push to GHCR,
# log in with CI_REGISTRY_USER and CI_REGISTRY_TOKEN, the run's own token that
# the workflow step supplies. No credential value is ever printed: an empty
# name fails naming the variable, never a value. The login lives in a private
# DOCKER_CONFIG that is deleted on exit, so the credential outlives neither
# this process nor reaches another job on the same Docker host. local never
# logs in, pushes or pulls, whatever the environment holds.
#
# A pushed image is tagged <content-hash>-<trust>: the content hash is the tag
# the Makefile built it under, and trust is main (a push to main) or pr. One
# tag per content hash and trust class; the registry's cleanup rule removes
# superseded versions (docs/release/engineering.md, "The CI image").
#
# A hold is a container created from the image and never started: `docker
# image rm` refuses an image a container uses, and the reaper keeps any tag
# whose image a container uses, so neither it nor the cache trim of a
# concurrent job on the same host can remove the image between this step and
# the job's last stage. The job's teardown releases it. On the local path the
# toolchain hold is created from the image ID and the stages run that ID; the
# integration hold is the Makefile's own (docker-build-it with IT_HOLD) and the
# stages run the content-hash tag, because compose resolves `image:` as a
# reference name and an image ID is not one. Its hold uses the tag's image ID
# all the same, so the reaper keeps the tag until the teardown.
#
# CI_REGISTRY_BACKOFF is the seconds between pull attempts, times the attempt
# number (default 10, so at most 10 s + 20 s of waiting across 3 attempts).
set -euo pipefail

readonly attempts=3
readonly ref_re='^[^@ ]+@sha256:[0-9a-f]{64}$'
readonly host_re='^[A-Za-z0-9.-]+(:[0-9]+)?$'

die() {
	echo "::error::ci-registry: $*" >&2
	exit 1
}

# registry_host prints the registry of the forge at GITHUB_SERVER_URL
# (handbook devops.md §2): on GitHub that is GHCR, ghcr.io, which is not the
# forge's own host; on Gitea the registry shares the web UI's host[:port], so
# it is derived from the URL rather than written down.
registry_host() {
	local url=${GITHUB_SERVER_URL:-}
	[ -n "$url" ] || die "GITHUB_SERVER_URL is empty: the registry is derived from it, never written down"
	local host=${url#*://}
	host=${host%%/*}
	[[ $host =~ $host_re ]] || die "GITHUB_SERVER_URL does not name a registry host"
	host=${host,,}
	case $host in github.com) host=ghcr.io ;; esac
	printf '%s\n' "$host"
}

# push_host prints the registry a push goes to: GHCR on GitHub; on the forge,
# RELEASE_REGISTRY, the release runner's own (ruling 173), validated as a
# host[:port] the way registry_host validates the forge's.
push_host() {
	local host
	host=$(registry_host)
	if [ "$host" = ghcr.io ]; then
		printf '%s\n' "$host"
		return 0
	fi
	host=${RELEASE_REGISTRY:-}
	[ -n "$host" ] || die "RELEASE_REGISTRY is empty: the release runner's credential is not in the job's environment"
	[[ $host =~ $host_re ]] || die "RELEASE_REGISTRY does not name a registry host"
	printf '%s\n' "${host,,}"
}

# registry_name names the registry at host $1 in an error message.
registry_name() {
	case $1 in
	ghcr.io) printf 'GHCR\n' ;;
	*) printf 'the Gitea registry\n' ;;
	esac
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

# credential_source names, in a message, the credential a login with
# operation $2 at host $1 uses: a push to the Gitea registry uses the release
# runner's own, which its runner env file puts in the job's environment
# (rulings 169 and 173); a pull, and any login to GHCR, uses the run's token.
# It names the credential, never its value.
credential_source() {
	case $1:$2 in
	ghcr.io:* | *:pull) printf "the run's token\n" ;;
	*) printf "the release runner's credential\n" ;;
	esac
}

# credential_names prints the user and token variable names that hold the
# credential source $1 names.
credential_names() {
	case $1 in
	"the run's token") printf 'CI_REGISTRY_USER CI_REGISTRY_TOKEN\n' ;;
	*) printf 'RELEASE_REGISTRY_USER RELEASE_REGISTRY_TOKEN\n' ;;
	esac
}

# login logs in to host $1 for operation $2, push or pull.
login() {
	local host=$1 src user token
	src=$(credential_source "$1" "$2")
	read -r user token < <(credential_names "$src")
	[ -n "${!user:-}" ] || die "$user is empty: $src is not in the job's environment"
	[ -n "${!token:-}" ] || die "$token is empty: $src is not in the job's environment"
	DOCKER_CONFIG=$(mktemp -d)
	export DOCKER_CONFIG
	trap 'rm -rf "$DOCKER_CONFIG"' EXIT
	if ! printf '%s' "${!token}" | docker login "$host" -u "${!user}" --password-stdin >/dev/null; then
		die "$(registry_name "$host") refused the login: $src cannot use $(registry_owner)'s container packages"
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
	host=$(push_host)
	owner=$(registry_owner)
	local repo=$host/$owner/$name
	local target=$repo:$hash-$trust
	login "$host" push
	docker tag "$image" "$target" || die "cannot tag $image as $target"
	docker push "$target" >&2 || die "$(registry_name "$host") refused the push of $target"
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
	login "$host" pull
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

# stage_modauth writes the module credentials the image build reads as its
# gomodauth BuildKit secret to the private file $1 (the form docker-build's
# build step writes). Nothing prints it.
stage_modauth() {
	(
		umask 077
		: >"$1"
		local v
		for v in GOPRIVATE GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0; do
			[ -z "${!v:-}" ] || printf '%s=%s\n' "$v" "${!v}" >>"$1"
		done
	)
}

# local_image is the path off a push to main: it takes family $1's image from
# this runner's daemon under its content-hash tag, building it when the daemon
# does not hold it, holds it as $2 and prints the reference the stages run.
# It never logs in, pushes or pulls.
local_image() {
	[ $# -eq 2 ] || die "usage: local toolchain|it <hold>"
	local family=$1 hold=$2 tag_re
	case $family in
	toolchain) tag_re='^qrdb-toolchain:[0-9a-f]{16}$' ;;
	it) tag_re='^qrdb-ci-it:[0-9a-f]{16}$' ;;
	*) die "family '$family' is neither toolchain nor it" ;;
	esac
	[[ $hold =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "'$hold' is not a hold name"
	local tag
	tag=$(env -u TOOLCHAIN_IMAGE -u IT_IMAGE make -s --no-print-directory "$family-image")
	[[ $tag =~ $tag_re ]] || die "'$tag' is not a content-hash tag of the $family family"
	# Global, not local: the EXIT trap runs after this function has returned.
	modauth=$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/gomodauth.XXXXXX")
	trap 'rm -f "$modauth"' EXIT
	stage_modauth "$modauth"
	if [ "$family" = it ]; then
		# docker-build-it builds only when the daemon lacks the tag, survives a
		# concurrent build of the same tag and holds the image as IT_HOLD.
		env -u IT_IMAGE make --no-print-directory docker-build-it IT_HOLD="$hold" MODULE_ENV_FILE="$modauth" >&2 ||
			die "cannot build or hold $tag on this runner"
		printf '%s\n' "$tag"
		return 0
	fi
	local n id
	for ((n = 1; n <= 2; n++)); do
		if docker image inspect "$tag" >/dev/null 2>&1; then
			echo "==> $tag is already built on this daemon" >&2
		else
			env -u TOOLCHAIN_IMAGE make --no-print-directory docker-build-toolchain MODULE_ENV_FILE="$modauth" >&2 ||
				die "cannot build $tag on this runner"
		fi
		# The image ID stands in for the digest a push would have minted; a
		# concurrent reap or trim can still drop the tag before the hold, and
		# the next attempt builds it again.
		id=$(docker image inspect -f '{{.Id}}' "$tag" 2>/dev/null) || continue
		[[ $id =~ ^sha256:[0-9a-f]{64}$ ]] || die "$tag has no image ID ('$id')"
		docker rm -f "$hold" >/dev/null 2>&1 || true
		if docker create --pull never --name "$hold" "$id" true >/dev/null; then
			echo "==> $hold holds $tag ($id) until the job's teardown" >&2
			printf '%s\n' "$id"
			return 0
		fi
	done
	die "$tag was removed before $hold could hold it"
}

release() {
	local hold
	for hold in "$@"; do
		[ -z "$hold" ] || docker rm -f "$hold" >/dev/null 2>&1 || true
	done
}

main() {
	[ $# -ge 1 ] || die "usage: ci-registry.sh push|pull|local|release|check-ref ..."
	local cmd=$1
	shift
	case $cmd in
	push) push "$@" ;;
	pull) pull "$@" ;;
	local) local_image "$@" ;;
	release) release "$@" ;;
	check-ref)
		local r
		for r in "$@"; do check_ref "$r"; done
		;;
	*) die "unknown command '$cmd'" ;;
	esac
}

main "$@"
