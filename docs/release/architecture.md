# Release — Architecture

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

The composition seams a release exposes to upstream composers (go-xkms,
go-quicbaas) are inventoried in [extension-seams.md](extension-seams.md).

## What a release is

A release is one version, published as a set of tags that all point at the
same commit:

| Tag | Module |
|---|---|
| `vX.Y.Z-alpha` | `github.com/jeremyhahn/go-qrdb` |
| `sdk/go/vX.Y.Z-alpha` | `github.com/jeremyhahn/go-qrdb/sdk/go` |
| `sdk/go/gorm/conformance/vX.Y.Z-alpha` | `github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance` |

The nested modules are discovered from the tree (`scripts/lockstep-modules.sh`)
and declared in `lockstep-baseline`, so a new module under `sdk/` joins the set
without a list being edited in two places.

**Version equality is the compatibility statement.** A consumer that pins
`go-qrdb@v0.22.0-alpha` and `sdk/go@v0.22.0-alpha` gets modules built and
tested together. A release whose tags point at different commits, or that
lacks one of them, is *skewed*. `go get` can resolve a skewed release, and
once a consumer has recorded it in `go.sum` nobody can take it back.

## Who creates a tag

**The project agent, by hand, with `make release-alpha`** — the same
standard every other first-party repository in this portfolio follows
(release-versioning.md §1a, the "Alpha" row: *"the project agent, any time CI
is green on main"*). There is no release bot, no bot secret and no workflow
that creates a tag:

```
 release PR (VERSION + CHANGELOG)  ──review──▶  main  ──CI green──▶
                                                                    │
                                          operator runs, by hand,
                                          make release-alpha VERSION=vX.Y.Z-alpha
                                                                    │
                   one atomic push of every tag ◀──────────────────┘
                   (lockstep audit, CHANGELOG check, standalone-resolution
                    and duplicate-tag checks already ran; signed tags)
                                                                    │
                                                                    ▼
                               the tag push triggers the Release workflow
                               (validate → gate → publish; see below)
```

1. **VERSION is authoritative.** The release is the version the merged
   `VERSION` file names. A release is therefore reviewed like any other change,
   as a commit that bumps `VERSION` and adds the matching `CHANGELOG.md`
   section, before anything is published.
2. **`make release-alpha` never writes VERSION.** It tags the merged bump and
   creates no commit. It refuses, before fetching or writing anything, when
   `VERSION` on `HEAD` does not already name the requested release ("merge the
   VERSION/CHANGELOG bump first"), when `HEAD` is unsigned, and when `HEAD` is
   not exactly the commit the remote's `main` holds.
3. **One atomic push of the tags.** The recipe audits the tags it has just
   created for lockstep, then pushes every tag in a single `git push --atomic`.
   The branch is not pushed: the release commit is already on `main`. The server
   applies all of the tags or none of them, so a skewed set cannot be
   half-published.
4. **The operator signs as themselves.** Every tag is created and signed
   through `AGENT_GIT`, the same identity and SSH key every other commit in
   this repository already carries. There is no separate credential to
   provision or protect: the write access that already lets an agent push a
   reviewed commit to `main` is the write access that lets it tag one.

## What the Release workflow does

`.github/workflows/release.yml` triggers on `push: tags: ["v*"]` — the root
tag only, since the lockstep tags (`sdk/go/v*`, `sdk/go/gorm/conformance/v*`)
do not themselves start with `v`. It does not create anything; it validates
what the operator just published and, once validated, publishes the release
notes and the release binaries:

1. **`validate`** — re-derives the facts `release-alpha` already checked
   locally, against what the server now holds: the tag is a well-formed
   semver (optionally `-alpha`), `VERSION` at the tagged commit is byte-equal
   to it, and `CHANGELOG.md`'s newest released section names it too
   (`scripts/check-version-reconciled.sh`). It then fetches and audits the
   lockstep tags with a bounded retry: the atomic push makes the server apply
   every tag together, but says nothing about how soon a *fetch* on a
   different connection observes all of them.
2. **`gate`** — re-runs the functional build/lint/test signal from ci.yml's
   `image` job (lint, build and the unit suites) against the exact tagged tree. It does not
   repeat every ci.yml step: `release-alpha` refuses to tag anything but the
   commit `main`'s own `ci-gate` already verified clean, so re-running a
   guard's self-test or a whole-tree scan against the same tree would answer
   a question that cannot have changed. What is worth re-running is the
   functional result — a checkout resolving the wrong object, a toolchain
   mismatch on this runner, a race the first run got lucky on.
3. **`publish`** — needs both of the above green, then builds the release
   artifacts (`make release-artifacts`, below), lifts the matching
   `## [X.Y.Z]` section out of `CHANGELOG.md`, publishes it as this tag's
   release notes through the forge's own releases API, and attaches every
   artifact to that release through the release's own `upload_url`. The
   artifacts are built first, so a build failure publishes nothing.

## Release artifacts

One Makefile path builds every published binary. `make release-artifacts`
runs it for each platform in `RELEASE_PLATFORMS` (`linux/amd64 linux/arm64`)
into a clean `dist/`, then writes `dist/SHA256SUMS`; `make release-cross
PLATFORM=<goos>/<goarch>` runs it for one platform. Per platform it builds
qrdbd and the headless qrdb CLI twice:

```
 GOOS/GOARCH from the platform, CGO_ENABLED=0, -trimpath,
 the default feature tags + gms_pure_go, the version stamp (-X main.version/gitCommit/buildDate)
        │
        ├── GOFIPS140=off     ──▶  dist/<binary>-<VERSION>-<goos>-<goarch>
        └── GOFIPS140=latest  ──▶  dist/<binary>-<VERSION>-<goos>-<goarch>-fips
```

- **No cgo, so no cross toolchain.** Nothing under `cmd/` or `pkg/` imports
  `"C"`. The one cgo dependency of qrdbd and qrdb is go-mysql-server's MySQL
  `REGEXP` engine, which links the system ICU libraries, so a native
  `make build-server` binary needs that ICU version on the host. The release
  builds add go-mysql-server's `gms_pure_go` tag, which swaps that engine for
  Go's `regexp` (RE2 syntax). That makes linux/arm64 a plain `go build` and
  every artifact a static binary. It is also the one behaviour a release
  binary does not share with a native build: `REGEXP`, `RLIKE` and the
  `REGEXP_*` functions take RE2 patterns, so backreferences and lookaround
  are refused. The Wails desktop client also needs cgo and is not a release
  artifact.
- **FIPS is a build-time choice.** `GOFIPS140` links Go's native FIPS 140-3
  module, at the same `latest` the nightly FIPS leg tests. The plain build
  sets `GOFIPS140=off` explicitly, so the caller's environment cannot turn it
  into a second FIPS binary. The enforcement level stays the operator's
  `GODEBUG` ([seal/fips140-mode.md](../seal/fips140-mode.md)).
- **Integrity is `SHA256SUMS`,** over every binary, attached with them. The
  release carries no separate signature; the signed tag is the provenance of
  the tree the binaries were built from.
- **CI cross-builds arm64 on every pull request.** The `image` job runs
  `make release-cross PLATFORM=linux/arm64` in the toolchain image beside its
  native build, so a change that breaks the arm64 build, plain or FIPS, fails
  the pull request rather than the tag. Nothing runs on arm64 in CI: the
  arm64 integration run is a separate, pre-RC step.
- **The shipped image is scanned on every pull request.** The same PR-tier
  `image` job runs `make trivy-image` over the `integration` image after its
  stages, with `--ignore-unfixed` and the module cache (`/gomodcache`) skipped
  (handbook `devops.md` §8), so a fixable vulnerability fails the pull request
  rather than the tag.

The workflow does not build a consumer program against the published tags.
The integration suites already exercise every SDK and API surface, and the
owner ruled a post-release consumer build unnecessary.

## Defence in depth

The **Release Lockstep** workflow still audits every tag push and every push
to `main`. It catches what `release-alpha`'s own pre-flight cannot see: a tag
that is deleted or moved after the fact. It runs on pull requests too, under
the same ref-keyed concurrency group as CI, whose
`cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}` spares `main`:
it publishes nothing, so a superseded audit is safe to cancel, and a tag push
is its own ref, so it supersedes only a re-push of the same tag. `release.yml`,
which publishes, has no such group.

## CI tiers

"CI green" above means `ci-gate` on the commit's **push to `main`**, which
always runs the full tier. CI runs in two tiers:

| Tier | When | Jobs |
|---|---|---|
| PR | every run | `image`, `test-typescript`, `arch-gates`, `ci-gate` |
| Full | push to `main`, `workflow_dispatch`, a pull request labelled `ci:full` | the PR tier, plus `integration-standalone` and `integration-features` (one leg per feature) |

`ci-gate` is the one required check in both. It fails on any result other
than `success`, with one exception: a job tagged `full` that was skipped
because the run did not select the full tier. In a full-tier run a skipped
job fails it as before. The pull-request trigger has no `labeled` type
(Gitea maps every label change to it, so each one started a run that
cancelled the PR's run in progress), so adding `ci:full` starts nothing by
itself: it takes effect on the PR's next push, an update-branch, or a
`workflow_dispatch`.

The full-tier jobs wait on the PR tier: `integration-standalone` and
`integration-features` each need `image`, which runs lint, the build, every
unit suite and the security stages, and `test-typescript` and `arch-gates`
beside it, so neither starts until the whole PR tier has passed, and
a pull request that fails lint or a unit suite never holds the shared runners
for the long legs. A full-tier run on a tree whose PR tier is red therefore
reports those jobs `skipped`, which `ci-gate` fails.

The `ci:full` label goes on a pull request once, when it is green on the PR
tier and ready to merge, never earlier and never twice; a `workflow_dispatch`
of the full tier on the PR branch is the accepted alternative.

Every workflow on pull requests has one concurrency group per ref
(ci-template v1.22.1, handbook `devops.md` §9): a newer push to a pull
request's ref cancels that ref's older run, but a push to `main` never
cancels an in-flight `main` run. The newer `main` run waits for it, so every
commit on `main` keeps its run and every merge is validated.
`test/buildgates/ci_tiers_test.go` holds the value.

No workflow runs on a schedule. `nightly.yml` (FIPS 140-3, trivy's full
report, and the signal job) runs only on `workflow_dispatch`, and is dispatched by hand each night together
with `ci.yml`.

`image` runs every stage of the handbook CI standard (`ci-standard.md`) in the
one image built from `build/docker/devcontainer/Dockerfile` at the commit under
test (ci-template v1.22.1): lint and the lint gates, the server and headless
CLI build and the linux/arm64 release cross-build, `make coverage` with the
other unit suites and `test-sdk`, security and deps-check in its `toolchain`
stage, then `trivy image` over its `integration` stage, all in one job.
`test-typescript` (the TypeScript SDK and the desktop frontend) and
`arch-gates` (the import-boundary, public-releasable and compose-target
gates) run on the runner beside it.

`image`'s two scans (`make security`'s trivy leg and
`make trivy-image`), scan only with a vulnerability database younger than
`TRIVY_DB_MAX_AGE_HOURS` (24). `scripts/trivy-certified-db.sh` tries the
runner's configured cache first, then trivy's documented pair in order,
`mirror.gcr.io/aquasec/trivy-db:2` then `ghcr.io/aquasecurity/trivy-db:2`,
certifying each with the same age check, and fails closed when none is fresh.
A fallback widens where the database may come from, never how
old it may be.

`integration-features` is one job with a matrix leg per feature: each leg runs
`make integration-test IT_FEATURE=<feature>` in that `integration` image,
under its own 45-minute limit, and the legs run in parallel. `ci-gate` needs
the job once and reads its aggregate result, which is `failure` when any leg
fails. `integration-standalone` still runs the Docker `test/integration/api`
suites on the `.devcontainer/` image.

`test/buildgates/ci_tiers_test.go` and
`test/buildgates/feature_jobs_gate_test.go` hold the tiers, the gate, the
triggers and the per-feature legs together; see engineering.md.

## Rejected alternatives

- **A workflow that creates the tags**, dispatched by the operator or
  triggered some other way. Rejected by the 2026-10-06 ruling: every sibling
  repository in the portfolio has the operator cut the tag directly, and a
  bot identity existing only to re-perform a push the operator is already
  trusted to make was overhead with no corresponding safety gain.
- **A version input on a dispatch trigger.** Rejected because `VERSION` would
  then be derived from the input, and a release would bypass the review of
  the VERSION and CHANGELOG commit.
- **A tag-push trigger that cuts the REST of the lockstep set** on seeing the
  root tag. Rejected because the first tag would already be published before
  the workflow ran, which is exactly the skew `release-alpha`'s single atomic
  push exists to prevent. The tag-push trigger this repository uses instead
  only validates a set `release-alpha` already published together.

## Release kinds

This repository cuts **internal** releases (`vX.Y.Z-alpha`) on its development
forge. The project agent prepares the release (the VERSION and CHANGELOG
commit) and, once it is merged and CI is green on `main`, cuts and pushes the
tags itself. Release candidates and public releases are cut under the
organisation's release policy and are out of scope here; a public release is
published separately and only with the owner's approval
(version-control.md §5a) — the same `push: tags` event, wherever it lands,
is what the Release workflow validates and annotates.
