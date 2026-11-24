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
# release-smoke.sh runs every release artifact of ONE platform natively
# (#916, docs/release/engineering.md "linux/arm64 before the RC"). For every
# binary x {plain, -fips} the file
#
#   <binary>-<version>-<goos>-<goarch>[-fips]
#
# must exist and be executable, and `<file> version` must exit 0 and print
# v<version> on stdout. The plain variant runs with GODEBUG unset; the -fips
# variant runs with GODEBUG=fips140=only, so the Go FIPS 140-3 module's
# integrity self-test runs at init on the platform that ships it.
#
# check-release-artifacts reads headers and build info and runs nothing;
# make integration-test runs binaries the harness builds from source with
# cgo, so this is the one procedure that executes what ships.
#
# The platform must be the host's ($GO env GOHOSTOS/GOHOSTARCH): a foreign
# binary cannot run natively, so another platform is refused (exit 2) before
# anything runs, as is a platform that is not <goos>/<goarch>.
#
# Every finding is one line on stderr naming the file; all artifacts are run
# before the script exits 1. Success is one "OK <platform>" line on stdout.
#
# Environment (the Makefile's release-smoke passes all of them):
#   RS_DIR       artifact directory (RELEASE_DIR)
#   RS_VERSION   BUILD_VERSION in the artifact names
#   RS_PLATFORM  goos/goarch of the artifacts, the host's
#   RS_BINARIES  space-separated binary names (SERVER_BINARY CLIENT_BINARY)
#   GO           go toolchain (default go), asked for the host platform
#
# Regression test: scripts/release-smoke_test.sh
# (make test-release-smoke, in LINT_GATES).

set -euo pipefail

me=release-smoke
dir=${RS_DIR:?RS_DIR is not set}
ver=${RS_VERSION:?RS_VERSION is not set}
platform=${RS_PLATFORM-}
binaries=${RS_BINARIES:?RS_BINARIES is not set}
GO=${GO:-go}

goos=${platform%%/*}
goarch=${platform#*/}
if [ "$goos/$goarch" != "$platform" ] || [ -z "$goos" ] || [ -z "$goarch" ] ||
	[ "${goarch#*/}" != "$goarch" ]; then
	echo "$me: set PLATFORM=<goos>/<goarch>, e.g. make release-smoke PLATFORM=linux/arm64 (got '$platform')" >&2
	exit 2
fi
host="$("$GO" env GOHOSTOS)/$("$GO" env GOHOSTARCH)"
if [ "$platform" != "$host" ]; then
	echo "$me: $platform artifacts cannot run natively on this $host host; run it on a $platform host" >&2
	exit 2
fi

# Both binaries print "<name> v<version>" (cmd/qrdbd/main.go, cmd/qrdb/version.go).
want="v$ver"
findings=0
count=0
out=$(mktemp)
err=$(mktemp)
trap 'rm -f "$out" "$err"' EXIT

# finding <file> <mismatch> reports one finding.
finding() {
	printf '%s: %s: %s\n' "$me" "$1" "$2" >&2
	findings=$((findings + 1))
}

for b in $binaries; do
	for v in '' -fips; do
		name="$b-$ver-$goos-$goarch$v"
		path="$dir/$name"
		if [ ! -f "$path" ]; then
			finding "$name" "missing"
			continue
		fi
		if [ ! -x "$path" ]; then
			finding "$name" "not executable"
			continue
		fi
		rc=0
		if [ -n "$v" ]; then
			GODEBUG=fips140=only "$path" version >"$out" 2>"$err" || rc=$?
		else
			env -u GODEBUG "$path" version >"$out" 2>"$err" || rc=$?
		fi
		count=$((count + 1))
		if [ "$rc" -ne 0 ]; then
			finding "$name" "version exited $rc; last output: $(cat "$out" "$err" | tail -n 5 | tr '\n' '|')"
			continue
		fi
		case "$(cat "$out")" in
		*"$want"*) ;;
		*) finding "$name" "version printed '$(tail -n 1 "$out")', want $want" ;;
		esac
	done
done

if [ "$findings" -ne 0 ]; then
	echo "$me: FAILED $platform: $findings findings" >&2
	exit 1
fi
echo "$me: OK $platform: $count artifacts ran"
