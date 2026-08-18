# Audit — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Audit() transport.AuditClient   // nil if the transport lacks AuditProvider
```

`pkg/audit`:

```go
type Logger interface {
	Log(ctx context.Context, entry *Entry) error
	Close() error
}

func New(cfg Config, kvStore kvstore.KVStore) (Logger, error)

type Config struct {
	Enabled     bool   `yaml:"enabled"`
	KVBackend   bool   `yaml:"kv_backend"`
	FileBackend bool   `yaml:"file_backend"`
	FilePath    string `yaml:"file_path"`
}
```

## Factory selection

| Condition | Result |
|---|---|
| `cfg.Enabled == false` | `noopLogger` |
| KV only | `kvLogger` |
| File only | `fileLogger` |
| Both | `multiLogger` wrapping both |
| Neither backend enabled | `noopLogger` |

**`KVBackend` enabled with a nil `kvStore` returns `*ConfigError`.** It is a
construction-time failure, not a silent downgrade to noop.

## Event schema

Target compliance schema (`pkg/audit/types.go`):

| Field | Meaning |
|---|---|
| `Timestamp` | UTC; auto-set to `time.Now()` if zero |
| `Actor` | identity from the mTLS certificate CN, JWT subject, or API key |
| `TenantID` | tenant owning the resource |
| `Role` | the actor's role **at the time of the event** |
| `Resource` | barrier, ceremony, keys, user, role, … |
| `Action` | initialize, unseal, sign, create, delete, … |
| `Outcome` | `success`, `failure:<reason>`, `denied:<reason>` |
| `Attributes` | JSON-serializable custom attributes |
| `ClientIP` | from `X-Forwarded-For` or the remote address |
| `RequestID` | for tracing across logs |
| `PrevHash` | `sha256:<hex>` of the previous entry — the tamper-detection chain |

> **Phased implementation.** The source documents the schema above as the
> target, with `Role`, extended `Outcome`, `Attributes`, `ClientIP`,
> `RequestID`, `PrevHash`, hash-chain computation/verification and the query API
> arriving in a later phase than the base `Entry`. **Verify individual fields
> against `pkg/audit/types.go` before depending on them.**

## Invariants

- **`Timestamp` zero means now.** An entry never carries a zero time.
- **KV keys are `__sys:audit/{tenantID}/{timestamp_nano}`.** The nanosecond
  component makes a prefix scan chronologically ordered, which is what makes
  range queries work without a secondary index.
- **The audit prefix is inside the internal-key guard** and is not enumerable
  through the user KV surface.
- **The file backend opens with `O_CREATE|O_APPEND|O_WRONLY`, mode `0600`**, and
  a mutex serializes concurrent writes.
- **`multiLogger` is best-effort**: the first error is captured, but every
  remaining backend is still attempted, for both `Log()` and `Close()`. A single
  backend failure must not suppress the trail in the healthy ones.
- **`kvLogger.Close()` is a no-op** — the KVStore lifecycle is managed
  externally.
- **Permission denials are audited** as `auth.denied` with the `ResultDenied`
  result value.
- **`PrevHash` chains entries**, so an edit or deletion is detectable.

## File output format

One JSON object per line:

```json
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit",
 "tenant_id":"system","subject":"admin","action":"user.create",
 "resource":"user:operator","result":"success",
 "event_time":"2026-02-26T12:00:00Z"}
```

With metadata, as a nested group:

```json
{"...":"...","metadata":{"reason":"key compromise"}}
```

## Audited events

| Action | Resource format | Emitted by |
|---|---|---|
| `user.create` / `update` / `delete` / `enable` / `disable` | `user:{username}` | user management |
| `role.create` / `update` / `delete` / `assign` / `unassign` | `role:{name}` | role management |
| `cert.generate` / `cert.revoke` | `cert:{serial}` | certificate management |
| `seal.initialize` / `unseal` / `seal` / `rotate` / `rekey` | `barrier` | seal subsystem |
| `admin.drain` | `node:{id}` | admin operations |
| `admin.snapshot` | `shard:{id}` | admin operations |
| `admin.member.add` / `admin.member.remove` | `shard:{id}` | membership management |
| `auth.denied` | `{resource}` | authentication middleware |

## Configuration

```yaml
audit:
  enabled: true         # master switch
  kv_backend: false     # store entries in the KVStore
  file_backend: true    # write JSON audit log to a file
  file_path: ""         # default "audit.log", relative to the data dir
```

Defaults (`DefaultConfig()`): `Enabled: true`, `KVBackend: false`,
`FileBackend: true`, `FilePath: "audit.log"`.

**Auditing is on by default**, to a file. That is the right default for a
compliance posture: turning it off has to be a deliberate act.

## Known gap

The audit query handlers across REST, QUIC, MCP, Unix and the SDK transports
currently carry a **hardcoded permission string** pending a
`permission.AuditQuery` constant. The intended permission is `audit:query`; the
code does not yet use a constant for it. Tracked defect, not a naming choice —
the same gap affects the auth-factor handlers, see
[../auth-factors/api.md](../auth-factors/api.md).

## Persistence

KV entries live under `__sys:audit/{tenantID}/{timestamp_nano}` and are
Raft-replicated like any other state-machine record. **Nothing prunes them** —
there is no retention policy in this feature.

## Test strategy

- **Unit**: factory selection for each of the five conditions including the
  `*ConfigError` on a nil KVStore, key format and ordering, file mode 0600, and
  `multiLogger` continuing past a failing backend.
- **The best-effort fan-out is the critical test** — a stop-at-first-error
  implementation would pass a naive test and lose the trail in production.
- **Chronological ordering** on a prefix scan is tested, since it is what the
  query surface depends on.
- **Error tests** per function.

## Observability

The audit log **is** the observability surface for security-relevant events.
Metrics and application logs are [../api-server/](../api-server/)'s concern.
