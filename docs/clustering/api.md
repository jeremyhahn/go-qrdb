# Clustering — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

Everything except `Health`, `Status` and `Capabilities` is **raft mode only**
and refuses in standalone with `requires mode: raft`.

## Capability matrix — node administration

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Admin()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Health | `GET /health` — **public** | `Health` | `admin.Health` | `qrdb_health` | `Health` | none (public) |
| Status | `GET /status` | `Status` | `admin.Status` | `qrdb_status` | `Status` | `admin:status` |
| Peers | `GET /peers` | `Peers` | `admin.Peers` | `qrdb_peers` | `Peers` | `admin:peers` |
| Capabilities | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Capabilities` | `admin:capabilities` |
| Drain | `POST /drain` | `Drain` | `admin.Drain` | `qrdb_drain` | `Drain` | `admin:drain` |
| Request snapshot | `POST /snapshot` | `RequestSnapshot` | `admin.RequestSnapshot` | `qrdb_snapshot` | `RequestSnapshot` | `admin:snapshot` |
| Add member | `POST /membership/add` | `AddMember` | `admin.AddMember` | `qrdb_add_member` | `AddMember` | `admin:add_member` |
| Remove member | `POST /membership/remove` | `RemoveMember` | `admin.RemoveMember` | `qrdb_remove_member` | `RemoveMember` | `admin:remove_member` |
| Start shard | `POST /admin/shards` | `StartShard` | `admin.StartShard` | `qrdb_start_shard` | `StartShard` | `admin:start_shard` |
| Stop shard | `DELETE /admin/shards/{id}` | `StopShard` | `admin.StopShard` | `qrdb_stop_shard` | `StopShard` | `admin:stop_shard` |
| Shard state | `GET /admin/shards/{id}/state` | `GetShardState` | `admin.GetShardState` | `qrdb_shard_state` | `GetShardState` | `admin:shard_state` |
| Topology | `GET /admin/topology` | `GetTopology` | `admin.GetTopology` | `qrdb_topology` | `GetTopology` | `admin:topology` |

**`GET /health` is one of only two unauthenticated routes in the server** (the
other is `GET /v1/config/initialized`), so a load balancer can probe a node it
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

`Client.Cluster()` exposes `Members`, `Leader`, `AddReplica`, `RemoveReplica`
and `WatchMembership`. **It adds no wire RPCs** — every method is a composition
of the rows above. `WatchMembership` polls at `DefaultMembershipPollInterval`
rather than streaming, so it works over unary-only REST.

## Forming a cluster from the SDK node

`node.NewClusteredNode(ctx, cfg, members, serverOpts, node.WithRaftMTLS(mtls))`
takes the other replicas' Raft identities in `cfg.Raft.MemberIdentities`
(`sdk.DaemonRaftConfig.MemberIdentities`), a comma-separated
`replicaID=spki:<hex>` list; the local entry is derived from `mtls.Cert`.
`node.MemberIdentityFromPEM(certPEM)` computes a peer's identity from its
certificate. Every refusal is a `*node.HostCreateError`: it wraps a
`*node.ConfigError` for `raft.member_identities` (itself unwrapping to the
`pkg/cluster` error) when the configuration is malformed or contradicts the
certificate, and `*quicraft.InvalidMemberError` when a member is left out.

`AddReplica` (like `AddMember` on every transport) carries no identity, so on a
multi-node cluster go-quicraft refuses it with `ErrMemberIdentityRequired`.

## Schemas

`POST /membership/add`: shard ID, replica ID, Raft address.
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
  "duration": 284000000000, "final_imbalance": 3.1 }
```

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

Cluster enrollment is not an API surface — it runs at daemon startup, below
every transport — so its failures are startup errors rather than status codes.
The Go surface is in `pkg/clusterenroll`, re-exported from
`sdk/go/clusterenroll`:

| Symbol | Meaning |
|---|---|
| `NoiseAvailable() bool` | whether this runtime may use the Noise NK method; false in FIPS 140-3 mode |
| `*NoiseFIPSUnavailableError` | a join configured with Noise as its only method, in FIPS 140-3 mode; the message names FIPS and the approved methods |
| `(*Advertiser).NoiseEnabled() bool` | whether this first node is running a Noise listener; a non-nil `Advertiser` means the node advertises enrollment, which is a different question |

See [engineering.md](engineering.md#fips-140-3--the-noise-nk-method-is-unavailable).

## Pagination

None of these results are paginated. `Peers`, `GetTopology` and the imbalance
report are bounded by cluster size.

## Versioning

Admin paths are unversioned (`/health`, `/status`, `/peers`, `/drain`,
`/snapshot`, `/membership/*`, `/capabilities`, `/admin/*`); rebalance paths are
under `/v1/`. Nothing here is deprecated.
