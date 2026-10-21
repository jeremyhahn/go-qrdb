# Storage Engines — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the object-storage
> LSM target design (see "Target design" at the end).**
> · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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
	DeleteRange(start, end []byte) error
	List(prefix []byte) ([][]byte, error)
	Exists(key []byte) (bool, error)
	Batch() Batch
	Snapshot() (Snapshot, error)
	Close() error
}
```

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
`pkg/storage/pebble`, `pkg/storage/objstore`.

`pkg/storage/objstore`'s typed errors, beside `UnsupportedProviderError`:

```go
type FormatMarkerMissingError struct{ Provider string }   // non-empty store, no marker
type FormatVersionMismatchError struct{ Found, Want string }
type InTreeJournalError struct{ Provider string }
type RecordCorruptError struct{ Name, Reason string }     // hashed record unparseable
type KeyMismatchError struct{ Name string }               // hashed record holds another key
```

`pkg/storage/file` — at-rest encryption is an option of `New`:

```go
func New(dir string, opts ...Option) (*Engine, error)
type Option func(*options)
type Encryptor interface {      // satisfied by *atrest.Encryptor
	Enabled() bool
	Wrap(base vfs.FS) vfs.FS
}
func WithEncryptor(enc Encryptor) Option   // nil or disabled: plaintext

type FormatMarkerMissingError struct{ Dir string }
type FormatVersionMismatchError struct{ Found, Want string }
type AtRestMismatchError struct{ Dir, Found, Want string }   // "plaintext" | "encrypted"
```

`pkg/atrest` — the one at-rest seam, shared by Pebble and the file engine:

```go
func New(barrier *seal.StorageBarrier) *Encryptor   // nil barrier: disabled
func (e *Encryptor) Enabled() bool
func (e *Encryptor) Apply(o *pebble.Options)        // Pebble: Options.FS
func (e *Encryptor) FS() vfs.FS
func (e *Encryptor) Wrap(base vfs.FS) vfs.FS        // file engine: over its base FS
```

```go
// pkg/server/lifecycle
func NewLocalStorage(backendType database.BackendType, dataDir string, enc *atrest.Encryptor,
	objCfg qrdbconfig.ObjstoreConfig) (storage.Storage, *pebble.DB, error)
```

`NewLocalStorage` opens `pebble` and `file` through `enc`; it is the
constructor every standalone node store goes through — the daemon, the SDK
node and the embedded server. A raft replica's `file` storage takes the same
encryptor from `statemachine.StorageConfig.Encryptor`.

`pkg/database` — re-encrypting a `file` store under a new key, the file
engine's counterpart of `ReencryptPebbleStore`:

```go
func ReencryptFileStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor,
	dstDir string, dstEnc *atrest.Encryptor) (count int64, err error)
func VerifyReencryptedFileStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor,
	dstDir string, dstEnc *atrest.Encryptor) error

type NodeStoreMigration struct {
	// ...
	StoreDirs     []string // Pebble stores
	FileStoreDirs []string // file stores
}
```

Both open the source under `srcEnc` and the destination under `dstEnc`
through `file.New`, so every record is decrypted under the old key and
re-sealed under the new one. A copy failure, cancellation included, is
`ReencryptCopyError`; a verify that finds a differing key, value or record
count is `ReencryptVerifyMismatchError`. The node migration journals every
directory of both lists and swaps them together.

The volume pool surface is in
[../volume-pools/engineering.md](../volume-pools/engineering.md).

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
  `go-qrdb/pkg/storage` for its storage operations. Changing the signature
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
  element of every scan to police a caller lifetime bug. On the PebbleDB
  backend such an iterator does **not** panic — it holds a reference
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
- **No engine isolates tenants.** Isolation is a database and barrier per
  tenant, above this layer; a key prefix is not a boundary.
- **Engines store what they are given.** Encryption is the barrier's layer;
  revisions are the state machine's.
- **One at-rest mechanism for every on-disk backend.** Under the seal,
  `pebble` and `file` write every file through `atrest.Encryptor`'s
  encrypting file system and nothing else; neither carries a cipher of its
  own. A new backend that writes files takes the encryptor's file system; it
  does not add a second mechanism.
- **A `file` store never mixes postures.** Its plaintext marker records
  `encrypted` or `plaintext`, and `New` refuses the other
  (`AtRestMismatchError`). The marker is the only plaintext file in an
  encrypted store.
- **A sealed barrier fails every record read and write.** The encrypting file
  system cannot unwrap a file's key, so the engine returns `seal.ErrSealed`.
  `Exists` answers from the hashed record file name without reading the
  record, so it does not consult the barrier.

## Configuration

```yaml
storage:
  backend: pebble        # pebble | memory | file | objstore
  cache_size: 0          # pebble block cache bytes; 0 = pebble default
  wal_dir: ""            # separate pebble WAL directory; empty = data dir
  max_open_files: 0      # 0 = pebble default
  objstore:              # used when a backend is objstore
    provider: local      # local | memory | s3 | gcs | azure
    settings: {}         # passed verbatim to the go-objstore factory
```

`pkg/config.StorageConfig` also carries `volume_pools` and
`allow_degraded_volume_boot`; see
[../volume-pools/engineering.md](../volume-pools/engineering.md).

A composer can replace the configured backend of a **standalone** node's data
plane: `server.WithStorageFactory` / `node.WithStorageFactory` take a
`func(dataDir string, dataPlaneFS vfs.FS) (storage.Storage, error)`, and the
node closes what it returns. A factory that writes files must route its I/O
through the `vfs.FS` it is given so the data stays under the barrier. A
clustered replica's storage is chosen by its state machine, which node options
do not reach, and the seal store is node-local by design and not behind the
factory. See [release/extension-seams.md](../release/extension-seams.md).

Raft-mode state machines select their backend separately with
`raft.sm_storage` (`pebble` default, `file`, `memory`) — see
[../run-modes/engineering.md](../run-modes/engineering.md). Objstore is refused
as a hot apply path. Every raft state machine commits a multi-key transaction
or batch through `Backend.ApplyAtomic`, which each of the five backends
implements all or nothing: one `storage.Batch` for `disk`, one acquisition of
every touched shard's lock for `fast` and the in-memory ones
([../key-value/engineering.md](../key-value/engineering.md)).

Cloud object-storage backends come from go-objstore and are build-tag gated:
`WITH_AWS_S3`, `WITH_GCP_STORAGE`, `WITH_AZURE_BLOB`,
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
| Objstore opened on a non-empty store without its format marker, or with another version | refused at open (`objstore.FormatMarkerMissingError`, `objstore.FormatVersionMismatchError`) | **yes** |
| Objstore `local` store holding a commit journal inside its data tree (the journal belongs in the sibling `<path>.journal` tree, derived from the physical root: absolute, cleaned and with every symlink resolved, so a symlink and its target open one data tree and one journal) | refused at open (`objstore.InTreeJournalError`); not recovered, restore from a backup | **yes** |
| Objstore hashed record truncated, malformed, or holding a key other than its name's | refused on read and listing (`objstore.RecordCorruptError`, `objstore.KeyMismatchError`) | **yes** |
| Non-pebble `sm_storage` with a state machine that carries its own storage (anything but `disk`, `fast`) | rejected (`SMStorageNotApplicableError`) | **yes** |
| A multi-key transaction or batch whose commit fails | nothing applied, on disk or to readers (`Backend.ApplyAtomic`) | **yes** |
| Seal barrier enabled with `sm_type: disk`/`fast` over `pebble` `sm_storage` | opened on the barrier's encfs (`atrest.Encryptor.FS`): every sstable, WAL and manifest encrypted | — |
| Seal barrier enabled with `sm_type: disk`/`fast` over `file` `sm_storage` | opened on the barrier's encfs (`file.WithEncryptor`): every record file and the commit journal encrypted | — |
| Seal barrier enabled with standalone `storage.backend: pebble` or `file` | opened on the barrier's encfs: every file but the `file` engine's format marker encrypted | — |
| Seal barrier enabled with standalone `storage.backend: objstore` | started; one WARN and an `unencryptedAtRest` capability entry name the gap | no |
| `file` store opened in the other at-rest posture (written plaintext, opened under the seal, or the reverse) | refused at open (`file.AtRestMismatchError`), nothing read | **yes** |
| Read or write on an encrypted store while the barrier is sealed | `seal.ErrSealed` from the encrypting file system | **yes** |
| Node encryption migration whose re-encrypted `file` store differs from its source | `ReencryptVerifyMismatchError`, wrapped in `MigrateStoreDirError` naming the directory; the migration rolls back before any swap and the store stays under the old key | **yes** |
| Opcode an engine has no handler for | refused in the result payload; the apply loop keeps advancing | **yes** |
| Engine write fault under a committed entry | returned from `Update`, halting Apply on that replica | **yes** |

## Persistence

- **File engine**: a hashed, flat, content-addressed layout — each key's
  record is named by the hex SHA-256 digest of the key, sharded two levels
  deep (two hex characters per level), holding `[keyLen:4 BE][key][value]` —
  with atomic temp-file + rename + fsync writes, every `MkdirAll`-created
  directory's parent fsynced too, and an all-or-nothing commit journal
  replayed at open (`pkg/storage/file/journal.go`), plus the
  `storage.Syncer` seam. That durability is why it is permitted as a hot
  apply path. The layout is an on-disk format with no migration from any
  other; see
  [architecture.md](architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking).
  Writes are serialized across processes by an advisory OS lock on
  `.qrdb-file-lock` (`pkg/util/filelock`, a thin wrapper over `flock(2)` /
  `LockFileEx`) plus an in-process mutex; `Exclusive` exposes it for a guarded
  read-check-write; see
  [architecture.md](architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking).
  `Delete` removes only the record, never its (fixed, bounded) shard
  directories, for the reason that doc gives: concurrent per-key workers
  (the `fast` state machine's write-through) make pruning them on a
  point-in-time emptiness check a race. `New` refuses a non-empty data
  directory lacking a matching format-version marker
  (`FormatMarkerMissingError`, `FormatVersionMismatchError`), or one whose
  marker records the other at-rest posture (`AtRestMismatchError`). With an
  enabled encryptor every record and the journal are written through the
  barrier's encrypting file system; the marker (`3/encrypted`) is the one
  plaintext file. The layout has
  no key-length or path-depth dependency at all; the global key-length cap (`protocol.MaxClientKeyLength`, 65,535
  bytes) is a client-input policy enforced before and at Apply, independent
  of any particular backend's constraints.
- **File engine commit cost**: a batch commit publishes its journal durably,
  then writes its records, then clears the journal, all under the write lock.
  The records are reduced to the last operation per key first, so a batch
  that writes one key twice leaves the in-order final state, and the distinct
  keys are then written concurrently, at most 32 at a time
  (`applyConcurrency` in `pkg/storage/file/batch.go`), so their fsyncs
  overlap rather than queue. A record write that fails fails the commit and
  leaves the journal in place; the next open replays it. `storage.ApplyFunc`
  states the contract that permits this: distinct keys may be applied in any
  order or concurrently.
- **Standalone group commit** (`pkg/backend/local.go`): proposals that
  arrive while one `Update` is in progress queue behind it, and the proposer
  that next holds the apply token applies the whole queue in one `Update`, so
  concurrent writers share one commit and its fsyncs instead of paying for
  one each. Every proposal is acknowledged only after the `Update` that
  applied it returns, so no write is acknowledged before the fsync covering
  it. Indices stay consecutive and applies stay sequential. An `Update` error
  fails every proposal in its group, and a panic in `Update` completes the
  group with `ApplyAbortedError` and releases the token. A proposer whose
  context is cancelled still has its proposal applied with its group: once
  queued, a write is not withdrawn. There is no setting; a lone proposal is a
  group of one.
- **Objstore engine**: one object per key, named by the key's encoding,
  never by the key itself. A key whose segmented lowercase hex name fits the
  provider's 1,024-byte limit is stored as `k/<hex…>_`, with its value as the
  body. A longer key is stored as `h/<sha256-hex>`, with a
  `uvarint(len(key)) || key || value` record that every read verifies. Every
  key up to the global key-length cap (`protocol.MaxClientKeyLength`, 65,535
  bytes), whatever its bytes, round-trips on every provider. `New` refuses a
  non-empty store lacking the `qrdb-objstore-format` marker for version `2`
  (`FormatMarkerMissingError`, `FormatVersionMismatchError`), so a store
  written in any other layout is refused, not misread.
  There is no migration. See
  [architecture.md](architecture.md#the-objstore-engine-encoded-object-names-breaking).
  A batch commits through a single journal object (publish, apply, clear;
  `pkg/storage/objstore/journal.go`), replayed at open. A commit that fails
  after it may have published its journal leaves the engine with a pending
  commit, and the next `Set`, `Delete`, `CompareAndSwap` or batch commit
  completes it before being accepted: a swap is never decided against a
  half-applied batch, and no write lands where a later replay would apply the
  batch over it. A batch commit excludes every other writer from its journal
  publish to its outcome, and a `Set` or `Delete` checks for a pending commit
  and writes under one lock, so a commit cannot fail between that check and
  that write; `Set` and `Delete` still run concurrently with each other. While
  the provider still cannot apply the journal, that next write fails and the
  journal stays.
- **Pebble engine**: an LSM store; the production default.
- **Memory engine**: nothing survives the process.

## Test strategy

- **Unit**, per engine, against the same interface conformance suite — an engine
  that passes it is usable everywhere the interface is accepted.
- **File engine journal**: crash-mid-write replay at open.
- **File engine at rest** (`pkg/storage/file/encrypted_test.go`): no key or
  value readable anywhere in an encrypted store's files, a batch-committed
  record included; a reopen and unseal reading every record back; reads and writes
  failing with `seal.ErrSealed` while sealed, including on a store reopened
  sealed; the posture marker refusing the other posture in both directions.
  End to end through every node composition: the embedded server over
  `pebble` and `file` (`sdk/go/standalone/server_encfs_test.go`: no
  plaintext on disk, sealed boot refused, a reseal failing record reads with
  `seal.ErrSealed` and an unseal restoring them), `lifecycle`
  (`TestNewLocalStorage_OnDiskBackendsEncryptThroughTheEncryptor`), raft
  replica storage (`TestStorageGenericSMsEncryptStorageUnderTheBarrier`) and a
  sealed three-node group over `file` replica storage
  (`TestRaftSMOverFileStorage_SealedLeavesNoPlaintextOnDisk`).
- **File store re-encryption** (`pkg/database/reencrypt_file_test.go`): every
  record read back under the new key and none under the old; verify refusing
  a differing key, value or record count; cancellation as
  `ReencryptCopyError`. The node migration moving a `file` store
  (`TestNodeStoreMigration_MovesFileStores`,
  `TestEncryptionMigration_SystemMovesFileStores`), the SDK provider
  classifying store directories by backend
  (`sdk/go/node/migrate_store_dirs_test.go`), and an SDK node on `file`
  migrating from strategy `none` to a passphrase and reading every record
  back after a restart
  (`TestNode_EnableEncryption_SystemNoneToPassphrase_FileBackend`). In raft
  mode, `qrdbd` naming each state machine's stores by `sm_type` and
  `sm_storage` (`cmd/qrdbd/system_migrate_stores_test.go`), and a real daemon
  with `--sm disk --sm-storage file` enabling encryption and serving its data
  back under the passphrase
  (`TestEnableEncryption_SystemScope_DiskOverFileStorage` in
  `test/integration/enable_encryption_test.go`).
- **File engine on tmpfs**: the file engine's unit tests, the storage parity
  tests and every file-engine cell of the `sdk/go/conformance` matrix make
  their directories with `pkg/testutil/memdir`, which creates them under
  `/dev/shm` on Linux and fails the test if that is not tmpfs, so an fsync
  returns at once and a test's deadline measures the code, not the runner's
  disk. `TestFileCells_RunOnTmpfs` asserts every file-engine cell's
  directories are on tmpfs. Integration tests and the commit benchmarks
  (`make bench-storage-file`) keep a real disk, since the disk is their
  subject.
- **Group commit and parallel record writes** (`pkg/backend/group_commit_test.go`,
  `pkg/storage/file/parallel_apply_test.go`), driven by a slow fake sync:
  32 concurrent proposals finish in about one sync time, no proposal is
  acknowledged before the sync covering it, an `Update` error or panic
  completes the whole group, a commit's record syncs overlap, a failed
  record sync is recovered from the journal at reopen, and the last
  operation on a key wins.
- **File engine cross-process lock** (`pkg/storage/file/exclusive_test.go`,
  `pkg/util/filelock`): `Exclusive` blocking another engine's `Set` and batch
  commit until it returns, and `New` waiting for a writer in flight; the lock
  and the in-process mutex released on a callback error, a callback panic, a
  failed lock and a failed unlock; `Close` releasing the lock and idempotent.
  The proof that the guarantee holds across *processes* is
  `pkg/database/backend_standalone_multiprocess_test.go`: the test binary
  re-executes itself as four children that open one `file` database in a temp
  directory and race forty `CompareAndSwap` increments each on one counter (the
  final value must equal the increments and no counter value may be won twice)
  and forty `SaveIfAbsent` rounds (exactly one winner per key). Disabling the
  lock makes both go red.
- **File engine hashed layout** (`pkg/storage/file/hashed_layout_test.go`):
  the format marker refusing an old-layout or mismatched-version directory
  and accepting a fresh or empty one; a key nested thousands of segments
  deep applying cleanly; `commitBufferedOps` refusing a batch's invalid op BEFORE
  writing the commit journal, so the batch leaves neither a journal nor a
  partial write and the engine reopens cleanly afterward; a seeded property
  test over random Put/Delete sequences with arbitrary `/` nesting; and a
  fuzz test (`FuzzEngine_SetGetDeleteRoundTrip`) over arbitrary key/value
  byte strings through Set/Get/Delete. The property is pinned one layer up,
  through `Update`, on every storage-generic state machine over both `file`
  and `pebble` `sm_storage` (`pkg/statemachine/prefix_property_test.go`) and
  on `pebble`/`mvcc` (`pkg/store/prefix_property_test.go`), at the disk
  state machine level for two keys where one is a prefix of the other
  (`pkg/statemachine/disk_prefix_collision_test.go`), and end to end through
  a real daemon and the CLI, including the deep-key case, in
  `test/integration/sm_storage_file_test.go`.
- **Objstore object names** (`pkg/storage/objstore/keys_test.go`,
  `faults_test.go`): binary and edge-case keys (NUL, `0xff`, invalid UTF-8,
  `..`, `//`, absolute paths, control characters, the marker's own name)
  round-tripping through Set/Get/Exists/List/iteration/Delete on the
  `memory` and `local` providers; key lengths across both name forms up to
  the 65,535-byte cap, with every direct name checked against go-objstore's
  own `ValidateKey` and the 255-byte component limit; non-canonical names
  skipped; corrupt and misplaced hashed records refused; the format marker
  refusing an unmarked or mismatched store; and provider read, list,
  exists and delete faults surfacing as themselves.
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
the same status.

## Target design: `objstore` as Pebble remote storage

> See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout)
> for the shape and the decisions this contract assumes. **Nothing below is
> implemented.**

### Exported surface (target)

`pkg/storage/objstore` stops exporting `Engine`/`Batch`/`Iterator`/`Snapshot`.
It exports a `remote.Storage` adapter and nothing else that is KV-shaped:

```go
// NewRemote constructs a Pebble remote.Storage backed by a go-objstore
// provider. enc is the barrier-driven encrypting wrapper; a nil or
// disabled encryptor is a no-op, matching pkg/atrest.Encryptor's convention.
func NewRemote(provider string, settings map[string]string, enc *atrest.Encryptor) (remote.Storage, error)

// Locator builds the remote.Locator for a (provider, prefix) pair qrdb
// controls; it never embeds a qrdb key.
func Locator(provider, prefix string) remote.Locator
```

`pkg/storage/pebble` gains one construction option:

```go
// WithRemote configures the Engine to keep its SSTables on remote storage
// reached through store, under locator, using strategy to decide which
// levels are remote (CreateOnSharedAll for a local binding — every level
// remote; CreateOnSharedLower is available but not used by phase 1).
// The local dir passed to pebble.New still roots the WAL, MANIFEST and
// block cache — WithRemote narrows what the local dir holds, it does
// not replace it.
func WithRemote(store remote.Storage, locator remote.Locator, strategy remote.CreateOnSharedStrategy) Option
```

A `*pebble.DB` opened with `WithRemote` is constructed with a `CreatorID`:
one per owning instance, generated once and persisted as a small local
marker file beside the WAL/MANIFEST directory — never derived from a qrdb
key, never shared between instances.

### Invariants (target, additive to the ones above)

- **An `objstore`-backed engine is a `pebble.Engine`.** It is not a second KV
  implementation: `storage.Storage`, `Batch`, `Iterator`, `Snapshot`, the
  closed-store contract (`ErrStorageClosed`, the PebbleDB reader gate) all
  apply unchanged. Anything this document says about the `pebble` engine
  elsewhere in this file is true of `objstore` too, because it is the same
  code with one `Option` added.
- **Segment object names never derive from a qrdb key.** They are Pebble's own
  `<hash>-<creator-id>-<file-num>.sst` / `.blob` naming
  (`objstorageprovider/remote_obj_name.go`). A review that finds a qrdb key,
  tenant id, or database name embedded in an object name sent to go-objstore
  on this path is a defect.
- **Encryption happens in the `remote.Storage` adapter, not in `vfs.FS`.**
  `pkg/atrest.Encryptor.Apply` continues to cover the local WAL/MANIFEST/local
  SSTables; it does **not** reach objects written through
  `WithRemote`. The adapter encrypts the whole object payload with the same
  `seal.StorageBarrier` AEAD before `CreateObject` and decrypts after
  `ReadObject`. A barrier-sealed database with `WithRemote` configured and no
  working encryptor fails closed at open rather than writing plaintext
  objects.
- **One prefix, one `CreatorID`, one owning instance — never shared.** A
  replacement instance (replacement Raft replica, or a reassigned local-binding
  owner) never reopens a previous instance's prefix; it gets a fresh one. This
  is a correctness invariant, not a cost optimization: two
  live Pebble instances writing under one `CreatorID`/prefix would corrupt each
  other's manifest.
- **`glacier` and `azurearchive` are refused at construction.** `NewRemote`
  returns a typed error (`ArchiveTierUnsupportedError`, new) for a provider
  go-objstore registers only as an `Archiver`, before any attempt to open a
  Pebble store against it. These stay backup/archive destinations only (see
  [../backup-restore/architecture.md](../backup-restore/architecture.md)).
- **No ranged read.** Until go-objstore exposes one, `ReadObject` fetches the
  whole object once per open and serves `ReadAt` from a local cache keyed by
  object name. This is a performance limitation of phase 1, stated in the
  adapter's doc comment.

### Configuration (target)

```yaml
storage:
  backend: objstore
  objstore:
    provider: s3          # local | memory | s3 | gcs | azure
    settings:
      bucket: qrdb-data
      prefix: "node-a/db-1"   # qrdb-controlled; never a key
    local_dir: /var/lib/qrdb/objstore   # WAL, MANIFEST, block cache
    create_on_shared: all    # all | lower | none (default: all for a local binding)
```

`local_dir` is resolved from the same local volume pool the `pebble` engine
uses (see [../volume-pools/engineering.md](../volume-pools/engineering.md)) —
this design adds no second local-path mechanism. `provider`/`settings` reuse
`storage.objstore` exactly as the node-level config documents them (see "Configuration" above and
[../databases/architecture.md](../databases/architecture.md#what-stays-node-level)).

### Error semantics (target, additive)

| Condition | Behavior | Fails closed? |
|---|---|---|
| `provider` is `glacier` or `azurearchive` | `ArchiveTierUnsupportedError` at construction | **yes** |
| Barrier sealed, no working encryptor, `WithRemote` configured | refused at open, nothing written | **yes** |
| A second instance attempts to open an already-owned `CreatorID`/prefix | refused — Pebble's own manifest/lock detects the conflict | **yes** |
| go-objstore `ReadObject` for an object this instance did not write (prefix violated) | not expected to occur (one prefix per instance); if it does, a corruption-class error, not silently served | **yes** |
| `raft.sm_storage: objstore` before the phase-3 gate accepts it | refused | **yes** |

### Test strategy (target, additive)

- The existing per-engine conformance suite runs over `pebble+remote`
  (local-provider go-objstore) exactly as it runs over `memory`/`file`/
  `pebble`.
- **Encryption round trip**: barrier on, write, restart, read — object bytes
  on the local-provider backing directory are not the plaintext; barrier off,
  same round trip, object bytes equal the plaintext, as for `pebble`'s local
  files.
- **Provider refusal**: `NewRemote("glacier", ...)` and
  `NewRemote("azurearchive", ...)` return `ArchiveTierUnsupportedError` before
  any network call.
- **Crash safety**: kill `-9` mid-compaction and mid-flush; reopen must
  succeed with no corruption on `pebble+remote`.
- **Name hygiene (build gate)**: a test asserting no object name
  `pkg/storage/objstore` sends to go-objstore's `Put`/`CreateObject`
  round-trips through anything shaped like a qrdb key — a regression guard for
  the invariant above.
