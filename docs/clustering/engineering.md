# Clustering — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

**Raft mode only.** In standalone every capability here refuses with
`requires mode: raft`.

## Exported surface

### Discovery — `pkg/discovery`

```go
type Method interface {
	Discover(ctx context.Context) ([]Peer, error)
	Name() string
	Stop() error
}

type Peer struct {
	NodeID  uint64
	Address string
}
```

Four shipped methods: `static`, `multicast`, `dns`, `token`.

### Cluster formation — `pkg/cluster`

```go
type Options struct {
	Method          string
	NodeID          uint64
	RaftAddress     string
	MulticastSecret string
	// ...
}

func BootstrapShard(ctx context.Context, node ..., opts Options, shardID uint64, createFn ..., shardCfg ...) error

// Exported helpers for finer-grained use:
func BuildDiscovery(...) (...)
func ParsePeers(...)   (...)
func IsSingleNode(...) bool
```

`BootstrapShard` runs, in order: **discover → validate addresses → add self →
check quorum → build member map → `StartShard`.**

### Dynamic membership — `pkg/cluster`

`MembershipManager`, built against two narrow interfaces so it is testable and
embeddable without import cycles:

- `MembershipStore` — lease-backed entries plus a prefix watch.
- `Reconfigurer` — Raft add/remove plus leader lookup, and `IsMember`.

SDK wiring: `client.NewMembershipManager(self, shardID, cfg)`, or the
lower-level `client.NewMembershipStore()` / `client.NewMembershipReconfigurer()`.

### Node administration — `sdk/go/transport`

```go
type AdminClient interface {
	Health(ctx context.Context) (*HealthResponse, error)
	Status(ctx context.Context) (*StatusResponse, error)
	Peers(ctx context.Context) (*PeersResponse, error)
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error
	RequestSnapshot(ctx context.Context, shardID uint64) error
	Drain(ctx context.Context) error
	Capabilities(ctx context.Context) (*CapabilitiesResponse, error)
	StartShard(ctx context.Context, req *StartShardRequest) error
	StopShard(ctx context.Context, shardID uint64) error
	GetShardState(ctx context.Context, shardID uint64) (*ShardStateResponse, error)
	GetTopology(ctx context.Context) (*TopologyResponse, error)
}
```

### SDK `Cluster()` facade

A thin facade over the already-wired control-plane operations — **it adds no new
wire RPCs**:

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)        // filtered from Peers()
leader, ok, _ := cc.Leader(ctx, shardID)      // from GetShardState()
_ = cc.AddReplica(ctx, shardID, id, raftAddr) // delegates to AddMember
_ = cc.RemoveReplica(ctx, shardID, id)        // delegates to RemoveMember
ch, _ := cc.WatchMembership(ctx, shardID)     // polls, emits deltas
// ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
```

`WatchMembership` **polls** at `DefaultMembershipPollInterval` rather than using
a streaming RPC, so it works over every transport including unary-only REST.

### Rebalance — `sdk/go/transport`

```go
type RebalanceClient interface {
	GetShardStats(ctx context.Context, shardID uint64) (*ShardStatsResponse, error)
	CreateRebalancePlan(ctx context.Context, req *RebalancePlanRequest) (*RebalancePlan, error)
	ExecuteRebalance(ctx context.Context, plan *RebalancePlan) (*RebalanceResult, error)
	GetImbalanceReport(ctx context.Context) (*ImbalanceReport, error)
}

type ShardStatsResponse struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}
type RebalancePlanRequest struct {
	MaxMoves      int     `json:"max_moves"`      // 0 = no limit
	DryRun        bool    `json:"dry_run"`
	TargetBalance float64 `json:"target_balance"` // 0-100; 0 = perfectly balanced
}
type RebalanceMove struct {
	ShardID        uint64 `json:"shard_id"`
	FromNode       string `json:"from_node"`
	ToNode         string `json:"to_node"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
}
type RebalancePlan struct {
	Moves             []RebalanceMove `json:"moves"`
	EstimatedDuration time.Duration   `json:"estimated_duration"`
	CurrentImbalance  float64         `json:"current_imbalance"`
}
type RebalanceResult struct {
	MovesCompleted int           `json:"moves_completed"`
	MovesFailed    int           `json:"moves_failed"`
	Duration       time.Duration `json:"duration"`
	FinalImbalance float64       `json:"final_imbalance"`
}
type NodeLoad struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	ShardCount  int    `json:"shard_count"`
	TotalKeys   uint64 `json:"total_keys"`
	TotalBytes  uint64 `json:"total_bytes"`
	LeaderCount int    `json:"leader_count"`
}
type ImbalanceReport struct {
	Nodes          []NodeLoad `json:"nodes"`
	ImbalanceScore float64    `json:"imbalance_score"`
	Hotspots       []string   `json:"hotspots"`
}
```

## Invariants

- **Dynamic membership is off by default.** Raft reconfiguration occurs only
  when a `Reconfigurer` is supplied **and the local node is the leader**.
- **The quorum guard (`MinQuorum`) refuses to remove a replica** when doing so
  would drop active membership below quorum. A transient liveness blip cannot
  destroy the cluster.
- **Join and leave are idempotent.** The manager consults
  `Reconfigurer.IsMember` and skips `AddMember` for replicas already present
  (the initial bootstrap members) and `RemoveMember` for replicas already gone.
- **One in-flight reconfiguration per replica**, applied by a single serialized
  worker; **latest intent wins**. Reconfiguration retries with capped
  exponential backoff.
- **Reconciliation is snapshot-based.** On startup and after a watch
  re-subscribe the manager reconciles against a full `ListMembers` snapshot, so
  members that joined or left while it was not watching converge in **both**
  directions. This is O(cluster size) and deliberate.
- **Multicast requires an operator-chosen PSK.** There is no default secret;
  announcements are HMAC-SHA256 authenticated.
- **Member entries live at `__sys:cluster/members/{nodeID}`**, lease-backed and
  refreshed on a keepalive interval. A dead node's keepalive stops, the lease
  expires, the entry is deleted, and a prefix watch surfaces the removal.
- **The membership store shares one lessor and watch hub with the SDK
  `Lease()`/`Watch()` surface** — one source of truth, two consumers.
- **The `Cluster()` facade adds no wire RPCs.** Everything it does is a
  composition of `Peers`, `GetShardState`, `AddMember` and `RemoveMember`.
- **Rebalance `plan` with `DryRun` produces the plan without executing it**;
  `execute` takes a plan as input, so plan and apply are separable.

## Permission model

| Permission | Gates |
|---|---|
| `admin:health` | node health |
| `admin:status` | detailed node status |
| `admin:peers` | shard peer membership |
| `admin:capabilities` | effective capability profile |
| `admin:drain` | drain mode |
| `admin:snapshot` | requesting a shard snapshot |
| `admin:add_member` / `admin:remove_member` | Raft reconfiguration — gated apart from reading peers |
| `admin:start_shard` / `admin:shard_state` | shard lifecycle and state |
| `rebalance:stats` / `rebalance:plan` / `rebalance:report` | read-only |
| `rebalance:execute` | **physically moves shards** — separate grant |

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Standalone mode | `requires mode: raft` | **yes** |
| Discovery finds no peers | `BootstrapShard` fails at the quorum check | **yes** |
| Multicast configured without a secret | method does not run | **yes** |
| `RemoveMember` would drop below `MinQuorum` | refused by the quorum guard | **yes** |
| `AddMember` for an existing replica | skipped (idempotent) | n/a |
| `RemoveMember` for an absent replica | skipped (idempotent) | n/a |
| Reconfiguration transient failure | retried with capped exponential backoff | — |
| Unknown shard on state/snapshot | not-found | yes |
| Missing permission | permission denied | **yes** |

## Configuration

Discovery lives under the `discovery` block — see
[../configuration/](../configuration/).

| Setting | Flag | Meaning |
|---|---|---|
| `discovery.dynamic_membership` | `--dynamic-membership` | enable the membership manager |
| `discovery.min_peers` | `--cluster-min-peers` | auto-removal quorum floor (`MinQuorum`) |
| `raft.peers` | — | `id=address` specs for the `static` method |
| DNS SRV target | — | `{service}.{proto}.{domain}`, default `_raft._udp` |

## Test strategy

- **Unit**: each discovery method, `Manager` chaining and retry,
  `BootstrapShard`'s six steps including the quorum refusal, and
  `MembershipManager` against the two narrow interfaces (that is what they are
  for).
- **The quorum guard and the idempotence checks are the important tests** —
  they are what stops automatic reconfiguration destroying a cluster.
- **No `time.Sleep`**: lease expiry and the keepalive are driven through the
  clock seam.
- **Integration**: the 3-node Raft suites — `isolation_raft_cluster_test.go`,
  `isolation_raft_dedicated_wal_test.go`, `isolation_raft_rebalance_test.go`,
  `isolation_raft_scale_test.go`, `cluster_test.go`, `drain_test.go`,
  `cluster_enable_encryption_test.go` — live in `make integration-test-cluster`.
  **That stage is not currently wired into CI**, so cluster behaviour is not
  exercised by CI today. This is a known and tracked gap.

## Observability

- `GET /health` is public; `admin status`, `admin peers` and `admin topology`
  are the operational views.
- `rebalance report` gives per-node load and names hotspots;
  `ImbalanceScore` is the single number to alert on.
- Membership changes are audited through [../audit/](../audit/).
