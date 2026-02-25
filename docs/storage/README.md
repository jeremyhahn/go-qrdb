<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Storage Engine Layer

go-qrdb provides the canonical storage engine layer used by both go-qrdb and go-xkms. The `pkg/storage/` package defines a unified `Engine` interface with multiple backend implementations.

## Engine Interface

```go
type Engine interface {
    Get(key string) ([]byte, error)
    Put(key string, data []byte) error
    Delete(key string) error
    List(prefix string) ([]string, error)
    Exists(key string) (bool, error)
    Close() error
}
```

This interface is also compatible with go-quicraft's `seal.StorageBackend` via Go duck typing — no explicit interface embedding required.

## Available Engines

| Engine | Package | Description |
|--------|---------|-------------|
| File | `pkg/storage/file/` | Key-to-path filesystem mapping for shard configs, metadata |
| Memory | `pkg/storage/memory/` | In-memory engine for testing and ephemeral data |
| PebbleDB | `pkg/storage/pebble/` | Extracted from Raft state machine, production-grade |
| Namespace | `pkg/storage/namespace/` | Tenant prefix isolation wrapping any engine |

## Configuration

```yaml
storage:
  engine: pebble    # file | memory | pebble | qrdb
  file:
    dir: /data/qrdb
  pebble:
    dir: /data/pebble
  qrdb:
    address: "localhost:63003"
    transport: grpc
```

## Namespace Isolation

The namespace engine wraps any `Engine` and auto-prefixes all keys with a tenant identifier:

```go
base := pebble.New(dir)
tenantEngine := namespace.New(base, "tenant-a")
// tenantEngine.Put("key", data) → base.Put("tenant-a/key", data)
// tenantEngine.List("") → filters to "tenant-a/" prefix only
```

## go-xkms Integration

go-xkms imports `go-qrdb/pkg/storage` for all storage operations. A thin QRDB networked adapter (`pkg/storage/qrdb/` in go-xkms) wraps the go-qrdb SDK client into the `Engine` interface for distributed mode.
