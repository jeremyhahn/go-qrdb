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
# secure-analyzer.sh: build pkg/secure/analyzer and run it over every Go module in the tree.
set -eu

ROOT="${SECURE_ANALYZER_ROOT:-.}"
TOOL_PKG=./pkg/secure/analyzer/cmd/secure-analyzer

GO="${GO:-go}"
GOLIST="${GOLIST:-$GO list}"

die() { printf '%s\n' "$@" >&2; exit 1; }

# build_tool compiles the singlechecker to an ABSOLUTE path.
#
# Absolute because vet-modules.sh cd's into each module before invoking GOVET,
# so a relative -vettool path resolves against the wrong directory for seven of
# the eight modules -- and `go vet` reports that as a tool it could not start,
# not as a clean sweep, which is at least loud. It is still worth not relying
# on the difference.
build_tool() {
	[ -n "${1:-}" ] || die "secure-analyzer: --build needs an output path"
	case "$1" in
	/*) _out="$1" ;;
	*) die "secure-analyzer: output path must be absolute, got [$1]" ;;
	esac
	(cd "$ROOT" && $GO build -o "$_out" "$TOOL_PKG") || \
		die "secure-analyzer: the analyzer itself does not build." \
		    "  Nothing downstream of this is a measurement of the tree."
	[ -x "$_out" ] || die "secure-analyzer: build reported success but produced no executable at $_out"
}

case "${1:---sweep}" in
--build)
	build_tool "${2:-}" ;;
--sweep)
	TAG_SETS="${2:-${TAG_SETS:-}}"
	GORM_TAGS="${3:-${GORM_TAGS:-}}"
	[ -n "$TAG_SETS" ] || die "secure-analyzer: TAG_SETS is empty; make must pass it"
	[ -n "$GORM_TAGS" ] || die "secure-analyzer: GORM_TAGS is empty; make must pass it"

	tool=$(mktemp -d)/secure-analyzer
	trap 'rm -rf "$(dirname "$tool")"' EXIT INT TERM
	build_tool "$tool"

	echo "secure-analyzer: securecheck rules 1-3 and securecopy, every module, every declared combination"
	GOVET="$GO vet -vettool=$tool" GOLIST="$GOLIST" VET_MODULES_ROOT="$ROOT" \
		"$ROOT/scripts/vet-modules.sh" --vet "$TAG_SETS" "$GORM_TAGS" || \
		die "secure-analyzer: securecheck reported findings, or a module was not swept." \
		    "  Rules 1 and 2 are absolute. Rule 3 is scoped, and the four clauses" \
		    "  plus the argument for each are in pkg/secure/analyzer/scope.go --" \
		    "  read it before assuming a finding is a false positive;" \
		    "  securecopy's sources, sinks and limits are in pkg/secure/analyzer/copyout.go."
	;;
*)
	die "usage: secure-analyzer.sh [--build <abs-out> | --sweep <TAG_SETS> <GORM_TAGS>]" ;;
esac
