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
# The latest-version check (handbook ci-standard.md §1, devops.md §9): every
# direct requirement of every tracked module is at its latest release, or the
# go.mod says why it is not.
#
#   usage: scripts/deps-check.sh        (from the repository root; `make deps-check`)
#
# A requirement is behind when `go list -m -u` reports an Update for it. A
# behind requirement passes only when its require line carries a `//` comment
# naming the reason, inline or on the comment lines directly above it; an
# undocumented pin is a defect. Indirect requirements are the direct ones'
# choice and are not checked, and a requirement replaced by a local directory
# has no release to be behind.
#
# The module set is every tracked go.mod, so a new module is checked the day it
# is committed. The root module missing from it means discovery failed: an empty
# loop must not report success. Every module is asked before failing, so one
# run names every undocumented pin. A module whose `go list` fails fails the
# check: the question was not answered.
#
# `go list -e` keeps one module's lookup failure from failing the whole listing,
# and the template turns a failure on a checked requirement back into a finding.
# Without -e, a module the check never inspects could fail it: a published
# module that requires `example.com/parent v0.0.0` and satisfies it with its own
# `replace => ../..` (go-quicraft/test/performance does) hands that v0.0.0 to
# every consumer, where no replace applies and the version does not exist. Where
# nothing else requires a real version, MVS selects v0.0.0 and `go list -m all`
# fails on it ("unknown revision v0.0.0"), though the consumer builds: its
# packages never resolve through that module.
#
# GO overrides the go binary (the Makefile passes $(GO); the test passes a
# fake).
set -euo pipefail

GO=${GO:-go}

# The template prints one line per checked requirement -- direct, and not
# replaced by a local directory -- that is behind or could not be looked up:
# `behind PATH VERSION LATEST` or `error PATH VERSION "MESSAGE"` (quoted, so a
# multi-line error stays one line). Every other
# module prints an empty line.
tmpl='{{if and (not .Main) (not .Indirect) (not (and .Replace (not .Replace.Version)))}}{{if .Error}}error {{.Path}} {{.Version}} {{printf "%q" .Error.Err}}{{else if .Update}}behind {{.Path}} {{.Version}} {{.Update.Version}}{{end}}{{end}}'

# documented GOMOD PATH VERSION succeeds when GOMOD's require line for PATH at
# VERSION carries a reason: an inline `//` comment, or a `//` comment line
# directly above it.
documented() {
  local gomod=$1 path=$2 version=$3 line prev_comment=0 fields
  while IFS= read -r line || [ -n "$line" ]; do
    read -r -a fields <<<"${line%%//*}"
    if [ "${fields[0]:-}" = require ]; then
      fields=("${fields[@]:1}")
    fi
    if [ "${fields[0]:-}" = "$path" ] && [ "${fields[1]:-}" = "$version" ]; then
      case $line in
        *//*) return 0 ;;
      esac
      [ "$prev_comment" -eq 1 ] && return 0
      return 1
    fi
    case ${line#"${line%%[![:space:]]*}"} in
      //*) prev_comment=1 ;;
      *) prev_comment=0 ;;
    esac
  done <"$gomod"
  return 1
}

mods=$(git ls-files -- 'go.mod' '*/go.mod' | tr '\n' ' ')
mods=${mods% }
case " $mods " in
  *" go.mod "*) ;;
  *) echo "ERROR: module discovery did not find the root go.mod (found: $mods)" >&2; exit 1 ;;
esac

bad=()
failed=()
unresolved=()
checked=0
for m in $mods; do
  d=$(dirname "$m")
  if ! out=$(cd "$d" && GOWORK=off "$GO" list -m -u -e -f "$tmpl" all); then
    failed+=("$d")
    continue
  fi
  checked=$((checked + 1))
  while read -r kind path version rest; do
    case ${kind:-} in
      behind)
        if documented "$m" "$path" "$version"; then
          echo "==> $d: $path $version held below $rest (reason in $m)"
        else
          bad+=("$d: $path $version, latest $rest")
        fi
        ;;
      error) unresolved+=("$d: $path $version: $rest") ;;
      '') ;;
      *) failed+=("$d (unexpected go list line: $kind $path $version $rest)") ;;
    esac
  done <<<"$out"
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo "ERROR: go list -m -u failed in: ${failed[*]} (go's error above names what it could not load)" >&2
fi
if [ "${#unresolved[@]}" -gt 0 ]; then
  echo "ERROR: direct requirements whose version or latest release could not be looked up:" >&2
  printf '  %s\n' "${unresolved[@]}" >&2
fi
if [ "${#bad[@]}" -gt 0 ]; then
  echo "ERROR: direct requirements behind their latest release with no reason in go.mod:" >&2
  printf '  %s\n' "${bad[@]}" >&2
  echo "Upgrade each, or put the reason it is held in a // comment on its require line (devops.md §9)." >&2
fi
if [ "${#failed[@]}" -gt 0 ] || [ "${#unresolved[@]}" -gt 0 ] || [ "${#bad[@]}" -gt 0 ]; then
  exit 1
fi
echo "==> every direct requirement is at its latest release or documented, in $checked modules: $mods"
