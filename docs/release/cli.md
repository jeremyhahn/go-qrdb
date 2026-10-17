# Release — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-16

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

The release surface is `make` targets. `qrdb` and `qrdbd` have no release
commands.

## `make release-alpha VERSION=vX.Y.Z-alpha`

Cuts a release. **Run by hand**, by the project agent, any time CI is green
on `main` (release-versioning.md §1a). There is no workflow to dispatch and
no bot that performs this step instead.

| Variable | Default | Meaning |
|---|---|---|
| `VERSION` | *(required)* | the release, `vX.Y.Z-alpha`. Must be given on the command line; a value inherited from the environment is refused |
| `RELEASE_REMOTE` | `dev` | the remote to publish to. A remote that points at github.com is refused |
| `AGENT_GIT` | the operator's signing wrapper, from `PATH` | the identity and signing wrapper every git write goes through -- the same one every other commit in this repository already carries |
| `REPO_NAME` | `go-qrdb` | the literal repository name handed to the wrapper |

`VERSION` is authoritative: the recipe tags `HEAD` and never writes VERSION
or commits. It refuses unless `VERSION` on `HEAD` already names the requested
release ("VERSION on HEAD names X, not Y; merge the VERSION/CHANGELOG bump
first"), `HEAD` is SSH-signed, and `HEAD` is exactly what the remote's `main`
holds. It pushes only the tags.

Once the push lands, `.github/workflows/release.yml` fires on the tag and
validates it (api.md), with no further action from the operator.

## `make check-tag-lockstep` / `make test-tag-lockstep`

Audits the tags this clone can see: every release version has a tag for each
lockstep module, and all of them are on one commit. `test-tag-lockstep` runs
the audit's own fixtures.

## `make check-version-reconciled [RELEASE_TAG=vX.Y.Z-alpha]` / `make test-version-reconciled`

Reconciles `VERSION`, `CHANGELOG.md`'s newest released section and, when
`RELEASE_TAG` is given, the tag itself. The same check `release-alpha`,
`release.yml`'s `validate` job, and the Release Lockstep workflow all run.

## `make release-artifacts` / `make release-cross PLATFORM=<goos>/<goarch>`

Build the release binaries into `dist/`: qrdbd and the headless qrdb CLI,
each plain and FIPS, named `<binary>-<VERSION>-<goos>-<goarch>[-fips]`.
`release-artifacts` empties `dist/`, builds every platform in
`RELEASE_PLATFORMS` and writes `dist/SHA256SUMS`; it is what `release.yml`'s
`publish` job runs. `release-cross` builds one platform, and is what CI runs
for `linux/arm64`.

| Variable | Default | Meaning |
|---|---|---|
| `RELEASE_PLATFORMS` | `linux/amd64 linux/arm64` | the platforms `release-artifacts` builds |
| `PLATFORM` | *(required by `release-cross`)* | the one platform `release-cross` builds |

## `make test-release-alpha`

Runs the `release-alpha` recipe against a local bare remote with a throwaway
signing key. It needs no network and no credentials.
