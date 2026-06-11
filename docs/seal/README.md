<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Seal Package — StorageBarrier

The `pkg/seal/` package provides transparent at-rest encryption for go-qrdb's storage layer. It extends go-quicraft's minimal barrier with database-layer features: Shamir threshold unsealing, multi-tenant barriers, storage wrapping, recovery keys, rekey, and root token generation.

## Why this exists

go-qrdb runs in three distinct deployment shapes:

1. **Software only** — single passphrase or Shamir M-of-N, no hardware. The default. The barrier itself is fully self-contained.
2. **None** — barrier disabled entirely, data stored in plaintext for dev/test or where the operator already provides full-disk encryption.
3. **Hardware-backed** — TPM2, PKCS#11, AWS KMS, GCP KMS, Azure Key Vault, or HashiCorp Vault as root of trust.

The first two ship inside qrdb. The third ships in go-xkms and is **registered into qrdb at composition time** through a `seal.StrategyRegistry`. qrdb has no compile-time dependency on any hardware code; it just calls a registered factory by name.

## Related pages

- [sealed-window.md](sealed-window.md) — Sealed-window endpoint allowlist, format-version sentinel, pre-host listener boot sequence.
- [shamir.md](shamir.md) — M-of-N share split, threshold unsealing, share lifecycle.
- [custodians.md](custodians.md) — Custodian registration, revocation, dual-factor auth.
- [multi-tenant.md](multi-tenant.md) — Per-tenant barrier isolation via `NamespacedBackend`.
- [compliance.md](compliance.md) — FIPS, FedRAMP, PCI-DSS, HIPAA mapping.
- [../pin/README.md](../pin/README.md) — PIN backend used by the SO/User PIN ceremony.

## Three-Layer Architecture

```
go-quicraft/pkg/seal/          Interfaces + memguard + minimal Barrier + Software/None/Shamir primitives
        ↑
go-qrdb/pkg/seal/              StorageBarrier: multi-tenant, storage wrapping, recovery, root tokens,
                               share exchange, StrategyRegistry (Software + None pre-registered)
go-qrdb/pkg/dek/               ClusterDEKManager, TenantDEKManager
        ↑
go-xkms/pkg/seal/              Hardware strategies (TPM2, PKCS#11, AWS KMS, GCP KMS, Azure KV, Vault)
                               registered into a qrdb StrategyRegistry at composition time
```

- **go-quicraft** defines the barrier interfaces (`Barrier`, `SnapshotBarrier`, `EpochTracker`) and a minimal default implementation with AES-256-GCM, epoch rotation, deployment ID isolation, brute-force protection, and memguard memory protection. It also owns the three software-shape strategies: `SoftwareStrategy`, `ShamirStrategy`, `NoneStrategy`.
- **go-qrdb** wraps go-quicraft's barrier in `StorageBarrier`, adding all database-layer features. The `pkg/dek/` package provides cluster and tenant DEK management using `seal.EncryptionBarrier` for key wrapping.
- **go-xkms** ships hardware `SealingStrategy` implementations and registers them into a qrdb `StrategyRegistry` so qrdb can resolve `--barrier-strategy <name>` at init time without importing any HSM SDK.

## Strategies owned by qrdb

| StrategyID | Constant | Source | When to use |
|------------|----------|--------|-------------|
| `passphrase` | `seal.StrategyPassphrase` | go-quicraft (re-exported) | Single passphrase, FIPS-aware KDF (Argon2id or PBKDF2-SHA256). The default. |
| `shamir`   | `seal.StrategyShamir`   | go-qrdb (`pkg/seal/strategy_shamir.go`) | M-of-N quorum unseal; the Shamir secret is a random unseal key that AEAD-wraps the root key (Vault-style indirection, see [shamir.md](shamir.md)). |
| `none`     | `seal.StrategyNone`     | go-quicraft (re-exported) | Barrier disabled; data stored plaintext. Dev/test or OS-level FDE deployments. |

> **Config alias:** the YAML `seal.strategy` field accepts `software` as a **deprecated alias** for `passphrase`. It is canonicalized once at config load (`SealConfig.CanonicalStrategy()`), so every consumer sees the canonical `passphrase` ID — the software/passphrase strategy reports `ID() == "passphrase"`.

Hardware strategies (`tpm2`, `pkcs11`, `aws-kms`, `gcp-kms`, `azure-kv`, `vault`) live in go-xkms and register themselves through `StrategyRegistry.Register` at composition time.

The runtime selection priority for **active strategy** when multiple are registered:

```
TPM2 > PKCS#11 > Software
```

(see `pkg/seal/strategy.go:73-83` and `DefaultPreferenceOrder`). When only Software (or only None) is registered, that strategy is selected unconditionally.

## StrategyRegistry — the canonical lookup table

`StrategyRegistry` decouples the daemon from concrete strategy implementations. The daemon parses `--barrier-strategy <name>` and calls `LookupFactory` on the registry; the registry returns a `StrategyFactory` that constructs the strategy on demand. (`pkg/seal/strategy_registry.go:39-58`)

```go
type StrategyRegistry interface {
    Register(name StrategyID, factory StrategyFactory) error
    Unregister(name StrategyID)
    LookupFactory(name StrategyID) (StrategyFactory, error)
    ListAvailable() []StrategyID
}

type StrategyFactory func() (SealingStrategy, error)
```

### Defaults — `NewDefaultStrategyRegistry`

`NewDefaultStrategyRegistry()` (`pkg/seal/strategy_registry.go:88-97`) returns a registry pre-populated with the two software strategies qrdb owns outright:

- `StrategySoftware` — built via `NewDeferredSoftwareStrategy()` so the passphrase can be supplied at Initialize/Unseal time rather than at construction.
- `StrategyNone` — built via `NewNoneStrategy()`.

`StrategyShamir` is intentionally NOT registered in the default registry because the Shamir strategy requires a `Threshold` and `Shares` configuration that only the daemon's init command can supply; the daemon constructs it inline at init time.

### Hardware injection — `WithStrategyRegistry`

Higher layers add hardware strategies on top by handing qrdb's server constructor a registry through `WithStrategyRegistry`:

```go
// pkg/server/server.go:478-486
func WithStrategyRegistry(reg seal.StrategyRegistry) ServerOption {
    return func(o *serverOptions) { o.strategyRegistry = reg }
}
```

The typical composition flow (driven by go-quicbaas):

```mermaid
sequenceDiagram
    participant App as quicbaas main
    participant Reg as seal.StrategyRegistry
    participant XKMS as go-xkms
    participant QRDB as qrdb server
    App->>Reg: NewDefaultStrategyRegistry()
    App->>XKMS: xkms.RegisterAll(Reg)
    XKMS->>Reg: Register(StrategyTPM2, factory)
    XKMS->>Reg: Register(StrategyPKCS11, factory)
    XKMS->>Reg: Register(StrategyAWSKMS, factory)
    App->>QRDB: server.New(cfg, ..., WithStrategyRegistry(Reg))
    QRDB->>Reg: LookupFactory("tpm2")
    Reg-->>QRDB: factory()
    QRDB->>QRDB: Initialize barrier with TPM2 strategy
```

The same registry can be used to inject test/fake strategies in integration tests without any hardware coupling.

### Direct injection — `WithSealStrategy`

For deployments that know the strategy at construction time (typical software-only setups, embedded SDK callers) `WithSealStrategy` bypasses the registry and hands the constructor a pre-built `SealingStrategy`:

```go
// pkg/server/server.go:469-476
func WithSealStrategy(strategy seal.SealingStrategy) ServerOption {
    return func(o *serverOptions) { o.sealStrategy = strategy }
}
```

Pick one or the other; the registry is for name-based dispatch at `qrdbd init --barrier-strategy <name>`, the direct injector is for compile-time decisions.

## StorageBarrier

```go
type StorageBarrier struct {
    logger         *slog.Logger
    base           StorageBackend     // optional storage wrapping
    barrier        *quicraftseal.Barrier // embedded go-quicraft barrier
    config         BarrierConfig
    strategies     map[StrategyID]SealingStrategy
    prefOrder      []StrategyID

    state          atomic.Int32       // 0 = sealed, 1 = unsealed
    activeStrategy atomic.Value       // StrategyID that sealed the root key
    accumulator    atomic.Value       // *shamirAccumulator for quorum unsealing
}
```

### Dual-Mode Operation

| Mode | Strategy Type | Encrypt/Decrypt | Key Rotation |
|------|--------------|-----------------|--------------|
| **Software / Shamir** | `SoftwareStrategy`, `ShamirStrategy` | Epoch-based HKDF DEKs via embedded barrier | `Rotate()` generates new epoch DEK |
| **None** | `NoneStrategy` | Passthrough (no encryption) | n/a |
| **Hardware** (xkms-registered) | TPM2, PKCS#11, cloud KMS, Vault | Delegates to `BarrierEncryptor` from strategy | Hardware manages key lifecycle |

### Storage Wrapping

When `base` is set, the barrier transparently encrypts all `Put` operations and decrypts all `Get` operations. When nil, the barrier operates as pure crypto (go-quicraft behavior for Raft WAL).

## StrategyNone — the no-encryption mode

`StrategyNone` (`pkg/seal/strategy.go:147,150-154`) selects the no-encryption barrier shape. All `Put` / `Get` operations bypass encryption and store data in plaintext. The barrier still maintains state machinery (sealed/unsealed) so callers and audit logs do not need to special-case it.

Use cases:

- Local development without a passphrase prompt
- Test fixtures that exercise the storage path without crypto overhead
- Deployments where the storage device already provides authenticated full-disk encryption (LUKS+integrity, BitLocker with TPM-bound VMK, etc.)

`StrategyNone` is registered in the default registry alongside `StrategySoftware`. Operators opt in with `qrdbd init --barrier-strategy none` or by selecting it explicitly via `WithSealStrategy(NewNoneStrategy())`.

> **Operational note**: When `none` is selected, the audit log marks every barrier operation with `strategy: none` so the absence of encryption is permanently visible in the trail. The xkms `--require-encryption` compliance gate refuses to come up against a `none`-mode barrier.

## Features

| Feature | Description | Documentation |
|---------|-------------|---------------|
| Shamir threshold unsealing | M-of-N quorum-based unseal with share accumulation | [shamir.md](shamir.md) |
| Multi-tenant barriers | Independent DEK per tenant via BarrierRegistry | [multi-tenant.md](multi-tenant.md) |
| Recovery keys | Independent Shamir split of the DEK for disaster recovery | [shamir.md](shamir.md) |
| Rekey | New unseal key + share set; cryptographically revokes all prior shares without rotating the root key | [shamir.md](shamir.md) |
| Root token generation | One-time admin token via share reconstruction | [shamir.md](shamir.md) |
| Audit logging | Structured audit events for all barrier operations | Inline below |
| Cluster auto-unseal | ShareExchangeCoordinator for distributed share exchange | Inline below |
| Cluster DEK management | Cluster-wide DEK with envelope encryption | [DEK Package](#dek-package) |
| Tenant DEK management | Per-tenant DEK isolation | [DEK Package](#dek-package) |

## DEK Package

The `pkg/dek/` package manages Data Encryption Keys at the cluster and tenant level. It keeps all encryption logic in the database layer, with no transitive xkms dependency.

### ClusterDEKManager

Manages the cluster-wide DEK used for encrypting data at rest across all nodes.

- Uses `seal.EncryptionBarrier` to wrap/unwrap the cluster DEK
- Supports key rotation with version tracking
- Background re-encryption after rotation with configurable batch size and concurrency
- Per-node synchronization state tracking
- Depends only on `*slog.Logger` and `seal.EncryptionBarrier`

```
Key Hierarchy:
  Master Key (per node, in-memory only)
      └── seal.EncryptionBarrier wraps Cluster DEK
              └── Cluster DEK wraps Tenant DEKs
```

### TenantDEKManager

Manages per-tenant DEKs, each isolated and wrapped by the cluster DEK.

- Creates, retrieves, rotates, and deletes per-tenant DEKs
- Wraps tenant DEKs using stdlib `crypto/aes` + `crypto/cipher` (AES-256-GCM)
- Pure Go stdlib cryptography
- Stores wrapped DEKs at `__sys:dek/tenant/{tenantID}` in a `StorageBackend`

```go
type TenantDEKManager interface {
    GetOrCreateTenantDEK(ctx context.Context, tenantID string) ([]byte, error)
    GetTenantDEK(ctx context.Context, tenantID string) ([]byte, error)
    RotateTenantDEK(ctx context.Context, tenantID string) error
    DeleteTenantDEK(ctx context.Context, tenantID string) error
    GetTenantDEKStatus(ctx context.Context, tenantID string) (*TenantDEKStatus, error)
}
```

### SDK Access

Both DEK managers are accessible through the Go SDK via `client.DEK()`, which returns a `transport.DEKClient`. All transports (REST, gRPC, UNIX, MCP, embedded) implement this interface.

```go
dekClient := client.DEK()

// Cluster DEK
err := dekClient.InitializeClusterDEK(ctx, &transport.InitClusterDEKRequest{
    Algorithm: "AES-256-GCM",
    KeyBytes:  32,
})

// Tenant DEK
tenantDEK, err := dekClient.GetOrCreateTenantDEK(ctx, "tenant-123")
```

## ShareExchangeCoordinator

The `ShareExchangeCoordinator` enables cluster-level auto-unseal by exchanging Shamir shares between nodes. It is transport-agnostic — callers inject a `ShareTransport` implementation.

```go
type ShareTransport interface {
    SendShare(ctx context.Context, toNodeID uint64, msg *ShareExchangeMessage) error
    BroadcastShareRequest(ctx context.Context, fromNodeID uint64) error
}
```

### Flow

1. Each node starts a coordinator with its `nodeID`, threshold, and transport
2. On boot, `CollectSharesWithTimeout` broadcasts a share request to peers
3. Peers respond with their encrypted shares via `OnShareReceived`
4. When the quorum threshold is met, the `UnsealHandler` callback fires
5. The handler calls `StorageBarrier.UnsealWithShares()` to reconstruct the root key

### Key Design

- **Lock-free state**: `started` and `quorumCalled` use `sync/atomic.Bool`
- **Exactly-once unseal**: `CompareAndSwap` ensures the handler fires once
- **Map-based dispatch**: Message routing uses `map[ShareMessageType]handler` for O(1) lookup
- **No `time.Sleep`**: Uses `time.NewTimer` + `select` for timeouts

## CA Key Lifecycle

The `DefaultCertProvider` (`pkg/cert/default_provider.go`) stores its ECDSA P-256 CA private key using the encryption barrier for at-rest protection and `memguard.LockedBuffer` for in-memory protection.

### Bootstrap

1. Generate ECDSA P-256 CA key pair (`crypto/rand`)
2. Encrypt private key via `EncryptionBarrier.Encrypt()`
3. Store wrapped blob in KVStore at `__sys:cert_ca/{tenantID}/wrapped_key`
4. Store CA cert PEM in KVStore at `__sys:cert_ca/{tenantID}/cert_pem`
5. Zero plaintext key (`memguard.WipeBytes`)

### Unseal to Operational

1. Load wrapped blob from KVStore
2. Decrypt via `EncryptionBarrier.Decrypt()`
3. Store plaintext key in `memguard.NewLockedBuffer()` (secure enclave)
4. Parse ECDSA private key from LockedBuffer bytes
5. Cache parsed signer (`crypto.Signer`) for signing operations

### Seal

1. `LockedBuffer.Destroy()` (securely zeroes and destroys memory)
2. Nil the cached signer
3. All `GenerateClientCert()` calls return `CAKeyNotAvailableError`

## Audit Logging

All barrier operations emit audit events when an `AuditLogger` is configured:

| Action | Resource | Outcome |
|--------|----------|---------|
| `initialize` | `barrier` | `allow` / `deny` |
| `unseal` | `barrier` | `allow` / `deny` |
| `seal` | `barrier` | `allow` |
| `rotate` | `barrier` | `allow` / `deny` |
| `rekey` | `barrier` | `allow` / `deny` |
| `recovery_generate` | `barrier` | `allow` / `deny` |
| `tenant_init` | `tenant:{id}` | `allow` / `deny` |

## SDK Client Usage

The barrier is accessible through the Go SDK via `client.Security().Barrier()`, which returns a `transport.BarrierClient`. All transports (REST, gRPC, UNIX, MCP, embedded) implement this interface.

### Setup

```go
import (
    sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
    "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
    "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

t, err := rest.New(
    transport.WithAddress("https://localhost:8443"),
    transport.WithTLSCAFile("/etc/qrdb/ca.pem"),
)
if err != nil {
    return err
}
client, err := sdk.NewClient(t)
if err != nil {
    return err
}
if err := client.Connect(ctx); err != nil {
    return err
}
defer client.Close()

barrier := client.Security().Barrier()
```

### Common Workflows

#### Initialize with Password

Initialize the barrier with a single master secret on first boot.

```go
err := barrier.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{
    Secret: "my-master-secret",
})
```

After initialization the barrier remains sealed. Unseal it to allow data access:

```go
err = barrier.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{
    Secret: "my-master-secret",
})
```

#### Initialize with `none` strategy

Selecting `none` skips secret prompting entirely. The barrier is immediately operational with no encryption applied to user data.

```bash
qrdbd init --barrier-strategy none
```

There is no `BarrierInitialize` call required for `none`; the daemon transitions to operational on startup. Audit events still fire with `strategy: none`.

#### Initialize with Shamir M-of-N

Initialize with Shamir secret sharing. Shares are **never returned in the
response** — each share is sealed into a server-side memguard enclave bound to
an enrolled Security Officer, who claims it exactly once over mTLS (see
[so-share-distribution-design.md](so-share-distribution-design.md) and
[shamir.md](shamir.md)). Register the officers (CSR-based) before
initializing.

```go
// 1. Admin registers each Security Officer with their CSR.
reg, err := barrier.OfficerRegister(ctx, &transport.OfficerRegisterRequest{
    Username: "so-alice",
    CSRPEM:   aliceCSR,
})
// reg.CertPEM: the officer's signed mTLS certificate
// reg.ShareIndex: the share that will be sealed to this officer

// 2. Initialize: the response is share-free.
resp, err := barrier.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{
    Secret:      "root-secret",
    Threshold:   3,
    TotalShares: 5,
})
// resp.Threshold: 3, resp.TotalShares: 5, resp.ShareCount: 5

// 3. Each officer claims their share with their own mTLS cert:
//    challenge nonce -> sign with officer key -> one-time share reveal.
ch, err := barrier.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "so-alice"})
claim, err := barrier.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{
    Username:  "so-alice",
    NonceHex:  ch.NonceHex,
    Signature: signature,
})
// claim.ShareBytes is returned exactly once; the server enclave is destroyed.
```

#### Unseal with Shamir Shares (Quorum)

Submit shares one at a time. Each call returns progress toward the threshold. The barrier unseals automatically once the threshold is met.

```go
shares := []string{"share-A", "share-B", "share-C"}

for _, share := range shares {
    progress, err := barrier.BarrierUnsealWithShare(ctx, &transport.BarrierUnsealShareRequest{
        Share: share,
    })
    if err != nil {
        return err
    }
    fmt.Printf("Progress: %d/%d\n", progress.Submitted, progress.Required)
    if progress.Complete {
        fmt.Println("Barrier unsealed")
        break
    }
}
```

To submit all shares in one call:

```go
err := barrier.BarrierUnsealWithShares(ctx, &transport.BarrierUnsealSharesRequest{
    Shares: shares,
})
```

#### Check Status

```go
status, err := barrier.BarrierStatus(ctx)
if err != nil {
    return err
}
fmt.Printf("sealed=%v strategy=%s hardware=%v\n",
    status.Sealed, status.Strategy, status.HardwareBacked)
```

#### Rekey

```go
resp, err := barrier.BarrierRekey(ctx, &transport.BarrierRekeyRequest{
    Threshold: 2,
    Total:     3,
})
// resp.Threshold / resp.TotalShares / resp.ShareCount — the new shares are
// sealed to the enrolled officers' enclaves; each officer must claim again.
```

#### Recovery Keys

Recovery shares follow the same officer-claim model through the recovery
officer registry — generation responses are share-free.

```go
// Register recovery officers first (CSR-based, same as system officers).
reg, err := barrier.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{
    Username: "ro-bob",
    CSRPEM:   bobCSR,
})

keys, err := barrier.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{
    Threshold: 2,
    Total:     3,
})
// keys.Threshold / keys.Total / keys.ShareCount — no key material returned.

// Recovery officers claim their shares one time each.
ch, err := barrier.RecoveryShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "ro-bob"})
claim, err := barrier.RecoveryShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{
    Username:  "ro-bob",
    NonceHex:  ch.NonceHex,
    Signature: signature,
})

status, err := barrier.BarrierHasRecoveryKeys(ctx)

err = barrier.BarrierRecoverWithKeys(ctx, &transport.BarrierRecoverWithKeysRequest{
    Keys: []string{"key-A", "key-B"},
})

err = barrier.BarrierDeleteRecoveryKeys(ctx)
```

#### Root Token Generation

```go
resp, err := barrier.BarrierGenerateRootToken(ctx, &transport.BarrierGenerateRootTokenRequest{
    Shares: []string{"share-A", "share-B", "share-C"},
})
```

#### Seal

```go
err := barrier.BarrierSeal(ctx)
```

For a **Shamir-strategy** barrier, single-admin `BarrierSeal` is rejected
(`ErrShamirBarrierRequiresQuorum`); M officers must each submit a seal vote
instead. The barrier seals automatically once the quorum reconstructs and
verifies the secret:

```go
progress, err := barrier.ShamirSealVote(ctx, &transport.ShamirSealVoteRequest{
    Share: shareA,
})
// progress.Required / progress.Submitted / progress.Complete
```

#### Shamir Share Management

```go
shares, err := barrier.BarrierShamirListShares(ctx)
err = barrier.BarrierShamirVerify(ctx)
err = barrier.BarrierShamirDeleteShare(ctx, &transport.BarrierShamirDeleteShareRequest{Index: 2})
err = barrier.BarrierShamirDeleteAllShares(ctx)
```

## REST API Reference

All endpoints are under the `/v1/barrier/` prefix. Mutation endpoints require an authenticated session with admin privileges.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/barrier/initialize` | Initialize with a single master key |
| `POST` | `/v1/barrier/unseal` | Unseal with the master key |
| `POST` | `/v1/barrier/seal` | Seal the barrier |
| `GET` | `/v1/barrier/status` | Return barrier state |
| `POST` | `/v1/barrier/shamir/initialize` | Initialize with Shamir M-of-N |
| `POST` | `/v1/barrier/shamir/unseal-share` | Submit one Shamir share |
| `POST` | `/v1/barrier/shamir/unseal-shares` | Submit multiple Shamir shares |
| `GET` | `/v1/barrier/shamir/shares` | List Shamir share metadata |
| `DELETE` | `/v1/barrier/shamir/shares/{index}` | Delete share by index |
| `DELETE` | `/v1/barrier/shamir/shares` | Delete all shares |
| `POST` | `/v1/barrier/shamir/verify` | Verify share integrity |
| `POST` | `/v1/barrier/rekey` | Rekey with new shares |
| `POST` | `/v1/barrier/recovery/generate` | Generate recovery keys |
| `POST` | `/v1/barrier/recovery/recover` | Unseal using recovery keys |
| `DELETE` | `/v1/barrier/recovery` | Delete recovery keys |
| `GET` | `/v1/barrier/recovery/status` | Check if recovery keys exist |
| `POST` | `/v1/barrier/root-token` | Generate a root token |
| `POST` | `/v1/barrier/officers` | Register a Security Officer (CSR) |
| `GET` | `/v1/barrier/officers` | List registered Security Officers |
| `GET` | `/v1/barrier/officers/claim-status` | Per-officer claim status |
| `POST` | `/v1/barrier/officers/{username}/claim/begin` | Issue a claim challenge nonce |
| `POST` | `/v1/barrier/officers/{username}/claim/complete` | One-time share reveal |
| `POST` | `/v1/barrier/shamir/seal-vote` | Submit a seal vote (Shamir quorum seal) |
| `POST` | `/v1/barrier/recovery/officers` | Register a recovery officer (CSR) |
| `POST` | `/v1/barrier/recovery/officers/{username}/claim/begin` | Recovery claim nonce |
| `POST` | `/v1/barrier/recovery/officers/{username}/claim/complete` | One-time recovery share reveal |

Successful mutation endpoints return `204 No Content`. Endpoints that return data respond with `200 OK` and a JSON body.

Notes on specific endpoints:

- `POST /v1/barrier/shamir/initialize` honors the **request's** `threshold` and `total_shares` (the operator/UI-chosen M-of-N scheme). When omitted (zero), the daemon's configured `seal.shamir_threshold` / `seal.shamir_total` are used. The response is **share-free** (`{threshold, total_shares, share_count}`) — shares are sealed to the enrolled Security Officers' enclaves and retrieved through the claim endpoints. The administrative root token is a separate call (`POST /v1/barrier/root-token`) that requires a quorum of claimed shares as input.
- The claim endpoints require the caller's **mTLS client certificate** to match the thumbprint bound at registration; the claim/complete response is the only message in the entire API that ever carries share bytes, and the server-side enclave is destroyed on delivery. These routes are **identity-gated** (`permission.IdentityGated`), not RBAC-gated: Security Officers are ceremony-registry principals registered before the RBAC user store exists, so the middleware requires only an authenticated mTLS identity and the handler's registry/thumbprint/signature checks are the authorization (the Vault posture — possession of the enrolled credential is the authorization).
- `POST /v1/barrier/seal` returns `422 Unprocessable Entity` for Shamir-strategy barriers (`ErrShamirBarrierRequiresQuorum`); use `/v1/barrier/shamir/seal-vote` (M distinct shares within the vote TTL) instead. After the seal quorum completes, `/v1/barrier/status` remains reachable on the same port via the sealed-window listener.
- `POST /v1/barrier/shamir/initialize` **auto-unseals** the barrier and triggers the sealed-window → full-server listener swap. Clients should expect a brief connection gap (~seconds) immediately after a successful initialize and poll `/health` until it returns `200`.
- `POST /v1/barrier/rekey` rotates the **distributed share set, not the root key**: unclaimed prior-round enclaves are destroyed, claim history resets (officers re-claim), but a quorum of prior-round shares still reconstructs the preserved root key. See [shamir.md](shamir.md#rekey).
- `GET /v1/barrier/status` includes `shamir_threshold` and `shamir_total` when the active strategy is `shamir` (absent otherwise), so clients can display the M-of-N scheme.
- `GET /health` returns `503 {"error":"barrier sealed","code":"SEALED"}` while the barrier is sealed and `200` once unsealed and bootstrapped.
- The **MCP barrier tools** (`qrdb_barrier_status`, `qrdb_barrier_shamir_initialize`, `qrdb_barrier_rekey`, `qrdb_barrier_recovery_generate`, `qrdb_barrier_root_token`) return the same canonical snake_case wire shapes as the REST endpoints above: status is `{sealed, strategy, hardware_backed, initialized_at (RFC3339, omitted when never initialized), shamir_threshold/shamir_total (when > 0)}`; Shamir initialize and rekey return the share-free `{threshold, total_shares, share_count}`; recovery generate returns `{threshold, total, share_count}`; root token returns `{token, created_at}`. SDK clients on every transport decode these shapes — handlers must never marshal Go service structs directly (PascalCase fields do not survive a snake_case round-trip).

### Barrier HTTP status mapping

All `/v1/barrier/*` handlers share one error mapper. Seal/ceremony errors map as follows; anything unrecognized falls through to the generic service mapper (e.g. `BarrierSealedError` → `503`), and unknown errors return `500`.

| Error | HTTP status |
|-------|-------------|
| `seal.ErrShamirBarrierRequiresQuorum` | `422 Unprocessable Entity` |
| `seal.ErrAlreadyUnsealed` | `409 Conflict` |
| `seal.ErrAlreadyInitialized` | `409 Conflict` |
| `seal.ErrNotInitialized` | `409 Conflict` |
| `seal.ErrInvalidCredentials` | `403 Forbidden` |
| `ceremony.ErrShareAlreadyClaimed` | `409 Conflict` |
| `ceremony.ErrNoPendingShare` | `404 Not Found` |
| barrier sealed (service error) | `503 Service Unavailable` |

## Web UI administration

The browser admin UI (`cmd/qrdb/frontend`) drives the barrier entirely over the REST API above:

- **Bootstrap.** When the UI connects to a sealed, *uninitialized* server it routes to the Server Initialization wizard (none / passphrase / Shamir). For Shamir, **no share material is ever displayed**: the wizard shows the M-of-N scheme and a per-officer claim-status table, and directs each Security Officer to authenticate with their own mTLS certificate at the `/officer/claim` page, where their share is revealed exactly once. No root token is minted at init time — officers generate one post-claim from a share quorum (`POST /v1/barrier/root-token`).
- **Unseal.** When the UI connects to a sealed, *initialized* server it shows an unseal screen; the Seal Manager also seals/unseals an already-loaded server. The unseal control adapts to the active strategy (passphrase entry vs Shamir share submission).
- **Sealed connectivity.** A sealed daemon serves only the `/v1/barrier/*` (and ceremony/credential) endpoints via its pre-host sealed-window listener — it does **not** serve the SPA itself (see [sealed-window.md](sealed-window.md)). In production the embedded UI is therefore reachable only once unsealed; for development the Vite dev server serves the SPA and proxies the API to the daemon (`QRDB_PROXY_TARGET`), so the init/unseal screens are reachable against a sealed backend.

### Desktop (Wails) seal flow

The desktop GUI's `SealBinding` (`cmd/qrdb/bindings_seal.go`) is a thin delegate over the Go SDK's `BarrierClient` on the active connection — it performs **no local share splitting or barrier simulation**. `InitializeShamir` returns the same share-free result as the REST endpoint (no share bytes, no root token); `Unseal` consults `BarrierStatus` first and routes by strategy (`shamir` → per-share `BarrierUnsealWithShare` with quorum progress, otherwise one-shot passphrase `BarrierUnseal`). For embedded servers, only `none`-mode barriers auto-initialize/auto-unseal at startup; passphrase and Shamir barriers honor the daemon's sealed-window semantics and require the same operator ceremony as a remote server.

### Per-tenant barriers in the UI

The Tenants admin view lets the **tenant admin choose the barrier strategy** — `none` / `passphrase` / `shamir` — defaulting to `passphrase`. A cryptographic barrier requires a storage **volume pool** configured on the daemon (`storage.volume_pools`); without one, `POST /v1/tenants/{id}/barrier/init` returns `volume pool not configured`. New tenants default to `prefix` isolation (no pool needed). Per-tenant `shamir` follows the same SO distribution model as the system barrier: tenant officers are registered per tenant (`POST /v1/tenants/{id}/barrier/officers`), init responses are share-free, each officer claims their share once via the tenant claim endpoints, the UI drives the per-tenant multi-share unseal ceremony via `POST /v1/tenants/{id}/barrier/unseal-share`, and sealing a Shamir tenant barrier requires a seal-vote quorum (`POST /v1/tenants/{id}/barrier/seal-vote`). See [multi-tenant.md](multi-tenant.md).

## Compliance Documentation

- **[../auth/roles.md](../auth/roles.md)** — Six-role model (SO, Admin, Operator, User, Auditor, Custodian) with FIPS 140-3 AS10.03 separation of duties
- **[compliance.md](compliance.md)** — FIPS 140-3, FedRAMP, PCI-DSS v4, HIPAA requirement cross-reference
- **[custodians.md](custodians.md)** — Custodian lifecycle, two-tier share model (operational Shamir + recovery keys), share-delivery protocol

## See Also

- [go-quicraft docs/06-crypto.md](https://github.com/jeremyhahn/go-quicraft/blob/main/docs/06-crypto.md) — Minimal barrier, memguard, epoch rotation, software/none/shamir primitives
- [go-xkms docs/seal/README.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/seal/README.md) — Hardware sealing strategies that register into qrdb's `StrategyRegistry`
- [../pin/README.md](../pin/README.md) — PIN backend interface (canonical PIN contract for the stack)
- [../credentials/README.md](../credentials/README.md) — Versioned credential rotation backed by the barrier
- [../audit/README.md](../audit/README.md) — Audit event logging and query API
