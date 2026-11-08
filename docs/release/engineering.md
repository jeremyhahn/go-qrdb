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
| `.github/workflows/release.yml` | the Release workflow: `push: tags: ["v*"]` only, `validate`, the gate jobs (`gate`, `gate-sweeps`, `gate-tags`, the `coverage` legs and `coverage-merge`) and `publish`, no bot secret of any kind |
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
- **The gate jobs run the same commands as the lint, build and unit-test
  stages of ci.yml's `lint`, `lint-gates`, `image` and unit-suite jobs**
  against the exact tagged tree, not a re-run of every ci.yml step — see
  architecture.md for why the rest is skipped. They run side by side, each
  under its own limit on a hosted runner (DS-7377): `gate` runs `make fmt`,
  `make vet`, `make lint`, `go build ./...` and `make build-server`;
  `gate-sweeps` runs `make vet-tags` and `make secure-analyzer`; `gate-tags`
  runs `make test-tags`; the `coverage` legs run `make coverage-leg` over
  `make -s print-coverage-legs`, and `coverage-merge` runs `make
  coverage-merge`, the one repo-wide coverage gate over every leg's profile. The two
  sweeps, `make vet-tags` and `make secure-analyzer`, run here on every tag
  although ci.yml runs them (`make lint-sweeps`, in the `lint` job) only on its
  nightly `schedule:` event, cron `0 4 * * *`, never on a pull request, a push
  or a dispatch (ci-template v1.30; the one schedule trigger, ruling 216(1)).
  `make lint` includes `lint-workflows`, which runs actionlint over
  `.github/workflows/` and `.gitea/workflows/` after asserting the binary is
  the pinned `ACTIONLINT_VERSION` (`scripts/tool-pins.sh`, the only copy of
  the pin; ci.yml carries none). The tool is baked into the runner and lane images and never installed over the
  network; a missing or mismatched actionlint fails the gate.
- **`publish` needs `validate`, `gate`, `gate-sweeps`, `gate-tags` and
  `coverage-merge`**, and addresses the forge's
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
| `test/buildgates/release_workflow_test.go` | `release.yml`'s shape, with a mutation for each property: an added trigger, a widened tag pattern, a hand-written tag push, a call to `make release-alpha`, a lockstep audit with no retry or none at all, a skipped VERSION/CHANGELOG check, a gate job (`gate`, `gate-sweeps`, `gate-tags`, the `coverage` legs and `coverage-merge`) missing one of the matching ci.yml commands, `publish` losing its write permission or its dependency on `gate` or `coverage-merge`, a hardcoded `github.com` instead of `github.api_url`, a `RELEASE_BOT` reference, and a step that tolerates failure. It also scans every OTHER workflow file for the same tag-write and `release-alpha` violations |
| `test/buildgates/action_pins_test.go` | every `uses:` is pinned to a 40-hex commit |
| `test/buildgates/ci_tiers_test.go` | ci.yml's tiers (see architecture.md, "CI tiers"): every job is in `ci-gate`'s `needs` and has a `check` line tagged `pr`, `code`, `full` or `nightly`; a `code` job carries exactly `if: needs.changes.outputs.docs_only != 'true'`, needs `changes`, and `ci-gate`'s DOCS_ONLY is `${{ needs.changes.outputs.docs_only }}`; a `full` job carries exactly the full-tier `if:`, ci.yml's `CI_FULL_TIER` is the same expression and `ci-gate`'s FULL_TIER is `${{ env.CI_FULL_TIER }}`; a `nightly` job carries exactly the schedule-or-dispatch `if:` and NIGHTLY is the same expression; a PR-tier job carries no `if:` and needs no code, full-tier or nightly job, and a code job needs no full-tier or nightly job; every `integration*` job (and a job named `trivy`) is `full`; the pull_request trigger lists `opened`, `synchronize` and `reopened` and not `labeled` (a label change starts no run). Every tier's jobs are pinned by name (`prTierJobs`: `changes`, `lint`, `lint-gates`, `arch-gates`; `codeTierJobs`: `test-typescript`, `image`, `coverage`, `coverage-merge`, `test-clusterproof`, `coverage-fts`, `test-tags`, `test-sdk`; `fullTierJobs`: `integration-standalone`, `integration-features`, `fips`; `nightlyTierJobs`: `soak`), each must be in ci.yml with its tag, and the tag counts carry discovery floors, so an empty parse cannot pass. It runs `ci-gate`'s real script under bash: a skip passes only for a `full` job outside a full-tier run, a `nightly` job outside a schedule or dispatch run or a `code` job when DOCS_ONLY is `true`, and `cancelled`, `failure`, an unreadable result, an unreadable FULL_TIER, NIGHTLY or DOCS_ONLY fail, including the `integration-features` matrix's aggregate result when one leg fails or is cancelled. It also scans every workflow: `ci.yml`'s group on `main` carries the event and the commit, a scheduled run's group is `schedule-<repository>` (ci-template v1.29.3), with a literal `cancel-in-progress: true` (no `main` run cancels another; an expression, a `main` group that carries the event only, or a ref-keyed group reddens it); every other workflow on pull_request has the ref-keyed group with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}` (a push to `main` never cancels a `main` run; a literal `true` reddens it), and none but `ci.yml` (its in-template nightly cron, the one scheduler under rulings 135 and 216(1)) has a `schedule:`. Each rule has a fixture mutation that must redden it |
| `test/buildgates/no_deprecated_test.go` (`TestNoLegacyOrDeprecatedAliasLanguage` alone is `make check-no-alias-language`, in `make lint-gates`) | no Go source, proto, CLI command or `docs/*.md` file carries a deprecated declaration, a redirect stub or legacy-alias language. The `docs/` scan is in `make lint-gates` as well as `make test` because a docs-only pull request runs lint and skips the unit suite |
| `test/buildgates/ci_logs_test.go` | no CI log carries a forge credential: every ci.yml job is in `credentialJobs` or in `unmaskedJobs` (`changes`, which prints only file names, and `ci-gate`, which prints only job results); in every job of `credentialJobs` (`docker-build`, `lint`, `lint-gates`, `image`, `coverage`, `coverage-merge`, `test-clusterproof`, `coverage-fts`, `test-tags`, `test-sdk`, `test-typescript`, `arch-gates`, `integration-standalone`, `integration-features`, `fips`, `soak`) the first step after the checkout is "Mask the forge credentials", and it masks `GIT_CONFIG_KEY_0`, `GIT_CONFIG_VALUE_0` and the token inside a `*://*:*@*` URL with `::add-mask::`; no step in ci.yml runs `env`, `printenv`, `set -x` or `set -o xtrace`, or prints a `.env`, a `gomodauth` file or `MODULE_ENV_FILE`; ci.yml's `GO_BUILD_TAGS` is exactly, in order, the set `make -s print-build-tags` composes. Each rule has a fixture mutation that must redden it |
| `test/buildgates/fips_legs_test.go` | the `fips` job's parallel legs are exactly `make test` and `make test-sdk` (#1230): every package `go list` finds under `make -s print-test-pkgs` and in `sdk/go` is printed by `make -s print-unit-leg-packages` for exactly one leg of `make -s print-fips-legs` (`UNIT_LEGS`), no leg is empty or runs a package outside both suites, and ci.yml's `fips` matrix is one literal `include:` list naming every leg once, each with its own `minutes` limit over 5 and at most 350 (a hosted job's ceiling, DS-7377), with `timeout-minutes: ${{ matrix.minutes }}`, `fail-fast: false` and one step running `make test-fips-leg FIPS_LEG='$FIPS_LEG'` from `${{ matrix.leg }}` with `GOTEST_TIMEOUT=$((FIPS_LEG_MINUTES - 5))m` from `${{ matrix.minutes }}`; a package may be in two legs only when both select tests by name; discovery floors on the leg and package counts, and a fixture mutation for each rule |
| `test/buildgates/fips_split_test.go` | a package the `fips` job splits by test name (#1234) runs each of its top-level tests in exactly one of its legs: every `Test`, `Fuzz` and `Example` function `go list` and `go/parser` find in the package's test files under `make -s print-build-tags` is selected by the `-run` and `-skip` patterns `make -s print-fips-leg-filter` prints for exactly one leg, each split leg names one package, and every pattern compiles and holds no `/`, quote, comma or space; discovery floors on the split-package and test counts, and a case for each rule |
| `test/buildgates/coverage_legs_test.go` | the sharded unit suites (DS-7377): `COVER_LEGS` and `FIPS_LEGS` are `UNIT_LEGS` and `TEST_SDK_LEGS` names only unit legs; ci.yml's and release.yml's `coverage` matrices are exactly `make -s print-coverage-legs`, and ci.yml's `test-sdk` matrix exactly `make -s print-test-sdk-legs`, each leg named once, no other axis, `fail-fast: false`, a literal `timeout-minutes` of at most 350 with go test's `GOTEST_TIMEOUT` that limit less 5 minutes, one step running the matrix's leg, and for a coverage leg the profile copied out of the stage (`IN_IMAGE_COPY_OUT`) and uploaded as `coverage-leg-<leg>`; each workflow's `coverage-merge` needs `coverage`, downloads every `coverage-leg-*` profile into `coverage/` and runs `make coverage-merge`. Discovery floors on the leg counts, and a fixture mutation for each rule |
| `test/buildgates/ci_runners_test.go` | where the workflows run (DS-7377): every `runs-on` is the literal `ubuntu-latest` but `code-review.yml`'s; no `if:` splits on `github.server_url` but in `nightly.yml`'s `signal`, which speaks the Gitea API, and in `batch.yml`; no workflow names the removed self-hosted machinery and no removed script is back in `scripts/`; every job has a `timeout-minutes` of at most 350; every `actions/cache` save is `actions/cache/save` under `env.CI_PUBLISH == 'true'`, never the combined action; no workflow writes a `GOMAXPROCS` or `go -p` count. Discovery floors on the workflow and job counts, and a fixture mutation for each rule |

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
| `test/buildgates/feature_jobs_gate_test.go` (also run by `make check-integration-feature-coverage`) | ci.yml's `integration-features` matrix is only `include` entries, each exactly a literal `shard` name (unique) and a non-empty literal `features` list, and the lists together name every feature of `make -s print-integration-features` exactly once and nothing else (a missing, duplicated or unknown feature fails with the feature and its fix), and that list is exactly the `features.mk` entries that map a file; each shard needs `docker-build` and runs, after the checkout, the "Mask the forge credentials" step first and "Free the runner's disk" (neither runs `make`), the download of the `it-image` artifact to `$RUNNER_TEMP/it-image`, `scripts/ci-image.sh load` with `IMAGE` and `ID` bound to `docker-build`'s `it_image` and `it_id` handing the image on as `IT_IMAGE`, `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT" IT_PREBUILT=1` with `IT_FEATURES: ${{ matrix.features }}` in the job env, and `make docker-down-it` under `always()`, with an `IT_PROJECT` that starts `qrdb-ci-` and carries the run, the attempt and the shard, and no job-level `IT_IMAGE`; `make -s it-image` prints a stable `qrdb-ci-it:<16-hex hash>`, `make docker-down-it` removes no image, and a concurrent load of the image does not make `make docker-build-it` fail; `timeout-minutes` is 120 (DS-7377); `fail-fast` is `false`; the job carries the full-tier `if:`; ci.yml runs on push and pull_request. Each rule has a fixture mutation that must redden it |
| `test/buildgates/apk_index_sum_test.go` | `make -s apk-index-sum`, against a stub `docker`, resolves the repository's Dockerfile to the `GO_IMAGE` that `scripts/tool-pins.sh` pins and prints only a sha256; the bare `scripts/apk-index-sum.sh` refuses that Dockerfile (its `FROM ${GO_IMAGE}` has no ARG default, so a default there would be a second copy of the pin); and ci.yml reads the sum exactly once, in `docker-build`, through `make -s apk-index-sum`, never the bare script: every other job loads that build |
| `test/buildgates/ci_image_digest_test.go` (run by `make test` and `make check-action-pins` with the rest of `test/buildgates`) | the workflow env `CI_PUBLISH` is exactly a push to `main`; `docker-build` sets `packages: write`, saves each image with `scripts/ci-image.sh save`, uploads it as `toolchain-image` or `it-image` from `<dir>/image.tar` with `if-no-files-found: error`, outputs each family's image, ID and digest reference from a step, and pushes only with `scripts/ci-image.sh push` under `env.CI_PUBLISH == 'true'`, with nothing ORed or negated around it, and `secrets.GITHUB_TOKEN`; no other job logs in, pushes, pulls or holds the token; every consumer needs `docker-build`, reads no `*_ref` and no unknown output, downloads each image artifact to `$RUNNER_TEMP/<artifact>`, loads it with its family's `*_image` and `*_id` outputs before its first stage of that image (`scripts/in-toolchain-image.sh`, `make integration-test`, which also needs `IT_PREBUILT=1`), and never builds or probes for an image (`docker image inspect`, `docker build`, `make docker-build-*`); the consumers are exactly `digestConsumersWant`, at least six. A copy of ci.yml with `lint` reverted to `docker image inspect \|\| make docker-build-toolchain` must fail it, and each rule has a fixture mutation that must redden it |
| `scripts/ci-image_test.sh` (`make test-ci-image`) | `scripts/ci-image.sh`, against a stub `docker`: `save` writes the tar and the ID and refuses a tag that is not a CI family's content hash; `load` accepts the image `docker-build` built, removes the tar, and refuses a substituted ID, a missing artifact or a malformed ID; `push` logs in on stdin into a private `DOCKER_CONFIG` it deletes, pushes `ghcr.io/<owner>/<name>:<hash>-main`, prints the digest, refuses an empty token and fails on a push with no digest, the token on no command line; `check-ref` accepts only a digest reference under the owner in GHCR |
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
`make docker-build-toolchain` builds the toolchain image alone, under its
content-hash tag, and skips the build when that tag is already on the daemon.
`make gosec` is the gosec scan
alone, with no network, which the PR tier's `lint` job runs and
`make security` calls.

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
`gomodauth` BuildKit secret (`$(stage-gomodauth)`). In CI the `docker-build`
job, the one job that builds an image, writes `GOPRIVATE` and the
`GIT_CONFIG_*` variables to a file under `$RUNNER_TEMP`, passes
it as `MODULE_ENV_FILE` (handbook `devcontainer.md` §7) and removes it after
the build; outside CI the recipe reads them from the environment. No layer, no
command line and no log line holds them: the recipe is silent, printing only
which source it used.

**Every job that can print a forge credential masks it first.** The runner
injects the credentials into every job, and the `docker-build`, `lint`,
`lint-gates`, `image`, `coverage`, `coverage-merge`, `test-clusterproof`, `coverage-fts`,
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

**Every job runs on a GitHub-hosted `ubuntu-latest` runner** (DS-7377): an
ephemeral VM with 4 vCPUs, 16 GB of memory, about 14 GB of free disk and a
6-hour job limit, exclusive to one job. Every `runs-on` is that literal label;
the review jobs in `code-review.yml` alone keep their self-hosted hosts. No
`if:` splits on `github.server_url` but in the one job that speaks the Gitea
API, `nightly.yml`'s `signal`, which keeps its guard so that it never runs on
github.com. Every job has a
`timeout-minutes` of at most 350, under the runner's 360, so a runaway job is
named rather than cancelled unnamed. `test/buildgates/ci_runners_test.go`
fails on any other `runs-on`, a `server_url` split outside those jobs, a job
with no limit or one over 350, a name of the removed self-hosted machinery
(the `bigdisk`, `batch` and `ci-gate` labels, the forge registry and its
`RELEASE_REGISTRY` credential, the named cache volumes and their trim, the
stale-stack reaper, the schedule guard), a removed script back in `scripts/`,
an `actions/cache` save on any event but a push to `main`, and a written
`GOMAXPROCS` or `go -p` count; each rule has a fixture mutation that must
redden it. `batch.yml`'s step-level `server_url` splits are its owner's and
exempt.

**The images are built once per run and handed on as artifacts.** Nothing a
job builds survives into another job's VM, so `docker-build` builds both
stages (`make docker-build`; `make -s toolchain-image` and `make -s it-image`
print the tags), then `scripts/ci-image.sh save` writes each image's
`docker save` tar and its image ID, and the job uploads the tars as the
artifacts `toolchain-image` and `it-image` (retention one day, compression
level 1, a missing file an error) and outputs `toolchain_image`,
`toolchain_id`, `it_image` and `it_id`. Every consumer downloads its artifact
to `$RUNNER_TEMP/<artifact>` and runs
`scripts/ci-image.sh load "$RUNNER_TEMP/<artifact>" "$IMAGE" "$ID"` before its
first stage of that image: the load refuses an image whose ID is not the one
`docker-build` recorded, and removes the tar to give its disk back. The ID
stands in for a digest: a tag can move, an ID cannot. A fork's pull request
takes the same path, because it needs no registry and no write token. No
consumer builds, pulls or probes for an image.

**Only a push to `main` publishes.** The workflow env `CI_PUBLISH` is true on
a push to `main` alone. Under it, and under it only, `docker-build`'s
`scripts/ci-image.sh push` tags each image
`ghcr.io/<owner>/go-qrdb-toolchain` or `go-qrdb-ci-it`, at
`<content-hash>-main`, logs in with the run's own `secrets.GITHUB_TOKEN` fed
to `docker login --password-stdin` in a private `DOCKER_CONFIG` it deletes,
pushes, and prints the digest reference GHCR reported, which `check-ref`
holds to `ghcr.io/<owner>/...@sha256:<64 hex>`; the job outputs them as
`toolchain_ref` and `it_ref` (the main build's images, by digest, for a
developer or a release to pull). The owner comes from the runner's
environment, lowercased, and is named in no tracked file. `docker-build`
alone has `packages: write`; no consumer reads a `*_ref` output or touches a
registry. A refused login or push fails the job with a typed `::error::` line
naming the variable, never its value.

`test/buildgates/ci_image_digest_test.go` holds both rules: `docker-build`
saves and uploads each family's artifact and pushes only under `CI_PUBLISH`
with `secrets.GITHUB_TOKEN`; no other job logs in, pushes, pulls or carries
the token; every consumer (`digestConsumersWant`: `coverage`,
`coverage-merge`, `coverage-fts`, `fips`, `image`, `integration-features`,
`lint`, `lint-gates`, `soak`, `test-clusterproof`, `test-sdk`, `test-tags`)
needs `docker-build`, downloads each image artifact it uses to
`$RUNNER_TEMP/<artifact>`, loads it with the matching `*_image` and `*_id`
outputs before its first stage of that image, and never builds one; an
integration stage runs with `IT_PREBUILT=1`. A copy of ci.yml with `lint`
reverted to a local build must fail it, and each rule has a fixture mutation
that must redden it. `scripts/ci-image_test.sh` (`make test-ci-image`) runs
the script's save, load, push and `check-ref` paths against a stub `docker`,
the token reaching no command line.

The consumers, each before its first stage: `lint`, in the PR tier
(`make lint`, `make deps-check`, a lint-class gate on the PR tier from
ci-template v1.28.0, and, on the PR tier, `make gosec`; the sweeps,
`make lint-sweeps`, run in it on the nightly `schedule:` event alone from
ci-template v1.30), `lint-gates`, in the PR tier (`make lint-gates`, its own
job from ci-template v1.27.0), the unit-suite jobs, in the code tier, one
suite each (ruling 211): the `coverage` legs and their `coverage-merge`,
`test-clusterproof`, `coverage-fts`, `test-tags` and the `test-sdk` legs, and
the `fips` legs, which take the toolchain image; `image`, in the code tier
(the build-tag assertion, the builds and, in the full tier, the linux/arm64
release cross-build, security and the trivy image scan), which takes the
toolchain image and, in the full tier, the integration image; the
`integration-features` shards, which take the integration image; and the
soak. The ci-template runs the stages but lint-gates in one image job; this
repository splits it because together the stages outlast one job's limit,
and each job has a limit sized to its own stages (architecture.md, "CI
tiers"). Every job that loads an image first frees the runner's disk ("Free
the runner's disk": the preinstalled .NET, Android, GHC and CodeQL trees it
never uses, and any preloaded image), which leaves room for the image tars
and the stage's build.
Each job then runs each stage in a toolchain container through
`scripts/in-toolchain-image.sh`. That container
holds the job's CPU bound: the job container's cgroup
`cpu.max`, rounded up (`nproc` with no quota; `IN_IMAGE_CPUS` overrides),
becomes `--cpus N`, `GOMAXPROCS=N` and `GOFLAGS=-p=N`, never a hardcoded
number (4 on a hosted runner; `test/buildgates/ci_runners_test.go` fails on a
count written into a workflow), and its memory bound: the job container's
cgroup `memory.max`, or with no limit, as on a hosted runner VM, the host's
memory less 2 GiB for the runner and the daemon (about 13g; 8g when
`/proc/meminfo` cannot say; `IN_IMAGE_MEMORY` overrides), becomes `--memory`
and `--memory-swap`, so the stage cannot swap the runner. `CI_CGROUP_PARENT`
(default empty) places it under a shared host's slice; a hosted runner sets
none. `IN_IMAGE_COPY_OUT`, a path inside the checkout, is copied out of the
stage's container once it exits, which is how a coverage leg hands its
profile to the upload. The jobs also need the `changes` job: on a docs-only pull
request, one whose every changed file matches `DOCS_ONLY_PATTERN` (markdown,
or under `docs/`), `lint` runs lint and deps-check and `lint-gates` the lint gates, both succeed, and
`image` is skipped with the unit-suite jobs and `test-typescript`, the code
tier. On such a run, a
pull request, `docker-build` builds and uploads the toolchain image alone
and pushes nothing (architecture.md, "CI tiers").
`changes` is never docs-only off a pull request or under
`ci:full`. The script copies the checkout into the container rather than
bind-mounting it, so the stage's writes stay off the runner's tree. Its files sit where a host's do:
`/tmp`, which holds `HOME`, go's `$WORK` and every `t.TempDir`, is on the
container's own layer, not a size-capped tmpfs that a full `make test` fills,
and `/dev/shm` is 2 GiB, not Docker's 64 MiB. Build and test run with no
network: the container's own namespace has only loopback, which a one-shot
`--rm` container joining it once the stage has started (granted `NET_ADMIN`
and nothing else) marks multicast-capable and routes `224.0.0.0/4` over before
it exits. The configurator signals its own progress on a marker volume the
stage mounts read-only: it writes `started` when it runs and `ready` once the
route is in place, and the stage's command waits for `ready` and the route,
`MULTICAST_WAIT` seconds at most (default 60) counted from `started`, not from
the stage's own start. A loaded daemon can take minutes to start the
configurator, and a clock counting that wait ran out before the route existed
(#1275). Until `started` the stage waits on the script, which retries a failed
configurator once (`ip route replace` makes the retry safe) and on a second
failure exits, removing the stage and the marker volume, and on a clock of its
own, `MULTICAST_START_WAIT` seconds (default 240: two configurator runs, each
allowed 120 s to start on a loaded daemon) from the stage's start, past which it exits 1, so
a stage whose script died before the configurator ran does not wait forever. So the multicast discovery tests join their group as
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

**The stage caches are `actions/cache` entries,** never a stage
container's layer or its `/tmp` (handbook `ci-standard.md` §6). A job that
compiles restores `$RUNNER_TEMP/ci-cache` with `actions/cache/restore`, keyed
`go-<job>[-<leg>]-<go.sum hash>-<commit>` and falling back to the newest
entry for the job and `go.sum`, then to the newest for the job, and sets
`CI_CACHE_DIR` to it; `scripts/in-toolchain-image.sh` bind-mounts its
`gobuild` and `golangci` directories as `GOCACHE` and
`GOLANGCI_LINT_CACHE`. A stage with no network keeps the image's own
read-only module cache, so it still proves the image builds offline; a bridge
stage takes a writable copy of it. Only a push to `main` saves an entry
(`actions/cache/save` under `CI_PUBLISH`; the combined `actions/cache`
action, which saves on every event, is refused), so a pull request reads
`main`'s cache and never writes one; GitHub evicts the least recently used
entries past the repository's 10 GB. Each leg has its own key, so legs never
race to save one entry. Off CI, with `CI_CACHE_DIR` unset, the caches are
named volumes on the developer's daemon, `<prefix>cache-{gobuild,golangci,gomod}`;
`CI_CACHE=off` mounts none. The build cache also holds `go test`'s result
cache: `make coverage-leg` leaves out `make test`'s `-count=1`, so a package
unchanged since a passing `main` run replays that result and its coverage
profile, and the packages `scripts/coverage-merge-subprocess.sh` covers run
beside the bulk run rather than after it.

Last, in the full tier only,
`make trivy-image TRIVY_IMAGE_INPUT=it-image.tar` scans the
integration image the job loaded, saved to `it-image.tar` by the
content-hash tag whose ID `scripts/ci-image.sh load` checked, so the scan
reads the bytes the shards run, and fails on a fixable HIGH or CRITICAL
finding; the job removes the tarball under `always()`.
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
`make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT" IT_PREBUILT=1`.
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
source) that `docker-build` built and uploaded and the shard loaded from the
`it-image` artifact, its ID checked ("The CI image", above). That target
runs the feature's selection, the
same one `make integration-test-<feature>` runs on a host, inside the hardened
`integration` service of `build/docker/devcontainer/compose.yaml`: read-only
root filesystem, every capability dropped, `no-new-privileges`, `/tmp` (also
`HOME`) an anonymous volume on the daemon's disk, removed with the container,
no network, and `GOMAXPROCS` Go's default, the container's CPU limit (the
compose service takes `IT_GOMAXPROCS`, empty unless a caller pins one). The suites run without `-race` (the
Makefile's `INT_GOTEST`): race detection is the unit suites' job, which keep
it, and under `-race` the multi-node clusters cost several times the CPU and
wall time. Within a leg, `-parallel` is the Makefile's `INT_PARALLEL`, 5,
one per transport: a single-node test whose daemon shares no port or path
with another calls `t.Parallel()`, and a per-protocol test whose subtests
each need a fresh daemon (an init, seal or drain is one-shot) runs those
subtests in parallel, while a per-protocol test whose subtests are
independent shares one daemon across every transport. The shards run in
parallel, each under its own `timeout-minutes: 120`, and
`feature_jobs_gate_test.go` refuses any other limit (DS-7377). The longest
shard measured 21.2 minutes (run 76127) on the self-hosted runners; planned
at a 4x slowdown on a 4-vCPU hosted runner that is about 85 minutes, inside
the limit. `GOMAXPROCS` reaches `go test`, the harness's `go build` and every
spawned qrdbd through the compose `integration` service, and the per-feature
`go test` takes `-p 4` (the Makefile's `INT_PROCS`, #1286); a shard runs one
package, so `-p` bounds only its build. The selection's per-package budget is
`GOTEST_TIMEOUT`, 30 minutes. A
feature whose `features.mk` entry is empty is in no shard: it would run zero
tests and pass. `fail-fast` is `false`, so one red shard does not cancel the
others.

Sharding exists because each leg pays the checkout, the credential mask, the
disk cleanup, the image download and load, and the teardown before and after
its tests: with one leg per feature, a feature whose tests take a quarter of a
minute held a runner for well over a minute. The lists are
balanced longest-first by the per-feature job times of main run 65165, with
setup assumed to be about 60 s of each leg (counted once per shard) and
`barrier-seal` taken at its first-attempt 300 s rather than the 585 s its
rerun recorded. `rate-limit`, added after that run, is counted at the 24 s its
test took in a lane, in shard 5, then the shard with the least expected time:

| Shard | Features | Expected (s) | Limit |
|---|---|---|---|
| 1 | audit databases distributed-locks multi-tenancy time-series | 552 | 120 min |
| 2 | backup-restore capabilities ceremony clustering firstboot tenant-dek | 551 | 120 min |
| 3 | barrier-seal certificate-authority guarded-swap key-wrapping rag trust-anchors watch | 563 | 120 min |
| 4 | api-server auth-factors replica-forwarding run-modes secondary-indexes storage-engines volume-pools | 551 | 120 min |
| 5 | cluster-verification collections embeddings identity-rbac key-value rate-limit shamir-custody vectors | 574 | 120 min |
| 6 | coordination credentials etcd-compatibility leases row-locking sql transactions trust-bootstrap | 562 | 120 min |

That run's 40 feature legs held 5654 slot-seconds (1.57 slot-hours; 5369, or
1.49, with `barrier-seal` at 300 s); the six shards are expected to hold 3353
(0.93 slot-hours), with the wall time of the job going from its slowest leg
to its slowest shard, about nine and a half minutes. A new feature goes in the
`features:` of the shard with the least expected time; rebalance when a
shard's measured time drifts well past the others.

Each shard names its own compose project, `IT_PROJECT`, from the run, the
attempt and the shard, under the `qrdb-ci-` prefix. The image is the
opposite: every shard of a run runs the one image `docker-build` built,
`qrdb-ci-it:<hash>` (`make -s it-image`), whose hash covers the build
arguments that change the image and the build context (the commit's tree,
plus, on a dirty checkout, the diff and the untracked files `.gitignore` does
not exclude). With `IT_PREBUILT=1`, `make integration-test` builds nothing: it
requires that tag on the daemon and fails, naming `scripts/ci-image.sh load`,
when it is not there. So a shard never runs a tree it built itself.

Each shard runs, after the checkout, "Mask the forge credentials", which
registers every `GIT_CONFIG_*` value, and the token inside any URL among
them, with `::add-mask::` before any step can print one; "Free the runner's
disk"; the download of the `it-image` artifact to `$RUNNER_TEMP/it-image`;
`scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"` with
`IMAGE` and `ID` bound to `docker-build`'s `it_image` and `it_id`, which
hands the image on as `IT_IMAGE`;
`make integration-test-features ... IT_PREBUILT=1`, every feature of the
shard; and, under `if: always()`, `make docker-down-it`, which removes the
shard's project and never the image. No workflow runs `docker compose`
itself (`integration_image_tag_test.go`); `feature_jobs_gate_test.go`
requires those steps in that order, the mask and the disk cleanup running no
`make`, the toolchain artifact or image never standing in for the
integration one, no rebuild in place of the load, the project name, and the
`always()`. The runner VM is the job's alone and ends with it, so nothing
reaps or holds an image: there is no other job on the daemon to remove it.

A feature that selects nothing says so and passes, before any image is built,
as `integration-test-<feature>` does. On a host, `make integration-test-<feature>`
and `make integration-down-<feature>` still run a feature without Docker;
`integration_job_names_test.go` holds those two goals to one set of names.

The union of the shard lists is `make -s print-integration-features`, each
feature in exactly one shard. The handbook's rule is to write matrices
literally (devops.md §1, "Write matrices literally"), so a reviewer reads the
shards in the diff, and ci.yml spells the lists out and `test/buildgates/feature_jobs_gate_test.go` fails on a feature
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

Each shard runs on its own `ubuntu-latest` runner, as the image job and the
standalone and soak jobs do. Load on a runner breaks a 3-node cluster's
elections and a CLI call's 10-second deadline first; a hosted runner VM runs
one job, so the load is the shard's own. The selection runs
`go test -timeout=30m` per package, under the job's limit, so a hung test
still dumps its goroutines.

Raft timing is not part of any budget. qrdbd takes no election, heartbeat or
tick setting from configuration: the values are code defaults
(`pkg/cluster/raft_tuning.go` and go-quicraft's), so CI cannot select a slower
profile.

### FIPS 140-3 legs

The `fips` job's legs (#1230, DS-7377) are `make -s print-fips-legs`, the
Makefile's `FIPS_LEGS`, which is `UNIT_LEGS`, the one partition of the unit
suites the `coverage` legs also run. On the self-hosted runners the legs
measured 7 to 14.3 minutes (run 76127); planned at a 4x slowdown on a 4-vCPU
hosted runner the slowest is about 57 minutes, so every leg's
`timeout-minutes` is its matrix entry's `minutes`, 120, and go test's
per-package budget is that less 5 minutes, so a runaway package is named
before the leg is killed. The table with each leg's packages is
docs/seal/fips140-mode.md, "The legs".
No assignment of whole packages brought `pkg/tenant` and `pkg/seal` under 30
minutes, and `pkg/server` left its leg no headroom, so each is split by test
name over two legs (#1234): `tenant-lifecycle` and `tenant`, `seal-barrier`
and `seal`, `server-raft` and `server`. The first leg of each selects tests
with `go test -run` (the Makefile's `UNIT_RUN_<leg>`), the second skips what
the first selects (`UNIT_SKIP_<leg>`, `-skip`), so a new test runs in the
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
merge: it is `workflow_dispatch` only and, like every other job but
code review's, runs on GitHub-hosted `ubuntu-latest` (DS-7377).
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
