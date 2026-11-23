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
#   testdata go.mod     scripts/depscheck/testdata/parse/go.mod is a fixture,
#   fixture             not a nested module: prints ./scripts/depscheck
#   go.mod              prints ALL
#   deleted package     a .go path whose directory holds no .go file prints ALL
#   tagged importer     pkg/storage/storage.go prints ./pkg/fts WITH_QRDB_FTS=1:
#                       pkg/fts imports pkg/storage only under qrdb_fts
#   tagged package      pkg/fts/analyzer.go prints ./pkg/fts (the stub) and
#                       ./pkg/fts WITH_QRDB_FTS=1 (the real implementation)
#   tagged file         pkg/capabilities/capabilities_fts.go prints
#                       ./pkg/capabilities WITH_QRDB_FTS=1
#   untagged            pkg/ratelimit/token_bucket.go prints no WITH_ line
#   rung                every print-optional-test-tags tag is an element of a
#                       print-tag-sets line (a supported tag set)
#   moved file          base..HEAD renaming pkg/ratelimit/errors.go to
#                       pkg/lease/errors.go prints ./pkg/lease and also the
#                       source ./pkg/ratelimit with its importer
#                       ./pkg/clientlimit
#   optional listing    a go whose listing under qrdb_fts fails: exit 2,
#   fails               nothing on stdout, stderr names qrdb_fts
#   default listing     a go whose every package listing fails: exit 2,
#   fails               nothing on stdout
#   bad args            no arguments exit 2
#   go.sum untouched    the suite leaves go.sum byte for byte as it found it

set -u

here=$(cd "$(dirname "$0")" && pwd)
script="$here/touched-packages.sh"
gosum="$here/../go.sum"
sum_before=$(cksum <"$gosum")
fails=0

pass() { echo "PASS: $1"; }
fail() {
	echo "FAIL: $1"
	fails=$((fails + 1))
}

has() { printf '%s\n' "$1" | grep -qxF "$2"; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

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

got=$(printf 'scripts/depscheck/testdata/parse/go.mod\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" ./scripts/depscheck; then pass "testdata go.mod fixture"; else fail "testdata go.mod fixture: rc=$rc got [$got]"; fi

got=$(printf 'go.mod\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ "$got" = ALL ]; then pass "go.mod"; else fail "go.mod: rc=$rc got [$got]"; fi

got=$(printf 'pkg/no_such_pkg/gone.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ "$got" = ALL ]; then pass "deleted package"; else fail "deleted package: rc=$rc got [$got]"; fi

got=$(printf 'pkg/storage/storage.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" "./pkg/fts WITH_QRDB_FTS=1"; then pass "tagged importer"; else fail "tagged importer: rc=$rc got [$got]"; fi

got=$(printf 'pkg/fts/analyzer.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" ./pkg/fts && has "$got" "./pkg/fts WITH_QRDB_FTS=1"; then pass "tagged package"; else fail "tagged package: rc=$rc got [$got]"; fi

got=$(printf 'pkg/capabilities/capabilities_fts.go\n' | "$script" --files 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && has "$got" "./pkg/capabilities WITH_QRDB_FTS=1"; then pass "tagged file"; else fail "tagged file: rc=$rc got [$got]"; fi

got=$(printf 'pkg/ratelimit/token_bucket.go\n' | "$script" --files 2>&1)
rc=$?
case $got in
*WITH_*) fail "untagged: rc=$rc got [$got]" ;;
*) if [ "$rc" -eq 0 ] && [ -n "$got" ]; then pass "untagged"; else fail "untagged: rc=$rc got [$got]"; fi ;;
esac

optional=$(make -s --no-print-directory -C "$here/.." print-optional-test-tags 2>&1)
orc=$?
sets=$(make -s --no-print-directory -C "$here/.." print-tag-sets 2>&1)
src=$?
if [ "$orc" -ne 0 ] || [ "$src" -ne 0 ] || [ -z "$optional" ]; then
	fail "rung: print-optional-test-tags rc=$orc [$optional], print-tag-sets rc=$src"
else
	rung_ok=1
	while read -r tag flag; do
		[ -n "$tag" ] || continue
		case $flag in
		WITH_*=1) ;;
		*)
			rung_ok=0
			fail "rung: $tag has no WITH_<FLAG>=1 word [$flag]"
			;;
		esac
		if ! printf '%s\n' "$sets" | tr ', ' '\n\n' | grep -qxF "$tag"; then
			rung_ok=0
			fail "rung: $tag is in no print-tag-sets line"
		fi
	done <<EOF
$optional
EOF
	[ "$rung_ok" -eq 0 ] || pass "rung"
fi

# moved file: a base tree with pkg/lease/errors.go at pkg/ratelimit/errors.go,
# so the diff base..HEAD is a rename out of pkg/ratelimit.
blob=$(git rev-parse HEAD:pkg/lease/errors.go)
GIT_INDEX_FILE="$tmp/index" git read-tree HEAD
GIT_INDEX_FILE="$tmp/index" git update-index --force-remove pkg/lease/errors.go
GIT_INDEX_FILE="$tmp/index" git update-index --add --cacheinfo "100644,$blob,pkg/ratelimit/errors.go"
tree=$(GIT_INDEX_FILE="$tmp/index" git write-tree)
base=$(git -c commit.gpgsign=false -c user.name=touched-packages-test -c user.email=touched-packages-test@example.invalid commit-tree -m "touched-packages_test base" "$tree")
if [ -n "$base" ]; then
	got=$("$script" "$base" HEAD 2>&1)
	rc=$?
	if [ "$rc" -eq 0 ] && has "$got" ./pkg/lease; then pass "moved file destination"; else fail "moved file destination: rc=$rc got [$got]"; fi
	if [ "$rc" -eq 0 ] && has "$got" ./pkg/ratelimit && has "$got" ./pkg/clientlimit; then pass "moved file source"; else fail "moved file source: rc=$rc got [$got]"; fi
else
	fail "fixture commit: git commit-tree made no base commit, moved file cases not run"
fi

# Unreadable listings: fake go binaries that fail one `go list` and run the
# real go for everything else.
realgo=$(command -v "${GO:-go}")
printf '#!/bin/sh\nfor a in "$@"; do\n\tcase $a in\n\t*qrdb_fts*)\n\t\techo "fake go: listing under qrdb_fts fails" >&2\n\t\texit 1\n\t\t;;\n\tesac\ndone\nexec "%s" "$@"\n' "$realgo" >"$tmp/go-fails-tagged"
printf '#!/bin/sh\nif [ "$1" = list ] && [ "${2:-}" = -m ]; then exec "%s" "$@"; fi\nif [ "$1" = list ]; then\n\techo "fake go: package listing fails" >&2\n\texit 1\nfi\nexec "%s" "$@"\n' "$realgo" "$realgo" >"$tmp/go-fails-list"
chmod +x "$tmp/go-fails-tagged" "$tmp/go-fails-list"

got=$(printf 'pkg/storage/storage.go\n' | GO="$tmp/go-fails-tagged" "$script" --files 2>"$tmp/err")
rc=$?
if [ "$rc" -eq 2 ] && [ -z "$got" ] && grep -qF qrdb_fts "$tmp/err"; then pass "optional listing fails"; else fail "optional listing fails: rc=$rc want 2, stdout [$got] want empty, stderr [$(cat "$tmp/err")]"; fi

got=$(printf 'pkg/storage/storage.go\n' | GO="$tmp/go-fails-list" "$script" --files 2>"$tmp/err")
rc=$?
if [ "$rc" -eq 2 ] && [ -z "$got" ]; then pass "default listing fails"; else fail "default listing fails: rc=$rc want 2, stdout [$got] want empty, stderr [$(cat "$tmp/err")]"; fi

"$script" </dev/null >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 2 ]; then pass "bad args"; else fail "bad args: rc=$rc want 2"; fi

if [ "$(cksum <"$gosum")" = "$sum_before" ]; then pass "go.sum untouched"; else fail "go.sum untouched: the suite changed go.sum"; fi

if [ "$fails" -ne 0 ]; then
	echo "touched-packages_test.sh: $fails case(s) failed"
	exit 1
fi
