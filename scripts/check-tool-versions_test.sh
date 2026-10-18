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
# Regression tests for check-tool-versions.sh.
#
# This repository's pins agree, permanently -- that is the point of the check --
# so running it here exercises only its ACCEPTING branch, and every rejecting
# branch would otherwise ship untested. Same reasoning as
# check-publication-bound_test.sh and check-tag-lockstep_test.sh.
#
# Each case builds a throwaway tree carrying one specific disagreement and
# asserts BOTH the exit status and the diagnostic. The diagnostic is asserted
# because the failure this check exists to catch is a silent one: a gate that
# rejects for the wrong stated reason sends the next person to the wrong file.
#
# The DISCOVERY FLOOR cases matter most. A pattern that stops matching turns
# this gate into a comparison over empty strings, which always agrees -- the
# check would go green while verifying nothing, which is the exact shape of the
# defect it was written to prevent.
#
# Usage: check-tool-versions_test.sh    (no arguments; needs a temp dir)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-tool-versions.sh"

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT INT TERM

FAILURES=0

# A tree whose pins all agree. Cases mutate a copy of it.
fixture() {
	DIR="${WORK}/$1"
	rm -rf "${DIR}"
	mkdir -p "${DIR}/.github/workflows" "${DIR}/.devcontainer" "${DIR}/scripts"

	cat > "${DIR}/scripts/tool-pins.sh" <<-'EOF'
		GO_IMAGE=golang:1.27.2-alpine3.24@sha256:0000
		GOLANGCI_LINT_VERSION=v2.14.0
		ACTIONLINT_VERSION=v1.7.12
		GOSEC_VERSION=v2.29.0
		GOVULNCHECK_VERSION=v1.8.0
		TRIVY_VERSION=v0.75.0
	EOF

	cat > "${DIR}/.github/workflows/ci.yml" <<-'EOF'
		name: CI
		env:
		  GO_BUILD_TAGS: "codec_json raft"
	EOF

	cat > "${DIR}/Makefile" <<-'EOF'
		TOOL_PINS := scripts/tool-pins.sh
		include $(TOOL_PINS)
		export ACTIONLINT_VERSION
		GOLANGCI_GOSEC_VERSION := v2.27.0
	EOF

	cat > "${DIR}/.devcontainer/Dockerfile" <<-'EOF'
		FROM golang:1.27.1
		ARG GOLANGCI_LINT_VERSION=v2.14.0
		ARG GOSEC_VERSION=v2.29.0
		ARG GOVULNCHECK_VERSION=v1.8.0
		ARG TRIVY_VERSION=v0.75.0
	EOF

	cat > "${DIR}/.devcontainer/Dockerfile.gpu" <<-'EOF'
		FROM nvidia/cuda:12.4.0-devel-ubuntu24.04
		ARG GOLANGCI_LINT_VERSION=v2.14.0
		ARG GOSEC_VERSION=v2.29.0
		ARG GOVULNCHECK_VERSION=v1.8.0
	EOF

	echo "${DIR}"
}

# assert <name> <want-status> <want-substring> <dir>
assert() {
	name="$1"
	want_status="$2"
	want_text="$3"
	dir="$4"

	set +e
	output=$("${CHECK}" "${dir}" 2>&1)
	got_status=$?
	set -e

	if [ "${got_status}" -ne "${want_status}" ]; then
		echo "FAIL: ${name}"
		echo "      exit status ${got_status}, want ${want_status}"
		echo "      output: ${output}"
		FAILURES=$((FAILURES + 1))
		return
	fi
	case "${output}" in
	*"${want_text}"*) ;;
	*)
		echo "FAIL: ${name}"
		echo "      diagnostic did not mention: ${want_text}"
		echo "      output: ${output}"
		FAILURES=$((FAILURES + 1))
		return
		;;
	esac
	echo "ok: ${name}"
}

# ---------------------------------------------------------------------------
# The accepting branch.
# ---------------------------------------------------------------------------

DIR=$(fixture agree)
assert "pins that agree pass" 0 "tool pins agree" "${DIR}"

# ---------------------------------------------------------------------------
# Disagreement, once per copy held to the pin file. Each is asserted
# separately: a comparison loop that dropped one file would still pass the
# others, and the gap would be invisible.
# ---------------------------------------------------------------------------

DIR=$(fixture ci-pin)
printf '  GOSEC_VERSION: "v2.29.0"\n' >> "${DIR}/.github/workflows/ci.yml"
assert "a ci.yml pin, even one that agrees, is rejected" 1 \
	".github/workflows/ci.yml pins GOSEC_VERSION" "${DIR}"

DIR=$(fixture ci-actionlint-pin)
printf '  ACTIONLINT_VERSION: "v1.7.11"\n' >> "${DIR}/.github/workflows/ci.yml"
assert "a ci.yml actionlint pin is rejected" 1 \
	".github/workflows/ci.yml pins ACTIONLINT_VERSION" "${DIR}"

DIR=$(fixture dockerfile-drift)
sed -i 's/ARG GOLANGCI_LINT_VERSION=v2.14.0/ARG GOLANGCI_LINT_VERSION=v2.7.2/' "${DIR}/.devcontainer/Dockerfile"
assert "a CPU Dockerfile default off the pin file is rejected" 1 \
	"GOLANGCI_LINT_VERSION is v2.7.2 in .devcontainer/Dockerfile" "${DIR}"

DIR=$(fixture gpu-drift)
sed -i 's/ARG GOVULNCHECK_VERSION=v1.8.0/ARG GOVULNCHECK_VERSION=v1.6.0/' "${DIR}/.devcontainer/Dockerfile.gpu"
assert "a GPU Dockerfile default off the pin file is rejected" 1 \
	"GOVULNCHECK_VERSION is v1.6.0 in .devcontainer/Dockerfile.gpu" "${DIR}"

DIR=$(fixture trivy-drift)
sed -i 's/ARG TRIVY_VERSION=v0.75.0/ARG TRIVY_VERSION=v0.60.0/' "${DIR}/.devcontainer/Dockerfile"
assert "a trivy default off the pin file is rejected" 1 \
	"TRIVY_VERSION is v0.60.0 in .devcontainer/Dockerfile but v0.75.0" "${DIR}"

# The authority is named in the diagnostic, because "these disagree" does not
# tell the next person which one to change.
DIR=$(fixture names-authority)
sed -i 's/ARG GOSEC_VERSION=v2.29.0/ARG GOSEC_VERSION=v2.28.0/' "${DIR}/.devcontainer/Dockerfile"
assert "the diagnostic names the pin file as the authority" 1 \
	"scripts/tool-pins.sh is the authority" "${DIR}"

# A pin moved in the pin file alone is drift in every copy, and each says so.
DIR=$(fixture pin-file-moved)
sed -i 's/^GOSEC_VERSION=v2.29.0/GOSEC_VERSION=v2.30.0/' "${DIR}/scripts/tool-pins.sh"
assert "a bump in the pin file alone names the copies left behind" 1 \
	"GOSEC_VERSION is v2.29.0 in .devcontainer/Dockerfile.gpu but v2.30.0" "${DIR}"

# ---------------------------------------------------------------------------
# One pin file: the Makefile reads it and does not copy it.
# ---------------------------------------------------------------------------

DIR=$(fixture makefile-assigns)
printf 'GOSEC_VERSION := v2.28.0\n' >> "${DIR}/Makefile"
assert "a Makefile assignment overriding the pin file is rejected" 1 \
	"the Makefile assigns GOSEC_VERSION" "${DIR}"

DIR=$(fixture makefile-conditional)
printf 'TRIVY_VERSION ?= v0.60.0\n' >> "${DIR}/Makefile"
assert "a conditional Makefile assignment is rejected too" 1 \
	"the Makefile assigns TRIVY_VERSION" "${DIR}"

DIR=$(fixture makefile-no-include)
sed -i '/^include/d' "${DIR}/Makefile"
assert "a Makefile that stops including the pin file is rejected" 1 \
	"the Makefile does not include scripts/tool-pins.sh" "${DIR}"

# Copies that are correctly absent must not be reported. Without these cases a
# future edit that held a tool to ci.yml, or actionlint to the Dockerfiles,
# would reject the tree this repository actually has.
DIR=$(fixture absent-where-not-installed)
assert "no pin in ci.yml and actionlint absent from the Dockerfiles is not a finding" 0 \
	"tool pins agree" "${DIR}"

DIR=$(fixture gpu-trivy)
printf 'ARG TRIVY_VERSION=v0.75.0\n' >> "${DIR}/.devcontainer/Dockerfile.gpu"
assert "an ARG TRIVY_VERSION in the GPU image is rejected" 1 \
	"Dockerfile.gpu declares ARG TRIVY_VERSION" "${DIR}"

# ---------------------------------------------------------------------------
# The discovery floor. These are the cases that keep the gate from passing
# vacuously, and each one is a real way the patterns break.
# ---------------------------------------------------------------------------

DIR=$(fixture pin-removed)
sed -i '/^GOSEC_VERSION/d' "${DIR}/scripts/tool-pins.sh"
assert "a pin that vanished from the pin file is a failure, not a pass" 1 \
	"no GOSEC_VERSION pin found in scripts/tool-pins.sh" "${DIR}"

DIR=$(fixture reformatted)
sed -i 's/^GOVULNCHECK_VERSION=v1.8.0/GOVULNCHECK_VERSION = v1.8.0/' "${DIR}/scripts/tool-pins.sh"
assert "an assignment the pattern no longer matches is a failure" 1 \
	"no GOVULNCHECK_VERSION pin found in scripts/tool-pins.sh" "${DIR}"

DIR=$(fixture floor-explains)
sed -i '/GOSEC_VERSION/d' "${DIR}/.devcontainer/Dockerfile"
assert "the floor explains why an empty lookup cannot be a pass" 1 \
	"empty value compares equal to another empty value" "${DIR}"

DIR=$(fixture file-moved)
rm "${DIR}/.devcontainer/Dockerfile.gpu"
assert "a moved file fails rather than narrowing the gate" 1 \
	"does not exist" "${DIR}"

DIR=$(fixture pin-file-missing)
rm "${DIR}/scripts/tool-pins.sh"
assert "a missing pin file fails rather than passing over nothing" 1 \
	"tool-pins.sh does not exist" "${DIR}"

# ---------------------------------------------------------------------------

if [ "${FAILURES}" -ne 0 ]; then
	echo ""
	echo "${FAILURES} case(s) failed"
	exit 1
fi

echo ""
echo "all check-tool-versions.sh cases passed"
