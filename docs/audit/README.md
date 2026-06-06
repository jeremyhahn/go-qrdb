<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Audit Package — Compliance-Grade Event Logging & Tamper Evidence

The `pkg/audit/` package provides compliance-grade structured event logging with tamper-detection. Every barrier operation, ceremony step, RBAC decision, key operation, and tenant change is logged with actor, resource, action, and outcome. Audit trail is append-only, hash-chained for integrity (SHA-256 per event), and queryable via REST/gRPC/MCP/QUIC/Unix protocols.

All security-relevant operations in QRDB emit structured audit entries that record the who, what, where, and outcome of each action. Phase 1 extends the existing Entry schema with hash-chain tamper evidence and phase 6 adds query API, retention policy, and multi-protocol retrieval.

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

## Event Schema (Phase 6 Target)

Source: `pkg/audit/types.go`

Phase 1 documents the target schema for full compliance-grade audit events. Phased implementation extends existing `Entry` with new fields:

```go
type AuditEvent struct {
    // Timestamp of the event (UTC).
    Timestamp       time.Time

    // Actor identity (from mTLS cert CN, JWT sub).
    Actor           string

    // Tenant ID (for multi-tenant deployments).
    TenantID        string

    // Role of the actor at the time of the event.
    Role            string

    // Resource being accessed (barrier, ceremony, keys, user, role, etc.).
    Resource        string

    // Action performed (initialize, unseal, sign, create, delete, etc.).
    Action          string

    // Outcome: "success", "failure:<reason>", "denied:<reason>".
    Outcome         string

    // Custom attributes (JSON-serializable).
    Attributes      map[string]any

    // Client IP (from X-Forwarded-For header or remote address).
    ClientIP        string

    // Request ID (for tracing across logs).
    RequestID       string

    // Hash of the previous audit event (for tamper-detection chain).
    // Format: "sha256:<hex-digest>"
    PrevHash        string
}
```

**Phase 1 (this phase)**: Documented target schema. Entry structure remains unchanged.

**Phase 6**: Extended `Entry` with `Role`, `Outcome` (with reasons), `Attributes` (map), `ClientIP`, `RequestID`, `PrevHash` fields. Hash-chain computation and verification implemented. Query API added.

| Field | Description | Example |
|---|---|---|
| `Timestamp` | UTC time of the event; auto-set to `time.Now()` if zero | `2026-02-26T12:00:00Z` |
| `Actor` | Username/identity (from cert CN, JWT, API key) | `custodian_a`, `admin` |
| `TenantID` | Tenant that owns the resource | `default` |
| `Role` | Role of actor at event time (so, admin, operator, user, auditor, custodian) | `custodian` |
| `Resource` | Resource type and identifier (barrier, ceremony, key:id, user:id, role:name) | `barrier`, `custodian:a` |
| `Action` | Action identifier (initialize, unseal, sign, enroll-custodian, create) | `unseal-share-complete` |
| `Outcome` | Result: "success", "failure:<reason>", "denied:<reason>" | `success`, `failure:invalid signature`, `denied:insufficient privilege` |
| `Attributes` | Optional context map (share_index, algorithm, threshold, etc.) | `{"share_index": 0, "algorithm": "ECDSA"}` |
| `ClientIP` | Client IP address | `192.168.1.100` |
| `RequestID` | Request trace ID (for correlation) | `req-abc123` |
| `PrevHash` | SHA-256 hash of previous event JSON (for tamper-detection chain) | `sha256:e5f6a7b8c9d0e1f2...` |

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
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit","tenant_id":"system","subject":"admin","action":"user.create","resource":"user:operator","result":"success","event_time":"2026-02-26T12:00:00Z"}
```

When entries include metadata, it appears as a nested JSON group:

```json
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit","tenant_id":"system","subject":"admin","action":"cert.revoke","resource":"cert:serial123","result":"success","event_time":"2026-02-26T12:00:00Z","metadata":{"reason":"key compromise"}}
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
