# Storage Engines — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; PROPOSED — pending owner
> decision — for the object-storage LSM target design (see "Target design" at
> the end).** · Scope: go-qrdb · Last verified: 2026-10-27

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
	Wrap(base filesys.FS) filesys.FS
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
func (e *Encryptor) FS() filesys.FS
func (e *Encryptor) Wrap(base filesys.FS) filesys.FS   // over any store's base FS
```

`pkg/filesys` is the store-neutral file-system interface (standard library
only) that `pkg/seal/encfs` and `pkg/atrest` are written against, so an
embedder that never opens Pebble does not compile it. `pkg/storage/pebble`
owns the one adapter between it and Pebble's `vfs.FS`:

```go
func VFS(fsys filesys.FS) vfs.FS
func FromVFS(fs vfs.FS) filesys.FS                             // nil: vfs.Default
func WrapVFS(base vfs.FS, wrap func(filesys.FS) filesys.FS) vfs.FS

opts.FS = pebblestore.WrapVFS(opts.FS, enc.Wrap)               // a Pebble store, encrypted
```

`filesys.Default.OpenDir` returns a handle whose `Sync` fsyncs the directory,
except on Windows, where `FlushFileBuffers` fails on a directory handle and
the `Sync` is a no-op, as in Pebble's own `vfs.Default`. A Pebble store opened
through `pebblestore.VFS(enc.FS())` therefore syncs its database directory on
Windows exactly as one over `vfs.Default` does.

```go
// pkg/server/lifecycle
func NewLocalStorage(backendType database.BackendType, dataDir string, enc *atrest.Encryptor,
	storageCfg qrdbconfig.StorageConfig) (storage.Storage, *pebble.DB, error)
```

`NewLocalStorage` opens `pebble` and `file` through `enc`; it is the
constructor every standalone node store goes through — the daemon, the SDK
node and the embedded server. It opens `pebble` with `storageCfg`'s
`CacheSize`, `WALDir` and `MaxOpenFiles` (a zero keeps
`pebbleStorage.DefaultConfig`'s value) and `objstore` with its `Objstore`
section; the embedded server's `standalone.ServerConfig` carries the three
Pebble fields itself. A raft replica's `file` storage takes the same
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
  `Exists` and `Delete` fail the same way, because an encrypted store names
  its records under a record-name key that is read through the barrier.

## Configuration

```yaml
storage:
  backend: pebble        # pebble | memory | file | objstore
  cache_size: 0          # pebble block cache bytes; 0 = 512 MiB
  wal_dir: ""            # separate pebble WAL directory; empty = data dir
  max_open_files: 0      # 0 = 2000
  objstore:              # used when a backend is objstore
    provider: local      # local | memory | s3 | gcs | azure
    settings: {}         # passed verbatim to the go-objstore factory; never a credential
    credentials:         # optional; empty = the provider's default chain
      key_file: ""       # owner-only file holding the access key ID
      secret_file: ""    # owner-only file holding the secret (alone: an account key)
```

`cache_size`, `wal_dir` and `max_open_files` tune the standalone data plane's
Pebble store, the one `NewLocalStorage` opens. A raft replica's state machines
open their own Pebble stores and do not read them.

The provider credential is a **file reference**, never a value. A credential
setting in `settings` (go-objstore's `common.RejectCredentialSettings` list,
e.g. `secret_key`, `access_key_id`, `account_key`) is refused by `qrdbd start`
at load with a `config.ValidationError` on `storage.objstore.settings`, through
the storage seam's `objstore.RejectCredentialSettings` (an
`*objstore.CredentialSettingError` naming the setting), and by
`objstore.New` at every open, through the same function and before any
provider's creator runs, so a creator added through `objstore.RegisterProvider`
that does not delegate to a go-objstore `Configure` never receives the value;
`pkg/config` itself does not import go-objstore, because a lean standalone
embedder compiles it without that module. Any other key under `credentials` is
refused at load with a `config.UnknownFieldError`. No refusal carries the
value. `objstore.New` takes `objstore.WithCredentialFiles`: it reads each file
through `secure.ReadFileHandle`, straight into a go-enclave locked-memory
handle (one trailing line ending stripped; a file other than a regular,
owner-owned file of mode `0600` or tighter, empty, or over 64 KiB is refused),
passes the handles to go-objstore as a `common.Credentials` for the data and
the journal store, and destroys both before it returns, on success and on
every failed-open path: go-objstore backends take what they need during
configuration and retain no handle. Before reading any file, `objstore.New`
refuses a reference the provider would ignore with an
`objstore.CredentialIgnoredError` naming the provider, the credential and the
file: at the pinned go-objstore v0.9.0-alpha, `s3` reads the key and the
secret, `azure` reads only the secret (as the account key), and `gcs` (Google
application default credentials), `local` and `memory` read none. Accepting
such a reference would open the store under the provider's ambient identity
while the operator believes the configured credential is in use. A provider
added through `objstore.RegisterProvider` declares its reads there instead;
the pinned table is never written, and a name go-objstore ships is refused
even when its build tag leaves it out of the binary, so a registration cannot
change what a shipped provider reads, nor be answered with a shipped
provider's pinned reads in place of its own declaration. The registered
creator lives in the seam's own `sync.Map`, which `New` consults before
go-objstore's factory: the factory keeps its creators in an unsynchronized
map, filled only by its package initialisation, so writing a registration
there would race every concurrent registration and open. `New` resolves the
provider once, before any file is read, and both checks the reads and opens
the stores with that one resolution: a name neither registry knows is refused
as unsupported then, so a registration completing during the open can never
supply a creator that skipped the credential check. The
file is opened with `O_NONBLOCK` and every check runs on the opened descriptor
(`fstat`), so a FIFO or device is refused at once instead of blocking the
open. File references were chosen over
environment variables (readable in `/proc/<pid>/environ` and copied into an
unzeroizable Go string) and over `pkg/credentials` (which stores through a
`StorageBarrier` and so cannot hold the credential that opens the storage).

`pkg/config.StorageConfig` also carries `volume_pools` and
`allow_degraded_volume_boot`; see
[../volume-pools/engineering.md](../volume-pools/engineering.md).

A composer can replace the configured backend of a **standalone** node's data
plane: `server.WithStorageFactory` / `node.WithStorageFactory` take a
`func(dataDir string, dataPlaneFS filesys.FS) (storage.Storage, error)`, and the
node closes what it returns. A factory that writes files must route its I/O
through the `filesys.FS` it is given (a Pebble store through
`pebblestore.VFS(dataPlaneFS)`) so the data stays under the barrier. The
data-plane FS names host paths (`filesys.HostFS`), and the adapter takes its
database lock through Pebble's own default file system: a host file's lock
belongs to the process, so every Pebble store in the process locks through
that one owner, whichever way it was opened, with the platform's own lock
(`LockFileEx` on Windows). Such a store holds its database lock whether or not
the barrier is enabled; a second open of a held directory, in this process or
another, fails rather than waits. A
**clustered** node's `disk` and `fast` replicas take a composer's storage
through `node.WithReplicaStorageFactory`, a
`func(shardID, replicaID uint64, dir string, dataPlaneFS filesys.FS) (storage.Storage, error)`
called once per replica with that replica's own directory; the node closes
each storage with its replica, and a factory error fails the replica's open
with `statemachine.ReplicaStorageOpenError`. The clustered MVCC store and the
state machines that carry their own storage refuse the option with
`statemachine.ReplicaStorageNotApplicableError`. The seal store is node-local
by design and not behind either factory. See [release/extension-seams.md](../release/extension-seams.md).

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
| Objstore provider credential inline in `storage.objstore.settings` or `credentials` | refused by `qrdbd start` at load (`config.ValidationError`, `config.UnknownFieldError`) and by `objstore.New` at open (`common.CredentialSettingError`), naming the key, never the value | **yes** |
| Objstore `key_file` without `secret_file` | refused at load (`config.ValidationError` on `storage.objstore.credentials.secret_file`) and at open (`objstore.ErrCredentialKeyWithoutSecret`) | **yes** |
| Objstore credential file referenced for a provider that does not read it (`key_file` on `azure`; either file on `gcs`, `local` or `memory`) | refused at open, before any file is read (`objstore.CredentialIgnoredError`), naming the provider and the file | **yes** |
| Objstore credential file missing, not a regular file (a FIFO is refused without blocking), not owner-only, empty or too large | refused at open (`objstore.CredentialFileError` wrapping `secure.FileHandleError` / `FileModeError` / `FileOwnerError`) naming the file, never the value | **yes** |
| Objstore credential handle unreadable inside go-objstore | refused at open (`objstore.CredentialUnavailableError`) naming the files | **yes** |
| Objstore opened on a non-empty store without its format marker, or with another version | refused at open (`objstore.FormatMarkerMissingError`, `objstore.FormatVersionMismatchError`) | **yes** |
| Objstore `local` store holding a commit journal inside its data tree (the journal belongs in the sibling `<path>.journal` tree, derived from the physical root: absolute, cleaned and with every symlink resolved, so a symlink and its target open one data tree and one journal) | refused at open (`objstore.InTreeJournalError`); not recovered, restore from a backup | **yes** |
| Objstore `local` namespace reopened through another root/prefix split of the same directory (its journal is looked for under the other root) | refused at open (`objstore.NamespaceSplitError`); reopen it as created | **yes** |
| Objstore prefix whose namespace directory would take an enclosing namespace's record, format marker, journal or owner-lock name (`ns/k/61_/` beside `ns/`) | refused at open, before anything is created (`objstore.NamespaceCollisionError`) | **yes** |
| Objstore `local` prefix that is a symlink alias of another namespace's directory | opened as that one namespace: data, journal and owner lock derive from the symlink-resolved prefix, so a second alias is refused while the first is open (`objstore.NamespaceOwnedError`) and either alias replays its journal | **yes** |
| Objstore `local` prefix that resolves outside the provider root | refused at open (`objstore.NamespaceOutsideRootError`) | **yes** |
| Objstore `local` namespace opened while another engine, in any process, has it open | refused at open (`objstore.NamespaceOwnedError`); a `Destroy` retried on a closed engine is refused the same way and deletes nothing | **yes** |
| `Exclusive` on an objstore engine whose provider cannot exclude a second opener (all but `local` and `memory`) | refused before `fn` runs (`storage.ExclusiveUnsupportedError`); the standalone backend's guarded verbs fall back to `storage.Guarded` (in-process exclusion) | **yes** |
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
  record is named by the hex SHA-256 digest of the key (in an encrypted
  store, the hex HMAC-SHA-256, [RFC 2104](https://www.rfc-editor.org/rfc/rfc2104),
  under the store's record-name key), sharded two levels
  deep (two hex characters per level), holding `[keyLen:4 BE][key][value]` —
  with atomic temp-file + rename + fsync writes, every `MkdirAll`-created
  directory's parent fsynced too, and an all-or-nothing commit journal
  replayed at open (`pkg/storage/file/journal.go`), plus the
  `storage.Syncer` seam. That durability is why it is permitted as a hot
  apply path. The layout is an on-disk format with no migration from any
  other; see
  [architecture.md](architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout).
  Writes are serialized across processes by an advisory OS lock on
  `.qrdb-file-lock` (`pkg/util/filelock`, a thin wrapper over `flock(2)` /
  `LockFileEx`) plus an in-process mutex; `Exclusive` exposes it for a guarded
  read-check-write; see
  [architecture.md](architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout).
  `Delete` removes only the record, never its (fixed, bounded) shard
  directories, for the reason that doc gives: concurrent per-key workers
  (the `fast` state machine's write-through) make pruning them on a
  point-in-time emptiness check a race. The format version is 4
  (`formatVersion`, `pkg/storage/file/file.go`). `New` refuses a non-empty data
  directory lacking a matching format-version marker
  (`FormatMarkerMissingError`, `FormatVersionMismatchError`), or one whose
  marker records the other at-rest posture (`AtRestMismatchError`). With an
  enabled encryptor every record, the journal and the record-name key
  (`.qrdb-file-names`) are written through the barrier's encrypting file
  system; the marker (`4/encrypted`) is the one plaintext file. The layout has
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
  non-empty store lacking the `qrdb-objstore-format` marker for version `3`
  (`FormatMarkerMissingError`, `FormatVersionMismatchError`), so a store
  written in any other layout is refused, not misread. The marker records the
  naming limit (`3\nname-limit=<n>\n`) and a reopen adopts it, refusing one
  that no longer fits beneath its prefix (`NameLimitError`). `Destroy`
  deletes only the engine's journal, marker and canonical `k/`/`h/` record
  names, so an engine nested beneath its prefix keeps every object.
  There is no migration. See
  [architecture.md](architecture.md#the-objstore-engine-encoded-object-names).
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
  journal stays. A `local` engine owns its namespace from `New` to `Close`
  through a non-blocking OS lock in the sibling `<path>.owner` tree
  (`filelock.File.TryLock`), so it keeps `Exclusive`, as `memory` does; on any
  other provider `Exclusive` returns `storage.ExclusiveUnsupportedError` and
  `Guarded` gives in-process exclusion. A `local` reopen under another
  root/prefix split is refused (`NamespaceSplitError`). A `local` prefix is
  resolved through its symlinks once at open (`canonicalPrefix`), and data,
  journal and owner lock all use the result, so aliases are one namespace; a
  prefix that would make a directory of an enclosing namespace's object is
  refused by `validObjectPrefix` (`NamespaceCollisionError`). See
  [architecture.md](architecture.md#the-objstore-engine-encoded-object-names).
- **Pebble engine**: an LSM store; the production default.
- **Memory engine**: nothing survives the process.

## Test strategy

- **Unit**, per engine, against the same interface conformance suite — an engine
  that passes it is usable everywhere the interface is accepted.
- **Engine behaviour**: the journal's crash-mid-write replay; the format
  marker; at-rest encryption (no key or value readable on disk, the sealed
  error while sealed, the posture marker); re-encryption of a `file` store;
  group commit; the cross-process lock; arbitrary keys (fuzz and seeded
  property tests); objstore object-name encoding. Every function has a happy
  path and an error path.
- **Parity cells**: the storage parity tests and the `sdk/go/conformance`
  matrix run every engine through the same cells. The binary-key cells
  (`pkg/storage/binary_parity_test.go`, `binary_key_parity_test.go`) hold every
  engine to the same answer for every single byte, a NUL-prefixed table under
  scans and `List`, keys shaped like paths (separators, `..`, a drive letter,
  control characters, an embedded NUL), a key long enough that objstore names
  it by hash, and binary keys through a batch, a rollback and a snapshot. The
  file-engine cells run on
  tmpfs (`pkg/testutil/memdir`), so a deadline measures the code, not the disk.
- **Fault-injection seams**: a slow fake sync for group commit, a failing
  record sync recovered from the journal, failing lock and unlock calls, and
  provider read, list, exists and delete faults for objstore. The
  cross-process lock is proven by re-executing the test binary as competing
  processes over one `file` database.
- **Integration**: real daemons over `file` and `pebble` replica storage,
  sealed and unsealed, including enabling encryption from strategy `none`
  (`make integration-test-storage-engines`). The commit benchmarks
  (`make bench-storage-file`) keep a real disk.

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
> for the shape, the direction it proposes, and the decisions (OS-1..OS-7)
> this contract assumes. It is future work under the SPEC row
> `object-storage-lsm`. **Nothing below is implemented**, and no
> configuration key, type or error named below exists yet.

### Exported surface (target)

`pkg/storage/objstore` stops exporting `Engine`/`Batch`/`Iterator`/`Snapshot`.
It exports a `remote.Storage` adapter and nothing else that is KV-shaped:

```go
// NewRemote constructs a Pebble remote.Storage backed by a go-objstore
// provider, resolved exactly as New resolves it today. enc is the
// barrier-driven encrypting wrapper (OS-4); a nil or disabled encryptor is a
// no-op, matching pkg/atrest.Encryptor's convention.
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
// block cache (OS-2) — WithRemote narrows what the local dir holds, it does
// not replace it.
func WithRemote(store remote.Storage, locator remote.Locator, strategy remote.CreateOnSharedStrategy) Option
```

A `*pebble.DB` opened with `WithRemote` is constructed with a `CreatorID`
(OS-3): one per owning instance, generated once and persisted as a small local
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
  SSTables exactly as today; it does **not** reach objects written through
  `WithRemote`. The adapter encrypts the whole object payload with the same
  `seal.StorageBarrier` AEAD before `CreateObject` and decrypts after
  `ReadObject`. A barrier-sealed database with `WithRemote` configured and no
  working encryptor fails closed at open, matching
  `statemachine.SMStorageUnencryptedError`'s existing precedent for
  `file`-backed state machines.
- **One prefix, one `CreatorID`, one owning instance — never shared.** A
  replacement instance (replacement Raft replica, or a reassigned local-binding
  owner) never reopens a previous instance's prefix; it gets a fresh one. This
  is OS-3's answer and is a correctness invariant, not a cost optimization: two
  live Pebble instances writing under one `CreatorID`/prefix would corrupt each
  other's manifest.
- **`glacier` and `azurearchive` are refused at construction.** `NewRemote`
  returns a typed error (`ArchiveTierUnsupportedError`, new) for a provider
  go-objstore registers only as an `Archiver`, before any attempt to open a
  Pebble store against it. These stay backup/archive destinations only (see
  [../backup-restore/architecture.md](../backup-restore/architecture.md)).
- **No ranged read yet (OS-6).** Until go-objstore exposes one, `ReadObject`
  fetches the whole object once per open and serves `ReadAt` from a local
  cache keyed by object name. This is a documented performance limitation of
  phase 1, not a silent one: the adapter's doc comment says so, and the real
  fix is the ranged read requested from go-objstore.
- **The provider credential crosses as go-objstore takes it.** `NewRemote`
  passes the provider and its settings to go-objstore as `New` does today;
  when the SPEC row `object-storage-credential-handle` lands, `NewRemote`
  takes the same protected-memory credential handle and never holds a
  plaintext copy.

### Configuration (target)

The keys a node reads today select the backend and its provider, and keep
their meaning:

```yaml
storage:
  backend: objstore      # selects the LSM layout once it ships
  objstore:
    provider: s3         # local | memory | s3 | gcs | azure
    settings:            # passed verbatim to the go-objstore factory
      bucket: qrdb-data
    create_on_shared: all  # proposed, not a key today: all | lower
```

The bucket prefix a store writes under is go-qrdb's to choose, per owning
instance (OS-3), and never carries a key. The WAL, MANIFEST and block cache
live in the store's local data directory, resolved as the `pebble` engine's
is — the node's data directory or a volume pool
([../volume-pools/](../volume-pools/)) — so this design adds no second
local-path mechanism and no `local_dir` key. `create_on_shared` is the only
proposed key; whether it is exposed at all, or fixed at `all` for a
non-replicated binding, is decided with OS-7.

### Error semantics (target, additive)

| Condition | Behavior | Fails closed? |
|---|---|---|
| `provider` is `glacier` or `azurearchive` | `ArchiveTierUnsupportedError` at construction | **yes** |
| Store written in the encoded-name layout (`qrdb-objstore-format` version `3`) | refused at open with the typed format error; no migration (OS-5) | **yes** |
| Barrier sealed, no working encryptor, `WithRemote` configured | refused at open, matching `SMStorageUnencryptedError`'s precedent | **yes** |
| A second instance attempts to open an already-owned `CreatorID`/prefix | refused — Pebble's own manifest/lock detects the conflict | **yes** |
| go-objstore `ReadObject` for an object this instance did not write (prefix violated) | not expected to occur (OS-3); if it does, a corruption-class error, not silently served | **yes** |
| `raft.sm_storage`/`state_storage: objstore` before the phase-3 gate accepts it | refused, as today | **yes** |

### Test strategy (target, additive)

- The existing per-engine conformance suite runs over `pebble+remote`
  (local-provider go-objstore) exactly as it runs over `memory`/`file`/
  `pebble` today.
- **Encryption round trip**: barrier on, write, restart, read — object bytes
  on the local-provider backing directory are not the plaintext; barrier off,
  same round trip, object bytes equal the plaintext (today's documented
  behavior for `pebble`'s local files, now asserted for remote objects too).
- **Provider refusal**: `NewRemote("glacier", ...)` and
  `NewRemote("azurearchive", ...)` return `ArchiveTierUnsupportedError` before
  any network call.
- **Crash safety**: kill `-9` mid-compaction and mid-flush; reopen must
  succeed with no corruption, matching the existing `pebble` engine's crash
  test, parametrized onto `pebble+remote`.
- **Name hygiene (build gate)**: a test asserting no object name
  `pkg/storage/objstore` sends to go-objstore's `Put`/`CreateObject`
  round-trips through anything shaped like a qrdb key — a regression guard for
  the invariant above.
