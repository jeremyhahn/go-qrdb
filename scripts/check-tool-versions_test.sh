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
	mkdir -p "${DIR}/.github/workflows" "${DIR}/.devcontainer"

	cat > "${DIR}/.github/workflows/ci.yml" <<-'EOF'
		name: CI
		env:
		  GOLANGCI_LINT_VERSION: "v2.14.0"
		  GOSEC_VERSION: "v2.29.0"
		  GOVULNCHECK_VERSION: "v1.8.0"
	EOF

	cat > "${DIR}/Makefile" <<-'EOF'
		GOLANGCI_LINT_VERSION := v2.14.0
		GOSEC_VERSION         := v2.29.0
		GOVULNCHECK_VERSION   := v1.8.0
		TRIVY_VERSION         := v0.75.0
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
assert "pins that agree pass" 0 "scanner pins agree" "${DIR}"

# ---------------------------------------------------------------------------
# Disagreement, once per file that is held to ci.yml. Each is asserted
# separately: a comparison loop that dropped one file would still pass the
# other two, and the gap would be invisible.
# ---------------------------------------------------------------------------

DIR=$(fixture makefile-drift)
sed -i 's/GOSEC_VERSION         := v2.29.0/GOSEC_VERSION         := v2.28.0/' "${DIR}/Makefile"
assert "a Makefile pin behind ci.yml is rejected" 1 \
	"GOSEC_VERSION is v2.28.0 in Makefile but v2.29.0" "${DIR}"

DIR=$(fixture dockerfile-drift)
sed -i 's/ARG GOLANGCI_LINT_VERSION=v2.14.0/ARG GOLANGCI_LINT_VERSION=v2.7.2/' "${DIR}/.devcontainer/Dockerfile"
assert "a CPU Dockerfile default behind ci.yml is rejected" 1 \
	"GOLANGCI_LINT_VERSION is v2.7.2 in .devcontainer/Dockerfile" "${DIR}"

DIR=$(fixture gpu-drift)
sed -i 's/ARG GOVULNCHECK_VERSION=v1.8.0/ARG GOVULNCHECK_VERSION=v1.6.0/' "${DIR}/.devcontainer/Dockerfile.gpu"
assert "a GPU Dockerfile default behind ci.yml is rejected" 1 \
	"GOVULNCHECK_VERSION is v1.6.0 in .devcontainer/Dockerfile.gpu" "${DIR}"

# The authority is named in the diagnostic, because "these disagree" does not
# tell the next person which one to change.
DIR=$(fixture names-authority)
sed -i 's/ARG GOSEC_VERSION=v2.29.0/ARG GOSEC_VERSION=v2.28.0/' "${DIR}/.devcontainer/Dockerfile"
assert "the diagnostic names ci.yml as the authority" 1 \
	"ci.yml is the authority" "${DIR}"

# ---------------------------------------------------------------------------
# trivy, whose authority is the Makefile rather than ci.yml.
# ---------------------------------------------------------------------------

DIR=$(fixture trivy-drift)
sed -i 's/ARG TRIVY_VERSION=v0.75.0/ARG TRIVY_VERSION=v0.60.0/' "${DIR}/.devcontainer/Dockerfile"
assert "a trivy pin that disagrees with the Makefile is rejected" 1 \
	"TRIVY_VERSION is v0.60.0 in .devcontainer/Dockerfile but v0.75.0" "${DIR}"

DIR=$(fixture trivy-authority)
sed -i 's/ARG TRIVY_VERSION=v0.75.0/ARG TRIVY_VERSION=v0.60.0/' "${DIR}/.devcontainer/Dockerfile"
assert "and says the Makefile is trivy's authority, not ci.yml" 1 \
	"ci.yml pins no trivy" "${DIR}"

# A trivy pin absent from ci.yml is CORRECT and must not be reported. Without
# this case, a future edit that held trivy to ci.yml like the other three would
# reject the tree this repository actually has.
DIR=$(fixture trivy-absent-from-ci)
assert "trivy missing from ci.yml is not a finding" 0 "scanner pins agree" "${DIR}"

DIR=$(fixture gpu-trivy)
printf 'ARG TRIVY_VERSION=v0.75.0\n' >> "${DIR}/.devcontainer/Dockerfile.gpu"
assert "an ARG TRIVY_VERSION in the GPU image is rejected" 1 \
	"Dockerfile.gpu declares ARG TRIVY_VERSION" "${DIR}"

# ---------------------------------------------------------------------------
# The discovery floor. These are the cases that keep the gate from passing
# vacuously, and each one is a real way the patterns break.
# ---------------------------------------------------------------------------

DIR=$(fixture pin-removed)
sed -i '/GOSEC_VERSION/d' "${DIR}/Makefile"
assert "a pin that vanished from the Makefile is a failure, not a pass" 1 \
	"no GOSEC_VERSION pin found in Makefile" "${DIR}"

DIR=$(fixture reformatted)
sed -i 's/GOVULNCHECK_VERSION   := v1.8.0/GOVULNCHECK_VERSION ?= v1.8.0/' "${DIR}/Makefile"
assert "an assignment the pattern no longer matches is a failure" 1 \
	"no GOVULNCHECK_VERSION pin found in Makefile" "${DIR}"

DIR=$(fixture floor-explains)
sed -i '/GOSEC_VERSION/d' "${DIR}/.github/workflows/ci.yml"
assert "the floor explains why an empty lookup cannot be a pass" 1 \
	"empty value compares equal to another empty value" "${DIR}"

DIR=$(fixture file-moved)
rm "${DIR}/.devcontainer/Dockerfile.gpu"
assert "a moved file fails rather than narrowing the gate" 1 \
	"does not exist" "${DIR}"

# ---------------------------------------------------------------------------

if [ "${FAILURES}" -ne 0 ]; then
	echo ""
	echo "${FAILURES} case(s) failed"
	exit 1
fi

echo ""
echo "all check-tool-versions.sh cases passed"
