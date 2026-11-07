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
# Regression tests for ci-registry.sh's push.
#
# One blob upload answered with a registry 500 failed a main run, because the
# push made a single attempt while the pull beside it retries (#1344). The push
# now retries on the pull's schedule: `attempts` tries, CI_REGISTRY_BACKOFF
# seconds times the attempt number between them. These cases run the script on
# a stub docker that fails the first FAKE_PUSH_FAILURES pushes and counts every
# push it is asked for, so a retry that never happens, or one too many, shows.
#
# Usage: ci-registry_test.sh    (no network, no docker, no Go build)

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SUBJECT="${SCRIPT_DIR}/ci-registry.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/bin"

# The stub docker: login and tag succeed, push fails while fewer than
# FAKE_PUSH_FAILURES pushes have been made, and image inspect prints the
# repository digest a successful push leaves behind.
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
case $1 in
login) cat >/dev/null; exit 0 ;;
tag) exit 0 ;;
push)
	n=$(($(cat "$FAKE_PUSH_COUNT" 2>/dev/null || echo 0) + 1))
	echo "$n" >"$FAKE_PUSH_COUNT"
	if [ "$n" -le "$FAKE_PUSH_FAILURES" ]; then
		echo "received unexpected HTTP status: 500 Internal Server Error" >&2
		exit 1
	fi
	exit 0
	;;
image)
	target=${!#}
	printf '%s@sha256:%064d\n' "${target%:*}" 0
	exit 0
	;;
esac
echo "unexpected docker invocation: $*" >&2
exit 1
EOF
chmod +x "$work/bin/docker"

PATH="$work/bin:$PATH"
GITHUB_SERVER_URL=https://registry.example.com
RELEASE_REGISTRY=registry.example.com
RELEASE_REGISTRY_USER=releaser
RELEASE_REGISTRY_TOKEN=not-a-secret
GITHUB_REPOSITORY_OWNER=owner
CI_REGISTRY_BACKOFF=0
FAKE_PUSH_COUNT="$work/pushes"
export PATH GITHUB_SERVER_URL RELEASE_REGISTRY RELEASE_REGISTRY_USER \
	RELEASE_REGISTRY_TOKEN GITHUB_REPOSITORY_OWNER CI_REGISTRY_BACKOFF FAKE_PUSH_COUNT

pass=0
fail=0

# expect <accept|reject> <label> <failures> <want-pushes> <want-substring>
expect() {
	local want=$1 label=$2 failures=$3 want_pushes=$4 want_text=$5 got pushes
	rm -f "$FAKE_PUSH_COUNT"
	if FAKE_PUSH_FAILURES=$failures "$SUBJECT" push qrdb-toolchain:0123456789abcdef qrdb-toolchain main >"$work/out" 2>&1; then
		got=accept
	else
		got=reject
	fi
	pushes=$(cat "$FAKE_PUSH_COUNT" 2>/dev/null || echo 0)
	if [ "$got" != "$want" ] || [ "$pushes" != "$want_pushes" ] || ! grep -q -- "$want_text" "$work/out"; then
		printf '  FAIL  %-62s want %s after %s pushes mentioning %s; got %s after %s\n' \
			"$label" "$want" "$want_pushes" "$want_text" "$got" "$pushes"
		while IFS= read -r line; do printf '          %s\n' "$line"; done <"$work/out"
		fail=$((fail + 1))
		return
	fi
	printf '  PASS  %-62s (%s after %s pushes)\n' "$label" "$got" "$pushes"
	pass=$((pass + 1))
}

digest="registry.example.com/releaser/qrdb-toolchain@sha256:$(printf '%064d' 0)"

echo "== a push the registry accepts at once =="
expect accept "first push succeeds" 0 1 "$digest"

echo "== a transient registry failure is retried =="
expect accept "first push fails, second succeeds" 1 2 "$digest"
expect accept "the retry is logged" 1 2 "push of registry.example.com/releaser/qrdb-toolchain:0123456789abcdef-main failed (attempt 1 of 3); retrying"

echo "== a registry that refuses every attempt still fails the job =="
expect reject "every push fails: exactly 3 attempts, then refusal" 99 3 "refused the push"

echo
echo "ci-registry.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
