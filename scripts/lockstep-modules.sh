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
# lockstep-modules.sh: the nested modules tagged in lockstep with the root module, one per line, sorted.

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
