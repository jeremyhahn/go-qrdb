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
# require-tool.sh — make a Go analysis tool available at an EXACT pinned
# version, or fail the job. Called from .github/workflows/ci.yml.
#
#   require-tool.sh <binary> <pin> <module-path> [extra go-install args...]
#
# WHY THIS EXISTS
#
# (#179) CI used to `go install tool@version` at job time, wrapped in a
# hand-rolled 3-attempt retry loop. The retry loop is the tell. Every host in
# this fleet resolves an AAAA record for sum.golang.org and none has a default
# IPv6 route, and runner job containers use host networking, so they inherit
# that stack exactly. Go usually falls back v6 -> v4, which is why the failure
# was intermittent rather than constant — it surfaces when the fallback does
# not complete, and a loaded runner makes that likelier. Measured fleet-wide as
# 47 network installs across 18 of 19 repos. The tools are now baked into the
# self-hosted runner image at pinned versions, so on that host this script
# performs no network access at all.
#
# (#195) An install step is ALSO a version substitution. `actions/setup-go`
# prepends GOPATH/bin to PATH, so a freshly installed binary SHADOWS the baked
# one in /usr/local/bin. This repository's ci.yml installed one golangci-lint
# release unconditionally over an image that bakes another — believing itself
# pinned by the image while running something else entirely.
#
# (#195, gosec) Pinning alone was never sufficient for gosec: it takes its
# version string from ldflags rather than from the module path, so
# `go install ...@v2.29.0` still self-reports `dev`. A job log therefore could
# not attribute a scan to a scanner version at all, and two runs of one commit
# could disagree with nothing to distinguish them. The ASSERTION below is the
# deliverable, not the pin: whatever the binary is and wherever it came from,
# this refuses to continue unless it SAYS it is the pinned version.
#
# FAIL-CLOSED, in both directions:
#   - A tool missing on a self-hosted runner is an IMAGE defect. It is not
#     quietly papered over with a network install, because doing so would
#     restore precisely the flake this removes while reporting success. A
#     failed tool install leaves a red check carrying NO verdict on the code;
#     failing closed on a missing binary is what makes the two distinguishable.
#   - A tool whose self-report does not match the pin fails, rather than
#     scanning with an unknown version and calling the result a gate.
#   - A version that cannot be READ AT ALL fails. An unreadable self-report is
#     not evidence of a matching version.
#
# The github.com fallback is deliberate and narrow: this workflow is dual-plane
# (see the github.server_url guards on the SARIF steps in ci.yml), and on
# GitHub-hosted runners no image of ours exists and there is nothing to bake
# into. It is the only path here that touches the network, and it still has to
# satisfy the same assertion afterwards.
#
# Adopted from go-luks2's implementation of the same contract; its regression
# suite came with it as scripts/require-tool_test.sh.

set -eu

bin="${1:?usage: require-tool.sh <binary> <pin> <module-path> [go-install args...]}"
pin="${2:?missing pinned version}"
module="${3:?missing module path}"
shift 3

# The image records what it baked. Read it back when present: a version skew
# between this workflow's pin and the image's is a real inconsistency, and
# surfacing it here means it is caught before a scan runs rather than inferred
# afterwards from a result nobody can reproduce.
manifest=/etc/ci-runner-tools.txt
if [ -r "$manifest" ]; then
	echo "runner image tool manifest:"
	sed 's/^/  /' "$manifest"
fi

if ! command -v "$bin" >/dev/null 2>&1; then
	case "${GITHUB_SERVER_URL:-}" in
	https://github.com)
		echo "note: ${bin} is absent; installing ${module}@${pin} (GitHub-hosted runner, no baked image)"
		go install "$@" "${module}@${pin}"
		# actions/setup-go puts GOPATH/bin on PATH, which is what made the
		# install-then-run-bare arrangement work before. Assert it rather than
		# assume it, and assert it HERE: an exported PATH does not survive this
		# script, so if the binary is not reachable now it will not be reachable
		# in the step that runs it, and that failure would arrive looking like a
		# missing tool rather than a broken PATH.
		command -v "$bin" >/dev/null 2>&1 || {
			echo "FAIL: installed ${module}@${pin} but ${bin} is still not on PATH." >&2
			echo "Expected $(go env GOPATH)/bin to be on PATH (actions/setup-go)." >&2
			exit 1
		}
		;;
	*)
		echo "FAIL: ${bin} is not installed on this runner." >&2
		echo "It is supposed to be baked into the runner image at ${pin}." >&2
		echo "Refusing to install it over the network: that is the intermittent" >&2
		echo "ENETUNREACH failure this arrangement exists to remove, and a job" >&2
		echo "that silently reinstates it hides an image defect (#179)." >&2
		echo "Ask for the runner image to be rebuilt." >&2
		exit 1
		;;
	esac
fi

# Self-report. Tools disagree on the flag, so try both spellings; a tool that
# answers neither has not told us its version, and that is a failure.
if ! report="$("$bin" --version 2>&1)" || [ -z "$report" ]; then
	if ! report="$("$bin" -version 2>&1)" || [ -z "$report" ]; then
		echo "FAIL: ${bin} would not report a version." >&2
		echo "An unreadable version is not a matching version." >&2
		exit 1
	fi
fi

# EXTRACT THE VERSION TOKEN FROM EACH TOOL'S DOCUMENTED FORMAT, then compare it
# EXACTLY. A substring test (`case "$report" in *"$bare"*`) FAILS OPEN: a
# `v1.8.0` pin accepts a report saying `v11.8.0`, and a `v2.29.0` pin accepts
# `v12.29.0`. CI would then run an unintended scanner version and print `ok` —
# defeating the exact reproducibility assertion this check exists to make. A
# check looser than the property it claims is the same defect as no check, with
# a green tick on top. require-tool_test.sh carries those near-misses as cases
# and can re-loosen this line to prove they still fail.
#
# A GENERIC "find something version-shaped" regex is not the fix either, and
# govulncheck shows why: its output's FIRST version-shaped token is the Go
# toolchain's.
#
#	Go: go1.27.1
#	Scanner: govulncheck@v1.8.0
#
# A loose extractor pins the wrong number. So each tool is parsed by its own
# documented line, anchored at the start of it:
#
#	golangci-lint   golangci-lint has version 2.14.0 built with go1.27.1 ...
#	gosec           Version: v2.29.0
#	govulncheck     Scanner: govulncheck@v1.8.0
#
# An UNRECOGNISED tool is a hard failure rather than a fallback to something
# looser. A version format nobody has read is a version that cannot be
# verified, and guessing at it is how the substring test got written.
case "$bin" in
golangci-lint)
	got="$(printf '%s\n' "$report" |
		sed -n 's/^golangci-lint has version \([^[:space:]][^[:space:]]*\).*/\1/p' |
		head -1)"
	;;
gosec)
	got="$(printf '%s\n' "$report" |
		sed -n 's/^Version:[[:space:]][[:space:]]*\([^[:space:]][^[:space:]]*\)[[:space:]]*$/\1/p' |
		head -1)"
	;;
govulncheck)
	got="$(printf '%s\n' "$report" |
		sed -n 's/^Scanner:[[:space:]][[:space:]]*govulncheck@\([^[:space:]][^[:space:]]*\).*/\1/p' |
		head -1)"
	;;
*)
	echo "FAIL: no documented version format is known for '${bin}'." >&2
	echo "Refusing to verify it by guessing at its output. Add its format to" >&2
	echo "the case statement in scripts/require-tool.sh, having read what the" >&2
	echo "tool actually prints." >&2
	exit 1
	;;
esac

if [ -z "$got" ]; then
	echo "FAIL: could not extract a version from ${bin}'s output." >&2
	echo "It said:" >&2
	printf '%s\n' "$report" | sed 's/^/  /' >&2
	echo "The expected format is the one documented in this script. A version" >&2
	echo "that could not be parsed is not a matching version — this fails" >&2
	echo "rather than falling back to a looser comparison." >&2
	exit 1
fi

# Compare exactly, with the `v` prefix optional on EITHER side: golangci-lint
# reports bare (2.14.0) while gosec and govulncheck report prefixed (v2.29.0),
# and the pins are written prefixed. Nothing else is normalised away.
if [ "${got#v}" != "${pin#v}" ]; then
	echo "FAIL: ${bin} is version ${got}, not the pinned ${pin}." >&2
	echo "It said:" >&2
	printf '%s\n' "$report" | sed 's/^/  /' >&2
	echo "Scan and lint results that cannot be attributed to a known version" >&2
	echo "are not reproducible, which is the defect this check exists for" >&2
	echo "(#195). Bump the pin in .github/workflows/ci.yml deliberately," >&2
	echo "together with the runner image, or restore the image." >&2
	exit 1
fi

echo "ok  ${bin} ${pin}  ($(command -v "$bin"))"
