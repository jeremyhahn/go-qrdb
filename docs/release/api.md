# Release — API

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

**There is no transport API for this feature.** Releasing is repository
tooling, not a capability of a running node, so none of the six transports
(REST, gRPC, Unix, QUIC, MCP, embedded) exposes it.

**The only entry point is the tag push itself.** `make release-alpha`, run by
hand, creates and pushes the tags (architecture.md); there is no
workflow-dispatch input and nothing to call to start a release. Pushing the
tag is what starts `.github/workflows/release.yml`, and that workflow takes
no inputs of its own — everything it checks comes from the tag
(`${{ github.ref_name }}`) and the repository content at the commit it
points to.

`release.yml` exposes one indirect side effect a caller can observe: once its
`validate` and `gate` jobs pass, `publish` calls the forge's own releases API
(`POST {{ github.api_url }}/repos/{owner}/{repo}/releases`) with the matching
`CHANGELOG.md` section as the release body, then attaches the
`make release-artifacts` binaries and `SHA256SUMS` to that release
(`.../releases/{id}/assets`). Those two calls are the workflow's only writes;
everything before them is read-only re-verification of what `release-alpha`
already published.

What a consumer sees is the published result: three Go module versions at one
commit, plus a release object carrying that version's CHANGELOG section and
its binaries, as described in [architecture.md](architecture.md).
