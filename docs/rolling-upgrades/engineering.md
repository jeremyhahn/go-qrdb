# Rolling Upgrades — Engineering

> **Status: NORMATIVE (draft for review)** · Scope: go-qrdb · Last verified: 2026-11-18
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

This document goes with the SPEC row `rolling-upgrades` ([SPEC.md](../SPEC.md),
issue #1628). It records the current state, reviews the row
against the standards it cites, lists the questions the review must answer, and
states the test plan. It designs nothing beyond the SPEC row. Wire formats,
message layouts and package changes come with the implementation lanes, after
review.

## Current state

| Channel | Version today | Where |
|---|---|---|
| Raft transport (go-quicraft) | ALPN `quicraft`; `WireVersion = 1`, pinned for all pre-release; `PreviousWireVersion`, `WireWindow{Current, Previous}`, `ErrUnsupportedVersion`; `MessageBatch.MaxBinVer`; the leader proposes `UpdateWireVersion` | go-quicraft `pkg/proto/constants.go` |
| Replica forwarding over QUIC | ALPN `qrdb-quic/3` (`wire.ProtocolV3`), the only protocol offered | `pkg/api/quic/wire/wire.go` |
| REST, gRPC and MCP forwarding | no version (MCP carries its own protocol version only) | `pkg/api/*` |
| Forward assertion | JSON with no version field | `pkg/auth/forward/assertion.go` |
| Cluster enrolment | first frame is version ‖ suite, version `0x01`, checked on receipt; `JoinRequest.Features` | `pkg/clusterenroll` |
| Key delivery | no version in the envelope; `qrdb/keydelivery/v1` appears only in the HPKE info string and the URL | `pkg/keydelivery` |
| Raft command encoding | `[op:1][payload]`, no version; an unknown opcode is `InvalidOpcodeError`, and a mixed group diverges | `pkg/protocol` |
| Cluster feature gate | 24 features, each advertised per node; `Gate.Enabled` is true only once a feature is activated cluster-wide (every node advertises it), and activation is permanent. Per-group early enablement was removed because log replay to a rolled-back replica diverges. `CheckActivated` (`UnsupportedFeatureError`) refuses to start a binary that lacks an activated feature; before activation a write keeps the documented fallback | `pkg/cluster/featuregate` |
| Node release version | `VERSION` passed as `-X main.version` (default `dev`), shown only by `qrdbd version`; peers do not exchange it | `cmd/qrdbd` |
| Capabilities RPC | 18 booleans, the run mode and the unencrypted-at-rest list; no server version | `CapabilitiesResponse` |
| SDK user create | sends `pending_ttl_seconds` (#1587, on main after v0.58.5-alpha, in no release yet). An older server drops it; the SDK then sees an account with no expiry and deletes it (`transport.EnforcePendingTTL`). This is an undo after the fact, not a refusal (#1601) | `sdk/go/transport` |
| SDK tenant list | sends the `status` filter (#1651, on main, in no release yet). An older server drops it and answers every tenant; the SDK's network transports and the CLI refuse the answer with `*transport.CapabilityUnsupportedError` and no tenants (`transport.EnforceStatusFilter`). A typed refusal after the fact: the server did the unfiltered read | `sdk/go/transport` |
| Test infrastructure | the clusterproof inventory has one image for the whole cluster; nothing builds an older release | `test/clusterproof` |

Documents that say version equality is required today and change with this row:
[clustering/user.md](../clustering/user.md) "Upgrading a cluster",
[release/architecture.md](../release/architecture.md) "Version equality is the
compatibility statement", [api-server/api.md](../api-server/api.md) "Version
skew", and [multi-tenancy/architecture.md](../multi-tenancy/architecture.md)
"No backward-compatibility code".

## Standards review

- **Semantic Versioning 2.0.0** ([reference copy](../reference/semver-2.0.0.md)).
  Item 4 says that under major version zero "anything may change at any time".
  SemVer therefore gives no compatibility promise between 0.y releases. The row
  must define "adjacent" itself (Q1); it cannot rely on SemVer for that.
- **RFC 7301 (ALPN)** ([reference copy](../reference/rfc7301.txt)). A client
  offers its protocol list and the server picks one. If none match, the server
  sends the `no_application_protocol` (120) alert (§3.2). Offering the current
  and the previous identifier, as an ordered list, is how a TLS channel
  negotiates under the standard. The identifiers are opaque byte strings. The
  version is not parsed out of them.
- **RFC 9000 / RFC 8446** (already indexed). QUIC carries ALPN in the TLS 1.3
  handshake, and QUIC connections without an agreed ALPN fail. Replica
  forwarding and the Raft transport inherit this.
- Non-TLS framing (the enrolment first frame, the Raft `WireWindow`) has no
  external standard. It follows the same rule: the receiver states its range, and
  a value outside it is a typed refusal.

## Questions for review

Each question has a recommendation. The owner ruled on Q4 (decision 231 (a))
and approved the SPEC row text as written. The others are settled
by convention unless the review objects; Q5 stays open. The SPEC row
merges on that recorded approval of its text, as the rules of
[SPEC.md](../SPEC.md) require.

1. **What "adjacent" means before 1.0.** Recommendation: consecutive minor
   releases, 0.y.z and 0.(y+1).z, with any patch levels. From 1.0 on: consecutive
   minor releases within one major, plus the last minor of major M to the first
   of M+1.
2. **Rollback.** Recommendation: N+1 back to N is supported only while no
   feature that N+1 introduced has been activated. After activation, the N
   binary refuses to start (`UnsupportedFeatureError`, which exists today).
3. **The go-quicraft seam.** The Raft wire version is pinned at 1 for
   pre-release. Recommendation: open a seam issue in go-quicraft to lift the pin
   and bump `WireVersion` per release that changes the Raft wire. The go-qrdb
   side consumes `WireWindow` and adds no Raft-level negotiation of its own.
4. **SPEC Boundaries ban on compatibility shims** ([SPEC.md](../SPEC.md)
   Boundaries; [multi-tenancy/architecture.md](../multi-tenancy/architecture.md)
   "No backward-compatibility code"). Speaking release N's wire from N+1 is
   compatibility code. Ruled: the ban is scoped. Code that
   speaks exactly one previous release's wire, behind the version negotiation and
   deleted in the release after, is allowed. Any other shim stays banned.
5. **How a client learns what a server supports** (the #1601 TTL case). Left
   open for review. The options are a server release version in the capabilities
   response, a per-feature capability bit, or a typed refusal from the server.
   The SPEC row requires only that the field is never silently dropped.
   The tenant list `status` filter (#1651) is answered by a client-side check
   of the returned statuses (`transport.EnforceStatusFilter`), so it needs no
   capability bit; that settles this field only, and the question stays open.

## Test plan

All tests are CLI multi-protocol end-to-end tests, with no sleeps (waits are on
observed state), under the cluster suites. **N** is the first release that
carries this row. The clusterproof inventory needs a per-node image to run
them. Each test states which pair of builds it runs on:

- **Released pair.** No release differs from the one before it in feature
  support yet: v0.58.5-alpha and earlier lack the pending-user TTL (feature
  `pending-user-ttl`, #1587), and #1587 is on main but unreleased. Once the
  first release that carries #1587 is tagged, T1-T4, T6 and T7 also run on the
  released pair. The released pair is the newest release of the newest minor
  and the newest release of the previous minor, so the pair is always one minor
  apart, as Q1 defines adjacency. Two patch releases of one minor are never the
  pair; a patch-pair run may be added separately and is not required by the
  row. T8 keeps v0.58.5-alpha as a fixed pre-TTL server, separate from the
  pair, because the TTL case needs a server without the field. In the same way,
  the T4 `pending-user-ttl` activation case runs on a fixed historical pair,
  the first release that carries #1587 against v0.58.5-alpha, separate from the
  advancing pair.
- **Synthetic N+1.** Until that release exists, every test runs on the
  synthetic pair. Until two releases carry the version announcement, T5
  (gap refusal) and the negotiation parts of T2 always do. It builds N+1 from the same tree
  with a higher release and wire version. This build also registers one
  test-only feature and one test-only request field, under a test build tag,
  so that T4 and T8 also run against a feature the older side lacks. The
  test-only feature also gives the N+1 build one gated replicated operation, a
  test-only Raft command reachable from the CLI under the same test build tag,
  so that gating before activation is observable (T9). Without them the two
  sides would support the same features, and those tests could not fail.

| Test | What it proves |
|---|---|
| T1 | Roll a three-node cluster N → N+1 one node at a time under the clusterproof workload. The linearizability check passes, and no write is lost. |
| T2 | Mid-upgrade clusters, 2×N + 1×(N+1) and 1×N + 2×(N+1). Every transport (gRPC, REST, QUIC, MCP, SQL wires) serves through the CLI on every node, and forwarding works in both directions (N → N+1 leader and N+1 → N leader). |
| T3 | Roll back N+1 → N before activation. Data written during the mixed phase reads back. |
| T4 | After activation, an N binary refuses to start, with the typed error naming the feature: on the synthetic pair, the test-only feature. On the advancing released pair, when the older member already supports every feature the newer one activates, T4 uses only the test-only feature of the synthetic build. The `pending-user-ttl` case runs on a fixed historical pair, kept separate from the advancing pair the way T8 keeps v0.58.5-alpha fixed: the first release with #1587 activates `pending-user-ttl`, and v0.58.5-alpha then refuses to start. |
| T5 | Version-gap refusal (N and N+2) on every channel: enrolment returns the typed refusal, ALPN ends in alert 120, the Raft `WireWindow` refuses. The node logs both versions, and the cluster does not diverge. |
| T6 | Enrolment across versions: an N+1 node joins an N cluster, and an N node joins an N+1 cluster before activation. |
| T7 | Key delivery across versions, in both directions. |
| T8 | An SDK with the TTL field → the fixed pre-TTL v0.58.5-alpha server: TTL user create is refused before any user exists, with a typed error. Today it is created and then deleted (#1601), which this test must reject. SDK → server on the same or a newer release works. The synthetic pair repeats this with the test-only field. This is the first case (#1601). |
| T9 | Pre-activation gating. In both mixed configurations (2×N + 1×(N+1) and 1×N + 2×(N+1)) and before the T3 rollback, the test-only gated operation invoked on an N+1 member is refused with the typed not-yet-active error (or takes the N path) on every transport, no member's Raft log carries the new command, and the N members keep applying. After every member runs N+1 and the feature activates, the same operation succeeds and replicates to every member. |

## Delivery

The row PR carries this document and the SPEC row only. Implementation follows
in lanes after review:

1. Release version announcement, gap refusal, and ALPN lists on the QUIC
   channels.
2. The go-quicraft seam (Q3).
3. SDK field negotiation (Q5) with the #1601 TTL create.
4. The matrix harness (per-node images, synthetic N+1) and tests T1–T8.
