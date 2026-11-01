# Distributed Locks — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Lock() transport.LockClient
```

`sdk/go/transport`:

```go
type LockClient interface {
	LockAcquire(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)
	LockTryLock(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)
	LockRelease(ctx context.Context, req *LockReleaseRequest) error
	LockExtend(ctx context.Context, req *LockExtendRequest) (*LockInfo, error)
	LockStatus(ctx context.Context, key string) (*LockInfo, error)
	LockList(ctx context.Context, prefix string) (*LockListResponse, error)
}

type LockInfo struct {
	Key          string `json:"key"`           // user key, without the internal prefix
	FencingToken int64  `json:"fencing_token"` // the lock key's MVCC create revision
	LeaseID      int64  `json:"lease_id"`
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"` // always 1 for non-reentrant
	TTL          int64  `json:"ttl"`
	AcquiredAt   int64  `json:"acquired_at"`
}

type LockAcquireRequest struct {
	Key       string `json:"key"`
	HolderID  string `json:"holder_id,omitempty"` // auto-generated UUID when empty
	TTL       int64  `json:"ttl"`
	Reentrant bool   `json:"reentrant,omitempty"`
}

type LockReleaseRequest struct{ Key string; FencingToken int64 }
type LockExtendRequest  struct{ Key string; FencingToken int64 }
type LockListResponse   struct{ Locks []*LockInfo }
```

Internal storage constant, `pkg/lock`:

```go
const lockKeyPrefix = "__sys:lock/"
```

Per-(tenant, database) resolution, `pkg/service`:

```go
// On service.Registry.
LockService(tenant, db string) (LockServicer, error)
```

Every transport resolves through it rather than holding a process-wide
servicer: `lockSvcFor` in `pkg/api/{rest,grpc,quic}` and `resolveLock` in
`pkg/api/mcp`. Each falls back to the singleton ONLY when no registry is
configured, which is the registry-less composition.

## Invariants

- **`FencingToken` is the lock key's MVCC create revision** — the revision of
  the put that acquired the lock, the etcd `CreateRevision` model. Revisions are
  globally ordered and monotonically increasing, and a released or expired lock
  is deleted, so **each successful acquisition of a given lock has a strictly
  higher token than the previous one.** This is the only ordering guarantee a
  downstream resource needs. A reentrant re-acquisition updates the key, which
  moves its `ModRevision` but not its create revision, so it returns the token
  of the first acquisition.
- **Acquisition is one write once `lock-create-revision-token` is active.**
  The token is then not copied into the lock record, so there is no second
  write that a concurrent release could interleave with. Before it is active
  the record still carries the token (see "Rolling upgrade" below), written by
  a second compare-and-swap on the acquire's own revision, so it can never
  write a released lock back.
- **Release and extend both validate the token** against the lock key's create
  revision. A stale token is refused with `NotLockHolderError` — this is
  what stops a resumed zombie holder from releasing a lock somebody else now
  holds.
- **Reentrancy is per holder and counted.** With `Reentrant: true`, the same
  `HolderID` re-acquiring increments `AcquireCount` and **reuses the existing
  lease** rather than granting a new one. The lock must be released the same
  number of times to be fully released. `AcquireCount` is always 1 for
  non-reentrant locks.
- **`lock.Service.Lock` blocks, `TryLock` does not.** Blocking is implemented as a watch on
  the lock key plus a CAS retry on release, not a poll loop.
- **TTL expiry releases the lock.** A crashed holder's lock is released by lease
  expiry with no operator action.
- **Lock records are invisible to user KV enumeration** — they live under
  `__sys:lock/`, inside the internal-key guard.
- **`HolderID` is not authenticated.** It is a reentrancy discriminator supplied
  by the caller; when empty, a UUID is generated. Never treat it as identity.
- **A lock is confined by the ENGINES its servicer holds, never by its key.**
  `__sys:lock/{key}` carries no tenant segment; the resolved servicer's
  transaction, lease, watch and MVCC servicers are the `(tenant, database)`
  boundary. Concretely: `List` never enumerates another tenant's lock names,
  `Status` never returns a foreign holder record, and **`LockHeldError` never
  carries a foreign `HolderID`** — the last of those is a leak even when the
  caller is correctly refused, so it is asserted on the error's contents.
- **The fencing token is not a tenancy mechanism.** A token is the MVCC
  revision of an acquisition *in its own keyspace*, so two tenants acquiring
  the same name in two namespaces routinely hold the same `int64`. That is
  harmless precisely because the keyspace, not the number, keeps them apart.
- **A scope that cannot serve a lock is refused, not degraded.** The accessor
  returns `service.CapabilityUnsupportedError` when the resolved bundle lacks
  any of transaction, lease, watch or MVCC — a database on a dedicated non-raft
  store. Mode `standalone` serves locks like raft mode, on every storage
  backend.

## Error semantics

| Condition | Error | Fails closed? |
|---|---|---|
| `TryLock` on a lock held by another holder | `LockHeldError` | n/a — this is the answer |
| `Lock`'s context ends while it waits | `ctx.Err()` (`context.DeadlineExceeded` or `context.Canceled`) | **yes** — never returns a lock it did not acquire |
| Release or extend with a stale/incorrect token | `NotLockHolderError` | **yes** |
| Release of a lock that is not held | `LockNotFoundError` | yes |
| Reentrant release with `AcquireCount > 1` | decrements; lock stays held | n/a |
| Release whose decrement or delete loses its CAS to a concurrent write | re-reads, re-checks the token and count, retries | n/a |
| Release that loses all 16 of those rounds | `ErrUnlockContended`; lock stays held | **yes** |
| `ttl <= 0` | `ErrInvalidTTL` | yes |
| Empty key | `ErrEmptyKey` | yes |
| Missing `locks:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

Every failure path refuses rather than returning a lock, because a lock returned
optimistically is worse than no lock at all.

## Configuration

No lock-specific configuration keys. TTL and wait timeout are per request. The
underlying lease behaviour is described in
[../leases/engineering.md](../leases/engineering.md).

## Persistence

Lock state is a JSON record under `__sys:lock/<key>` (`lockValue` in
`pkg/lock/types.go`) holding the holder, reentrancy flag, acquire count,
acquisition time, lease ID and TTL. The fencing token is the key's create
revision, which the MVCC store keeps for every key, and this binary never reads
it from the record. A reentrant update rewrites the record and leaves the
create revision unchanged.

### Rolling upgrade: `lock-create-revision-token`

A binary from before the create-revision token reads a lock's token from the
record's `fencing_token` field. The field is retired behind the
`lock-create-revision-token` cluster feature
(`featuregate.LockCreateRevisionToken`), which `service.NewLockService` wires
into the locker through `lock.WithTokenFieldRetired`. The gate is the lock
shard's own: a tenant's or a database's lock is served through decorated
transaction services (quota, key namespace), and `NewLockService` follows
their `service.TransactionUnwrapper` chain to the `TransactionService` whose
host and shard it asks.

- **Until every replica of the group advertises it and it is activated**, an
  acquisition stores `fencing_token` with a compare-and-swap on the acquire's
  own revision (on the ambiguous-commit path too), and a re-entry or a
  reentrant decrement rewrites it. The value is always the key's create
  revision, so an old node and this one report the same token.
- **Once it is active** the field is omitted from every write. A host that
  gates nothing (a standalone node, or a locker built without the option)
  omits it at once.

The store-back is retried before the acquire returns: a write that fails with a
not-leader refusal or an unknown outcome (`lock.WithRetryable`, wired by the
service layer) is sent again, the same compare-and-swap, 50 ms later, at most
four attempts in all and never past the caller's context. A compare that lost
is not retried, since the record is no longer the one this acquire wrote; that
is also how a retry of a write that did commit ends, writing nothing. Any
other failure is final at once.

The store-back is still a second write: between the acquisition and the
store-back, or for good if every attempt fails (the lock stays held and the
failure is logged), an old node reads the record without the field: its Status and List
report token 0, and its Extend and Unlock fall back to the record's mod
revision, which equals the create revision until the record is next written.
Activating the feature ends the window.
`pkg/lock/mixed_version_test.go` runs an old-node double against this binary in
both directions.

## Test strategy

- **Unit**: `pkg/lock` — acquire/try/release/extend/status/list, reentrant
  counting, stale-token refusal, and token monotonicity across successive
  acquisitions.
- **Cross-tenant isolation**: `pkg/server/lock_tenancy_test.go` drives the REAL
  data plane — an MVCC engine over in-memory storage with a real lessor and
  watch hub — through the registry accessor, under all three isolation types
  (`prefix`, `volume`, `raft`), and asserts each of denial, disclosure (List,
  Status and `LockHeldError.HolderID`) and fencing-token semantics. It also
  asserts the inverse — two holders INSIDE one tenant still contend — because
  isolation bought by making the lock stop locking would pass every other
  assertion.
- **Transport resolution**: `lock_scope_test.go` in `pkg/api/{rest,grpc,quic,mcp}`
  asserts that EVERY lock verb resolves through the registry and does not reach
  the singleton, that the registry-less composition still serves the singleton,
  that a refused resolution propagates rather than falling back, and that a
  server with neither fails closed. Every verb is covered because an unresolved
  verb is an unconfined verb.
- **No `time.Sleep`.** Blocking acquisition and TTL expiry are driven through
  the watch and clock seams with channels and waitgroups, per the repo rule.
- **Error tests** for each function, minimum happy path plus error path.
- **Integration**: `TestLock_CLI_PerProtocol` (`lock_cli_test.go`, below)
  drives every verb on every network transport against a self-spawned node.
  The Docker suite `test/integration/api/cli_lock_test.go`
  (`make integration-test-standalone` / `make integration-test-cluster`) adds
  lifecycle, contention, fencing-token, reentrant and list tests per protocol.
  No test covers contention across nodes or a lock surviving a leader change.

## Observability

- Each lock verb's authorization decision is audited as an `auth.authorize`
  event carrying its `locks:*` permission ([../audit/](../audit/)). There is
  no lock-specific audit event.
- Lock acquisition and release appear as key events under `__sys:lock/` and are
  therefore not visible on a user watch — use `qrdb lock list` for visibility.
  `qrdb lock list` shows the caller's own `(tenant, database)` scope only.

## Integration testing

`make integration-test-distributed-locks` runs this feature's self-spawned integration tests: `lock_cli_test.go`. The per-transport accounting, including every gap, is the `distributed-locks` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
