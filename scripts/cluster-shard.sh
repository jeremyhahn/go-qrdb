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
# cluster-shard.sh — split the cluster integration suite into legs.
#
#   cluster-shard.sh run I/N   print the `go test -run` expression for leg I of N
#   cluster-shard.sh check N   fail unless the N legs partition the suite
#
# The cluster suite (./test/integration/, `make integration-test-cluster`) is
# serial (-p 1, -parallel 1) and outgrew one package timeout on a loaded
# runner: 36.7 min quiet, 43.1 min at 2x CPU oversubscription, against a 45 min
# limit. CI runs it as N matrix legs instead, each with this expression.
#
# The legs come from `go test -list` on the package, under the same tags the
# run compiles with, so the list is exactly what the run would execute. It is
# sorted bytewise and dealt round-robin: leg I takes the tests at positions
# I, I+N, I+2N, ... A new test lands in some leg without anyone editing a
# list, and a renamed one moves with its name.
#
# Each leg's expression is anchored, ^(TestA|TestB)$, because go test matches
# -run unanchored: an unanchored TestFoo would also select TestFooBar, running
# it in two legs. `check` is the proof: it applies every leg's expression to
# the full list, the way go test would, and fails when a test is selected by
# no leg or by more than one. A floor on the list size stops an empty list
# (a package that compiled to no tests) from partitioning vacuously.
#
# Environment:
#   GO                  go command (default: go)
#   CLUSTER_SHARD_TAGS  build tags for `go test -list` (the Makefile passes
#                       INT_FEATURE_TAGS, the run's own tags)
#   CLUSTER_SHARD_PKG   package to list (default: ./test/integration/)
#   CLUSTER_SHARD_LIST  a file of test names, one per line, used INSTEAD of
#                       `go test -list` (the guard's own tests use it)
#   CLUSTER_SHARD_MIN   discovery floor (default: 250)

set -eu

GO=${GO:-go}
PKG=${CLUSTER_SHARD_PKG:-./test/integration/}
MIN=${CLUSTER_SHARD_MIN:-250}

die() {
    printf 'cluster-shard: %s\n' "$1" >&2
    exit 1
}

usage() {
    die "usage: $0 run I/N | check N"
}

# positive VALUE NAME — refuse anything but a positive decimal integer.
positive() {
    case $1 in
        '' | *[!0-9]* | 0*) die "$2 must be a positive integer, got '$1'" ;;
    esac
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
LIST="$TMP/list"

# list — the suite's test names, sorted bytewise, into $LIST. go test's exit
# status is checked on its own (POSIX sh has no pipefail): a package that
# does not compile must fail here, not yield an empty list.
list() {
    if [ -n "${CLUSTER_SHARD_LIST:-}" ]; then
        cp "$CLUSTER_SHARD_LIST" "$TMP/raw" || die "cannot read $CLUSTER_SHARD_LIST"
    elif ! "$GO" test -tags "${CLUSTER_SHARD_TAGS:-integration}" -list '^Test' "$PKG" > "$TMP/raw" 2>&1; then
        cat "$TMP/raw" >&2
        die "go test -list $PKG failed"
    fi
    grep -E '^Test[A-Za-z0-9_]*$' "$TMP/raw" | LC_ALL=C sort -u > "$LIST" || true
    count=$(wc -l < "$LIST")
    if [ "$count" -lt "$MIN" ]; then
        die "$PKG lists $count tests, under the floor of $MIN: refusing to partition a list that did not compile or was not found"
    fi
}

# deal I N — the tests of leg I of N, dealt round-robin from the sorted list.
deal() {
    awk -v i="$1" -v n="$2" '(NR - 1) % n == i - 1' "$LIST"
}

# expr I N — leg I's anchored -run expression.
expr_for() {
    names=$(deal "$1" "$2" | paste -sd '|' -)
    [ -n "$names" ] || die "leg $1 of $2 is empty: more legs than tests"
    printf '^(%s)$\n' "$names"
}

run() {
    case $1 in
        */*) i=${1%%/*}; n=${1#*/} ;;
        *) die "CLUSTER_SHARD must be I/N, got '$1'" ;;
    esac
    positive "$i" "I"
    positive "$n" "N"
    [ "$i" -le "$n" ] || die "leg $i is past the last leg, $n"
    list
    expr_for "$i" "$n"
}

check() {
    positive "$1" "N"
    n=$1
    list
    : > "$TMP/selected"
    i=1
    while [ "$i" -le "$n" ]; do
        re=$(expr_for "$i" "$n")
        grep -E "$re" "$LIST" >> "$TMP/selected" || true
        i=$((i + 1))
    done
    LC_ALL=C sort "$TMP/selected" > "$TMP/selected.sorted"
    dup=$(uniq -d "$TMP/selected.sorted")
    missing=$(LC_ALL=C comm -23 "$LIST" "$TMP/selected.sorted")
    if [ -n "$dup" ]; then
        printf 'cluster-shard: selected by more than one of %s legs:\n%s\n' "$n" "$dup" >&2
    fi
    if [ -n "$missing" ]; then
        printf 'cluster-shard: selected by none of %s legs:\n%s\n' "$n" "$missing" >&2
    fi
    if [ -n "$dup" ] || [ -n "$missing" ]; then
        exit 1
    fi
    printf 'cluster-shard: %s legs partition the %s tests of %s\n' "$n" "$(wc -l < "$LIST" | tr -d ' ')" "$PKG"
}

[ $# -eq 2 ] || usage
case $1 in
    run) run "$2" ;;
    check) check "$2" ;;
    *) usage ;;
esac
