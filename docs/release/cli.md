# Release — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

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
| `VERSION` | *(required)* | the release, `vX.Y.Z-alpha`. Must be given on the command line |
| `RELEASE_REMOTE` | `dev` | the remote to publish to. A remote that points at github.com is refused |
| `AGENT_GIT` | a fixed wrapper name set in the Makefile, resolved on `PATH` | the identity and signing wrapper every git write goes through |
| `REPO_NAME` | `go-qrdb` | the literal repository name handed to the wrapper |

`VERSION`, `RELEASE_REMOTE`, `AGENT_GIT`, `REPO_NAME` and `SUBMODULE_TAGS` are
each refused when the value comes from the environment (`make -e`, including
an inherited `MAKEFLAGS=-e`).

`VERSION` is authoritative: the recipe tags `HEAD` and never writes VERSION
or commits. It refuses unless `VERSION` on `HEAD` already names the requested
release ("VERSION on HEAD names X, not Y; merge the VERSION/CHANGELOG bump
first"), `HEAD` is SSH-signed, and `HEAD` is exactly what the remote's `main`
holds. It also refuses a branch other than `main`, a dirty tree, a skewed tag
set, an existing tag, a missing CHANGELOG section and a remote whose fetch
and push URLs differ; the messages are in
[When `release-alpha` refuses](user.md#when-release-alpha-refuses). It pushes
only the tags.

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
`publish` job runs. `release-cross` builds one platform, and is what CI's full tier runs
for `linux/arm64`.

Both end with `make check-release-artifacts`
(`scripts/check-release-artifacts.sh`), which proves each artifact is what
its name says before anything publishes: the file exists for every binary,
platform and variant; the `GOOS`/`GOARCH` that `go version -m` reads out of
it and the ELF header's machine (62 amd64, 183 arm64) match the name; it was
built with `CGO_ENABLED=0`, `-trimpath` and every release build tag; the
`-fips` variant records `GOFIPS140=latest` and the plain one no `GOFIPS140`.
`release-artifacts` also requires `SHA256SUMS` to list exactly those
artifacts and every checksum to verify; `release-cross` checks its one
platform and no `SHA256SUMS`. A goarch with no known ELF machine is refused.
Every finding prints one line naming the file; the target fails after
reporting them all. `make test-check-release-artifacts` (a lint gate) pins
the check against real fixture builds.

| Variable | Default | Meaning |
|---|---|---|
| `RELEASE_PLATFORMS` | `linux/amd64 linux/arm64` | the platforms `release-artifacts` builds |
| `PLATFORM` | *(required by `release-cross`)* | the one platform `release-cross` builds |
| `PLATFORMS` | `$(RELEASE_PLATFORMS)` | the platforms `check-release-artifacts` checks |
| `CRA_SUMS` | `1` | `check-release-artifacts` also checks `SHA256SUMS` (`0` skips it) |

## `make test-cross PLATFORM=<goos>/<goarch>`

Compiles and links the test binaries of one platform, plain and FIPS, and
runs none of them (`go test -exec /bin/true`, `CGO_ENABLED=0`). `go test ./...`
stops at a nested `go.mod`, so each module it covers is built from its own
directory:

- the root module, `./...` twice: with the release build tags (the unit
  tests) and with the release build tags plus `integration` (every
  integration-tagged test, the `test/integration` suites included);
- the Go SDK (`sdk/go`), with the same two tag sets;
- `test/clusterproof`, with the tags `make test-clusterproof` uses
  (`codec_json,raft,clusterproof`).

The other nested modules (`test/performance/*`, `test/vanguardprobe`,
`sdk/go/gorm/conformance`) are out: no release test target runs them.
`GOFIPS140` selects the variant as
`release-cross` does. `make test-cross PLATFORM=linux/arm64` on an amd64 host
catches an arm64-only compile failure before the arm64 integration run before
the RC (engineering.md, "linux/arm64 before the RC"). Without `PLATFORM` it
prints the usage line and exits 2.

## `make test-release-alpha`

Runs the `release-alpha` recipe against a local bare remote with a throwaway
signing key. It needs no network and no credentials.
