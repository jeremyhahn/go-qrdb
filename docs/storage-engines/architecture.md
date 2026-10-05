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
| File | `pkg/storage/file/` | key-to-path filesystem mapping; shard configs, metadata |
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

### The file engine: one directory per key, not one file per key (BREAKING)

**On-disk layout changed; no migration.** A security and availability probe
found that writing a key and then a `/` prefix of that key, in either order,
could kill the shard. The file engine used
to map each key to a single file: '/' bytes delimited directory levels, so a
key that was a '/' prefix of another key resolved under the SAME path as that
other key's parent directory. Writing `dir/child` and then `dir` is Set
colliding with a directory it had itself just created, and PebbleDB and
in-memory storage have no such path at all — two ordinary byte-string keys,
never a filesystem collision. The failure mode this produced was `SM.Apply`
returning a genuine (if misleading) error from the storage layer: go-quicraft
treats any non-nil `Update` error as fatal and halts `lastApplied` on that
replica, so an ordinary pair of client writes could take the whole shard down
with it. Because the system database lives on the system shard, that included
authentication.

The fix changes WHAT a key maps to, not whether '/' is read as a hierarchy:
every key maps to a **directory**, and its value lives in a sentinel file
(`.kv`) inside that directory. A key and every key for which it is a '/'
prefix each get their own directory and their own `.kv`; one directory
holding both its own sentinel file and a subdirectory for a longer key is
ordinary filesystem structure, not a collision. `pkg/storage/file/keyenc.go`
also closes a second, narrower gap in the same codec: a leading, trailing, or
doubled '/' in a key used to produce an empty path segment that
`filepath.Clean` silently collapsed into its neighbour (`"a/"` and `"a"` named
the same directory); those three cases are now escaped rather than read as
separators, so the key-to-path mapping is a true bijection over every byte
string, not only over "ordinarily shaped" ones.

This is a **breaking on-disk format change**. Per the project's
no-backward-compatibility policy there is no migration code: a data directory
written by the old layout is not read by the new one. Start the replica over
raft's restore or backup/restore path, or reseed `sm_storage: file` from a
backup, rather than upgrading the directory in place.

A key whose encoded path would contain a '/'-delimited segment this
filesystem cannot name as one path component (NAME_MAX, 255 bytes on every
filesystem this engine targets) is refused with
`file.KeySegmentTooLongError` — a declared refusal (see
[key-value/engineering.md](../key-value/engineering.md#refusal-and-fault-at-the-apply-seam)),
never a storage fault, so it cannot halt Apply either. This is scoped to the
file backend alone: PebbleDB and in-memory storage have no path-component
limit, so the same key that is refused under `sm_storage: file` is accepted
under the `pebble` default.

Deleting a key removes only its `.kv` sentinel; the directory itself is left
in place even when nothing else remains in it. This is deliberate, not an
oversight: `storage.Storage` carries no promise that `Set` and `Delete` on
DIFFERENT keys are serialized against each other, and the `fast` state
machine's write-through issues them from concurrent per-shard workers,
ordered only per key. Removing a directory on the strength of one
point-in-time "is it empty" check would race a concurrent `Set` of a key for
which the deleted one is a '/' prefix, between that check and the `Set`'s own
`MkdirAll`. A harmless leftover empty directory is the tradeoff for not
having that race at all.

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
