# Clustering — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-16

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

Everything except `Health`, `Status` and `Capabilities` is **raft mode only**
and refuses in standalone with `requires mode: raft`.

## Capability matrix — node administration

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Admin()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Health | `GET /health` — **public** | `Health` | `admin.Health` | `qrdb_health` | `Health` | none on REST; `admin:health` on the others |
| Status | `GET /status` | `Status` | `admin.Status` | `qrdb_status` | `Status` | `admin:status` |
| Peers | `GET /peers` | `Peers` | `admin.Peers` | `qrdb_peers` | `Peers` | `admin:peers` |
| Capabilities | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Capabilities` | `admin:capabilities` |
| Drain | `POST /drain` | `Drain` | `admin.Drain` | `qrdb_drain` | `Drain` | `admin:drain` |
| Request snapshot | `POST /snapshot` | `RequestSnapshot` | `admin.RequestSnapshot` | `qrdb_snapshot` | `RequestSnapshot` | `admin:snapshot` |
| Add member | `POST /membership/add` | `AddMember` | `admin.AddMember` | `qrdb_add_member` | `AddMember` | `admin:add_member` |
| Add member as observer | `POST /membership/add-observer` | `AddObserverMember` | `admin.AddObserverMember` | `qrdb_add_observer_member` | `AddObserverMember` | `admin:add_member` |
| Remove member | `POST /membership/remove` | `RemoveMember` | `admin.RemoveMember` | `qrdb_remove_member` | `RemoveMember` | `admin:remove_member` |
| Promote member (observer → voter) | `POST /membership/promote` | `PromoteMember` | `admin.PromoteMember` | `qrdb_promote_member` | `PromoteMember` | `admin:add_member` |
| Transfer leadership | `POST /membership/transfer-leadership` | `TransferLeadership` | `admin.TransferLeadership` | `qrdb_transfer_leadership` | `TransferLeadership` (`transport.LeaderTransferrer`) | `admin:remove_member` |
| List pending join requests | `GET /v1/cluster/join-requests` | `ListPendingJoinRequests` | `admin.ListPendingJoinRequests` | `qrdb_list_pending_join_requests` | `ListPendingJoinRequests` | `cluster:join_token` |
| Approve join request | `POST /v1/cluster/join-requests/approve` | `ApproveJoinRequest` | `admin.ApproveJoinRequest` | `qrdb_approve_join_request` | `ApproveJoinRequest` | `cluster:join_token` |
| Reject join request | `POST /v1/cluster/join-requests/reject` | `RejectJoinRequest` | `admin.RejectJoinRequest` | `qrdb_reject_join_request` | `RejectJoinRequest` | `cluster:join_token` |
| Start shard | `POST /admin/shards` | `StartShard` | `admin.StartShard` | `qrdb_start_shard` | `StartShard` | `admin:start_shard` |
| Stop shard | `DELETE /admin/shards/{id}` | `StopShard` | `admin.StopShard` | `qrdb_stop_shard` | `StopShard` | `admin:stop_shard` |
| Shard state | `GET /admin/shards/{id}/state` | `GetShardState` | `admin.GetShardState` | `qrdb_shard_state` | `GetShardState` | `admin:shard_state` |
| Topology | `GET /admin/topology` | `GetTopology` | `admin.GetTopology` | `qrdb_topology` | `GetTopology` | `admin:topology` |
| Replica state hashes | `GET /admin/state-hashes` | `GetStateHashes` | `admin.GetStateHashes` | `qrdb_state_hashes` | `StateHashes` | `admin:state_hash` |
| Replica states (applied index + state-machine hash) | `GET /admin/replica-states` | `GetReplicaStates` | `admin.GetReplicaStates` | `qrdb_replica_states` | `ReplicaStates` | `admin:replica_state` |
| Node-keyspace compaction policy | `GET /admin/node-compaction` | `GetNodeCompaction` | `admin.GetNodeCompaction` | `qrdb_node_compaction` | `NodeCompaction` | `admin:node_compaction` |
| Set node-keyspace compaction policy | `PUT /admin/node-compaction` | `SetNodeCompaction` | `admin.SetNodeCompaction` | `qrdb_update_node_compaction` | `SetNodeCompaction` | `admin:node_compaction` |

**Promote member is on every transport.** Server-side it is served through
`service.MemberPromoter`, a capability interface separate from
`AdminServicer`, which the five transport bindings and `AdminService` each
type-assert for. Client-side `PromoteMember` is a plain method on
`transport.AdminClient` (`Client.Admin().PromoteMember(...)`), the same as
`AddMember`/`RemoveMember`.

**Add member as observer is `service.ObserverMemberAdder`**, a capability
interface like `MemberPromoter`. On the SDK side a caller type-asserts
`client.Admin().(transport.ObserverMemberAdder)`. It is the add-side
counterpart of promote: a plain `AddMember` always adds a voter, so promote
needs an observer added this way to act on.

**Transfer leadership is `service.LeaderTransferrer`**, a capability
interface for the same reason `MemberPromoter` is one, and it is on every
transport: REST `POST /membership/transfer-leadership` with the body
`{"shard_id": 1, "replica_id": 3}`, gRPC/Unix `TransferLeadership`
(`TransferLeadershipRequest{shard_id, replica_id}`, answering `Empty`), QUIC
`admin.TransferLeadership`, MCP tool `qrdb_transfer_leadership` (arguments
`shard_id`, `replica_id`), the embedded transport, and the CLI
(`qrdb admin member transfer-leadership`, [cli.md](cli.md)). A caller
type-asserts `client.Admin().(transport.LeaderTransferrer)`, or calls
`Client.Cluster().TransferLeadership(ctx, shardID, replicaID)`, which does the
assertion and returns `*transport.NotImplementedError` on a transport without
it.

The call asks Raft to hand the shard's leadership to the named voter and
returns once the shard's leader observes that voter leading, bounded by five
seconds on the server. **Every node serves it**: a follower forwards the call
to the shard's leader (`replica_forwarding`), and relays the leader's answer.
It carries `admin:remove_member` because moving leadership is the step
`RemoveMember` already takes on its own when the replica it removes leads the
shard. A server whose admin service lacks the capability answers `501` /
`UNIMPLEMENTED`, which the SDK reports as `*transport.CapabilityUnsupportedError`.

**`LEADER_TRANSFER_TIMEOUT` is the one stable code for a transfer that did not
finish**, on every wire transport. Its class is Unavailable (`503` /
`UNAVAILABLE`): nothing changed and the call may be retried; leadership may
still move later if the target recovers. The refusal carries the code and four
metadata keys beside the message:

| Key | Value |
|---|---|
| `shard_id` | the shard |
| `from_replica_id` | the replica that led when the call began |
| `to_replica_id` | the target |
| `timeout_ms` | the bound that passed, in milliseconds |

| Transport | Where the code and metadata travel |
|---|---|
| REST | the error body: `{"error": "...", "code": "LEADER_TRANSFER_TIMEOUT", "metadata": {...}}` |
| gRPC / Unix | a `google.rpc.ErrorInfo` status detail, domain `qrdb.v1` |
| QUIC | the error frame's `ErrorInfo`, domain `qrdb.v1` |
| MCP | the tool result's structured error envelope, `reason` and `metadata` |

The SDK rebuilds `*sdk.LeaderTransferTimeoutError` from that metadata, so
`errors.As(err, &timeout)` matches on every transport, embedded included; a
refusal whose metadata does not decode is returned as the
`*transport.RequestError` the server sent. The typed errors, re-exported from
the SDK package:

| Error | Class | Cause |
|---|---|---|
| `sdk.LeaderTransferTimeoutError` | Unavailable (`503`) | the target has not taken over within the bound — a stopped or partitioned target produces this |
| `sdk.UnknownShardError` | NotFound (`404`) | the shard is not loaded on the leader |
| `sdk.UnknownReplicaError` | NotFound (`404`) | the target is replica 0 or one the shard has never had |
| `sdk.LeaderTransferTargetError` | FailedPrecondition (`409`) | the target is an observer, a witness, or was removed |
| `transport.NotLeaderError` | — | embedded only: this node does not lead the shard; `LeaderID` names the one that does |

Over a wire transport only the timeout is rebuilt as its typed error; the
other three arrive as the transport's error for their class. Naming the
current leader succeeds at once.

**List/approve/reject pending join requests are `service.
JoinRequestReviewer`**, likewise a capability interface, implemented by
`AdminService` once member-side CSR join is configured
(`SetClusterJoinTokenStore`) — the same bind `MintJoinToken` depends on,
since the approval queue lives in the identical replicated store. See
[architecture.md](architecture.md#approval-queue-tokenless-join) for the
admission-vs-signing split: `ApproveJoinRequest` only records a decision,
and never itself calls the cluster CA.

**Drain, snapshot, add/remove member, leadership transfer, start/stop shard, shard state,
topology, replica state hashes, replica states and the node-keyspace compaction policy are system-tenant only** (cross-scope), as is every `rebalance:*`
route: they act on, or describe, shards every tenant shares, and name no
tenant. A tenant's Admin or SO -- whose built-in roles carry several of these
grants -- is refused with `403` / `PermissionDenied` on every transport.
Health, status, peers and capabilities stay self-scoped; `admin:peers` backs
the etcd listener's tenant-facing `Cluster.MemberList`.

**Start shard refuses the user range.** Shard IDs from `shard.UserMin` (1000)
up are issued by the cluster to databases; a start that names one is refused
with `service.UserRangeShardError` (400 / `INVALID_ARGUMENT`) on every
transport before the host is touched, so no group started by hand can hold an
ID the claimer gives a database.

**`GET /health` is unauthenticated**, so a load balancer can probe a node it
has no credentials for.

## Capability matrix — rebalance

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Shard stats | `GET /v1/rebalance/shards/{id}` | `RebalanceGetShardStats` | `rebalance.GetShardStats` | `qrdb_rebalance_shard_stats` | `GetShardStats` | `rebalance:stats` |
| Create plan | `POST /v1/rebalance/plan` | `RebalanceCreatePlan` | `rebalance.CreatePlan` | `qrdb_rebalance_plan` | `CreateRebalancePlan` | `rebalance:plan` |
| Execute | `POST /v1/rebalance/execute` | `RebalanceExecute` | `rebalance.Execute` | `qrdb_rebalance_execute` | `ExecuteRebalance` | `rebalance:execute` |
| Imbalance report | `GET /v1/rebalance/report` | `RebalanceGetImbalanceReport` | `rebalance.GetImbalanceReport` | `qrdb_rebalance_report` | `GetImbalanceReport` | `rebalance:report` |

`RebalanceExecute` takes a `RebalancePlan` as its request body — plan and apply
are separate calls, so a plan can be reviewed (or stored) before it runs.

## Membership through the SDK facade

`Client.Cluster()` exposes `Members`, `Leader`, `LeaderEndpoint`, `AddReplica`,
`RemoveReplica`, `PromoteReplica`, `TransferLeadership` and `WatchMembership`. **It adds no wire RPCs** — every
method is a composition of the rows above. `WatchMembership` polls at `DefaultMembershipPollInterval`
rather than streaming, so it works over unary-only REST.

## Forming a cluster from the SDK node

`node.NewClusteredNode(ctx, cfg, members, serverOpts, node.WithRaftMTLS(mtls))`
takes the other replicas' Raft identities in `cfg.Raft.MemberIdentities`
(`sdk.DaemonRaftConfig.MemberIdentities`), a comma-separated
`replicaID=spki:<hex>` list; the local entry is derived from the node's own
certificate: the leaf of `mtls.Certificate` when set, else `mtls.Cert`.
`mtls.Certificate` is a `*tls.Certificate` whose `PrivateKey` may be any
`crypto.Signer`, so a node whose key is held on a TPM or HSM forms a cluster
with no key PEM; `mtls.CACert` is required either way.
`node.MemberIdentityFromPEM(certPEM)` computes a peer's identity from its
certificate. Every refusal is a `*node.HostCreateError`: it wraps a
`*node.ConfigError` for `raft.member_identities` (itself unwrapping to the
`pkg/cluster` error) when the configuration is malformed or contradicts the
certificate, and `*quicraft.InvalidMemberError` when a member is left out.

## Adding a replica

`AdminClient.AddMember(ctx, shardID, replicaID, raftAddr, identity)` and
`Cluster().AddReplica(ctx, shardID, replicaID, raftAddr, identity)` take the
replica's member identity, or `""` to use the one go-quicraft already committed
for the replica or its host. Every transport carries it: the gRPC, Unix and
QUIC `AddMemberRequest.member_identity` field, the REST body's `identity`, the
MCP `qrdb_add_member` tool's `identity` argument, and the embedded transport's
`identity` argument. The refusals are typed and coded alike on every
transport; see [engineering.md](engineering.md#error-semantics). Start shard
takes no identities: it resolves every other member's from the committed
memberships, as a dedicated database's founders do, and answers the same typed
refusals with the same codes.

## Replica state hashes

A read of **this node's** replicas: for every shard the node replicates, the
replica's applied index and a SHA-256 digest of its state machine at exactly
that index. Each node answers from its own state machines with no proposal and
no leader round trip, so a caller compares replicas by asking every node and
lining the entries up by `(tenant_id, shard_id)`: two replicas at the same
`applied_index` must report the same `hash`. The digest definition and the
consistency guarantee are in
[engineering.md](engineering.md#replica-state-hashes).

Server-side it is `service.ReplicaStateHasher`, a capability interface
separate from `AdminServicer` for the reason `MemberPromoter` is one; the
transports type-assert for it. Client-side it is
`transport.ReplicaStateHasher`: a caller type-asserts
`client.Admin().(transport.ReplicaStateHasher)`, and all six SDK transports
implement it.

| Transport | Binding |
|---|---|
| REST | `GET /admin/state-hashes`, optional repeated `shard` query parameter (`?shard=1&shard=7`) |
| gRPC | `QRDBService.GetStateHashes(GetStateHashesRequest{repeated uint64 shard_ids})` |
| Unix | served by the same `qrdbServiceServer` gRPC implementation the TCP gRPC listener uses |
| QUIC | `admin.GetStateHashes`, the gRPC request and response messages as its payload |
| MCP | the `qrdb_state_hashes` tool, optional `shard_ids` integer array argument |
| Embedded | `StateHashes(ctx, shardIDs)`, a direct in-process call |

An empty shard list means every shard. A non-empty list selects the shards
with those IDs on every host the node runs; an ID this node does not
replicate selects nothing and is not an error.

Response (`GetStateHashesResponse`):

```json
{ "node_id": "node1",
  "replicas": [
    { "tenant_id": "", "shard_id": 1, "replica_id": 1,
      "applied_index": 4182, "records": 913,
      "hash": "9f2c…64 lowercase hex digits…", "error": "" },
    { "tenant_id": "", "shard_id": 3, "replica_id": 1,
      "applied_index": 0, "records": 0, "hash": "",
      "error": "state machine does not support state digests" },
    { "tenant_id": "acme", "shard_id": 1, "replica_id": 1,
      "applied_index": 77, "records": 12,
      "hash": "1b7e…", "error": "" } ] }
```

| Field | Meaning |
|---|---|
| `node_id` | the answering node's host ID, the `node_id` `GET /admin/topology` reports |
| `tenant_id` | `""` for a shard of the node's own (system) host; the tenant ID for a shard of an isolated tenant's host |
| `shard_id`, `replica_id` | the shard and this node's replica of it |
| `applied_index` | the replica's applied index the digest was computed at |
| `records` | how many records the digest covers |
| `hash` | the SHA-256 digest, 64 lowercase hex digits |
| `error` | empty on success; otherwise why this one replica has no digest, with `applied_index`, `records` and `hash` zero |

Entries are ordered by `tenant_id`, then `shard_id`. **One replica's failure
never fails the call:** a shard whose state machine cannot produce a digest
(a state-machine type without digest support, a witness, a replica not yet
loaded) reports it in its own `error` and every other entry is still
returned.

**Isolated tenants' shards are hashed inside their own barrier.** Each
started isolated-tenant host computes its own digests over the plaintext its
barrier decrypts, so a tenant's entries are comparable across replicas the
same way the system host's are. The read never starts a tenant's host: a
tenant whose host is not running on this node has no entries. A digest and a
record count disclose no record content, which is why a cross-scope read may
carry them for every tenant.

## Replica states

A read of what each replica this node hosts has applied, and the hash of
the state machine it holds at that point. Two replicas of one shard that
report the **same applied index** must report the **same hash**; a
different hash at an equal index means their state machines diverged. It is
the read that lets an operator, or a verification harness, prove that the
replicas of a shard agree, rather than infer it from the absence of errors.

**Every node answers for its own replicas, and only those.** The read is
never forwarded to a leader: the leader cannot report another node's state
machine, so asking it would answer a different question. To compare the
replicas of a shard, ask every node that hosts one and group the answers by
shard. A leader, a follower and an observer all answer the same way.

| Request | Response |
|---|---|
| `GET /admin/replica-states` | `200` → every replica on this node |
| `GET /admin/replica-states?shard_id=7` | `200` → this node's replica of shard 7 only |
| `shard_id` not a positive integer | `400` — `*rest.InvalidParameterError` |
| `shard_id` names a shard this node holds no state-machine replica of | `404` — `*service.ReplicaNotHostedError` |
| standalone mode | `501` — `*service.ReplicaStateUnsupportedError` (`requires mode: raft`) |
| a hosted state machine that computes no hash | `501` — `*service.StateMachineHashUnsupportedError`, naming the shard |
| barrier sealed | `503` — the sealed-window refusal every non-allowlisted route answers |

```json
{ "node_id": "node-2",
  "replicas": [ { "shard_id": 1, "replica_id": 2,
                  "applied_index": 48211, "hash": 1305972914470152846 } ] }
```

`shard_id` is optional; absent or `0` means every replica on the node.
`replicas` is ordered by `shard_id` and is empty — never absent — on a
node that hosts none. A **witness** holds no state machine and is never
listed; a filter that names a shard this node hosts only as a witness is
`404`, the same as a shard it does not host. A shard that stops on this
node while the read runs is left out of an unfiltered answer rather than
failing it.

**The hash.** `hash` is the unsigned 64-bit value the shard's state
machine computes over its replicated state (xxHash64, in key order, over
every key and value the state machine holds except node-local
bookkeeping). It is comparable between replicas **of the same shard** at
an equal `applied_index`, and nothing else: not across shards, not across
indexes, and not across releases that change what a state machine holds.
Every state machine go-qrdb ships computes one; an embedder's own state
machine that does not is reported as above rather than listed with a
made-up value. JSON carries it as a number, as it carries every other
64-bit field on this API; a client whose numbers are IEEE doubles must
decode it as a 64-bit integer, or it cannot compare two hashes exactly.

**The cost.** Computing a hash reads the whole state machine, so the read
costs time in proportion to the data a shard holds and holds the state
machine's read lock while it runs. It is a diagnostic, not something to
poll on a hot path.

**Read it on a quiet shard.** `applied_index` and `hash` are taken one
after the other, not as one atomic pair, so a reading taken while the
shard is applying entries can pair an index with the hash of a slightly
later state. Such a reading makes two replicas *disagree* at an equal
index; it can never make diverged replicas agree. A comparison is
therefore sound when it is taken after writes stop; a disagreement read
from a shard that was still applying entries proves nothing until it is
read again on a quiet shard.

The same read on every transport:

| Transport | Binding |
|---|---|
| REST | `GET /admin/replica-states`, above |
| gRPC | `QRDBService.GetReplicaStates(GetReplicaStatesRequest{shard_id})` → `ReplicaStatesResponse{node_id, replicas}`, permission carried as a `(qrdb.v1.permission)` method option |
| QUIC | `admin.GetReplicaStates`, the same protobuf messages as gRPC |
| Unix | served by the same `qrdbServiceServer` gRPC implementation the TCP gRPC listener uses |
| MCP | the `qrdb_replica_states` tool, optional numeric `shard_id` argument; the result text is the JSON above |
| Embedded | `Client.Admin().ReplicaStates(ctx, shardID)`, a direct in-process call |

Server-side it is `service.ReplicaStateReader`, a capability interface
separate from `AdminServicer` for the reason `MemberPromoter` is one; the
real `AdminService` implements it, and every transport type-asserts for
it. Client-side, because all six SDK transports implement it,
`ReplicaStates` is a plain method on `transport.AdminClient`. It requires
`admin:replica_state` (`ScopeCross`, system tenant only); no built-in role
other than `Owner` carries it, the same as `admin:shard_state` and
`admin:topology`.

## Schemas

`POST /membership/add`: shard ID, replica ID, Raft address, optional `identity`.
`POST /membership/remove`: shard ID, replica ID.

`ShardStatsResponse`:

```json
{ "shard_id": 1, "key_count": 1048576, "byte_count": 4294967296,
  "leader_id": 2, "replica_count": 3 }
```

`RebalancePlanRequest`:

```json
{ "max_moves": 10, "dry_run": true, "target_balance": 5.0 }
```

`max_moves: 0` means no limit; `target_balance: 0` means perfectly balanced.

`RebalancePlan`:

```json
{ "moves": [ { "shard_id": 7, "from_node": "node1", "to_node": "node4",
               "estimated_bytes": 2147483648 } ],
  "estimated_duration": 300000000000, "current_imbalance": 22.4 }
```

`RebalanceResult`:

```json
{ "moves_completed": 9, "moves_failed": 1,
  "duration": 284000000000, "final_imbalance": 3.1,
  "failures": [ { "shard_id": 7, "from_node": "node1", "to_node": "node4",
                  "step": "add-observer", "error": "..." } ] }
```

`failures` carries one entry per failed move, in plan order, so its length
equals `moves_failed`; it is omitted when every move completed. `step` names
the phase of a dedicated-group move that failed (`dropping`, `leader-routing`,
`wait-leader`, `find-source`, `add-placement`, `add-observer`, `remove-placement`,
`remove-node`) and is empty for a generic shard move. gRPC, QUIC and Unix
carry the same entries as repeated `RebalanceMoveFailure` messages.

Executing a plan first reads every database placement, because a move of a
shard that belongs to a tenant's dedicated Raft group must update that
group's placement record and deliver its keys.
A node that cannot read the placement records (for example during an
election) refuses the whole plan before any move with
`*service.RebalancePlacementReadError`: REST `503`, gRPC/MCP `UNAVAILABLE`.
Nothing was changed; resubmit the plan.

`ImbalanceReport`:

```json
{ "nodes": [ { "node_id": "node1", "address": "node1.example.com:63001",
               "shard_count": 12, "total_keys": 8000000,
               "total_bytes": 34359738368, "leader_count": 5 } ],
  "imbalance_score": 22.4, "hotspots": ["node1"] }
```

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Standalone mode (cluster verbs) | 501 | `UNIMPLEMENTED` — `requires mode: raft` |
| Unknown shard | 404 | `NOT_FOUND` |
| Removal would drop below `MinQuorum` | 409 | `FAILED_PRECONDITION` |
| Add/remove of an already-present/absent replica | 200 | `OK` — idempotent, skipped |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Node draining | 503 | `UNAVAILABLE` |
| Leadership transfer did not complete in its bound | 503 | `UNAVAILABLE` |
| Leadership transfer to an unknown replica | 404 | `NOT_FOUND` |
| Leadership transfer to an observer, witness or removed replica | 409 | `FAILED_PRECONDITION` |
| Consensus refused the request for now (busy, dropped, shard not ready, no leader yet) | 503 | `UNAVAILABLE` — retry |
| A consensus proposal or read timed out | 504 | `DEADLINE_EXCEEDED` — retry |

Cluster enrollment mostly runs at daemon startup, below every transport, so
most of its failures are startup errors rather than status codes. The one
exception is the member-side CSR join's SPKI/Direct transport: an admitted,
unsealed member with `--cluster-join-enabled` registers a plain-HTTPS route,
`POST /v1/enroll/join`, on its existing admin listener, pinned the same way
`/v1/ca/bootstrap` is. Unlike every other route in this document it needs no
bearer credential — the join-admission token in the request body is the
credential — and it is never a member of the sealed-window allowlist, because
the cluster CA it signs with is available only once the barrier is unsealed.

| Request / response | Meaning |
|---|---|
| `{"token", "csr_der", "node_uri"}` → `{"cert_pem"}` | the single-use token is consumed (CAS-delete) and checked, the CSR's signature verified, and the leaf is issued for the token's BOUND identity -- `node_uri` in the request is never consulted FOR SIGNING; a joiner does not get to assert an identity of its own choosing |
| `{"token", "csr_der", "node_uri"}` → `{"error"}`, `403` | an unknown, expired, already-consumed, wrong-trust-domain, or retired-identity token, a malformed/unsigned CSR, a CSR whose subject alternative names do not match `node_uri` exactly (one URI SAN, no DNS/IP/email SANs), or a signing failure -- one cause-independent reason for the token itself, so the response carries no token-validity oracle; the CSR-shape checks (weak key, SAN mismatch) report their own specific message before the token is ever touched, so a corrected resubmission with the SAME token still succeeds |
| unconfigured (`--cluster-join-enabled` unset) | the path is not registered at all — not a `404`, an absent route |
| rate-limited or locked out | `429`, shared with the Noise_NK transport by source address — switching transports mid-attack gains nothing |

### `POST /v1/cluster/join-tokens`

Mints a single-use, identity-bound join token for an admin to hand to a
joining node. Requires permission `cluster:join_token` (`ScopeCross`,
platform-only — held by `Owner` and by the cluster `Admin` role; only the Admin grant survives strict separation of
duties, see [../seal/fips140-mode.md](../seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips)). Available whenever
an admin service is configured; reports `501` when member-side CSR join
itself is not configured on this node (no join-token store bound).

| Request | Response |
|---|---|
| `{"node_id", "ttl_seconds"}` → `201` | `{"token", "expires_at"}` — the token is returned exactly once; there is no "list minted tokens" read path |
| missing or malformed `node_id` | `400` — `*clusterenroll.InvalidJoinIdentityError` |
| `ttl_seconds` outside `[MinJoinTokenTTL, MaxJoinTokenTTL]` | `400` — `*clusterenroll.JoinTokenTTLError` |
| member-side CSR join not configured on this node | `501` — `*service.ClusterJoinTokenNotConfiguredError` |

This verb is implemented on all six transports (`transport.
ClusterJoinTokenMinter`, consumed by the `qrdb admin join-token mint` CLI —
see [cli.md](cli.md)):

| Transport | Binding |
|---|---|
| REST | `POST /v1/cluster/join-tokens`, above |
| gRPC | `QRDBService.MintJoinToken`, permission carried as a `(qrdb.v1.permission)` method option exactly like every other gRPC verb |
| QUIC | `admin.MintJoinToken` verb |
| Unix | served by the same `qrdbServiceServer` gRPC implementation the TCP gRPC listener uses |
| MCP | the `qrdb_mint_join_token` tool |
| Embedded | direct in-process call, no serialization |

The five REMOTE bindings (REST, gRPC, QUIC, Unix, MCP) report the identical
`ScopeCross` permission denial for a tenant-scoped caller, and the
identical typed errors above for every other failure — there is exactly one
implementation of the mint logic (`service.AdminService.MintJoinToken`),
never one per transport.

**Embedded authorizes through the same `AuthMiddleware` as the network
transports** when one is installed; with none installed it admits every
call, as a network transport run with `--auth-enabled=false` does.
`AdminService.MintJoinToken` carries no permission check of its own: like
every other `AdminServicer` verb, enforcement lives in the transport.
The rule this implies is about what a response CARRIES, not which verb it
names: a secret-bearing output -- `MintJoinToken`'s minted token, in
particular -- must never be returned through a less-trusted local surface
such as a desktop webview, even though ordinary admin verbs with no
secret in their response may stay wired through one as the operator's own
local UI — see [../embedding.md](../embedding.md).

### `GET /v1/cluster/join-requests`, `POST .../approve`, `POST .../reject`

Lists, approves and rejects CSRs a joiner queued through the tokenless
member-side CSR approval path (`req.Token == ""`; see
[architecture.md](architecture.md#approval-queue-tokenless-join)). All
three require permission `cluster:join_token` (`ScopeCross`,
platform-only) — the identical whole-cluster admission decision minting a
join token already requires. Available whenever an admin service is
configured; reports `501` when member-side CSR join itself is not
configured on this node, exactly as the join-token verb above does.

| Request | Response |
|---|---|
| `GET /v1/cluster/join-requests` | `200` → `{"requests": [{"id", "node_uri", "replica_id", "features", "submitted_at", "expires_at"}, ...]}` — only entries still awaiting a decision; a decided (approved, rejected, delivered) entry is never listed |
| `POST .../approve` `{"id", "node_id"}` | `200` → `{"status":"ok"}` — binds the CSR to `spiffe://<trust domain>/node/<node_id>`, never signs it |
| `POST .../reject` `{"id", "reason"}` | `200` → `{"status":"ok"}` — a terminal refusal |
| `node_id` already admitted or reserved | `409` — `*clusterenroll.ExistingMemberError`, the identical refusal `join-token mint` reports for the same condition |
| `id` absent, expired, or already decided | `404` — `*clusterenroll.PendingJoinRequestNotFoundError` |
| member-side CSR join not configured on this node | `501` — `*service.ClusterJoinTokenNotConfiguredError` |

Implemented on all six transports (`transport.JoinRequestReviewer`,
consumed by the `qrdb admin join-request` CLI — see [cli.md](cli.md)):

| Transport | Binding |
|---|---|
| REST | the three routes above |
| gRPC | `QRDBService.{ListPendingJoinRequests,ApproveJoinRequest,RejectJoinRequest}` |
| QUIC | `admin.{ListPendingJoinRequests,ApproveJoinRequest,RejectJoinRequest}` |
| Unix | served by the same `qrdbServiceServer` gRPC implementation the TCP gRPC listener uses |
| MCP | `qrdb_list_pending_join_requests`, `qrdb_approve_join_request`, `qrdb_reject_join_request` |
| Embedded | direct in-process call, no serialization |

Unlike `MintJoinToken`'s response, nothing these three verbs return is a
secret, so none of the embedding.md local-surface exposure rules apply to
them.

### `POST /v1/cluster/join-identities/retire`

Permanently refuses any future admission for a node identity -- a fresh
join-token mint, or a renewal of one whose admission has lapsed -- after
it is removed from the cluster or its issued certificate must be treated
as revoked (see [architecture.md](architecture.md#admission-lifecycle-lapsed-renewal-retirement-csr-identity)).
Requires permission `cluster:join_identity_retire` (`ScopeCross`,
platform-only — no built-in role carries it by default). Available
whenever an admin service is configured; reports `501` when member-side
CSR join itself is not configured on this node (no join-token store
bound -- the SAME store `MintJoinToken` uses).

| Request | Response |
|---|---|
| `{"node_id", "reason"}` → `200` | `{"status": "ok"}` — `reason` is optional, free-text, carried for audit only |
| missing or malformed `node_id` | `400` — `*clusterenroll.InvalidJoinIdentityError` |
| member-side CSR join not configured on this node | `501` — `*service.ClusterJoinTokenNotConfiguredError` |

Retirement is unconditional: it succeeds regardless of the identity's
current state (absent, reserved, active, lapsed, or already retired) and
is never reclaimed by a later join attempt, lapsed or not. A subsequent
`MintJoinToken` for the SAME identity reports `403` with the distinct
`*clusterenroll.RetiredJoinIdentityError`, never the generic
`*ExistingMemberError` an active (non-retired) admission reports.

This verb is implemented on all six transports (a method on
`transport.AdminClient` directly, since — unlike `MintJoinToken` — its
response carries no secret; consumed by the `qrdb admin join-token retire`
CLI — see [cli.md](cli.md)):

| Transport | Binding |
|---|---|
| REST | `POST /v1/cluster/join-identities/retire`, above |
| gRPC | `QRDBService.RetireJoinIdentity`, permission carried as a `(qrdb.v1.permission)` method option exactly like every other gRPC verb |
| QUIC | `admin.RetireJoinIdentity` verb |
| Unix | served by the same `qrdbServiceServer` gRPC implementation the TCP gRPC listener uses |
| MCP | the `qrdb_retire_join_identity` tool |
| Embedded | direct in-process call, no serialization |

The five REMOTE bindings report the identical `ScopeCross` permission
denial for a tenant-scoped caller, and the identical typed errors above
for every other failure — there is exactly one implementation
(`service.AdminService.RetireJoinIdentity`), never one per transport.
Unlike `MintJoinToken`, this response carries no secret, so — like
`PromoteMember` — it is wired through the embedded transport the same way
every other ordinary admin verb is, with no carve-out in
[../embedding.md](../embedding.md).

The Noise_NK join transport and the trust-anchor fetch have no HTTP surface of
their own; their Go surface is in `pkg/clusterenroll`, re-exported from
`sdk/go/clusterenroll`:

| Symbol | Meaning |
|---|---|
| `NoiseAvailable() bool` | whether this runtime may use the Noise NK suite; false in FIPS 140-3 mode |
| `*NoiseFIPSUnavailableError` | a join configured with Noise as its only method, in FIPS 140-3 mode; the message names FIPS and the P-256 flag and approved methods to use |
| `P256KeyFile`, `JoinP256KeyFile` | file names of the persisted P-256 responder keys (`p256-key.hex`, `join-p256-key.hex`) |
| `*P256KeyFileSizeError` | a persisted P-256 key file that is not a 32-byte hex key; never regenerated |
| `*JoinSuiteUnavailableError`, `*MissingJoinServerKeyError` | a join exchange with no usable suite for this runtime, or with no server key configured for the suite chosen |
| `(*Advertiser).P256PublicKey()` | the P-256 responder public key joiners pin; always available, FIPS or not |
| `(*Advertiser).NoiseEnabled() bool` | whether THIS node is running a Noise listener; a non-nil `Advertiser` means the node advertises enrollment, which is a different question. Every admitted, unsealed member gets one -- see [architecture.md](architecture.md#member-side-csr-join-key-a-cluster-ca) -- not only the node that generated the cluster CA |

See [engineering.md](engineering.md#fips-140-3--the-p-256-suite-replaces-noise-nk)
and [architecture.md](architecture.md) for the member-side CSR join design.

The cluster feature gate is not an API surface either: a node refused under a
feature the cluster activated fails at startup with
`*featuregate.UnsupportedFeatureError`. Its Go surface, for an embedder
composing its own node, is `pkg/cluster/featuregate` and
`(*sdk.Client).StartFeatureGate` (see
[engineering.md](engineering.md#cluster-feature-gate--pkgclusterfeaturegate)).

## Pagination

None of these results are paginated. `Peers`, `GetTopology` and the imbalance
report are bounded by cluster size.

## Versioning

Admin paths are unversioned (`/health`, `/status`, `/peers`, `/drain`,
`/snapshot`, `/membership/*`, `/capabilities`, `/admin/*`); rebalance paths are
under `/v1/`. Nothing here is deprecated.
