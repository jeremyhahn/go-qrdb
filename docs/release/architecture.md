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

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

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

## The one path that creates tags

```
 release PR (VERSION + CHANGELOG)  ──review──▶  main
                                                 │
                         owner dispatches the Release workflow
                                                 │
                                                 ▼
 read VERSION ─▶ make release-alpha ─▶ one atomic push of every tag ─▶ lockstep audit
                 (lockstep audit,        (all refs applied,             of the tags the
                  CHANGELOG check,        or none)                       remote now holds
                  standalone-resolution
                  and duplicate-tag
                  checks; signed tags)
```

1. **VERSION is authoritative.** The release is the version the merged
   `VERSION` file names. A release is therefore reviewed like any other change,
   as a commit that bumps `VERSION` and adds the matching `CHANGELOG.md`
   section, before anything is published. The workflow takes no version input.
   A typed version would be a second source of truth.
2. **The workflow wraps `make release-alpha`, and the recipe never writes
   VERSION.** The recipe a person could run by hand is the recipe the workflow
   runs, with the same checks. It tags the merged bump and creates no commit.
   It refuses, before fetching or writing anything, when `VERSION` on `HEAD`
   does not already name the requested release ("merge the VERSION/CHANGELOG
   bump first"), when `HEAD` is unsigned, and when `HEAD` is not exactly the
   commit the remote's `main` holds.
3. **One atomic push of the tags.** The recipe audits the tags it has just
   created for lockstep, then pushes every tag in a single `git push --atomic`.
   The branch is not pushed: the release commit is already on `main`. The server
   applies all of the tags or none of them, so a skewed set cannot be
   half-published.
4. **Tag protection makes it the only path.** The release tag patterns are
   protected so that only the release bot, the identity the workflow
   authenticates as, can create them. A direct tag push from a workstation is
   refused at the server, and is not merely reported afterwards. The bot's token
   is never persisted in the checkout: it reaches only the step that pushes, as
   a per-step header in that step's environment.
5. **The run ends on what the server holds.** After the push the workflow
   fetches the remote's tags and runs the lockstep audit over them, so a
   skewed set on the server fails the run that published it.

The workflow does not build a consumer program against the published tags.
The integration suites already exercise every SDK and API surface, and the
owner ruled a post-release consumer build unnecessary.

## Defence in depth

The **Release Lockstep** workflow still audits every tag push and every push to
`main`. It catches what a creation-time policy cannot see: a tag that is deleted
or moved after the fact.

## Rejected alternatives

- **A server-side pre-receive hook** that rejects a root tag without its
  counterparts. It would be preventive too, but it lives in the forge's host
  configuration, outside this repository, and it cannot answer who may cut a
  release. It remains a possible second layer.
- **A version input on the workflow.** Rejected because `VERSION` would then
  be derived from the input, and a release would bypass the review of the
  VERSION and CHANGELOG commit.
- **A tag-push trigger that cuts the rest of the set.** Rejected because the
  first tag would already be published before the workflow ran, which is
  exactly the skew this design prevents.

## Release kinds

This repository cuts **internal** releases (`vX.Y.Z-alpha`) on its development
forge. The project's agent prepares the release (the VERSION and CHANGELOG
commit) and dispatches the Release workflow. The workflow, running as the
release bot, is what creates the tags. Release candidates and public releases
are cut under the organisation's release policy and are out of scope here; a
public release is published separately and only with the owner's approval.
