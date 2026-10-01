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

# Release — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

**There is no transport API for this feature.** Releasing is repository
tooling, not a capability of a running node, so none of the six transports
(REST, gRPC, Unix, QUIC, MCP, embedded) exposes it.

The only programmatic entry point is the forge's workflow-dispatch API, which
starts the **Release** workflow (`.github/workflows/release.yml`) on `main`.
The workflow takes **no inputs**: the version is whatever `VERSION` on `main`
names ([architecture.md](architecture.md)). A dispatch against any ref other
than `main` fails, because the workflow checks out `main` and `release-alpha`
refuses to run on any other branch.

What a consumer sees is the published result: three Go module versions at one
commit, as described in [architecture.md](architecture.md).
