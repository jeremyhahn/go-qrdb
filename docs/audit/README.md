<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Audit Logging

The `pkg/audit/` package provides configurable dual-backend audit logging
with support for internal KVStore database and/or filesystem JSON log
output. All security-relevant operations in QRDB emit structured audit
entries that record the who, what, where, and outcome of each action.

## Interface

Source: `pkg/audit/audit.go`

```go
type Logger interface {
    Log(ctx context.Context, entry *Entry) error
    Close() error
}
```

The `Logger` interface is the single abstraction consumed by all QRDB
subsystems. Callers construct an `Entry` and pass it to `Log()`. The
concrete implementation determines where the entry is persisted.

## Entry Structure

Source: `pkg/audit/types.go`

```go
type Entry struct {
    Timestamp time.Time         `json:"timestamp"`
    TenantID  string            `json:"tenantID"`
    Subject   string            `json:"subject"`
    Action    string            `json:"action"`
    Resource  string            `json:"resource"`
    Result    string            `json:"result"`
    Metadata  map[string]string `json:"metadata,omitempty"`
}
```

| Field       | Description                                             | Example                   |
|-------------|---------------------------------------------------------|---------------------------|
| `Timestamp` | UTC time of the event; auto-set to `time.Now()` if zero | `2026-02-26T12:00:00Z`   |
| `TenantID`  | Tenant that owns the resource                           | `default`                 |
| `Subject`   | Username extracted from the caller's identity           | `admin`                   |
| `Action`    | Dotted action identifier                                | `user.create`             |
| `Resource`  | Resource type and identifier                            | `user:operator`           |
| `Result`    | Outcome: `"success"` or `"denied"`                      | `success`                 |
| `Metadata`  | Optional key-value pairs with additional context        | `{"ip": "10.0.0.1"}`     |

Result constants are defined for consistency:

```go
const (
    ResultSuccess = "success"
    ResultDenied  = "denied"
)
```

## Implementations

### kvLogger

Source: `pkg/audit/kv_logger.go`

Writes entries as JSON-serialized values into the QRDB KVStore. The key
format uses a system prefix to isolate audit data from user keyspace:

```
__sys:audit/{tenantID}/{timestamp_nano}
```

The nanosecond timestamp ensures natural chronological ordering when
scanning the key prefix. The KVStore lifecycle is managed externally;
`Close()` is a no-op.

### fileLogger

Source: `pkg/audit/file_logger.go`

Uses Go's `log/slog` package with `slog.NewJSONHandler` to write
structured audit events to a log file. The file is opened with
`O_CREATE|O_APPEND|O_WRONLY` and mode `0600`. A mutex serializes
concurrent writes.

Output format (one JSON object per line):

```json
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit","tenant_id":"default","subject":"admin","action":"user.create","resource":"user:operator","result":"success","event_time":"2026-02-26T12:00:00Z"}
```

When entries include metadata, it appears as a nested JSON group:

```json
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit","tenant_id":"default","subject":"admin","action":"cert.revoke","resource":"cert:serial123","result":"success","event_time":"2026-02-26T12:00:00Z","metadata":{"reason":"key compromise"}}
```

### multiLogger

Source: `pkg/audit/multi_logger.go`

Fans out entries to multiple backends. The first error encountered is
captured, but remaining backends are still attempted so that a single
backend failure does not suppress the audit trail in healthy backends.
Both `Log()` and `Close()` follow this best-effort pattern.

### noopLogger

Source: `pkg/audit/noop.go`

Silently discards all entries. Returned when auditing is disabled or
when no backends are configured.

## Factory

Source: `pkg/audit/audit.go`

```go
func New(cfg Config, kvStore kvstore.KVStore) (Logger, error)
```

The factory inspects the configuration and constructs the appropriate
logger:

| Condition                             | Result                                    |
|---------------------------------------|-------------------------------------------|
| `cfg.Enabled == false`                | `noopLogger`                              |
| KV only (`KVBackend && !FileBackend`) | `kvLogger`                                |
| File only (`FileBackend && !KVBackend`)| `fileLogger`                             |
| Both backends enabled                 | `multiLogger` wrapping kv + file loggers  |
| No backends enabled                   | `noopLogger`                              |

When `KVBackend` is enabled, the `kvStore` parameter must be non-nil or
a `*ConfigError` is returned.

## Configuration

Source: `pkg/audit/types.go`, `pkg/config/config.go`

```go
type Config struct {
    Enabled     bool   `yaml:"enabled"`
    KVBackend   bool   `yaml:"kv_backend"`
    FileBackend bool   `yaml:"file_backend"`
    FilePath    string `yaml:"file_path"`
}
```

YAML reference:

```yaml
audit:
  enabled: true         # Master switch for audit logging
  kv_backend: false     # Store audit entries in the QRDB KVStore
  file_backend: true    # Write JSON audit log to a file
  file_path: ""         # Default: "audit.log" (relative to data dir)
```

Default configuration (`DefaultConfig()`):

| Field         | Default      | Description                               |
|---------------|-------------|-------------------------------------------|
| `Enabled`     | `true`      | Audit logging is enabled by default       |
| `KVBackend`   | `false`     | KVStore backend disabled by default       |
| `FileBackend` | `true`      | File backend enabled by default           |
| `FilePath`    | `"audit.log"` | Relative path resolved against data dir |

## Audited Events

The following operations emit audit entries across the QRDB subsystems:

| Action               | Resource Format       | Emitted By                     |
|---------------------|-----------------------|--------------------------------|
| `user.create`       | `user:{username}`     | User management                |
| `user.update`       | `user:{username}`     | User management                |
| `user.delete`       | `user:{username}`     | User management                |
| `user.enable`       | `user:{username}`     | User management                |
| `user.disable`      | `user:{username}`     | User management                |
| `role.create`       | `role:{name}`         | Role management                |
| `role.update`       | `role:{name}`         | Role management                |
| `role.delete`       | `role:{name}`         | Role management                |
| `role.assign`       | `role:{name}`         | Role management                |
| `role.unassign`     | `role:{name}`         | Role management                |
| `cert.generate`     | `cert:{serial}`       | Certificate management         |
| `cert.revoke`       | `cert:{serial}`       | Certificate management         |
| `seal.initialize`   | `barrier`             | Seal subsystem                 |
| `seal.unseal`       | `barrier`             | Seal subsystem                 |
| `seal.seal`         | `barrier`             | Seal subsystem                 |
| `seal.rotate`       | `barrier`             | Seal subsystem                 |
| `seal.rekey`        | `barrier`             | Seal subsystem                 |
| `admin.drain`       | `node:{id}`           | Admin operations               |
| `admin.snapshot`    | `shard:{id}`          | Admin operations               |
| `admin.member.add`  | `shard:{id}`          | Membership management          |
| `admin.member.remove` | `shard:{id}`        | Membership management          |
| `auth.denied`       | `{resource}`          | Authentication middleware      |

Permission-denied events use the `ResultDenied` result value and are
always logged regardless of backend selection to maintain a complete
security audit trail.

## Error Types

Source: `pkg/audit/errors.go`

| Error Type      | Returned When                                       | Unwrap |
|-----------------|-----------------------------------------------------|--------|
| `*WriteError`   | Writing an entry to a backend fails                 | Yes    |
| `*FileError`    | The audit log file cannot be opened or created      | Yes    |
| `*ConfigError`  | Configuration is invalid (e.g. nil kvStore)         | No     |

All error types implement `error`. `WriteError` and `FileError` also
implement `Unwrap()` for use with `errors.Is` and `errors.As`.

```go
type WriteError struct {
    Backend string  // "kv" or "file"
    Err     error
}

type FileError struct {
    Path string
    Err  error
}

type ConfigError struct {
    Detail string
}
```

## Cross-References

- [Configuration Reference](../12-configuration.md) -- Complete YAML reference
- [Seal Package](../seal/README.md) -- Barrier audit events
- [Authentication](../auth/README.md) -- Identity extraction for Subject field
- [Architecture](../01-architecture.md) -- System overview and package structure
