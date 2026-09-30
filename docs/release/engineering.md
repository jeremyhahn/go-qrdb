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

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

## Where it lives

| File | Holds |
|---|---|
| `.github/workflows/release.yml` | the Release workflow: `workflow_dispatch` only, guarded off github.com, one release at a time and never cancelled mid-run |
| `Makefile` — `release-alpha` | the release recipe: explicit version, lockstep audit, CHANGELOG reconciliation, standalone resolution of each nested module, duplicate-tag check, signed annotated tags, one atomic push, and a probe that asks the remote what it holds when a push reports failure |
| `scripts/release-bot-git.sh` | the `AGENT_GIT` wrapper the workflow hands to `release-alpha`: the release bot's identity and SSH signing key, from the environment, with no fallback |
| `scripts/check-tag-lockstep.sh`, `scripts/lockstep-modules.sh`, `lockstep-baseline` | the lockstep audit and the module-set derivation both the publisher and the auditor read |
| `.github/workflows/release-lockstep.yml` | the defence-in-depth audit on every tag push |

## Invariants

- **The workflow never names a version.** It reads `VERSION` and passes
  `VERSION="v$(cat VERSION)"` to `release-alpha`. `test/buildgates` fails if a
  literal version, a version input or a trigger other than
  `workflow_dispatch` appears.
- **No step writes a tag by hand.** `release-alpha` is the only tag writer.
  `test/buildgates` rejects any `git tag` or `git push` in a `run:` step of
  this workflow or of any other workflow, and rejects any other workflow that
  runs `make release-alpha`.
- **The VERSION-authoritative path creates no commit.** When `HEAD:VERSION`
  already equals the requested version, the recipe creates the tags at `HEAD`.
  It still requires `HEAD` to carry an SSH signature. When `VERSION` names an
  older release, the recipe keeps its original path: it writes and signs the
  VERSION commit itself, then tags that commit.
- **Every tag is annotated, SSH-signed and on the release commit**, verified
  per tag before anything is pushed, and the whole set is audited for lockstep
  before the push. Any failure before the push removes every tag and commit the
  run created.
- **The push is one `git push --atomic`** of the branch and exactly the release
  tags, never `--tags`.
- **The run ends with a lockstep audit of the remote's tags**, fetched after
  the push, over the same module set `release-alpha` tagged
  (`lockstep-modules.sh`).

## Credentials

| Name | Kind | Used for |
|---|---|---|
| `RELEASE_BOT_TOKEN` | secret | the checkout and the push. Tag protection admits this principal only |
| `RELEASE_BOT_SIGNING_KEY` | secret | the bot's private SSH signing key. It is written to the runner's temporary directory with mode 0600 and removed in an `always()` step |
| `RELEASE_BOT_NAME`, `RELEASE_BOT_EMAIL` | variables | the tagger identity |

The workflow refuses to start, naming what is missing, when any of the four is
unset. The wrapper refuses too, so a local misuse fails the same way.

## Tests

| Test | Proves |
|---|---|
| `scripts/check-release-alpha_test.sh` (`make test-release-alpha`) | the recipe against a local bare remote and a throwaway signing key, including the VERSION-authoritative path: HEAD is tagged, no commit is made, every tag is signed and on HEAD, exactly the branch and the tags are published, and an unsigned HEAD is refused. These cases fail on the recipe before the path existed |
| `scripts/check-tag-lockstep_test.sh` (`make test-tag-lockstep`) | the audit refuses a skewed set, including one whose deepest module's tag sits on another commit |
| `test/buildgates/release_workflow_test.go` | the workflow's shape, with a mutation for each property: an added tag trigger, a cancellable run, running on github.com, the runner's own token, a hand-written tag push, a typed version, a missing post-push audit, or an audit that does not fetch the remote's tags first |
| `test/buildgates/action_pins_test.go` | every `uses:` is pinned to a 40-hex commit |
