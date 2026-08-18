# Storage Engines and Volume Pools — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`pkg/storage`:

```go
type Engine interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
	Delete(key string) error
	List(prefix string) ([]string, error)
	Exists(key string) (bool, error)
	Close() error
}
```

Implementations: `pkg/storage/file`, `pkg/storage/memory`,
`pkg/storage/pebble`, `pkg/storage/namespace`.

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

- **`Engine` is a six-method interface and is a cross-repo contract.**
  go-xkms imports `go-qrdb/pkg/storage` for all its storage operations and
  wraps the go-qrdb SDK client into this interface for distributed mode.
  Changing the signature breaks go-xkms.
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
| Backing directory unwritable | error at open, not at first write | **yes** |
| Objstore configured as a hot apply path | validation error | **yes** |
| Non-pebble `sm_storage` with a pebble-native state machine | rejected | **yes** |
| `VolumePools()` on an unsupporting transport | returns **nil** — callers must nil-check | — |
| Pool `Pick` with an unknown pool name | not-found | yes |

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
backend-specific messages. Volume pool composition is visible through
`VolumePool.BackendCount` and `LocalPaths`.
