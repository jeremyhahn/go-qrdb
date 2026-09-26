# Storage Engines and Volume Pools — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`pkg/storage`:

```go
type Reader interface {
	Get(key []byte) ([]byte, error)
	NewIterator(opts *IteratorOptions) Iterator
}

type Storage interface {
	Reader
	Set(key, value []byte) error
	Delete(key []byte) error
	List(prefix []byte) ([][]byte, error)
	Exists(key []byte) (bool, error)
	Batch() Batch
	Snapshot() (Snapshot, error)
	Close() error
}
```

> **Correction.** This block previously named a six-method `Engine` interface
> with `string` keys and a `Put` method. No such interface exists; the contract
> is `storage.Storage` above, keyed by `[]byte`, with `Set` rather than `Put`,
> plus `Batch` and `Snapshot`. `file.Engine` is the file backend's concrete
> type, which is where the name came from. The invariant below is restated
> against the real name.

The closed-store vocabulary, shared by every backend:

```go
var ErrStorageClosed = &StorageError{Code: "storage_closed", ...}  // THE sentinel
type ClosedError struct{}                                          // unwraps to it
func IsClosed(err error) bool
func NewClosedBatch() Batch        // the Batch a closed engine hands out
func NewClosedIterator() Iterator  // the Iterator a closed engine hands out
```

Re-exported as `sdk.ErrStorageClosed`, `sdk.StorageClosedError` and
`sdk.IsStorageClosed`.

Implementations: `pkg/storage/file`, `pkg/storage/memory`,
`pkg/storage/pebble`, `pkg/storage/objstore`, `pkg/storage/namespace`.

`pkg/storage/pool`:

```go
func NewPebbleStateStore(storage *pebblestore.PebbleStorage) (*PebbleStateStore, error)
func NewReplicatedStateStore() *ReplicatedStateStore
func NewBoundReplicatedStateStore(kv ReplicatedKV) (*ReplicatedStateStore, error)
```

The pool manager is constructed wrapped: `objpool.LocalOnly(innerMgr)`
(`pkg/storage/pool/volumepool.go`).

`sdk/go/transport` — the volume pool SDK surface:

```go
type VolumePoolClient interface {
	List(ctx context.Context) ([]VolumePool, error)
	Pick(ctx context.Context, poolName string, hint VolumePoolHint) (*VolumePoolBackend, error)
	RegisterPool(ctx context.Context, req RegisterVolumePoolRequest) error
}

type VolumePool struct {
	Name         string   `json:"name"`
	Strategy     string   `json:"strategy"`
	BackendCount int      `json:"backendCount"`
	LocalPaths   []string `json:"localPaths,omitempty"`
}
type VolumePoolHint struct {
	TenantID string `json:"tenantID,omitempty"`
	SizeHint int64  `json:"sizeHint,omitempty"`
	Region   string `json:"region,omitempty"`
}
type VolumePoolBackend struct {
	PoolName  string `json:"poolName"`
	LocalPath string `json:"localPath,omitempty"`
}
type RegisterVolumePoolRequest struct {
	Name     string   `json:"name"`
	Paths    []string `json:"paths"`
	Strategy string   `json:"strategy,omitempty"`
}
```

Reached from the client with `Client.VolumePools()`, which returns nil when the
transport does not implement `VolumePoolProvider` — **nil-check it**.

## Invariants

- **An engine returns an error from `Update` only when the state machine cannot
  continue.** Raft treats a non-nil `Update` error as a fatal apply failure, so
  returning one for an outcome that is a deterministic consequence of valid
  committed input — an opcode this engine does not implement, a subsystem it was
  not constructed with — stops `lastApplied` advancing and costs the shard every
  subsequent linearizable read, on every engine sharing it. Such an outcome is
  ENCODED into `sm.Result.Data` instead. The error itself declares which it is,
  by implementing `protocol.CapabilityError`; anything that declares nothing is
  a fault and halts. `pkg/engine`, `pkg/store` and `pkg/statemachine` share one
  classifier, `statemachine.ResolveApplyError`, and `test/buildgates` fails the
  build if an apply seam does not route through it. The full contract is in
  [key-value/engineering.md](../key-value/engineering.md#refusal-and-fault-at-the-apply-seam).
- **`Storage` is a cross-repo contract.** go-xkms imports
  `go-qrdb/pkg/storage` for all its storage operations and wraps the go-qrdb
  SDK client into this interface for distributed mode. Changing the signature
  breaks go-xkms.
- **An operation issued THROUGH a closed engine returns an error; it never
  panics.** Every backend answers every post-`Close` operation with an error
  that `errors.Is(err, ErrStorageClosed)` matches, including the two methods
  whose signatures cannot return one — `Batch` and `NewIterator` hand back the
  shared inert handles from `NewClosedBatch` / `NewClosedIterator`, which
  report the closure from each of their own methods. A backend that improvises
  its own answer here is a defect: this is one implementation shared by all of
  them so it cannot drift into four dialects.
- **A handle the caller created BEFORE `Close` and kept afterwards is a
  different case, and the boundary is deliberate.** The gate makes handle
  *creation* atomic with respect to `Close`; it does not wrap each `Next` on an
  iterator already in flight, which would put a gate acquisition on every
  element of every scan to police a caller lifetime bug. Measured on the
  PebbleDB backend: such an iterator does **not** panic — it holds a reference
  to the LSM version it was created against and keeps serving that pinned,
  internally consistent view. It does not report the closure. The leak is not
  silent either way: `Close` returns an error naming the leaked handle, which
  is the caller's signal to close its iterators first.
- **`Close` on the PebbleDB backend waits for in-flight operations.** PebbleDB
  answers a use-after-close with a panic rather than an error, so a bare
  closed-flag check is advisory: the check and the DB call are two steps and
  `Close` fits between them. The backend holds a reader gate across both, and
  `Close` takes it exclusively. The other backends release no resource whose
  use-after-close is fatal and do not carry the gate — after `Close` returns
  all four are identical, and only PebbleDB needs the ordering *during* it.
- **A closed engine's iterator is not an empty range.** `Error()` reports
  `ErrStorageClosed`; a scan that silently returned "no keys" from a closed
  store would be a wrong answer rather than a loud one.
- **The interface satisfies go-quicraft's `seal.StorageBackend` by duck
  typing** — no embedding, no import. Both sides must stay compatible by
  agreement, and there is no compiler check across the repo boundary.
- **The namespace engine is the only tenant-isolation mechanism at this layer.**
  It wraps any engine and prefixes every key; `List("")` filters to the tenant's
  prefix. A raw engine is **not** tenant-safe.
- **Volume pools are local-only.** `objpool.LocalOnly` wraps the manager because
  the consumer is PebbleDB, which needs a real filesystem path. This is a
  documented invariant, not debt — do not remove the wrapper.
- **`VolumePoolBackend.LocalPath` is a filesystem path on the serving node.**
  It is not portable across nodes.
- **Engines store what they are given.** Encryption is the barrier's layer;
  revisions are the state machine's.

## Configuration

```yaml
storage:
  engine: pebble    # file | memory | pebble | qrdb
  file:
    dir: /var/lib/qrdb/data
  pebble:
    dir: /var/lib/qrdb/pebble
  qrdb:
    address: "db.example.com:63003"
    transport: grpc
```

Raft-mode state machines select their backend separately with
`raft.sm_storage` (`pebble` default, `file`, `memory`) — see
[../run-modes/engineering.md](../run-modes/engineering.md). Objstore is refused
as a hot apply path.

Cloud object-storage backends come from go-objstore and are build-tag gated:
`WITH_AWS_S3`, `WITH_MINIO`, `WITH_GCP_STORAGE`, `WITH_AZURE_BLOB`,
`WITH_GLACIER`, `WITH_AZURE_ARCHIVE`.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Key absent on `Get` | not-found, distinct from an empty value | n/a |
| Any operation after `Close` | error matching `ErrStorageClosed`, on every backend | **yes** |
| `Batch()` / `NewIterator()` after `Close` | inert handle reporting `ErrStorageClosed` from every method | **yes** |
| `Close` racing an in-flight PebbleDB operation | the operation completes, or returns `ErrStorageClosed`; never a panic | **yes** |
| Closed store reaching gRPC / REST | `Unavailable` / `503`, not `Internal` / `500`, on **every verb** — read, write, lease-write and batch alike | yes |
| Iterator created before `Close` and advanced after it | serves its pinned version; never panics. `Close` reported the leak | — |
| Backing directory unwritable | error at open, not at first write | **yes** |
| Objstore configured as a hot apply path | validation error | **yes** |
| Non-pebble `sm_storage` with a pebble-native state machine | rejected | **yes** |
| `VolumePools()` on an unsupporting transport | returns **nil** — callers must nil-check | — |
| Pool `Pick` with an unknown pool name | not-found | yes |
| Opcode an engine has no handler for | refused in the result payload; the apply loop keeps advancing | **yes** |
| Engine write fault under a committed entry | returned from `Update`, halting Apply on that replica | **yes** |

The nil return from `Client.VolumePools()` is the one sharp edge here: it is not
an error value, so an unchecked call panics rather than failing cleanly.

## Persistence

- **File engine**: key-to-path filesystem mapping, with atomic
  temp-file + rename + fsync writes and an all-or-nothing commit journal
  replayed at open (`pkg/storage/file/journal.go`), plus the `storage.Syncer`
  seam. That durability is why it is permitted as a hot apply path.
- **Pebble engine**: an LSM store; the production default.
- **Memory engine**: nothing survives the process.
- **Namespace engine**: no storage of its own; it rewrites keys.

## Test strategy

- **Unit**, per engine, against the same interface conformance suite — an engine
  that passes it is usable everywhere the interface is accepted.
- **Namespace wrapper**: prefixing on write, prefix filtering on `List`, and
  that a second tenant's keys are invisible.
- **File engine journal**: crash-mid-write replay at open.
- **Volume pool**: `LocalOnly` refusing a non-local backend, and `Pick`
  returning a usable local path.
- **Error tests** per function, happy path plus error path.

## Observability

Storage errors surface as typed errors at the service boundary rather than as
backend-specific messages. A closed store is reported to an operator as a
`503`/`Unavailable` — a node going away, not a fault — and is matched in code
with `sdk.IsStorageClosed`, never by comparing the message.

**The classification is per-condition, not per-verb.** It lives in
`pkg/api/errclass`, the one table every transport renders, among the rows that
win however deeply the condition is wrapped, so the wrapper rows
(`ReadError`, `ProposeError`, `BatchError`) cannot claim it first. A
`GET` meeting a dying node and a `PUT` meeting one are the same fact and get
the same status. Volume pool composition is visible through
`VolumePool.BackendCount` and `LocalPaths`.
