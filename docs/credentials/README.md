<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Credentials Service (`pkg/credentials`)

`pkg/credentials/` manages named credentials with strategy-pluggable storage and **versioned rotation with grace periods**. It is the canonical place every consumer of the qrdb stack (CLI, SDK, transports) reaches for storing rotation-tracked secrets.

## Why this exists

A bare KV API is not enough for credentials. A real credential workflow needs:

- **Rotation** — atomically replace the current value with a new one.
- **Grace periods** — the previous value remains retrievable by version for a configurable window so dependents can roll forward without a synchronised cutover.
- **Enumeration** — list every credential name without exposing values.
- **Versioned reads** — retrieve any non-expired historical version explicitly by number.
- **GC** — reap expired historical versions on a deterministic cadence.

`pkg/credentials` provides all of the above behind one interface, one service, and two strategies (`manual`, `barrier`). The PIN management surface is **separate** — PINs live in `pkg/pin` because they need backend-native lockout semantics that credentials do not.

## Strategies

`pkg/credentials/config.go:11-24`

| Strategy | Description |
|----------|-------------|
| `manual` | Credentials held in-memory inside **memguard secure enclaves** (`secure.Secret`) — never as raw, garbage-collected heap bytes. Retrieval blocks until an operator submits the credential (used during ceremony for non-resident secrets). Plaintext is materialized only transiently (`Open → copy → Destroy`) on read; on delete/rotation the backing enclave is destroyed. |
| `barrier` | Credentials encrypted and stored via `seal.StorageBarrier` (encrypt-then-store on the at-rest storage backend) — the production default. |

Future HSM-only strategies will surface `ErrEnumerationNotSupported` from `ListCredentials` rather than break the interface.

## Boundary: qrdb credentials (L2) vs quicbaas secrets/KMS (L4)

These are deliberately **not** the same thing and do **not** overlap:

| | `pkg/credentials` (qrdb, L2) | quicbaas KMS / `pkg/secrets` (L4) |
|---|---|---|
| **Purpose** | Minimal, self-contained custody of the handful of **operational/boot secrets** the engine itself needs — chiefly the **ceremony `manual` strategy** (security officers submit non-resident secrets that block retrieval until provided). | Full **product secrets-management & KMS** for application/tenant secrets: rich key lifecycle, per-key authz, key-use audit, operator-settable rotation policy. |
| **Layer** | Runs in **standalone qrdb** with no quicbaas present (xkms/quicbaas embed qrdb). | Built **on top of** qrdb's engine; absent in standalone qrdb. |
| **Backing** | `manual` = in-memory memguard enclave; `barrier` = qrdb seal `StorageBarrier` (encrypted-at-rest). | qrdb engine (KV/barrier) via the SDK, plus xkms hardware seal injection. |
| **Scope** | A small set of named engine secrets + versioned rotation. | Arbitrary tenant/app secret trees, large scale. |

qrdb does **not** ship a `secrets`/`kms` package (the old `secrets-envelope` was removed in the single-KEK/DEK refactor). There is no duplication within qrdb: `pkg/credentials` is the only secret-bearing store, and it is the engine's own minimal custody — not a competitor to quicbaas's product KMS, which composes *above* it. This store is also distinct from the **KEK/DEK key custody** (the seal barrier) and the **user KV data plane** (Raft state machine); credentials never live in the user KV store.

## CredentialServicer interface

The service is the consumer-facing entry point. Every transport handler (REST / gRPC / QUIC / MCP / UNIX / embedded) routes through this interface.

`pkg/service/credential_service.go:26-71`

```go
type CredentialServicer interface {
    // Classic CRUD
    CredentialStore(ctx context.Context, name string, value []byte) error
    CredentialRetrieve(ctx context.Context, name string) ([]byte, error)
    CredentialSubmit(ctx context.Context, name string, value []byte) error
    CredentialDelete(ctx context.Context, name string) error
    CredentialStrategy() string

    // Versioned rotation (the new 5 methods added in Phase 8e)
    CredentialList(ctx context.Context) ([]string, error)
    CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
    CredentialRetrieveVersion(ctx context.Context, name string, version uint64) ([]byte, error)
    CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
    CredentialGCExpired(ctx context.Context, name string) (int, error)
}
```

`CredentialService` (`pkg/service/credential_service.go:75-145`) is the canonical implementation; it is a thin pass-through to `credentials.CredentialService`.

## Versioned rotation API

The five new methods on `credentials.CredentialService` (`pkg/credentials/versioned.go:135-247`):

### `ListCredentials`

```go
func (s *CredentialService) ListCredentials(ctx context.Context) ([]string, error)
```

Returns the names of every stored credential. Names only — values are never returned by enumeration. Sorted lexicographically. Returns `ErrEnumerationNotSupported` when the active backend cannot enumerate (no current strategy returns it; the error is reserved for future HSM-only strategies).

### `RotateCredential`

```go
func (s *CredentialService) RotateCredential(ctx context.Context, name string, opts RotateOptions) (*RotateResult, error)

type RotateOptions struct {
    NewValue    []byte
    GracePeriod time.Duration // 0 == wipe previous version immediately
}

type RotateResult struct {
    Name            string
    PreviousVersion uint64    // 0 if the credential did not exist before
    NewVersion      uint64    // always PreviousVersion+1, or 1 on first write
    GraceUntil      time.Time // when the previous version stops being retrievable
}
```

Atomic rotation. If `name` did not exist before, behaves identically to `StoreCredential` and returns `NewVersion == 1`. `GracePeriod == 0` securely wipes the previous value immediately (`secure.Wipe`); a positive grace period writes the previous value to a historical-version slot that remains retrievable until `GraceUntil`.

Returns `ErrInvalidGracePeriod` for negative durations.

### `RetrieveCredentialVersion`

```go
func (s *CredentialService) RetrieveCredentialVersion(ctx context.Context, name string, version uint64) ([]byte, error)
```

Returns the stored credential value for an explicit version. Pass `version == 0` to mean "current version" (identical to `RetrieveCredential`).

Errors:

- `ErrVersionNotFound` — the version was never written.
- `ErrVersionExpired` — the version was once present but its grace period elapsed (GC may or may not have reaped the storage yet — either way the caller is denied).
- `ErrCredentialNotFound` — the credential name does not exist.

### `ListCredentialVersions`

```go
func (s *CredentialService) ListCredentialVersions(ctx context.Context, name string) ([]CredentialVersion, error)

type CredentialVersion struct {
    Name       string
    Version    uint64
    Value      []byte    // ALWAYS nil from this method — use RetrieveCredentialVersion to fetch
    CreatedAt  time.Time
    IsCurrent  bool      // exactly one entry per credential is current at any time
    GraceUntil time.Time // zero when IsCurrent==true or no grace was used
}
```

Returns the current version plus every non-expired historical version. Each entry's `Value` is `nil`; call `RetrieveCredentialVersion` to read a specific value.

### `GCExpiredVersions`

```go
func (s *CredentialService) GCExpiredVersions(ctx context.Context, name string) (int, error)
```

Reaps every historical version of `name` whose `GraceUntil` has elapsed. Returns the count reaped. Idempotent and safe to call repeatedly.

Recommended cadence: iterate `ListCredentials` and call `GCExpiredVersions` for each name once per minute (or whatever granularity matches your shortest grace period).

## Storage layout

A versioned credential at logical name `<name>` is laid out as three keys (see `pkg/credentials/versioned.go:22-41`):

| Key | Contents |
|-----|----------|
| `credentials/<name>`        | The current value (always the latest). The same key the pre-rotation API has always used. |
| `credentials/<name>#meta`   | JSON metadata: current version + grace history (`credentialMeta`). |
| `credentials/<name>#v<N>`   | Historical value for version `N`. Kept while in grace; deleted by GC after expiry. |

The plain `credentials/<name>` key carries the same data the pre-versioned API used. Credentials originally written before rotation existed look like "version 1 with no recorded metadata"; the first `RotateCredential` call promotes them by writing the `#meta` sidecar. Operators can run with legacy credentials forever — promotion is on first rotation.

`credentialMeta` JSON shape (`pkg/credentials/versioned.go:108-123`):

```json
{
  "current_version": 5,
  "history": [
    {"version": 3, "created_at": "2026-05-01T12:00:00Z", "grace_until": "2026-05-22T12:00:00Z"},
    {"version": 4, "created_at": "2026-05-15T00:00:00Z", "grace_until": "2026-05-22T00:00:00Z"}
  ]
}
```

History entries are pruned by `GCExpiredVersions` as their `grace_until` elapses.

## Errors

`pkg/credentials/errors.go`

| Sentinel | Cause |
|----------|-------|
| `ErrInvalidStrategy` | Configured strategy not in `validStrategies`. |
| `ErrNilBarrier` | Barrier strategy selected but no barrier supplied to `NewCredentialService`. |
| `ErrNilLogger` | Logger argument is nil. |
| `ErrEmptyCredentialName` | Name empty / whitespace-only. |
| `ErrEmptyCredentialValue` | Value `nil` or zero length. |
| `ErrCredentialNotFound` | Credential does not exist. |
| `ErrCredentialAlreadySubmitted` | Manual-strategy `SubmitCredential` invoked on a name that already has a value. |
| `ErrEnumerationNotSupported` | Backend cannot enumerate (reserved). |
| `ErrVersionNotFound` | Requested version was never written. |
| `ErrVersionExpired` | Requested version's grace period elapsed. |
| `ErrInvalidGracePeriod` | `RotateOptions.GracePeriod` is negative. |

## Canonical flows

### First-time store + rotation

```go
svc := /* CredentialServicer */

// First write
_ = svc.CredentialStore(ctx, "db-master", []byte("alpha"))

// Rotate to a new value, keeping the old one retrievable for 24 hours
res, err := svc.CredentialRotate(ctx, "db-master", credentials.RotateOptions{
    NewValue:    []byte("bravo"),
    GracePeriod: 24 * time.Hour,
})
// res.PreviousVersion == 1, res.NewVersion == 2, res.GraceUntil ~= now + 24h

// Consumers that haven't rolled forward yet can still fetch v1
v1, _ := svc.CredentialRetrieveVersion(ctx, "db-master", 1)
```

### Listing + housekeeping

```go
names, _ := svc.CredentialList(ctx)
for _, n := range names {
    versions, _ := svc.CredentialListVersions(ctx, n)
    for _, v := range versions {
        if v.IsCurrent {
            fmt.Printf("%s v%d (current)\n", n, v.Version)
        } else {
            fmt.Printf("%s v%d (grace until %s)\n", n, v.Version, v.GraceUntil.Format(time.RFC3339))
        }
    }
    reaped, _ := svc.CredentialGCExpired(ctx, n)
    if reaped > 0 {
        log.Info("gc reaped", "name", n, "count", reaped)
    }
}
```

### Manual-strategy (ceremony bootstrap)

```go
// Server side: block on the named credential. The call returns when an operator
// submits it via SubmitCredential, or when ctx is cancelled.
secret, err := svc.CredentialRetrieve(ctx, "ceremony/recovery-key")

// Operator side (over an authenticated channel):
_ = svc.CredentialSubmit(ctx, "ceremony/recovery-key", []byte("the-key"))
```

## Service construction

`pkg/credentials/service.go:46-77`

```go
func NewCredentialService(cfg *Config, barrier *seal.StorageBarrier, logger *slog.Logger) (*CredentialService, error)
```

Rules:

- `logger` must not be nil.
- `cfg == nil` or `cfg.Strategy == ""` defaults to `StrategyManual`.
- `barrier` is required when `cfg.Strategy == StrategyBarrier`; ignored otherwise.

The `CredentialServicer` wrapper that the transports consume:

```go
// pkg/service/credential_service.go:82-86
func NewCredentialService(c *credentials.CredentialService) CredentialServicer
```

## Memory hygiene

- Manual strategy: every stored value is held in an internal map. Deletion and grace-expiry both call `secure.Wipe` on the byte slice before removing it from the map.
- Barrier strategy: bytes are wiped in-memory once they are written to storage (the storage backend now owns the ciphertext).
- Versioned reads always return a copy of the stored bytes, never the underlying slice, so callers cannot mutate the in-memory state.

## Test coverage

- `pkg/credentials/service_test.go` — base CRUD across both strategies.
- `pkg/credentials/versioned_test.go` — rotation, grace expiry, GC, legacy v1 promotion, error paths.
- `pkg/credentials/coverage_test.go` — error-path coverage smoke tests.

## SDK / Transport exposure

`CredentialServicer` is wired into every transport in the SDK:

| Transport | Path | Notes |
|-----------|------|-------|
| Embedded | `sdk/go/transport/embedded/credentials.go` | Direct method calls. |
| gRPC     | `pkg/api/grpc/credentials_server.go` / `pkg/api/grpc/service_credentials.go` | 1:1 wrappers around `CredentialServicer`. |
| QUIC     | `pkg/api/quic/handlers_credentials.go` | gob-encoded request/response. |
| REST     | `pkg/api/rest/handlers_credentials.go` | JSON envelope. |
| MCP      | `pkg/api/mcp/tools_ceremony_credentials.go` | MCP tool surface. |
| UNIX     | gRPC-over-UDS, reuses the gRPC server. |

The five versioned-rotation methods landed in lockstep across every transport in Phase 8e; there are no `NotImplementedError` returns on the credential surface in any SDK transport.

### Operation → wire mapping & authorization

Every credential operation is exposed on all wire transports and guarded by a
permission under the default-deny posture. The non-versioned `Store`/`Retrieve`
pair is the most recent addition (`Retrieve` returns secret material, so it
carries its own `credentials:retrieve` permission).

| Operation | Permission | gRPC RPC | REST | QUIC | MCP tool |
|-----------|-----------|----------|------|------|----------|
| Store | `credentials:store` | `CredentialStore` | `POST /v1/credentials/store` | `credential.Store` | `qrdb_credential_store` |
| Retrieve | `credentials:retrieve` | `CredentialRetrieve` | `GET /v1/credentials/{name}/value` | `credential.Retrieve` | `qrdb_credential_retrieve` |
| Submit | `credentials:submit` | `CredentialSubmit` | `POST /v1/credentials/submit` | `credential.Submit` | `qrdb_credential_submit` |
| Strategy | `credentials:strategy` | `CredentialStrategy` | `GET /v1/credentials/strategy` | `credential.Strategy` | `qrdb_credential_strategy` |
| List | `credentials:list` | `CredentialList` | `GET /v1/credentials` | `credential.List` | `qrdb_credential_list` |
| Delete | `credentials:delete` | `CredentialDelete` | `DELETE /v1/credentials/{name}` | `credential.Delete` | `qrdb_credential_delete` |
| Rotate | `credentials:rotate` | `CredentialRotate` | `POST /v1/credentials/{name}/rotate` | `credential.Rotate` | `qrdb_credential_rotate` |
| RetrieveVersion | `credentials:retrieve_version` | `CredentialRetrieveVersion` | `GET /v1/credentials/{name}/versions/{v}` | `credential.RetrieveVersion` | `qrdb_credential_retrieve_version` |
| ListVersions | `credentials:list_versions` | `CredentialListVersions` | `GET /v1/credentials/{name}/versions` | `credential.ListVersions` | `qrdb_credential_list_versions` |
| GCExpired | `credentials:gc_expired` | `CredentialGCExpired` | `POST /v1/credentials/{name}/gc` | `credential.GCExpired` | `qrdb_credential_gc_expired` |

The Go SDK exposes all of these via `client.Credentials()` (a
`transport.CredentialClient`). The `qrdb credential` CLI group wraps the same
client: `submit`, `store`, `retrieve`, `list`, `strategy`, `delete`, `rotate`,
`versions`, `retrieve-version`, `gc`.

## See Also

- [../pin/README.md](../pin/README.md) — PIN management (separate from credentials; native lockout, FIPS-aware hashing).
- [../seal/README.md](../seal/README.md) — The barrier that backs `StrategyBarrier`.
- [../ceremony/README.md](../ceremony/README.md) — How ceremony consumes the manual strategy.
- `pkg/credentials/versioned.go` — Canonical reference for the versioned-rotation implementation.
- `pkg/service/credential_service.go` — `CredentialServicer` interface + adapter.
