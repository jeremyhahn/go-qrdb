<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Release — User guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

## One-time setup (repository owner)

The Release workflow is only the **sole** release path once the forge enforces
it. Two settings do that, and both belong to the repository owner. The
repository cannot apply them to itself.

### 1. Tag protection

Protect these tag patterns so that **only the release bot** may create them:

| Pattern | Covers |
|---|---|
| `v*` | the root module |
| `sdk/go/v*` | the Go SDK |
| `sdk/go/gorm/conformance/v*` | the GORM conformance module |

On Gitea this is *Settings → Tags → Protected tags*, with the release bot as
the only allowed user for each pattern. Protected tags also cannot be deleted
or moved, which matches the rule that a published tag is immutable.

Check it: a direct `git push <remote> vX.Y.Z-alpha` from a workstation must be
**rejected by the server**. With the three patterns protected, the release
bot's `git push --atomic` of all three tags succeeds.

### 2. The release bot's credentials

Create a dedicated account for the release bot. Give it write access to this
repository, and register its SSH signing public key on the account. Then add:

| Name | Kind | Value |
|---|---|---|
| `RELEASE_BOT_TOKEN` | Actions secret | an access token for the bot with repository write scope. The workflow never persists it in the checkout; only the step that pushes receives it |
| `RELEASE_BOT_SIGNING_KEY` | Actions secret | the bot's private SSH signing key (OpenSSH format) |
| `RELEASE_BOT_NAME` | Actions variable | the tagger name |
| `RELEASE_BOT_EMAIL` | Actions variable | the tagger email |

If any of them is missing, the workflow stops at its first check and names the
missing item.

### 3. The public-releasability rules

`make check-public-releasable` keeps the private development fleet's values
out of every tracked file. Those values are the rules themselves, so they are
not in this repository either: the gate reads them at run time from an
untracked pattern file and fails, naming where it looked, when there is none.
It looks in this order:

1. the file named by `PUBLIC_RELEASE_PATTERNS_FILE`;
2. `../handbook/software/conventions/public-release-patterns`, in the handbook
   checkout beside this repository;
3. `$XDG_CONFIG_HOME/go-qrdb/public-release-patterns` (`~/.config/...` when
   `XDG_CONFIG_HOME` is unset).

The file is line-oriented. Blank lines and `#` lines are ignored; every other
line is `<class>[/i] <ERE>`, where the class is `lab-address`, `lab-host` or
`owner-path`, `/i` matches case-insensitively, and the ERE is POSIX extended.
Every class needs at least one rule. For CI, store the file's content as the
Actions secret `PUBLIC_RELEASE_PATTERNS`; the Lint job writes it to a temp file
for the one step that reads it.

| Name | Kind | Value |
|---|---|---|
| `PUBLIC_RELEASE_PATTERNS` | Actions secret | the pattern file's content |

The same gate refuses any tracked file that is a compiled executable, whatever
its content. It recognises them by their leading bytes: ELF, Mach-O (thin or
fat) and PE. A binary built in place, such as `go build` run inside a `cmd/`
directory, carries whatever the build machine wrote into it. Untrack it with
`git rm --cached <path>` and ignore its path. An executable that must be
tracked on purpose is listed by exact path, with the reason, in
`EXECUTABLE_EXEMPT` at the top of `scripts/check-public-releasable.sh`. The
list is empty. Other binaries, such as images and test fixtures, are scanned
like any other file.

## Cutting a release

1. **Prepare it as a change.** In one commit, set `VERSION` to the new version
   without the `v` (for example `0.22.0-alpha`), and move the `[Unreleased]`
   notes in `CHANGELOG.md` into a `## [0.22.0-alpha] - YYYY-MM-DD` section.
   Pre-1.0, breaking changes bump MINOR. Land the commit on `main` through
   review; it must be signed.
2. **Dispatch the Release workflow** on `main`. It reads `VERSION` and runs
   `make release-alpha`. That creates the three signed tags on the `VERSION`
   commit and pushes them together.
3. **Read the last step.** *Lockstep audit of the published tags* re-reads
   the tags the server holds. If it fails, the tags are already published and
   immutable. Repair the skew and release the next PATCH version; never move or
   delete a tag.

## When it refuses

Every refusal before the push leaves nothing behind, so fixing the cause and
dispatching again is the whole recovery. The most common refusals:

| Message mentions | Cause |
|---|---|
| `CHANGELOG.md does not carry` | the release section is missing or not the newest one |
| `tag ... already exists` | this version was already released; bump `VERSION` |
| `not in lockstep` | a published tag set is already skewed; repair that first |
| `does not resolve standalone` | a nested module's `go.mod` does not match its sources |
| `carries no SSH signature` | the `VERSION` commit on `main` is unsigned |
| `VERSION on HEAD names X, not Y` | the VERSION/CHANGELOG bump is not merged; merge it, then release |
| `is not what ... main holds` | the commit being released is not exactly the remote's `main` |
