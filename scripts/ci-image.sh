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
#   ci-image.sh load <dir> <image> <id>            loads <dir>/image.tar, checks it is <id>, tags <image>
#   ci-image.sh push <local-image> <name> <id>     prints ghcr.io/<owner>/<name>@sha256:<hex>
#   ci-image.sh untag <image>...                   removes save's per-run tags
#   ci-image.sh prune [hours]                      self-hosted retention of the CI images
#   ci-image.sh check-ref <ref>...                 validates digest references
#
# save resolves the content-hash tag to its image ID once and archives that
# ID under a per-run tag (<image>-run-<run id>-<attempt>) created from it, so
# another run that moves the shared tag on a shared daemon between the
# resolve and the save cannot change what is archived; it prints the ID only
# after checking the per-run tag still names it. load fails unless the
# archive's per-run tag names the ID docker-build recorded, so a truncated or
# substituted artifact is refused rather than run.
#
# push logs in to GHCR with CI_REGISTRY_USER and CI_REGISTRY_TOKEN, which the
# workflow step supplies from the run's actor and GITHUB_TOKEN (packages:
# write). The login lives in a private DOCKER_CONFIG deleted on exit, and no
# credential value is ever printed: an empty name fails naming the variable.
# push takes the ID save recorded, never the shared tag: it pushes a per-run
# tag, <content-hash>-main-<run id>-<attempt>, created from that ID, and checks
# the pushed tag still names it before printing the digest.
# A refused push is tried again, up to CI_REGISTRY_ATTEMPTS times in all
# (default 3), waiting CI_REGISTRY_BACKOFF seconds (default 10) times the
# number of the failed attempt, so one registry 5xx on one blob upload does
# not fail the job; a push refused every time does. The login is not retried.
#
# prune runs in the always() teardown of every self-hosted job that builds or
# loads a CI image (a pool daemon outlives the job): see prune below.
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

# run_suffix prints this run's own tag suffix, <run id>-<run attempt>: no
# other run on a shared daemon writes a tag that carries it.
run_suffix() {
	local s=${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}
	[[ $s =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,63}$ ]] || die "GITHUB_RUN_ID/GITHUB_RUN_ATTEMPT do not form a tag suffix"
	printf '%s\n' "$s"
}

# run_tag prints the per-run tag of the content-hash tag $1.
run_tag() { printf '%s-run-%s\n' "$1" "$(run_suffix)"; }

# save resolves the content-hash tag to its image ID once, then archives that
# ID and nothing else: the tag is shared by every run with the same build
# inputs, so on a shared daemon another run can move it to other bytes at any
# moment. The archive carries a per-run tag created from the ID; the ID behind
# it is checked again after the save, and only then is the ID printed.
pin() {
	[ $# -eq 2 ] || die "usage: pin <image> <iidfile>"
	local image=$1 iidfile=$2 id rtag got
	check_image "$image"
	# The ID is the one the build itself wrote (docker build --iidfile), never
	# a lookup of the shared tag: another job on this daemon can retag that
	# between the build and this pin.
	[ -f "$iidfile" ] || die "$iidfile does not exist: pin $image with the ID file its build wrote"
	id=$(tr -d '[:space:]' < "$iidfile")
	[[ $id =~ $id_re ]] || die "$iidfile holds '$id', not an image ID"
	rtag=$(run_tag "$image")
	docker tag "$id" "$rtag" || die "cannot tag $id as $rtag"
	got=$(image_id "$rtag")
	[ "$got" = "$id" ] || die "$rtag names $got after the tag, not $id, the image $image's build wrote"
	echo "==> pinned $image ($id) as $rtag" >&2
	printf '%s\n' "$id"
}

save() {
	[ $# -eq 2 ] || die "usage: save <image> <dir>"
	local image=$1 dir=$2 id rtag got
	check_image "$image"
	rtag=$(run_tag "$image")
	# The ID comes from the per-run tag pin set at the build, never from the
	# shared tag, which another run on the daemon can move after the build.
	docker image inspect "$rtag" >/dev/null 2>&1 || die "$rtag does not exist: pin $image right after its build"
	id=$(image_id "$rtag")
	mkdir -p "$dir"
	docker save -o "$dir/image.tar" "$rtag" || die "cannot save $rtag"
	got=$(image_id "$rtag")
	[ "$got" = "$id" ] || die "$rtag names $got after the save, not $id, the image $image was pinned to: the archive is refused"
	printf '%s\n' "$id" >"$dir/image.id"
	echo "==> saved $image ($id) as $rtag, $(du -h "$dir/image.tar" | cut -f1)" >&2
	printf '%s\n' "$id"
}

# untag removes the per-run tags save created, at the producer's teardown.
untag() {
	local image
	for image in "$@"; do
		[ -n "$image" ] || continue
		check_image "$image"
		docker rmi "$(run_tag "$image")" >/dev/null 2>&1 || true
	done
}

load() {
	[ $# -eq 3 ] || die "usage: load <dir> <image> <id>"
	local dir=$1 image=$2 want=$3 got out loaded="" l
	check_image "$image"
	[[ $want =~ $id_re ]] || die "'$want' is not an image ID"
	[ -s "$dir/image.tar" ] || die "$dir/image.tar is missing or empty: the docker-build artifact did not download"
	out=$(docker load -i "$dir/image.tar") || die "cannot load $dir/image.tar"
	printf '%s\n' "$out" >&2
	# The archive carries the producer's per-run tag, which no other run
	# writes, so the ID it names is the archive's own.
	while IFS= read -r l; do
		case $l in "Loaded image: $image-run-"*) loaded=${l#Loaded image: } ;; esac
	done <<<"$out"
	[ -n "$loaded" ] || die "$dir/image.tar carries no per-run tag of $image"
	got=$(image_id "$loaded")
	[ "$got" = "$want" ] || die "$image loaded as $got, not $want, the image docker-build built"
	docker tag "$want" "$image" || die "cannot tag $want as $image"
	rm -f "$dir/image.tar"
	echo "==> $image is $want, the image docker-build built" >&2
	# The verified ID, never the tag, is what the stages run: the tag is
	# shared by every commit with the same build inputs (and two builds of
	# them differ, the base stage runs apk upgrade), so on a shared daemon
	# another job can load different bytes under it after this check. The ID
	# is recorded in the job's own directory, where scripts/in-toolchain-image.sh
	# swaps it in for the tag, and printed for a caller that runs it directly.
	record_id "$image" "$want"
	echo "$want"
}

# The job's record of verified image IDs, one file per tag: CI_IMAGE_IDS, else
# RUNNER_TEMP/ci-image-ids (a runner's own, emptied at each job's start). No
# directory (a local run) records nothing. scripts/in-toolchain-image.sh reads it.
ids_dir() { echo "${CI_IMAGE_IDS:-${RUNNER_TEMP:+$RUNNER_TEMP/ci-image-ids}}"; }

record_id() {
	local dir key
	dir=$(ids_dir)
	[ -n "$dir" ] || return 0
	key=${1//[^A-Za-z0-9._-]/_}
	mkdir -p "$dir" || die "cannot create $dir"
	printf '%s\n' "$2" > "$dir/$key.tmp" || die "cannot record $1's ID in $dir"
	mv -f "$dir/$key.tmp" "$dir/$key" || die "cannot record $1's ID in $dir"
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
	[ $# -eq 3 ] || die "usage: push <local-image> <name> <id>"
	local image=$1 name=$2 id=$3 owner got
	check_image "$image"
	[[ $id =~ $id_re ]] || die "'$id' is not an image ID"
	[[ $name =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "image name '$name' is not a registry repository name"
	owner=$(registry_owner)
	local repo=$registry/$owner/$name
	local target=$repo:${image##*:}-main-$(run_suffix)
	local attempts=${CI_REGISTRY_ATTEMPTS:-3} backoff=${CI_REGISTRY_BACKOFF:-10} n=1
	[[ $attempts =~ ^[1-9][0-9]{0,2}$ ]] || die "CI_REGISTRY_ATTEMPTS must be a positive integer (at most 999), the number of push attempts"
	[[ $backoff =~ ^(0|[1-9][0-9]{0,3})$ ]] || die "CI_REGISTRY_BACKOFF must be a non-negative integer of seconds (at most 9999)"
	login "$owner"
	# The pushed tag is created from the ID save recorded and is this run's
	# own: the shared content-hash tag is never pushed, since another run
	# can move it between the save and the push.
	docker tag "$id" "$target" || die "cannot tag $id as $target"
	# Only the push is retried: a refused login is a credential fault.
	until docker push "$target" >&2; do
		[ "$n" -lt "$attempts" ] || die "GHCR refused the push of $target after $attempts attempts"
		echo "ci-image: push of $target failed (attempt $n of $attempts); retrying in $((n * backoff))s" >&2
		sleep $((n * backoff))
		n=$((n + 1))
	done
	got=$(image_id "$target")
	[ "$got" = "$id" ] || die "$target names $got after the push, not $id, the image docker-build saved"
	local digest="" d
	while IFS= read -r d; do
		case $d in "$repo@"*) digest=$d ;; esac
	done < <(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$target")
	[ -n "$digest" ] || die "the push of $target reported no digest"
	check_ref "$digest"
	docker rmi "$target" >/dev/null 2>&1 || true
	printf '%s\n' "$digest"
}

# ci_ref reports whether the reference $1 is one of the CI images: a local
# qrdb-toolchain or qrdb-ci-it tag (content-hash or per-run), or a pushed
# go-qrdb-toolchain or go-qrdb-ci-it tag.
ci_ref() {
	case ${1%:*} in
	qrdb-toolchain | qrdb-ci-it | */go-qrdb-toolchain | */go-qrdb-ci-it) return 0 ;;
	esac
	return 1
}

# epoch prints the seconds since the epoch of the RFC 3339 time $1, 0 when
# docker reports none (the zero time) or it does not parse.
epoch() {
	local t
	t=$(date -d "$1" +%s 2>/dev/null) || t=0
	[ "$t" -gt 0 ] 2>/dev/null || t=0
	printf '%s\n' "$t"
}

# prune is the self-hosted retention of the CI images. A pool daemon outlives
# every job, and each commit's integration image has its own tag, so without
# it the images accumulate. It removes each CI image tag whose image was
# neither built nor tagged (a build, a load or a save tags it) in the last
# <hours> (default 24), skipping every image a running container uses; the
# daemon deletes an image with its last tag, and refuses one a stopped
# container holds, which is left. No other image is touched.
prune() {
	[ $# -le 1 ] || die "usage: prune [hours]"
	local hours=${1:-24} now cutoff running="" c id ref t created tagged newest
	[[ $hours =~ ^[1-9][0-9]{0,3}$ ]] || die "prune bound '$hours' is not a whole number of hours (1-9999)"
	now=$(date +%s)
	cutoff=$((now - hours * 3600))
	while IFS= read -r c; do
		[ -n "$c" ] || continue
		running+=" $(docker inspect -f '{{.Image}}' "$c" 2>/dev/null || true)"
	done < <(docker ps -q --no-trunc)
	while read -r id ref; do
		case $ref in *:'<none>') continue ;; esac
		ci_ref "$ref" || continue
		case " $running " in *" $id "*)
			echo "==> prune: kept $ref, a running container uses it" >&2
			continue ;;
		esac
		t=$(docker image inspect -f '{{.Created}} {{.Metadata.LastTagTime}}' "$id" 2>/dev/null) || continue
		created=$(epoch "${t%% *}")
		tagged=$(epoch "${t#* }")
		newest=$((created > tagged ? created : tagged))
		[ "$newest" -lt "$cutoff" ] || continue
		if docker rmi "$ref" >/dev/null 2>&1; then
			echo "==> prune: removed $ref ($id)" >&2
		else
			echo "==> prune: kept $ref, the daemon refused its removal" >&2
		fi
	done < <(docker image ls --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}')
}

main() {
	[ $# -ge 1 ] || die "usage: ci-image.sh pin|save|load|push|untag|prune|check-ref ..."
	local cmd=$1
	shift
	case $cmd in
	pin) pin "$@" ;;
	save) save "$@" ;;
	load) load "$@" ;;
	push) push "$@" ;;
	untag) untag "$@" ;;
	prune) prune "$@" ;;
	check-ref)
		[ $# -ge 1 ] || die "usage: check-ref <ref>..."
		local r
		for r in "$@"; do check_ref "$r"; done
		;;
	*) die "unknown command '$cmd'" ;;
	esac
}

main "$@"
