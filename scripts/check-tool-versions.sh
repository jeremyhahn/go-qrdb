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
# check-tool-versions.sh: every copy of a tool pin names the version in scripts/tool-pins.sh.

set -eu

REPO="${1:-.}"

PINS="$REPO/scripts/tool-pins.sh"
CI_YML="$REPO/.github/workflows/ci.yml"
MAKEFILE="$REPO/Makefile"
DOCKERFILE="$REPO/build/docker/devcontainer/Dockerfile"

# What each copy carries. The gpu stage installs neither trivy nor actionlint,
# and the dev stage no actionlint.
DOCKERFILE_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION TRIVY_VERSION"
GPU_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION"
ALL_TOOLS="GOLANGCI_LINT_VERSION GOSEC_VERSION GOVULNCHECK_VERSION ACTIONLINT_VERSION TRIVY_VERSION TRIVY_IMAGE"

failed=0

fail() {
	echo "FAIL: $*" >&2
	failed=1
}

for f in "$PINS" "$CI_YML" "$MAKEFILE" "$DOCKERFILE"; do
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

# The lines of stage <stage>: from its `FROM ... AS <stage>` line to the next
# FROM (or the end of the file).
stage_block() {
	sed -n "/^FROM .* AS $1\$/,/^FROM /p" "$DOCKERFILE"
}

# arg_pin <stage> <name> -- `ARG NAME=vX.Y.Z` inside that stage only.
arg_pin() {
	stage_block "$1" | sed -n "s/^ARG $2=\(.*\)$/\1/p" | head -n 1
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
		df=$(arg_pin dev "$name")
		require "$df" "build/docker/devcontainer/Dockerfile stage dev" "$name" && held "$df" "build/docker/devcontainer/Dockerfile stage dev" "$name" "$want"
		;;
	esac
	case " $GPU_TOOLS " in
	*" $name "*)
		gpu=$(arg_pin gpu "$name")
		require "$gpu" "build/docker/devcontainer/Dockerfile stage gpu" "$name" && held "$gpu" "build/docker/devcontainer/Dockerfile stage gpu" "$name" "$want"
		;;
	esac
done

# The shared global ARG TRIVY_IMAGE carries no default: a default would be a
# copy of the image pin that this script does not hold.
if grep -q '^ARG TRIVY_IMAGE=' "$DOCKERFILE"; then
	fail "build/docker/devcontainer/Dockerfile gives ARG TRIVY_IMAGE a default."
	echo "      The dev and CI stages share one TRIVY_IMAGE with no default; make" >&2
	echo "      passes scripts/tool-pins.sh's pin. Delete the default." >&2
fi

# The gpu stage installs no trivy, so an ARG for it would pin nothing.
if stage_block gpu | grep -q '^ARG TRIVY_VERSION'; then
	fail "build/docker/devcontainer/Dockerfile stage gpu declares ARG TRIVY_VERSION."
	echo "      That stage installs no trivy -- nothing runs 'make security' in the GPU" >&2
	echo "      image -- so the ARG would be a copy of the pin that pins nothing." >&2
	echo "      If the GPU image is ever taught to scan, add the install first and this" >&2
	echo "      assertion second." >&2
fi

if [ "$failed" -ne 0 ]; then
	exit 1
fi

echo "tool pins agree: scripts/tool-pins.sh and the dev and gpu stages of build/docker/devcontainer/Dockerfile; ci.yml and the Makefile carry none"
