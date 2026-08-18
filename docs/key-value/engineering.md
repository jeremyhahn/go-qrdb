# Key-Value Store — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

### SDK — `sdk/go`

```go
func (c *Client) KV() transport.KVClient
func (c *Client) MVCC() transport.MVCCClient
func (c *Client) KVStore() KVStore          // kvstore adapter for DAOs
func (c *Client) DatabaseScoped(name string) *Client
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`sdk/go/transport`:

```go
type KVClient interface {
	Put(ctx context.Context, key string, value []byte, opts ...WriteOption) error
	Get(ctx context.Context, key string, opts ...ReadOption) ([]byte, error)
	Delete(ctx context.Context, key string, opts ...WriteOption) error
	List(ctx context.Context, prefix string, opts ...ReadOption) ([]string, error)
	Scan(ctx context.Context, prefix string, opts ...ReadOption) (map[string][]byte, error)
	Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error)
}

type MVCCClient interface {
	PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...WriteOption) (*MVCCKeyValue, error)
	GetRevision(ctx context.Context, key string, opts ...ReadOption) (*MVCCKeyValue, error)
	GetAtRevision(ctx context.Context, key string, revision int64, opts ...ReadOption) (*MVCCKeyValue, error)
	Range(ctx context.Context, start, end []byte, rangeOpts RangeOptions, opts ...ReadOption) (*RangeResponse, error)
	Compact(ctx context.Context, revision int64) error
	CurrentRevision(ctx context.Context) (int64, error)
	RestoreToRevision(ctx context.Context, req *RestoreRevisionRequest) (*RestoreRevisionResult, error)
}
```

Every transport carries the compile-time proof that it implements both:
`var _ transport.KVClient = (*Transport)(nil)`.

### Consistency levels — `pkg/consistency`

`consistency.Level` is an `iota` enum, in this order:

| Constant | `String()` | Mechanism | Typical latency |
|---|---|---|---|
| `Strong` | `strong` | full Raft consensus (SyncRead) | 10–50 ms |
| `Linearizable` | `linearizable` | Raft ReadIndex — **the default** | 1–5 ms |
| `LeaseRead` | `lease_read` | leader serves locally under a valid leadership lease | sub-ms |
| `Eventual` | `eventual` | local state machine, no coordination | <1 ms |
| `FollowerRead` | — | follower confirms applied index via ReadIndex, then reads locally | between `Eventual` and `Strong` |

`LeaseRead` trades a staleness window bounded by the election timeout during a
leader failover. `Eventual` has no such bound on a partitioned node.

### Write durability — `pkg/writemode`

Re-exported from go-quicraft; qrdb does not define its own:

| Constant | Meaning |
|---|---|
| `writemode.Sync` | fsync before acknowledging |
| `writemode.WALDurable` | WAL-durable, deferred data fsync |
| `writemode.Batched` | group-committed by the batch aggregator |

### Internal key guard — `pkg/service`

```go
const InternalKeyPrefixSystem = "__sys:"
func InternalKeyPrefixes() []string
func IsInternalKey(key string) bool
func SkipInternalKeyFilter(prefix string) bool
```

Reserved prefixes: `__sys:`, `__tbl:`, `__idx:`, the table-sequence prefix,
`ts:`, `idx:metric:`, `idx:tag:`, `retention:`.

`InternalKeyPrefixes` returns a **copy** so no caller can widen the user
keyspace by mutating the backing array.

`SkipInternalKeyFilter` grants an enumeration escape only when the requested
prefix lies *inside* a reserved namespace whose owner reads its own records
back through the shared `KVServicer`. A prefix that merely *spans* one — `_`,
which covers `__sys:` and `__tbl:`, or `t`, which covers `ts:` — is an ordinary
user prefix and is filtered. That distinction is what stops a one-character
sweep becoming an enumeration of the internal keyspace.

## Invariants

- **Reads and writes are scoped to exactly one (tenant, database) pair**,
  resolved server side. The tenant comes from the authenticated identity; a
  client-supplied tenant field is never trusted.
- **`DatabaseScoped` returns a new client**, sharing the connection but carrying
  its own immutable scope. Scopes never leak between clients and no global
  mutable state is involved. On a transport with no per-database support (the
  embedded transport) the receiver is returned unchanged.
- **Revision monotonicity**: the store's current revision only increases.
  `Compact(r)` discards revisions strictly older than `r`; reads below the
  compaction floor fail closed rather than returning an approximation.
- **Restore is a logical undo, never a Raft rewind.** Every change is proposed
  as an ordinary forward write. Committed history is never discarded.
- **Restore is not atomic across keys.** Each key is an independent Raft
  proposal; partial application is possible and retry is safe.
- **Restore blast radius is capped at `service.MaxRestoreKeys` (10000).** A
  scope matching more is refused whole, never silently truncated.
- **Restore scope is exactly one of `--prefix` or `--keys`.** Neither (unscoped)
  and both (ambiguous) are refused.
- **`kv restore` and as-of reads require `raft.sm_type: mvcc`.** The default
  `pebble` state machine keeps no revision history and refuses them.

## Error semantics

Errors are typed values in `pkg/errors` and the per-transport error packages
(`pkg/api/errors*.go`); `fmt.Errorf` is not used. Every error crosses the SDK
boundary as a typed value and is mapped per transport by
[../../conventions](../api-server/engineering.md).

| Condition | Behavior | Fails closed? |
|---|---|---|
| Key absent | not-found, distinct from an empty value | n/a |
| Requested revision above current | invalid-argument | yes |
| Requested revision below the compaction floor | error, never an approximate value | **yes** |
| As-of read against a non-MVCC state machine | `service: requires raft.sm_type: mvcc` | **yes** |
| Restore naming a reserved prefix or key | refused, HTTP 403 equivalent, **no override** | **yes** |
| Restore with no scope or both scopes | refused | **yes** |
| Restore matching more than `MaxRestoreKeys` | refused whole | **yes** |
| Restore CAS conflict without `--force` | key left untouched, reported as `CONFLICT` | **yes** |
| Barrier sealed | request refused; no plaintext and no ciphertext served | **yes** |
| Node is not the leader and consistency requires it | routed to the leader, or refused when no resolver is configured | yes |

The restore refusal on `__sys:` has no override by design: restoring
`__sys:roles/…` or `__sys:users/…` to an earlier revision would undo a
permission revocation.

## Configuration

| Key | Values | Default | Effect |
|---|---|---|---|
| `raft.sm_type` | `pebble`, `memory`, `mvcc` | `pebble` | State-machine wrapper. Only `mvcc` supports as-of reads, watch replay and selective restore. |
| `storage.backend` | `pebble`, `file`, `memory` | `pebble` | Underlying storage engine. |
| `mvcc.compaction_retention` | integer | — | Minimum revisions retained; sets the compaction floor. |

```yaml
raft:
  sm_type: mvcc
mvcc:
  compaction_retention: 1000
```

Full reference: [../configuration/](../configuration/).

## Persistence

- **Key layout** is owned by the state machine. The MVCC engine encodes
  `(key, revision)` composite keys so a range scan at a revision is a bounded
  seek rather than a full scan.
- **Revision triple**: each value carries create-revision, mod-revision and
  version, deliberately etcd-shaped so
  [../etcd-compatibility/](../etcd-compatibility/) is a translation.
- **Compatibility**: the reserved-prefix set is additive. Adding a prefix
  narrows the user keyspace, so a new one is a breaking change for any consumer
  already writing under it and must be treated as such under
  `conventions/release-versioning.md`.

## Test strategy

- **Unit**: `pkg/kv`, `pkg/mvcc`, `pkg/service` — happy path plus error path per
  function, 90%+ coverage goal.
- **Integration, standalone stage**: `make integration-test-standalone`. Runs in
  CI today.
- **Integration, cluster stage**: `make integration-test-cluster` — the 3-node
  Raft suites (`isolation_raft_cluster_test.go`, `cluster_test.go`,
  `drain_test.go`, `backup_restore_test.go` and siblings). The harness is wired
  in the `Makefile`; **the CI job that runs it is not yet wired**, so these
  suites are dark in CI. Tracked as a known gap.
- **Transport parity**: each transport's `CommandDefinition` set is exercised
  against the same expectations, so a `NotImplementedError` on one transport
  fails parity rather than passing as "partial".
- **Fault injection seams**: the storage backend and the state machine are both
  interfaces, so a failing backend is injected rather than simulated.

## Observability

- **Metrics** are exposed on `GET /metrics` in Prometheus format, gated on a
  configured gatherer.
- **Health** is `GET /health`; it is a public route by design so a load balancer
  can probe an unauthenticated node.
- **Audit**: KV mutations are recorded through [../audit/](../audit/) when the
  audit logger is configured.
- **Logging** uses the shared `pkg/logger`; levels are `trace | debug | info |
  warn | error`.
