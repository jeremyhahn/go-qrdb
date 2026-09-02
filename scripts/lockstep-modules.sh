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
# lockstep-modules.sh — the nested modules that are tagged in lockstep with the
# root module (release-versioning.md P2), one per line, sorted.
#
# THIS IS THE ONE DERIVATION. `release-alpha` publishes this set and
# check-tag-lockstep.sh audits it, and they call this rather than each deriving
# it. Two derivations that agree today are one edit away from disagreeing, and
# the failure is silent in exactly the direction that matters -- an auditor
# narrower than the publisher passes green while a namespace goes unchecked.
# That gap was real: the publisher was widened to discover modules while the
# audit still hard-coded sdk/go (reviewed on PR #212).
#
# SCOPED TO sdk/, which is the judgement in this script. `find .` also matches
# test/performance/{qrdb,tikv,etcd-benchmark,cmd/kvcompare}/go.mod -- local
# benchmark harnesses that must never be tagged -- and excluding them would
# need a hand-maintained exclusion list, which is the drift this exists to
# remove wearing a different hat. sdk/ is the published consumer surface; a
# module outside it is not part of the release. The failure directions decide
# it: a module wrongly INCLUDED gets an irrevocable tag, a module wrongly
# EXCLUDED gets a missing one that check-tag-lockstep and the Release Lockstep
# workflow both report. Only one of those is recoverable.
#
# NOT THE SAME QUESTION AS scripts/vet-modules.sh, which walks the WHOLE tree
# because `go vet` must cover every module including the benchmark harnesses.
# Same mechanism, different predicate: "what do we compile" against "what do we
# publish". They are deliberately not unified -- one function serving both
# would need a mode flag, and the flag would be the bug. If you are here to
# consolidate them, that is the reason not to. Ruled 2026-09-02.
#
# Usage:
#   lockstep-modules.sh [repo-dir]                 list the modules, one per line
#   lockstep-modules.sh --baseline <module> [dir]  print that module's baseline
#
# The baseline lookup lives here rather than in each caller because it parses
# the same data this script already owns -- the lockstep set and the metadata
# about it. That is one question with two projections, not two questions
# wearing one helper.
#
# Exit status: 0 when at least one module is found, 1 otherwise. An empty set
# is an ERROR, not an answer: a release with no lockstep leg is the skew this
# whole mechanism exists to prevent, so "I found nothing" must never be
# mistaken for "there are none".

set -eu

if [ "${1:-}" = "--baseline" ]; then
	MODULE="${2:?lockstep-modules: --baseline needs a module}"
	cd "${3:-.}"
	[ -f lockstep-baseline ] || {
		echo "lockstep-modules: lockstep-baseline is missing" >&2
		exit 1
	}
	# First field match, comments and blanks ignored. An unlisted module exits
	# nonzero with no output: undeclared is not the same as declared-unreleased,
	# and the caller must be able to tell them apart.
	BASELINE=$(awk -v m="${MODULE}" '
		/^[[:space:]]*#/ { next }
		NF >= 2 && $1 == m { print $2; found = 1; exit }
		END { exit found ? 0 : 1 }
	' lockstep-baseline) || exit 1

	# VALIDATE HERE, in the one parser, so both consumers fail closed without
	# each re-deriving what a baseline may say. A malformed line must refuse
	# rather than be handed on as an exotic version string: downstream it would
	# be fed to `git rev-parse refs/tags/<garbage>`, fail, and be reported as
	# "not tagged yet" -- indistinguishable from the legitimate pending state.
	#
	# The accepted version form is release-versioning.md 1's sanctioned tag
	# vocabulary: vX.Y.Z with an optional -alpha/-beta/-rc and no leading zeroes.
	case "${BASELINE}" in
	unreleased) ;;
	*)
		num='(0|[1-9][0-9]*)'
		if ! printf '%s' "${BASELINE}" |
			grep -Eq "^v${num}\\.${num}\\.${num}(-(alpha|beta|rc))?$"; then
			echo "lockstep-modules: ${MODULE} has a malformed baseline '${BASELINE}' —" >&2
			echo "  expected a version like v0.1.0-alpha, or the word 'unreleased'" >&2
			exit 1
		fi
		;;
	esac

	printf '%s\n' "${BASELINE}"
	exit 0
fi

cd "${1:-.}"

# `find | sort` reports find's failure as sort's success, so the raw output is
# captured first and its status checked. Lifted from a defect measured in
# scripts/vet-modules.sh, where a part-way find failure produced a confident
# "clean across all 0 modules".
RAW=$(find sdk -name go.mod -not -path '*/vendor/*' -not -path '*/testdata/*' 2>/dev/null) || {
	echo "lockstep-modules: walking sdk/ for go.mod failed" >&2
	exit 1
}

MODULES=$(printf '%s\n' "${RAW}" | sed -n 's|/go\.mod$||p' | sort)

if [ -z "${MODULES}" ]; then
	echo "lockstep-modules: no nested modules found under sdk/" >&2
	exit 1
fi

printf '%s\n' "${MODULES}"
