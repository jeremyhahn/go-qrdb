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
# check-tool-versions.sh — every copy of a tool pin names the version
# scripts/tool-pins.sh names (go-qrdb#323, #738).
#
# THE AUTHORITY IS scripts/tool-pins.sh, the one pin file of handbook
# devcontainer.md §2. The CI image's toolchain stage
# (build/docker/devcontainer/Dockerfile) sources it and the Makefile includes
# it, so those two cannot disagree with it and are not compared. Two copies
# remain, each for a reason, and this script holds each to the pin file:
#
#   .devcontainer/Dockerfile       the developer devcontainer's ARG defaults.
#   .devcontainer/Dockerfile.gpu   #322 gave those ARGs no defaults, so make's
#                                  value was the only one; that broke VS Code
#                                  "Reopen in Container" and a bare
#                                  `docker compose build`, and the owner ruled on
#                                  2026-09-05 that a workflow regression is not a
#                                  defect fix. The defaults are a DELIBERATE
#                                  second copy, and this script is what makes
#                                  that safe.
#
# And two copies must NOT exist. A `NAME :=` (or `?=`, `=`) assignment in the
# Makefile: the Makefile includes the pin file; an assignment after the include
# silently overrides it, which is the #154 defect one level down -- a build
# through `make` and a build of the CI image baking different analysers while
# both claim the same pin. And a `NAME:` pin in .github/workflows/ci.yml: since
# ci-template v1.21 (#798) every stage runs in the CI image, which carries the
# tools at the pin file's versions, so a workflow pin would be a second
# authority that nothing reads.
#
# WHAT DRIFT COSTS, measured. #154: the image installed gosec at `@latest`,
# which self-reports its version as `dev` because gosec takes that string from
# ldflags — so `make ci` printed `Gosec  : dev` and a finding under it could not
# be attributed to a scanner version AT ALL. golangci-lint sat at v2.7.2 against
# ci.yml's pin, so a developer's tree was linted against a different rule set
# from the gate's while both reported passing the same gate.
#
# Dockerfile.gpu deliberately declares NO TRIVY_VERSION: nothing runs `make
# security` in the GPU image, and an ARG for a tool the file never installs is a
# pin that pins nothing. That absence is asserted here rather than assumed.
#
# DISCOVERY HAS A FLOOR. Every lookup that finds no pin is a failure, not a
# vacuous pass: a renamed file or a reformatted assignment would otherwise turn
# this gate into a comparison over empty strings that always agrees.
#
# WHAT THIS DOES NOT CHECK. That the pinned version is the version INSTALLED --
# a pin says which analyser should be there, not which one is. That is
# require-tool.sh's job, run by the Makefile's scanner targets and by ci.yml.
#
# Usage: check-tool-versions.sh [repo-dir]
#
#   repo-dir  defaults to the current directory
#
# Exit status: 0 when every pin agrees, 1 otherwise.

set -eu

REPO="${1:-.}"

PINS="$REPO/scripts/tool-pins.sh"
CI_YML="$REPO/.github/workflows/ci.yml"
MAKEFILE="$REPO/Makefile"
DOCKERFILE="$REPO/.devcontainer/Dockerfile"
DOCKERFILE_GPU="$REPO/.devcontainer/Dockerfile.gpu"

# What each copy carries. The GPU image installs neither trivy nor actionlint,
# and the developer devcontainer no actionlint.
DOCKERFILE_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION TRIVY_VERSION"
GPU_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION"
ALL_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION ACTIONLINT_VERSION TRIVY_VERSION"

failed=0

fail() {
	echo "FAIL: $*" >&2
	failed=1
}

for f in "$PINS" "$CI_YML" "$MAKEFILE" "$DOCKERFILE" "$DOCKERFILE_GPU"; do
	[ -f "$f" ] || {
		echo "FAIL: $f does not exist -- this check cannot verify what it claims to." >&2
		echo "      A moved file must fail here rather than silently narrow the gate." >&2
		exit 1
	}
done

# `NAME=value` at the left margin of the pin file.
pin() {
	sed -n "s/^$1=\\([^[:space:]]*\\)[[:space:]]*$/\\1/p" "$PINS" | head -n 1
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

# held <value> <where> <name> <authority-value>
held() {
	[ "$1" = "$4" ] && return 0
	fail "$3 is $1 in $2 but $4 in scripts/tool-pins.sh."
	echo "      scripts/tool-pins.sh is the authority: the CI image and the Makefile" >&2
	echo "      read it. Change the pin there and in every copy listed above, together." >&2
}

# The Makefile reads the pin file rather than copying it.
if ! grep -q '^include \$(TOOL_PINS)' "$MAKEFILE" ||
	! grep -q '^TOOL_PINS[[:space:]]*:=[[:space:]]*scripts/tool-pins.sh' "$MAKEFILE"; then
	fail "the Makefile does not include scripts/tool-pins.sh (TOOL_PINS := scripts/tool-pins.sh, include \$(TOOL_PINS))."
fi

for name in $ALL_TOOLS; do
	want=$(pin "$name")
	require "$want" "scripts/tool-pins.sh" "$name" || continue

	if grep -q "^$name[[:space:]]*[:?]\{0,1\}=" "$MAKEFILE"; then
		fail "the Makefile assigns $name, overriding scripts/tool-pins.sh."
		echo "      The Makefile includes the pin file; a second assignment is a second pin." >&2
	fi

	if grep -q "^[[:space:]]*$name[[:space:]]*:" "$CI_YML"; then
		fail ".github/workflows/ci.yml pins $name."
		echo "      Every CI stage runs in the CI image, built from scripts/tool-pins.sh;" >&2
		echo "      a workflow pin is a second pin that nothing reads. Delete it." >&2
	fi

	case " $DOCKERFILE_TOOLS " in
	*" $name "*)
		df=$(arg_pin "$DOCKERFILE" "$name")
		require "$df" ".devcontainer/Dockerfile" "$name" && held "$df" ".devcontainer/Dockerfile" "$name" "$want"
		;;
	esac
	case " $GPU_TOOLS " in
	*" $name "*)
		gpu=$(arg_pin "$DOCKERFILE_GPU" "$name")
		require "$gpu" ".devcontainer/Dockerfile.gpu" "$name" && held "$gpu" ".devcontainer/Dockerfile.gpu" "$name" "$want"
		;;
	esac
done

# The GPU image installs no trivy, so an ARG for it would pin nothing.
if grep -q '^ARG TRIVY_VERSION' "$DOCKERFILE_GPU"; then
	fail ".devcontainer/Dockerfile.gpu declares ARG TRIVY_VERSION."
	echo "      That file installs no trivy -- nothing runs 'make security' in the GPU" >&2
	echo "      image -- so the ARG would be a copy of the pin that pins nothing." >&2
	echo "      If the GPU image is ever taught to scan, add the install first and this" >&2
	echo "      assertion second." >&2
fi

if [ "$failed" -ne 0 ]; then
	exit 1
fi

echo "tool pins agree: scripts/tool-pins.sh and both .devcontainer Dockerfiles; ci.yml and the Makefile carry none"
