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
# Regression tests for check-mod-tidy.sh: a tidy module passes, an untidy one
# fails with its diff and the remedy, a module the check could not run in fails
# as UNKNOWN and never as untidy, and discovery without the root go.mod fails.
#
# Each case is a throwaway git repository. The tidy and untidy cases run the
# real go command on modules with no requirements, under GOPROXY=off, so
# nothing is downloaded; the could-not-run cases put a fake go on GO that
# writes a download error to stderr and nothing to stdout.
#
# Usage: check-mod-tidy_test.sh    (no network)

set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SUBJECT="${SCRIPT_DIR}/check-mod-tidy.sh"
REAL_GO="${GO:-go}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

export GOPROXY=off GOFLAGS=
export STATE="$work/state"

# fakego fails every call the way a proxy timeout does.
cat >"$work/bin/fakego" <<'EOF'
#!/bin/sh
echo 'go: downloading github.com/stretchr/testify v1.11.1' >&2
echo 'go: github.com/stretchr/testify@v1.11.1: Get "https://proxy.example.com/x.zip": net/http: TLS handshake timeout' >&2
exit 1
EOF
# flakygo fails its first call and reports tidy on every later one.
cat >"$work/bin/flakygo" <<'EOF'
#!/bin/sh
if [ ! -e "$STATE" ]; then
	: >"$STATE"
	echo 'go: net/http: TLS handshake timeout' >&2
	exit 1
fi
exit 0
EOF
chmod +x "$work/bin/fakego" "$work/bin/flakygo"

# repo <name> <module-dir>... makes a git repository holding a tidy,
# requirement-free module in each named directory ("." is the root) and
# prints its path.
repo() {
	local r="$work/$1" d
	shift
	mkdir -p "$r"
	git -C "$r" -c init.defaultBranch=main init -q
	for d in "$@"; do
		mkdir -p "$r/$d"
		printf 'module example.com/%s\n\ngo 1.27\n' "${d//[.\/]/x}" >"$r/$d/go.mod"
		printf 'package p\n' >"$r/$d/p.go"
	done
	git -C "$r" add -A
	echo "$r"
}

pass=0
fail=0

# run <label> <repo> <go> <accept|reject> <want-substring> [<absent-substring>]
run() {
	local label="$1" r="$2" gocmd="$3" want="$4" text="$5" absent="${6:-}" got
	rm -f "$STATE"
	if (cd "$r" && GO="$gocmd" "$SUBJECT") >"$work/out" 2>&1; then got=accept; else got=reject; fi
	if [ "$got" != "$want" ] || ! grep -qF -- "$text" "$work/out" ||
		{ [ -n "$absent" ] && grep -qF -- "$absent" "$work/out"; }; then
		printf '  FAIL  %-62s want %s with "%s"%s, got %s\n' "$label" "$want" "$text" \
			"${absent:+ and without \"$absent\"}" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
		return
	fi
	printf '  PASS  %-62s (%s)\n' "$label" "$got"
	pass=$((pass + 1))
}

r=$(repo tidy . sub)
run "tidy modules pass" "$r" "$REAL_GO" accept "every module is tidy: go.mod sub/go.mod"

r=$(repo untidy . sub)
printf 'example.com/stale v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n' >"$r/sub/go.sum"
git -C "$r" add -A
run "untidy module fails with the remedy" "$r" "$REAL_GO" reject \
	"not tidy in: sub. Run 'GOWORK=off go mod tidy' in each" "UNKNOWN"
run "untidy module's diff is printed" "$r" "$REAL_GO" reject \
	"-example.com/stale v1.0.0"

r=$(repo broken .)
run "could-not-run module fails as UNKNOWN" "$r" "$work/bin/fakego" reject \
	"tidiness is UNKNOWN in: ." "not tidy"
run "could-not-run prints the tool's error" "$r" "$work/bin/fakego" reject \
	"TLS handshake timeout" "go: downloading"
run "could-not-run is retried once, and says so" "$r" "$work/bin/fakego" reject \
	"could not run; retrying once"
run "a retry that succeeds passes and says so" "$r" "$work/bin/flakygo" accept \
	".: tidy on the retry"

r=$(repo noroot sub)
run "discovery without the root go.mod fails" "$r" "$REAL_GO" reject \
	"module discovery did not find the root go.mod"

echo
echo "check-mod-tidy.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
