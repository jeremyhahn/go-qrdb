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

# Release — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

The release surface is `make` targets. `qrdb` and `qrdbd` have no release
commands.

## `make release-alpha VERSION=vX.Y.Z-alpha`

Cuts a release. The Release workflow is the caller once tag protection is on.
From a workstation the server then refuses the push, which is intended.

| Variable | Default | Meaning |
|---|---|---|
| `VERSION` | *(required)* | the release, `vX.Y.Z-alpha`. Must be given on the command line; a value inherited from the environment is refused |
| `RELEASE_REMOTE` | `dev` | the remote to publish to. A remote that points at github.com is refused |
| `AGENT_GIT` | the operator's signing wrapper, from `PATH` | the identity and signing wrapper every git write goes through. The workflow passes `scripts/release-bot-git.sh` |
| `REPO_NAME` | `go-qrdb` | the literal repository name handed to the wrapper |

If `VERSION` on the current `HEAD` already names the release, the recipe tags
`HEAD` and makes no commit. Otherwise it writes and signs the VERSION commit
first.

## `make check-tag-lockstep` / `make test-tag-lockstep`

Audits the tags this clone can see: every release version has a tag for each
lockstep module, and all of them are on one commit. `test-tag-lockstep` runs
the audit's own fixtures.

## `make test-release-alpha`

Runs the recipe against a local bare remote with a throwaway signing key. It
needs no network and no credentials.
