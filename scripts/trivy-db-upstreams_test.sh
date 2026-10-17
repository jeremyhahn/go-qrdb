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
# Regression tests for trivy-db-upstreams.sh: the bridge first on a forge,
# marked http://, the documented pair alone on github.com, and a refusal of a
# server URL that is not a bare origin.
#
# Usage: trivy-db-upstreams_test.sh    (no network, no trivy)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SUBJECT="${SCRIPT_DIR}/trivy-db-upstreams.sh"
PAIR="mirror.gcr.io/aquasec/trivy-db:2 ghcr.io/aquasecurity/trivy-db:2"

pass=0
fail=0

# run <label> <want-exit> <want-output> [server-url]: an absent server-url runs
# the script with no argument, reading GITHUB_SERVER_URL.
run() {
	label="$1"
	want_rc="$2"
	want_out="$3"
	shift 3
	rc=0
	out=$("$SUBJECT" "$@" 2>/dev/null) || rc=$?
	if [ "$rc" != "$want_rc" ] || [ "$out" != "$want_out" ]; then
		printf '  FAIL  %-58s want rc=%s "%s", got rc=%s "%s"\n' "$label" "$want_rc" "$want_out" "$rc" "$out"
		fail=$((fail + 1))
		return
	fi
	printf '  PASS  %-58s (rc=%s)\n' "$label" "$rc"
	pass=$((pass + 1))
}

unset GITHUB_SERVER_URL
run "no server URL: documented pair" 0 "$PAIR"
run "github.com: documented pair" 0 "$PAIR" https://github.com
run "github.com with trailing slash: documented pair" 0 "$PAIR" https://github.com/

run "forge over http: bridge first" 0 "http://forge.example.com/agent-infrastructure/trivy-db:2 $PAIR" http://forge.example.com
run "forge over https: bridge still marked http://" 0 "http://forge.example.com/agent-infrastructure/trivy-db:2 $PAIR" https://forge.example.com
run "forge with port and trailing slash" 0 "http://forge.example.com:3000/agent-infrastructure/trivy-db:2 $PAIR" http://forge.example.com:3000/

GITHUB_SERVER_URL=http://forge.example.com
export GITHUB_SERVER_URL
run "server URL read from GITHUB_SERVER_URL" 0 "http://forge.example.com/agent-infrastructure/trivy-db:2 $PAIR"
run "argument wins over GITHUB_SERVER_URL" 0 "$PAIR" https://github.com
unset GITHUB_SERVER_URL

run "no scheme: refused" 2 "" forge.example.com
run "a path: refused" 2 "" http://forge.example.com/sub
run "whitespace: refused" 2 "" "http://forge.example.com extra"
run "scheme alone: refused" 2 "" http://

echo
echo "trivy-db-upstreams.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
