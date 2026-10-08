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
# check-trivy-db.sh — a trivy scan does not say how old the database it ruled
# with was, and will happily rule with an arbitrarily old one (go-qrdb#451).
#
# WHAT WAS MEASURED
#
# trivy decides whether to refresh by reading `NextUpdate` out of the database
# artifact it already has. That field is DATA INSIDE THE ARTIFACT, not a
# wall-clock test, so a database that declares a distant NextUpdate is used
# unconditionally. Measured on trivy v0.74.0 against this tree, with the
# network removed so no refresh was possible:
#
#   metadata.json UpdatedAt 2026-06-30, NextUpdate 2027-01-01
#     -> scanned, exit 0, and the output was byte-for-byte the shape of a scan
#        against a database from that morning. No warning, no age, nothing.
#
#   metadata.json UpdatedAt 2026-06-30, NextUpdate 2026-07-01
#     -> "Need to update DB", download fails, FATAL, non-zero.
#
# So trivy already fails closed on an EXPIRED database, and has no opinion at
# all about a database that merely claims not to be expired. The second case is
# the one this script exists for: a scan that certifies a tree using data older
# than the advisory it was meant to find, and reports success.
#
# That is the worst shape a gate can take. A false RED is noisy and
# self-correcting, because someone investigates it. A false GREEN is silent:
# the local run and the CI run disagree and only one of them is right, and
# nothing in either output says which.
#
# WHAT IT ASSERTS
#
# The age of the vulnerability database trivy WILL USE, against the wall clock,
# refusing to certify rather than certifying blindly. It is not a second
# expiry policy layered over trivy's: the default limit is 24 hours, which is
# exactly the horizon trivy's own NextUpdate uses (trivy-db is rebuilt upstream
# every six hours and its metadata sets NextUpdate = UpdatedAt + 24h). The
# check adds the comparison trivy omits; it does not invent a stricter rule.
#
# It reads `trivy version --format json` rather than $TRIVY_CACHE_DIR/db/
# metadata.json, because that is trivy answering for its own cache — it honours
# --cache-dir, TRIVY_CACHE_DIR and any config file, so the answer describes the
# database the NEXT INVOCATION in the same environment will load. Reading the
# path directly would be this script guessing at a location trivy is free to
# change.
#
# trivy-certified-db.sh runs it after each download source (the configured cache,
# then trivy's upstream repositories), so a stale cache falls back rather than
# failing the scan. `make trivy` calls that BETWEEN a `--download-db-only` refresh and a
# `--skip-db-update` scan, so the database this certifies and the database the
# scan rules with are the same one, and neither leg can move under the other.
#
# DISCOVERY HAS A FLOOR, as in check-tool-versions.sh and check-bundled-gosec.sh:
# an absent database, an absent timestamp, a timestamp that does not parse and
# a timestamp in the future are all FAILURES. Every one of them means the age is
# unknown, and an unknown age is not a young one. With no database at all trivy
# prints `{"Version":"0.74.0"}` and nothing else — a shape a check looking for
# "too old" would pass.
#
# The timestamp is converted with days-from-civil arithmetic in awk rather than
# with `date -d`, which is a GNU extension. A security gate that silently means
# something different on a BusyBox image is the class of defect this file is
# about.
#
# Usage: check-trivy-db.sh [max-age-hours] [trivy-binary]
#
#   max-age-hours    defaults to 24, the horizon trivy's own NextUpdate uses
#   trivy-binary     defaults to `trivy` on PATH
#
# Exit status: 0 when the database is present and younger than the limit,
# 1 otherwise.

set -eu

MAX_AGE_HOURS="${1:-24}"
BIN="${2:-trivy}"

case "$MAX_AGE_HOURS" in
'' | *[!0-9]* | 0)
	echo "FAIL: max-age-hours must be a positive whole number of hours, got '${MAX_AGE_HOURS}'." >&2
	exit 1
	;;
esac

command -v "$BIN" >/dev/null 2>&1 || {
	echo "FAIL: ${BIN} is not installed, so the age of the vulnerability database is unknown." >&2
	echo "      An unknown age is not a young one. The scanner is baked into the" >&2
	echo "      devcontainer at TRIVY_VERSION; \`make trivy\` refuses the same way (#299)." >&2
	exit 1
}

if ! report=$("$BIN" version --format json 2>&1) || [ -z "$report" ]; then
	echo "FAIL: \`${BIN} version --format json\` did not answer." >&2
	printf '%s\n' "$report" | sed 's/^/      /' >&2
	exit 1
fi

# Scoped to the VulnerabilityDB object before the timestamp is read. trivy also
# reports a JavaDB with an UpdatedAt of its own, and a pattern matching the
# field name alone would grade this gate on the age of a database it does not
# consult. The object has no nested objects, so [^}] is a sufficient boundary.
vulndb=$(printf '%s\n' "$report" |
	sed -n 's/.*"VulnerabilityDB"[[:space:]]*:[[:space:]]*{\([^}]*\)}.*/\1/p')

[ -n "$vulndb" ] || {
	echo "FAIL: ${BIN} reports no vulnerability database." >&2
	printf '%s\n' "$report" | sed 's/^/      /' >&2
	echo "      With an empty cache trivy prints only its own version, which is the" >&2
	echo "      shape above. There is nothing to scan with, so there is nothing this" >&2
	echo "      check can certify -- run \`trivy fs --download-db-only .\` first." >&2
	exit 1
}

updated=$(printf '%s\n' "$vulndb" |
	sed -n 's/.*"UpdatedAt"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

[ -n "$updated" ] || {
	echo "FAIL: ${BIN} reports a vulnerability database with no UpdatedAt." >&2
	printf '%s\n' "$report" | sed 's/^/      /' >&2
	echo "      The field this check reads has moved or been renamed. That is a" >&2
	echo "      failure and not a pass: the age is unknown either way." >&2
	exit 1
}

# RFC 3339 in UTC, which is what trivy writes. An offset form would be read as
# UTC and misdated by up to a day, so it is refused rather than guessed at.
epoch=$(printf '%s\n' "$updated" | awk '
	function days(y, m, d,   era, yoe, doy, doe) {
		if (m <= 2) y -= 1
		era = int((y >= 0 ? y : y - 399) / 400)
		yoe = y - era * 400
		doy = int((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5) + d - 1
		doe = yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy
		return era * 146097 + doe - 719468
	}
	/^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9](\.[0-9]+)?Z$/ {
		d = days(substr($0, 1, 4) + 0, substr($0, 6, 2) + 0, substr($0, 9, 2) + 0)
		t = (substr($0, 12, 2) + 0) * 3600 + (substr($0, 15, 2) + 0) * 60 + (substr($0, 18, 2) + 0)
		printf "%d\n", d * 86400 + t
	}
') || epoch=""

[ -n "$epoch" ] || {
	echo "FAIL: ${BIN} reports UpdatedAt '${updated}', which is not an RFC 3339 UTC timestamp." >&2
	echo "      Expected YYYY-MM-DDThh:mm:ss[.fraction]Z. An offset form would be read" >&2
	echo "      as UTC and misdated by up to a day, so it is refused rather than" >&2
	echo "      guessed at -- an unreadable age is not a young one." >&2
	exit 1
}

now=$(date -u +%s)
age=$((now - epoch))
limit=$((MAX_AGE_HOURS * 3600))

[ "$age" -ge 0 ] || {
	echo "FAIL: ${BIN}'s vulnerability database is dated ${updated}, which is in the future." >&2
	echo "      No database was built after now. Either the cache metadata is wrong or" >&2
	echo "      this host's clock is, and both make the age meaningless." >&2
	exit 1
}

if [ "$age" -gt "$limit" ]; then
	echo "FAIL: ${BIN}'s vulnerability database is $((age / 3600))h old (updated ${updated}); the limit is ${MAX_AGE_HOURS}h." >&2
	echo "" >&2
	echo "      A scan against this database would report a verdict, exit 0 on a clean" >&2
	echo "      tree, and say nothing about the advisories published since. That is a" >&2
	echo "      gate green for the wrong reason, so it fails closed instead (#451)." >&2
	echo "" >&2
	echo "      trivy refreshes by reading NextUpdate out of the artifact it already" >&2
	echo "      holds, so a database claiming not to be expired is never re-downloaded" >&2
	echo "      however old it is. Refresh it: trivy fs --download-db-only ." >&2
	exit 1
fi

echo "ok  ${BIN} vulnerability database is $((age / 3600))h old (updated ${updated}, limit ${MAX_AGE_HOURS}h)"
