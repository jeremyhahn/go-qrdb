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

# Release — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-14

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
notes:

1. **`validate`** — re-derives the facts `release-alpha` already checked
   locally, against what the server now holds: the tag is a well-formed
   semver (optionally `-alpha`), `VERSION` at the tagged commit is byte-equal
   to it, and `CHANGELOG.md`'s newest released section names it too
   (`scripts/check-version-reconciled.sh`). It then fetches and audits the
   lockstep tags with a bounded retry: the atomic push makes the server apply
   every tag together, but says nothing about how soon a *fetch* on a
   different connection observes all of them.
2. **`gate`** — re-runs the functional build/lint/test signal from ci.yml's
   `lint`, `build` and `test` jobs against the exact tagged tree. It does not
   repeat every ci.yml step: `release-alpha` refuses to tag anything but the
   commit `main`'s own `ci-gate` already verified clean, so re-running a
   guard's self-test or a whole-tree scan against the same tree would answer
   a question that cannot have changed. What is worth re-running is the
   functional result — a checkout resolving the wrong object, a toolchain
   mismatch on this runner, a race the first run got lucky on.
3. **`publish`** — needs both of the above green, then lifts the matching
   `## [X.Y.Z]` section out of `CHANGELOG.md` and publishes it as this tag's
   release notes through the forge's own releases API.

The workflow does not build a consumer program against the published tags.
The integration suites already exercise every SDK and API surface, and the
owner ruled a post-release consumer build unnecessary.

## Defence in depth

The **Release Lockstep** workflow still audits every tag push and every push
to `main`. It catches what `release-alpha`'s own pre-flight cannot see: a tag
that is deleted or moved after the fact. It runs on pull requests too, under
the same ref-keyed `cancel-in-progress: true` concurrency group as CI: it
publishes nothing, so a superseded audit is safe to cancel, and a tag push is
its own ref, so it supersedes only a re-push of the same tag. `release.yml`,
which publishes, has no such group.

## CI tiers

"CI green" above means `ci-gate` on the commit's **push to `main`**, which
always runs the full tier. CI runs in two tiers:

| Tier | When | Jobs |
|---|---|---|
| PR | every run | `lint`, `build`, `test`, `test-sdk`, `test-typescript`, `security`, `ci-gate` |
| Full | push to `main`, `workflow_dispatch`, a pull request labelled `ci:full` | the PR tier, plus `integration-standalone`, `integration-features` (one job per feature), `trivy` |

`ci-gate` is the one required check in both. It fails on any result other
than `success`, with one exception: a job tagged `full` that was skipped
because the run did not select the full tier. In a full-tier run a skipped
job fails it as before. The `labeled` pull-request trigger is what lets
adding `ci:full` start a full-tier run on the same head.

No workflow runs on a schedule. `nightly.yml` (FIPS 140-3, trivy's full
report, and the signal job) runs only on `workflow_dispatch`, and is dispatched by hand each night together
with `ci.yml`.

`integration-features` is one job with a matrix leg per feature: each leg runs
`make integration-test-<feature>` under its own 45-minute limit, and the legs
run in parallel. `ci-gate` needs the job once and reads its aggregate result,
which is `failure` when any leg fails.

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
