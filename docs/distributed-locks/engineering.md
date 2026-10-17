# Distributed Locks — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11
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
	FencingToken int64  `json:"fencing_token"` // MVCC ModRevision at acquisition
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
// The fourteenth accessor on service.Registry, with the same arity as the
// thirteen beside it.
LockService(tenant, db string) (LockServicer, error)
```

Every transport resolves through it rather than holding a process-wide
servicer: `lockSvcFor` in `pkg/api/{rest,grpc,quic}` and `resolveLock` in
`pkg/api/mcp`. Each falls back to the singleton ONLY when no registry is
configured, which is the registry-less composition.

## Invariants

- **`FencingToken` is the MVCC `ModRevision` at acquisition.** Revisions are
  globally ordered and monotonically increasing, so **each successful
  acquisition of a given lock has a strictly higher token than the previous
  one.** This is the only ordering guarantee a downstream resource needs.
- **Release and extend both validate the token** against the lock's current
  `ModRevision`. A stale token is refused with `NotLockHolderError` — this is
  what stops a resumed zombie holder from releasing a lock somebody else now
  holds.
- **Reentrancy is per holder and counted.** With `Reentrant: true`, the same
  `HolderID` re-acquiring increments `AcquireCount` and **reuses the existing
  lease** rather than granting a new one. The lock must be released the same
  number of times to be fully released. `AcquireCount` is always 1 for
  non-reentrant locks.
- **`Lock` blocks, `TryLock` does not.** Blocking is implemented as a watch on
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
  returns `ServiceNotImplementedError{Service: "lock"}` when the resolved
  bundle lacks any of transaction, lease, watch or MVCC — a database on a
  dedicated non-raft store. Mode `standalone` serves locks like raft mode
 ; on its file or objstore backend the lease grant underneath is
  refused with `CapabilityUnsupportedError`.

## Error semantics

| Condition | Error | Fails closed? |
|---|---|---|
| `TryLock` on a lock held by another holder | not-acquired result (no lock returned) | n/a — this is the answer |
| `Lock` exceeds its wait timeout | timeout error | **yes** — never returns a lock it did not acquire |
| Release or extend with a stale/incorrect token | `NotLockHolderError` | **yes** |
| Release of a lock that is not held | not-found | yes |
| Reentrant release with `AcquireCount > 1` | decrements; lock stays held | n/a |
| `ttl <= 0` | invalid-argument | yes |
| Missing `locks:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

Every failure path refuses rather than returning a lock, because a lock returned
optimistically is worse than no lock at all.

## Configuration

No lock-specific configuration keys. TTL and wait timeout are per request. The
underlying lease behaviour is described in
[../leases/engineering.md](../leases/engineering.md).

## Persistence

Lock state is a record under `__sys:lock/<key>` holding the holder, lease ID,
reentrancy flag and acquire count. The fencing token is not stored separately —
it *is* the record's `ModRevision`, which is why it cannot drift from the
store's ordering.

## Test strategy

- **Unit**: `pkg/lock` — acquire/try/release/extend/status/list, reentrant
  counting, stale-token refusal, and token monotonicity across successive
  acquisitions.
- **Cross-tenant isolation**: `pkg/server/lock_tenancy_test.go` drives the REAL
  data plane — an MVCC engine over in-memory storage with a real lessor and
  watch hub — through the registry accessor, under all three isolation types
  (`prefix`, `volume`, `raft`), and asserts each of denial, disclosure (List,
  Status and `LockHeldError.HolderID`) and fencing-token semantics. `volume`
  and `raft` were sound before the accessor existed; they are in the matrix so
  the fix is shown not to have narrowed to the mode that was broken. It also
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
- **Integration**: lock parity across transports in the standalone stage.
  Contention across nodes and lock survival through a leader change belong to
  the cluster stage (`make integration-test-cluster`). CI runs that stage's
  tests in the `integration-features` job (`.github/workflows/ci.yml`), one leg per feature (`make integration-test-<feature>`), required by
  `ci-gate`.

## Known documentation drift

`docs/18-distributed-locking.md` documents the REST paths as `/v1/lock/*`. The
code registers them **unversioned** (`/lock/acquire`, `/lock/trylock`,
`/lock/release`, `/lock/extend`, `/lock/status`, `/lock/list`). The code is
correct; [api.md](api.md) records the real paths.

## Observability

- Acquire, release, extend and expiry are audited through [../audit/](../audit/).
- Lock acquisition and release appear as key events under `__sys:lock/` and are
  therefore not visible on a user watch — use `qrdb lock list` for visibility.
  `qrdb lock list` shows the caller's own `(tenant, database)` scope only.

## Integration testing

`make integration-test-distributed-locks` runs this feature's self-spawned integration tests: `lock_cli_test.go`. The per-transport accounting, including every gap, is the `distributed-locks` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
