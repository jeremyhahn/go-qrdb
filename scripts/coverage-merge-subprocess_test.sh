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
# Regression tests for coverage-merge-subprocess.sh: usage validation, that
# --list's discovery actually finds the known subprocess sites (not a fixed
# list any more -- round 2 review found the hard-coded list had already missed
# two real ones), that the discovery floor guard is wired up, and that --run
# really does land a CHILD process's execution in the merged profile.
#
# That last check drives --run end to end against a throwaway fixture module,
# not this repository: running this repository's subprocess packages under
# -race is `make coverage`'s own job and too slow to repeat here on every
# change to this script, but a one-package fixture builds in seconds and is
# the only way to assert the property the script exists for (round 4 review:
# without it, every check here stayed green while children that did not
# forward -test.gocoverdir contributed nothing to the gate).
set -eu

GO="${GO:-go}"

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/coverage-merge-subprocess.sh"

fails=0
note() { printf '  %s\n' "$@"; }
fail() { fails=$((fails + 1)); printf 'FAIL: %s\n' "$@" >&2; }
pass() { note "ok: $*"; }

# No arguments: must fail with a usage message, not silently do nothing.
if out=$("$SCRIPT" 2>&1); then
	fail "no arguments exited 0; expected a usage error. output: $out"
else
	case "$out" in
	*usage*) pass "no arguments -> usage error" ;;
	*) fail "no arguments: expected a usage message, got: $out" ;;
	esac
fi

# An unknown flag must fail the same way.
if out=$("$SCRIPT" --bogus 2>&1); then
	fail "--bogus exited 0; expected a usage error. output: $out"
else
	case "$out" in
	*usage*) pass "--bogus -> usage error" ;;
	*) fail "--bogus: expected a usage message, got: $out" ;;
	esac
fi

# --run with no path must fail closed rather than writing to an empty string.
if out=$("$SCRIPT" --run 2>&1); then
	fail "--run with no path exited 0; expected an error. output: $out"
else
	pass "--run with no path -> error"
fi

cd "$SCRIPT_DIR/.."

# --list discovers packages rather than naming a fixed set (round 2 review:
# the fixed set had already missed pkg/logger and pkg/seal/encfs). What this
# test pins is that discovery actually FINDS every subprocess site known to
# exist in the tree right now -- a false negative here means the grep pattern
# stopped matching real code, which is exactly the silent-omission failure
# the previous, hand-written list had.
#
# This list names only the subprocess sites that exist as of THIS commit
# (round 4 review: an earlier version named pkg/secret and pkg/ceremony too,
# which this commit's own tree does not have yet -- those sites are added by
# the security commit that follows, which is where their assertions belong.
# A coverage-mechanism commit whose own test depends on a LATER commit's
# fixtures is not independently green, which defeats the point of landing it
# first).
got=$("$SCRIPT" --list)
for want_pkg in \
	"github.com/jeremyhahn/go-qrdb/cmd/qrdbd" \
	"github.com/jeremyhahn/go-qrdb/pkg/secure" \
	"github.com/jeremyhahn/go-qrdb/pkg/logger" \
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"; do
	if printf '%s\n' "$got" | grep -qx "$want_pkg"; then
		pass "--list discovers $want_pkg"
	else
		fail "--list did not discover $want_pkg; discovery may have regressed. got:
$got"
	fi
done

# Discovery must be deterministic: the same tree run twice gives the same
# list, in the same order, so the Makefile's bulk-exclusion pattern and this
# script's own --run agree on what "the subprocess packages" means within one
# `make coverage` invocation.
got2=$("$SCRIPT" --list)
if [ "$got" = "$got2" ]; then
	pass "--list is deterministic across repeated invocations"
else
	fail "--list returned different output on a second call with no tree change.
first:
$got
second:
$got2"
fi

# Every package --list names must actually exist in the module. A dangling
# entry here would mean the discovery grep matched a file whose directory
# `go list` cannot resolve.
missing=""
for pkg in $got; do
	if ! go list "$pkg" >/dev/null 2>&1; then
		missing="$missing $pkg"
	fi
done
if [ -n "$missing" ]; then
	fail "--list names package(s) that do not resolve via 'go list':$missing"
else
	pass "every --list package resolves via 'go list'"
fi

# The discovery floor must actually gate --run: set it above what discovery
# can possibly find and confirm the script refuses rather than silently
# proceeding with too few packages. This is the guard against the pattern
# someday matching nothing (or almost nothing) without anyone noticing.
if out=$(COVERAGE_MERGE_DISCOVERY_FLOOR=999999 "$SCRIPT" --run /dev/null 2>&1); then
	fail "--run with an unreachable discovery floor exited 0; expected the floor guard to refuse. output: $out"
else
	case "$out" in
	*floor*) pass "an unreachable discovery floor refuses --run" ;;
	*) fail "expected the floor-guard message, got: $out" ;;
	esac
fi

# --run end to end: a CHILD process's execution must reach the merged
# profile. The fixture module has one function reachable only from the
# parent, one reachable only from a child that forwards -test.gocoverdir the
# way every subprocess test in this tree does, and one reachable only from a
# child that merely inherits GOCOVERDIR through its environment.
#
# The forwarded child's function must be counted: that is the property the
# gate depends on. The inherit-only child's function must NOT be -- go's test
# binary writes coverage into, and then removes, its own temporary directory
# when -test.gocoverdir is absent -- which is the measured reason every
# subprocess site here forwards the flag explicitly, and it also proves this
# assertion can fail: were the script to stop capturing child coverage at
# all, the forwarded case would read 0 exactly like the inherited one.
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT INT TERM
mkdir -p "$fixture/pkg/fixture/testdata" "$fixture/cmd"
: >"$fixture/pkg/fixture/testdata/marker"
cat >"$fixture/go.mod" <<'GOMOD'
module example.com/covfixture

go 1.21
GOMOD
cat >"$fixture/pkg/fixture/fixture.go" <<'GOSRC'
package fixture

func ParentOnly() int { return 1 }

func ChildForwarded() int { return 2 }

func ChildInheritedOnly() int { return 3 }
GOSRC
cat >"$fixture/pkg/fixture/fixture_test.go" <<'GOSRC'
package fixture

import (
	"os"
	"os/exec"
	"testing"
)

const childEnv = "COVFIXTURE_CHILD"

func runChild(t *testing.T, mode string, args ...string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), childEnv+"="+mode)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child %s: %v\n%s", mode, err, out)
	}
}

func TestForwarded(t *testing.T) {
	if os.Getenv(childEnv) == "forwarded" {
		ChildForwarded()
		return
	}
	ParentOnly()
	args := []string{"-test.run=^TestForwarded$"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	runChild(t, "forwarded", args...)
}

// TestRunsFromPackageDir fails unless the binary runs from its package's
// directory, as `go test` runs it: relative testdata/ paths depend on it.
func TestRunsFromPackageDir(t *testing.T) {
	if _, err := os.Stat("testdata/marker"); err != nil {
		t.Fatalf("testdata/marker not found relative to the working directory: %v", err)
	}
}

func TestInheritedOnly(t *testing.T) {
	if os.Getenv(childEnv) == "inherited" {
		ChildInheritedOnly()
		return
	}
	runChild(t, "inherited", "-test.run=^TestInheritedOnly$")
}
GOSRC

# count_for <line>: the merged profile's execution count for the one block on
# fixture.go line <line>, or "missing" if the profile has no such block.
count_for() {
	awk -v want="fixture.go:$1." 'index($1, want) { print $3; found = 1 } END { if (!found) print "missing" }' "$caller/merged.out"
}

# The script is driven from a DIFFERENT directory than the module it covers,
# with COVERAGE_MERGE_ROOT naming the module and a RELATIVE output path: --list
# must discover under the root, and the profile must land relative to the
# caller, not the root (the cwd defect: both used to depend on where the
# script happened to be invoked from).
caller="$fixture/caller"
mkdir -p "$caller"
listed=$(cd "$caller" && GOWORK=off GOFLAGS= COVERAGE_MERGE_ROOT="$fixture" GO="$GO" "$SCRIPT" --list 2>&1) || true
if [ "$listed" = "example.com/covfixture/pkg/fixture" ]; then
	pass "--list discovers under COVERAGE_MERGE_ROOT, not the caller's directory"
else
	fail "--list from another directory with COVERAGE_MERGE_ROOT set returned: $listed"
fi

if out=$(cd "$caller" && GOWORK=off GOFLAGS= COVERAGE_MERGE_ROOT="$fixture" COVERAGE_MERGE_DISCOVERY_FLOOR=1 GO="$GO" \
	"$SCRIPT" --run merged.out 2>&1); then
	if [ -s "$caller/merged.out" ] && [ ! -e "$fixture/merged.out" ]; then
		pass "--run writes a relative output path relative to the caller"
	else
		fail "--run with a relative output path did not write $caller/merged.out (or wrote under the root instead)"
	fi
	parent=$(count_for 3)
	forwarded=$(count_for 5)
	inherited=$(count_for 7)
	if [ "$parent" = "missing" ] || [ "$parent" -eq 0 ]; then
		fail "--run: the parent-only function reads '$parent' in the merged profile, want > 0"
	else
		pass "--run records the parent process's execution"
	fi
	if [ "$forwarded" = "missing" ] || [ "$forwarded" -eq 0 ]; then
		fail "--run: a child forwarding -test.gocoverdir reads '$forwarded' in the merged profile, want > 0 -- child coverage is not reaching the gate"
	else
		pass "--run records a child that forwards -test.gocoverdir"
	fi
	if [ "$inherited" = "missing" ] || [ "$inherited" -ne 0 ]; then
		fail "--run: an inherit-only child reads '$inherited', want 0 -- the measured reason subprocess sites forward the flag no longer holds; revisit the script header"
	else
		pass "--run confirms an inherit-only child is invisible, so forwarding stays required"
	fi
else
	fail "--run against the fixture module failed: $out"
fi

if [ "$fails" -gt 0 ]; then
	echo "coverage-merge-subprocess_test: $fails failure(s)" >&2
	exit 1
fi
echo "coverage-merge-subprocess_test: all checks passed"
