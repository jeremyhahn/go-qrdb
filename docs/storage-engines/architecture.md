# Storage Engines — Architecture

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the object-storage
> LSM target design.** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.
>
> This document has two layers.
>
> - **Current state** sections (everything through "What this feature
>   deliberately does not do") describe the code as shipped.
> - **Target design: object-storage LSM segment layout** is a design contract
>   for how `pkg/storage/objstore` is rebuilt: object storage moves to an LSM
>   segment layout, not one object per key. Its open questions are listed,
>   each with a recommendation, in "Open decisions" inside that section.
>   **Nothing in that section is implemented.**
>
> Volume pools are documented in [../volume-pools/](../volume-pools/).

## Problem

The layer that decides *what history is kept* and the layer that decides *where
bytes land* have different lifetimes and different failure modes. Fusing them
makes every new backend re-implement revision handling, and makes tenant
isolation a per-backend concern rather than a composed one.

## Shape chosen

A narrow `Engine` interface with several implementations, and a wrapping engine
that adds tenant isolation to any of them.

```go
type Storage interface {
	Reader                                  // Get, NewIterator
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

The interface is also compatible with go-quicraft's `seal.StorageBackend` **via
Go duck typing** — no explicit interface embedding is required, so the two
projects share a shape without either importing the other's declaration.

### Engines

| Engine | Package | What it is |
|---|---|---|
| File | `pkg/storage/file/` | hashed, flat, content-addressed filesystem mapping; shard configs, metadata |
| Memory | `pkg/storage/memory/` | in-memory, for tests and ephemeral data |
| PebbleDB | `pkg/storage/pebble/` | production-grade, extracted from the Raft state machine |
| ObjStore | `pkg/storage/objstore/` | object-storage-backed, via go-objstore; arbitrary byte keys under encoded object names |

An engine carries no tenant knowledge. Every tenant is isolated by its own
database and barrier, not by a key prefix; see
[../seal/multi-tenant.md](../seal/multi-tenant.md).

### The file engine: a hashed, flat, content-addressed layout (BREAKING)

**No migration from any other layout.** The layout derives no path from the
key's bytes. A path derived from the key fails in ways that are fatal to a
replica: a key that is a `/` prefix of another collides with the other key's
parent directory, and a key of thousands of `/`-delimited segments exceeds
`PATH_MAX`. Either failure, returned from `SM.Apply` as a storage error, stops
`lastApplied` advancing on that replica, and on the system shard that takes
authentication down with it. PebbleDB and in-memory storage have no such path:
two keys are two byte strings.

Each key's record is named by the
**hex SHA-256 digest of the key** in a plaintext store, and by the **hex
HMAC-SHA-256 ([RFC 2104](https://www.rfc-editor.org/rfc/rfc2104)) of the key
under the store's record-name key** in an encrypted one (stdlib
`crypto/sha256` and `crypto/hmac`; no hand-rolled hashing; see "At-rest
encryption" below). Either name is sharded two levels deep by the first four hex characters (two
characters per level: 256 subdirectories per level, 65,536 leaf directories
in total — a small, FIXED number regardless of keyspace size). The record
file holds `[keyLen:4 BE][key][value]`: the real key travels with its value,
so a read can verify the stored key matches the one asked for, and a listing
can recover every key without a separate index.

This gives every key the same properties, independent of its length or
content:

- **Constant path length and depth.** No key, however deep or long, can
  produce `ENAMETOOLONG` or exceed a single filesystem's `NAME_MAX` — there
  is no segment derived from the key's bytes at all.
- **No collisions, no prefix relationships, no case or Unicode
  ambiguity.** SHA-256 collisions are not a practical concern, and a key is
  never a filesystem-meaningful prefix of another, because `/` is not read
  as a path separator.
- **A fixed, bounded set of shard directories.** `Delete` removes a key's
  record file but never prunes the (at most 65,536) shard directories that
  can ever exist: `storage.Storage` carries no promise that `Set` and
  `Delete` on DIFFERENT keys are serialized against each other, and the
  `fast` state machine's write-through issues them from concurrent per-shard
  workers, ordered only per key, so two keys that happen to hash into the
  same shard directory can have their `Set` and `Delete` in flight at once.
  Pruning on a point-in-time "is it empty" check would race a concurrent
  `Set` hashing into that shard between the check and its own `MkdirAll`.
  Keeping the fixed, small set of shard directories forever costs nothing
  measurable and removes that race entirely.
- **List and the iterator pay for it in I/O, not correctness.** A key's
  filename carries no information about the key (it is a hash), so a
  prefix scan or full iteration must open and decode every candidate record
  to recover its key. That is the deliberate tradeoff of a flat,
  content-addressed layout.

**Format version marker.** `New` writes a marker (`.qrdb-file-format`,
naming the current format version and the store's at-rest posture,
`4/encrypted` or `4/plaintext`) into a directory it finds empty, and
refuses to open a NON-empty directory that lacks one or names a different
version (`FormatMarkerMissingError`, `FormatVersionMismatchError`) — a
validity check, not migration code. A marker naming the other posture is
refused too (`AtRestMismatchError`); see "At-rest encryption" below. A
directory written in any other layout, or in a future format this binary
predates, is refused rather than silently misread: a version-3 directory
fails at open with `FormatVersionMismatchError`. "Non-empty" ignores
`lost+found`: a filesystem's own mount-root directory, never written by this
engine, so a mount root that holds nothing else is a fresh mount, not
unmarked data.

**Durability.** Every directory `MkdirAll` creates — the engine root and
each of a key's two shard levels — has its PARENT fsynced immediately after
creation, publishing the new directory entry; a record's temp-file,
fsync, atomic-rename sequence. A
concurrent `Set` that loses the race to create a shard directory a sibling
key just created, or that simply finds it already there, fsyncs the parent
itself before proceeding rather than trust that the creator's own fsync has
already landed — the two are unordered with respect to each other.

**Several processes on one directory.** The engine may be opened on the same
directory by more than one process — an application that loads a PKCS#11
module and the daemon serving the same token, for example. Every mutation
(`Set`, `Delete`, a batch commit, and the open-time recovery) holds an
exclusive advisory operating-system lock, `flock(2)` on Unix and `LockFileEx`
on Windows, taken on a permanent, empty file in the engine root
(`.qrdb-file-lock`; like `lost+found`, it does not count as content when `New`
decides a directory is fresh). The lock is the kernel's, not a protocol of our
own, so a process that dies releases it. A mutex in the engine covers the other
goroutines of the same process, since an OS lock belongs to the open file.
`New` takes the lock around the format marker, journal replay and stray
temp-file cleanup, so a second process opening the directory waits for a write
in flight instead of deleting its temporary file or replaying its journal. The
format marker is checked first: a directory `New` refuses (a missing marker
beside content, or another format version) is refused before its journal is
replayed, so the refusal writes nothing.

*Guarded writes.* `SaveIfAbsent`, `CompareAndSwap` and `CompareAndDelete` on a
database whose backend is `file` read a key and then write it, and that pair
must be indivisible. The engine therefore implements the optional
`storage.Exclusive` seam: `Exclusive(fn)` runs `fn` with the same lock held,
handing it a read/write view that does not take the lock again, and the
standalone backend runs each guarded verb inside it. Two processes racing a
swap on one counter cannot both pass the compare.

*Readers take no lock* and stay correct: a record is replaced by renaming a
fully written temporary file over it, so a read sees the old record or the new
one. `Snapshot` and iteration are consistent per record, not across a batch
committing in another process. The lock is advisory
and only binds processes using this engine; the directory must be on a local
filesystem, because `flock` is not dependable over network filesystems. Sharing
covers the `file` engine opened directly; a wrapper over the engine that does
not forward `Exclusive` gives its guarded verbs in-process exclusion only.

**The format reaches every `file.New` caller.** It is wider than
`raft.sm_storage: file` (`sm_type: disk`/`fast`) alone: `file.New` also
backs [database's `BackendFile` data plane](../databases/architecture.md),
every database's barrier-metadata store regardless of that database's own
configured backend (`pkg/database/manager.go`'s `openBarrierMetaBackend` —
deliberately backend-independent, since the metadata must survive a restart
even for an in-memory database), and the backup repository
`qrdbd start --backup-dir` names. Each opens its directory through the same
`file.New`, so each refuses a directory in another layout the same way. There
is no migration code: start the replica over raft's restore or the
backup/restore path, or reseed `sm_storage: file` from a backup, rather than
upgrading a directory in place.

**A `file`-backed backup is in the layout of the binary that wrote it.** A
binary with a different format version opens it as a `file.New` target and
refuses it (`FormatMarkerMissingError` or `FormatVersionMismatchError`)
rather than misreading it. There is no cross-version restore path for
`sm_storage: file`/`BackendFile` backups. See
[docs/backup-restore/user.md](../backup-restore/user.md).

**Operational note: keep `sm_storage` identical across a shard's
replicas.** Raft replicates the command log, not a storage representation,
so replicas composing different backends still apply the identical command
sequence and are not unsafe by construction — but nothing verifies this at
cluster join, and a backend-specific defect is the kind of thing a mismatch
would surface only indirectly, through a state-hash divergence (`qrdb admin state-hash`, see
[../clustering/api.md](../clustering/api.md#replica-state-hashes)). There is no
operational reason to mix it.

### At-rest encryption: one mechanism for every on-disk backend

**The file engine does its I/O through the barrier's encrypting file system,
the same one Pebble writes through.** There
is one at-rest mechanism for every backend that writes files, and no
file-specific cipher.

The mechanism is go-quicraft's `seal/encfs`, reached through
`pkg/atrest.Encryptor`. Each file it creates starts with a header carrying a
fresh per-file data key wrapped by the barrier, and its contents are sealed
with AES-GCM under that key. `atrest.Encryptor.FS` hands Pebble the encrypting
file system as `Options.FS`; `atrest.Encryptor.Wrap` layers it over the file
engine's own base file system, which `file.WithEncryptor` installs. A
`storage.backend: file` node store opens the engine this way
(`lifecycle.NewLocalStorage`) on the standalone daemon, the SDK node and the
embedded server alike, and so does a raft replica's `raft.sm_storage: file`.

What a `file` store leaves on disk under the seal:

| Artefact | At rest |
|---|---|
| record file, `[keyLen:4 BE][key][value]` | ciphertext: the key and the value alike |
| record file name | the hex HMAC-SHA-256 of the key under the store's record-name key, sharded two levels deep: without the barrier no name can be computed for a guessed key, and two stores holding the same keys share no name |
| `.qrdb-file-names` | ciphertext: the record-name key, 32 random bytes minted with the store's first write |
| commit journal | ciphertext |
| `.qrdb-file-format` | plaintext, `4/encrypted`: it says how every other file is read, so it is readable before that choice is made, and holds nothing about the data |
| record count and sizes | visible, as Pebble's file count and sizes are |

**Posture is fixed per directory.** The marker records whether the store was
written through the barrier. Opening a `4/plaintext` store with an encryptor,
or a `4/encrypted` store without one, fails with `AtRestMismatchError`
naming both. Pebble's encrypting file system passes a pre-existing plaintext
file through on read; the file engine refuses the whole store instead, so a
store never serves plaintext records beside encrypted ones. A store created
under strategy `none` is `4/encrypted`: the format is the encrypted one and
only the key is credential-free. A store written by another format version
is refused at open; there is no migration code.

**Sealed.** A store can be opened while the barrier is sealed: `New` reads
only the plaintext marker. Every record read or write then fails with the
barrier's error, `seal.ErrSealed`. `Exists` and `Delete` fail the same way:
a record's name needs the record-name key, which is read through the barrier.
Once read, the key is held for the engine's life. A store that has never been
written has no key file and so no records; its reads find nothing and mint no
key.

**Re-encryption.** A node encryption migration — `barrier enable-encryption`,
or any recycle of the node's key — moves a `file` node store to the new key
the way it moves a Pebble one: in the same journaled swap. Each record is
read through the old key and written to a sibling store opened under the new
key. The copy is then verified record by record against the source, and the
directories are swapped. A record left under the old key is unreadable once
the old key is gone, so under either `--mechanism` (`swap` or `in-place`) the
copy goes through the file engine itself, record by record.
`NodeStoreMigration.FileStoreDirs` names the stores moved this way on a
standalone node, where the migration provider classifies each node store
directory by its backend. In raft mode `qrdbd` classifies the state machine's
stores the same way, by `sm_type` and `sm_storage`, so `sm_storage: file` is
moved record by record beside the WAL.

**Per backend:**

| Backend | Under the seal |
|---|---|
| `pebble` | every file through the encrypting file system |
| `file` | every record and journal file through the encrypting file system |
| `objstore` | no file system to encrypt: object names encode the keys; a standalone node store is unencrypted and the capabilities report names it (`unencryptedAtRest`) |
| `memory` | nothing persisted |

### The objstore engine: encoded object names (BREAKING)

**No migration.** A storage key is arbitrary bytes, but a
go-objstore provider accepts only a UTF-8 object name of at most 1,024
bytes, free of NUL, control characters, `..` and `//`. A filesystem provider
also limits each path component to 255 bytes. Named verbatim, a binary key,
a key carrying any of those sequences, or a key past those lengths would be
refused by the provider, and a key such as `a/` would name a directory, not
an object. So that every key the rest of qrdb accepts — anything up to the
global 65,535-byte cap (`protocol.MaxClientKeyLength`) — can be stored, every
key is stored under a name derived from it, in one of two forms:

- **Direct**, `k/` + the key's lowercase hex, cut into segments of at most
  128 characters joined by `/`, with `_` appended to the last. Every name
  of this form is a valid object name on every provider, and no component
  exceeds 129 bytes. Hex keeps byte order and keeps prefixes: the hex of a
  byte prefix is a string prefix of the hex of every key it prefixes, so
  `List(prefix)` still narrows on the provider. The `_` terminator means a
  key's object can never share a name with the directory a longer key's
  segments need.
- **Hashed**, `h/` + the lowercase hex SHA-256 of the key, for a key whose
  direct name would exceed the provider's 1,024-byte name limit (keys longer
  than about 500 bytes). This is the file engine's approach above: the
  object's body is a record carrying the key itself,
  `uvarint(len(key)) || key || value`, and every read verifies that the
  stored key hashes to the object's name and equals the key requested
  (`KeyMismatchError`), refusing a truncated or malformed record
  (`RecordCorruptError`). Hashed names cannot narrow a listing, so `List`
  and iteration read every hashed record for its key. That cost falls only
  on keys too long to name directly.

Anything under `k/` or `h/` that is not the canonical name of some key is
not a record — for example, a provider's in-flight temporary file — and
every listing skips it.

**Existing data.** `New` writes a format marker object
(`qrdb-objstore-format`, version `2`) into an empty store, and refuses to
open a store that is not empty and has no marker (`FormatMarkerMissingError`)
or that carries another version (`FormatVersionMismatchError`). A store
written with verbatim object names has no marker, so it is refused at open,
not read with its keys silently missing. There is no migration code: move
the data by reading it out through the binary that wrote it and writing it
in through this one, or start the store empty. The store must be dedicated to the engine:
any object the engine did not write makes an unmarked store non-empty.

### Alternatives rejected

- **One engine.** Rejected: memory is required for tests to be fast and
  hermetic; file is required where a real filesystem layout matters; Pebble is
  required for production write throughput.
- **Per-backend tenant isolation, or a key-prefix wrapper.** Rejected: it would
  be reimplemented per backend and get subtly different each time, and a prefix
  is not a cryptographic boundary. Each tenant gets its own database and
  barrier, above this layer.
- **A fatter interface.** Rejected: `storage.Storage` (`Get`, `NewIterator`,
  `Set`, `Delete`, `DeleteRange`, `List`, `Exists`, `Batch`, `Snapshot`,
  `Close`) is what a backend must provide.
  Anything richer (revisions, ranges, transactions) is built above it, which is
  what lets a new backend be small.

### The closed engine is part of the interface, not an afterthought

`Close` is one of the interface's methods, so "what happens to a call that
arrives after it" is part of the contract every backend owes — and it is a
contract a **composed** system needs, because the layer that closes the store
and the layer still writing to it are usually different goroutines with
different lifetimes.

One condition, one identity: **`ErrStorageClosed`**. Every backend's refusal
unwraps to it, so a consumer writes one `errors.Is` rather than one branch per
backend, and never a message comparison.

Two design consequences:

- **`Batch()` and `NewIterator()` return a value, not a `(value, error)` pair**,
  so a closed engine cannot refuse them the way `Get` does. Rather than let
  each backend improvise — a live handle over stale state, or one wired to a
  released resource — `pkg/storage` owns the two inert handles all of them
  return, `NewClosedBatch` and `NewClosedIterator`.
- **Ordering `Close` against in-flight work is a per-backend cost, paid where
  it buys something.** A backend whose `Close` releases a resource that panics
  on later use must make its closed check binding rather than advisory; one
  whose `Close` only sets a flag has nothing to order against. PebbleDB is the
  first kind and carries a reader gate; memory, file and objstore are the
  second and do not.

### Alternatives rejected for the closed-store contract

- **Recover the panic at the SDK boundary and convert it.** Rejected: it
  converts a symptom at a layer that cannot tell an orderly shutdown from a
  genuine bug, and every consumer above would inherit the same guesswork. The
  layer holding the resource is the only one that can answer the question, so
  that is where it is answered.
- **Let each consumer match the panic text.** Rejected outright: it turns a
  reworded message in a third-party dependency into a silent crash.

## Volume pools

Volume pools, the named per-node places a tenant's store lives, are their own
feature: [../volume-pools/](../volume-pools/architecture.md). `pkg/volume`
holds the `Registry` of `Provider`s (`NewDirProvider` is the one this repo
ships); `pkg/server/lifecycle.NewVolumeRegistry` builds it.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `storage.Storage` interface | `pkg/storage` | go-qrdb |
| The memory, file, pebble and objstore engines | `pkg/storage/{memory,file,pebble,objstore}` | go-qrdb |
| Volume registry and providers | `pkg/volume` | go-qrdb |
| Object-storage backends (local, memory, S3, GCS, Azure, Glacier) and the pool primitive | — | **go-objstore** |
| `seal.StorageBackend` shape, reached from a `storage.Storage` through `seal.NewStorageAdapter` | `pkg/seal` | **go-quicraft** |
| State machines composed over these | `pkg/statemachine`, `pkg/engine` | go-qrdb |

## Consumers outside this repo

**go-xkms imports `go-qrdb/pkg/storage` for all storage operations.** A thin
networked adapter in go-xkms (`pkg/storage/qrdb/` there) wraps the go-qrdb SDK
client into the `storage.Storage` interface for distributed mode. That makes the
`storage.Storage` signature a cross-repo contract: changing it is a breaking change for
go-xkms, not only for this repo.

## Trust boundaries

- **Tenant isolation is not this layer's job.** An engine does not know about
  tenants; each tenant's engine is its own database behind its own barrier.
- **At-rest encryption is the barrier's job** — see
  [../barrier-seal/](../barrier-seal/). An engine that writes files writes
  them through the file system it is given; under the seal that is the
  barrier's encrypting file system, so the engine holds no key and implements
  no cipher. Engines store what they are given.
- Volume pool paths are filesystem paths on the node; a pool registration is a
  privileged operation.

## What this feature deliberately does not do

- **No revision history.** That belongs to the MVCC state machine — see
  [../key-value/](../key-value/architecture.md).
- **No cipher of its own.** The barrier supplies the encrypting file system
  (and, for `objstore`, the value cipher).
- **No cloud object-storage backends of its own.** Those are go-objstore's, and
  are build-tag gated (`awss3`, `gcpstorage`, `azureblob`, `glacier`,
  `azurearchive`).

## Target design: object-storage LSM segment layout

### Problem with the shipped `objstore` engine

`pkg/storage/objstore` puts one object per key, named from the key (see
[the encoded layout](#the-objstore-engine-encoded-object-names-breaking)).
The encoded names make every key storable, but every write is one PUT and
every listing walks one object per key, so write and listing cost scale with
key count, not with data volume. Keys too long to name directly also lose
provider-side prefix narrowing.

### Direction this design takes

**Object storage moves to an LSM segment layout.** Keys live inside sorted,
immutable segment files; object names are short, opaque segment identifiers
that carry no key material. The SPEC row `object-storage-lsm` holds its status.

### Shape chosen

**Do not write an LSM from scratch. Compose Pebble v2's remote-storage support
over go-objstore.** qrdb already depends on `github.com/cockroachdb/pebble/v2`
for the `pebble` engine. Pebble v2 can keep its sorted SSTables on an
object-storage-shaped backend through two pieces in
`objstorage/remote` / `objstorage/objstorageprovider`:

```go
type remote.Storage interface {
    io.Closer
    ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error)
    CreateObject(objName string) (io.WriteCloser, error)
    List(prefix, delimiter string) ([]string, error)
    Delete(objName string) error
    Size(objName string) (int64, error)
    IsNotExistError(err error) bool
}

pebble.Options{
    Experimental: pebble.Experimental{
        RemoteStorage:         remote.StorageFactory,  // locator -> remote.Storage
        CreateOnShared:        remote.CreateOnSharedAll, // every sstable remote
        CreateOnSharedLocator: remote.Locator("<provider>/<prefix>"),
    },
}
```

Object names Pebble generates for remote objects are its own, not qrdb's keys:
`<hash>-<creator-id>-<file-num>.sst` (e.g. `1a3f-2-000001.sst`,
`objstorageprovider/remote_obj_name.go`) — at most ~24 bytes, no NUL bytes, no
path separators. Segment names never derive from qrdb keys, so object-name limits never
constrain a key.

**`pkg/storage/objstore` stops being a KV engine and becomes a
`remote.Storage` adapter over go-objstore**, consumed by `pkg/storage/pebble`
through a new construction option, e.g. `pebble.WithRemote(objstore.NewRemote(provider,
settings), locator, strategy)`. There is no second, parallel engine
implementation:

```
pkg/storage/pebble        — unchanged: *pebble.DB, storage.Storage, Batch, Iterator, Snapshot
pkg/storage/objstore       — rewritten: remote.Storage adapter + encrypting wrapper over go-objstore
                             (ReadObject/CreateObject/List/Delete/Size/IsNotExistError)
```

This is what makes "every engine on every backend" true for `objstore` by
construction rather than by re-implementation: `pebble.PebbleStorage` is
already composed by `MVCCEngine`, secondary indexes, time series and vector
storage. An `objstore`-backed database becomes a `*pebble.DB` whose SSTables
happen to live in a bucket; nothing above `storage.Storage` changes, and
nothing needs to be re-taught to a second KV implementation. The flat
`objstore.Engine` and its commit journal are dropped: Pebble's own WAL and
manifest give this backend the atomic-batch and crash-replay guarantees the
`pebble` backend has, because it **is** the `pebble` backend with one storage
option changed.

Composition, conceptually:

```
                     +-------------------------------+
                     |pkg/storage/pebble.PebbleStorage|   storage.Storage
                     |   (*pebble.DB, unchanged)      |   (Get/Set/Delete/List/
                     +----------------+----------------+   Exists/Batch/Snapshot)
                                      |
                 local vfs.FS         |         remote.Storage
         (WAL, MANIFEST, block cache) |     (sorted, immutable SSTables)
                                      |
                              +-------+--------+
                              |  pkg/storage/   |
                              |  objstore       |  encrypts, then calls go-objstore
                              |  (remote.Storage|
                              |   adapter)      |
                              +-------+--------+
                                      |
                          go-objstore common.Storage
                          (local | s3 | gcs | azure)
```

### Open decisions

The questions implementing this design must settle, each with a
recommendation.

**OS-1 — Is Pebble v2's remote storage a stable, supported surface?**
No — it is real, functional and already used in production by CockroachDB, but
every knob lives under `pebble.Options.Experimental`
(`RemoteStorage`, `CreateOnShared`, `CreateOnSharedLocator`,
`SecondaryCacheSizeBytes`), which is Pebble's own naming for "no API
stability promise." Two concrete caveats: (1) it requires
`FormatMajorVersion >= FormatMinForSharedObjects` (`= FormatVirtualSSTables`);
Pebble's `Options.EnsureDefaults` bumps the format version for us
automatically the moment `CreateOnShared != CreateOnSharedNone` is set, so this needs no manual version pinning on our side,
but it does mean a store opened with remote storage can never be read by an
older Pebble build. (2) Every Pebble instance that writes to a shared locator
needs a `CreatorID` (`SetCreatorID`, `objstorage.CreatorID`), assigned once and
persisted locally — this is the "whose object is this" identity the design
below (OS-4) builds on.
**Recommendation: proceed.** "Experimental" here means Pebble reserves the
right to change the Go API shape across versions, not that the mechanism is
unsound; CockroachDB's disaggregated-storage deployments are the production
proof. Pin to an exact `pebble/v2` version (already qrdb's practice) and treat
an upgrade that touches `objstorage/remote` as a reviewed change, same as any
other Pebble bump.

**OS-2 — WAL placement.**
Pebble's WAL and MANIFEST are never remote-storage candidates — only SSTables
(and blobs) go through `remote.Storage`; `WALDir` is always a local `vfs.FS`
path, and `WALFailover` only fails over to a second *local* directory.
There is no "remote WAL" option to evaluate; the question is really whether
the local WAL needs to be durable on its own.
- A `raft.sm_storage: objstore` database (hot apply
  path): the Raft log is the durability backstop beyond `lastApplied`, exactly
  as for `sm_storage: pebble` today. The local WAL can be small and the
  replica's own recovery already depends on replaying from the Raft log past
  whatever the local WAL lost — this is not a new property this design
  introduces.
- A `backend: objstore` database (today's "local", non-replicated binding —
  see `../databases/architecture.md`): there is no replicated log underneath
  it. The local WAL **must** be durable, on a real local disk, exactly like
  `backend: pebble` today. The volume holding a `pebble` engine's data
  directory also roots the WAL/MANIFEST/block-cache directory for an
  `objstore`-backed local binding. **Nothing here is optional or remote.**
**Recommendation:** local, durable WAL + MANIFEST in both modes, in the
database's volume. Only the SSTables move to the bucket.

**OS-3 — Ownership: per-replica prefix, or shared across replicas?**
qrdb's Raft model has every replica apply independently to its own state
machine and its own storage (`../key-value/architecture.md` — "replication is
the host's property, not the engine's"). CockroachDB's own shared-storage work
goes further (replicas sharing one physical SSTable set, follower reads served
from shared storage without a local copy) — that changes *what a replica is*,
which is out of scope for this design.
**Recommendation: each owning Pebble instance gets its own bucket prefix and
its own `CreatorID` — never shared.** For a `backend: objstore` local binding
there is exactly one instance (one owning node per OD-2 in
`../databases/architecture.md`), so this is trivially one prefix. For a future
Raft-replicated `objstore` hot apply path (OS-7, not phase 1), each replica
still applies independently and still gets its own exclusive prefix; a
replica that is replaced does not inherit the old replica's objects, which are
reclaimed like any other obsolete SSTable. Compaction ownership therefore
never crosses an instance boundary, and cost is the same N-times storage a
Raft group already pays on local disks today — this design changes *where*
the bytes are durable, not how many copies of them must exist.

**OS-4 — Encryption at rest: a real gap, not a detail.**
`pkg/atrest.Encryptor.Apply` wraps `pebble.Options.FS` — a `vfs.FS` — with
`seal/encfs`. `Options.Experimental.RemoteStorage` is a **different**
interface; Pebble never routes a remote object through `FS` at all. **The
barrier wiring does not reach remote SSTables: this path has no encrypting
layer.**
- A: go-objstore's own `common.Encrypter` and encrypting storage wrapper.
  Rejected — a second, parallel crypto path with its own key material is
  exactly the "no hand-rolled crypto, one canonical home" problem this
  project avoids everywhere else, and it does not share key custody with the
  barrier.
- B: rely on the provider's own server-side encryption (SSE-S3, SSE-KMS) only.
  Rejected as the sole mechanism — it is provider-conditional (the "local" and
  "memory" providers have none), and leaves data outside the barrier's custody
  model entirely.
- C (recommended): a new encrypting `remote.Storage` wrapper in
  `pkg/storage/objstore`, parallel in spirit to `seal/encfs` but shaped for
  whole-object payloads instead of a `vfs.File`: encrypt the plaintext object
  with the **same `seal.StorageBarrier` AEAD** (AES-256-GCM, already audited,
  already the mechanism `encfs` uses) before `CreateObject`, decrypt after
  `ReadObject`. No new cipher, no new key hierarchy — the barrier's existing
  per-tenant DEK is the key, as it already is for local Pebble files.
**Recommendation: C.** This closes the gap rather than documenting around it.

**OS-5 — Key length.**
Pebble's `remoteObjectName` never takes a qrdb key as input, so go-objstore's
`common.MaxKeyLength` (1,024 bytes) and the 255-byte path-component limit
never bound a key.
**Recommendation:** none needed; the direct and hashed object names have no
role in the segment layout.

**OS-6 — go-objstore has no ranged read.**
`common.Storage.Get`/`GetWithContext` return a whole-object `io.ReadCloser`;
there is no `ReadAt`/byte-range method. Pebble's `remote.ObjectReader.ReadAt`
expects concurrent, partial, offset reads of potentially large SSTables — the
pattern every mainstream object-store API (S3, GCS, Azure) supports
natively via HTTP Range, and every local filesystem supports via
`os.File.ReadAt`. go-objstore's own interface does not expose it.
**Recommendation:** add a ranged read method to go-objstore's
`common.Storage`, in go-objstore. Until it lands, `pkg/storage/objstore`'s
`ReadObject` fetches the whole object once per Pebble open (not per `ReadAt`
call — cached locally, keyed by object name) and serves `ReadAt` from the
cached bytes; this is a performance caveat for cold reads of large SSTables,
not a correctness one, and Pebble's own `SecondaryCacheSizeBytes` block cache
already absorbs the common case (repeated reads of the same block range).

**OS-7 — Lifting "objstore is refused as a hot apply path."**
[engineering.md](engineering.md) refuses objstore as `raft.sm_storage`.
Pebble's native atomic batch and WAL serve the *local-binding* case. Whether
to also allow it as a Raft `sm_storage` choice is a separate question with its own
blast radius (every replica now writes to a bucket under load, under
compaction, under leader failover) and is **not** part of phase 1 of this
design.
**Recommendation:** lift the refusal only for `backend: objstore` **local**
(non-raft) bindings in phase 1 (see "Phased implementation plan"). Leave the
Raft hot-apply-path refusal in place until a phase 2 that is scoped,
acceptance-tested under compaction and leader-failover load, and — because it
changes what a replica durably writes where — proposed into a group the same
way every new Raft-visible behavior is gated (the cluster feature gate,
`../databases/architecture.md`).

### Alternatives considered

- **Keep one object per key with encoded names** (the shipped layout).
  Rejected as the end state: it makes every key storable but does nothing
  for write or listing cost, which scale with key count regardless of
  encoding.
- **Keep the flat engine and its commit journal for atomic batches.** Moot:
  Pebble's own WAL gives the LSM design atomic batches; nothing to emulate.
- **A different pure-Go LSM-on-object-storage library.** Surveyed and none
  found at comparable maturity: the closest prior art (e.g. SlateDB) is Rust,
  not Go, and RocksDB-Cloud is C++. Pebble is already a qrdb dependency,
  already production-hardened as the `pebble` engine, and its remote-storage
  support is exercised in production by CockroachDB. No alternative library
  clears that bar.
- **CockroachDB-style disaggregated storage** (replicas sharing one physical
  SSTable set; followers serve reads from shared storage without their own
  local copy). Rejected: it changes the Raft apply model
  itself (replicas stop being independent appliers), which is a materially
  larger change than a storage engine, and neither "every engine runs on
  every backend" nor "no node-local logical state" needs it — see OS-3.
- **An in-house LSM.** Not pursued, and nothing here forces it: Pebble's
  remote storage reaches the target shape (sorted segments, short opaque segment names) without writing
  one.

### Recovery and backup, briefly

- **Node loss, `backend: objstore` (single owner, local binding).** The
  bucket already holds every durable SSTable; only the local WAL/MANIFEST are
  lost with the node. Reopening on a replacement node is cheap (no data
  re-transfer, just MANIFEST + WAL recovery) **provided the old owner is
  fenced before the new one opens the same prefix** — that fencing and
  ownership handoff is `../databases/architecture.md`'s placement/catalog
  design (OD-2, OD-4), not something this storage engine invents. This design depends on that mechanism; it does not duplicate it.
- **Node loss, a Raft replica (if OS-7 is later accepted).** Unchanged from
  today's `sm_storage: pebble`: a replacement replica gets a fresh local
  directory and its own new prefix/`CreatorID`, and is caught up through the
  existing `SaveSnapshot`/`RecoverFromSnapshot` path (`pkg/statemachine`). The dead replica's old prefix is simply
  abandoned and reclaimed — consistent with OS-3's no-sharing answer.
- **Backup and restore stay on the existing logical path, unchanged.**
  `../backup-restore/architecture.md`'s shard snapshots already iterate
  through `storage.Reader` to a destination that can itself be a go-objstore
  URI — that is backend-agnostic today and stays so. A bucket-native fast
  backup (copying live SSTable objects directly, using Pebble's own
  ref-tracking metadata instead of re-reading every key) is a plausible
  future optimization but is explicitly **not** part of this design: it is
  not needed for correctness, and backup/restore stays simple.

### Provider coverage

Every go-objstore provider that supports fast, random reads is viable:
`local`, `memory`, `s3`, `gcs`, `azure` — reachable at Pebble object
names of ~24 bytes, nowhere near any provider's key-length ceiling (OS-5).
**`glacier` and `azurearchive` are archive-only tiers with retrieval delays
measured in hours** (go-objstore registers them only as `common.Archiver`, never as
`common.Storage`): they
cannot serve `ReadObject` at the latency an LSM read path needs and must stay
what they already are — backup/archive destinations reached through
`../backup-restore/`, never a live `objstore` provider.

### Component boundaries (target)

| Piece | Package | Owner |
|---|---|---|
| `pebble.PebbleStorage` and its `Batch`, `Iterator` and `Snapshot` | `pkg/storage/pebble` | go-qrdb (unchanged) |
| `remote.Storage` adapter over go-objstore, encrypting wrapper (OS-4) | `pkg/storage/objstore` | go-qrdb (rewritten) |
| `common.Storage` providers (local, s3, gcs, azure) and their build tags | — | **go-objstore** |
| Ranged/offset reads on `common.Storage` (OS-6, not yet available) | — | **go-objstore**, not implemented here |
| `remote.Storage`, `objstorage.CreatorID`, `Options.Experimental.RemoteStorage` | — | **Pebble v2** (`cockroachdb/pebble/v2`) |
| Fencing and ownership handoff for a single-owner local binding | `pkg/database`, `pkg/placement` | go-qrdb (`../databases/` design, depended on, not owned here) |

### Phased implementation plan

**Phase 1 — the adapter and the engine option.**
Rewrite `pkg/storage/objstore` as the `remote.Storage` adapter (OS-6's interim
whole-object cache) plus the encrypting wrapper (OS-4). Add
`pebble.WithRemote(...)` to `pkg/storage/pebble`, wiring
`Options.Experimental.{RemoteStorage,CreateOnShared,CreateOnSharedLocator}`
and a `CreatorID` persisted beside the local directory. Delete the flat
`objstore.Engine` and its commit journal (see "Shape chosen").
- *Acceptance:* (1) the existing engine-conformance suite passes with `pebble+remote` added
  as a parametrized backend, local-provider go-objstore; (2) a kill-process
  mid-compaction test shows no corruption and a clean reopen; (3) an
  encryption round-trip test with the barrier on and off (OS-4); (4) a
  provider-construction test asserting `glacier`/`azurearchive` are refused
  with a typed error before any SSTable write is attempted; (5) `gofmt -l`,
  `make vet-tags`, and a build-gate test that `pkg/storage/objstore` imports
  neither a qrdb key type nor anything key-shaped — only object names.

**Phase 2 — wire `backend: objstore` as a local binding.**
Remove the `database.InvalidBackendError` refusal at `openStorage` for
`backend: objstore`; route it through `pebble.WithRemote` like any other
local binding. Depends on `../databases/architecture.md`'s binding resolver
for the surrounding plumbing; this phase owns only the storage side.
- *Acceptance:* (1) `qrdb database create ... --backend objstore` round-trips
  every engine (KV, MVCC, transactions, secondary indexes, vector, time
  series) by exact key in that database's own bucket prefix; (2) a restart
  with the local directory intact (normal restart, not node loss) recovers
  with no data movement; (3) a test that removes *only* the local directory
  (simulating node loss) and documents — does not silently paper over — that
  reopening safely requires the fencing/ownership handoff from
  `../databases/`; this phase does not claim to solve that.

**Phase 3 — OS-7, gated, if accepted.**
Lift the hot-apply-path refusal for `raft.sm_storage: objstore`, behind the
existing cluster feature gate.
- *Acceptance:* a three-node Raft group on `raft.sm_storage: objstore`; kill one
  replica, replace it, confirm catch-up via `SaveSnapshot`/
  `RecoverFromSnapshot` without reading the dead replica's old prefix; confirm
  the dead prefix's objects are reclaimed; a compaction-under-load soak test.

**Phase 4 — deferred, not scheduled.**
Bucket-native fast backup/restore (OS-7's sibling: using Pebble's live-object
set and ref metadata instead of a full logical scan). No acceptance criteria
yet — explicitly future work, not blocking phases 1–3.

## Companion documents

- [../01-architecture.md](../01-architecture.md) — the system architecture overview, including the storage layer.
