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
# Fails unless a trivy binary reports exactly the pinned version.

set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: check-trivy-version.sh <trivy-binary> <pin>" >&2
	exit 2
fi
bin="$1"
pin="$2"

if ! report="$("$bin" --version 2>&1)"; then
	echo "ERROR: '$bin --version' failed:" >&2
	printf '%s\n' "$report" >&2
	exit 1
fi

got=""
count=0
nl='
'
IFS_SAVE="$IFS"
IFS="$nl"
set -f # split on newlines only, never glob the report
for line in $report; do
	case "$line" in
	"Version: "*)
		got="${line#Version: }"
		count=$((count + 1))
		;;
	esac
done
IFS="$IFS_SAVE"

if [ "$count" -ne 1 ]; then
	echo "ERROR: '$bin --version' printed ${count} 'Version: ' lines, want 1:" >&2
	printf '%s\n' "$report" >&2
	exit 1
fi
if [ "$got" != "${pin#v}" ]; then
	echo "ERROR: trivy reports version '${got}', want exactly '${pin#v}'" >&2
	exit 1
fi
echo "ok: trivy ${got}"
