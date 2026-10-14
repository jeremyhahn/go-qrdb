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

# Release — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-14

## Where it lives

| File | Holds |
|---|---|
| `Makefile` — `release-alpha` | the release recipe, run by hand: explicit version, lockstep audit, CHANGELOG reconciliation, standalone resolution of each nested module, duplicate-tag check, signed annotated tags, one atomic push, and a probe that asks the remote what it holds when a push reports failure |
| `.github/workflows/release.yml` | the Release workflow: `push: tags: ["v*"]` only, three jobs (`validate`, `gate`, `publish`), no bot secret of any kind |
| `scripts/check-tag-lockstep.sh`, `scripts/lockstep-modules.sh`, `lockstep-baseline` | the lockstep audit and the module-set derivation both the publisher and the auditor read |
| `scripts/check-version-reconciled.sh` | VERSION/CHANGELOG/tag reconciliation, run by `release-alpha`, by the Release Lockstep workflow, and by `validate` above |
| `.github/workflows/release-lockstep.yml` | the defence-in-depth audit on every tag push |
| `Makefile` — `check-mod-tidy` | the Lint step that runs `go mod tidy -diff` in every tracked module, so an untidy `go.mod` or `go.sum` fails the change that makes it rather than the release |

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
- **`gate` runs the same commands as ci.yml's `lint`, `build` and `test`
  jobs** (`make fmt`, `make vet`, `make vet-tags`, `make secure-analyzer`,
  `make lint`, `go build ./...`, `make build-server`, `make coverage`,
  `make test-tags`) against the exact tagged tree, not a re-run of every
  ci.yml step — see architecture.md for why the rest is skipped.
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
| `scripts/check-release-alpha_test.sh` (`make test-release-alpha`) | the recipe against a local bare remote and a throwaway signing key, with the merged bump as the fixture's state: HEAD is tagged, no commit is made, every tag is signed and on HEAD, exactly the two tags are pushed, and three refusals -- a VERSION argument the file does not name, a bump the remote's `main` does not hold, and an unsigned HEAD. The first two fail on a recipe that still wrote VERSION itself |
| `scripts/check-tag-lockstep_test.sh` (`make test-tag-lockstep`) | the audit refuses a skewed set, including one whose deepest module's tag sits on another commit |
| `test/buildgates/release_workflow_test.go` | `release.yml`'s shape, with a mutation for each property: an added trigger, a widened tag pattern, a hand-written tag push, a call to `make release-alpha`, a lockstep audit with no retry or none at all, a skipped VERSION/CHANGELOG check, a `gate` job missing one of the matching ci.yml commands, `publish` losing its write permission or its dependency on `gate`, a hardcoded `github.com` instead of `github.api_url`, a reintroduced `RELEASE_BOT` reference, and a step that tolerates failure. It also scans every OTHER workflow file for the same tag-write and `release-alpha` violations |
| `test/buildgates/action_pins_test.go` | every `uses:` is pinned to a 40-hex commit |
| `test/buildgates/ci_tiers_test.go` | ci.yml's two tiers (see architecture.md, "CI tiers"): every job is in `ci-gate`'s `needs` and has a `check` line tagged `pr` or `full`; a `full` job carries exactly the full-tier `if:` and FULL_TIER is the same expression; a PR-tier job carries no `if:` and needs no full-tier job; the integration jobs and trivy are `full`; the pull_request trigger lists `labeled`. Both tiers' jobs are pinned by name (`prTierJobs`, `fullTierJobs`), each must be in ci.yml with its tag, and the tag counts carry discovery floors, so an empty parse cannot pass. It runs `ci-gate`'s real script under bash: a skip passes only for a `full` job outside a full-tier run, and `cancelled`, `failure`, an unreadable result or an unreadable FULL_TIER fail. It also scans every workflow: one on pull_request has the ref-keyed `cancel-in-progress: true` group, and none but `automerge.yml` has a `schedule:`. Each rule has a fixture mutation that must redden it |

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
| `test/integration/COVERAGE.md` | the per-feature, per-transport (REST/gRPC/QUIC/MCP/Unix/embedded) CLI coverage accounting, and every known gap |
| `Makefile` — `INT_PROJECT`, `INT_IMAGE`, `INT_GPU_IMAGE` | the per-run identity of a Docker integration run (`make integration-test-standalone`, `-cluster`, `-cluster-tenancy`, `-dao`, `-gpu`, `-embedding`, `-rag`): the compose project, and the images the run builds and runs, `go-qrdb-int:<INT_PROJECT>` and `go-qrdb-int-gpu:<INT_PROJECT>`, exported as `QRDB_INT_IMAGE` and `QRDB_INT_GPU_IMAGE` |
| `Makefile` — `integration-down` | the one teardown of a Docker integration run: `docker compose down -v --remove-orphans` for the run's project, then removal of the run's own images. Every stage calls it after its tests, pass or fail; CI's `always()` teardown steps call it with the job's `INT_PROJECT` |
| `test/buildgates/integration_image_tag_test.go` | every service a `test/integration/api` compose file builds names its image through a required interpolation (`${VAR:?...}`, never a literal tag or a `${VAR:-default}`); each such variable is exported from a Makefile variable derived from `INT_PROJECT`; `integration-image` tags its build with it; every stage that brings the stack up passes `-p $(INT_PROJECT)` and ends with `integration-down`, which removes the images; no workflow runs `docker compose` itself, and every job that runs a stage tears down through `make integration-down` under `always()` with the stage's own `INT_PROJECT` |

`AGENTS.md`'s Testing section is where a lane reads "how do I test my feature
in isolation" first; this table is the engineering-level index of where that
tooling lives, kept beside this folder's other test/buildgates entries rather
than duplicated.

### Per-run integration images

A Docker integration run's compose project was already per run, but the image
it built and ran was the fixed `go-qrdb-dev:latest`, which the devcontainer
also builds (from a different Dockerfile stage). Two runs on one host rebuilt
that tag between each other's build and `up`, so a test container's CLI could
drive a server baked from another tree; it surfaced as "unknown method" from a
server that predated the RPC. Each run now builds and runs its own tag, derived
from `INT_PROJECT`, and removes it when the run ends. The compose files require
the tag (`${QRDB_INT_IMAGE:?...}`) and refuse every command without it, `down`
included, so a stack is always driven through `make`; to tear one down by hand,
run `make integration-down INT_PROJECT=<project>`. Two concurrent runs on one
machine need distinct `INT_PROJECT` values, as before. The devcontainer keeps
its own stable `go-qrdb-dev:latest`.

### Cluster integration runners

The `integration-cluster` and `integration-cluster-tenancy` jobs in
`.github/workflows/ci.yml` run on the shared `ubuntu-latest` (bare-metal)
runners, like every other job. Those runners are loaded (load averages of 40
to 70 on 80 threads), and load breaks a 3-node cluster's elections and a CLI
call's 10-second deadline first. So the cluster stage runs in a fixed budget.
The Makefile sets it, so a local `make integration-test-cluster` runs in the
same budget:

| Makefile variable | Default | Applied as |
|---|---|---|
| `CLUSTER_CPUS` | `8` | `cpus:` on the `integration-tests` compose service (docker `--cpus`) |
| `CLUSTER_MEM_RESERVATION` | `8g` | `mem_reservation:` on the same service (docker `--memory-reservation`): a soft floor under host memory pressure, never a limit that kills a test |
| `CLUSTER_PARALLEL` | `1` | `go test -parallel`. The stage already runs `-p 1` over one package, so this bounds the one `t.Parallel` suite, which otherwise boots five 3-node clusters at once |

The budget is on the compose service, not on the job (`container: options:`).
The job starts that service through the host's dockerd as a container of its
own, and that container holds the `go test` process and every qrdbd and qrdb
process the tests spawn. Under the CPU cap each of those Go processes sizes
`GOMAXPROCS` to 8, not to the runner's 80 threads. Eight cluster jobs at once
still leave a runner 16 threads. The other integration stages leave both
variables unset, which docker reads as no limit.

Raft timing is not part of the budget. qrdbd takes no election, heartbeat or
tick setting from configuration: the values are code defaults
(`pkg/cluster/raft_tuning.go` and go-quicraft's), so CI cannot select a slower
profile.

### Cluster suite legs

The full cluster suite is serial (`-p 1`, `-parallel 1`), so its run time is
the sum of its tests: measured on a 4-CPU budget, 36.7 minutes quiet and 43.1
minutes at 2x CPU oversubscription. That outgrew the 45-minute package timeout
it had, and lowering parallelism has nothing left to give. So
`integration-cluster` is a matrix of three legs
(`strategy.matrix.shard: [1, 2, 3]`, `fail-fast: false`), each running
`make integration-test-cluster CLUSTER_SHARD=<leg>/3` in its own compose
project. `ci-gate` reads the job's aggregate result, which fails when any leg
fails. The job is in the full tier (architecture.md, "CI tiers"): all three
legs share its full-tier `if:`, so a plain pull request skips them together and
`ci-gate` excuses that skip; a push to `main`, a dispatch or a `ci:full` pull
request runs all three.

| Makefile variable | Default | Effect |
|---|---|---|
| `CLUSTER_SHARD` | empty | `I/N` runs leg `I` of `N`; empty runs the whole suite. It replaces `INTEGRATION_RUN`, so setting both is refused |
| `CLUSTER_SHARDS` | `3` | the leg count `check-cluster-shards` proves, and the size the workflow's matrix must have |
| `INTEGRATION_GO_TIMEOUT` | `100m` | `go test -timeout` for the package, read by the `integration-tests` compose service; under the job's 120-minute limit, so a hung test still dumps its goroutines |

`scripts/cluster-shard.sh` builds a leg's `-run` expression: it lists the
package's tests with `go test -list` under the run's own tags, sorts them
bytewise and deals them round-robin, so leg `I` takes positions `I`, `I+N`,
`I+2N`... A new test lands in a leg without anyone editing a list. Each
expression is anchored, `^(TestA|TestB)$`, because `go test -run` matches
unanchored and `TestFoo` would otherwise also select `TestFooBar`.

Two Lint steps hold the split honest. `make check-cluster-shards` applies every
leg's expression to the full `go test -list` output and fails when a test is
selected by no leg, or by more than one, or when the list is under a floor of
250 tests (a package that compiled to nothing). `make test-cluster-shards`
runs its rejecting branches on fixture lists.
`test/buildgates/cluster_shard_gate_test.go` fails when the workflow's matrix
is not exactly `1..CLUSTER_SHARDS`, when the step does not pass
`CLUSTER_SHARD=${{ matrix.shard }}/N` or a per-leg `INT_PROJECT`, when
`fail-fast` is not off, when the job's `if:` is not exactly the full-tier
condition, or when `ci-gate` stops requiring the job.

To run one leg by hand: `make integration-test-cluster CLUSTER_SHARD=2/3`, or
print its expression with `./scripts/cluster-shard.sh run 2/3` (set
`CLUSTER_SHARD_TAGS` to the `INT_FEATURE_TAGS` value). The cluster tenancy job
is not split: its selection runs in 5 to 10 minutes.

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
