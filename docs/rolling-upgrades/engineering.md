# Rolling Upgrades — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-23
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
issue #1628). It states the release negotiation as built:
the set of adjacent releases a node accepts (SPEC clause (1)), what each
node-to-node channel announces and where it refuses a gap (clauses (2) and
(3)), the feature gate (clause (4)), the review of the row against the
standards it cites, the review questions and their status, and the test map
(clause (6)). It designs nothing beyond the SPEC row; what is still open is
named under [Delivery](#delivery). The user-facing account is
[clustering/user.md](../clustering/user.md) "Upgrading a cluster".

## Adjacent releases

Every `qrdbd` carries its release: `make build` and the Docker image stamp the
`VERSION` file into `pkg/release.version` with `-X`, `release.Version()` returns
it, and `qrdbd version` prints it. A binary built without the stamp (`go test`,
`go run`) is release `dev` (`release.Dev`).

Two releases are adjacent (`release.Adjacent`, `pkg/release/release.go`), and
may run in one cluster, when they are:

1. the same major.minor, with any patch levels and pre-release identifiers;
2. consecutive minor releases of one major, `M.y` and `M.(y+1)`; before 1.0 this
   is `0.y` and `0.(y+1)`, since Semantic Versioning gives major version zero no
   compatibility promise of its own;
3. the last minor of major M and `(M+1).0`, where the last minor of M is
   declared data (`lastMinor` in `pkg/release`), added by the release that cuts
   M+1. No major boundary has shipped, so `lastMinor` is empty today and every
   major boundary is refused.

Anything else is a gap, and so is a release that does not parse (an empty
announcement included). `release.Check(channel, peer)` admits `dev` on either
side (a test build) and an adjacent peer, and otherwise returns a
`*release.GapError` naming the channel and both releases, for example
`release: enrollment: peer release "0.58.0-alpha" is not adjacent to local
release "0.60.0-alpha"`. The error class table maps it to
`FailedPrecondition` (`pkg/api/errclass`).

## Channels

| Channel | Announced / negotiated | Where the check runs | Refusal | Package |
|---|---|---|---|---|
| Cluster enrolment | `JoinRequest.ReleaseVersion` (the joiner's release); `JoinResponse.ReleaseVersion` carries the member's release on a refusal. The first frame keeps its version ‖ suite byte (`0x01`) | the signing member, before the join token is consumed, so the same token works once the joiner runs an adjacent release; a pending join keeps the checked release | `*release.GapError`, channel `enrollment`; REST 409; the joiner rebuilds the typed gap from the response and exits | `pkg/clusterenroll` (`join_csr.go`, `join_http.go`, `pending_join.go`) |
| Raft membership | `Member.ReleaseVersion` in each node's member record | `featuregate.Gate.AdmitReplica` / `admitRelease` on every add, reading the member stores afresh and rechecking immediately before the proposal; `Gate.Start` refuses a restart onto a release no longer adjacent to the committed members | `*release.GapError`, channel `raft-membership`; a candidate with no announced release is `*featuregate.UnannouncedCandidateError`; gRPC and QUIC `FAILED_PRECONDITION`, REST 409 | `pkg/cluster` (`membership.go`), `pkg/cluster/featuregate` |
| Key delivery | `Envelope.ReleaseVersion` (the sender's release), bound into the HPKE info string (`sealInfo`), so it cannot be altered without failing `Open` | the receiver, before the placement gate and before `Open` | `*release.GapError`, channel `key-delivery`; REST 409 with the receiver's release; the sender returns `ErrDelivery` joined with the typed gap | `pkg/keydelivery` (`hpke.go`, `keydelivery.go`), `pkg/clusterkeys` (`receiver.go`, `sender.go`) |
| Replica forwarding (REST, gRPC, QUIC, MCP) | `CallerAssertion.ReleaseVersion` (JSON `release_version`), the forwarding node's release inside the signed on-behalf assertion | the receiving node, after the assertion's signature verifies | `*release.GapError`, channel `replica-forwarding`; REST and MCP 409 (never the opaque 401, never admitted anonymously), gRPC and QUIC `FAILED_PRECONDITION` | `pkg/auth/forward` (`assertion.go`), `pkg/auth/middleware` (`onbehalf.go`), `pkg/api/mcp` |
| QUIC frame protocol (clients and replica forwarding) | TLS ALPN (RFC 7301) from `wire.Protocols`: the current identifier first and at most one previous; today `qrdb-quic/3` only | the TLS 1.3 handshake | alert 120 `no_application_protocol` (QUIC error `0x178`), never a decode failure; the Go SDK reports it as a typed, non-retryable refusal | `pkg/api/quic/wire`, `sdk/go/transport/quic` |
| Raft transport | unchanged: ALPN `quicraft`, go-quicraft `WireVersion` / `WireWindow`, internal to go-quicraft | go-quicraft | go-quicraft `ErrUnsupportedVersion` | go-quicraft (seam Q3, clause (7), open) |
| Raft command encoding | `[op:1][payload]`, no version; a new command is used only after its feature activates (clause (4)) | the feature gate, before proposing | `UnsupportedFeatureError` / the documented pre-activation fallback | `pkg/protocol`, `pkg/cluster/featuregate` |
| SDK request fields | a request-feature bit per field in the capabilities response; today `pendingUserTTL` (proto `pending_user_ttl`, field 21), absent on v0.58.5-alpha and earlier, so it reads as false | every Go SDK transport (REST, gRPC, QUIC, Unix, MCP), before the TTL-bearing `CreateUser` is sent (`transport.RequirePendingUserTTL`); the embedded transport needs none. `EnforcePendingTTL` stays as the post-create guard for a stale or mixed-cluster answer | `*transport.CapabilityUnsupportedError`; nothing reaches the server | `pkg/capabilities`, `sdk/go/transport` (clause (5), Q5 ruled (b)) |

**Feature gate (clause (4)).** Each node advertises its features.
`Gate.Enabled` is true only once a feature is activated cluster-wide (every
member advertises it), and activation is permanent; per-group early enablement
was removed because log replay to a rolled-back replica diverges.
`Gate.CheckActivated` refuses to start a binary that lacks an activated feature
(`UnsupportedFeatureError`). A node embedded through the Go SDK that the release
check refuses (an activated feature it lacks, `UnsupportedFeatureError`, or a
release gap to the cluster's members, `*release.GapError`) is quarantined: it
withdraws its member record and feature advertisement, closes its Raft host (it
neither votes nor replicates) and refuses every data-plane call with the
refusal, which `Node.Refusal` returns (`sdk/go/node/feature_gate.go`).

**Admission across leaders and restarts.** Release admission is rechecked
immediately before a membership change is proposed. The per-node `proposeMu`
serialises adds through one leader from that recheck to the add's outcome. A
release intent, `{ReleaseIntentPrefix}{candidate, path-escaped}/{release}/{claimant
replica ID}`, is claimed (`PutIfAbsent`) before the proposal and every live
intent and release record is then listed linearizably: of two non-adjacent
adds, through the same or different leaders, at least one sees the other's
intent and is refused with the release gap. `EnforceRelease` records the
restarting member's release before it lists the intents, so a restart and an
add across each other cannot both pass. An intent is retired once its
candidate is a committed member with its own release record, or, while the
candidate is not a member, once no advertisement lists its claimant (the
claimant's fence died with its lease) or the claimant is this node and its
`proposeMu` is free (no add of this node is between claim and outcome), so a
leader that crashed mid-add never blocks later upgrades. go-quicraft #1192
(a configuration check on the Raft worker at the proposal boundary) is no
longer needed for the release rule; it stays a hardening item
([plans/1628.md](../plans/1628.md) "rw1628wa").

Documents that said version equality was required and change with this row:
[clustering/user.md](../clustering/user.md) "Upgrading a cluster",
[release/architecture.md](../release/architecture.md) "Version equality is the
compatibility statement" (module tags; cluster adjacency is this document),
[api-server/api.md](../api-server/api.md) "Version skew", and
[multi-tenancy/architecture.md](../multi-tenancy/architecture.md)
"No backward-compatibility code" (scoped by Q4).

## Standards review

- **Semantic Versioning 2.0.0** ([reference copy](../reference/semver-2.0.0.md)).
  Item 4 says that under major version zero "anything may change at any time".
  SemVer therefore gives no compatibility promise between 0.y releases. The row
  defines "adjacent" itself ([Adjacent releases](#adjacent-releases)); it does
  not rely on SemVer for that.
- **RFC 7301 (ALPN)** ([reference copy](../reference/rfc7301.txt)). A client
  offers its protocol list and the server picks one. If none match, the server
  sends the `no_application_protocol` (120) alert (§3.2). Offering the current
  and the previous identifier, as an ordered list, is how a TLS channel
  negotiates under the standard. The identifiers are opaque byte strings. The
  version is not parsed out of them.
- **RFC 9000 / RFC 8446** (already indexed). QUIC carries ALPN in the TLS 1.3
  handshake, and QUIC connections without an agreed ALPN fail. Replica
  forwarding and the Raft transport inherit this.
- Non-TLS framing (the enrolment request, the key-delivery envelope, the
  on-behalf assertion, the Raft `WireWindow`) has no external standard. It
  follows the same rule: the sender states its release, the receiver checks it
  against its range, and a value outside it is a typed refusal.

## Questions for review

The owner ruled on Q4 (a) and approved the SPEC row text as written, and ruled
Q5 (b). Q1 and Q2 are settled by convention. Q3 stays open.

1. **What "adjacent" means before 1.0.** Adopted; see
   [Adjacent releases](#adjacent-releases).
2. **Rollback.** Adopted: N+1 back to N is supported only while no
   feature that N+1 introduced has been activated. After activation, the N
   binary refuses to start (`UnsupportedFeatureError`). Tested by T3 and T4.
3. **The go-quicraft seam.** Open; go-quicraft decides, with the owner. The
   Raft wire version is pinned at 1 for pre-release. Recommendation: a seam
   issue in go-quicraft lifts the pin and bumps `WireVersion` per release that
   changes the Raft wire. The go-qrdb side consumes `WireWindow` and adds no
   Raft-level negotiation of its own. Until then the Raft transport is
   unchanged (clause (7)), and the Raft leg of T5 waits on it.
4. **SPEC Boundaries ban on compatibility shims** ([SPEC.md](../SPEC.md)
   Boundaries; [multi-tenancy/architecture.md](../multi-tenancy/architecture.md)
   "No backward-compatibility code"). Speaking release N's wire from N+1 is
   compatibility code. Ruled: the ban is
   scoped. Code that speaks exactly one previous release's wire, behind the
   version negotiation and deleted in the release after, is allowed
   (`wire.Protocols` keeps at most one previous identifier). Any other shim
   stays banned.
5. **How a client learns what a server supports** (the #1601 TTL case).
   Ruled (b): a per-feature capability bit. The server
   advertises a request-feature bit in the capabilities response for each
   request field an older server would drop; today `pendingUserTTL`. A bit an
   older server does not send reads as false. Every Go SDK transport reads the
   bit before a TTL-bearing `CreateUser` and refuses with
   `*transport.CapabilityUnsupportedError` without sending it. There is no
   release-version comparison in the SDK and no server-side refusal path.
   The bit is separate from the `pending-user-ttl` feature gate: it says the
   binary honours the field, not that the feature is activated cluster-wide.

## Test plan

All tests are CLI multi-protocol end-to-end tests, with no sleeps (waits are on
observed state). They are in `test/integration`, feature `clustering`
(`test/integration/features.mk`, `make integration-test-clustering`), and run
in the CI shard "Integration". **N** is the first release that carries this
row. Each test states which pair of builds it runs on:

- **Synthetic N+1.** The N+1 build is the same tree built with the
  `qrdb_upgrade_probe` tag (`buildUpgradeBinaries`,
  `test/integration/rolling_upgrade_helpers_test.go`). It registers the
  test-only feature `upgrade-probe` with one gated replicated operation, the
  test-only Raft command `OpUpgradeProbe` (opcode `0xF0`), reachable from the
  CLI as a put under `upgrade-probe/`, so that T4 and T9 run against a feature
  the older side lacks; it also offers an extra ALPN identifier ahead of
  `qrdb-quic/3`. Version tests stamp distinct releases into the same tree
  (`buildReleaseBinary`), so N, N+1 and N+2 announce real, parseable releases.
- **Released pair.** No release differs from the one before it in feature
  support yet: v0.58.5-alpha and earlier lack the pending-user TTL (feature
  `pending-user-ttl`, #1587). Once the first release that carries #1587 and
  this row is tagged, T1-T4, T6 and T7 also run on the released pair, which is
  not run yet. The released pair is the newest release of the newest minor and
  the newest release of the previous minor, so the pair is always one minor
  apart. Two patch releases of one minor are never the pair. T8 keeps
  v0.58.5-alpha as a fixed pre-TTL server, separate from the pair; the T4
  `pending-user-ttl` activation case runs on a fixed historical pair, the first
  release that carries #1587 against v0.58.5-alpha.

| Test | What it proves | Test |
|---|---|---|
| T1 | Roll a three-node cluster N → N+1 one node at a time under a recorded workload. The clusterproof linearizability checker (`test/clusterproof/cmd/historycheck`) passes on the history, and no write is lost. | `TestRollingUpgrade_RollForward_CLI` |
| T2 | Mid-upgrade clusters, 2×N + 1×(N+1) and 1×N + 2×(N+1). Every transport serves through the CLI on every node, and forwarding works in both directions. | `TestRollingUpgrade_MixedCluster_CLI` |
| T3 | Roll back N+1 → N before activation. Data written during the mixed phase reads back. | `TestRollingUpgrade_RollBackBeforeActivation_CLI` |
| T4 | After activation, an N binary refuses to start, with the typed error naming the feature (the test-only feature on the synthetic pair). The `pending-user-ttl` case on the fixed historical pair waits on that release. | `TestRollingUpgrade_OldBinaryRefusedAfterActivation_CLI` |
| T5 | Version-gap refusal (N and N+2): enrolment returns the typed refusal naming both releases and the founders keep exactly their members on every transport; a QUIC client offering no protocol the node speaks ends in alert 120; a candidate with no announced release is refused. The Raft `WireWindow` leg waits on Q3. | `TestRollingUpgrade_VersionGapRefused_CLI`, `TestRollingUpgrade_UnannouncedCandidateRefused_CLI` |
| T6 | Enrolment across versions: an N+1 node joins an N cluster, and an N node joins an N+1 cluster before activation. | `TestRollingUpgrade_EnrolmentAcrossVersions_CLI` |
| T7 | Key delivery across versions, in both directions. | `TestRollingUpgrade_KeyDeliveryAcrossVersions_CLI` |
| T8 | An SDK with the TTL field → a server without the `pendingUserTTL` bit: TTL user create is refused before any request reaches the server, with `*transport.CapabilityUnsupportedError`, on every SDK transport, and a create without a TTL still succeeds; on the current server every protocol's `qrdb admin capabilities` reports the bit. The run against the released v0.58.5-alpha binary joins the released-pair runs. | `TestRESTCreateUser_OlderServerRefusesPendingTTL`, `TestGRPCUser_CreateUser_OlderServerRefusesPendingTTL`, `TestE2E_CreateUser_OlderServerRefusesPendingTTL` (QUIC), `TestUnixRT_CreateUser_OlderServerRefusesPendingTTL`, `TestTransport_CreateUser_OlderServerRefusesPendingTTL` (MCP), `TestUsers_CLI_PendingTTL_PerProtocol` |
| T9 | Pre-activation gating. In both mixed configurations, the test-only gated operation invoked on an N+1 member is refused with the typed not-yet-active error (or takes the N path) on every transport, no member's Raft log carries the new command, and the N members keep applying. After every member runs N+1 and the feature activates, the same operation succeeds and replicates to every member. | `TestT9_PreActivationGating` |

## Delivery

PR #1638 delivers, on the row branch:

1. The release stamp and the adjacency rule (`pkg/release`: `Version`,
   `Adjacent`, `Check`, `GapError`), and `qrdbd version`.
2. Release announcement and gap refusal on enrolment, Raft membership
   admission (including the restart check and the unannounced-candidate
   refusal), key delivery and replica forwarding, with the REST, gRPC, QUIC
   and MCP codes above; ALPN lists on the QUIC channels, typed in the Go SDK;
   release intents that serialise admission across leaders and against
   restarts, retired when their candidate records or their claimant is gone.
3. The cluster-wide feature gate with permanent activation and the Go SDK
   node quarantine.
4. The synthetic N+1 build and tests T1-T7 and T9.
5. Clause (5) under Q5 (b): the `pendingUserTTL` request-feature bit on every
   transport, the Go SDK's pre-send refusal, and T8 per transport.

What remains open:

- **Clause (7), Q3 and the Raft leg of T5**: the Raft transport's version
  window waits on the go-quicraft seam.
- **Released-pair runs**: they start once a release carrying this row is
  tagged; T8 against the released v0.58.5-alpha binary runs with them.
