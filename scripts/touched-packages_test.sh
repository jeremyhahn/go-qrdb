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
# touched-packages_test.sh — the suite behind `make test-touched-packages`.
# Each case feeds paths to `scripts/touched-packages.sh --files` on stdin,
# against the live tree ($GO overrides `go`):
#
#   changed package     pkg/ratelimit/token_bucket.go prints ./pkg/ratelimit
#   direct importer     ... and ./pkg/clientlimit, whose clientlimit.go
#                       imports pkg/ratelimit
#   non-importer        ... and not ./pkg/lease, which does not import it
#   testdata            pkg/secure/analyzer/testdata/x counts for
#                       ./pkg/secure/analyzer
#   docs only           docs/leases/user.md prints nothing
#   nested module       sdk/go/node/close_race_test.go (sdk/go has its own
#                       go.mod) prints nothing
#   go.mod              prints ALL
#   deleted package     a .go path whose directory holds no .go file prints ALL
#   moved file          base..HEAD renaming pkg/ratelimit/errors.go to
#                       pkg/lease/errors.go prints ./pkg/lease and also the
#                       source ./pkg/ratelimit with its importer
#                       ./pkg/clientlimit
#   bad args            no arguments exit 2

set -u

here=$(cd "$(dirname "$0")" && pwd)
script="$here/touched-packages.sh"
fails=0

pass() { echo "PASS: $1"; }
fail() {
	echo "FAIL: $1"
	fails=$((fails + 1))
}

has() { printf '%s\n' "$1" | grep -qxF "$2"; }

got=$(printf 'pkg/ratelimit/token_bucket.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" ./pkg/ratelimit; then pass "changed package"; else fail "changed package: rc=$rc got [$got]"; fi
if [ "$rc" -eq 0 ] && has "$got" ./pkg/clientlimit; then pass "direct importer"; else fail "direct importer: rc=$rc got [$got]"; fi
if [ "$rc" -eq 0 ] && ! has "$got" ./pkg/lease; then pass "non-importer"; else fail "non-importer: rc=$rc got [$got]"; fi

got=$(printf 'pkg/secure/analyzer/testdata/x\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" ./pkg/secure/analyzer; then pass "testdata"; else fail "testdata: rc=$rc got [$got]"; fi

got=$(printf 'docs/leases/user.md\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$got" ]; then pass "docs only"; else fail "docs only: rc=$rc got [$got]"; fi

got=$(printf 'sdk/go/node/close_race_test.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$got" ]; then pass "nested module"; else fail "nested module: rc=$rc got [$got]"; fi

got=$(printf 'go.mod\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ "$got" = ALL ]; then pass "go.mod"; else fail "go.mod: rc=$rc got [$got]"; fi

got=$(printf 'pkg/no_such_pkg/gone.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ "$got" = ALL ]; then pass "deleted package"; else fail "deleted package: rc=$rc got [$got]"; fi

# moved file: a base tree with pkg/lease/errors.go at pkg/ratelimit/errors.go,
# so the diff base..HEAD is a rename out of pkg/ratelimit.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
blob=$(git rev-parse HEAD:pkg/lease/errors.go)
GIT_INDEX_FILE="$tmp/index" git read-tree HEAD
GIT_INDEX_FILE="$tmp/index" git update-index --force-remove pkg/lease/errors.go
GIT_INDEX_FILE="$tmp/index" git update-index --add --cacheinfo "100644,$blob,pkg/ratelimit/errors.go"
tree=$(GIT_INDEX_FILE="$tmp/index" git write-tree)
base=$(git -c commit.gpgsign=false commit-tree -m "touched-packages_test base" "$tree")
got=$("$script" "$base" HEAD 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" ./pkg/lease; then pass "moved file destination"; else fail "moved file destination: rc=$rc got [$got]"; fi
if [ "$rc" -eq 0 ] && has "$got" ./pkg/ratelimit && has "$got" ./pkg/clientlimit; then pass "moved file source"; else fail "moved file source: rc=$rc got [$got]"; fi

"$script" </dev/null >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 2 ]; then pass "bad args"; else fail "bad args: rc=$rc want 2"; fi

if [ "$fails" -ne 0 ]; then
	echo "touched-packages_test.sh: $fails case(s) failed"
	exit 1
fi
