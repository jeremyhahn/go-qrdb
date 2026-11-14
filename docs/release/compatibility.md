# Release — Compatibility policy

> **Status: proposed, awaiting owner acknowledgement** · Scope: go-qrdb · Last verified: 2026-11-12
> Becomes normative once the owner acknowledges it (a ruling or a line on
> issue #566); until then [architecture.md](architecture.md) and the pre-1.0
> rule in [CHANGELOG.md](../../CHANGELOG.md) govern.

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

What go-qrdb promises its users about change: which surfaces are public, what
a version number says about each, and how a public thing is retired. This is
item 9 of the public-release checklist (issue #566, recorded in
[readiness.md](readiness.md)). The [owner choices](#owner-choices) at the end
must be settled before the policy is acknowledged.

## Before the first release candidate

Until the first release candidate is tagged, nothing below binds. APIs, wire
messages, CLI flags, configuration keys and on-disk formats change freely; a
replaced thing is deleted outright, with no compatibility shim, alias or
migration path kept for it. Versions are `0.MINOR.PATCH-alpha`: MINOR carries
breaking changes, PATCH does not move the API (the rule stated in
[CHANGELOG.md](../../CHANGELOG.md)).

## From the first release candidate on

The version is `MAJOR.MINOR.PATCH` under
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html), shared by
the root module and `sdk/go`, which are tagged in lockstep (see
[architecture.md](architecture.md)). Release candidates of a version
(`X.Y.Z-rc.N`) may still break each other; the promise starts at the release
`X.Y.Z` they lead to, and holds for every later release of that MAJOR.

- **PATCH**: fixes only. No public surface below gains, loses or changes
  anything, except to close a security defect (named in the CHANGELOG).
- **MINOR**: additive. New surface may appear; existing surface may be
  deprecated, never removed or changed in meaning.
- **MAJOR**: may remove deprecated surface and make any other breaking change
  listed under the release's CHANGELOG entry.

### Public surfaces

| Surface | What is public | Breaking (MAJOR only) | Not breaking |
|---|---|---|---|
| Go SDK | Exported identifiers of the `github.com/jeremyhahn/go-qrdb/sdk/go` module and its packages, outside `internal/`. | Removing or renaming an exported identifier; changing a signature, a struct field's type, or documented behaviour; adding a method to an interface users implement. | New packages, functions, types, struct fields, options; new methods on interfaces users only consume. |
| Wire | Every message, field, method, path, status and error code of the REST, gRPC, QUIC and Unix-socket transports, and the protocol versions they negotiate. | Removing or renaming a method, path, field or enum value; changing a field's type or meaning; reusing a protobuf field number or name; making an optional field required; changing an error code for an existing condition. | New methods, paths, optional fields, enum values clients must already tolerate as unknown; new error detail beside an unchanged code. |
| CLI | Command names, flags and their arguments, exit codes, and machine-readable output (JSON and other structured formats). | Removing or renaming a command or flag; changing a default; changing an exit code or a structured-output field. | New commands, flags, output fields. Human-readable text and its layout are not public. |
| On-disk formats | The data directory, Raft log and snapshots, backups and exports. | A release that cannot read data written by an earlier release of the same MAJOR. | A new format the previous release cannot read, if it is enabled only after an explicit finalize step (see [owner choice C](#owner-choices)). |
| Configuration | Configuration file keys, their types and defaults, and the environment variables and flags that set them. | Removing or renaming a key; changing a type, a default, or the meaning of a value. | New keys with a default that keeps earlier behaviour. |

Not public: the root module's Go packages (they back the binaries, not
callers; see [owner choice F](#owner-choices)), log text, metric names marked
experimental, test helpers, and any surface behind a feature gate that is not
yet on by default.

### Deprecation

1. A deprecated thing keeps working, unchanged, for the lifetime of its MAJOR
   (see [owner choice B](#owner-choices) for the minimum).
2. Deprecation is announced in the release that makes it, under the
   CHANGELOG's `### Deprecated` heading, naming the replacement.
3. The surface itself says so: a `// Deprecated:` comment in Go, the
   `deprecated` option in protobuf, a deprecation marker in the REST API description, a warning on
   stderr for a CLI flag or configuration key, each naming the replacement.
4. Removal happens only in a MAJOR, under `### Removed`, and nothing replaced
   is kept beyond it as a shim or alias.

## Owner choices

Each needs an answer before the status above becomes normative. The
recommendation is listed first.

| # | Question | Options | Recommendation |
|---|---|---|---|
| A | What version does the first release candidate carry? | (a) `1.0.0-rc.1`; (b) `0.N.0-rc.1`, keeping the 0.x line through the first public release. | (a): semver gives 0.x no stability promise, so a policy that binds from the first RC needs MAJOR 1. |
| B | How long does a deprecated wire field, method, CLI flag or configuration key live? | (a) until the next MAJOR, and at least two MINOR releases after the one that deprecates it; (b) until the next MAJOR only; (c) a fixed time (for example 12 months) regardless of version. | (a): removal is breaking, so it can only be a MAJOR; the two-MINOR floor stops a deprecation landing just before a MAJOR with no warning period. |
| C | Which on-disk data must a release read, and may it be downgraded? | (a) any data written by any earlier release of the same MAJOR; a format the previous MINOR cannot read is enabled only by an explicit finalize step, so a downgrade by one MINOR works until finalize; (b) only data of the previous MINOR, upgrades step by step; (c) as (a) without the finalize step. | (a): it is what lets a cluster roll back a bad upgrade, and it keeps the promise within a MAJOR. |
| D | Which mixed-version clusters are supported during a rolling upgrade? | (a) adjacent MINOR releases (N and N+1) of one MAJOR, and any PATCH mix; (b) PATCH mixes only, MINOR upgrades take the cluster down; (c) any two releases of one MAJOR. | (a): the industry norm for replicated databases, and testable as a fixed pair in CI. |
| E | Which releases get fixes? | (a) the latest MINOR, plus security fixes for the previous MINOR for 6 months after its successor; (b) the latest MINOR only. | (a) once there are outside users; (b) is enough for the first RC. |
| F | Is any root-module Go package public API? | (a) no: only `sdk/go` is; root packages users must not import move under `internal/` before the first RC; (b) every exported root package is public. | (a): keeps the promise to the surface that is documented and tested as a client, and lets the server change freely. |
| G | Are feature-gated surfaces covered? | (a) no, until the gate is on by default; then from that release; (b) yes, from the release that adds them. | (a): a gate exists so the surface can still change. |

## Gaps

- Before choice F (a) holds, the root module's exported packages that callers
  must not import need moving under `internal/`; no audit of them is recorded.
- No CI job yet checks the promise: an API diff of `sdk/go` against the last
  tag, a protobuf breaking-change check, a read test of data written by the
  previous release, and a mixed-version cluster test (choices C and D).
