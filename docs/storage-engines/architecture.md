# Storage Engines — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.
>
> Volume pools are documented in [../volume-pools/](../volume-pools/).

## Problem

The layer that decides *what history is kept* and the layer that decides *where
bytes land* have different lifetimes and different failure modes. Fusing them
makes every new backend re-implement revision handling, and makes tenant
isolation a per-backend concern rather than a composed one.

## Shape chosen

A narrow `Storage` interface with several implementations, and a wrapping engine
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

### The file engine: a hashed, flat, content-addressed layout

A non-empty directory with no format marker, or with a marker naming another
version, is refused at open. The layout derives no path from the
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

### The objstore engine: encoded object names

A non-empty store with no format marker, or with a marker naming another
version, is refused at open. A storage key is arbitrary bytes, but a
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

## Not built

An object-storage LSM segment layout for `objstore` (SPEC row
`object-storage-lsm`, `future`) is not implemented. The shipped `objstore`
engine stores one object per key, as described above.

## Companion documents

- [../01-architecture.md](../01-architecture.md) — the system architecture overview, including the storage layer.
