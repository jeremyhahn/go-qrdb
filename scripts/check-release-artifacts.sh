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
# check-release-artifacts.sh proves every release artifact is what its name
# says (#916, docs/release/architecture.md "Release artifacts"). For every
# binary x platform x {plain, -fips} the file
#
#   <binary>-<version>-<goos>-<goarch>[-fips]
#
# must exist, and the build settings `go version -m` reads out of it must
# record the name's GOOS and GOARCH, CGO_ENABLED=0, -trimpath=true and every
# release tag in -tags. The -fips variant must record GOFIPS140=<module> (the
# setting Go writes when GOFIPS140 selects the FIPS 140-3 module at build
# time); the plain one must record none, or off -- a build with
# GOFIPS140=off records no GOFIPS140 setting at all. The ELF header's
# e_machine must be the name's goarch: 62 for amd64, 183 for arm64. A goarch
# with no known ELF machine, or a goos that does not produce ELF, is refused
# before anything is checked: the script refuses what it cannot check.
#
# With CRA_SUMS=1, SHA256SUMS in the artifact directory must list exactly the
# expected artifacts (none missing, none extra) and every listed checksum
# must verify with `sha256sum -c` from that directory.
#
# Every finding is one line on stderr naming the file and the mismatch; all
# files are checked before the script exits 1, so one run reports every
# finding. Success is one "OK: N artifacts" line on stdout.
#
# Environment (the Makefile's check-release-artifacts and release-cross pass
# all of them):
#   CRA_DIR          artifact directory (RELEASE_DIR)
#   CRA_VERSION      BUILD_VERSION in the artifact names
#   CRA_PLATFORMS    space-separated goos/goarch list
#   CRA_BINARIES     space-separated binary names (SERVER_BINARY CLIENT_BINARY)
#   CRA_SUMS         1 = also check SHA256SUMS, 0 = skip it
#   CRA_FIPS_MODULE  RELEASE_FIPS_MODULE, the GOFIPS140 of the -fips variant
#   CRA_TAGS         RELEASE_TAGS, space-separated
#   GO               go toolchain (default go)
#
# Regression test: scripts/check-release-artifacts_test.sh
# (make test-check-release-artifacts, in LINT_GATES).

set -eu

me=check-release-artifacts
dir=${CRA_DIR:?CRA_DIR is not set}
ver=${CRA_VERSION:?CRA_VERSION is not set}
platforms=${CRA_PLATFORMS:?CRA_PLATFORMS is not set}
binaries=${CRA_BINARIES:?CRA_BINARIES is not set}
sums=${CRA_SUMS:?CRA_SUMS is not set}
module=${CRA_FIPS_MODULE:?CRA_FIPS_MODULE is not set}
tags=${CRA_TAGS:?CRA_TAGS is not set}
GO=${GO:-go}

case "$sums" in 0 | 1) ;; *)
	echo "$me: CRA_SUMS is '$sums', want 0 or 1" >&2
	exit 2
	;;
esac
case "$module" in off)
	echo "$me: CRA_FIPS_MODULE is off; the -fips variant needs a FIPS module" >&2
	exit 2
	;;
esac

# elf_machine <goarch> prints the ELF e_machine the goarch builds for.
elf_machine() {
	case "$1" in
	amd64) echo 62 ;;
	arm64) echo 183 ;;
	*) return 1 ;;
	esac
}

# Refuse what cannot be checked, before checking anything.
for p in $platforms; do
	goos=${p%%/*}
	goarch=${p#*/}
	if [ "$goos/$goarch" != "$p" ] || [ -z "$goos" ] || [ -z "$goarch" ]; then
		echo "$me: platform '$p' is not <goos>/<goarch>" >&2
		exit 1
	fi
	if [ "$goos" != linux ]; then
		echo "$me: no ELF check for goos $goos (only linux artifacts are ELF)" >&2
		exit 1
	fi
	if ! elf_machine "$goarch" >/dev/null; then
		echo "$me: no ELF machine known for $goarch" >&2
		exit 1
	fi
done

findings=0
# finding <file> <mismatch> reports one finding.
finding() {
	printf '%s: %s: %s\n' "$me" "$1" "$2" >&2
	findings=$((findings + 1))
}

tab=$(printf '\t')

# check_one <name> <goos> <goarch> <fips 0|1> checks one artifact.
check_one() {
	name=$1 want_os=$2 want_arch=$3 is_fips=$4
	path="$dir/$name"
	if [ ! -f "$path" ]; then
		finding "$name" "missing"
		return 0
	fi

	# e_machine: two bytes little-endian at offset 18, after the ELF magic.
	magic=$(od -An -N4 -tx1 "$path" | tr -d ' \n')
	if [ "$magic" != 7f454c46 ]; then
		finding "$name" "not an ELF file (magic $magic)"
	else
		set -- $(od -An -j18 -N2 -tu1 "$path")
		machine=$(($1 + 256 * $2))
		want_machine=$(elf_machine "$want_arch")
		if [ "$machine" != "$want_machine" ]; then
			finding "$name" "ELF e_machine is $machine, want $want_machine ($want_arch)"
		fi
	fi

	# `go version -m` writes tab-separated records; build settings are
	# "<tab>build<tab><key>=<value>".
	if ! info=$("$GO" version -m "$path" 2>&1); then
		finding "$name" "go version -m cannot read its build info"
		return 0
	fi
	got_os='' got_arch='' got_cgo='' got_trim='' got_tags='' got_fips=''
	seen_os=0 seen_arch=0 seen_cgo=0 seen_trim=0 seen_tags=0 seen_fips=0
	while IFS="$tab" read -r kind kv rest; do
		[ "$kind" = build ] || continue
		key=${kv%%=*}
		val=${kv#*=}
		case "$key" in
		GOOS) got_os=$val seen_os=1 ;;
		GOARCH) got_arch=$val seen_arch=1 ;;
		CGO_ENABLED) got_cgo=$val seen_cgo=1 ;;
		-trimpath) got_trim=$val seen_trim=1 ;;
		-tags) got_tags=$val seen_tags=1 ;;
		GOFIPS140) got_fips=$val seen_fips=1 ;;
		esac
	done <<EOF
$info
EOF

	# setting <label> <seen> <got> <want> compares one recorded setting.
	setting() {
		if [ "$2" -eq 0 ]; then
			finding "$name" "$1 is not recorded, want $4"
		elif [ "$3" != "$4" ]; then
			finding "$name" "$1 is $3, want $4"
		fi
	}
	setting GOOS "$seen_os" "$got_os" "$want_os"
	setting GOARCH "$seen_arch" "$got_arch" "$want_arch"
	setting CGO_ENABLED "$seen_cgo" "$got_cgo" 0
	setting -trimpath "$seen_trim" "$got_trim" true

	if [ "$seen_tags" -eq 0 ]; then
		finding "$name" "-tags is not recorded, want $tags"
	else
		for t in $tags; do
			case ",$got_tags," in *",$t,"*) ;; *) finding "$name" "-tags lacks $t" ;; esac
		done
	fi

	if [ "$is_fips" -eq 1 ]; then
		setting GOFIPS140 "$seen_fips" "$got_fips" "$module"
	elif [ "$seen_fips" -eq 1 ] && [ "$got_fips" != off ]; then
		finding "$name" "GOFIPS140 is $got_fips, want off or none"
	fi
	return 0
}

# The expected artifact names, one per line, in build order.
expected=''
count=0
for p in $platforms; do
	goos=${p%%/*}
	goarch=${p#*/}
	for b in $binaries; do
		for v in '' -fips; do
			name="$b-$ver-$goos-$goarch$v"
			expected="$expected$name
"
			count=$((count + 1))
			if [ -n "$v" ]; then f=1; else f=0; fi
			check_one "$name" "$goos" "$goarch" "$f"
		done
	done
done

if [ "$sums" -eq 1 ]; then
	sumfile="$dir/SHA256SUMS"
	if [ ! -f "$sumfile" ]; then
		finding SHA256SUMS "missing"
	else
		listed=''
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			hash=${line%% *}
			lname=${line#"$hash"}
			lname=${lname# }
			lname=${lname# }
			lname=${lname#\*}
			listed="$listed$lname
"
			case "
$expected" in
			*"
$lname
"*) ;;
			*)
				finding SHA256SUMS "lists $lname, which is not a release artifact"
				continue
				;;
			esac
			[ -f "$dir/$lname" ] || continue
			if ! printf '%s  %s\n' "$hash" "$lname" |
				(cd "$dir" && sha256sum -c --status -) 2>/dev/null; then
				finding SHA256SUMS "$lname: checksum does not match"
			fi
		done <"$sumfile"
		while IFS= read -r name; do
			[ -n "$name" ] || continue
			case "
$listed" in
			*"
$name
"*) ;;
			*) finding SHA256SUMS "does not list $name" ;;
			esac
		done <<EOF
$expected
EOF
	fi
fi

if [ "$findings" -ne 0 ]; then
	echo "$me: FAILED: $findings findings" >&2
	exit 1
fi
if [ "$sums" -eq 1 ]; then
	echo "$me: OK: $count artifacts in $dir, SHA256SUMS lists exactly them and every checksum verifies"
else
	echo "$me: OK: $count artifacts in $dir"
fi
