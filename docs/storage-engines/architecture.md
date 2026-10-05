# Storage Engines and Volume Pools — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| ObjStore | `pkg/storage/objstore/` | object-storage-backed, via go-objstore |
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

**On-disk layout changed twice in succession; no migration either time.** A
security and availability probe found that writing a key and then a `/`
prefix of that key, in either order, could kill the shard. The file engine
originally mapped each key to a single file, `/` bytes read as directory
levels, so a key that was a `/` prefix of another resolved under the exact
path that other key's parent directory needed. `SM.Apply` returning the
resulting rename/mkdir failure as a genuine storage error is fatal to
go-quicraft — `lastApplied` stops advancing on that replica and the shard
goes dark for every later linearizable read — and because the system
database lives on the system shard, that took authentication down with it.
PebbleDB and in-memory storage have no such path at all: two ordinary
byte-string keys, never a filesystem collision.

A first fix (one directory per key instead of one file per key) closed the
exact collision reported, but a follow-up review found two more ways the
same root cause — a path DERIVED FROM THE KEY — kept producing failure modes
of its own:

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
validity check, not migration code. A directory written by either previous
layout, or a future one this binary predates, is refused rather than
silently misread.

**Durability.** Every directory `MkdirAll` creates — the engine root and
each of a key's two shard levels — has its PARENT fsynced immediately after
creation, publishing the new directory entry; a record's temp-file,
fsync, atomic-rename sequence is unchanged from the previous layouts.

This is, again, a **breaking on-disk format change**. Per the project's
no-backward-compatibility policy there is no migration code: a data
directory written by either previous layout is not read by this one. Start
the replica over raft's restore or backup/restore path, or reseed
`sm_storage: file` from a backup, rather than upgrading the directory in
place.

**Operational note: keep `sm_storage` identical across a shard's
replicas.** Raft replicates the command log, not a storage representation,
so replicas composing different backends still apply the identical command
sequence and are not unsafe by construction — but nothing today verifies
this at cluster join, and a backend-specific defect (the exact shape this
section is about) is the kind of thing a mismatch would surface only
indirectly, through a `StateMachineHash` divergence. There is no
operational reason to mix it.

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
