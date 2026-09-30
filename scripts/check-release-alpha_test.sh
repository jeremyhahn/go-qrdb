#!/bin/sh
#
# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# check-release-alpha_test.sh -- regression tests for the release-alpha target.
#
# release-alpha is around sixty lines of orchestration whose failure mode is a
# PUBLISHED tag. A tag is visible to `go get` the moment it lands and
# release-versioning.md 3 makes it immutable, so a defect here is not a red
# build to fix in the morning: it is a release consumers have already resolved
# and that cannot be retracted. Running the recipe once by hand proves it works
# today and nothing at all about tomorrow. These cases are the durable half, and
# they exist because every defect they pin was really in the v1 recipe -- an
# unsigned owner-identity commit, lightweight tags, `|| true` swallowing a
# failed commit and tagging anyway, and a push that published every local tag.
#
# HERMETIC BY CONSTRUCTION. Each case builds a throwaway repository and a LOCAL
# BARE repository standing in for the `dev` remote, and points AGENT_GIT at a
# fake wrapper with the same CLI shape as the real one. Nothing reaches Gitea,
# the network, the real `dev` remote, or the caller's git identity and signing
# key, so this runs identically on a developer box and on a CI runner with no
# credentials. That is deliberate: a test that can only pass on one host is a
# test that quietly stops running.
#
# THE FAKE WRAPPER RECORDS ITS ARGV, and one case asserts the exact call list.
# Outcome-only assertions cannot tell "signed correctly" from "signed by luck":
# if the recipe passed the wrong repo name, dropped `-s`, or reached for
# `--tags`, the resulting objects can still look plausible. How the signing
# wrapper was invoked is part of the contract, so it is checked directly.
#
# The recipe under test is the REAL Makefile, copied into each fixture. Nothing
# is transcribed or extracted, so these cases cannot drift away from the target
# they cover.
#
# Usage: check-release-alpha_test.sh   (no arguments; needs git, make, ssh-keygen)
#
# Exit status: 0 when every case passes, 1 otherwise.

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "${SCRIPT_DIR}/.." && pwd)
MAKEFILE="${REPO_ROOT}/Makefile"

# FAIL CLOSED on a missing tool, and never degrade. ssh-keygen is what makes the
# signature assertions real; a signing test that "passes" on a host with no
# signer is worse than one that refuses to run, because it reports green for a
# property it never checked. A reviewer's container without ssh-keygen must see
# this message, not a pass.
for TOOL in git make ssh-keygen go; do
	if ! command -v "${TOOL}" >/dev/null 2>&1; then
		echo "REFUSING TO RUN: ${TOOL} is required and was not found."
		echo "  These cases assert that release commits and tags are really signed."
		echo "  Without ${TOOL} that cannot be checked, and a green result here would"
		echo "  mean nothing. Install ${TOOL} and re-run; this target never skips."
		exit 1
	fi
done

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT INT TERM

FAILURES=0
CANARY="${WORK}/canary"
# The literal text of an injected payload. It is built here rather than written
# inline at each call site so that this script's own shell never expands it.
PAYLOAD='$(shell touch '"${CANARY}"')'

ok() { echo "ok    $1"; }

bad() {
	echo "FAIL  $1: $2"
	FAILURES=$((FAILURES + 1))
}

# A real key, so the signature assertions are real. It is generated here and
# thrown away with the work directory; the agent's key is never touched.
ssh-keygen -q -t ed25519 -N '' -C release-alpha-test -f "${WORK}/signing.key"

cat > "${WORK}/agent-git.sh" <<'WRAPPER'
#!/bin/sh
# Fake agent-git.sh. Same CLI shape as the real wrapper --
# agent-git.sh <repo> <git args...> -- with a throwaway identity and signing key
# instead of the agent's, plus a record of every invocation so a test can assert
# HOW it was called and not merely what the objects look like afterwards.
#
# It derives its identity from the repo name it is HANDED, exactly as the real
# wrapper does. That is what makes "the release commit is authored by
# agent-go-qrdb" a real assertion about the argument the recipe passed, rather
# than a restatement of how the fixture was configured.
set -eu

{
	SEP=''
	for ARG in "$@"; do
		printf '%s%s' "${SEP}" "${ARG}"
		SEP='|'
	done
	printf '\n'
} >> "${FAKE_GIT_LOG}"

REPO="$1"
shift

# Injected failures, selected by FAKE_GIT_FAIL. Each models a real way this
# wrapper, or the objects it produces, can be wrong -- including two that fail
# NOTHING and are visible only to the recipe's own assertions.
IDENT="agent-${REPO}"
SIGN_COMMIT=true
SKEW=''
case "${FAKE_GIT_FAIL:-}" in
commit)
	if [ "${1:-}" = commit ]; then
		echo "fake agent-git: refusing to commit" >&2
		exit 1
	fi
	;;
sdktag)
	if [ "${1:-}" = tag ]; then
		for ARG in "$@"; do
			case "${ARG}" in
			sdk/go/*)
				echo "fake agent-git: refusing to tag ${ARG}" >&2
				exit 1
				;;
			esac
		done
	fi
	;;
push)
	if [ "${1:-}" = push ]; then
		echo "fake agent-git: refusing to push" >&2
		exit 1
	fi
	;;
nosign)
	# A wrapper that is not the signing wrapper. Nothing fails; the commit is
	# simply unsigned, which only the recipe's gpgsig assertion can catch.
	SIGN_COMMIT=false
	;;
badidentity)
	# A wrapper handed, or ignoring, the wrong repo name. Again nothing fails.
	IDENT="agent-not-this-repo"
	;;
skewtag)
	# Create the sdk/go leg at the WRONG commit. The tag is annotated, signed
	# and well formed, so every per-tag assertion passes; only the post-tag
	# lockstep audit sees it. That audit used to sit OUTSIDE the cleanup path,
	# which is the escape this mode exists to keep closed.
	if [ "${1:-}" = tag ]; then
		for ARG in "$@"; do
			case "${ARG}" in
			sdk/go/*) SKEW='HEAD~1' ;;
			esac
		done
	fi
	;;
esac

GIT() {
	git \
		-c "user.name=${IDENT}" \
		-c "user.email=${IDENT}@release-alpha-test.invalid" \
		-c gpg.format=ssh \
		-c "user.signingkey=${FAKE_SIGNING_KEY}" \
		-c "commit.gpgsign=${SIGN_COMMIT}" \
		-c tag.gpgsign=true \
		"$@"
}

# Receive-side outcomes. A push that FAILS BEFORE CONTACTING THE REMOTE (the
# `push` mode above) cannot exercise the interesting case: whether the recipe
# trusts the client's exit code about what the remote did. These three do.
# Args here are: push --atomic <remote> HEAD <ref>...
case "${FAKE_GIT_FAIL:-}" in
pushthenfail)
	if [ "${1:-}" = push ]; then
		GIT "$@" >&2 || exit 1
		echo "fake agent-git: the remote applied every ref, then the client failed" >&2
		exit 1
	fi
	;;
pushpartial)
	if [ "${1:-}" = push ]; then
		GIT push "$3" HEAD "$5" >&2 || true
		echo "fake agent-git: published a subset, then failed" >&2
		exit 1
	fi
	;;
tagthenfail)
	# Creates the tag, then reports failure -- the tag-creation twin of
	# pushthenfail. Only reached for the FIRST tag, which is what makes it a
	# test of whether the cleanup set knew about a tag the wrapper made before
	# it errored.
	if [ "${1:-}" = tag ]; then
		GIT "$@" >&2 || exit 1
		echo "fake agent-git: the tag was created, then the client failed" >&2
		exit 1
	fi
	;;
pushunqueryable)
	if [ "${1:-}" = push ]; then
		URL=$(GIT remote get-url "$3")
		mv "${URL}" "${URL}.gone"
		echo "fake agent-git: the push failed and the remote is now unreachable" >&2
		exit 1
	fi
	;;
esac

GIT "$@" ${SKEW}
WRAPPER
chmod +x "${WORK}/agent-git.sh"

# fixture NAME -- a throwaway repository plus a local bare repository standing
# in for `dev`, and the real Makefile and hygiene scripts copied in.
#
# The directory is deliberately NOT named go-qrdb. REPO_NAME must come from the
# Makefile and never from the checkout directory: derived from the directory, a
# worktree or a CI workspace signs and attributes the release as the wrong
# agent, which is the defect that made REPO_NAME a literal in the first place.
#
# VERSION starts at 0.1.0-alpha and CHANGELOG.md already carries a 0.2.0-alpha
# section, which is the state a release is cut from -- notes written first, then
# the version stamped. stray-tag exists because `push --tags` would publish it.
fixture() {
	DIR="${WORK}/$1"
	mkdir -p "${DIR}/scripts"
	git init -q -b main "${DIR}"
	git -C "${DIR}" config user.name "release-alpha-test"
	git -C "${DIR}" config user.email "release-alpha-test@invalid"
	git -C "${DIR}" config commit.gpgsign false
	git -C "${DIR}" config tag.gpgsign false
	cp "${MAKEFILE}" "${DIR}/Makefile"
	cp "${SCRIPT_DIR}/check-tag-lockstep.sh" "${DIR}/scripts/"
	cp "${SCRIPT_DIR}/check-version-reconciled.sh" "${DIR}/scripts/"
	cp "${SCRIPT_DIR}/lockstep-modules.sh" "${DIR}/scripts/"
	# The lockstep set is DISCOVERED from the module tree now, not declared, so
	# a fixture needs a real one. sdk/go alone by default; a case that wants to
	# prove discovery picks up a new module adds one and re-commits.
	mkdir -p "${DIR}/sdk/go"
	printf 'module github.com/jeremyhahn/go-qrdb/sdk/go\n\ngo 1.26\n' > "${DIR}/sdk/go/go.mod"
	# Every discovered module must be DECLARED in lockstep-baseline, so a
	# fixture needs one. sdk/go is baselined at the fixture's own first release.
	printf '%s\n' 'sdk/go v0.1.0-alpha' > "${DIR}/lockstep-baseline"
	printf '0.1.0-alpha\n' > "${DIR}/VERSION"
	{
		echo '# Changelog'
		echo ''
		echo '## [Unreleased]'
		echo ''
		echo '## [0.2.0-alpha] - 2026-01-02'
		echo ''
		echo '### Added'
		echo ''
		echo '- the release under test'
		echo ''
		echo '## [0.1.0-alpha] - 2026-01-01'
		echo ''
		echo '### Added'
		echo ''
		echo '- the previous release'
	} > "${DIR}/CHANGELOG.md"
	git -C "${DIR}" add -A
	git -C "${DIR}" commit -q --no-gpg-sign -m "fixture"
	git -C "${DIR}" tag --no-sign v0.1.0-alpha
	git -C "${DIR}" tag --no-sign sdk/go/v0.1.0-alpha
	git -C "${DIR}" tag --no-sign stray-tag
	git init -q --bare "${DIR}.git"
	git -C "${DIR}" remote add dev "${DIR}.git"
	git -C "${DIR}" push -q dev main
}

# Everything a refusal must leave untouched: the working tree, the commit, the
# VERSION file, every local tag, and every ref on the remote.
state() {
	git -C "$1" status --porcelain
	git -C "$1" rev-parse HEAD
	cat "$1/VERSION"
	git -C "$1" tag | sort
	git ls-remote "$1.git" | sort
}

# run DIR [make variables...] -- invoke the target with a scrubbed environment,
# so no value the caller happens to export can decide the result. FAIL_MODE
# selects the fake wrapper's injected failure and defaults to none.
run() {
	DIR="$1"
	shift
	rm -f "${DIR}.gitlog"
	set +e
	# ENV_PRESET is intentionally unquoted: its values are controlled here and
	# must word-split into separate NAME=VALUE assignments, so a case can put a
	# release parameter into the INHERITED ENVIRONMENT and prove it is ignored.
	# It is applied after the -u list, which is what makes it win over the scrub.
	OUTPUT=$(
		cd "${DIR}" &&
			env -u VERSION -u RELEASE_REMOTE -u REPO_NAME -u AGENT_GIT \
				-u REL_VERSION -u REL_REMOTE -u REL_REPO -u REL_AGENT_GIT \
				-u REL_SUBMODULE_TAGS -u SUBMODULE_TAGS \
				-u MAKEFLAGS -u MAKELEVEL -u MFLAGS \
				GIT_TERMINAL_PROMPT=0 \
				GOPROXY=off GOFLAGS=-mod=mod \
				${ENV_PRESET:-} \
				FAKE_GIT_LOG="${DIR}.gitlog" \
				FAKE_SIGNING_KEY="${WORK}/signing.key" \
				FAKE_GIT_FAIL="${FAIL_MODE:-}" \
				make release-alpha AGENT_GIT="${WORK}/agent-git.sh" "$@" 2>&1
	)
	STATUS=$?
	set -e
	FAIL_MODE=''
	ENV_PRESET=''
}

# refuse NAME DIR EXPECTED_TEXT [make variables...]
#
# The recipe must fail, say why, and leave the repository and the remote
# byte-identical. The state comparison is the point: "refused" and "refused
# after writing VERSION" are different outcomes and only one of them is safe,
# and an exit status alone cannot tell them apart.
refuse() {
	NAME="$1"
	DIR="$2"
	WANT="$3"
	shift 3
	BEFORE=$(state "${DIR}")
	run "${DIR}" "$@"
	AFTER=$(state "${DIR}")
	if [ "${STATUS}" -eq 0 ]; then
		bad "${NAME}" "exit 0, want non-zero"
		return
	fi
	case "${OUTPUT}" in
	*"${WANT}"*) ;;
	*)
		bad "${NAME}" "output does not contain '${WANT}'"
		echo "${OUTPUT}" | sed 's/^/        /'
		return
		;;
	esac
	if [ "${BEFORE}" != "${AFTER}" ]; then
		bad "${NAME}" "the repository or the remote changed before the refusal"
		printf '%s\n' "${BEFORE}" > "${WORK}/before"
		printf '%s\n' "${AFTER}" > "${WORK}/after"
		diff -u "${WORK}/before" "${WORK}/after" | sed 's/^/        /' || true
		return
	fi
	ok "${NAME}"
}

# no_canary NAME -- an injected $(shell ...) payload must arrive as literal text
# and never execute. Checked as a side effect on the filesystem rather than by
# reading the diagnostic, because a payload that ran and then failed a guard
# still produces a plausible-looking refusal.
no_canary() {
	if [ -e "${CANARY}" ]; then
		bad "$1" "the injected payload EXECUTED"
		rm -f "${CANARY}"
	else
		ok "$1"
	fi
}

# assert_clean NAME DIR -- the contract for EVERY failure past the VERSION
# write: the repository is exactly as it was. No release tag, no release commit,
# no staged or modified VERSION, nothing published. A partial release is worse
# than a failed one -- it blocks the retry on the recipe's own debris.
assert_clean() {
	NAME="$1"
	DIR="$2"
	LEFT=$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha' | tr '\n' ' ')
	if [ -n "${LEFT}" ]; then
		bad "${NAME}" "a tag survived: ${LEFT}"
	elif [ "$(git -C "${DIR}" log -1 --format=%s)" = "chore(release): v0.2.0-alpha" ]; then
		bad "${NAME}" "the release commit survived"
	elif [ "$(cat "${DIR}/VERSION")" != "0.1.0-alpha" ]; then
		bad "${NAME}" "VERSION was not restored: $(cat "${DIR}/VERSION")"
	elif [ -n "$(git -C "${DIR}" status --porcelain)" ]; then
		bad "${NAME}" "the working tree was left dirty: $(git -C "${DIR}" status --porcelain | tr '\n' ' ')"
	elif [ -n "$(git ls-remote --tags "${DIR}.git")" ]; then
		bad "${NAME}" "something was published"
	else
		ok "${NAME}"
	fi
}

# assert_retry NAME DIR -- and the recovery must be nothing at all: with the
# cause removed, running the target again works, with no manual reset and no
# tag deletion. An earlier revision left the debris and printed instructions
# instead; the instructions were wrong, which is how this pair of assertions
# came to exist.
assert_retry() {
	NAME="$1"
	DIR="$2"
	run "${DIR}" VERSION=v0.2.0-alpha
	if [ "${STATUS}" -ne 0 ]; then
		bad "${NAME}" "the retry failed"
		echo "${OUTPUT}" | sed 's/^/        /'
	elif [ "$(git ls-remote "${DIR}.git" | awk '{print $2}' | grep -c -v '\^{}$')" -ne 3 ]; then
		bad "${NAME}" "the retry did not publish main plus exactly two tags"
	else
		ok "${NAME}"
	fi
}

# ---------------------------------------------------------------------------
# Refusals that must happen before anything is written
# ---------------------------------------------------------------------------

fixture no-version
refuse "no VERSION argument is refused" "${WORK}/no-version" \
	"state the version explicitly"

fixture malformed
refuse "a two-field version is refused" "${WORK}/malformed" \
	"is not of the form vX.Y.Z-alpha" VERSION=v0.2-alpha

fixture leading-zero
refuse "a leading-zero version is refused" "${WORK}/leading-zero" \
	"is not of the form vX.Y.Z-alpha" VERSION=v0.02.0-alpha

fixture no-alpha
refuse "a version without -alpha is refused" "${WORK}/no-alpha" \
	"is not of the form vX.Y.Z-alpha" VERSION=v1.0.0

fixture not-main
git -C "${WORK}/not-main" checkout -q -b topic
refuse "a release from a branch other than main is refused" "${WORK}/not-main" \
	"releases are cut from main" VERSION=v0.2.0-alpha

fixture bad-agent-git
refuse "an AGENT_GIT that does not resolve is refused" "${WORK}/bad-agent-git" \
	"does not resolve to an executable" \
	VERSION=v0.2.0-alpha AGENT_GIT=no-such-agent-git.sh

fixture no-remote
refuse "a RELEASE_REMOTE that does not exist is refused" "${WORK}/no-remote" \
	"no git remote named nosuch" \
	VERSION=v0.2.0-alpha RELEASE_REMOTE=nosuch

# A release tag is permanent and public the instant it lands, and AGENTS.md
# requires explicit approval before any push to github.com. The refusal is
# asserted for both URL spellings, and it deliberately comes BEFORE the
# duplicate-tag check -- so this case never touches the network, which is also
# what keeps it hermetic.
fixture github-ssh-remote
git -C "${WORK}/github-ssh-remote" remote add github "git@github.com:jeremyhahn/go-qrdb.git"
refuse "an ssh github.com remote is refused" "${WORK}/github-ssh-remote" \
	"points at github.com" VERSION=v0.2.0-alpha RELEASE_REMOTE=github

fixture github-https-remote
git -C "${WORK}/github-https-remote" remote add github "https://github.com/jeremyhahn/go-qrdb.git"
refuse "an https github.com remote is refused" "${WORK}/github-https-remote" \
	"points at github.com" VERSION=v0.2.0-alpha RELEASE_REMOTE=github

# DNS host names are case-insensitive, so this is the same host. A
# case-sensitive substring match waved it through.
fixture github-mixedcase-remote
git -C "${WORK}/github-mixedcase-remote" remote add github "git@GitHub.com:jeremyhahn/go-qrdb.git"
refuse "a mixed-case github.com remote is refused" "${WORK}/github-mixedcase-remote" \
	"points at github.com" VERSION=v0.2.0-alpha RELEASE_REMOTE=github

# The bypass: an innocuous fetch URL and a github.com PUSHURL. `git remote
# get-url` reads the fetch URL; `git push` uses the pushurl. A fetch-only check
# passed this and published to GitHub anyway.
fixture github-pushurl-remote
git -C "${WORK}/github-pushurl-remote" remote set-url --push dev "git@github.com:jeremyhahn/go-qrdb.git"
refuse "a remote whose PUSHURL is github.com is refused" "${WORK}/github-pushurl-remote" \
	"points at github.com" VERSION=v0.2.0-alpha

# Hostname case, on the PUSHURL specifically. The lowercase pushurl form is
# covered above; this is the combination -- a pushurl that is both the thing git
# actually publishes to and spelled in mixed case.
fixture github-mixedcase-pushurl
git -C "${WORK}/github-mixedcase-pushurl" remote set-url --push dev "git@GitHub.COM:jeremyhahn/go-qrdb.git"
refuse "a mixed-case github.com PUSHURL is refused" "${WORK}/github-mixedcase-pushurl" \
	"points at github.com" VERSION=v0.2.0-alpha

# THE CI FAILURE. `git remote get-url` applies url.<base>.insteadOf rewriting,
# and this fleet rewrites https://github.com/jeremyhahn/ to the internal Gitea
# host so Go can resolve private modules. Under that config the guard saw a
# Gitea URL for a remote configured as https://github.com/... and ACCEPTED it.
# It passed locally and failed on CI for exactly that reason, and the ssh form
# kept refusing because an https-scoped rewrite does not touch it -- which is
# the pass/fail split that gave the cause away.
fixture github-rewritten-away
git -C "${WORK}/github-rewritten-away" remote add github "https://github.com/jeremyhahn/go-qrdb.git"
git -C "${WORK}/github-rewritten-away" config \
	"url.http://x:tok@gitea.invalid/jeremyhahn/.insteadOf" "https://github.com/jeremyhahn/"
refuse "a github.com remote rewritten away by insteadOf is still refused" \
	"${WORK}/github-rewritten-away" "points at github.com" \
	VERSION=v0.2.0-alpha RELEASE_REMOTE=github

# And the other direction, which is why BOTH views are checked rather than just
# the configured one: a remote configured at a benign host that insteadOf
# rewrites INTO github.com would really publish there.
fixture github-rewritten-into
git -C "${WORK}/github-rewritten-into" remote add elsewhere "https://internal.invalid/jeremyhahn/go-qrdb.git"
git -C "${WORK}/github-rewritten-into" config \
	"url.https://github.com/jeremyhahn/.insteadOf" "https://internal.invalid/jeremyhahn/"
refuse "a benign remote rewritten INTO github.com is refused" \
	"${WORK}/github-rewritten-into" "points at github.com" \
	VERSION=v0.2.0-alpha RELEASE_REMOTE=elsewhere

# A remote that fetches from one place and pushes to another. Neither URL is
# GitHub, so the publication guard has nothing to say -- but when a push cannot
# be confirmed the recipe asks the remote what it holds, and that query reads
# the FETCH url. On a split remote it would interrogate one server about what
# it wrote to another and could report "nothing was published" about a live
# release. Found by pairing each guard with the operation it guards, not by a
# reviewer.
fixture split-remote
git init -q --bare "${WORK}/split-elsewhere.git"
git -C "${WORK}/split-remote" remote set-url --push dev "${WORK}/split-elsewhere.git"
refuse "a remote that fetches and pushes to different places is refused" \
	"${WORK}/split-remote" "fetches from" VERSION=v0.2.0-alpha

# An unreachable remote is not an absent tag. The remote resolves, so the
# existence guard passes, but nothing can be read from it -- and the recipe must
# refuse rather than read silence as "no such tag" and half-publish a release.
#
# The fetch is now the first thing that talks to the remote, so the fetch is
# what reports this. The `ls-remote` rc-128 branch further down stays as the
# backstop: it answers the narrower question of a remote that can be fetched
# from but not queried, and it is the guard that must never fail OPEN.
fixture unreachable-remote
git -C "${WORK}/unreachable-remote" remote set-url dev "${WORK}/gone.git"
refuse "an unreachable remote is refused, not read as an absent tag" \
	"${WORK}/unreachable-remote" "could not fetch tags from dev" \
	VERSION=v0.2.0-alpha

# Dirty AND stale: a tag exists on the remote that this clone has never seen.
# The refusal has to come before the fetch, or a refused invocation has still
# created local refs. Asserting only "exit non-zero" would not notice; the
# assertion that matters is that the remote-only tag did NOT arrive.
NAME="a dirty working tree is refused before any ref is fetched"
fixture dirty
DIR="${WORK}/dirty"
git -C "${DIR}" tag --no-sign v0.1.9-alpha
git -C "${DIR}" tag --no-sign sdk/go/v0.1.9-alpha
git -C "${DIR}" push -q dev refs/tags/v0.1.9-alpha refs/tags/sdk/go/v0.1.9-alpha
git -C "${DIR}" tag -d v0.1.9-alpha sdk/go/v0.1.9-alpha >/dev/null
echo scratch >> "${DIR}/CHANGELOG.md"
BEFORE_DIRTY=$(state "${DIR}")
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0, want non-zero"
elif git -C "${DIR}" rev-parse -q --verify refs/tags/v0.1.9-alpha >/dev/null; then
	bad "${NAME}" "the remote-only tag was fetched before the refusal"
elif [ "$(state "${DIR}")" != "${BEFORE_DIRTY}" ]; then
	bad "${NAME}" "state changed before the refusal"
else
	case "${OUTPUT}" in
	*"working tree not clean"*) ok "${NAME}" ;;
	*) bad "${NAME}" "wrong diagnostic" ;;
	esac
fi

# Both legs of the pair, so the lockstep prerequisite passes and the guard under
# test is the one that fires. A root tag alone would be caught earlier, by
# check-tag-lockstep, and the case would pass for the wrong reason.
fixture dup-local
git -C "${WORK}/dup-local" tag --no-sign v0.2.0-alpha
git -C "${WORK}/dup-local" tag --no-sign sdk/go/v0.2.0-alpha
refuse "a tag that already exists locally is refused" "${WORK}/dup-local" \
	"already exists locally" VERSION=v0.2.0-alpha

# The same tag present ONLY on the remote. A local-only check would miss it and
# fail the push after VERSION was already committed.
# Since the recipe fetches before auditing, this is now caught by the LOCAL
# duplicate check -- the fetch brought the published tag into the clone.
# Asserted explicitly rather than with refuse(), because that fetch legitimately
# changes the local tag list; the invariant is that no RELEASE state was
# created. The remote duplicate check further down remains the backstop for a
# tag that appears between the fetch and the push. Both legs are published so
# the lockstep audit has nothing else to complain about.
NAME="a tag that exists only on the remote is refused"
fixture dup-remote
DIR="${WORK}/dup-remote"
git -C "${DIR}" tag --no-sign v0.2.0-alpha
git -C "${DIR}" tag --no-sign sdk/go/v0.2.0-alpha
git -C "${DIR}" push -q dev refs/tags/v0.2.0-alpha refs/tags/sdk/go/v0.2.0-alpha
git -C "${DIR}" tag -d v0.2.0-alpha sdk/go/v0.2.0-alpha >/dev/null
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0; a published version was re-cut"
elif [ "$(git -C "${DIR}" log -1 --format=%s)" = "chore(release): v0.2.0-alpha" ]; then
	bad "${NAME}" "the release commit was created"
elif [ -n "$(git -C "${DIR}" status --porcelain)" ]; then
	bad "${NAME}" "the working tree was left dirty"
else
	case "${OUTPUT}" in
	*"already exists"*) ok "${NAME}" ;;
	*) bad "${NAME}" "wrong diagnostic" ;;
	esac
fi

fixture no-changelog
refuse "a version with no CHANGELOG section is refused" "${WORK}/no-changelog" \
	"does not carry 0.3.0-alpha as its newest released section" \
	VERSION=v0.3.0-alpha

# A skew that exists ONLY ON THE REMOTE. The local clone is clean, so the
# prerequisite audit passes; only fetching first reveals it. Asserted
# explicitly rather than with refuse(), because the fetch legitimately brings
# the remote's tags into the clone -- that is a read-refresh, not a release
# mutation, and the invariant that matters is that no RELEASE state was created.
NAME="a skew that exists only on the remote stops the release"
fixture remote-only-skew
DIR="${WORK}/remote-only-skew"
git -C "${DIR}" tag --no-sign v0.1.5-alpha
git -C "${DIR}" push -q dev refs/tags/v0.1.5-alpha
git -C "${DIR}" tag -d v0.1.5-alpha >/dev/null
if ! "${DIR}/scripts/check-tag-lockstep.sh" "${DIR}" >/dev/null 2>&1; then
	bad "${NAME}" "the fixture is wrong: the local audit already fails, so the case proves nothing"
else
	run "${DIR}" VERSION=v0.2.0-alpha
	if [ "${STATUS}" -eq 0 ]; then
		bad "${NAME}" "exit 0; the release was cut on top of a published skew"
	elif [ -n "$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha')" ]; then
		bad "${NAME}" "a release tag was created"
	elif [ "$(git -C "${DIR}" log -1 --format=%s)" = "chore(release): v0.2.0-alpha" ]; then
		bad "${NAME}" "the release commit was created"
	elif [ -n "$(git -C "${DIR}" status --porcelain)" ]; then
		bad "${NAME}" "the working tree was left dirty"
	else
		case "${OUTPUT}" in
		*"MISSING  sdk/go/v0.1.5-alpha"*) ok "${NAME}" ;;
		*) bad "${NAME}" "the refusal does not name the remote-only skew" ;;
		esac
	fi
fi

# check-tag-lockstep is a prerequisite, so a skew that is already published
# stops the release before it starts rather than being inherited by it.
fixture pre-existing-skew
git -C "${WORK}/pre-existing-skew" tag --no-sign v0.1.5-alpha
refuse "a pre-existing lockstep skew stops the release" \
	"${WORK}/pre-existing-skew" "MISSING  sdk/go/v0.1.5-alpha" \
	VERSION=v0.2.0-alpha

# ---------------------------------------------------------------------------
# Command injection. Each payload is a $(shell ...) that would touch a canary
# file. A make variable set on the command line is expanded when the recipe's
# environment is built, even in a makefile that never mentions it, so these must
# be run as REAL recipe invocations -- `make -n` builds no environment and makes
# an injectable makefile look clean.
# ---------------------------------------------------------------------------

fixture inject-version
refuse "an injected VERSION is refused" "${WORK}/inject-version" \
	"is not of the form vX.Y.Z-alpha" "VERSION=v0.2.0-alpha${PAYLOAD}"
no_canary "an injected VERSION does not execute"

fixture inject-remote
refuse "an injected RELEASE_REMOTE is refused" "${WORK}/inject-remote" \
	"no git remote named" VERSION=v0.2.0-alpha "RELEASE_REMOTE=${PAYLOAD}dev"
no_canary "an injected RELEASE_REMOTE does not execute"

fixture inject-repo
refuse "an injected REPO_NAME is refused" "${WORK}/inject-repo" \
	"is not a plain repo name" VERSION=v0.2.0-alpha "REPO_NAME=${PAYLOAD}go-qrdb"
no_canary "an injected REPO_NAME does not execute"

fixture inject-agent-git
refuse "an injected AGENT_GIT is refused" "${WORK}/inject-agent-git" \
	"does not resolve to an executable" VERSION=v0.2.0-alpha \
	"AGENT_GIT=${PAYLOAD}/bin/true"
no_canary "an injected AGENT_GIT does not execute"

# SUBMODULE_TAGS is declared with `override`, so a command line cannot reach it
# at all. The payload must still not execute -- an override does not stop make
# from expanding a command-line value when it builds the recipe environment.
NAME="an injected SUBMODULE_TAGS is ignored"
fixture inject-submodules
run "${WORK}/inject-submodules" VERSION=v0.2.0-alpha "SUBMODULE_TAGS=${PAYLOAD}sdk/go"
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif [ "$(git ls-remote --tags "${WORK}/inject-submodules.git" | grep -c -v '\^{}$')" -ne 2 ]; then
	bad "${NAME}" "the override changed which tags were published"
else
	ok "${NAME}"
fi
no_canary "an injected SUBMODULE_TAGS does not execute"

# The internal aliases are just as settable from a command line as the public
# names. `override` is what keeps the sanitised value winning; without it an
# attacker would simply set REL_* and skip the sanitiser entirely.
fixture inject-rel-version
refuse "an injected REL_VERSION is overridden and refused" \
	"${WORK}/inject-rel-version" "state the version explicitly" \
	"REL_VERSION=${PAYLOAD}v0.2.0-alpha"
no_canary "an injected REL_VERSION does not execute"

fixture inject-rel-remote
refuse "an injected REL_REMOTE is overridden and refused" \
	"${WORK}/inject-rel-remote" "state the version explicitly" \
	"REL_REMOTE=${PAYLOAD}dev"
no_canary "an injected REL_REMOTE does not execute"

# ---------------------------------------------------------------------------
# Release parameters inherited from the environment
#
# An environment variable IS a defined make variable, so a `?=` default does not
# override it and `unexport` does not help -- unexport only keeps a name out of
# child environments. These two cases are why the defaults are `:=`: with `?=`
# any shell or CI job that exports RELEASE_REMOTE silently redirects where the
# release is PUSHED, and one that exports a well-formed VERSION lets a bare
# `make release-alpha` cut a release nobody named.
# ---------------------------------------------------------------------------

fixture inherited-version
ENV_PRESET='VERSION=v0.2.0-alpha'
refuse "an inherited VERSION does not satisfy the explicit-version guard" \
	"${WORK}/inherited-version" "state the version explicitly"

# `make -e` makes the environment beat the Makefile, and it arrives through an
# inherited MAKEFLAGS without anybody typing it. Both cases below would
# otherwise defeat the `:=` defaults entirely.
fixture makeflags-e-version
ENV_PRESET='MAKEFLAGS=-e VERSION=v9.9.9-alpha'
refuse "an environment VERSION under MAKEFLAGS=-e is refused" \
	"${WORK}/makeflags-e-version" "coming from the ENVIRONMENT"

fixture makeflags-e-remote
git -C "${WORK}/makeflags-e-remote" remote add elsewhere "${WORK}/e2.git"
git init -q --bare "${WORK}/e2.git"
ENV_PRESET='MAKEFLAGS=-e RELEASE_REMOTE=elsewhere'
refuse "an environment RELEASE_REMOTE under MAKEFLAGS=-e is refused" \
	"${WORK}/makeflags-e-remote" "coming from the ENVIRONMENT" VERSION=v0.2.0-alpha

NAME="an inherited RELEASE_REMOTE does not redirect the push"
fixture inherited-remote
DIR="${WORK}/inherited-remote"
git -C "${DIR}" remote add elsewhere "${WORK}/elsewhere.git"
git init -q --bare "${WORK}/elsewhere.git"
ENV_PRESET='RELEASE_REMOTE=elsewhere'
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif [ -n "$(git ls-remote --tags "${WORK}/elsewhere.git")" ]; then
	bad "${NAME}" "the release was published to the inherited remote"
elif [ "$(git ls-remote --tags "${DIR}.git" | grep -c -v '\^{}$')" -ne 2 ]; then
	bad "${NAME}" "the release was not published to dev"
else
	ok "${NAME}"
fi

# ---------------------------------------------------------------------------
# A tag name is untrusted input
#
# release-lockstep.yml passes github.ref_name into
# `make check-version-reconciled RELEASE_TAG=...`, and this branch widened that
# workflow's trigger to sdk/**/v*, routing arbitrary deeper tag names there. Git
# accepts far more in a tag name than a shell treats as inert.
#
# These assert on the SIDE EFFECT's absence, not on the exit code. The reported
# reproduction exited 0 *while* running the injection, so exit status is exactly
# the signal that fails here.
# ---------------------------------------------------------------------------

# reconcile DIR TAG -- run the reconciliation target in a fixture
reconcile() {
	RDIR="$1"
	set +e
	OUTPUT=$(
		cd "${RDIR}" &&
			env -u RELEASE_TAG -u REL_RELEASE_TAG -u MAKEFLAGS -u MAKELEVEL \
				make check-version-reconciled RELEASE_TAG="$2" 2>&1
	)
	STATUS=$?
	set -e
}

NAME="a tag carrying a shell metacharacter cannot execute"
fixture tag-injection
DIR="${WORK}/tag-injection"
CANARY_TAG="${WORK}/tag-injection-canary"
rm -f "${CANARY_TAG}"
reconcile "${DIR}" "sdk/go/gorm/conformance/v0.2.0-alpha;touch ${CANARY_TAG}"
if [ -e "${CANARY_TAG}" ]; then
	bad "${NAME}" "THE INJECTED COMMAND RAN — the canary file exists"
	rm -f "${CANARY_TAG}"
elif [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0 for a tag that is not a release tag"
else
	case "${OUTPUT}" in
	*"is not a release tag"*) ok "${NAME}" ;;
	*) bad "${NAME}" "refused, but not by the grammar check" ;;
	esac
fi

# The grammar check closes the class, not just the injection: a tag that is
# harmless but cannot name a release is refused too, with the expected form
# named so the reader knows what was wanted.
NAME="a harmless tag outside the grammar is refused"
fixture tag-offgrammar
reconcile "${WORK}/tag-offgrammar" "release-candidate-3"
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0"
else
	case "${OUTPUT}" in
	*"expected [<module-path>/]vX.Y.Z"*) ok "${NAME}" ;;
	*) bad "${NAME}" "no message naming the expected form" ;;
	esac
fi

# And a legitimate deeper tag still reconciles, which is what the widened
# trigger exists to support.
NAME="a legitimate deeper tag still reconciles"
fixture tag-deeper-ok
printf '0.2.0-alpha\n' > "${WORK}/tag-deeper-ok/VERSION"
reconcile "${WORK}/tag-deeper-ok" "sdk/go/gorm/conformance/v0.2.0-alpha"
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "a valid deeper tag was refused"
	echo "${OUTPUT}" | sed 's/^/        /'
else
	ok "${NAME}"
fi

# ---------------------------------------------------------------------------
# The lockstep namespace is not a per-invocation choice
#
# SUBMODULE_TAGS is declared with `override`, so neither the command line nor
# the environment can shorten or redirect it. The trigger below was a real
# BLOCKING finding on PR #212: with SUBMODULE_TAGS= the recipe passed its
# preflight lockstep check, committed VERSION, created the signed ROOT tag, and
# only then failed the post-tag audit -- leaving a signed stray tag and a
# release commit behind, and blocking the retry on its own debris.
# ---------------------------------------------------------------------------

NAME="an empty SUBMODULE_TAGS cannot drop the sdk/go lockstep leg"
fixture empty-submodules
run "${WORK}/empty-submodules" VERSION=v0.2.0-alpha SUBMODULE_TAGS=
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif ! git -C "${WORK}/empty-submodules" rev-parse -q --verify refs/tags/sdk/go/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "the sdk/go lockstep tag was not created"
elif [ "$(git ls-remote --tags "${WORK}/empty-submodules.git" | grep -c -v '\^{}$')" -ne 2 ]; then
	bad "${NAME}" "the published tag set is not the lockstep pair"
else
	ok "${NAME}"
fi

NAME="an unexpected SUBMODULE_TAGS cannot redirect the lockstep namespace"
fixture other-submodules
run "${WORK}/other-submodules" VERSION=v0.2.0-alpha SUBMODULE_TAGS=some/other/module
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif git -C "${WORK}/other-submodules" rev-parse -q --verify refs/tags/some/other/module/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "a tag was cut in the namespace named on the command line"
elif ! git -C "${WORK}/other-submodules" rev-parse -q --verify refs/tags/sdk/go/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "the sdk/go lockstep tag was not created"
else
	ok "${NAME}"
fi

# The set is DISCOVERED from the module tree, so a module added to sdk/ joins
# the lockstep without anyone editing a list. That is the whole point: the
# declared list had already drifted past sdk/go/gorm/conformance (go-qrdb#50).
# Discovery picks a new module up with no edit to the Makefile -- but it is not
# released until somebody DECLARES it, which is the whole point of the baseline:
# adding a directory must not silently start publishing an irrevocable tag.
NAME="a newly discovered module is refused until it is declared"
fixture undeclared-new-module
DIR="${WORK}/undeclared-new-module"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a second nested module, undeclared"
# The lockstep prerequisite speaks first here, because the audit now fails on an
# undeclared module too -- both halves refuse, and the audit is upstream.
refuse "${NAME}" "${DIR}" "UNDECLARED  sdk/go/gorm/conformance" VERSION=v0.2.0-alpha

# THE BOOTSTRAP, which must keep working: a baseline naming a version that does
# not exist YET, when it is the version being cut. This is how a module's first
# release happens, and the fix for the case below must not break it.
NAME="a baseline equal to the version being cut is the first-release path"
fixture baseline-is-this-release
DIR="${WORK}/baseline-is-this-release"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
printf '%s\n' 'sdk/go/gorm/conformance v0.2.0-alpha' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "first release of a second module"
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the first release of a module was refused: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif ! git -C "${DIR}" rev-parse -q --verify refs/tags/sdk/go/gorm/conformance/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "the module got no tag"
else
	ok "${NAME}"
fi

# THE FOURTH STATE. A baseline naming a version that does not exist and is NOT
# the one being cut leaves the audit reporting that module `pending` forever, so
# every later release could omit its counterpart without failing anything. The
# refusal must land BEFORE any commit or tag: on this branch, a refusal after an
# irreversible step is not a refusal.
NAME="a baseline naming a version that never existed is refused"
fixture baseline-never-existed
DIR="${WORK}/baseline-never-existed"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
printf '%s\n' 'sdk/go/gorm/conformance v0.20.0-alpha' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a baseline that never existed"
refuse "${NAME}" "${DIR}" "no root tag v0.20.0-alpha exists" VERSION=v0.2.0-alpha

# A malformed baseline is refused, not treated as an exotic version string --
# downstream it would be fed to git rev-parse, fail, and be reported as "not
# tagged yet", which is indistinguishable from the legitimate pending state.
NAME="a malformed baseline line is refused"
fixture baseline-malformed
DIR="${WORK}/baseline-malformed"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
printf '%s\n' 'sdk/go/gorm/conformance 0x3-nonsense' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a malformed baseline"
refuse "${NAME}" "${DIR}" "malformed baseline" VERSION=v0.2.0-alpha

# Declared 'unreleased' is a decision, and it is a decision NOT to release --
# so the cut refuses rather than quietly omitting the module.
NAME="a module declared unreleased blocks the cut"
fixture unreleased-new-module
DIR="${WORK}/unreleased-new-module"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
printf '%s\n' 'sdk/go/gorm/conformance unreleased' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a second nested module, unreleased"
refuse "${NAME}" "${DIR}" "declared 'unreleased'" VERSION=v0.2.0-alpha

# And once declared at the version being cut, it is published -- with no edit to
# the Makefile, which is what deriving the set bought.
NAME="a module declared at the version being cut is published"
fixture discovers-new-module
DIR="${WORK}/discovers-new-module"
mkdir -p "${DIR}/sdk/go/gorm/conformance"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/gorm/conformance/go.mod"
printf '%s\n' 'sdk/go/gorm/conformance v0.2.0-alpha' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a second nested module, declared"
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif ! git -C "${DIR}" rev-parse -q --verify refs/tags/sdk/go/gorm/conformance/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "the new module got no lockstep tag"
elif [ "$(git ls-remote "${DIR}.git" | awk '{print $2}' | grep -c -v '\^{}$')" -ne 4 ]; then
	bad "${NAME}" "expected main plus three tags to be published"
else
	ok "${NAME}"
fi

# A lockstep tag is permanent and consumers resolve it, so a module whose
# go.mod does not match its own sources must not get one -- the exact skew the
# lockstep tag exists to prevent, created by the mechanism meant to prevent it.
# Owner ruling 2026-09-02, prompted by sdk/go/gorm/conformance, which is untidy
# on main today and therefore blocks releases until go-qrdb#50 lands.
NAME="a module that does not resolve standalone blocks the release"
fixture unresolvable-module
DIR="${WORK}/unresolvable-module"
mkdir -p "${DIR}/sdk/go/broken"
printf 'module github.com/jeremyhahn/go-qrdb/sdk/go/broken\n\ngo 1.26\n' \
	> "${DIR}/sdk/go/broken/go.mod"
# An import that go.mod does not require: `go mod tidy -diff` reports changes
# are needed, exactly as it does for the conformance module.
printf 'package broken\n\nimport _ "example.com/absent"\n' \
	> "${DIR}/sdk/go/broken/x.go"
# Declared, so the standalone-resolution check is the one that speaks rather
# than the baseline check upstream of it.
printf '%s\n' 'sdk/go/broken v0.2.0-alpha' >> "${DIR}/lockstep-baseline"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a module whose go.mod does not match its sources"
refuse "${NAME}" "${DIR}" "does not resolve standalone" VERSION=v0.2.0-alpha

# Discovery is scoped to sdk/, and that scope is the one judgement in it. This
# repository really does carry four benchmark harnesses under test/performance
# with their own go.mod files; a `find .` would tag every one of them, and the
# tags would be irrevocable. Pinned here so the scope cannot be widened by
# accident.
NAME="a module outside sdk/ is not tagged"
fixture module-outside-sdk
DIR="${WORK}/module-outside-sdk"
mkdir -p "${DIR}/test/performance/harness"
printf 'module github.com/jeremyhahn/go-qrdb/test/performance/harness\n\ngo 1.26\n' \
	> "${DIR}/test/performance/harness/go.mod"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "a benchmark harness module"
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "the release failed: exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif git -C "${DIR}" rev-parse -q --verify refs/tags/test/performance/harness/v0.2.0-alpha >/dev/null; then
	bad "${NAME}" "a benchmark harness was given an irrevocable release tag"
elif [ "$(git ls-remote "${DIR}.git" | awk '{print $2}' | grep -c -v '\^{}$')" -ne 3 ]; then
	bad "${NAME}" "the published ref set is not main plus the two lockstep tags"
else
	ok "${NAME}"
fi

# A discovery that finds NOTHING must not read as "there are no nested
# modules". That would publish a root tag with no lockstep leg at all and pass
# every other check -- the fail-open shape of the defect being fixed.
# Two distinct ways the set can come back empty, and they are different
# branches: sdk/ present but carrying no go.mod, and sdk/ absent entirely so the
# walk itself fails. Both must refuse; neither may be read as "there are none".
fixture no-modules
rm -f "${WORK}/no-modules/sdk/go/go.mod"
printf 'placeholder\n' > "${WORK}/no-modules/sdk/keep"
git -C "${WORK}/no-modules" add -A
git -C "${WORK}/no-modules" commit -q --no-gpg-sign -m "sdk/ with no modules in it"
# Now that the audit reads the SAME derivation, the discovery's own refusal is
# the first to speak -- the lockstep prerequisite fails before the recipe body
# runs. Both are correct refusals; this asserts the one that actually fires.
# The recipe's own non-empty assertion is kept as defence in depth for a caller
# that reaches the body with an empty set some other way, and is not
# independently reachable from a fixture -- said plainly rather than implied by
# a case that no longer tests it.
refuse "an empty discovery is refused, not read as 'there are none'" \
	"${WORK}/no-modules" "no nested modules found under sdk/" VERSION=v0.2.0-alpha

fixture no-sdk-tree
rm -rf "${WORK}/no-sdk-tree/sdk"
git -C "${WORK}/no-sdk-tree" add -A
git -C "${WORK}/no-sdk-tree" commit -q --no-gpg-sign -m "no sdk tree at all"
refuse "a discovery whose walk fails is refused, not read as an empty set" \
	"${WORK}/no-sdk-tree" "walking sdk/ for go.mod failed" VERSION=v0.2.0-alpha

# Discovery happens when make PARSES the file; the tags are cut much later.
# Reading a value at entry and acting on it later is the gap behind two of this
# branch's review findings, so the recipe re-checks the discovered set against
# the tree instead of trusting it -- and a re-check that cannot fail is not a
# check.
#
# The seam is the fixture's own copy of check-tag-lockstep.sh, which the recipe
# runs twice: once as the prerequisite, and again after the fetch. The first
# run drops a marker OUTSIDE the repository (so the working tree stays clean
# for the guard that checks it), and the second removes the module -- putting
# the deletion after the clean-tree guard and before the re-check, which is the
# only window where this guard is the one that speaks.
NAME="a discovered module that vanishes before the cut is refused"
fixture vanishing-module
DIR="${WORK}/vanishing-module"
printf '\nif [ -f ../.audit-ran ]; then rm -f sdk/go/go.mod; else : > ../.audit-ran; fi\n' \
	>> "${DIR}/scripts/check-tag-lockstep.sh"
git -C "${DIR}" add -A
git -C "${DIR}" commit -q --no-gpg-sign -m "audit removes the module mid-run"
BEFORE_VANISH=$(git -C "${DIR}" rev-parse HEAD)
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0; a module that is no longer there was tagged"
elif [ "$(git -C "${DIR}" rev-parse HEAD)" != "${BEFORE_VANISH}" ]; then
	bad "${NAME}" "the release commit survived"
elif [ -n "$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha')" ]; then
	bad "${NAME}" "a tag was created"
else
	case "${OUTPUT}" in
	*"has no go.mod now"*) ok "${NAME}" ;;
	*) bad "${NAME}" "wrong diagnostic"; echo "${OUTPUT}" | sed 's/^/        /' ;;
	esac
fi

# ---------------------------------------------------------------------------
# Failures partway through, where the damage would be
#
# One contract for all of them: past the VERSION write, the target either
# publishes or leaves the repository exactly as it was. Every case below both
# asserts that AND proves the recovery is nothing -- run the target again.
#
# The last three fail NOTHING on their own: the wrapper succeeds, the objects
# are well formed, and only the recipe's own assertions notice. Those are the
# paths that used to escape the cleanup handler.
# ---------------------------------------------------------------------------

# The `|| true` defect: v1 swallowed a failed VERSION commit and tagged anyway,
# producing a tag whose VERSION file disagreed with its own name.
fixture failed-commit
FAIL_MODE=commit
run "${WORK}/failed-commit" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "a failed VERSION commit is fatal" "exit 0, want non-zero"
else
	ok "a failed VERSION commit is fatal"
fi
assert_clean "a failed VERSION commit leaves nothing behind" "${WORK}/failed-commit"
assert_retry "a retry after a failed VERSION commit needs no manual steps" "${WORK}/failed-commit"

# A pair half-created. The root tag exists and is valid when the sdk leg fails.
fixture partial-tags
FAIL_MODE=sdktag
run "${WORK}/partial-tags" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "a failure partway through the tag pair is fatal" "exit 0, want non-zero"
else
	ok "a failure partway through the tag pair is fatal"
fi
assert_clean "a failure partway through the tag pair leaves nothing behind" "${WORK}/partial-tags"
assert_retry "a retry after a partial tag pair needs no manual steps" "${WORK}/partial-tags"

# THE PR #212 ESCAPE. Both tags are created, annotated and signed, so the type
# and signature assertions pass -- the sdk leg simply points at the wrong
# commit. It is now caught by the per-tag "points at the release commit" check,
# which covers every DISCOVERED module rather than only the ones the shared
# audit script knows about; before that check existed the post-tag lockstep
# audit caught it, and that audit sat OUTSIDE the cleanup path, which is what
# used to leave both signed tags and the release commit behind.
fixture post-tag-skew
FAIL_MODE=skewtag
run "${WORK}/post-tag-skew" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "a post-tag lockstep skew is fatal" "exit 0, want non-zero"
else
	case "${OUTPUT}" in
	*"does not point at the release commit"*) ok "a post-tag lockstep skew is fatal" ;;
	*) bad "a post-tag lockstep skew is fatal" "wrong diagnostic" ;;
	esac
fi
assert_clean "a post-tag lockstep skew leaves nothing behind" "${WORK}/post-tag-skew"
assert_retry "a retry after a post-tag lockstep skew needs no manual steps" "${WORK}/post-tag-skew"

# An AGENT_GIT that is executable and succeeds, but does not sign.
fixture unsigned-commit
FAIL_MODE=nosign
run "${WORK}/unsigned-commit" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "an unsigned release commit is fatal" "exit 0, want non-zero"
else
	case "${OUTPUT}" in
	*"no SSH signature"*) ok "an unsigned release commit is fatal" ;;
	*) bad "an unsigned release commit is fatal" "wrong diagnostic" ;;
	esac
fi
assert_clean "an unsigned release commit leaves nothing behind" "${WORK}/unsigned-commit"

# An AGENT_GIT that signs, but as the wrong principal.
fixture wrong-identity
FAIL_MODE=badidentity
run "${WORK}/wrong-identity" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "a release commit from the wrong identity is fatal" "exit 0, want non-zero"
else
	case "${OUTPUT}" in
	*"not the repository agent"*) ok "a release commit from the wrong identity is fatal" ;;
	*) bad "a release commit from the wrong identity is fatal" "wrong diagnostic" ;;
	esac
fi
assert_clean "a release commit from the wrong identity leaves nothing behind" "${WORK}/wrong-identity"

# The wrapper CREATES the tag and then exits nonzero. If the cleanup set is
# populated only after the wrapper returns, that tag is invisible to it, and the
# next attempt fails the duplicate-tag preflight on this run's own debris. Same
# shape as trusting a push's exit code: the operation can succeed while the
# report of it fails.
NAME="a tag created by a wrapper that then fails is still cleaned up"
fixture tag-then-fail
DIR="${WORK}/tag-then-fail"
FAIL_MODE=tagthenfail
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0"
else
	ok "${NAME}"
fi
assert_clean "a tag created before a reported failure leaves nothing behind" "${DIR}"
assert_retry "a retry after a created-then-failed tag needs no manual steps" "${DIR}"

# --atomic means a failed push published nothing, so there is nothing to keep.
fixture failed-push
FAIL_MODE=push
run "${WORK}/failed-push" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "a failed push is fatal" "exit 0, want non-zero"
else
	ok "a failed push is fatal"
fi
assert_clean "a failed push leaves nothing behind" "${WORK}/failed-push"
assert_retry "a retry after a failed push needs no manual steps" "${WORK}/failed-push"

# ---------------------------------------------------------------------------
# What the remote actually holds, when the client cannot say
#
# `--atomic` guarantees the remote applies every ref or none; it guarantees
# nothing about whether the CLIENT hears so. The `push` mode above rejects
# BEFORE contacting the remote, so it cannot reach any of this -- these three
# modes fail on the receive side, after the remote has done something.
# ---------------------------------------------------------------------------

# The server commits everything, then the client errors. Treating that as
# "nothing was published" and deleting the local objects would destroy the only
# local copy of a live, immutable release.
NAME="a push that publishes and then reports failure is a SUCCESS"
fixture push-then-fail
DIR="${WORK}/push-then-fail"
FAIL_MODE=pushthenfail
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "exit ${STATUS}; the client's error was believed over the remote"
	echo "${OUTPUT}" | sed 's/^/        /'
elif [ "$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha' | wc -l)" -ne 2 ]; then
	bad "${NAME}" "the local tags for a PUBLISHED release were deleted"
elif [ "$(git -C "${DIR}" log -1 --format=%s)" != "chore(release): v0.2.0-alpha" ]; then
	bad "${NAME}" "the release commit for a PUBLISHED release was rolled back"
elif [ "$(git ls-remote --tags "${DIR}.git" | grep -c -v '\^{}$')" -ne 2 ]; then
	bad "${NAME}" "the fixture did not actually publish; the case proves nothing"
else
	case "${OUTPUT}" in
	*"Released v0.2.0-alpha"*) ok "${NAME}" ;;
	*) bad "${NAME}" "it did not report the release as published" ;;
	esac
fi

# Some refs landed and some did not. --atomic is meant to make this impossible,
# which is exactly why it must not be treated as either of the clean outcomes.
NAME="a partially published push preserves everything and fails"
fixture push-partial
DIR="${WORK}/push-partial"
FAIL_MODE=pushpartial
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0 on a mixed remote state"
elif [ "$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha' | wc -l)" -ne 2 ]; then
	bad "${NAME}" "local tags were deleted while part of the release is published"
elif [ "$(git -C "${DIR}" log -1 --format=%s)" != "chore(release): v0.2.0-alpha" ]; then
	bad "${NAME}" "the release commit was rolled back while part of the release is published"
else
	case "${OUTPUT}" in
	*"MIXED state"*"NOTHING HAS BEEN ROLLED BACK"*) ok "${NAME}" ;;
	*) bad "${NAME}" "the diagnostic does not say what was and was not published" ;;
	esac
fi

# The push fails and the remote then cannot be queried at all. Unknown is not
# absent: preserve everything and say so.
NAME="an unverifiable push preserves everything and says it is unknown"
fixture push-unqueryable
DIR="${WORK}/push-unqueryable"
FAIL_MODE=pushunqueryable
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -eq 0 ]; then
	bad "${NAME}" "exit 0 without being able to confirm anything"
elif [ "$(git -C "${DIR}" tag --list 'v0.2.0-alpha' 'sdk/go/v0.2.0-alpha' | wc -l)" -ne 2 ]; then
	bad "${NAME}" "local tags were deleted without establishing that nothing was published"
elif [ "$(git -C "${DIR}" log -1 --format=%s)" != "chore(release): v0.2.0-alpha" ]; then
	bad "${NAME}" "the release commit was rolled back without establishing anything"
else
	case "${OUTPUT}" in
	*"UNKNOWN"*) ok "${NAME}" ;;
	*) bad "${NAME}" "the diagnostic does not say the outcome is unknown" ;;
	esac
fi
mv "${WORK}/push-unqueryable.git.gone" "${WORK}/push-unqueryable.git" 2>/dev/null || true

# ---------------------------------------------------------------------------
# The successful cut
# ---------------------------------------------------------------------------

fixture release
DIR="${WORK}/release"
run "${DIR}" VERSION=v0.2.0-alpha
RELEASED=0
if [ "${STATUS}" -ne 0 ]; then
	bad "a clean release succeeds" "exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
else
	RELEASED=1
	ok "a clean release succeeds"
fi

# Everything below inspects objects the release was supposed to create. If it
# did not run, they cannot be judged -- and reading a missing ref under `set -e`
# would abort the script and swallow the summary, turning a clear red into an
# ambiguous one.
if [ "${RELEASED}" -eq 0 ]; then
	bad "the post-release assertions" "not run: the release itself failed, so there is nothing to inspect"
else

NAME="the release commit is signed"
if [ "$(git -C "${DIR}" cat-file commit HEAD | grep -c '^gpgsig')" -eq 1 ]; then
	ok "${NAME}"
else
	bad "${NAME}" "expected exactly one gpgsig header on HEAD"
fi

# Authored AND committed as the agent. The wrapper takes its identity from the
# repo name it is handed, so this asserts the argument the recipe passed -- and
# the fixture directory is not named go-qrdb, so a REPO_NAME derived from the
# checkout would fail here.
NAME="the release commit is authored and committed by the repo agent"
IDENT=$(git -C "${DIR}" log -1 --format='%an %cn')
if [ "${IDENT}" = "agent-go-qrdb agent-go-qrdb" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "identity is '${IDENT}', want 'agent-go-qrdb agent-go-qrdb'"
fi

NAME="the tagged commit carries the version it is tagged with"
if [ "$(git -C "${DIR}" show HEAD:VERSION)" = "0.2.0-alpha" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "VERSION at HEAD is $(git -C "${DIR}" show HEAD:VERSION)"
fi

# A lightweight tag is a bare ref with no object of its own, so it cannot carry
# a signature whatever tag.gpgsign says. `cat-file -t` returning `commit` rather
# than `tag` IS the v1 defect, which is why the type is asserted and not just
# the presence of a signature.
for TAG in v0.2.0-alpha sdk/go/v0.2.0-alpha; do
	NAME="${TAG} is an annotated tag object"
	TYPE=$(git -C "${DIR}" cat-file -t "refs/tags/${TAG}" 2>/dev/null || echo absent)
	if [ "${TYPE}" = tag ]; then
		ok "${NAME}"
	else
		bad "${NAME}" "cat-file -t is '${TYPE}', want 'tag' (lightweight tags cannot be signed)"
	fi

	NAME="${TAG} carries a signature"
	if git -C "${DIR}" cat-file tag "refs/tags/${TAG}" | grep -q 'BEGIN SSH SIGNATURE'; then
		ok "${NAME}"
	else
		bad "${NAME}" "no signature block in the tag object"
	fi

	NAME="${TAG} points at the release commit"
	if [ "$(git -C "${DIR}" rev-parse "refs/tags/${TAG}^{commit}" 2>/dev/null || echo none)" = "$(git -C "${DIR}" rev-parse HEAD)" ]; then
		ok "${NAME}"
	else
		bad "${NAME}" "the tag does not peel to HEAD"
	fi
done

# Exactly main plus the two intended tags. v1's `push --tags` published every
# local tag, so the assertion is on the FULL published ref set, not on the
# presence of the two that were wanted.
NAME="exactly main and the two release tags are published"
PUBLISHED=$(git ls-remote "${DIR}.git" | awk '{print $2}' | grep -v '\^{}$' | sort | tr '\n' ' ')
WANT="refs/heads/main refs/tags/sdk/go/v0.2.0-alpha refs/tags/v0.2.0-alpha "
if [ "${PUBLISHED}" = "${WANT}" ]; then
	ok "${NAME}"
else
	bad "${NAME}" "published '${PUBLISHED}', want '${WANT}'"
fi

# ---------------------------------------------------------------------------
# How the signing wrapper was invoked
#
# The objects can look right for the wrong reason. This pins the exact call
# list: the repo name passed to every invocation, `-s` on both tags, `--atomic`
# and an explicit ref list on the push, and no sixth call of any kind.
# ---------------------------------------------------------------------------

NAME="the signing wrapper is invoked exactly as intended"
{
	echo 'go-qrdb|add|VERSION'
	echo 'go-qrdb|commit|-q|-m|chore(release): v0.2.0-alpha'
	echo 'go-qrdb|tag|-s|-m|v0.2.0-alpha|v0.2.0-alpha'
	echo 'go-qrdb|tag|-s|-m|sdk/go/v0.2.0-alpha|sdk/go/v0.2.0-alpha'
	echo "go-qrdb|push|--atomic|dev|$(git -C "${DIR}" rev-parse HEAD):refs/heads/main|refs/tags/v0.2.0-alpha|refs/tags/sdk/go/v0.2.0-alpha"
} > "${WORK}/expected.log"
if diff -u "${WORK}/expected.log" "${DIR}.gitlog" > "${WORK}/log.diff" 2>&1; then
	ok "${NAME}"
else
	bad "${NAME}" "the recorded invocations differ from the contract"
	sed 's/^/        /' "${WORK}/log.diff"
fi

# Stated separately because it is the defect, not a detail of the call list: a
# single `--tags` anywhere republishes every tag on the machine.
NAME="the push never uses --tags"
if grep -q -- '--tags' "${DIR}.gitlog"; then
	bad "${NAME}" "an invocation used --tags"
else
	ok "${NAME}"
fi

fi

# ---------------------------------------------------------------------------
# The VERSION-authoritative path (#13, owner ruling 2026-09-30). The Release
# workflow reads VERSION from main and passes it back as VERSION=, so main
# already holds the release commit: the recipe must tag HEAD, make no commit of
# its own, and still refuse a HEAD that is not signed.
# ---------------------------------------------------------------------------

# bump_version DIR SIGN -- land the reviewed VERSION bump on main, as a merged
# release PR would, signed or not.
bump_version() {
	printf '0.2.0-alpha\n' > "$1/VERSION"
	git -C "$1" add VERSION
	if [ "$2" = signed ]; then
		git -C "$1" -c gpg.format=ssh -c "user.signingkey=${WORK}/signing.key" \
			commit -q -S -m "chore(release): v0.2.0-alpha"
	else
		git -C "$1" commit -q --no-gpg-sign -m "chore(release): v0.2.0-alpha"
	fi
	git -C "$1" push -q dev main
}

NAME="VERSION already naming the release tags HEAD and commits nothing"
fixture authoritative
DIR="${WORK}/authoritative"
bump_version "${DIR}" signed
HEAD_BEFORE=$(git -C "${DIR}" rev-parse HEAD)
run "${DIR}" VERSION=v0.2.0-alpha
if [ "${STATUS}" -ne 0 ]; then
	bad "${NAME}" "exit ${STATUS}"
	echo "${OUTPUT}" | sed 's/^/        /'
elif [ "$(git -C "${DIR}" rev-parse HEAD)" != "${HEAD_BEFORE}" ]; then
	bad "${NAME}" "HEAD moved: the recipe made a commit of its own"
elif grep -q '^go-qrdb|commit' "${DIR}.gitlog"; then
	bad "${NAME}" "the wrapper was asked to commit"
else
	ok "${NAME}"
fi

NAME="the authoritative path puts every lockstep tag on the reviewed commit"
BAD_TAG=''
for T in v0.2.0-alpha sdk/go/v0.2.0-alpha; do
	[ "$(git -C "${DIR}" rev-parse "${T}^{commit}" 2>/dev/null)" = "${HEAD_BEFORE}" ] || BAD_TAG="${BAD_TAG} ${T}"
	git -C "${DIR}" cat-file tag "${T}" 2>/dev/null | grep -q 'BEGIN SSH SIGNATURE' || BAD_TAG="${BAD_TAG} ${T}(unsigned)"
done
if [ -n "${BAD_TAG}" ]; then
	bad "${NAME}" "wrong or unsigned:${BAD_TAG}"
else
	ok "${NAME}"
fi

NAME="the authoritative path publishes main plus exactly the two tags"
if [ "$(git ls-remote "${DIR}.git" | awk '{print $2}' | grep -c -v '\^{}$')" -ne 3 ]; then
	bad "${NAME}" "remote holds: $(git ls-remote "${DIR}.git" | awk '{print $2}' | tr '\n' ' ')"
else
	ok "${NAME}"
fi

fixture authoritative-unsigned
bump_version "${WORK}/authoritative-unsigned" unsigned
refuse "an unsigned HEAD that already names the release is refused" \
	"${WORK}/authoritative-unsigned" "carries no SSH signature" VERSION=v0.2.0-alpha

if [ "${FAILURES}" -ne 0 ]; then
	echo "${FAILURES} case(s) failed"
	exit 1
fi

echo "all cases passed"
