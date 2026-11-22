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
# touched-features_test.sh — the suite behind `make test-touched-features`.
# Each case feeds paths to `scripts/touched-features.sh --files` on stdin and
# compares the printed lines, against the live features.mk table:
#
#   lease file          a test/integration file one feature maps prints that
#                       feature (leases)
#   shared file         a file two features map prints both (api-server,
#                       capabilities)
#   shared + docs       a docs-only path beside it adds nothing
#   docs only           docs/ and *.md paths print nothing
#   Makefile            prints ALL (it holds the dispatch)
#   package             a Go package path prints ALL (the table maps no
#                       package, and ruling 1171 forbids a second list)
#   features.mk         the table itself prints ALL
#   workflow            .github prints ALL
#   empty               no paths print nothing
#   bad args            one non-flag argument exits 2

set -u

here=$(cd "$(dirname "$0")" && pwd)
script="$here/touched-features.sh"
fails=0
nl='
'

check() {
	name=$1
	input=$2
	want=$3
	got=$(printf '%s' "$input" | "$script" --files 2>&1)
	rc=$?
	if [ "$rc" -eq 0 ] && [ "$got" = "$want" ]; then
		echo "PASS: $name"
	else
		echo "FAIL: $name: rc=$rc want [$want] got [$got]"
		fails=$((fails + 1))
	fi
}

check "lease file" "test/integration/lease_cli_test.go$nl" "leases"
check "shared file" "test/integration/cli_multiprotocol_test.go$nl" "api-server${nl}capabilities"
check "shared + docs" "test/integration/cli_multiprotocol_test.go${nl}docs/watch/architecture.md$nl" "api-server${nl}capabilities"
check "docs only" "docs/watch/architecture.md${nl}README.md$nl" ""
check "Makefile" "Makefile$nl" "ALL"
check "package" "pkg/lease/lease.go$nl" "ALL"
check "features.mk" "test/integration/features.mk$nl" "ALL"
check "workflow" ".github/workflows/ci.yml$nl" "ALL"
check "empty" "" ""

"$script" a </dev/null >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 2 ]; then
	echo "PASS: bad args"
else
	echo "FAIL: bad args: rc=$rc want 2"
	fails=$((fails + 1))
fi

if [ "$fails" -ne 0 ]; then
	echo "touched-features_test.sh: $fails case(s) failed"
	exit 1
fi
