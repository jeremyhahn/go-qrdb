<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Capabilities API

The `pkg/capabilities/` package is the runtime feature truth of a QRDB
process. Build-tagged features register themselves via `init()`; at boot the
composition root **seals** the effective profile — the intersection of build
tags, run mode, and configuration (`config.EffectiveCapabilities` →
`capabilities.Seal`) — and every transport reports it through the single
service funnel (`service.AdminService.Capabilities`). **A `false` value can
therefore mean not-compiled, mode-gated, or config-disabled.** Before a
composition root seals (library embeddings, tests), `Get` falls back to the
historical compile-time view: raft mode, core features true, tag features
per registration. See [docs/run-modes.md](../run-modes.md) for the
three-gate model (build tag > run mode > config).

## Package API

Source: `pkg/capabilities/capabilities.go`

```go
// Capabilities describes the effective feature set of the QRDB process.
type Capabilities struct {
    Mode        string `json:"mode"` // "standalone" | "raft"
    KV          bool   `json:"kv"`
    MVCC        bool   `json:"mvcc"`
    Transaction bool   `json:"transaction"`
    Batch       bool   `json:"batch"`
    Watch       bool   `json:"watch"`
    Lease       bool   `json:"lease"`
    Lock        bool   `json:"lock"`
    Index       bool   `json:"index"`
    Cluster     bool   `json:"cluster"`
    Backup      bool   `json:"backup"`
    Rebalance   bool   `json:"rebalance"`
    TimeSeries  bool   `json:"timeSeries"`
    Vector      bool   `json:"vector"`
    SQL         bool   `json:"sql"`
    MySQL       bool   `json:"mysql"`
    PostgreSQL  bool   `json:"postgresql"`
    Etcd        bool   `json:"etcd"`
    FTS         bool   `json:"fts"`

    // UnencryptedAtRest lists, when seal is enabled, where the node keeps
    // data on disk outside the barrier's at-rest encryption. Empty otherwise.
    UnencryptedAtRest []string `json:"unencryptedAtRest,omitempty"`
}

// Register records a feature as compiled in. Called from init() in
// build-tagged files.
func Register(feature string)

// Seal stores the effective profile computed by the composition root at
// boot (build tags ∩ run mode ∩ config). After Seal, Get returns exactly
// this profile.
func Seal(c Capabilities)

// Sealed reports whether a composition root has sealed the profile.
func Sealed() bool

// Get returns the effective profile (sealed), or the legacy compile-time
// fallback before sealing.
func Get() Capabilities

// Has reports whether the named feature has been registered ("compiled
// in?", not "effective?").
func Has(feature string) bool

// Reset clears registrations and any sealed profile (testing only).
func Reset()
```

## Feature Classification

| Feature | Type | Available |
|---------|------|-----------------|
| KV | Core | Always |
| Batch | Core | Always |
| Index | Core | Always |
| MVCC | Raft-only | mode `raft` (narrowable via `mvcc.enabled`) |
| Transaction | Raft-only | mode `raft` |
| Watch | Raft-only | mode `raft` (narrowable via `watch.enabled`) |
| Lease | Raft-only | mode `raft` (narrowable via `lease.enabled`) |
| Lock | Raft-only | mode `raft` |
| Cluster | Raft-only | mode `raft` |
| Backup | Raft-only | mode `raft` |
| Rebalance | Raft-only | mode `raft` |
| TimeSeries | Optional | No (requires `qrdb_ts` build tag) |
| Vector | Optional | No (requires `qrdb_vector` build tag) |
| SQL | Optional | No (requires `qrdb_sql` build tag) |
| MySQL | Optional | No (requires `qrdb_mysql` build tag, implies `qrdb_sql`) |
| PostgreSQL | Optional | No (requires `qrdb_pgsql` build tag, implies `qrdb_sql`) |
| Etcd | Optional + raft-only | No (requires `qrdb_etcd` build tag AND mode `raft`) |
| FTS | Optional | No (requires `qrdb_fts` build tag) |

## Build Tags

Optional features are gated by Go build constraints. Each feature has a dedicated source file containing only an `init()` function that calls `Register()`.

### TimeSeries

The `qrdb_ts` tag gates the time-series engine itself (`pkg/timeseries`
storage, aggregation, and the `TimeSeriesServicer` interface), not just the
CLI command tree and the capability flag. Builds without `qrdb_ts` compile
`pkg/timeseries/stub.go` instead: the pure data and wire types stay real so
untagged consumers keep compiling and marshaling identically, while every
storage operation returns `ErrNotCompiled` ("timeseries support not
compiled: build with -tags qrdb_ts"). The shared typed errors in
`pkg/timeseries/errors.go` remain in both builds.

The legacy bare `timeseries` build tag — previously on
`pkg/errors/timeseries_errors.go` and the
`pkg/statemachine/timeseries_{adapter,factory,statemachine}.go` trio — was
retagged to `qrdb_ts`. qrdb's own Makefile never set that tag; only
cross-repo workspace builds defining an unrelated `timeseries` feature tag
activated those files, and that accidental coupling is now gone.

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

### SQL

The `qrdb_sql` tag gates the shared `pkg/sql` core (go-mysql-server engine,
ad-hoc `/sql/query` service, in-process SQL driver). The MySQL and
PostgreSQL wire protocols are engines layered over it, so `qrdb_mysql` and
`qrdb_pgsql` builds always enable `qrdb_sql` as well (enforced by the
Makefile).

Source: `pkg/capabilities/capabilities_sql.go`

```go
//go:build qrdb_sql

package capabilities

func init() {
    Register("sql")
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
    Register("pgsql")
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

### FTS

Source: `pkg/capabilities/capabilities_fts.go`

```go
//go:build qrdb_fts

package capabilities

func init() {
    Register("fts")
}
```

### Makefile Integration

The Makefile exposes variables that control which build tags are included:

| Variable | Build Tag | Default |
|----------|-----------|---------|
| `WITH_QRDB_TS` | `qrdb_ts` | 1 |
| `WITH_QRDB_VECTOR` | `qrdb_vector` | 1 |
| `WITH_QRDB_SQL` | `qrdb_sql` | 1 |
| `WITH_QRDB_MYSQL` | `qrdb_mysql` | 1 |
| `WITH_QRDB_PGSQL` | `qrdb_pgsql` | 1 |
| `WITH_QRDB_ETCD` | `qrdb_etcd` | 1 |
| `WITH_QRDB_FTS` | `qrdb_fts` | 0 |

`WITH_QRDB_MYSQL=1` or `WITH_QRDB_PGSQL=1` forces `WITH_QRDB_SQL=1`: the
wire protocols cannot exist without the shared SQL core.

```bash
# Build with the default feature set (all optional features except FTS)
make build

# Build without any SQL support (drops go-mysql-server entirely)
make build WITH_QRDB_SQL=0 WITH_QRDB_MYSQL=0 WITH_QRDB_PGSQL=0

# Build with the ad-hoc SQL query service but no wire protocols
make build WITH_QRDB_MYSQL=0 WITH_QRDB_PGSQL=0

# Build with FTS support
make build WITH_QRDB_FTS=1
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
  "sql": true,
  "mysql": true,
  "postgresql": true,
  "etcd": true,
  "fts": false
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

- [api.md](../api-server/api.md) -- Full API endpoint reference
- [sdk/README.md](../api-server/engineering.md) -- Go SDK with capabilities client
- [cli/README.md](../api-server/cli.md) -- CLI command tree including `admin capabilities`
