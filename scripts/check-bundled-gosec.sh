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
# check-bundled-gosec.sh — golangci-lint bundles a gosec, and this repository's
# security exclusions were measured against a SPECIFIC one (go-qrdb#295).
#
# WHY THIS EXISTS
#
# Two legs of this repository's security gate run gosec, and they cannot run
# the same one:
#
#   make gosec / gosec-sdk   the gosec CLI, pinned directly by GOSEC_VERSION
#   make lint / lint-sdk     the gosec INSIDE golangci-lint, pinned only
#                            transitively by whatever that release depends on
#
# So the second version is real, it judges pull requests, and until this script
# it was written down nowhere. It moves when golangci-lint moves, silently, and
# with it moves the meaning of every gosec entry in .golangci.yml.
#
# That is not hypothetical. golangci-lint v2.13.2 bundled gosec v2.28.0, whose
# G703 accepts filepath.Clean as a path-traversal sanitizer; the directly
# pinned v2.29.0 does not, having removed Clean from its sanitizer list with
# the note that Clean "evaluates '..' elements instead of rejecting them, so
# the traversal succeeds". The two legs therefore differed in SOUNDNESS, not
# just in strictness, and the .golangci.yml entry written against the older one
# was INERT while reading as a decision the gate had made (#181, #295). From
# golangci-lint v2.14.0 the bundled gosec is v2.29.0, the same as the direct
# pin, but nothing keeps it so: the next golangci-lint release chooses its own.
#
# WHAT IT ASSERTS, AND WHY NOT THE OBVIOUS THING
#
# The obvious assertion — bundled gosec == GOSEC_VERSION — cannot hold and is
# not what this checks. Nothing in this repository chooses golangci-lint's
# dependency; demanding they match would be a gate that is red by construction,
# which is a gate nobody keeps.
#
# What it asserts instead is that the bundled gosec is THE ONE THE EXCLUSIONS
# WERE MEASURED AGAINST — GOLANGCI_GOSEC_VERSION in the Makefile, a reviewed
# value with a measurement behind it. When golangci-lint is bumped and its
# gosec moves with it, this goes RED and names the re-measurement, instead of
# the exclusion lists quietly coming to mean something else. The divergence
# from GOSEC_VERSION is then PRINTED, every run, so the fact that this tree is
# judged by two different gosecs is visible rather than deducible.
#
# It reads the binary's own build info rather than a pin, for the same reason
# require-tool.sh reads a self-report: a pin says which analyser should be
# there, not which one is. `go version -m` is stronger than a self-report,
# because a dependency version comes from the module graph and cannot be handed
# in by ldflags — which is precisely how gosec's own version string came to say
# `dev` (#154, #195).
#
# THIS AND require-tool.sh ARE ONE ASSERTION IN TWO HALVES, and the ci.yml
# Lint job runs them in that order: require-tool.sh establishes that the
# golangci-lint on PATH IS the pinned one, and this establishes what that
# binary bundles. Neither half is checked here twice: golangci-lint's own
# module version is REPORTED below, not asserted, because a release built from
# a tarball rather than by `go install` records `(devel)` for its main module
# while still recording exact versions for every dependency. Asserting a field
# that is legitimately absent in one install method would fail for a reason
# that has nothing to do with the property.
#
# DISCOVERY HAS A FLOOR, as in check-tool-versions.sh: every lookup that finds
# nothing is a failure, never a vacuous pass. A renamed variable or a
# reformatted assignment would otherwise turn this into a comparison over empty
# strings, which always agree.
#
# Usage: check-bundled-gosec.sh [repo-dir] [golangci-lint-binary]
#
#   repo-dir                 defaults to the current directory
#   golangci-lint-binary     defaults to `golangci-lint` on PATH
#
# Exit status: 0 when the bundled gosec is the measured one, 1 otherwise.

set -eu

REPO="${1:-.}"
BIN="${2:-golangci-lint}"

MAKEFILE="$REPO/Makefile"
PINS="$REPO/scripts/tool-pins.sh"
GOSEC_MODULE="github.com/securego/gosec/v2"
GOLANGCI_MODULE="github.com/golangci/golangci-lint/v2"

for f in "$MAKEFILE" "$PINS"; do
	[ -f "$f" ] || {
		echo "FAIL: $f does not exist -- this check cannot verify what it claims to." >&2
		echo "      A moved file must fail here rather than silently narrow the gate." >&2
		exit 1
	}
done

# `NAME := vX.Y.Z` at the left margin of the Makefile: GOLANGCI_GOSEC_VERSION
# is a measurement recorded beside the code that relies on it, not a tool pin.
make_pin() {
	sed -n "s/^$1[[:space:]]*:=[[:space:]]*\([^[:space:]]*\)[[:space:]]*$/\1/p" \
		"$MAKEFILE" | head -n 1
}

# `NAME=vX.Y.Z` at the left margin of the pin file, the form
# check-tool-versions.sh reads every tool pin in.
pin() {
	sed -n "s/^$1=\([^[:space:]]*\)[[:space:]]*$/\1/p" "$PINS" | head -n 1
}

measured=$(make_pin GOLANGCI_GOSEC_VERSION)
direct=$(pin GOSEC_VERSION)

for pair in "GOLANGCI_GOSEC_VERSION:$measured:$MAKEFILE" "GOSEC_VERSION:$direct:$PINS"; do
	name=${pair%%:*}
	rest=${pair#*:}
	[ -n "${rest%%:*}" ] && continue
	echo "FAIL: no $name pin found in ${rest#*:}." >&2
	echo "      The pattern that reads it has stopped matching -- a renamed variable" >&2
	echo "      or a reformatted assignment. This is a failure and not a pass because" >&2
	echo "      an empty value compares equal to another empty value." >&2
	exit 1
done

command -v go >/dev/null 2>&1 || {
	echo "FAIL: go is not on PATH, so the build info of ${BIN} cannot be read." >&2
	echo "      An unreadable dependency version is not a matching one." >&2
	exit 1
}

binpath=$(command -v "$BIN" 2>/dev/null) || binpath=""
[ -n "$binpath" ] || {
	echo "FAIL: ${BIN} is not installed, so what it bundles cannot be read." >&2
	echo "      It is supposed to be baked into the runner image and into the" >&2
	echo "      devcontainer at GOLANGCI_LINT_VERSION. Refusing to install it over" >&2
	echo "      the network: that is the intermittent failure require-tool.sh exists" >&2
	echo "      to remove (#179)." >&2
	exit 1
}

if ! buildinfo=$(go version -m "$binpath" 2>&1) || [ -z "$buildinfo" ]; then
	echo "FAIL: could not read the build info of ${binpath}." >&2
	printf '%s\n' "$buildinfo" | sed 's/^/      /' >&2
	exit 1
fi

# `go version -m` writes tab-separated records:
#
#	<path>: go1.27.1
#		path	github.com/golangci/golangci-lint/v2/cmd/golangci-lint
#		mod	github.com/golangci/golangci-lint/v2	v2.14.0	h1:...
#		dep	github.com/securego/gosec/v2	v2.29.0	h1:...
#
# The `dep` record is matched on BOTH the field kind and the exact module path;
# matching the path alone would also accept the `mod` record of a gosec built
# from its own repository. A `=>` record, when present, IMMEDIATELY FOLLOWS the
# dep it replaces and names different code at a different version -- so the
# record after the match is read too, and a replaced gosec is refused rather
# than reported under the replaced-away version.
parsed=$(printf '%s\n' "$buildinfo" | awk -v m="$GOSEC_MODULE" '
	found && $1 == "=>" { printf "%s|%s@%s\n", ver, $2, $3; done = 1; exit }
	found                { printf "%s|\n", ver; done = 1; exit }
	$1 == "dep" && $2 == m { ver = $3; found = 1; next }
	END { if (found && !done) printf "%s|\n", ver }
')
bundled=${parsed%%|*}
replacement=${parsed#*|}

carrier=$(printf '%s\n' "$buildinfo" |
	awk -v m="$GOLANGCI_MODULE" '$1 == "mod" && $2 == m { print $3; exit }')

[ -n "$bundled" ] || {
	echo "FAIL: ${binpath} records no dependency on ${GOSEC_MODULE}." >&2
	echo "      Either it is not golangci-lint, or it was built without module" >&2
	echo "      information, or golangci-lint has stopped vendoring gosec. All three" >&2
	echo "      mean the gosec that judges .golangci.yml's exclusions is unknown," >&2
	echo "      which is the state this check exists to refuse." >&2
	printf '%s\n' "$buildinfo" | sed 's/^/      /' >&2
	exit 1
}

[ -z "$replacement" ] || {
	echo "FAIL: ${binpath} replaces ${GOSEC_MODULE} with ${replacement}." >&2
	echo "      A replaced dependency is not the code the release ships, so the" >&2
	echo "      version recorded against ${GOSEC_MODULE} says nothing about what" >&2
	echo "      actually analyses this tree. That is an unknown analyser, and this" >&2
	echo "      check refuses one rather than reporting the replaced-away version." >&2
	exit 1
}

# Compared EXACTLY, with the `v` prefix optional on either side -- never as a
# substring. A substring test accepts v12.28.0 for a v2.28.0 pin, which is a
# check looser than the property it claims; require-tool.sh carries the same
# comparison and the same warning.
if [ "${bundled#v}" != "${measured#v}" ]; then
	echo "FAIL: ${BIN} bundles gosec ${bundled}, not the measured ${measured}." >&2
	echo "      ${binpath}${carrier:+ (golangci-lint ${carrier})}" >&2
	echo "" >&2
	echo "      GOLANGCI_GOSEC_VERSION is not a pin -- nothing here chooses" >&2
	echo "      golangci-lint's dependencies. It records which gosec the security" >&2
	echo "      exclusions in .golangci.yml were MEASURED against. A different one" >&2
	echo "      reports a different set of findings, so those entries no longer mean" >&2
	echo "      what their comments say: an entry measured as load-bearing may now be" >&2
	echo "      inert, and an inert entry reads as a decision the gate made." >&2
	echo "" >&2
	echo "      Re-measure, then update GOLANGCI_GOSEC_VERSION together with the" >&2
	echo "      counts recorded beside each entry in .golangci.yml. The procedure is" >&2
	echo "      in the comment above that block." >&2
	exit 1
fi

echo "ok  ${BIN}${carrier:+ ${carrier}} bundles gosec ${bundled}, the version .golangci.yml's exclusions were measured against"

if [ "${bundled#v}" != "${direct#v}" ]; then
	echo "note  the two security legs run DIFFERENT gosecs, by construction:"
	echo "        make lint / lint-sdk    gosec ${bundled}  (bundled in ${BIN})"
	echo "        make gosec / gosec-sdk  gosec ${direct}  (GOSEC_VERSION, pinned directly)"
	echo "      Neither list of exclusions describes both legs. Each entry carries the"
	echo "      leg it was measured on; do not copy one file's entry into the other."
fi
