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
# Regression tests for deps-check.sh.
#
# The real check asks the module proxy, so its answer depends on the day; these
# cases replace `go` with a fake that prints a fixed `go list -m -u` answer per
# module, and assert BOTH the exit status and the diagnostic for each branch:
# a behind requirement with no reason, the two places a reason may sit, the
# adoption window for a young release, a failed lookup, and the discovery floor
# that keeps an empty module set from passing.
#
# Usage: deps-check_test.sh    (no arguments; needs a temp dir and git)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
CHECK="${SCRIPT_DIR}/deps-check.sh"

WORK=$(mktemp -d)
# The real-go case's module cache is read-only, as go leaves every module cache.
trap 'chmod -R u+w "${WORK}" 2>/dev/null; rm -rf "${WORK}"' EXIT INT TERM

FAILURES=0

# The fake go: `go list` prints the module directory's .fake-list and exits
# with its .fake-status (default 0). Anything else is a test defect.
FAKE_GO="${WORK}/fake-go"
cat >"${FAKE_GO}" <<-'EOF'
	#!/bin/sh
	[ "$1" = list ] || { echo "fake go: unexpected: $*" >&2; exit 99; }
	[ -f .fake-list ] && cat .fake-list
	exit "$(cat .fake-status 2>/dev/null || echo 0)"
EOF
chmod +x "${FAKE_GO}"

# A tree of two modules, both current. Cases mutate a copy of it.
fixture() {
	DIR="${WORK}/$1"
	rm -rf "${DIR}"
	mkdir -p "${DIR}/sdk/go"
	cat >"${DIR}/go.mod" <<-'EOF'
		module example.com/root

		go 1.27

		require (
			example.com/a v1.0.0
			// Held: v1.3 drops the API the WAL reader needs.
			example.com/b v1.2.0
			example.com/c v0.4.0 // held: v0.5 needs Go 1.28
			example.com/d v2.0.0
		)
	EOF
	cat >"${DIR}/sdk/go/go.mod" <<-'EOF'
		module example.com/root/sdk/go

		go 1.27

		require example.com/e v1.1.0
	EOF
	git -C "${DIR}" init -q
	git -C "${DIR}" add go.mod sdk/go/go.mod
	echo "${DIR}"
}

# assert <name> <want-status> <want-substring> <dir> [<go>]
#
# <go> defaults to the fake.
assert() {
	name="$1"
	want_status="$2"
	want_text="$3"
	dir="$4"
	go_bin="${5:-${FAKE_GO}}"

	set +e
	output=$(cd "${dir}" && GO="${go_bin}" "${CHECK}" 2>&1)
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
# The accepting branches.
# ---------------------------------------------------------------------------

DIR=$(fixture current)
assert "every requirement current passes, naming both modules" 0 "in 2 modules: go.mod sdk/go/go.mod" "${DIR}"

DIR=$(fixture inline-reason)
printf 'behind example.com/c v0.4.0 v0.5.0\n' >"${DIR}/.fake-list"
assert "a behind requirement with an inline reason passes" 0 "example.com/c v0.4.0 held below v0.5.0" "${DIR}"

DIR=$(fixture reason-above)
printf '\nbehind example.com/b v1.2.0 v1.3.0\n\n' >"${DIR}/.fake-list"
assert "a behind requirement with a reason on the line above passes" 0 "example.com/b v1.2.0 held below v1.3.0" "${DIR}"

# The adoption window: publish times relative to now, one day and eight days
# old, either side of the check's seven.
NOW=$(date +%s)
YOUNG=$((NOW - 86400))
OLD=$((NOW - 8 * 86400))

DIR=$(fixture young-unheld)
printf 'behind example.com/a v1.0.0 v1.1.0 %s\n' "${YOUNG}" >"${DIR}/.fake-list"
assert "a release younger than the window is reported, not failed" 0 "example.com/a v1.0.0 behind v1.1.0, released 24h ago (under the 7-day adoption window" "${DIR}"

DIR=$(fixture young-held)
printf 'behind example.com/c v0.4.0 v0.5.0 %s\n' "${YOUNG}" >"${DIR}/.fake-list"
assert "a held reason is honoured for a young release" 0 "example.com/c v0.4.0 held below v0.5.0" "${DIR}"

DIR=$(fixture old-held)
printf 'behind example.com/c v0.4.0 v0.5.0 %s\n' "${OLD}" >"${DIR}/.fake-list"
assert "a held reason is honoured for an old release" 0 "example.com/c v0.4.0 held below v0.5.0" "${DIR}"

# ---------------------------------------------------------------------------
# The rejecting branches.
# ---------------------------------------------------------------------------

DIR=$(fixture undocumented)
printf 'behind example.com/a v1.0.0 v1.1.0\n' >"${DIR}/.fake-list"
assert "a behind requirement with no reason is rejected" 1 "example.com/a v1.0.0, latest v1.1.0" "${DIR}"

# The reason above b must not cover the next line: a comment documents the one
# requirement it sits on.
DIR=$(fixture comment-scope)
printf 'behind example.com/d v2.0.0 v2.1.0\n' >"${DIR}/.fake-list"
assert "a reason above one requirement does not cover the next" 1 "example.com/d v2.0.0, latest v2.1.0" "${DIR}"

# A single-line `require` in a nested module, found and checked like the root.
DIR=$(fixture nested)
printf 'behind example.com/e v1.1.0 v1.2.0\n' >"${DIR}/sdk/go/.fake-list"
assert "a nested module's undocumented pin is rejected" 1 "sdk/go: example.com/e v1.1.0, latest v1.2.0" "${DIR}"

# Every module is asked before failing, so one run names every finding.
DIR=$(fixture all-named)
printf 'behind example.com/a v1.0.0 v1.1.0\n' >"${DIR}/.fake-list"
printf 'behind example.com/e v1.1.0 v1.2.0\n' >"${DIR}/sdk/go/.fake-list"
assert "one run names the findings in every module" 1 "sdk/go: example.com/e v1.1.0" "${DIR}"

# A reason recorded for another version is not a reason for this one.
DIR=$(fixture version-mismatch)
printf 'behind example.com/c v0.3.0 v0.5.0\n' >"${DIR}/.fake-list"
assert "a reason on another version's line does not count" 1 "example.com/c v0.3.0, latest v0.5.0" "${DIR}"

DIR=$(fixture old-unheld)
printf 'behind example.com/a v1.0.0 v1.1.0 %s\n' "${OLD}" >"${DIR}/.fake-list"
assert "a release older than the window with no reason is rejected" 1 "example.com/a v1.0.0, latest v1.1.0" "${DIR}"

# An upgrade whose time cannot be read is treated as old: missing, go's `-`,
# and something that is not a time all fail.
for published in '' '-' '2026-10-20T00:00:00Z'; do
	DIR=$(fixture "no-time${published}")
	printf 'behind example.com/a v1.0.0 v1.1.0 %s\n' "${published}" >"${DIR}/.fake-list"
	assert "an unreadable release time ('${published}') fails closed" 1 "example.com/a v1.0.0, latest v1.1.0" "${DIR}"
done

# A young and an old upgrade together: the run fails and names only the old one.
DIR=$(fixture young-and-old)
printf 'behind example.com/a v1.0.0 v1.1.0 %s\nbehind example.com/d v2.0.0 v2.1.0 %s\n' "${YOUNG}" "${OLD}" >"${DIR}/.fake-list"
assert "a young and an old upgrade fail on the old one" 1 "with no reason in go.mod:
  .: example.com/d v2.0.0, latest v2.1.0
Upgrade each" "${DIR}"
assert "a young and an old upgrade report the young one" 1 "example.com/a v1.0.0 behind v1.1.0, released 24h ago" "${DIR}"

# ---------------------------------------------------------------------------
# Unanswered questions fail: a lookup that failed checked nothing.
# ---------------------------------------------------------------------------

DIR=$(fixture lookup-failed)
echo 1 >"${DIR}/sdk/go/.fake-status"
assert "a failed go list fails the check and names the module" 1 "go list -m -u failed in: sdk/go" "${DIR}"

# go list -e answers the listing but records a checked requirement's lookup
# failure; that is still an unanswered question.
DIR=$(fixture unresolved)
printf 'error example.com/a v1.0.0 "unknown revision v1.0.0"\n' >"${DIR}/.fake-list"
assert "a direct requirement that cannot be looked up fails the check" 1 ".: example.com/a v1.0.0: \"unknown revision v1.0.0\"" "${DIR}"

DIR=$(fixture unexpected-line)
printf 'surprise example.com/a v1.0.0\n' >"${DIR}/.fake-list"
assert "a go list line the check cannot read fails the check" 1 "unexpected go list line: surprise example.com/a" "${DIR}"

# ---------------------------------------------------------------------------
# The real go command, offline, over a file:// module proxy: the template and
# -e are go's to interpret, which the fake cannot show.
#
# example.com/perf is published the way go-quicraft/test/performance is: it
# requires example.com/parent v0.0.0 and satisfies it with its own
# `replace => ../..`, which applies nowhere else. The consumer selects v0.0.0,
# the proxy has no such version, and `go list -m -u all` without -e fails on a
# module the check never inspects.
# ---------------------------------------------------------------------------

GO_BIN=$(command -v "${GO:-go}")
PROXY="${WORK}/proxy"
# publish <path> <version> <go.mod body> [<time>]
#
# <time> defaults to 2026-01-01, older than the adoption window.
publish() {
	mkdir -p "${PROXY}/$1/@v"
	printf '%s\n' "$2" >>"${PROXY}/$1/@v/list"
	printf '{"Version":"%s","Time":"%s"}\n' "$2" "${4:-2026-01-01T00:00:00Z}" >"${PROXY}/$1/@v/$2.info"
	printf '%s\n' "$3" >"${PROXY}/$1/@v/$2.mod"
}
publish example.com/perf v1.0.0 "$(printf 'module example.com/perf\n\ngo 1.21\n\nrequire example.com/parent v0.0.0\n\nreplace example.com/parent => ../..')"
publish example.com/lib v1.0.0 "$(printf 'module example.com/lib\n\ngo 1.21')"
publish example.com/lib v1.1.0 "$(printf 'module example.com/lib\n\ngo 1.21')"
publish example.com/fresh v1.0.0 "$(printf 'module example.com/fresh\n\ngo 1.21')"
publish example.com/fresh v1.1.0 "$(printf 'module example.com/fresh\n\ngo 1.21')" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# real_fixture <name> <require lines>
real_fixture() {
	DIR="${WORK}/$1"
	rm -rf "${DIR}"
	mkdir -p "${DIR}"
	printf 'module example.com/consumer\n\ngo 1.21\n\nrequire (\n%s\n)\n' "$2" >"${DIR}/go.mod"
	git -C "${DIR}" init -q
	git -C "${DIR}" add go.mod
	echo "${DIR}"
}

# The environment is the whole of the real go's configuration here: no network,
# no checksum database, no go.env from the host, a module cache of its own.
REAL_GO="${WORK}/real-go"
cat >"${REAL_GO}" <<EOF
#!/bin/sh
export GOENV=off GOTOOLCHAIN=local GOPROXY='file://${PROXY}' GOSUMDB=off \
	GOPRIVATE= GONOPROXY= GONOSUMDB= GOFLAGS=-mod=mod GOMODCACHE='${WORK}/modcache'
exec '${GO_BIN}' "\$@"
EOF
chmod +x "${REAL_GO}"

DIR=$(real_fixture parent-v0 "$(printf '\texample.com/perf v1.0.0\n\texample.com/lib v1.1.0')")
assert "an unpublished v0.0.0 an indirect module brings in does not fail the check" 0 "in 1 modules: go.mod" "${DIR}" "${REAL_GO}"

DIR=$(real_fixture parent-v0-behind "$(printf '\texample.com/perf v1.0.0\n\texample.com/lib v1.0.0')")
assert "a behind direct requirement beside an unpublished v0.0.0 is still found" 1 ".: example.com/lib v1.0.0, latest v1.1.0" "${DIR}" "${REAL_GO}"

# go's own Update.Time reaches the window: a release published now is reported.
DIR=$(real_fixture fresh-behind "$(printf '\texample.com/fresh v1.0.0')")
assert "go's publish time of a release made now keeps it under the window" 0 "example.com/fresh v1.0.0 behind v1.1.0, released 0h ago" "${DIR}" "${REAL_GO}"

DIR=$(fixture no-root)
git -C "${DIR}" rm -q --cached go.mod
assert "discovery without the root go.mod is a failure, not a pass" 1 "did not find the root go.mod" "${DIR}"

# ---------------------------------------------------------------------------

if [ "${FAILURES}" -ne 0 ]; then
	echo ""
	echo "${FAILURES} case(s) failed"
	exit 1
fi

echo ""
echo "all deps-check.sh cases passed"
