<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Capabilities API

The `pkg/capabilities/` package provides compile-time feature registration that flows from build tags through the service layer to all API protocols, SDKs, and the frontend UI. Core features are always enabled; optional features register themselves via `init()` in build-tagged source files.

## Package API

Source: `pkg/capabilities/capabilities.go`

```go
// Capabilities describes the compile-time feature set of the QRDB binary.
type Capabilities struct {
    KV          bool `json:"kv"`
    MVCC        bool `json:"mvcc"`
    Transaction bool `json:"transaction"`
    Batch       bool `json:"batch"`
    Watch       bool `json:"watch"`
    Lease       bool `json:"lease"`
    Index       bool `json:"index"`
    TimeSeries  bool `json:"timeSeries"`
    Vector      bool `json:"vector"`
    MySQL       bool `json:"mysql"`
    PostgreSQL  bool `json:"postgresql"`
    Etcd        bool `json:"etcd"`
}

// Register records a feature as available. Called from init() in
// build-tagged files.
func Register(feature string)

// Get returns the current set of registered capabilities.
func Get() Capabilities

// Has reports whether the named feature has been registered.
func Has(feature string) bool

// Reset clears all registered features (testing only).
func Reset()
```

## Feature Classification

| Feature | Type | Always Available |
|---------|------|-----------------|
| KV | Core | Yes |
| MVCC | Core | Yes |
| Transaction | Core | Yes |
| Batch | Core | Yes |
| Watch | Core | Yes |
| Lease | Core | Yes |
| Index | Core | Yes |
| TimeSeries | Optional | No (requires `qrdb_ts` build tag) |
| Vector | Optional | No (requires `qrdb_vector` build tag) |
| MySQL | Optional | No (requires `qrdb_mysql` build tag) |
| PostgreSQL | Optional | No (requires `qrdb_pgsql` build tag) |
| Etcd | Optional | No (requires `qrdb_etcd` build tag) |

## Build Tags

Optional features are gated by Go build constraints. Each feature has a dedicated source file containing only an `init()` function that calls `Register()`.

### TimeSeries

Source: `pkg/capabilities/capabilities_ts.go`

```go
//go:build qrdb_ts

package capabilities

func init() {
    Register("timeSeries")
}
```

### Vector

Source: `pkg/capabilities/capabilities_vector.go`

```go
//go:build qrdb_vector

package capabilities

func init() {
    Register("vector")
}
```

### MySQL

Source: `pkg/capabilities/capabilities_mysql.go`

```go
//go:build qrdb_mysql

package capabilities

func init() {
    Register("mysql")
}
```

### PostgreSQL

Source: `pkg/capabilities/capabilities_pgsql.go`

```go
//go:build qrdb_pgsql

package capabilities

func init() {
    Register("postgresql")
}
```

### Etcd

Source: `pkg/capabilities/capabilities_etcd.go`

```go
//go:build qrdb_etcd

package capabilities

func init() {
    Register("etcd")
}
```

### Makefile Integration

The Makefile exposes variables that control which build tags are included:

| Variable | Build Tag | Default |
|----------|-----------|---------|
| `WITH_QRDB_TS` | `qrdb_ts` | unset |
| `WITH_QRDB_VECTOR` | `qrdb_vector` | unset |
| `WITH_QRDB_MYSQL` | `qrdb_mysql` | unset |
| `WITH_QRDB_PGSQL` | `qrdb_pgsql` | unset |
| `WITH_QRDB_ETCD` | `qrdb_etcd` | unset |

```bash
# Build with all optional features
make build WITH_QRDB_TS=1 WITH_QRDB_VECTOR=1 WITH_QRDB_MYSQL=1 WITH_QRDB_PGSQL=1 WITH_QRDB_ETCD=1

# Build with TimeSeries only
make build WITH_QRDB_TS=1

# Build with etcd protocol support
make build WITH_QRDB_ETCD=1

# Build with core features only (default)
make build
```

## Service Integration

The service layer exposes capabilities via the `AdminServicer` interface.

Source: `pkg/service/service.go`

```go
type AdminServicer interface {
    Capabilities(ctx context.Context) (*capabilities.Capabilities, error)
    // ...
}
```

## Protocol Endpoints

All API protocols surface a capabilities endpoint. The response is always the same `Capabilities` struct serialized according to the protocol format.

| Protocol | Endpoint | Method |
|----------|----------|--------|
| REST | `/v1/capabilities` | `GET` |
| gRPC | `Capabilities(CapabilitiesRequest)` | Unary RPC |
| MCP | `qrdb_capabilities` tool | Tool call |
| Unix | Same gRPC surface over Unix domain socket | Unary RPC |

### Example REST Response

```json
{
  "kv": true,
  "mvcc": true,
  "transaction": true,
  "batch": true,
  "watch": true,
  "lease": true,
  "index": true,
  "timeSeries": true,
  "vector": false,
  "mysql": true,
  "postgresql": true,
  "etcd": true
}
```

## SDK Usage

```go
import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

client, err := sdk.NewClient(
    sdk.WithTransport("grpc"),
    sdk.WithAddress("localhost:63003"),
)
defer client.Close()

caps, err := client.Admin().Capabilities(ctx)
if caps.TimeSeries {
    // TimeSeries operations available
}
```

## CLI Usage

```bash
qrdb admin capabilities
qrdb admin capabilities --output json
```

## Frontend Integration

The frontend queries capabilities on load and uses the response to dynamically show or hide navigation items and views. When `TimeSeries` is `false`, the time-series sidebar item and all associated views are hidden. The same applies to `Vector`.

## Data Flow

```
Build tag (qrdb_ts)
  → init() calls Register("timeSeries")
    → Get() returns Capabilities{TimeSeries: true}
      → AdminServicer.Capabilities()
        → REST / gRPC / MCP / Unix response
          → SDK client.Admin().Capabilities()
            → Frontend dynamic UI
```

## Thread Safety

The global feature registry is protected by a `sync.RWMutex`. `Register()` acquires a write lock; `Get()` and `Has()` acquire read locks. Registration occurs during `init()` before any concurrent access, so contention is effectively zero at runtime.

## See Also

- [api.md](../api.md) -- Full API endpoint reference
- [sdk/README.md](../sdk/README.md) -- Go SDK with capabilities client
- [cli/README.md](../cli/README.md) -- CLI command tree including `admin capabilities`
