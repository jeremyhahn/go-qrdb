# Release — User guide

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

## One-time setup (repository owner)

**None, beyond ordinary write access.** There is no release bot, no dedicated
signing key and no secret to provision: the project agent cuts a release with
its own existing git identity, the one that already signs every other commit
on `main`. The one thing worth protecting deliberately:

### The public-releasability rules

`make check-public-releasable` keeps the private development fleet's values
out of every tracked file. Those values are the rules themselves, so they are
not in this repository either: the gate reads them at run time from an
untracked pattern file and fails, naming where it looked, when there is none.
It looks in this order:

1. the file named by `PUBLIC_RELEASE_PATTERNS_FILE`;
2. `../handbook/software/conventions/public-release-patterns`, in the handbook
   checkout beside this repository;
3. `$XDG_CONFIG_HOME/go-qrdb/public-release-patterns` (`~/.config/...` when
   `XDG_CONFIG_HOME` is unset).

The file is line-oriented. Blank lines and `#` lines are ignored; every other
line is `<class>[/i] <ERE>`, where the class is `lab-address`, `lab-host` or
`owner-path`, `/i` matches case-insensitively, and the ERE is POSIX extended.
Every class needs at least one rule. For CI, store the file's content as the
Actions secret `PUBLIC_RELEASE_PATTERNS`; the Lint job writes it to a temp file
for the one step that reads it.

| Name | Kind | Value |
|---|---|---|
| `PUBLIC_RELEASE_PATTERNS` | Actions secret | the pattern file's content |

The same gate refuses any tracked file that is a compiled executable, whatever
its content. It recognises them by their leading bytes: ELF, Mach-O (thin or
fat) and PE. A binary built in place, such as `go build` run inside a `cmd/`
directory, carries whatever the build machine wrote into it. Untrack it with
`git rm --cached <path>` and ignore its path. An executable that must be
tracked on purpose is listed by exact path, with the reason, in
`EXECUTABLE_EXEMPT` at the top of `scripts/check-public-releasable.sh`. The
list is empty. Other binaries, such as images and test fixtures, are scanned
like any other file.

## Running CI's stages yourself

CI runs its stages in the image built from `build/docker/devcontainer/`, at
the commit under test. The same targets run it anywhere Docker with BuildKit
is available; nothing is installed on the host.

| Command | Does |
|---|---|
| `make docker-build` | builds the `toolchain` and `integration` stages, with the versions in `scripts/tool-pins.sh` |
| `make -s toolchain-image` | prints the toolchain image's tag |
| `make docker-run CMD='make test'` | runs one command in the toolchain image, this checkout bind-mounted, as your uid, with no network |
| `make integration-test IT_FEATURE=<feature>` | runs one feature's integration suites in the hardened integration container; without `IT_FEATURE`, all of them |
| `make docker-down-it` | removes an integration run's stack and image (`IT_PROJECT`, `IT_IMAGE`) |
| `make deps-check` | fails when a direct requirement is behind its latest release with no reason in its `go.mod` |

To raise a tool, change its line in `scripts/tool-pins.sh` and the matching
line in ci.yml's `env:`; `make check-tool-versions` fails while the two
differ. To hold a module below its latest release, put the reason in a `//`
comment on its `require` line, or on the comment line directly above it.

## Cutting a release

1. **Prepare it as a change.** In one commit, set `VERSION` to the new version
   without the `v` (for example `0.22.0-alpha`), and move the `[Unreleased]`
   notes in `CHANGELOG.md` into a `## [0.22.0-alpha] - YYYY-MM-DD` section.
   Pre-1.0, breaking changes bump MINOR. Land the commit on `main` through
   review; it must be signed.
2. **Once CI is green on that commit, run `make release-alpha
   VERSION=v0.22.0-alpha`** from a clone whose `HEAD` is exactly that commit.
   It creates the three signed tags and pushes them together in one atomic
   push. This is the whole release action — nothing else to dispatch.
3. **Watch the Release workflow.** The tag push just started it. It
   re-validates the tag, VERSION and CHANGELOG, re-runs the build/lint/test
   gate against the tagged tree, builds the release artifacts, and publishes
   the release notes with the artifacts attached. If it
   fails, the tags are already published and immutable: diagnose from its
   logs, repair the cause, and release the next PATCH version. Never move or
   delete a tag.

## When `release-alpha` refuses

Every refusal before the push leaves nothing behind, so fixing the cause and
re-running the command is the whole recovery. The most common refusals:

| Message mentions | Cause |
|---|---|
| `CHANGELOG.md does not carry` | the release section is missing or not the newest one |
| `tag ... already exists` | this version was already released; bump `VERSION` |
| `not in lockstep` | a published tag set is already skewed; repair that first |
| `does not resolve standalone` | a nested module's `go.mod` or `go.sum` does not match its sources; run `GOWORK=off go mod tidy` in it. CI's `make check-mod-tidy` should have caught this first |
| `carries no SSH signature` | the `VERSION` commit on `main` is unsigned |
| `VERSION on HEAD names X, not Y` | the VERSION/CHANGELOG bump is not merged; merge it, then release |
| `is not what ... main holds` | the commit being released is not exactly the remote's `main` |

## When the Release workflow refuses

Every refusal here runs after the tags are already published and immutable —
it cannot roll anything back. Diagnose from the failing job's log and release
the next PATCH version rather than trying to retarget the tag:

| Job | Typical cause |
|---|---|
| `validate` | the tag, VERSION or CHANGELOG drifted from what `release-alpha` checked locally (should not happen if it ran to completion), or a lockstep tag genuinely never arrived after ten retries |
| `gate` | the tagged tree fails a build/lint/test check that passed on `main` moments earlier — investigate the runner and the checkout before assuming the code regressed |
| `publish` | a release artifact failed to build (nothing is published), or the forge's releases API rejected the request (for example, a release for this tag already exists) |

## Release artifacts

Each release carries these files, `<v>` being `VERSION` (for example
`0.49.0-alpha`):

| File | What it is |
|---|---|
| `qrdbd-<v>-linux-amd64` | the server, x86-64 |
| `qrdbd-<v>-linux-amd64-fips` | the server, x86-64, Go FIPS 140-3 module |
| `qrdbd-<v>-linux-arm64` | the server, 64-bit Arm (for example a Raspberry Pi 4) |
| `qrdbd-<v>-linux-arm64-fips` | the server, 64-bit Arm, Go FIPS 140-3 module |
| `qrdb-<v>-linux-amd64`, `-linux-amd64-fips`, `-linux-arm64`, `-linux-arm64-fips` | the headless `qrdb` CLI, the same four ways |
| `SHA256SUMS` | the SHA-256 of every binary above |

Every binary is statically linked (no cgo) and built with the default feature
set (`make -s print-build-tags`) plus `gms_pure_go`. Because of that tag, the
MySQL `REGEXP` family (`REGEXP`, `RLIKE`, `REGEXP_LIKE`, `REGEXP_REPLACE`,
`REGEXP_SUBSTR`, `REGEXP_INSTR`) uses Go's RE2 syntax rather than ICU's:
backreferences and lookaround are refused. A binary built from source with
`make build-server` keeps ICU and needs the ICU shared libraries on the host.
The desktop `qrdb` client is not a release artifact.

To verify a download, put it beside `SHA256SUMS` and run:

```
sha256sum --check --ignore-missing SHA256SUMS
file qrdbd-<v>-linux-arm64          # ELF 64-bit LSB executable, ARM aarch64, statically linked
go version -m qrdbd-<v>-linux-arm64 # build settings: GOARCH=arm64, CGO_ENABLED=0
./qrdbd-<v>-linux-arm64 version     # version and commit match the release tag
```

`go version -m` on a `-fips` binary also lists `GOFIPS140`. A FIPS binary
selects the module at build time; the enforcement level is still the
operator's, `GODEBUG=fips140=only` at run time, as
[seal/fips140-mode.md](../seal/fips140-mode.md) explains.

To build the same files locally, run `make release-artifacts`; to build one
platform, `make release-cross PLATFORM=linux/arm64` ([cli.md](cli.md)).
