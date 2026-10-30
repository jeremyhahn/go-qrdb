# Release — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
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

## Where it lives

| File | Holds |
|---|---|
| `Makefile` — `release-alpha` | the release recipe, run by hand: explicit version, lockstep audit, CHANGELOG reconciliation, standalone resolution of each nested module, duplicate-tag check, signed annotated tags, one atomic push, and a probe that asks the remote what it holds when a push reports failure |
| `.github/workflows/release.yml` | the Release workflow: `push: tags: ["v*"]` only, three jobs (`validate`, `gate`, `publish`), no bot secret of any kind |
| `scripts/check-tag-lockstep.sh`, `scripts/lockstep-modules.sh`, `lockstep-baseline` | the lockstep audit and the module-set derivation both the publisher and the auditor read |
| `scripts/check-version-reconciled.sh` | VERSION/CHANGELOG/tag reconciliation, run by `release-alpha`, by the Release Lockstep workflow, and by `validate` above |
| `.github/workflows/release-lockstep.yml` | the defence-in-depth audit on every tag push |
| `Makefile` — `check-mod-tidy`, `scripts/check-mod-tidy.sh` | the Lint step that runs `go mod tidy -diff` in every tracked module, so an untidy `go.mod` or `go.sum` fails the change that makes it rather than the release. It fails two ways: `go.mod/go.sum is not tidy in: <module>` with the diff, when the module is untidy; `tidiness is UNKNOWN in: <module>` with the tool's error, when `go mod tidy -diff` could not run there (a download timeout, a module missing from the cache), which says nothing about whether the module is tidy. Only the second is retried, once. `test-mod-tidy` is its self-test |

## Invariants

- **No workflow creates a tag or runs `make release-alpha`.** `test/buildgates`
  scans every workflow file, `release.yml` included, for a hand-written
  `git tag`/`git push --tags`/`refs/tags/` write or a `make release-alpha`
  invocation, and fails if it finds either anywhere.
- **`release.yml`'s only trigger is `push: tags: ["v*"]`.** Not the lockstep
  modules' own tags (`sdk/go/v*` does not match the glob `v*`), and nothing
  else — `test/buildgates` fails on an added trigger.
- **`release-alpha` never writes VERSION and never commits.** It tags `HEAD`
  and refuses, before anything is fetched or written, unless three things
  hold: `VERSION` on `HEAD` already names the requested release (otherwise:
  "VERSION on HEAD names X, not Y; merge the VERSION/CHANGELOG bump first"),
  `HEAD` carries an SSH signature, and `HEAD` is exactly what the remote's
  `main` holds. `VERSION=` is still a required argument, as the operator's
  statement of what they expect to publish; it is checked against the file
  and never replaces it.
- **Every tag is annotated, SSH-signed and on the release commit**, verified
  per tag before anything is pushed, and the whole set is audited for lockstep
  before the push. Any failure before the push removes every tag the run
  created.
- **The push is one `git push --atomic`** of exactly the release tags, never
  `--tags` and never a branch.
- **`validate` retries the lockstep audit.** The atomic push makes the server
  apply every tag together; it says nothing about how soon a `git fetch` on a
  different connection observes all of them, so the step fetches and
  re-audits up to ten times, three seconds apart, before failing.
- **`gate` runs the same commands as the lint, build and unit-test stages of
  ci.yml's `lint`, `lint-gates`, `image` and unit-suite jobs** (`make fmt`, `make vet`, `make vet-tags`, `make secure-analyzer`,
  `make lint`, `go build ./...`, `make build-server`, `make coverage`,
  `make test-tags`) against the exact tagged tree, not a re-run of every
  ci.yml step — see architecture.md for why the rest is skipped. The two
  sweeps, `make vet-tags` and `make secure-analyzer`, run here on every tag
  although ci.yml runs them (`make lint-sweeps`) only on a push to `main` and
  on `workflow_dispatch`, never on a pull request.
  `make lint` includes `lint-workflows`, which runs actionlint over
  `.github/workflows/` and `.gitea/workflows/` after asserting the binary is
  the pinned `ACTIONLINT_VERSION` (`scripts/tool-pins.sh`, the only copy of
  the pin; ci.yml carries none). The tool is baked into the runner and lane images and never installed over the
  network; a missing or mismatched actionlint fails the gate.
- **`publish` needs both `validate` and `gate`**, and addresses the forge's
  own releases API through `${{ github.api_url }}` rather than a hardcoded
  host, so the same workflow file publishes correctly on Gitea and on
  GitHub. It authenticates with the run's own token
  (`${{ secrets.GITHUB_TOKEN }}`, scoped by its job's own
  `permissions: contents: write`) — there is no separate bot identity.

## Credentials

**None.** The operator's existing git identity signs every tag, and the
Release workflow's `publish` job uses the run's own token, scoped by its
`permissions:` block. Nothing here needs a dedicated secret or variable.

## Tests

| Test | Proves |
|---|---|
| `scripts/check-release-alpha_test.sh` (`make test-release-alpha`) | the recipe against a local bare remote and a throwaway signing key, with the merged bump as the fixture's state: HEAD is tagged, no commit is made, every tag is signed and on HEAD, exactly the two tags are pushed, and three refusals -- a VERSION argument the file does not name, a bump the remote's `main` does not hold, and an unsigned HEAD |
| `scripts/check-tag-lockstep_test.sh` (`make test-tag-lockstep`) | the audit refuses a skewed set, including one whose deepest module's tag sits on another commit |
| `test/buildgates/release_workflow_test.go` | `release.yml`'s shape, with a mutation for each property: an added trigger, a widened tag pattern, a hand-written tag push, a call to `make release-alpha`, a lockstep audit with no retry or none at all, a skipped VERSION/CHANGELOG check, a `gate` job missing one of the matching ci.yml commands, `publish` losing its write permission or its dependency on `gate`, a hardcoded `github.com` instead of `github.api_url`, a `RELEASE_BOT` reference, and a step that tolerates failure. It also scans every OTHER workflow file for the same tag-write and `release-alpha` violations |
| `test/buildgates/action_pins_test.go` | every `uses:` is pinned to a 40-hex commit |
| `test/buildgates/ci_tiers_test.go` | ci.yml's tiers (see architecture.md, "CI tiers"): every job is in `ci-gate`'s `needs` and has a `check` line tagged `pr`, `code`, `full` or `nightly`; a `code` job carries exactly `if: needs.changes.outputs.docs_only != 'true'`, needs `changes`, and `ci-gate`'s DOCS_ONLY is `${{ needs.changes.outputs.docs_only }}`; a `full` job carries exactly the full-tier `if:`, ci.yml's `CI_FULL_TIER` is the same expression and `ci-gate`'s FULL_TIER is `${{ env.CI_FULL_TIER }}`; a `nightly` job carries exactly the schedule-or-dispatch `if:` and NIGHTLY is the same expression; a PR-tier job carries no `if:` and needs no code, full-tier or nightly job, and a code job needs no full-tier or nightly job; every `integration*` job (and a job named `trivy`) is `full`; the pull_request trigger lists `opened`, `synchronize` and `reopened` and not `labeled` (a label change starts no run). Every tier's jobs are pinned by name (`prTierJobs`: `changes`, `lint`, `lint-gates`, `arch-gates`; `codeTierJobs`: `test-typescript`, `image`, `coverage`, `test-clusterproof`, `coverage-fts`, `test-tags`, `test-sdk`; `fullTierJobs`: `integration-standalone`, `integration-features`, `fips`; `nightlyTierJobs`: `soak`), each must be in ci.yml with its tag, and the tag counts carry discovery floors, so an empty parse cannot pass. It runs `ci-gate`'s real script under bash: a skip passes only for a `full` job outside a full-tier run, a `nightly` job outside a schedule or dispatch run or a `code` job when DOCS_ONLY is `true`, and `cancelled`, `failure`, an unreadable result, an unreadable FULL_TIER, NIGHTLY or DOCS_ONLY fail, including the `integration-features` matrix's aggregate result when one leg fails or is cancelled. It also scans every workflow: `ci.yml`'s group on `main` carries the event and the commit, a scheduled run's group is `schedule-<repository>` (ci-template v1.29.3), with a literal `cancel-in-progress: true` (no `main` run cancels another; an expression, a `main` group that carries the event only, or a ref-keyed group reddens it); every other workflow on pull_request has the ref-keyed group with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}` (a push to `main` never cancels a `main` run; a literal `true` reddens it), and none but `ci.yml` (its in-template nightly cron, the one scheduler under rulings 135 and 216(1)) and `automerge.yml` has a `schedule:`. Each rule has a fixture mutation that must redden it |
| `test/buildgates/ci_schedule_guard_test.go` | ci.yml's schedule guard (ci-template v1.29.3; architecture.md, "CI tiers"): the `schedule-guard` job needs nothing, runs on `ubuntu-latest` with `actions: read` and `contents: read` only, outputs `verdict`, uses no action, and passes `AGENT_TOKEN` only on a schedule run; every `bigdisk` job is held, either itself (it carries the guard clause and needs `schedule-guard`) or through a need on a held job under an `if:` with no status function, and the count of `bigdisk` jobs carries a discovery floor; `ci-gate` carries `always()` with the not-`busy` clause, needs `schedule-guard`, and reads SCHEDULE and SCHEDULE_GUARD. It runs `ci-gate`'s real script under bash (a schedule run fails on any verdict but `run`, a dispatch ignores the verdict) and the guard step's real script against a stub `curl` and the image's `jq` (`run`, `busy` on a push run on `main` in progress or waiting, the `AGENT_TOKEN` fallback, `unreadable` when both tokens are refused, and no token on a command line). Each rule has a fixture mutation that must redden it |
| `test/buildgates/no_deprecated_test.go` (`TestNoLegacyOrDeprecatedAliasLanguage` alone is `make check-no-alias-language`, in `make lint-gates`) | no Go source, proto, CLI command or `docs/*.md` file carries a deprecated declaration, a redirect stub or legacy-alias language. The `docs/` scan is in `make lint-gates` as well as `make test` because a docs-only pull request runs lint and skips the unit suite |
| `test/buildgates/ci_logs_test.go` | no CI log carries a forge credential: every ci.yml job is in `credentialJobs` or in `unmaskedJobs` (`changes`, which prints only file names, and `ci-gate`, which prints only job results); in every job of `credentialJobs` (`docker-build`, `lint`, `lint-gates`, `image`, `coverage`, `test-clusterproof`, `coverage-fts`, `test-tags`, `test-sdk`, `test-typescript`, `arch-gates`, `integration-standalone`, `integration-features`, `fips`, `soak`) the first step after the checkout is "Mask the forge credentials", and it masks `GIT_CONFIG_KEY_0`, `GIT_CONFIG_VALUE_0` and the token inside a `*://*:*@*` URL with `::add-mask::`; no step in ci.yml runs `env`, `printenv`, `set -x` or `set -o xtrace`, or prints a `.env`, a `gomodauth` file or `MODULE_ENV_FILE`; ci.yml's `GO_BUILD_TAGS` is exactly, in order, the set `make -s print-build-tags` composes. Each rule has a fixture mutation that must redden it |
| `test/buildgates/fips_legs_test.go` | the `fips` job's parallel legs are exactly `make test` and `make test-sdk` (#1230): every package `go list` finds under `make -s print-test-pkgs` and in `sdk/go` is printed by `make -s print-fips-leg-packages` for exactly one leg of `make -s print-fips-legs`, no leg is empty or runs a package outside both suites, and ci.yml's `fips` matrix is one literal `include:` list naming every leg once, each with its own `minutes` limit over 5 and at most 45 (ruling 221(5)), with `timeout-minutes: ${{ matrix.minutes }}`, `fail-fast: false` and one step running `make test-fips-leg FIPS_LEG='$FIPS_LEG'` from `${{ matrix.leg }}` with `GOTEST_TIMEOUT=$((FIPS_LEG_MINUTES - 5))m` from `${{ matrix.minutes }}`; a package may be in two legs only when both select tests by name; discovery floors on the leg and package counts, and a fixture mutation for each rule |
| `test/buildgates/fips_split_test.go` | a package the `fips` job splits by test name (#1234) runs each of its top-level tests in exactly one of its legs: every `Test`, `Fuzz` and `Example` function `go list` and `go/parser` find in the package's test files under `make -s print-build-tags` is selected by the `-run` and `-skip` patterns `make -s print-fips-leg-filter` prints for exactly one leg, each split leg names one package, and every pattern compiles and holds no `/`, quote, comma or space; discovery floors on the split-package and test counts, and a case for each rule |

## Test tooling this feature folder also owns

No `docs/testing/` folder exists, so the per-feature integration-test
tooling that spans every OTHER folder documents itself here, beside the
other whole-tree test/buildgates gates this file already tracks.

| File | Holds |
|---|---|
| `test/integration/features.mk` | the ONE table mapping every `docs/<feature>/` folder to the integration test files, relative to `test/integration/`, whose `Test` functions belong to it |
| `Makefile` — `integration-test-%` | the pattern rule, one target per feature (`make integration-test-<feature>`), that reads `features.mk`, extracts the mapped files' `Test` function names and runs exactly them over the self-spawned `sdk/go/testutil/qrdbdharness` suites — no Docker |
| `Makefile` — `test-pkg` | `make test-pkg PKG=./pkg/foo/...`: one package's unit tests under the same tags `make test` uses |
| `test/buildgates/feature_integration_coverage_test.go` (`make check-integration-feature-coverage`) | every `docs/*/architecture.md` folder has a `features.mk` entry, and every non-Docker integration `Test` function is named by some feature's file list |
| `Makefile` — `INT_FEATURES`, `print-integration-features` | every feature whose `features.mk` entry maps at least one file, sorted: the features CI's `integration-features` shards run, each in one shard. `make -s print-integration-features` prints them |
| `Makefile` — `integration-test-features` | runs each feature in `IT_FEATURES` past a failing one, prints a PASS/FAIL line with seconds per feature and a summary naming the failures, and fails if any failed; an unknown name is a FAIL. `IT_FEATURES_IN=host` (default) runs `integration-test-feature`, `image` runs `integration-test IT_FEATURE=<f>` |
| `test/buildgates/feature_jobs_gate_test.go` (also run by `make check-integration-feature-coverage`) | ci.yml's `integration-features` matrix is only `include` entries, each exactly a literal `shard` name (unique) and a non-empty literal `features` list, and the lists together name every feature of `make -s print-integration-features` exactly once and nothing else (a missing, duplicated or unknown feature fails with the feature and its fix), and that list is exactly the `features.mk` entries that map a file; each shard needs `docker-build` and runs exactly seven steps, the "Mask the forge credentials" step first (it runs no `make`), then the age-gated reap of `qrdb-ci-` stacks, the unconditional read of the Alpine package index into `APK_INDEX_SUM` (`make -s apk-index-sum`, which runs `scripts/apk-index-sum.sh`, ci-template v1.29, on the `GO_IMAGE` pin), `scripts/ci-registry.sh pull "$IT_REF" "$IT_HOLD"` under `env.CI_PUBLISH == 'true'` with an `IT_REF` that is `docker-build`'s `it_ref` output (DS-3516), `scripts/ci-registry.sh local it "$IT_HOLD"` under `env.CI_PUBLISH != 'true'`, each handing its image on as `IT_IMAGE`, `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image` with `IT_FEATURES: ${{ matrix.features }}` in the job env, and `make docker-down-it` under `always()`, with an `IT_PROJECT` that starts `qrdb-ci-` and carries the run, the attempt and the shard, and no job-level `IT_IMAGE`; `make -s it-image` prints a stable `qrdb-ci-it:<16-hex hash>`, and `make docker-down-it` removes no image; against a stub `docker` that untags one of several names freely and refuses to remove the last name of an image a container uses, `make docker-build-it` holds the image (`IT_HOLD`) so the reaper keeps it however old it is, even when the reaper removed it between the check and the hold, removes it once `make docker-release-it` released the hold, and removes a dead job's stale hold and its image in one pass; the reaper removes a stale image pulled from the registry, by tag and by digest, and keeps one a hold uses (every name of it: the local tag, the registry tag and the digest), one that is fresh, and another repository's; `timeout-minutes` is set and at most 45 (#1286; 30 on a light fleet); `fail-fast` is `false`; the job carries the full-tier `if:`; ci.yml runs on push and pull_request. Each rule has a fixture mutation that must redden it |
| `test/buildgates/apk_index_sum_test.go` | `make -s apk-index-sum`, against a stub `docker`, resolves the repository's Dockerfile to the `GO_IMAGE` that `scripts/tool-pins.sh` pins and prints only a sha256; the bare `scripts/apk-index-sum.sh` refuses that Dockerfile (its `FROM ${GO_IMAGE}` has no ARG default, so a default there would be a second copy of the pin); and every ci.yml step that reads the sum runs `make -s apk-index-sum`, never the bare script |
| `test/buildgates/ci_image_digest_test.go` (run by `make test` and `make check-action-pins` with the rest of `test/buildgates`) | the workflow env `CI_PUBLISH` is exactly a push to `main`; no workflow or job env holds the registry token, and every step of every job that logs in, pushes or pulls (`scripts/ci-registry.sh push\|pull`, `docker login\|push\|pull`) or carries the token runs under `env.CI_PUBLISH == 'true'`, with nothing ORed or negated around it, so no pull request touches the registry; `docker-build` sets `packages: write`, pushes with `scripts/ci-registry.sh push`, validates its outputs against `^[^@ ]+@sha256:[0-9a-f]{64}$` under the same condition and outputs `toolchain_ref` and `it_ref` from a step; every other job that reads one of those outputs needs `docker-build`, sets `packages: read`, has no `runs-on` that follows `docker-build`, reads no other output and pulls each output it reads with `scripts/ci-registry.sh pull` bound to that output before its first stage of that image; every job that runs a stage (`scripts/in-toolchain-image.sh`, `make integration-test`) or reads an output has a `scripts/ci-registry.sh local toolchain\|it` step under `env.CI_PUBLISH != 'true'` before that stage, pulls on a push to `main` unless it is dispatch-only, and never probes for or builds an image itself (`docker image inspect`, `docker build`, `make docker-build-*`); at least six consumers. A copy of ci.yml with `lint` reverted to `docker image inspect \|\| make docker-build-toolchain` must fail it, and each rule has a fixture mutation that must redden it |
| `test/buildgates/ci_registry_test.go` (with the rest of `test/buildgates`) | `scripts/ci-registry.sh push` logs in to the registry of the forge the run is on and names it when the login is refused; `scripts/ci-registry.sh local`, against a stub `docker` that fails on and records any login, push, pull or tag, with the registry token and a job's image overrides in its environment, builds the toolchain image only when the daemon lacks its tag, holds it by image ID and prints the ID, builds the integration image through `make docker-build-it IT_HOLD=<hold>` and prints its tag, hands the build a module-auth file without the token and removes it, and refuses a family or hold name it does not know |
| `test/integration/COVERAGE.md` | the per-feature, per-transport (REST/gRPC/QUIC/MCP/Unix/embedded) CLI coverage accounting, and every known gap |
| `Makefile` — `INT_PROJECT`, `INT_IMAGE`, `INT_GPU_IMAGE` | the per-run identity of a Docker integration run, the host plus, in CI, the run, attempt and feature goal (`make integration-test-standalone`, `-cluster`, `-dao`, `-gpu`, `-embedding`, `-rag-ollama`): the compose project, and the images the run builds and runs, `go-qrdb-int:<INT_PROJECT>` and `go-qrdb-int-gpu:<INT_PROJECT>`, exported as `QRDB_INT_IMAGE` and `QRDB_INT_GPU_IMAGE` |
| `Makefile` — `integration-down` | the one teardown of a Docker integration run: `docker compose down -v --remove-orphans` for the run's project, then removal of the run's own images. Every stage calls it after its tests, pass or fail; CI's `always()` teardown steps call it with the job's `INT_PROJECT` |
| `test/buildgates/integration_image_tag_test.go` | every service a `test/integration/api` compose file builds names its image through a required interpolation (`${VAR:?...}`, never a literal tag or a `${VAR:-default}`); each such variable is exported from a Makefile variable derived from `INT_PROJECT`; `integration-image` tags its build with it; every stage that brings the stack up passes `-p $(INT_PROJECT)` and ends with `integration-down`, which removes the images; no workflow runs `docker compose` itself, and every job that runs one of those stages (the Makefile targets that bring the stack up, not a per-feature `integration-test-<feature>`) tears down through `make integration-down` under `always()` with the stage's own `INT_PROJECT` |
| `test/buildgates/integration_job_names_test.go` | with CI's run and attempt in the environment, every `integration-test-<feature>` leg, a second attempt and another run derive distinct `INT_PROJECT`, `INT_IMAGE` and `INT_GPU_IMAGE` on one host, each a valid compose project name and image tag, and one job derives the same names on every invocation, so its teardown finds them |

`AGENTS.md`'s Testing section is where a lane reads "how do I test my feature
in isolation" first; this table is the engineering-level index of where that
tooling lives, kept beside this folder's other test/buildgates entries rather
than duplicated.

### The CI image

CI's stages run in one image, built from `build/docker/devcontainer/Dockerfile`
at the commit under test (handbook `ci-standard.md` §2, `devcontainer.md` §1).
`.devcontainer/` is a different image, the developer's VS Code container and
the Docker `test/integration/api` stack, and runs none of these stages.

| Stage | Holds | Runs |
|---|---|---|
| `base` | the Go toolchain by digest, gcc for `-race`, a non-root user, every module's verified cache; `GOPROXY=off` | nothing itself |
| `toolchain` | base plus every tool the Makefile calls, at the versions in `scripts/tool-pins.sh`, and `ssh-keygen` (Alpine's `openssh-keygen`, not the ssh client or server), which `make test-release-alpha` signs its test tags with | `make build-server build-cli-headless`, `make release-cross PLATFORM=linux/arm64`, `make test`, `make security`, `make deps-check` |
| `integration` | base plus the source, its suites compiled at build time | `make integration-test IT_FEATURE=<feature>`, hardened by `compose.yaml` |

`scripts/tool-pins.sh` is the one pin file: `make docker-build` passes its
values to the Dockerfile as build arguments, the Makefile includes it, and
`make check-tool-versions` holds `.devcontainer/`'s ARG defaults to it and
fails on a tool pin in ci.yml or an assignment in the Makefile, either of
which would be a second pin. Module credentials reach the build only as the
`gomodauth` BuildKit secret (`$(stage-gomodauth)`). In CI the `docker-build` job, and off a push to `main`
`scripts/ci-registry.sh local` in a consumer, writes `GOPRIVATE` and the
`GIT_CONFIG_*` variables to a file under `$RUNNER_TEMP`, passes
it as `MODULE_ENV_FILE` (handbook `devcontainer.md` §7) and removes it after
the build; outside CI the recipe reads them from the environment. No layer, no
command line and no log line holds them: the recipe is silent, printing only
which source it used.

**Every job that can print a forge credential masks it first.** The runner
injects the credentials into every job, and the `docker-build`, `lint`,
`lint-gates`, `image`, `coverage`, `test-clusterproof`, `coverage-fts`,
`test-tags`, `test-sdk`, `test-typescript`, `arch-gates`,
`integration-features`, `integration-standalone`, `fips` and `soak` jobs, each of which runs a make
target, a script, npm or curl, open with "Mask the forge credentials", which
registers each credential value with the runner's log masker before any other
step can print it. `changes` and `ci-gate` run only inline steps that print
the changed file names and the jobs' results, and carry no mask. No step
prints the environment. `test/buildgates/ci_logs_test.go` fails when the mask
is not the first step of a job in its `credentialJobs` list, when a ci.yml job
is in neither that list nor `unmaskedJobs` (the two above), when a step prints
the environment, or when ci.yml's `GO_BUILD_TAGS` differs from
`make -s print-build-tags`.

**Findings are reported by path, line and rule only.** The tree scanners
(`make check-publication-bound`, `make check-public-releasable`), `make lint`'s
golangci-lint (`--output.text.print-issued-lines=false`) and actionlint
(`-oneline`) name a finding without quoting the line it is in, so a log never
repeats the value a scanner exists to keep out of the tree.

**The images take one of two paths, and only a push to `main` publishes.**
The workflow env `CI_PUBLISH` is true on a push to `main` alone; every step
that logs in, pushes or pulls runs under `if: env.CI_PUBLISH == 'true'`, and
its counterpart on every other event, a pull request, a `workflow_dispatch`
or the nightly `schedule:`, under `if: env.CI_PUBLISH != 'true'`.

| Event | `docker-build` | Consumers take the image | Hold |
|---|---|---|---|
| push to `main` | on the `release` runner: builds both stages, logs in, pushes, outputs the digests | `scripts/ci-registry.sh pull <digest> <hold>` (logs in) | a container of the digest |
| pull request, `workflow_dispatch`, nightly `schedule:` | on `bigdisk`: builds both stages; no login, no push, empty outputs | `scripts/ci-registry.sh local toolchain\|it <hold>` on the job's own runner (no login) | toolchain: a container of the image ID; integration: `make docker-build-it`'s `IT_HOLD`, a container of the tag |

**On a push to `main` the images are built once per run and pulled by
digest** (DS-3516). The `docker-build` job builds both stages
(`make docker-build`; `make -s toolchain-image` prints the toolchain tag) and
pushes each to the forge's registry (handbook `devops.md` §2): the Gitea
registry, which the release runner's `RELEASE_REGISTRY` names (below) and so
is never written in a tracked file, or `ghcr.io` when the run is on GitHub,
as `qrdb-ci-toolchain` and `qrdb-ci-it`. The images go under the namespace of
the credential that pushes them: on the forge the release credential's own
namespace, the one namespace it can write (a push under the repository
owner's was refused, main run 71805), and on GitHub the repository owner's,
with the run's own token. `scripts/ci-registry.sh` takes the owner from the
runner's environment and names it in no tracked file; its `check-ref` accepts
a reference under the repository owner, or under the release credential's own
namespace where that credential is in the environment, and no other. `scripts/ci-registry.sh push` tags each
`<content-hash>-<trust>`, the 16-hex hash the Makefile built it under and the
trust class (`main`), and prints the digest the registry reported. The job
validates each against `^[^@ ]+@sha256:[0-9a-f]{64}$` and passes them on as
the outputs `toolchain_ref` and `it_ref`. There is one tag per content hash and
trust class, so a rebuilt tree pushes a new version under the same tag; the
registry's cleanup rule for the owner's container packages removes the
superseded versions, and no job deletes from the registry.

**The push runs on the release runner, which alone holds the registry
credential** (ruling 169). On the forge a push to `main` places `docker-build`
on the runner labelled `release`; every other event places it on `bigdisk`,
and github.com keeps `ubuntu-latest`. `runs-on` cannot read `env`, so the job
restates `CI_PUBLISH`'s condition from the `github` context, and it stays one
job definition.

**The release runner's credential contract is the three names its runner env
file exports** (ruling 173): `RELEASE_REGISTRY`, the
registry's host[:port], which the push goes to and the digest reference names;
`RELEASE_REGISTRY_USER`; and `RELEASE_REGISTRY_TOKEN`. The forge push in
`scripts/ci-registry.sh` reads those names and no others; it is not a
repository secret, and no new identity or token exists for it. An empty name,
or a `RELEASE_REGISTRY` that is no host[:port], fails the push with a typed
`::error::` naming the variable and the release runner's credential.
`RELEASE_REGISTRY` is the same registry the consumers derive from
`github.server_url`: a pull refuses a reference in any other registry, and the
push refuses a `RELEASE_REGISTRY` whose host[:port] is not that one, with a
typed `::error::` naming both, so a misconfigured release runner fails once,
in `docker-build`, rather than in every consumer. No step,
job or workflow `env:` in ci.yml sets any of the three, since an entry would
mask the runner's. A name change to the contract is an Infrastructure PR to
claude-agents, announced before it lands on the claude-agents issue that
records the runner's contract, and this repo follows it. On github.com the push step hands the run's own
`secrets.GITHUB_TOKEN` on under `RUN_REGISTRY_*` as `CI_REGISTRY_*`, empty on
the forge. On github.com every consumer's pull logs in to GHCR with the run's
`secrets.GITHUB_TOKEN` (the runner masks it), set only on the steps that log
in; on the forge every consumer pulls anonymously, with no `docker login`,
because the release credential's namespace is public and the run's token,
which the forge issues with an empty scope, cannot log in to its registry.
Every consumer stays on `bigdisk`. The job's `permissions:` grant `packages: write` to
`docker-build` and `packages: read` to every consumer.
`test/buildgates/ci_image_digest_test.go` fails when `docker-build`'s
`runs-on` is anything but that one expression, when any `env:` in ci.yml sets
a `RELEASE_REGISTRY*` name, or when any other job can run on `release`.
`scripts/ci-registry.sh` feeds the credential to `docker login --password-stdin` in a
private `DOCKER_CONFIG` that it deletes on exit, so the credential reaches no
other job on the same Docker host. A refused login, push or pull fails the job
with a typed `::error::` line that names the credential it used (the release
runner's, or the run's token) and never its value, or, for a pull on the
forge, says the pull was anonymous: there is no local rebuild
to fall back to.

**On every other event nothing is published** (handbook `ci-standard.md` §9).
`docker-build` still builds both stages, so a broken Dockerfile fails the
pull request in the PR tier, but it neither logs in nor pushes, and its outputs
are empty. Each consumer instead runs `scripts/ci-registry.sh local`, which
asks the Makefile for the family's content-hash tag (`make -s toolchain-image`,
`make -s it-image`), builds the image only when the job's own runner's daemon
does not hold that tag, holds it, and prints the reference the stages run. The
toolchain image is held and run by its image ID, which stands in for the digest
a push would have minted: a tag can move under a concurrent build, an ID
cannot. The integration image is run by its tag, because compose's `image:`
resolves a name and an ID is not one; `make docker-build-it IT_HOLD=<hold>`
creates the hold from that tag, the container the reaper and
`make integration-test` look for. The tag is a hash of the build inputs, so
every job of a run, on any runner, runs an image of the same tree. The module
credentials reach such a build as they do `docker-build`'s, in a
`MODULE_ENV_FILE` under `$RUNNER_TEMP` that the script removes on exit; the
registry token is on none of these steps.

Eleven jobs consume the images, each before its first stage: `lint`, in the
PR tier (`make lint`, `make deps-check`, a lint-class gate on the PR tier
from ci-template v1.28.0, and, on a push to `main` and a dispatch,
`make lint-sweeps`), `lint-gates`, in the PR tier (`make lint-gates`, its own
job from ci-template v1.27.0), the five unit-suite jobs, in the code tier,
one suite each (ruling 211): `coverage` (`make coverage`),
`test-clusterproof`, `coverage-fts`, `test-tags` and `test-sdk`, and `fips`,
which take the toolchain image; `image`, in the code tier (the build-tag
assertion, the builds and, in the full tier, the linux/arm64 release
cross-build, security and the trivy image scan), which takes the
toolchain image and, in the full tier, the integration image; the
`integration-features` legs, which take the integration image; and the soak,
which runs on a `workflow_dispatch` or the nightly `schedule:`, never on a
push, and so always takes the local path. On a push to `main` a consumer
pulls exactly the digest it was handed with `scripts/ci-registry.sh pull` (at
most 3 attempts, 10 s and 20 s apart). The ci-template runs the stages but
lint-gates in one image job; this repository splits it because together
the stages outlast one job's 90-minute limit on a loaded runner, and each job
has a limit sized to its own stages (architecture.md, "CI tiers"). A consumer runs on any
runner: nothing places it beside `docker-build`, and none of them builds or
probes for an image except through `scripts/ci-registry.sh local`. The
Makefile's defaults, the content-hashed local tags, are unchanged for a
developer host; a digest reference makes `make integration-test` require the
pulled image rather than build it, and `make docker-build-it` refuses one.
`test/buildgates/ci_image_digest_test.go` names those eleven jobs
(`digestConsumersWant`), fails when discovery stops finding one, holds every
consumer to both paths (a case drops each named job's pull, then its local
build, and requires the gate to report the job), and every step of every job that logs in, pushes, pulls or carries the
registry token to `CI_PUBLISH`; `test/buildgates/ci_registry_test.go` runs
`scripts/ci-registry.sh local` against a stub `docker` that fails on any
registry verb.
Each job then runs each stage in a toolchain container through
`scripts/in-toolchain-image.sh`. That container
holds the job's CPU bound: the job container's cgroup
`cpu.max`, rounded up (`nproc` with no quota; `IN_IMAGE_CPUS` overrides),
becomes `--cpus N`, `GOMAXPROCS=N` and `GOFLAGS=-p=N`, never a hardcoded
number, and its memory bound: the job container's cgroup `memory.max` (8g
with no limit; `IN_IMAGE_MEMORY` overrides) becomes `--memory` and
`--memory-swap`, so the stage cannot swap the runner host. It runs under the
runner host's `ci.slice` (`CI_CGROUP_PARENT`; empty passes no parent). The jobs also need the `changes` job: on a docs-only pull
request, one whose every changed file matches `DOCS_ONLY_PATTERN` (markdown,
or under `docs/`), `lint` runs lint and deps-check and `lint-gates` the lint gates, both succeed, and
`image` is skipped with the unit-suite jobs and `test-typescript`, the code
tier. On such a run, a
pull request and so the local path, `docker-build` builds the toolchain image
alone and pushes nothing (architecture.md, "CI tiers").
`changes` is never docs-only off a pull request or under
`ci:full`. A runner reaches
its Docker daemon through a socket, so the script copies the checkout into the
container rather than bind-mounting it. Its files sit where a host's do:
`/tmp`, which holds `HOME`, go's `$WORK` and every `t.TempDir`, is on the
container's own layer, not a size-capped tmpfs that a full `make test` fills,
and `/dev/shm` is 2 GiB, not Docker's 64 MiB. Build and test run with no
network: the container's own namespace has only loopback, which a one-shot
`--rm` container joining it once the stage has started (granted `NET_ADMIN`
and nothing else) marks multicast-capable and routes `224.0.0.0/4` over before
it exits; the stage's command waits for that route, `MULTICAST_WAIT` seconds
at most (default 60). So the multicast discovery tests join their group as
they do on a host, nothing leaves the namespace, and no container holds the
namespace but the stage: the holder it replaced slept until the script's EXIT
trap removed it, and every job killed first left one running. The script
traps `INT`, `TERM` and `HUP` as well as `EXIT`, and labels both containers
`qrdb.ci.job=<GITHUB_RUN_ID>-<GITHUB_JOB>`, so a sweep tells a job's leftover
from a live one without guessing by age; `make test-in-toolchain-image` proves
each signal path on a stub `docker`. `make security`, full tier only, and `make deps-check`, every run, run on the bridge network, because
govulncheck reads the vulnerability database and deps-check asks the module
proxy.

**The Docker integration stack has one network namespace and, by default, no
network** (`test/integration/api/docker-compose.yml`, run by
`make integration-test-standalone`, `-cluster` and `-docker`). Its `netns`
service holds the namespace with `network_mode: none`. The two servers and
the test runner join it with `network_mode: "service:netns"` and talk over
loopback: `qrdbd` on 63001-63005, `qrdbd-shamir` on 64001-64005, and the
runner reaches them at `127.0.0.1:<port>`. Nothing in the stack needs a
network at run time, because the image fetched its modules at build. A
service per bridge attachment gave the host one veth per container start and
stop. On a many-core runner each veth add or remove is a storm of hundreds of
uevents, one per tx and rx queue. The stack now creates no veth. The opt-in
Ollama overlay (`docker-compose.embedding.yml`) pulls its model at run time,
so it gives the holder one bridge attachment, which is one veth for the
stack.

**The stage caches live in named volumes on the runner's Docker host,** never
in a stage container's layer or its `/tmp` (handbook `ci-standard.md` §6). Every stage mounts `<prefix>cache-<class>-gobuild` as
`GOCACHE` and `<prefix>cache-<class>-golangci` as `GOLANGCI_LINT_CACHE`, and
a bridge stage mounts `<prefix>cache-<class>-gomod` as `GOMODCACHE`; a stage
with no network keeps the image's own read-only module cache, so it still
proves the image builds offline. The volumes are kept across jobs, one set per
repository, trust class and Docker host. The class is `CI_CACHE_CLASS`:
`main` for a push to `main`, `pr` for every other event, so a pull request
never writes a cache that `main`'s build reads. After the image build,
`scripts/trim-ci-caches.sh` bounds the repository's cache volumes on the host
to `CI_CACHE_MAX_GIB` (32 GiB by default), and it never fails the job. Over
the bound, it first deletes the build and golangci-lint cache entries unused
for three days, then one day, 12, 6 and 3 hours, measuring after each, and
only then removes the largest volume no container mounts; the module cache is
never thinned, only removed whole, because removing a volume empties the build
cache and the next `make lint` compiles the tree from nothing. `make test-trim-ci-caches`, in
`make lint-gates`, tests the order on a stub `docker`. The build cache also
holds `go test`'s result cache: `make coverage`'s bulk run leaves out
`make test`'s `-count=1`, so a package unchanged since a passing run replays
that result and its coverage profile, and the packages
`scripts/coverage-merge-subprocess.sh` covers run beside the bulk run rather
than after it.
`scripts/reap-stale-ci-stacks.sh` removes only compose-labelled and anonymous
volumes, never these. `CI_CACHE=off` mounts none; a bridge stage then takes a
writable copy of the image's module cache instead.

Last, in the full tier only,
`make trivy-image TRIVY_IMAGE_INPUT=it-image.tar` scans the
integration image the job holds, saved to `it-image.tar` by its digest
reference on a push to `main` or by its content-hash tag otherwise, so the
scan reads the bytes the legs run, and fails on a fixable HIGH or CRITICAL
finding; the job removes the tarball and releases its holds on both images
(`scripts/ci-registry.sh release`) under `always()`.
It takes the two image-only flags of handbook `devops.md` §8:
`--ignore-unfixed`, because a base image carries advisories with no fixed
package, and `--skip-dirs` over the image's Go module cache, `/gomodcache`
(the handbook names the template image's `/opt/go/mod`), whose third-party
releases, fixed by `go.sum`, carry their own doc examples and test-fixture
keys; a secret in the image's own layers still blocks. It keeps
`--skip-db-update` behind the database-age certification, so a stale database
fails. `make trivy` (trivy fs) and `make security` never take
`--ignore-unfixed`.

`make deps-check` (`scripts/deps-check.sh`) is the latest-version check
(`ci-standard.md` §1, `devops.md` §9): every direct requirement of every
tracked module is at its latest release, or its `go.mod` require line carries a
`//` comment saying why it is held. An unheld upgrade published less than seven
days ago (`MIN_AGE_DAYS`, by the `Update.Time` `go list -m -u` reports) is
reported with its age and does not fail the check, since no one could yet have
adopted it; one seven or more days old fails, and one whose time cannot be read
is treated as old. A module whose lookup fails, offline or without credentials,
fails the check: the question was not answered.
CI runs it in the `lint` job after `make lint` on every run, a docs-only pull
request included (ci-template v1.28.0, ruling 219(3)): it needs no build
output, only `go.mod` and the module proxy, so a module behind its floor fails
the pull request rather than `main` after the merge.
`scripts/deps-check_test.sh` (`make test-deps-check`, in CI's `lint-gates` job) proves
each branch with a fake `go`, and the publish time with the real one over a
`file://` proxy.

### Per-run integration images

A Docker integration run has its own compose project and builds and runs its
own image tag, derived from `INT_PROJECT`, and removes it when the run ends.
A shared tag would let two runs on one host rebuild it between each other's
build and `up`, so a test container's CLI could drive a server baked from
another tree. The compose files require
the tag (`${QRDB_INT_IMAGE:?...}`) and refuse every command without it, `down`
included, so a stack is always driven through `make`; to tear one down by hand,
run `make integration-down INT_PROJECT=<project>`. The devcontainer keeps
its own stable `go-qrdb-dev:latest`.

`INT_PROJECT` defaults to `qrdb-int-<host>`, and in CI to
`qrdb-int-<host>-<run id>-<attempt>-<feature>`, the feature being the
`integration-test-<feature>` goal the job runs. The host alone is not an
identity: a runner that runs jobs on the host gives them all its hostname, and
the per-feature legs below run several at once there. Each leg, and each
attempt of it, owns its project, image tags and teardown;
`test/buildgates/integration_job_names_test.go` derives the names for every
leg on one host and fails when two match. Outside CI, two concurrent runs on
one machine still need distinct `INT_PROJECT` values.

### Per-feature integration jobs

CI runs the self-spawned integration suites (`test/integration`, no
Docker) as `integration-features` in
`.github/workflows/ci.yml`: six matrix legs, the shards, each carrying a
literal feature list (`features:` in its `include` entry, handed to the step
as `IT_FEATURES`) and running
`make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image`.
That target runs `make integration-test IT_FEATURE=<feature>` for each feature
in the list, one after another, with `make -k` semantics: a failing feature
does not stop the rest, each prints `==> integration-test-features: PASS|FAIL
<feature> <seconds>s` as it ends, a summary repeats every line and names the
failing features, and the shard fails if any failed. A name that is not a
feature with integration tests is a FAIL, not a zero-test pass. On a host,
`make integration-test-features IT_FEATURES="<feature> ..."` (the default
`IT_FEATURES_IN=host`) runs the same loop over `make integration-test-<feature>`.
The loop is the Makefile's, not the workflow's, because the integration image
has no `make`; each feature is its own `compose run` in it.

`make integration-test IT_FEATURE=<feature>` runs on the integration image
(`build/docker/devcontainer/Dockerfile`, stage `integration`: base plus the
source) that, on a push to `main`, `docker-build` pushed and the shard pulled
by digest, and on any other event the shard took from its own runner by
content-hash tag ("The CI image", above). That target
runs the feature's selection, the
same one `make integration-test-<feature>` runs on a host, inside the hardened
`integration` service of `build/docker/devcontainer/compose.yaml`: read-only
root filesystem, every capability dropped, `no-new-privileges`, `/tmp` (also
`HOME`) an anonymous volume on the daemon's disk, removed with the container,
no network, and `GOMAXPROCS=8`. The suites run without `-race` (the
Makefile's `INT_GOTEST`): race detection is the unit suites' job, which keep
it, and under `-race` the multi-node clusters cost several times the CPU and
wall time. Within a leg, `-parallel` is the Makefile's `INT_PARALLEL`, 5,
one per transport: a single-node test whose daemon shares no port or path
with another calls `t.Parallel()`, and a per-protocol test whose subtests
each need a fresh daemon (an init, seal or drain is one-shot) runs those
subtests in parallel, while a per-protocol test whose subtests are
independent shares one daemon across every transport. The shards run in
parallel, each under its own `timeout-minutes: 45`, and
`feature_jobs_gate_test.go` refuses a limit over 45 (#1286). On a light fleet
the limit is 30, about three times a shard's expected time, and the worst
shard of main run 70592 took 22 minutes; in run 70834, on one overloaded
runner host while the other was off, five of six shards hit 30. Both numbers
go back to 30 when the second host returns. GOMAXPROCS=8 reaches `go test`,
the harness's `go build` and every spawned qrdbd through the compose
`integration` service, and the per-feature `go test` takes `-p 4` (the
Makefile's `INT_PROCS`, #1286); a shard runs one package, so `-p` bounds only
its build, at four packages at once on a shared runner host. A
feature whose `features.mk` entry is empty is in no shard: it would run zero
tests and pass. `fail-fast` is `false`, so one red shard does not cancel the
others.

Sharding exists because each leg pays the checkout, the credential mask, the
reap, the image resolve and hold, and the teardown before and after its
tests: with one leg per feature, a feature whose tests take a quarter of a
minute held a `bigdisk` runner slot for well over a minute. The lists are
balanced longest-first by the per-feature job times of main run 65165, with
setup assumed to be about 60 s of each leg (counted once per shard) and
`barrier-seal` taken at its first-attempt 300 s rather than the 585 s its
rerun recorded:

| Shard | Features | Expected (s) | Limit |
|---|---|---|---|
| 1 | audit databases distributed-locks multi-tenancy time-series | 552 | 30 min |
| 2 | backup-restore capabilities ceremony clustering firstboot tenant-dek | 551 | 30 min |
| 3 | barrier-seal certificate-authority guarded-swap key-wrapping rag trust-anchors watch | 563 | 30 min |
| 4 | api-server auth-factors replica-forwarding run-modes secondary-indexes storage-engines volume-pools | 551 | 30 min |
| 5 | cluster-verification collections embeddings identity-rbac key-value shamir-custody vectors | 550 | 30 min |
| 6 | coordination credentials etcd-compatibility leases row-locking sql transactions trust-bootstrap | 562 | 30 min |

That run's 40 feature legs held 5654 slot-seconds (1.57 slot-hours; 5369, or
1.49, with `barrier-seal` at 300 s); the six shards are expected to hold 3329
(0.92 slot-hours), with the wall time of the job going from its slowest leg
to its slowest shard, about nine and a half minutes. A new feature goes in the
`features:` of the shard with the least expected time; rebalance when a
shard's measured time drifts well past the others.

Each leg names its own compose project, `IT_PROJECT`, from the run, the attempt
and the shard, under the `qrdb-ci-` prefix, so legs running at once on one
runner never share a stack. The image is the opposite: every leg of a run
runs an image of the one tree. On a push to `main` the leg's `IT_IMAGE` is
`docker-build`'s `it_ref` output, a digest reference into the forge's
registry (DS-3516); on any other event it is the Makefile's
content-addressed default, `qrdb-ci-it:<hash>` (`make -s it-image`), as on a
developer host ("The CI image", above). The pull or the local build step sets
it for the steps after it; the job sets none. The hash covers
the build arguments that change the image and of the build context
(the commit's tree, plus, on a dirty checkout, the diff and the untracked files
`.gitignore` does not exclude), which `make docker-build-it` builds when
`docker image inspect` misses it. Given a digest reference instead,
`make integration-test` builds nothing: it requires the image on the daemon
and fails, naming `scripts/ci-registry.sh pull`, when it is not there. So a
leg runs on whichever runner takes it, never on a tree another runner built,
and never rebuilds an image another runner pushed.

Each leg runs six steps. The first after the checkout is "Mask the forge
credentials", which registers every `GIT_CONFIG_*` value, and the token inside
any URL among them, with `::add-mask::` before any step can print one. It then
runs `scripts/reap-stale-ci-stacks.sh qrdb-ci-`, which removes `qrdb-ci-`
projects older than the longest job can live (about three hours), and
the images no job holds that were created or last tagged more than two hours
ago (`REAP_IMAGE_MIN_AGE`, 7200 s; the later of the two, because a rebuild
from a full cache keeps the old `.Created` but re-tags the image): the local
`qrdb-ci-it` tags, and the `qrdb-ci-it` and `qrdb-ci-toolchain` images pulled
from the registry, by registry tag or, when only a digest names one, by
digest. Then, on a push to `main`,
`scripts/ci-registry.sh pull "$IT_REF" "$IT_HOLD"` pulls the digest, `IT_REF`
being `it_ref`, and on any other event
`scripts/ci-registry.sh local it "$IT_HOLD"` builds or reuses the image on
the leg's runner; each holds the image and sets `IT_IMAGE`, and
`make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image`
runs every feature of the shard on it. It ends with a teardown step under
`if: always()`, `make docker-down-it`, which removes that leg's project and
its hold, and never the image, on a pass, a failure or a cancellation.
Removing names nobody created is not an error. No workflow runs `docker
compose` itself (`integration_image_tag_test.go`); `feature_jobs_gate_test.go`
requires the six steps, the mask first, the pull under `CI_PUBLISH` with its
`IT_REF` bound to `it_ref`, the local build under its negation, no job-level
`IT_IMAGE`, the project name, and the `always()`. The image job reaps under `qrdb-ci-`
too, so a runner that only runs the PR tier still drops old images.

The image's age does not protect a live job's image: a pull by digest records
no tag time, and an unchanged tree pushes nothing new, so the image a job has
just pulled can carry any age, and another job's reaper or cache trim on the
same Docker host could remove it between the pull and the job's last stage.
What protects it is a hold: every job that takes an image names one per image,
`qrdb-ci-hold-<run>-<attempt>-<job or shard>` (`IT_HOLD`, and in the
toolchain consumers `TOOLCHAIN_HOLD`), and `scripts/ci-registry.sh pull`
creates a container of that name from the pulled image, never started, before
it returns. `scripts/ci-registry.sh local` holds the same way: the toolchain
image from its image ID, the integration image through
`make docker-build-it IT_HOLD=<hold>`, from its tag; an image built or found
on the local path is protected exactly as a pulled one is. The job's `always()` teardown removes it
(`scripts/ci-registry.sh release`, or `make docker-down-it` in a leg and the
soak). The reaper asks `docker ps -a --filter ancestor=<name>` before it
removes any name of an aged image and keeps every name of one a container
uses, so it cannot remove a held image or any reference to it. `docker image
rm` alone would not be enough: it refuses only an image's last name and untags
any other, so the producer's local tag plus the registry references would let
it drop the digest a holding job's next `docker create` runs. If the reaper removes the image between
the pull or the local build and the hold, `docker create --pull never` fails
and the next attempt pulls or builds it again. `docker-build` holds what it built only from the build to the
push and releases that hold under `always()`; nothing hands a hold from one job
to another, since each consumer takes its image on its own runner. A job that dies
before its teardown leaves a `qrdb-ci-` container in the created state, which
the reaper removes once it is stale, and the image in the same pass.
`feature_jobs_gate_test.go` runs the build recipe and the reaper against a stub
`docker` that, as dockerd does, untags one of several names of an image freely
and refuses only the last name of one a container uses, and fails if the
reaper removes a held image or any of its names, local or pulled.

A feature that selects nothing says so and passes, before any image is built,
as `integration-test-<feature>` does. On a host, `make integration-test-<feature>`
and `make integration-down-<feature>` still run a feature without Docker;
`integration_job_names_test.go` holds those two goals to one set of names.

The union of the shard lists is `make -s print-integration-features`, each
feature in exactly one shard. Gitea cannot expand a matrix computed at run
time (handbook devops.md §1, "Write matrices literally"), so ci.yml spells the
lists out and `test/buildgates/feature_jobs_gate_test.go` fails on a feature
no shard runs, a feature two shards (or one shard twice) run, and a listed
name the Makefile does not print, naming the feature and the edit that fixes
it. A new test needs no list edit; a new feature, or an entry that gains or
loses its last file, needs one shard's `features:` changed.

`make integration-test-<feature>` is the Makefile's `integration-test-%`
pattern rule, and GNU make prefers an explicit rule to a pattern rule. An
explicit target named for a feature therefore hides that feature's selection,
from `make integration-test-features` on a host and from anyone typing the target, which is why the Docker RAG run
with the Ollama sidecar is `integration-test-rag-ollama`.
`test/buildgates/feature_target_shadow_test.go` (in
`make check-integration-feature-coverage`) fails when any `features.mk` feature
has an explicit rule of its own. Give a new Docker or opt-in target a name that
is not a feature's.

`ci-gate` needs the job once. A matrix job's `needs.<job>.result` is the
aggregate over its legs, `failure` when any leg fails, so one failed leg turns
the gate red; `ci_tiers_test.go` runs the gate's script with a failed, a
cancelled and a skipped leg. The job is in the full tier (architecture.md, "CI
tiers"): every leg shares its full-tier `if:`, so a plain pull request skips
them together and `ci-gate` excuses that skip; a push to `main`, the nightly
schedule, a dispatch or a `ci:full` pull request runs all of them from its next push, update-branch or dispatch (a label change alone starts no run).
No leg starts before `docker-build`, `test-typescript` and `arch-gates` have
passed: the job needs those three, as does `integration-standalone`, and not
`lint`, `lint-gates`, `image` or the unit-suite jobs, so the legs start once the images are built and run beside
those jobs' checks; `ci-gate` still fails the run on any red check. The label goes on once, when the pull
request is green on the PR tier and ready to merge (architecture.md, "CI
tiers").

Each leg runs on a shared `bigdisk` (bare-metal) runner, the label that carries
Docker and the disk the integration image needs (`ubuntu-latest` on github.com), as
the image job and the standalone and soak jobs do. Load on a shared runner breaks a
3-node cluster's elections and a CLI call's 10-second deadline first. The
`integration` compose service carries the CPU half of the cluster
stage's budget (below) as `GOMAXPROCS=8`: the `go test` process and every
qrdbd and qrdb process the harness spawns inherit it, rather than sizing
themselves to the runner's thread count. The selection runs `go test -timeout=30m` per package, under the job's limit, so a hung test
still dumps its goroutines.

Raft timing is not part of any budget. qrdbd takes no election, heartbeat or
tick setting from configuration: the values are code defaults
(`pkg/cluster/raft_tuning.go` and go-quicraft's), so CI cannot select a slower
profile.

### FIPS 140-3 legs

The `fips` job's legs (#1230) are balanced from the measured wall times of
run 68401, the first run of the seven-leg split, whose legs took: `api` 7.3
minutes, `sdk` 17.4, `rest` 18.2, `sdk-transport` 18.3, `server` 30.2, `seal`
36.5 and `service` 42.8. A leg's time was its slowest package plus 3 to 4
minutes of build and setup, so the three legs over 30 minutes were each one
package: `pkg/tenant` (38.6 minutes, in `service`), `pkg/seal` (32.9) and
`pkg/server` (25.7). Each now runs in a leg of its own (`tenant`, `seal`,
`server`), and the rest of their old legs went to `auth`, `service` and
`api-keys`; the table with each leg's packages, slowest package, expected time
and limit is docs/seal/fips140-mode.md, "The legs". Each leg's
`timeout-minutes` is its matrix entry's `minutes`, its expected time with room
for a loaded runner and at most 45, and go test's per-package budget is that
less 5 minutes, so a runaway package is named before the leg is killed.
No assignment of whole packages brought `pkg/tenant` and `pkg/seal` under 30
minutes, and `pkg/server` left its leg no headroom, so each is split by test
name over two legs (#1234): `tenant-lifecycle` and `tenant`, `seal-barrier`
and `seal`, `server-raft` and `server`. The first leg of each selects tests
with `go test -run` (the Makefile's `FIPS_RUN_<leg>`), the second skips what
the first selects (`FIPS_SKIP_<leg>`, `-skip`), so a new test runs in the
second until a pattern names it, and `test/buildgates/fips_split_test.go`
fails when a top-level test of a split package runs in no leg or in two. The
packages' tests run one after another and their cost is the FIPS KDF spread
over hundreds of tests (6.0 and 7.0 minutes without FIPS mode for `pkg/seal`
and `pkg/tenant`, 22 and 27.5 under it on one 80-core host), so a name split
halves a leg's time; the one outlier, a 195-second race test on the
production KDF, now uses the cheap test parameters. Rebalance from each leg's "Package results and slowest tests"
step when a leg's measured time drifts toward its limit.

### Cluster stage, locally

`make integration-test-cluster` runs the whole `test/integration` package in
one compose container, for a local run; CI does not run it. The suite is serial
(`-p 1`, `-parallel 1`), so its run time is the sum of its tests. The Makefile
sets its budget:

| Makefile variable | Default | Applied as |
|---|---|---|
| `CLUSTER_CPUS` | `8` | `cpus:` on the `integration-tests` compose service (docker `--cpus`), lowered to the docker host's CPU count when the host has fewer. A whole number; `0` is no cap |
| `CLUSTER_MEM_RESERVATION` | `8g` | `mem_reservation:` on the same service (docker `--memory-reservation`): a soft floor under host memory pressure, never a limit that kills a test |
| `CLUSTER_PARALLEL` | `1` | `go test -parallel`. The stage already runs `-p 1` over one package, so this bounds the one `t.Parallel` suite, which otherwise boots five 3-node clusters at once |
| `INTEGRATION_RUN` | empty | a `-run` expression narrowing the stage; empty runs every test |
| `INTEGRATION_GO_TIMEOUT` | `100m` | `go test -timeout` for the package, read by the `integration-tests` compose service, so a hung test still dumps its goroutines |

The budget is on the compose service: that container holds the `go test`
process and every qrdbd and qrdb process the tests spawn, and under the CPU cap
each of those Go processes sizes `GOMAXPROCS` to the cap. The other compose stages
leave the budget variables unset, which docker reads as no limit.

`CLUSTER_CPUS` is an upper bound, not a requirement. dockerd refuses a
`--cpus` above the CPUs it has ("range of CPUs is from 0.01 to 4.00"), so a
fixed 8 would not start the container on a 4-CPU host and the stage would run
no test.
`scripts/cluster-cpus.sh` reads the daemon's own count (`docker info` NCPU,
the figure dockerd checks against, not the calling container's `nproc`),
applies the smaller of the two and logs both:

```
cluster-cpus: capping the cluster stage at 4 CPUs (CLUSTER_CPUS=8, docker host has 4)
```

A daemon whose count cannot be read fails the stage rather than running
uncapped. Nothing else assumes 8 CPUs: `CLUSTER_PARALLEL` is 1 regardless, and
`INTEGRATION_GO_TIMEOUT` is sized for a 4-CPU budget. `make test-cluster-cpus` (Lint) runs the script against a stub
docker for the lowering, the pass-through and every refusal.

### Batch workflow

`.github/workflows/batch.yml` is long-running CPU work that never gates a
merge: it is `workflow_dispatch` only and runs on runners labelled `batch`,
which no other workflow uses (`.github/actionlint.yaml` declares the label).
Dispatch it on `main` after a merge:

```
POST /repos/{owner}/{repo}/actions/workflows/batch.yml/dispatches
     {"ref":"main","inputs":{"jobs":"all"}}
```

`jobs` picks `all`, `fuzz`, `race-full`, `soak` or `bench`; `fuzztime`
(default `20m`) and `soak_minutes` (default `60`) size the long jobs.

| Job | Runs |
|---|---|
| `fuzz` | every `Fuzz*` target in the root module under `BUILD_TAGS`, each for `fuzztime`, over four shards |
| `race-full` | `make test`'s packages under `BUILD_TAGS` with `-race -count=3` |
| `soak` | `make integration-test-cluster` in a loop for `soak_minutes`, then `make integration-down` under `always()` |
| `bench` | every benchmark under `./pkg/...` and `./cmd/...` with `BUILD_TAGS`, `-benchmem -count=5` |

A defect is reported on a log line that starts with `BATCH-FINDING` — a fuzz
crasher (with its new corpus file), a data race or a failing test in any of
the three race runs, or a failed soak iteration — and the logs are uploaded as
artifacts when a job fails. The bench job always uploads `bench-<sha>.txt`; a
regression is found by comparing it with the previous dispatch on `main` using
`benchstat`, since a run cannot read another run's artifact.

## Integration Target

Run `make integration-test-release` for this feature. The target intentionally selects zero Go integration tests because release is a tag/workflow process; the coverage accounting row is `test/integration/COVERAGE.md` under `release`.
