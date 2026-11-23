# Release — Public release readiness

> **Status: INFORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-23
> An evidence record; [architecture.md](architecture.md) is normative.

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

The evidence audit of the public-release checklist: the SPEC row
`release-readiness` in [docs/SPEC.md](../SPEC.md), tracked by issue #566.
Issue #566 is a checklist, not work; this page records, item by item,
whether the item is met today, the evidence for that state, and the next
action. [architecture.md](architecture.md) describes what a release is and
how it is cut; this page says how far the tree is from the first public one.

Audited on 2026-11-12 against `main` at `e8b579229d`, VERSION
`0.58.0-alpha`; re-audited on 2026-11-23 against the row #566 branch
(base `5dc90e44f3`), VERSION `0.58.0-alpha`; re-checked on 2026-11-23
against the row #566 tip `116f361dcc` (base `e0faf70e32`), VERSION
`0.58.0-alpha`: the go.mod and sdk/go/go.mod pins, VERSION, CHANGELOG
`[Unreleased]`, the `.github/workflows/` files and their `runs-on` values,
and the compatibility.md status are as recorded below, so every state
stands.

States:

- **met**: the evidence below holds on the audited tree.
- **not met**: work in this repository is needed.
- **waits on a lower project**: the item depends on a release of a project
  below go-qrdb in the dependency order (go-quicraft → go-qrdb → go-xkms →
  …); nothing in this repository can close it first.

## Checklist

| # | Item (issue #566) | State | Evidence | Next action |
|---|---|---|---|---|
| 1 | Every other Public release milestone issue closed or moved out | not met | The open SPEC rows marked planned in [docs/SPEC.md](../SPEC.md) belong to the milestone; this row is one of them. | Close or re-milestone each open issue of the milestone; re-audit when only #566 remains. |
| 2 | VERSION bumped and a CHANGELOG fragment | not met | [VERSION](../../VERSION) reads `0.58.0-alpha`, a pre-release; [CHANGELOG.md](../../CHANGELOG.md) has an `[Unreleased]` section and no release-candidate entry. | At the cut, set VERSION to the RC version and move `[Unreleased]` under it (`make release-alpha` path, see [architecture.md](architecture.md#who-creates-a-tag)). |
| 3 | No consumer pin decreased | met | Every in-house pin in [go.mod](../../go.mod) and [sdk/go/go.mod](../../sdk/go/go.mod) is at or above the version the previous release used (go-codec v1.5.1-alpha, go-enclave v0.12.0-alpha, go-objstore v0.9.1-alpha, go-quicraft v0.24.3-alpha, go-truststrap v0.7.1-alpha). | Re-check at the cut: diff both go.mod files against the last tag; no line may go down. |
| 4 | Upstream go-codec at an RC or public release | waits on a lower project | [go.mod](../../go.mod): `go-codec v1.5.1-alpha`. | Needs a go-codec RC tag, then a bump lane. |
| 5 | Upstream go-enclave at an RC or public release | waits on a lower project | [go.mod](../../go.mod) and [sdk/go/go.mod](../../sdk/go/go.mod): `go-enclave v0.12.0-alpha` (#1654). | Needs a go-enclave RC tag, then a bump lane. |
| 6 | Upstream go-objstore at an RC or public release | waits on a lower project | [go.mod](../../go.mod): `go-objstore v0.9.1-alpha`. | Needs a go-objstore RC tag, then a bump lane. |
| 7 | Upstream go-quicraft at an RC or public release | waits on a lower project | [go.mod](../../go.mod): `go-quicraft v0.24.3-alpha`. | Needs a go-quicraft RC tag, then a bump lane. |
| 8 | Upstream go-truststrap at an RC or public release | waits on a lower project | [go.mod](../../go.mod): `go-truststrap v0.7.1-alpha`. | Needs a go-truststrap RC tag, then a bump lane. |
| 9 | Compatibility policy acknowledged | not met | [compatibility.md](compatibility.md) proposes the policy (status: proposed, awaiting owner acknowledgement) and lists the owner choices it needs; [CHANGELOG.md](../../CHANGELOG.md) states the pre-1.0 rule and links it. No owner acknowledgement is recorded. | Owner settles the choices in [compatibility.md](compatibility.md#owner-choices) and acknowledges the policy (a ruling or a line on #566); then mark its status normative and this row met. |
| 10 | No lab addresses, fleet hostnames or owner paths in tracked files, by a gate | not met | `make check-public-releasable` (re-run 2026-11-23): exit 2 (make; the script exits 1). It fails closed: no fleet pattern file was readable (`$PUBLIC_RELEASE_PATTERNS_FILE` unset, no handbook or XDG copy), so it scanned nothing. `make check-publication-bound` (re-run 2026-11-23 on the 566 tip): exit 0, 329 feature-folder files, this one included, under `docs/*/` publishable. | Run `make check-public-releasable` with the pattern file present (see [user.md](user.md)) and fix every hit; require it in the release path. |
| 11 | No internal-only workflow files | not met | `.github/workflows/` holds `batch.yml`, `ci.yml`, `code-review.yml`, `deps-bump.yml`, `nightly.yml`, `release-lockstep.yml`, `release.yml`, `scans.yml`. All but `ci.yml` and `code-review.yml` run on `ubuntu-latest`. `ci.yml`'s preflight job runs on the `self-hosted` `bigdisk` label for a push or same-repository PR unless `vars.CI_PREFLIGHT_RUNS_ON` overrides it, and picks per run between `ubuntu-latest` and the `bigdisk`/`bigdisk-docker` pools from repository variables; a fork's PR and a non-github.com server take `ubuntu-latest` for every job (ci-template v1.52-v1.54). `code-review.yml` runs its review jobs on the `codex` and `qwen` self-hosted labels, each with a `# self-hosted:` reason and the `codex` line with a `lab-string-ok:` waiver. | `ci.yml` now falls back to hosted runners on a fork; still to decide: the preflight's self-hosted default for same-repository events, and whether `code-review.yml`'s review hosts stay (record why); then mark met. |
| 12 | sdk/go tagged in lockstep with the root module | met | `.github/workflows/release-lockstep.yml` triggers on `v*`, `sdk/go/v*` and `sdk/**/v*` tags and fails a root `vX.Y.Z` pushed without `sdk/go/vX.Y.Z`; `make check-tag-lockstep` runs the same check before the atomic push; `release.yml` audits the lockstep tags. | Confirm at the first RC that `sdk/go/v<version>` is pushed atomically with `v<version>`. |

## Gate results

| Target | Exit code | Result |
|---|---|---|
| `make check-public-releasable` | 2 | FAIL, fail-closed: no fleet pattern file readable, so no rule was loaded. Not a finding against the tree; the gate did not run. |
| `make check-publication-bound` | 0 | ok (2026-11-23, 566 tip): scanned 329 of 329 feature-folder files, this one included, under `docs/*/`; all publishable. |

## Not met today

Items 1, 2, 9, 10 and 11 need work here; items 4 to 8 wait on RC tags of the
lower projects, in dependency order.
