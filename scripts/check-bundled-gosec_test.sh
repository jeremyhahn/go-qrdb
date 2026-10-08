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
# Regression tests for check-bundled-gosec.sh.
#
# The bundled gosec matches its recorded version for as long as golangci-lint
# stays put -- that is the point -- so running the check here exercises only its
# ACCEPTING branch, and every rejecting branch would otherwise ship untested.
# Same reasoning as require-tool_test.sh and check-tool-versions_test.sh.
#
# THE FIXTURE IS REAL OUTPUT, captured from `go version -m` on a golangci-lint
# v2.14.0 built by `go install`, not written from memory. The tab-separated
# record shape and the ordering of `dep` and `=>` are the whole of what the
# parser depends on.
#
# `go` and `golangci-lint` are faked on a REPLACED PATH rather than a prepended
# one: prepending leaves a developer's real binaries reachable, so the "tool is
# absent" cases would find them and the test for absence could not make
# anything absent.
#
# Usage: check-bundled-gosec_test.sh    (no network, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/check-bundled-gosec.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

PATH="$work/bin:/usr/bin:/bin"
export PATH

pass=0
fail=0

# repo <name> <golangci-gosec-pin> <gosec-pin> -- a Makefile carrying the two
# values the check reads. Cases mutate the arguments, not a copy of the tree.
repo() {
	dir="$work/repo-$1"
	rm -rf "$dir"
	mkdir -p "$dir"
	{
		echo "GOLANGCI_LINT_VERSION := v2.14.0"
		[ "$2" = "-" ] || echo "GOLANGCI_GOSEC_VERSION := $2"
		[ "$3" = "-" ] || echo "GOSEC_VERSION         := $3"
	} > "$dir/Makefile"
	echo "$dir"
}

# fakego <build-info-lines...> -- a `go` whose `version -m` prints the given
# records. Anything else it is asked for is a failure: this subject calls it
# exactly one way.
fakego() {
	{
		echo '#!/bin/sh'
		echo 'if [ "$1" = "version" ] && [ "$2" = "-m" ]; then'
		echo "cat <<'EOF'"
		printf '%s\n' "$@"
		echo 'EOF'
		echo 'exit 0'
		echo 'fi'
		echo 'echo "unexpected go invocation: $*" >&2'
		echo 'exit 1'
	} > "$work/bin/go"
	chmod +x "$work/bin/go"
}

fakego_broken() {
	printf '#!/bin/sh\necho "go: no such file" >&2\nexit 1\n' > "$work/bin/go"
	chmod +x "$work/bin/go"
}

fakelint() {
	printf '#!/bin/sh\nexit 0\n' > "$work/bin/golangci-lint"
	chmod +x "$work/bin/golangci-lint"
}

# expect <accept|reject> <label> <want-substring> <check args...>
expect() {
	want="$1"
	label="$2"
	want_text="$3"
	shift 3
	if "$CHECK" "$@" >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	if [ "$got" != "$want" ]; then
		printf '  FAIL  %-62s want %s, got %s\n' "$label" "$want" "$got"
		sed 's/^/          /' "$work/out"
		fail=$((fail + 1))
		return
	fi
	if [ -n "$want_text" ]; then
		if ! grep -q -- "$want_text" "$work/out"; then
			printf '  FAIL  %-62s output did not mention: %s\n' "$label" "$want_text"
			sed 's/^/          /' "$work/out"
			fail=$((fail + 1))
			return
		fi
	fi
	printf '  PASS  %-62s (%s)\n' "$label" "$got"
	pass=$((pass + 1))
}

TAB=$(printf '\t')
gosecdep() { printf '%sdep%s github.com/securego/gosec/v2%s%s%sh1:pF2HSLcnY5voqpxQumEe0O5YAAbvF4Syu4qeIewpLyc=' "$TAB" "$TAB" "$TAB" "$1" "$TAB"; }

# The real records, in the real order, from a `go install`ed golangci-lint.
# The optional second argument adds the `=>` record go writes IMMEDIATELY AFTER
# the dep it replaces -- that position is the whole of what the parser uses to
# tie a replacement to its dependency, so the fixture has to honour it.
buildinfo_lines() {
	printf '%s: go1.27.1\n' "$work/bin/golangci-lint"
	printf '%spath%sgithub.com/golangci/golangci-lint/v2/cmd/golangci-lint\n' "$TAB" "$TAB"
	printf '%smod%sgithub.com/golangci/golangci-lint/v2%sv2.14.0%sh1:ot8QffRa4LzAAEgtvNYrVs9esxQ7xoTwoAv6uhp4ngA=\n' "$TAB" "$TAB" "$TAB" "$TAB"
	printf '%sdep%sgithub.com/ccojocar/zxcvbn-go%sv1.0.4%sh1:FWnCIRMXPj43ukfX000kvBZvV6raSxakYr1nzyNrUcc=\n' "$TAB" "$TAB" "$TAB" "$TAB"
	printf '%sdep%sgithub.com/securego/gosec/v2%s%s%sh1:pF2HSLcnY5voqpxQumEe0O5YAAbvF4Syu4qeIewpLyc=\n' "$TAB" "$TAB" "$TAB" "$1" "$TAB"
	[ -z "${2:-}" ] || printf '%s=>%s%s%sh1:0000000000000000000000000000000000000000000=\n' "$TAB" "$TAB" "$2" "$TAB"
	printf '%sdep%sgolang.org/x/tools%sv0.39.0%sh1:0000000000000000000000000000000000000000000=\n' "$TAB" "$TAB" "$TAB" "$TAB"
	printf '%sbuild%s-buildmode=exe\n' "$TAB" "$TAB"
}

fakelint

echo "== the accepting branch =="
fakego "$(buildinfo_lines v2.29.0)"
expect accept "bundled v2.29.0, measured v2.29.0" \
	"bundles gosec v2.29.0" "$(repo ok v2.29.0 v2.30.0)"

echo "== the divergence from GOSEC_VERSION is PRINTED, not merely tolerated =="
expect accept "note names both legs and both versions" \
	"DIFFERENT gosecs" "$(repo ok v2.29.0 v2.30.0)"

echo "== a golangci-lint from a release tarball records (devel) for its own module =="
# Only the main-module record is (devel); dependency versions stay exact. The
# subject reports that field and asserts the dep, so this must still ACCEPT --
# asserting a field one legitimate install method omits would fail for a reason
# unrelated to the property.
fakego "$(printf '%s: go1.27.1\n%smod%sgithub.com/golangci/golangci-lint/v2%s(devel)\n%s\n' \
	"$work/bin/golangci-lint" "$TAB" "$TAB" "$TAB" "$(gosecdep v2.29.0)")"
expect accept "main module (devel), dep exact" \
	"bundles gosec v2.29.0" "$(repo ok v2.29.0 v2.30.0)"

echo "== golangci-lint moved and took a different gosec with it: must REJECT =="
fakego "$(buildinfo_lines v2.30.0)"
expect reject "bundled v2.30.0 against measured v2.29.0" \
	"Re-measure" "$(repo ok v2.29.0 v2.30.0)"

echo "== the near-miss a substring comparison would accept: must REJECT =="
fakego "$(buildinfo_lines v12.29.0)"
expect reject "bundled v12.29.0 against measured v2.29.0" \
	"bundles gosec v12.29.0" "$(repo ok v2.29.0 v2.30.0)"
fakego "$(buildinfo_lines v2.29.01)"
expect reject "bundled v2.29.01 against measured v2.29.0" \
	"bundles gosec v2.29.01" "$(repo ok v2.29.0 v2.30.0)"

echo "== the v prefix is optional on either side =="
fakego "$(buildinfo_lines 2.29.0)"
expect accept "bundled 2.29.0 against measured v2.29.0" \
	"bundles gosec 2.29.0" "$(repo ok v2.29.0 v2.30.0)"

echo "== a replaced gosec is unknown code: must REJECT =="
fakego "$(buildinfo_lines v2.29.0 "example.com/fork/gosec${TAB}v0.0.1")"
expect reject "gosec replaced by a fork" \
	"replaces github.com/securego/gosec/v2" "$(repo ok v2.29.0 v2.30.0)"

echo "== nothing to read is never a pass =="
fakego "$(printf '%s: go1.27.1\n%smod%sgithub.com/golangci/golangci-lint/v2%sv2.14.0\n' \
	"$work/bin/golangci-lint" "$TAB" "$TAB" "$TAB")"
expect reject "no gosec dependency recorded" \
	"records no dependency" "$(repo ok v2.29.0 v2.30.0)"

# The gosec module named by a `mod` record instead of a `dep` record is the
# gosec binary itself, not a golangci-lint that bundles it. Accepting it would
# let the wrong binary satisfy this check.
fakego "$(printf '%s: go1.27.1\n%smod%sgithub.com/securego/gosec/v2%sv2.30.0\n' \
	"$work/bin/golangci-lint" "$TAB" "$TAB" "$TAB")"
expect reject "gosec's own binary offered as the carrier" \
	"records no dependency" "$(repo ok v2.29.0 v2.30.0)"

fakego_broken
expect reject "go cannot read the build info" \
	"could not read the build info" "$(repo ok v2.29.0 v2.30.0)"

echo "== the discovery floor: a lookup that finds nothing must FAIL =="
fakego "$(buildinfo_lines v2.29.0)"
expect reject "GOLANGCI_GOSEC_VERSION absent from the Makefile" \
	"no GOLANGCI_GOSEC_VERSION pin" "$(repo nopin - v2.30.0)"
expect reject "GOSEC_VERSION absent from the Makefile" \
	"no GOSEC_VERSION pin" "$(repo nogosec v2.29.0 -)"
expect reject "Makefile absent" \
	"does not exist" "$work/no-such-repo"

echo "== a missing tool is an image defect, not a network install =="
rm -f "$work/bin/golangci-lint"
expect reject "golangci-lint absent" \
	"is not installed" "$(repo ok v2.29.0 v2.30.0)"
fakelint
rm -f "$work/bin/go"
expect reject "go absent" \
	"go is not on PATH" "$(repo ok v2.29.0 v2.30.0)"

echo
echo "check-bundled-gosec.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
