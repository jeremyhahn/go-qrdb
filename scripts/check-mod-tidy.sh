#!/usr/bin/env bash
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
# check-mod-tidy.sh: every tracked module's go.mod and go.sum match its own
# sources, by `GOWORK=off go mod tidy -diff`, which mutates nothing.
#
# The module set is every tracked go.mod in the current directory's
# repository, not a list, so a new module is checked the day it is committed.
# The root go.mod missing from it means discovery failed, and an empty loop
# must not report success. Every module is asked before failing, so one run
# names all of them.
#
# `go mod tidy -diff` has two ways to exit non-zero, and they mean different
# things:
#
#   - a diff on stdout: the module is NOT TIDY. The diff is printed (bounded)
#     with the remedy.
#   - no diff: the check COULD NOT RUN (a module download failed, a proxy
#     timed out, the cache lacks a module under GOPROXY=off). Whether the
#     module is tidy is UNKNOWN. The tool's stderr is printed without its
#     `go: downloading` progress lines. This case alone is retried, once, and
#     the output says so.
#
# Both fail the gate; neither passes silently.
#
# Usage: check-mod-tidy.sh            (from the repository root)
# Environment: GO, the go command (default: go).

set -euo pipefail

GO="${GO:-go}"
# Bounds on what one module may print, so one failure cannot bury the rest.
DIFF_MAX_LINES=200
STDERR_MAX_LINES=40

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

mapfile -t mods < <(git ls-files -- 'go.mod' '*/go.mod')
case " ${mods[*]} " in
*" go.mod "*) ;;
*)
	echo "ERROR: module discovery did not find the root go.mod (found: ${mods[*]})"
	exit 1
	;;
esac

# print_bounded <file> <max-lines> prints at most max-lines of file, indented,
# and says how many it left out.
print_bounded() {
	local file="$1" max="$2" total
	total=$(wc -l <"$file")
	head -n "$max" "$file" | sed 's/^/    /'
	if [ "$total" -gt "$max" ]; then
		echo "    ... $((total - max)) more lines not shown"
	fi
}

# tidy_diff <dir> runs the check once and prints its verdict: tidy, untidy or
# unknown. stdout and stderr are left in $work/out and $work/err.
tidy_diff() {
	if (cd "$1" && GOWORK=off $GO mod tidy -diff) >"$work/out" 2>"$work/err"; then
		echo tidy
	elif [ -s "$work/out" ]; then
		echo untidy
	else
		echo unknown
	fi
}

untidy=
unknown=
for m in "${mods[@]}"; do
	d=$(dirname "$m")
	verdict=$(tidy_diff "$d")
	if [ "$verdict" = unknown ]; then
		echo "==> $d: go mod tidy -diff could not run; retrying once"
		verdict=$(tidy_diff "$d")
		if [ "$verdict" = tidy ]; then
			echo "==> $d: tidy on the retry"
		fi
	fi
	case "$verdict" in
	untidy)
		echo "==> $d: not tidy; go mod tidy -diff wants:"
		print_bounded "$work/out" "$DIFF_MAX_LINES"
		untidy="$untidy $d"
		;;
	unknown)
		echo "==> $d: go mod tidy -diff could not run, so its tidiness is UNKNOWN:"
		{ grep -v '^go: downloading ' "$work/err" || true; } >"$work/err.filtered"
		print_bounded "$work/err.filtered" "$STDERR_MAX_LINES"
		unknown="$unknown $d"
		;;
	esac
done

if [ -n "$untidy" ]; then
	echo "ERROR: go.mod/go.sum is not tidy in:$untidy. Run 'GOWORK=off go mod tidy' in each and commit the result"
fi
if [ -n "$unknown" ]; then
	echo "ERROR: tidiness is UNKNOWN in:$unknown. 'go mod tidy -diff' could not run there (its error is above); this is not a finding that the module is untidy, so fix the cause and run the gate again"
fi
if [ -n "$untidy$unknown" ]; then
	exit 1
fi
echo "==> every module is tidy: ${mods[*]}"
