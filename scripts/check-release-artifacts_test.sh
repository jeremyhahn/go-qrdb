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
# Regression tests for scripts/check-release-artifacts.sh (#916): it passes a
# correct release set, with and without SHA256SUMS, and names every file that
# is not what its name says -- the wrong GOARCH and ELF machine, a plain
# binary as the -fips one and the reverse, a build without -trimpath or with a
# release tag missing, a missing artifact, a SHA256SUMS with an extra line, a
# missing line or a wrong hash -- refuses a goarch it has no ELF machine for,
# and reports every finding rather than the first.
#
# The fixtures are faithful: a stdlib-only `package main` built by the real
# toolchain with the release flags (CGO_ENABLED=0, -trimpath, -tags, GOFIPS140
# off or the Makefile's RELEASE_FIPS_MODULE) for linux/amd64 and linux/arm64,
# under the release names. Each failure case breaks one property of a copy of
# that set. No network (GOPROXY=off), no repository module is built.
#
# Usage: check-release-artifacts_test.sh

set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
CHECK="$SCRIPT_DIR/check-release-artifacts.sh"
GO=${GO:-go}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

# mkvar <name> prints the Makefile's value of a variable.
mkvar() {
	make -s --no-print-directory -C "$ROOT" -f Makefile --eval "print-var:;@echo \$($1)" print-var
}
fips=$(mkvar RELEASE_FIPS_MODULE)
[ -n "$fips" ] && [ "$fips" != off ] || { echo "RELEASE_FIPS_MODULE is '$fips'" >&2; exit 1; }

ver=1.2.3-fixture
tags="fixa fixb"
bins="srv cli"
platforms="linux/amd64 linux/arm64"

# The fixture module: its own go.mod, stdlib only, outside the repository so
# no go.work or GOFLAGS of the caller reaches it.
mod="$work/mod"
mkdir -p "$mod"
printf 'package main\n\nfunc main() { println("release fixture") }\n' >"$mod/main.go"
export GOFLAGS='' GOPROXY=off GOWORK=off GOTOOLCHAIN=local GO111MODULE=on
(cd "$mod" && "$GO" mod init example.com/fixture >/dev/null 2>&1)

# build <out> <goarch> <gofips140> <go build flags>... builds the fixture.
build() {
	local out="$1" arch="$2" f="$3"
	shift 3
	(cd "$mod" && GOOS=linux GOARCH="$arch" GOFIPS140="$f" CGO_ENABLED=0 \
		"$GO" build "$@" -o "$out" .)
}

# The good set: every binary x platform x {plain, -fips}, plus SHA256SUMS
# written as release-artifacts writes it.
good="$work/good"
mkdir -p "$good"
for arch in amd64 arm64; do
	build "$work/plain-$arch" "$arch" off -trimpath -tags "$tags"
	build "$work/fips-$arch" "$arch" "$fips" -trimpath -tags "$tags"
	for b in $bins; do
		cp "$work/plain-$arch" "$good/$b-$ver-linux-$arch"
		cp "$work/fips-$arch" "$good/$b-$ver-linux-$arch-fips"
	done
done
build "$work/untrimmed-arm64" arm64 off -tags "$tags"
build "$work/onetag-arm64" arm64 off -trimpath -tags fixa
(cd "$good" && sha256sum srv-* cli-* >SHA256SUMS)

pass=0
fail=0
n=0

ok() {
	printf '  PASS  %s\n' "$1"
	pass=$((pass + 1))
}

bad() {
	printf '  FAIL  %s: %s\n' "$1" "$2"
	printf '%s\n' "$out" | while IFS= read -r l; do printf '          %s\n' "$l"; done
	fail=$((fail + 1))
}

# copyset sets d to a fresh copy of the good set's directory.
copyset() {
	n=$((n + 1))
	d="$work/case$n"
	cp -R "$good" "$d"
}

# check <dir> <sums> [platforms] runs the script; sets rc and out.
check() {
	rc=0
	CRA_DIR="$1" CRA_VERSION="$ver" CRA_PLATFORMS="${3:-$platforms}" CRA_BINARIES="$bins" \
		CRA_SUMS="$2" CRA_FIPS_MODULE="$fips" CRA_TAGS="$tags" GO="$GO" \
		"$CHECK" >"$work/out" 2>&1 || rc=$?
	out=$(<"$work/out")
}

# passes <label> <dir> <sums>: exit 0 and the OK line for all 8 artifacts.
passes() {
	check "$2" "$3"
	if [ "$rc" -ne 0 ]; then
		bad "$1" "exited $rc, want 0"
	else
		case "$out" in
		*"OK: 8 artifacts"*) ok "$1" ;;
		*) bad "$1" "no 'OK: 8 artifacts' line" ;;
		esac
	fi
}

# fails <label> <dir> <sums> <platforms> <want>...: exit 1, no OK line, and
# every <want> substring in the output.
fails() {
	local label="$1" dir="$2" sums="$3" plats="$4" w
	shift 4
	check "$dir" "$sums" "$plats"
	if [ "$rc" -ne 1 ]; then
		bad "$label" "exited $rc, want 1"
		return
	fi
	case "$out" in *"OK: "*)
		bad "$label" "printed an OK line"
		return
		;;
	esac
	for w in "$@"; do
		case "$out" in *"$w"*) ;; *)
			bad "$label" "no '$w' in the output"
			return
			;;
		esac
	done
	ok "$label"
}

a64="srv-$ver-linux-arm64"

passes "a correct set passes without SHA256SUMS" "$good" 0
passes "a correct set passes with SHA256SUMS" "$good" 1

copyset
cp "$d/srv-$ver-linux-amd64" "$d/$a64"
fails "an amd64 binary named -linux-arm64 fails on GOARCH and the ELF machine" "$d" 0 "$platforms" \
	"$a64: GOARCH is amd64, want arm64" "$a64: ELF e_machine is 62, want 183 (arm64)"

copyset
cp "$d/$a64" "$d/$a64-fips"
fails "a plain binary named -fips fails on the FIPS setting" "$d" 0 "$platforms" \
	"$a64-fips: GOFIPS140 is not recorded, want $fips"

copyset
cp "$d/$a64-fips" "$d/$a64"
fails "a FIPS binary under the plain name fails on the FIPS setting" "$d" 0 "$platforms" \
	"$a64: GOFIPS140 is $fips, want off or none"

copyset
cp "$work/untrimmed-arm64" "$d/$a64"
fails "a build without -trimpath fails" "$d" 0 "$platforms" \
	"$a64: -trimpath is not recorded, want true"

copyset
cp "$work/onetag-arm64" "$d/$a64"
fails "a build missing a release tag fails" "$d" 0 "$platforms" \
	"$a64: -tags lacks fixb"

copyset
rm "$d/cli-$ver-linux-amd64-fips"
fails "a missing artifact fails" "$d" 0 "$platforms" \
	"cli-$ver-linux-amd64-fips: missing"

copyset
cp "$d/$a64" "$d/srv-$ver-linux-riscv64"
(cd "$d" && sha256sum "srv-$ver-linux-riscv64" >>SHA256SUMS)
fails "SHA256SUMS with an extra line fails" "$d" 1 "$platforms" \
	"SHA256SUMS: lists srv-$ver-linux-riscv64, which is not a release artifact"

copyset
while IFS= read -r l; do
	case "$l" in *" cli-$ver-linux-arm64") ;; *) printf '%s\n' "$l" ;; esac
done <"$good/SHA256SUMS" >"$d/SHA256SUMS"
fails "SHA256SUMS with a missing line fails" "$d" 1 "$platforms" \
	"SHA256SUMS: does not list cli-$ver-linux-arm64"

copyset
while IFS= read -r l; do
	case "$l" in
	*" $a64") printf '%s  %s\n' "$(sha256sum <"$good/$a64-fips" | cut -d' ' -f1)" "$a64" ;;
	*) printf '%s\n' "$l" ;;
	esac
done <"$good/SHA256SUMS" >"$d/SHA256SUMS"
fails "SHA256SUMS with a wrong hash fails" "$d" 1 "$platforms" \
	"SHA256SUMS: $a64: checksum does not match"

fails "a goarch with no known ELF machine is refused" "$good" 0 "linux/amd64 linux/riscv64" \
	"check-release-artifacts: no ELF machine known for riscv64"

copyset
cp "$d/$a64" "$d/$a64-fips"
rm "$d/cli-$ver-linux-amd64"
fails "two bad files report two findings" "$d" 0 "$platforms" \
	"$a64-fips: GOFIPS140 is not recorded" "cli-$ver-linux-amd64: missing" \
	"check-release-artifacts: FAILED: 2 findings"

echo
echo "check-release-artifacts_test.sh: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
