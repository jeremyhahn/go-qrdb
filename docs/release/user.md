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
