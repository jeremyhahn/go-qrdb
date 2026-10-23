# Release — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21

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
- **`gate` runs the same commands as the lint, build and unit-test stages of
  ci.yml's `lint` and `image` jobs** (`make fmt`, `make vet`, `make vet-tags`, `make secure-analyzer`,
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
| `test/buildgates/ci_tiers_test.go` | ci.yml's tiers (see architecture.md, "CI tiers"): every job is in `ci-gate`'s `needs` and has a `check` line tagged `pr`, `code`, `full` or `nightly`; a `code` job carries exactly `if: needs.changes.outputs.docs_only != 'true'`, needs `changes`, and `ci-gate`'s DOCS_ONLY is `${{ needs.changes.outputs.docs_only }}`; a `full` job carries exactly the full-tier `if:`, ci.yml's `CI_FULL_TIER` is the same expression and `ci-gate`'s FULL_TIER is `${{ env.CI_FULL_TIER }}`; a `nightly` job carries exactly the schedule-or-dispatch `if:` and NIGHTLY is the same expression; a PR-tier job carries no `if:` and needs no code, full-tier or nightly job, and a code job needs no full-tier or nightly job; every `integration*` job (and a job named `trivy`) is `full`; the pull_request trigger lists `opened`, `synchronize` and `reopened` and not `labeled` (a label change starts no run). Every tier's jobs are pinned by name (`prTierJobs`: `changes`, `lint`, `arch-gates`; `codeTierJobs`: `test-typescript`, `image`; `fullTierJobs`: `integration-standalone`, `integration-features`; `nightlyTierJobs`: `soak`), each must be in ci.yml with its tag, and the tag counts carry discovery floors, so an empty parse cannot pass. It runs `ci-gate`'s real script under bash: a skip passes only for a `full` job outside a full-tier run, a `nightly` job outside a dispatch or a `code` job when DOCS_ONLY is `true`, and `cancelled`, `failure`, an unreadable result, an unreadable FULL_TIER, NIGHTLY or DOCS_ONLY fail, including the `integration-features` matrix's aggregate result when one leg fails or is cancelled. It also scans every workflow: `ci.yml`'s group on `main` carries the event only, with a literal `cancel-in-progress: true` (a newer push to `main` cancels the `main` run of the same event in flight; an expression, a `main` group that also carries the sha, or a ref-keyed group reddens it); every other workflow on pull_request has the ref-keyed group with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}` (a push to `main` never cancels a `main` run; a literal `true` reddens it), and none but `automerge.yml` has a `schedule:`. Each rule has a fixture mutation that must redden it |
| `test/buildgates/no_deprecated_test.go` (`TestNoLegacyOrDeprecatedAliasLanguage` alone is `make check-no-alias-language`, in `make lint-gates`) | no Go source, proto, CLI command or `docs/*.md` file carries a deprecated declaration, a redirect stub or legacy-alias language. The `docs/` scan is in `make lint-gates` as well as `make test` because a docs-only pull request runs lint and skips the unit suite |
| `test/buildgates/ci_logs_test.go` | no CI log carries a forge credential: every ci.yml job is in `credentialJobs` or in `unmaskedJobs` (`changes`, which prints only file names, and `ci-gate`, which prints only job results); in every job of `credentialJobs` (`docker-build`, `lint`, `image`, `test-typescript`, `arch-gates`, `integration-standalone`, `integration-features`, `fips`, `soak`) the first step after the checkout is "Mask the forge credentials", and it masks `GIT_CONFIG_KEY_0`, `GIT_CONFIG_VALUE_0` and the token inside a `*://*:*@*` URL with `::add-mask::`; no step in ci.yml runs `env`, `printenv`, `set -x` or `set -o xtrace`, or prints a `.env`, a `gomodauth` file or `MODULE_ENV_FILE`; ci.yml's `GO_BUILD_TAGS` is exactly, in order, the set `make -s print-build-tags` composes. Each rule has a fixture mutation that must redden it |

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
| `Makefile` — `INT_FEATURES`, `print-integration-features` | every feature whose `features.mk` entry maps at least one file, sorted: the legs of CI's `integration-features` job. `make -s print-integration-features` prints them |
| `test/buildgates/feature_jobs_gate_test.go` (also run by `make check-integration-feature-coverage`) | ci.yml's `integration-features` matrix lists exactly `make -s print-integration-features`, in order, and that list is exactly the `features.mk` entries that map a file; each leg runs exactly four steps, the "Mask the forge credentials" step first (it runs no `make`), then the age-gated reap of `qrdb-ci-` stacks, `make integration-test IT_FEATURE=${{ matrix.feature }}` and `make docker-down-it` under `always()`, with an `IT_PROJECT` that starts `qrdb-ci-` and carries the run, the attempt and the feature, and no `IT_IMAGE`; `make -s it-image` prints a stable `qrdb-ci-it:<16-hex hash>`, and `make docker-down-it` removes no image; against a stub `docker` that refuses to remove an image a container uses, `make docker-build-it` holds the image (`IT_HOLD`) so the reaper keeps it however old it is, even when the reaper removed it between the check and the hold, removes it once `make docker-release-it` released the hold, and removes a dead job's stale hold and its image in one pass; `timeout-minutes` is set and at most 45; `fail-fast` is `false`; the job carries the full-tier `if:`; ci.yml runs on push and pull_request. Each rule has a fixture mutation that must redden it |
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
`gomodauth` BuildKit secret (`$(stage-gomodauth)`). In CI the `docker-build`,
`lint` and `image` jobs each write the four `GIT_CONFIG_*` variables to a file under `$RUNNER_TEMP`, pass
it as `MODULE_ENV_FILE` (handbook `devcontainer.md` §7) and remove it after
the build; outside CI the recipe reads them from the environment. No layer, no
command line and no log line holds them: the recipe is silent, printing only
which source it used.

**Every job that can print a forge credential masks it first.** The runner
injects the credentials into every job, and the `docker-build`, `lint`,
`image`, `test-typescript`, `arch-gates`, `integration-features`,
`integration-standalone`, `fips` and `soak` jobs, each of which runs a make
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

The `docker-build` job, in the PR tier, builds both stages (`make docker-build`;
`make -s toolchain-image` prints the toolchain tag) and passes both image
names to the jobs after it as job outputs. Two jobs need it and run the
stages side by side in the one toolchain image: `lint`, in the PR tier
(`make lint`, `make lint-gates` and, on a push to `main` and a dispatch,
`make lint-sweeps`), and `image`, in the code tier (the build-tag assertion,
the builds, `make coverage` and the other unit suites and, in the full tier,
security, deps-check and the trivy image scan). The ci-template runs them all
in one image job; this repository splits it because together the stages
outlast one job's 90-minute limit on a loaded runner, and each job has a
limit sized to its own stages (architecture.md, "CI tiers"). Each
first runs the idempotent builds of what it uses: `make
docker-build-toolchain` only when its daemon lacks the content-hashed
toolchain tag, since that target rebuilds whenever it runs, and, in `image`
alone, `make docker-build-it`, which skips a held tag itself; `lint` takes no
hold on the integration image. On the runner `docker-build` used both are
cache hits; on another they build, so a daemon without the images never fails
the job. Each job then runs each stage in a toolchain container through
`scripts/in-toolchain-image.sh`. That container
holds the job's CPU bound: the job container's cgroup
`cpu.max`, rounded up (`nproc` with no quota; `IN_IMAGE_CPUS` overrides),
becomes `--cpus N`, `GOMAXPROCS=N` and `GOFLAGS=-p=N`, never a hardcoded
number. Both jobs also need the `changes` job: on a docs-only pull
request, one whose every changed file matches `DOCS_ONLY_PATTERN` (markdown,
or under `docs/`), `lint` runs lint and the lint gates and succeeds, and
`image` is skipped with `test-typescript`, the code tier. On such a run
`docker-build` builds the toolchain image alone and outputs an empty
integration image, which no job reads (architecture.md, "CI tiers").
`changes` is never docs-only off a pull request or under
`ci:full`. A runner reaches
its Docker daemon through a socket, so the script copies the checkout into the
container rather than bind-mounting it. Its files sit where a host's do:
`/tmp`, which holds `HOME`, go's `$WORK` and every `t.TempDir`, is on the
container's own layer, not a size-capped tmpfs that a full `make test` fills,
and `/dev/shm` is 2 GiB, not Docker's 64 MiB. Build and test run with no
network: the container joins the network namespace of a holder container that
has only loopback, which the holder (granted `NET_ADMIN` and nothing else)
marks multicast-capable and routes `224.0.0.0/4` over, so the multicast
discovery tests join their group as they do on a host and nothing leaves the
namespace. `make security` and `make deps-check`, full tier only, run on the bridge network, because
govulncheck reads the vulnerability database and deps-check asks the module
proxy.

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
never thinned, only removed whole. While removal came first, it emptied the
build cache, and the next `make lint` compiled the tree from nothing: 56
minutes, against seconds on a warm cache. `make test-trim-ci-caches`, in
`make lint-gates`, tests the order on a stub `docker`. The build cache also
holds `go test`'s result cache: `make coverage`'s bulk run leaves out
`make test`'s `-count=1`, so a package unchanged since a passing run replays
that result and its coverage profile, and the packages
`scripts/coverage-merge-subprocess.sh` covers run beside the bulk run rather
than after it.
`scripts/reap-stale-ci-stacks.sh` removes only compose-labelled and anonymous
volumes, never these. `CI_CACHE=off` mounts none; a bridge stage then takes a
writable copy of the image's module cache instead.

Last, in the full tier only (a pull request builds no image for a registry),
`make trivy-image TRIVY_IMAGE_INPUT=it-image.tar` scans the
integration image, saved to `it-image.tar`, and fails on a fixable HIGH or
CRITICAL finding; the job removes the tarball and releases its hold on the
image (`make docker-release-it`) under `always()`, and keeps the image, which
the integration legs on the same daemon reuse (below).
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
`scripts/deps-check_test.sh` (`make test-deps-check`, in CI's `lint` job) proves
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
`.github/workflows/ci.yml`: one matrix leg per feature, each running
`make integration-test IT_FEATURE=<feature>`. That target builds the
integration image (`build/docker/devcontainer/Dockerfile`, stage
`integration`: base plus the source) and runs the feature's selection, the
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
independent shares one daemon across every transport. The legs run in parallel, each under its own
`timeout-minutes: 45`; the 45-minute limit is not raised. A feature whose
`features.mk` entry is empty is not a leg: it would run zero tests and pass.
`fail-fast` is `false`, so one red leg does not cancel the others.

Each leg names its own compose project, `IT_PROJECT`, from the run, the attempt
and the feature, under the `qrdb-ci-` prefix, so legs running at once on one
runner never share a stack. The image is the opposite: every leg uses the
Makefile's content-addressed `IT_IMAGE`, `qrdb-ci-it:<hash>` (`make -s
it-image`), a hash of the build arguments that change the image and of the
build context (the commit's tree, plus, on a dirty checkout, the diff and the
untracked files `.gitignore` does not exclude). Every leg of a run on one
runner names the same image, `make docker-build-it` builds it only when
`docker image inspect` misses it, and the rest reuse it. Two legs that miss together both build, and only the first `--load` can tag the
result: the second fails with `image ... already exists`. That build's leg
then finds the image held, and because the tag is the content it carries on
with it; any other failed build fails the leg with the build's own status
(`feature_jobs_gate_test.go` runs the recipe against a stub `docker` for both
cases). A `flock` would not serialize them, because the job containers
share the runner's Docker daemon, not a filesystem. Each leg runs four
steps. The first after the checkout is "Mask the forge credentials", which
registers every `GIT_CONFIG_*` value, and the token inside any URL among them,
with `::add-mask::` before any step can print one. It then runs
`scripts/reap-stale-ci-stacks.sh qrdb-ci-`, which removes `qrdb-ci-`
projects older than the longest job can live (about three hours), and
`qrdb-ci-it` images no job holds that were created or last tagged more than
two hours ago (`REAP_IMAGE_MIN_AGE`, 7200 s; the later of the two, because a rebuild from a full cache keeps the old `.Created` but
re-tags the image). It ends with a teardown step under
`if: always()`, `make docker-down-it`, which removes that leg's project and
its hold, and never the image, on a pass, a failure or a cancellation.
Removing names nobody created is not an error. No workflow runs `docker
compose` itself (`integration_image_tag_test.go`); `feature_jobs_gate_test.go`
requires the four steps, the mask first, the project name, no `IT_IMAGE`, and
the `always()`. The image job reaps under `qrdb-ci-` too, so a runner that
only runs the PR tier still drops old integration images.

The image's age does not protect a live job's image: a cache hit on an
unchanged tree builds nothing, so the image a job has just resolved keeps its
first build's `.Created` and can be any age, and another job's reaper could
remove it between the image job's build and the trivy step's `docker save`.
What protects it is a hold: every job that uses the
image (the image job, each leg and the soak) names one in its `IT_HOLD` environment,
`qrdb-ci-hold-<run>-<attempt>-<job or feature>`, and `make docker-build-it`
creates a container of that name from the image, never started, before it
returns; `make docker-release-it` (the image job's teardown, and
`make docker-down-it`) removes it. The `docker-build` job takes the image
job's hold under the image job's name and leaves it on success; the image
job's `docker-build-it` finds it already holding this tree's image, and its
`always()` teardown releases it. A `docker-build` that fails or is cancelled
releases the hold itself, and a hold the image job never reaches (the run was
cancelled between the two, or the image job ran on another runner) is a stale
created container the reaper removes. `docker image rm` without force refuses an
image any container uses, so the reaper cannot remove a held image. If the
reaper removes the image between `docker-build-it`'s check and its hold,
`docker create --pull never` fails and the recipe builds and holds it once
more. A job that dies before its teardown leaves a `qrdb-ci-` container in
the created state, which the reaper removes once it is stale, and the image
in the same pass. `feature_jobs_gate_test.go` runs the build recipe and the
reaper against a stub `docker` that refuses to remove an image a container
uses, and fails if the reaper removes a held image.

Sharing one image between runners would take a registry: `--cache-from` and
`--cache-to type=registry` in `DEVCONTAINER_BUILD`, or a push by the image job
and a pull by each leg. ci.yml holds no registry credential, so that needs a
repository secret with push and pull rights on the forge's container registry
for a `qrdb-ci-it` package, which only the owner can add.

A feature that selects nothing says so and passes, before any image is built,
as `integration-test-<feature>` does. On a host, `make integration-test-<feature>`
and `make integration-down-<feature>` still run a feature without Docker;
`integration_job_names_test.go` holds those two goals to one set of names.

The leg list is `make -s print-integration-features`. Gitea cannot expand a
matrix computed at run time (handbook devops.md §1, "Write matrices
literally"), so ci.yml spells the list out and
`test/buildgates/feature_jobs_gate_test.go` fails when the two differ, printing
the line to paste. A new test needs no list edit; a new feature, or an entry
that gains or loses its last file, needs that one line changed.

`make integration-test-<feature>` is the Makefile's `integration-test-%`
pattern rule, and GNU make prefers an explicit rule to a pattern rule. An
explicit target named for a feature therefore hides that feature's selection,
from the leg and from anyone typing the target, which is why the Docker RAG run
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
them together and `ci-gate` excuses that skip; a push to `main`, a dispatch or
a `ci:full` pull request runs all of them from its next push, update-branch or dispatch (a label change alone starts no run).
No leg starts before `docker-build`, `test-typescript` and `arch-gates` have
passed: the job needs those three, as does `integration-standalone`, and not
`lint` or `image`, so the legs start once the images are built and run beside
those jobs' checks; `ci-gate` still fails the run on any red check. The label goes on once, when the pull
request is green on the PR tier and ready to merge (architecture.md, "CI
tiers").

Each leg runs on a shared `ubuntu-latest` (bare-metal) runner, like every other
job. Those runners are loaded (load averages of 40 to 70 on 80 threads), and
load breaks a 3-node cluster's elections and a CLI call's 10-second deadline
first. The `integration` compose service carries the CPU half of the cluster
stage's budget (below) as `GOMAXPROCS=8`: the `go test` process and every
qrdbd and qrdb process the harness spawns inherit it, rather than sizing
themselves to the runner's 80 threads. The selection runs `go test -timeout=30m` per package, under the job's limit, so a hung test
still dumps its goroutines.

Measured on a shared host (load average about 66 on 80 threads, no
`GOMAXPROCS` cap), each well under the limit: `barrier-seal` 6.8 minutes,
`multi-tenancy` 5.1 minutes, `clustering` 3.9 minutes.

Raft timing is not part of any budget. qrdbd takes no election, heartbeat or
tick setting from configuration: the values are code defaults
(`pkg/cluster/raft_tuning.go` and go-quicraft's), so CI cannot select a slower
profile.

### Cluster stage, locally

`make integration-test-cluster` runs the whole `test/integration` package in
one compose container, for a local run; CI does not run it. The suite is serial
(`-p 1`, `-parallel 1`), so its run time is the sum of its tests: measured on a
4-CPU budget, 36.7 minutes quiet and 43.1 minutes at 2x CPU oversubscription.
The Makefile sets its budget:

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
the suite's timings that size `INTEGRATION_GO_TIMEOUT` were measured on a
4-CPU budget. `make test-cluster-cpus` (Lint) runs the script against a stub
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
