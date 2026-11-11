#!/usr/bin/env bash
# ci-image.sh hands the CI images from the docker-build job to every job that
# runs a stage in them, on GitHub-hosted runners (docs/release/engineering.md,
# "The CI image").
#
# A hosted runner is an ephemeral VM, exclusive to one job: nothing a job
# builds survives into another job, and no image is on its daemon at the
# start. So docker-build builds each image once, at the commit under test,
# saves it to a tar file and uploads it as a workflow artifact, and every
# consumer downloads that artifact and loads it. A consumer never rebuilds:
# the integration stage upgrades its Alpine packages at build time, so a
# rebuild of the same commit can differ, and the stages must run the image
# docker-build built.
#
# The artifact path serves every event the same way, a pull request from a
# fork included: an artifact needs no registry credential, and a fork's run
# holds a read-only token that cannot write a package. On a push to main,
# docker-build ALSO pushes both images to GHCR by digest with the run's own
# GITHUB_TOKEN, which publishes main's toolchain image for developers
# (`make docker-run`) and for the release workflow; consumers inside the run
# still take the artifact, so one code path is exercised on every event.
#
#   ci-image.sh save <image> <dir>                 writes <dir>/image.tar and <dir>/image.id
#   ci-image.sh load <dir> <image> <id>            loads <dir>/image.tar, checks <image> is <id>
#   ci-image.sh push <local-image> <name>          prints ghcr.io/<owner>/<name>@sha256:<hex>
#   ci-image.sh check-ref <ref>...                 validates digest references
#
# save prints the image ID, the content address of the image's config; load
# fails unless the tag it loaded names that same ID, so a truncated or
# substituted artifact is refused rather than run.
#
# push logs in to GHCR with CI_REGISTRY_USER and CI_REGISTRY_TOKEN, which the
# workflow step supplies from the run's actor and GITHUB_TOKEN (packages:
# write). The login lives in a private DOCKER_CONFIG deleted on exit, and no
# credential value is ever printed: an empty name fails naming the variable.
# A pushed image is tagged <content-hash>-main, one tag per content hash.
# A refused push is tried again, up to CI_REGISTRY_ATTEMPTS times in all
# (default 3), waiting CI_REGISTRY_BACKOFF seconds (default 10) times the
# number of the failed attempt, so one registry 5xx on one blob upload does
# not fail the job; a push refused every time does. The login is not retried.
set -euo pipefail

readonly registry=ghcr.io
readonly ref_re='^[^@ ]+@sha256:[0-9a-f]{64}$'
readonly id_re='^sha256:[0-9a-f]{64}$'
readonly image_re='^qrdb-(toolchain|ci-it):[0-9a-f]{16}$'

die() {
	echo "::error::ci-image: $*" >&2
	exit 1
}

# check_image refuses $1 unless it is a content-hash tag of the toolchain or
# integration family, the only images the CI hands on.
check_image() {
	[[ $1 =~ $image_re ]] || die "'$1' is not a content-hash tag of qrdb-toolchain or qrdb-ci-it"
}

# image_id prints the ID of the image $1 names on this daemon.
image_id() {
	local id
	id=$(docker image inspect -f '{{.Id}}' "$1" 2>/dev/null) || die "$1 is not on this daemon"
	[[ $id =~ $id_re ]] || die "$1 has no image ID ('$id')"
	printf '%s\n' "$id"
}

save() {
	[ $# -eq 2 ] || die "usage: save <image> <dir>"
	local image=$1 dir=$2 id
	check_image "$image"
	id=$(image_id "$image")
	mkdir -p "$dir"
	docker save -o "$dir/image.tar" "$image" || die "cannot save $image"
	printf '%s\n' "$id" >"$dir/image.id"
	echo "==> saved $image ($id), $(du -h "$dir/image.tar" | cut -f1)" >&2
	printf '%s\n' "$id"
}

load() {
	[ $# -eq 3 ] || die "usage: load <dir> <image> <id>"
	local dir=$1 image=$2 want=$3 got
	check_image "$image"
	[[ $want =~ $id_re ]] || die "'$want' is not an image ID"
	[ -s "$dir/image.tar" ] || die "$dir/image.tar is missing or empty: the docker-build artifact did not download"
	docker load -i "$dir/image.tar" >&2 || die "cannot load $dir/image.tar"
	got=$(image_id "$image")
	[ "$got" = "$want" ] || die "$image loaded as $got, not $want, the image docker-build built"
	rm -f "$dir/image.tar"
	echo "==> $image is $want, the image docker-build built" >&2
}

# registry_owner prints the repository owner, lowercased, the namespace the
# run's token may write. It names the variable, never its value.
registry_owner() {
	local owner=${GITHUB_REPOSITORY_OWNER:-}
	[ -n "$owner" ] || die "GITHUB_REPOSITORY_OWNER is empty: the push names the namespace from it"
	owner=${owner,,}
	[[ $owner =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "GITHUB_REPOSITORY_OWNER does not name a registry owner"
	printf '%s\n' "$owner"
}

# check_ref refuses $1 unless it is a digest reference in GHCR under the
# repository owner.
check_ref() {
	[[ $1 =~ $ref_re ]] || die "'$1' is not a digest reference (<registry>/<owner>/<name>@sha256:<64 hex>)"
	local owner
	owner=$(registry_owner)
	case $1 in
	"$registry/$owner/"*) ;;
	*) die "'$1' is not under $registry/$owner, the namespace the run's token writes" ;;
	esac
}

login() {
	local v
	for v in CI_REGISTRY_USER CI_REGISTRY_TOKEN; do
		[ -n "${!v:-}" ] || die "$v is empty: the run's token is not in the step's environment"
	done
	DOCKER_CONFIG=$(mktemp -d)
	export DOCKER_CONFIG
	trap 'rm -rf "$DOCKER_CONFIG"' EXIT
	printf '%s' "$CI_REGISTRY_TOKEN" | docker login "$registry" -u "$CI_REGISTRY_USER" --password-stdin >/dev/null ||
		die "GHCR refused the login: the run's token cannot write $1's packages (does the job grant packages: write?)"
}

push() {
	[ $# -eq 2 ] || die "usage: push <local-image> <name>"
	local image=$1 name=$2 owner
	check_image "$image"
	[[ $name =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "image name '$name' is not a registry repository name"
	owner=$(registry_owner)
	local repo=$registry/$owner/$name
	local target=$repo:${image##*:}-main
	local attempts=${CI_REGISTRY_ATTEMPTS:-3} backoff=${CI_REGISTRY_BACKOFF:-10} n=1
	[[ $attempts =~ ^[1-9][0-9]{0,2}$ ]] || die "CI_REGISTRY_ATTEMPTS must be a positive integer (at most 999), the number of push attempts"
	[[ $backoff =~ ^(0|[1-9][0-9]{0,3})$ ]] || die "CI_REGISTRY_BACKOFF must be a non-negative integer of seconds (at most 9999)"
	login "$owner"
	docker tag "$image" "$target" || die "cannot tag $image as $target"
	# Only the push is retried: a refused login is a credential fault.
	until docker push "$target" >&2; do
		[ "$n" -lt "$attempts" ] || die "GHCR refused the push of $target after $attempts attempts"
		echo "ci-image: push of $target failed (attempt $n of $attempts); retrying in $((n * backoff))s" >&2
		sleep $((n * backoff))
		n=$((n + 1))
	done
	local digest="" d
	while IFS= read -r d; do
		case $d in "$repo@"*) digest=$d ;; esac
	done < <(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$target")
	[ -n "$digest" ] || die "the push of $target reported no digest"
	check_ref "$digest"
	printf '%s\n' "$digest"
}

main() {
	[ $# -ge 1 ] || die "usage: ci-image.sh save|load|push|check-ref ..."
	local cmd=$1
	shift
	case $cmd in
	save) save "$@" ;;
	load) load "$@" ;;
	push) push "$@" ;;
	check-ref)
		[ $# -ge 1 ] || die "usage: check-ref <ref>..."
		local r
		for r in "$@"; do check_ref "$r"; done
		;;
	*) die "unknown command '$cmd'" ;;
	esac
}

main "$@"
