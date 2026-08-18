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
type Engine interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
	Delete(key string) error
	List(prefix string) ([]string, error)
	Exists(key string) (bool, error)
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
| Namespace | `pkg/storage/namespace/` | tenant prefix isolation **wrapping any engine** |

The namespace engine is the important one architecturally: isolation is
composed, not reimplemented per backend.

```go
base := pebble.New(dir)
tenantEngine := namespace.New(base, "tenant-a")
// Put("key", data)  →  base.Put("tenant-a/key", data)
// List("")          →  filtered to the "tenant-a/" prefix
```

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
