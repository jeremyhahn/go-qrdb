# Storage Engines and Volume Pools — Architecture

> **Status: NORMATIVE for shipped behaviour; PROPOSED — pending owner ruling —
> for the object-storage LSM target design.** · Scope: go-qrdb · Last verified:
> 2026-10-06
>
> This document has two layers.
>
> - **Current state** sections (everything through "What this feature
>   deliberately does not do") describe the code as shipped. They are
>   unchanged by the proposal below.
> - **Target design: object-storage LSM segment layout** is a design contract
>   for how `pkg/storage/objstore` is rebuilt. The owner has ruled that object
>   storage moves to an LSM segment layout and rejected the one-object-per-key
>   model; the open questions the ruling did not settle are listed, each with a
>   recommendation, in "Decisions needing the owner" inside that section.
>   **Nothing in that section is implemented.**

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
| Namespace | `pkg/storage/namespace/` | tenant prefix isolation **wrapping any engine** |

The namespace engine is the important one architecturally: isolation is
composed, not reimplemented per backend.

```go
base := pebble.New(dir)
tenantEngine := namespace.New(base, "tenant-a")
// Set("key", data)  →  base.Set("tenant-a/key", data)
// List(nil)         →  filtered to the "tenant-a/" prefix
```

### The file engine: a hashed, flat, content-addressed layout (BREAKING)

**On-disk layout changed; no migration.** A security and availability probe
found that writing a key and then a `/` prefix of that key, in either
order, could kill the shard. The file engine originally mapped each key to
a single file, `/` bytes read as directory levels, so a key that was a `/`
prefix of another resolved under the exact path that other key's parent
directory needed. `SM.Apply` returning the resulting rename/mkdir failure
as a genuine storage error is fatal to go-quicraft — `lastApplied` stops
advancing on that replica and the shard goes dark for every later
linearizable read — and because the system database lives on the system
shard, that took authentication down with it. PebbleDB and in-memory
storage have no such path at all: two ordinary byte-string keys, never a
filesystem collision.

This is the **second** layout the file engine has ever had, and the
**only** layout change that has shipped. An intermediate design (one
directory per key instead of one file per key) closed the exact collision
the probe reported, but review of it, within this same unreleased change
and before any release carried it, found two more ways the same root
cause — a path DERIVED FROM THE KEY — kept producing failure modes of its
own, so it was replaced before shipping rather than fixed a third time:

- **B1: an adversarially deep key exceeds `PATH_MAX`.** A key built from
  thousands of short `/`-delimited segments (e.g. `strings.Repeat("a/",
  2100)+"a"`) produced an absolute path longer than the kernel's path-length
  limit, so `os.MkdirAll` returned `ENAMETOOLONG` — the identical fatal-Apply
  shape as the original bug, from a different cause.
- **B2/B3: a refused op inside a batch left the commit journal published
  before the refusal was discovered,** so recovery replayed the SAME refusal
  on every future `Open` and failed it permanently, and a refused batch or
  transaction could partially apply before the refusal was found partway
  through.

The current layout removes the dependency on the key's SHAPE entirely rather
than patching a third edge case into it. Each key's record is named by the
**hex SHA-256 digest of the key** (stdlib `crypto/sha256`; no hand-rolled
hashing), sharded two levels deep by the first four hex characters (two
characters per level: 256 subdirectories per level, 65,536 leaf directories
in total — a small, FIXED number regardless of keyspace size). The record
file holds `[keyLen:4 BE][key][value]`: the real key travels with its value,
so a read can verify the stored key matches the one asked for, and a listing
can recover every key without a separate index.

This gives every key the same properties, independent of its length or
content:

- **Constant path length and depth.** No key, however deep or long, can
  produce `ENAMETOOLONG` or exceed a single filesystem's `NAME_MAX` — there
  is no segment derived from the key's bytes at all, which is also why
  `KeySegmentTooLongError` (an interim measure from the one-directory-per-key
  layout) no longer exists; there is nothing left for it to refuse.
- **No collisions, no prefix relationships, no case or Unicode
  ambiguity.** SHA-256 collisions are not a practical concern, and a key is
  never a filesystem-meaningful prefix of another the way `/` bytes used to
  make one, because `/` is not read as a path separator at all anymore.
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
  to recover its key — unlike the previous layouts, where a keys-only scan
  never opened a file at all. That is the deliberate tradeoff of a flat,
  content-addressed layout.

**Format version marker.** `New` writes a marker (`.qrdb-file-format`,
naming the current format version) into a directory it finds empty, and
refuses to open a NON-empty directory that lacks one or names a different
version (`FormatMarkerMissingError`, `FormatVersionMismatchError`) — a
validity check, not migration code. A directory written by the shipped
one-file-per-key layout, the never-shipped one-directory-per-key design, or
a future format this binary predates, is refused rather than silently
misread. "Non-empty" ignores `lost+found`: a filesystem's own mount-root
directory, never written by any layout this engine has had, so a mount
root that holds nothing else is a fresh mount, not unmarked legacy data.

**Durability.** Every directory `MkdirAll` creates — the engine root and
each of a key's two shard levels — has its PARENT fsynced immediately after
creation, publishing the new directory entry; a record's temp-file,
fsync, atomic-rename sequence is unchanged from the previous layout. A
concurrent `Set` that loses the race to create a shard directory a sibling
key just created, or that simply finds it already there, fsyncs the parent
itself before proceeding rather than trust that the creator's own fsync has
already landed — the two are unordered with respect to each other.

This is a **breaking on-disk format change**, and it is wider than
`raft.sm_storage: file` (`sm_type: disk`/`fast`) alone: `file.New` also
backs
[database's `BackendFile` data plane](../databases/architecture.md) and
every database's barrier-metadata store regardless of that database's OWN
configured backend (`pkg/database/manager.go`'s `openBarrierMetaBackend` —
deliberately backend-independent, since the metadata must survive a
restart even for an in-memory database), partitions
(`pkg/storage/partition`'s `FileFactory`), and the repository `qrdbd
restore --backup-dir` reads from. Every one of these opens a data
directory through the identical `file.New`, so every one is refused
in-place identically, and every one needs the identical remedy. Per the
project's no-backward-compatibility policy there is no migration code: a
data directory written by the previous (shipped) layout is not read by
this one. Start the replica over raft's restore or backup/restore path, or
reseed `sm_storage: file` from a backup, rather than upgrading the
directory in place.

**A backup taken before this change cannot be restored by a binary that
carries it.** A `file`-backed backup's bytes ARE the old layout; a binary
built with this change opens them, like any other `file.New` target, as a
data directory in the NEW layout and refuses them
(`FormatMarkerMissingError`) rather than silently misreading one-file-
per-key records as hashed ones. There is no cross-version restore path for
`sm_storage: file`/`BackendFile` backups by design (no-backward-
compatibility policy): a backup made before this change must be restored
by a binary from before this change, or not at all. See
[docs/backup-restore/user.md](../backup-restore/user.md).

**Operational note: keep `sm_storage` identical across a shard's
replicas.** Raft replicates the command log, not a storage representation,
so replicas composing different backends still apply the identical command
sequence and are not unsafe by construction — but nothing today verifies
this at cluster join, and a backend-specific defect (the exact shape this
section is about) is the kind of thing a mismatch would surface only
indirectly, through a `StateMachineHash` divergence. There is no
operational reason to mix it.

### The objstore engine: encoded object names (BREAKING)

**Object layout changed; no migration.** The objstore engine used to name
each object with its key verbatim. A storage key is arbitrary bytes, but a
go-objstore provider accepts only a UTF-8 object name of at most 1,024
bytes, free of NUL, control characters, `..` and `//`. A filesystem provider
also limits each path component to 255 bytes. Under the verbatim layout,
a binary key, a key carrying any of those sequences, or a key past those
lengths was refused by the provider, and a key such as `a/` named a
directory, not an object. A key the rest of qrdb accepts — anything up to
the global 65,535-byte cap (`protocol.MaxClientKeyLength`) — could not be
stored.

Every key is now stored under a name derived from it, in one of two forms:

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
written under the verbatim layout is therefore refused at open, not read
with its keys silently missing. Per the no-backward-compatibility policy
there is no migration code. Move the data by reading it out through a
binary from before this change and writing it in through one that carries
it, or start the store empty. The store must be dedicated to the engine:
any object the engine did not write makes an unmarked store non-empty.

### Alternatives rejected

- **One engine.** Rejected: memory is required for tests to be fast and
  hermetic; file is required where a real filesystem layout matters; Pebble is
  required for production write throughput.
- **Per-backend tenant isolation.** Rejected: it would be reimplemented three
  times and get subtly different each time. The namespace wrapper makes it one
  implementation over all of them.
- **A fatter interface.** Rejected: six methods is what a backend must provide.
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
  return. Improvisation here is what produced a backend-specific crash on one
  engine and a silently-wrong empty scan on two others.
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
- **Let each consumer match the panic text.** Rejected outright: it is what the
  absence of this contract forced on a consumer, and it turns a reworded
  message in a third-party dependency into a silent crash.

## Volume pools

`pkg/storage/pool` is a thin adapter over **go-objstore**'s pool primitive,
providing storage volumes for databases and tenants that need a dedicated store.

**Volume pools are deliberately local-only** — the manager is wrapped in
`objpool.LocalOnly`. The consumer is PebbleDB, which needs a real filesystem
path; a remote object-store backend cannot provide one. **This is a documented
invariant, not debt.** Do not "fix" it by removing the wrapper.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `Engine` interface and the file/memory/pebble engines | `pkg/storage` | go-qrdb |
| Namespace (tenant prefix) wrapper | `pkg/storage/namespace` | go-qrdb |
| Volume pool adapter, state stores | `pkg/storage/pool` | go-qrdb |
| Object-storage backends (local, memory, S3, GCS, Azure, MinIO, Glacier) and the pool primitive | — | **go-objstore** |
| `seal.StorageBackend` shape | — | **go-quicraft** (satisfied by duck typing) |
| State machines composed over these | `pkg/statemachine`, `pkg/engine` | go-qrdb |

## Consumers outside this repo

**go-xkms imports `go-qrdb/pkg/storage` for all storage operations.** A thin
networked adapter in go-xkms (`pkg/storage/qrdb/` there) wraps the go-qrdb SDK
client into the `Engine` interface for distributed mode. That makes the
`Engine` signature a cross-repo contract: changing it is a breaking change for
go-xkms, not only for this repo.

## Trust boundaries

- **Tenant isolation is the namespace wrapper's job**, applied by composition.
  An engine handed to a tenant-scoped consumer is already wrapped; the raw
  engine is not tenant-safe and is not meant to be.
- **At-rest encryption is the barrier's job**, above this layer — see
  [../barrier-seal/](../barrier-seal/). Engines store what they are given.
- Volume pool paths are filesystem paths on the node; a pool registration is a
  privileged operation.

## What this feature deliberately does not do

- **No revision history.** That belongs to the MVCC state machine — see
  [../key-value/](../key-value/architecture.md).
- **No encryption.** The barrier sits above.
- **No remote volume pools.** `LocalOnly` is deliberate.
- **No cloud object-storage backends of its own.** Those are go-objstore's, and
  are build-tag gated (`awss3`, `minio`, `gcpstorage`, `azureblob`, `glacier`,
  `azurearchive`).

## Target design: object-storage LSM segment layout

### Problem with the shipped `objstore` engine

`pkg/storage/objstore` today puts one object per key, named after the key
itself (see `Persistence` in [engineering.md](engineering.md)). This fails two
ways go-objstore's own validator and the measured ceiling on its "local"
provider make unavoidable:

- **Correctness.** go-objstore's `common.ValidateKey` refuses a NUL byte, a
  leading `/`, `..`, and non-UTF-8 bytes. qrdb's own keys routinely contain all
  of these — the lease table prefixes every key with `0x00`
  (`pkg/lease.TablePrefix`); MVCC, row-lock and secondary-index keys embed
  big-endian integers and arbitrary caller bytes. A key-as-object-name engine
  fails outright on such keys.
- **Scale.** Even a hex-encoding fix (explored and rejected — see "Alternatives
  rejected" below) tops out at a 96-byte raw key on the "local" provider,
  because the encoded name is a flat filename plus a `.metadata.json` sidecar
  under the filesystem's `NAME_MAX` (255 bytes), and every write is one PUT —
  so write and listing cost scale with key count, not with data volume.

### Ruling this design implements

**Owner ruling (final, not reopened here): object storage moves to an LSM
segment layout.** Keys live inside sorted, immutable segment files; object
names are short, opaque segment identifiers that carry no key material. The
one-object-per-key model, including the hex-encoding workaround explored on
`lane/objstore-binary-keys`, is rejected.

### Shape chosen

**Do not write an LSM from scratch. Compose Pebble v2's remote-storage support
over go-objstore.** qrdb already depends on `github.com/cockroachdb/pebble/v2`
(v2.1.4) for the `pebble` engine. Pebble v2 can keep its sorted SSTables on an
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
path separators. **This retires the entire key-length problem**: it is not
narrowed, it does not apply, because segment names never derive from qrdb
keys. See "Decisions needing the owner" (OS-5) for the disposition of the
hex-encoding branch this replaces.

**The new `pkg/storage/objstore` package stops being a KV engine and becomes a
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
construction rather than by re-implementation: `pkg/storage/pebble.Engine` is
already composed by `MVCCEngine`, secondary indexes, time series and vector
storage. An `objstore`-backed database becomes a `*pebble.DB` whose SSTables
happen to live in a bucket; nothing above `storage.Storage` changes, and
nothing needs to be re-taught to a second KV implementation. The old flat
`Engine`/`Batch`/`Iterator` in `pkg/storage/objstore` and the planned
open-time-replay commit journal for it
(`docs/databases/engineering.md`, Lane 1A) are both dropped: Pebble's own WAL
and manifest already give this backend the same atomic-batch and crash-replay
guarantees the `pebble` backend has today, for the same reason — it **is**
the `pebble` backend, with one storage option changed.

Composition, conceptually:

```
                     +-------------------------------+
                     |   pkg/storage/pebble.Engine    |   storage.Storage
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
                          (local | s3 | gcs | azure | minio)
```

### Decisions needing the owner

Eight questions were asked of this design; each is answered with a
recommendation below. None of these reopen the owner's LSM ruling — they are
what implementing it requires deciding.

**OS-1 — Is Pebble v2.1.4's remote storage a stable, supported surface?**
No — it is real, functional and already used in production by CockroachDB, but
every knob lives under `pebble.Options.Experimental`
(`RemoteStorage`, `CreateOnShared`, `CreateOnSharedLocator`,
`SecondaryCacheSizeBytes`), which is Pebble's own naming for "no API
stability promise." Two concrete caveats: (1) it requires
`FormatMajorVersion >= FormatMinForSharedObjects` (`= FormatVirtualSSTables`);
Pebble's `Options.EnsureDefaults` bumps the format version for us
automatically the moment `CreateOnShared != CreateOnSharedNone` is set
(`options.go:1495-1498`), so this needs no manual version pinning on our side,
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
- A `raft.sm_storage` / `raft.state_storage: objstore` database (hot apply
  path): the Raft log is the durability backstop beyond `lastApplied`, exactly
  as for `sm_storage: pebble` today. The local WAL can be small and the
  replica's own recovery already depends on replaying from the Raft log past
  whatever the local WAL lost — this is not a new property this design
  introduces.
- A `backend: objstore` database (today's "local", non-replicated binding —
  see `../databases/architecture.md`): there is no replicated log underneath
  it. The local WAL **must** be durable, on a real local disk, exactly like
  `backend: pebble` today. Volume pools already enforce this
  (`objpool.LocalOnly`) for the `pebble` engine's data directory; the same
  pool now also roots the WAL/MANIFEST/block-cache directory for an
  `objstore`-backed local binding. **Nothing here is optional or remote.**
**Recommendation:** local, durable WAL + MANIFEST in both modes, from the
existing local-only volume pool. Only the SSTables move to the bucket.

**OS-3 — Ownership: per-replica prefix, or shared across replicas?**
qrdb's Raft model has every replica apply independently to its own state
machine and its own storage (`../key-value/architecture.md` — "replication is
the host's property, not the engine's"). CockroachDB's own shared-storage work
goes further (replicas sharing one physical SSTable set, follower reads served
from shared storage without a local copy) — that changes *what a replica is*,
which is out of scope for a storage-engine lane and conflicts with nothing
this ruling asked for.
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
existing barrier wiring does not reach remote SSTables, full stop; this is not
covered by "engines store what they are given" today because the engine has
no encrypting layer on this path yet.**
- A: go-objstore has its own `Encrypter`/`EncryptedStorage` (`pkg/common`).
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

**OS-5 — The raw-key-length ceiling is retired, not narrowed.**
Confirmed by reading Pebble's object-naming code: `remoteObjectName` never
takes a qrdb key as input. Every key-length concern in
`lane/objstore-binary-keys` (go-objstore's `MaxKeyLength` of 1024 bytes, the
255-byte path-segment ceiling, the 96-byte measured "local"-provider floor
after hex-doubling) was a property of the rejected one-object-per-key model.
**Recommendation:** treat `lane/objstore-binary-keys` as superseded. It should
not be merged; its analysis is preserved by reference in this document rather
than by keeping the branch alive.

**OS-6 — go-objstore has no ranged read.**
`common.Storage.Get`/`GetWithContext` return a whole-object `io.ReadCloser`;
there is no `ReadAt`/byte-range method. Pebble's `remote.ObjectReader.ReadAt`
expects concurrent, partial, offset reads of potentially large SSTables — the
pattern every mainstream object-store API (S3, GCS, Azure, MinIO) supports
natively via HTTP Range, and every local filesystem supports via
`os.File.ReadAt`. go-objstore's own interface does not expose it.
**Recommendation:** file a Gitea issue on `go-objstore` (this repo's agent
does not edit that repo directly — ownership boundary) requesting a ranged
read method on `common.Storage`. Until it lands, `pkg/storage/objstore`'s
`ReadObject` fetches the whole object once per Pebble open (not per `ReadAt`
call — cached locally, keyed by object name) and serves `ReadAt` from the
cached bytes; this is a performance caveat for cold reads of large SSTables,
not a correctness one, and Pebble's own `SecondaryCacheSizeBytes` block cache
already absorbs the common case (repeated reads of the same block range).

**OS-7 — Lifting "objstore is refused as a hot apply path."**
Today's `storage-engines/engineering.md` table refuses objstore as
`raft.sm_storage` outright. With Pebble's native atomic batch and WAL, that
refusal's original reason (no atomic multi-key commit on the old flat engine)
no longer holds for the *local-binding* case. Whether to also allow it as a
Raft `sm_storage`/`state_storage` choice is a separate question with its own
blast radius (every replica now writes to a bucket under load, under
compaction, under leader failover) and is **not** part of phase 1 of this
design.
**Recommendation:** lift the refusal only for `backend: objstore` **local**
(non-raft) bindings in phase 1 (see "Phased implementation plan"). Leave the
Raft hot-apply-path refusal in place until a phase 2 that is scoped,
acceptance-tested under compaction and leader-failover load, and — because it
changes what a replica durably writes where — proposed into a group the same
way this program gates every new Raft-visible behavior (cluster feature gate,
per the existing ruling in `../databases/architecture.md`).

### Alternatives considered

- **Hex-encode keys, keep one object per key** (`lane/objstore-binary-keys`).
  Rejected by the owner ruling this document implements, and superseded by
  OS-5 above: it fixes correctness at a 96-byte ceiling and does nothing for
  write or listing cost, which scale with key count regardless of encoding.
- **Emulate atomic batches by ordering writes on the flat engine**, as
  `../databases/architecture.md` had proposed for the old `objstore` engine.
  Moot: Pebble's own WAL gives the LSM design atomic batches for free: nothing
  to emulate.
- **A different pure-Go LSM-on-object-storage library.** Surveyed and none
  found at comparable maturity: the closest prior art (e.g. SlateDB) is Rust,
  not Go, and RocksDB-Cloud is C++. Pebble is already a qrdb dependency,
  already production-hardened as the `pebble` engine, and its remote-storage
  support is exercised in production by CockroachDB. No alternative library
  clears that bar.
- **CockroachDB-style disaggregated storage** (replicas sharing one physical
  SSTable set; followers serve reads from shared storage without their own
  local copy). Rejected for this program: it changes the Raft apply model
  itself (replicas stop being independent appliers), which is a materially
  larger change than a storage engine, and is not what "every engine runs on
  every backend" or "no node-local logical state" asked for — see OS-3.
- **An in-house LSM.** Not pursued — flagged in the brief as the last resort,
  and nothing here forces it: Pebble's remote storage reaches the ruling's
  target shape (sorted segments, short opaque segment names) without writing
  one.

### Recovery and backup, briefly

- **Node loss, `backend: objstore` (single owner, local binding).** The
  bucket already holds every durable SSTable; only the local WAL/MANIFEST are
  lost with the node. Reopening on a replacement node is cheap (no data
  re-transfer, just MANIFEST + WAL recovery) **provided the old owner is
  fenced before the new one opens the same prefix** — that fencing and
  ownership handoff is `../databases/architecture.md`'s placement/catalog
  design (OD-2, OD-4; "Lane 2B" there), not something this storage engine
  invents. This design depends on that mechanism; it does not duplicate it.
- **Node loss, a Raft replica (if OS-7 is later accepted).** Unchanged from
  today's `sm_storage: pebble`: a replacement replica gets a fresh local
  directory and its own new prefix/`CreatorID`, and is caught up through the
  existing `SaveSnapshot`/`RecoverFromSnapshot` path
  (`pkg/statemachine/*.go`). The dead replica's old prefix is simply
  abandoned and reclaimed — consistent with OS-3's no-sharing answer.
- **Backup and restore stay on the existing logical path, unchanged.**
  `../backup-restore/architecture.md`'s shard snapshots already iterate
  through `storage.Reader` to a destination that can itself be a go-objstore
  URI — that is backend-agnostic today and stays so. A bucket-native fast
  backup (copying live SSTable objects directly, using Pebble's own
  ref-tracking metadata instead of re-reading every key) is a plausible
  future optimization but is explicitly **not** part of this design: it is
  not needed for correctness, and backup/restore stays simple per the
  owner's constraint.

### Provider coverage

Every go-objstore provider that supports fast, random reads is viable:
`local`, `memory`, `s3`, `gcs`, `azure`, `minio` — reachable at Pebble object
names of ~24 bytes, nowhere near any provider's key-length ceiling (OS-5).
**`glacier` and `azurearchive` are archive-only tiers with retrieval delays
measured in hours** (go-objstore registers them only as `Archiver`, never as
`common.Storage` — see `pkg/factory/factory_coverage_gaps_test.go`): they
cannot serve `ReadObject` at the latency an LSM read path needs and must stay
what they already are — backup/archive destinations reached through
`../backup-restore/`, never a live `objstore` provider.

### Component boundaries (target)

| Piece | Package | Owner |
|---|---|---|
| `pebble.Engine`, `storage.Storage`, `Batch`, `Iterator`, `Snapshot` | `pkg/storage/pebble` | go-qrdb (unchanged) |
| `remote.Storage` adapter over go-objstore, encrypting wrapper (OS-4) | `pkg/storage/objstore` | go-qrdb (rewritten) |
| `common.Storage` providers (local, s3, gcs, azure, minio) and their build tags | — | **go-objstore** |
| Ranged/offset reads on `common.Storage` (OS-6, not yet available) | — | **go-objstore** — tracked by a Gitea issue there, not implemented here |
| `remote.Storage`, `objstorage.CreatorID`, `Options.Experimental.RemoteStorage` | — | **Pebble v2** (`cockroachdb/pebble/v2`) |
| Fencing and ownership handoff for a single-owner local binding | `pkg/database`, `pkg/placement` | go-qrdb (`../databases/` design, depended on, not owned here) |

### Phased implementation plan

**Phase 1 — the adapter and the engine option.**
Rewrite `pkg/storage/objstore` as the `remote.Storage` adapter (OS-6's interim
whole-object cache) plus the encrypting wrapper (OS-4). Add
`pebble.WithRemote(...)` to `pkg/storage/pebble`, wiring
`Options.Experimental.{RemoteStorage,CreateOnShared,CreateOnSharedLocator}`
and a `CreatorID` persisted beside the local directory. Delete the old flat
`Engine`/`Batch`/`Iterator`/`Snapshot` in `pkg/storage/objstore`; drop the
commit-journal item for `objstore` from `../databases/engineering.md` Lane 1A
(superseded — see "Shape chosen").
- *Acceptance:* (1) the existing engine-conformance suite
  (`../databases/engineering.md` Lane 1A) passes with `pebble+remote` added
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
(its own Lane 2A) for the surrounding plumbing; this lane owns only the
storage side.
- *Acceptance:* (1) `qrdb database create ... --backend objstore` round-trips
  every engine (KV, MVCC, transactions, secondary indexes, vector, time
  series) by exact key in that database's own bucket prefix; (2) a restart
  with the local directory intact (normal restart, not node loss) recovers
  with no data movement; (3) a test that removes *only* the local directory
  (simulating node loss) and documents — does not silently paper over — that
  reopening safely requires the fencing/ownership handoff from
  `../databases/` Lane 2B; this lane does not claim to solve that.

**Phase 3 — OS-7, gated, if the owner accepts it.**
Lift the hot-apply-path refusal for `raft.sm_storage`/`state_storage:
objstore`, behind the existing cluster feature gate.
- *Acceptance:* a three-node Raft group on `state_storage: objstore`; kill one
  replica, replace it, confirm catch-up via `SaveSnapshot`/
  `RecoverFromSnapshot` without reading the dead replica's old prefix; confirm
  the dead prefix's objects are reclaimed; a compaction-under-load soak test.

**Phase 4 — deferred, not scheduled.**
Bucket-native fast backup/restore (OS-7's sibling: using Pebble's live-object
set and ref metadata instead of a full logical scan). No acceptance criteria
yet — explicitly future work, not blocking phases 1–3.
