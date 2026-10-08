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
# check-tool-versions.sh — the analyser pins in ci.yml, the Makefile and both
# devcontainer Dockerfiles name the same versions (go-qrdb#323).
#
# Four scanner versions are written in four files. A Dockerfile cannot read a
# make variable and a workflow env block is not visible to either, so nothing
# but this script keeps them in step.
#
# WHY A CHECK AND NOT ONE COPY. #322 first removed the duplication by giving
# the Dockerfile ARGs no defaults, so the Makefile's value was the only one and
# there was nothing to drift. That broke VS Code "Reopen in Container" and a
# bare `docker compose build`, which #302 requires keep working, and the owner
# ruled on 2026-09-05 that a workflow regression is not a defect fix. The
# defaults came back as a DELIBERATE second copy. This script is the whole of
# what makes that safe: without it the ruling silently reintroduces the #154
# defect one level down, where a build through `make` and a build around it
# bake different analysers into the same image tag and nothing says so.
#
# WHAT DRIFT COSTS, measured. #154: the image installed gosec at `@latest`,
# which self-reports its version as `dev` because gosec takes that string from
# ldflags — so `make ci` printed `Gosec  : dev` and a finding under it could not
# be attributed to a scanner version AT ALL. A new finding and a moved scanner
# were indistinguishable. golangci-lint sat at v2.7.2 against ci.yml's pin,
# so a developer's tree was linted against a different rule set from the gate's
# while both reported passing the same gate.
#
# THE AUTHORITY IS ci.yml for the three Go analysers, because that env block is
# what the runner installs and therefore what actually judges a pull request.
# The Makefile and the Dockerfiles are held to it. trivy is NOT in ci.yml — the
# security suite runs it from the image only — so for trivy the Makefile is the
# authority and the CPU Dockerfile is held to that.
#
# Dockerfile.gpu deliberately declares NO TRIVY_VERSION: nothing runs `make
# security` in the GPU image, and an ARG for a tool the file never installs is a
# pin that pins nothing. That absence is asserted here rather than assumed, so
# adding the ARG "for symmetry" fails instead of quietly creating a fifth copy.
#
# DISCOVERY HAS A FLOOR. Every lookup that finds no pin is a failure, not a
# vacuous pass. Each of these patterns depends on a file path and a spelling,
# and both change; a renamed file or a reformatted assignment would otherwise
# turn this gate into a comparison over an empty set that always agrees. That
# failure mode is the one this script exists to prevent, so it must not be the
# script's own.
#
# WHAT THIS DOES NOT CHECK. That the pinned version is the version INSTALLED in
# the image — a pin says which analyser should be there, not which one is. That
# is require-tool.sh's job, asserted inside both Dockerfiles at build time and
# again in ci.yml. The two together are the guarantee; this half is the cheap
# one and runs in a second.
#
# Usage: check-tool-versions.sh [repo-dir]
#
#   repo-dir  defaults to the current directory
#
# Exit status: 0 when every pin agrees, 1 otherwise.

set -eu

REPO="${1:-.}"

CI_YML="$REPO/.github/workflows/ci.yml"
MAKEFILE="$REPO/Makefile"
DOCKERFILE="$REPO/.devcontainer/Dockerfile"
DOCKERFILE_GPU="$REPO/.devcontainer/Dockerfile.gpu"

# The three analysers ci.yml is the authority for, plus trivy, which it is not.
GO_ANALYSERS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION"

failed=0

fail() {
	echo "FAIL: $*" >&2
	failed=1
}

for f in "$CI_YML" "$MAKEFILE" "$DOCKERFILE" "$DOCKERFILE_GPU"; do
	[ -f "$f" ] || {
		echo "FAIL: $f does not exist -- this check cannot verify what it claims to." >&2
		echo "      A moved file must fail here rather than silently narrow the gate." >&2
		exit 1
	}
done

# `NAME: "vX.Y.Z"` in the workflow env block.
ci_pin() {
	sed -n "s/^[[:space:]]*$1:[[:space:]]*\"\{0,1\}\([^\"[:space:]]*\)\"\{0,1\}[[:space:]]*$/\1/p" \
		"$CI_YML" | head -n 1
}

# `NAME := vX.Y.Z` at the left margin.
make_pin() {
	sed -n "s/^$1[[:space:]]*:=[[:space:]]*\([^[:space:]]*\)[[:space:]]*$/\1/p" \
		"$MAKEFILE" | head -n 1
}

# `ARG NAME=vX.Y.Z`.
arg_pin() {
	sed -n "s/^ARG $2=\(.*\)$/\1/p" "$1" | head -n 1
}

# require <value> <where> <name> -- a lookup that found nothing is a broken
# pattern, and the gate says which one rather than comparing empty strings.
require() {
	[ -n "$1" ] && return 0
	fail "no $3 pin found in $2."
	echo "      The pattern that reads it has stopped matching -- a renamed file or a" >&2
	echo "      reformatted assignment. This is a failure and not a pass because an" >&2
	echo "      empty value compares equal to another empty value." >&2
	return 1
}

for name in $GO_ANALYSERS; do
	ci=$(ci_pin "$name")
	mk=$(make_pin "$name")
	df=$(arg_pin "$DOCKERFILE" "$name")
	gpu=$(arg_pin "$DOCKERFILE_GPU" "$name")

	require "$ci" ".github/workflows/ci.yml" "$name" || continue
	require "$mk" "Makefile" "$name" || continue
	require "$df" ".devcontainer/Dockerfile" "$name" || continue
	require "$gpu" ".devcontainer/Dockerfile.gpu" "$name" || continue

	for pair in "Makefile:$mk" ".devcontainer/Dockerfile:$df" ".devcontainer/Dockerfile.gpu:$gpu"; do
		where=${pair%%:*}
		value=${pair#*:}
		[ "$value" = "$ci" ] && continue
		fail "$name is $value in $where but $ci in .github/workflows/ci.yml."
		echo "      ci.yml is the authority: its env block is what the runner installs," >&2
		echo "      so it is what actually judges a pull request. Change the pin there" >&2
		echo "      and in every file listed above, together." >&2
	done
done

# trivy: the Makefile is the authority, because ci.yml carries no trivy pin --
# the security suite runs it out of the image rather than installing it.
mk_trivy=$(make_pin TRIVY_VERSION)
df_trivy=$(arg_pin "$DOCKERFILE" TRIVY_VERSION)
if require "$mk_trivy" "Makefile" "TRIVY_VERSION" &&
	require "$df_trivy" ".devcontainer/Dockerfile" "TRIVY_VERSION"; then
	if [ "$mk_trivy" != "$df_trivy" ]; then
		fail "TRIVY_VERSION is $df_trivy in .devcontainer/Dockerfile but $mk_trivy in the Makefile."
		echo "      ci.yml pins no trivy, so the Makefile is the authority for it." >&2
	fi
fi

# The GPU image installs no trivy, so an ARG for it would pin nothing.
if grep -q '^ARG TRIVY_VERSION' "$DOCKERFILE_GPU"; then
	fail ".devcontainer/Dockerfile.gpu declares ARG TRIVY_VERSION."
	echo "      That file installs no trivy -- nothing runs 'make security' in the GPU" >&2
	echo "      image -- so the ARG would be a fifth copy of the pin that pins nothing." >&2
	echo "      If the GPU image is ever taught to scan, add the install first and this" >&2
	echo "      assertion second." >&2
fi

if [ "$failed" -ne 0 ]; then
	exit 1
fi

echo "scanner pins agree across ci.yml, Makefile and both devcontainer Dockerfiles"
