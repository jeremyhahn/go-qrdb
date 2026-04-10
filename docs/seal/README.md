<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Seal Package — ExtendedBarrier

The `pkg/seal/` package provides transparent at-rest encryption for go-qrdb's storage layer. It extends go-quicraft's minimal barrier with database-layer features: Shamir threshold unsealing, multi-tenant barriers, storage wrapping, recovery keys, rekey, and root token generation.

## Three-Layer Architecture

```
go-quicraft/pkg/seal/          Interfaces + memguard + minimal barrier
        ↑
go-qrdb/pkg/seal/              ExtendedBarrier: Shamir, multi-tenant, storage, recovery
go-qrdb/pkg/dek/               ClusterDEKManager, TenantDEKManager
        ↑
go-xkms/pkg/seal/              Hardware strategies: TPM2, PKCS#11, cloud KMS
```

- **go-quicraft** defines the barrier interfaces (`Barrier`, `SnapshotBarrier`, `EpochTracker`) and a minimal default implementation with AES-256-GCM, epoch rotation, deployment ID isolation, brute-force protection, and memguard memory protection.
- **go-qrdb** embeds go-quicraft's barrier in `ExtendedBarrier`, adding all database-layer features. The `pkg/dek/` package provides cluster and tenant DEK management using `seal.EncryptionBarrier` for key wrapping.
- **go-xkms** adds hardware `SealingStrategy` implementations that plug into any layer via IoC.

## ExtendedBarrier

```go
type ExtendedBarrier struct {
    *quicraftseal.Barrier          // embedded minimal barrier (memguard, epochs)

    strategies     map[string]SealingStrategy
    prefOrder      []string
    mode           barrierMode        // software or hardware
    encryptor      atomic.Value       // SymmetricEncrypter for hardware mode
    accumulator    atomic.Value       // *shamirAccumulator for quorum unsealing
    base           StorageBackend     // optional storage wrapping
    auditLogger    AuditLogger
    activeStrategy atomic.Value
}
```

### Dual-Mode Operation

| Mode | Strategy Type | Encrypt/Decrypt | Key Rotation |
|------|--------------|-----------------|--------------|
| **Software** | SoftwareStrategy, ShamirStrategy | Epoch-based HKDF DEKs via embedded barrier | `Rotate()` generates new epoch DEK |
| **Hardware** | TPM2, PKCS#11, cloud KMS | Delegates to `BarrierEncryptor` from strategy | Hardware manages key lifecycle |

### Storage Wrapping

When `base` is set, the barrier transparently encrypts all `Put` operations and decrypts all `Get` operations. When nil, the barrier operates as pure crypto (go-quicraft behavior for Raft WAL).

## Features

| Feature | Description | Documentation |
|---------|-------------|---------------|
| Shamir threshold unsealing | M-of-N quorum-based unseal with share accumulation | [shamir.md](shamir.md) |
| Multi-tenant barriers | Independent DEK per tenant via BarrierRegistry | [multi-tenant.md](multi-tenant.md) |
| Recovery keys | Independent Shamir split of the DEK for disaster recovery | [shamir.md](shamir.md) |
| Rekey | Generate new shares without changing the root key | [shamir.md](shamir.md) |
| Root token generation | One-time admin token via share reconstruction | [shamir.md](shamir.md) |
| Audit logging | Structured audit events for all barrier operations | Inline below |
| Cluster auto-unseal | ShareExchangeCoordinator for distributed share exchange | Inline below |
| Cluster DEK management | Cluster-wide DEK with envelope encryption | [DEK Package](#dek-package) |
| Tenant DEK management | Per-tenant DEK isolation | [DEK Package](#dek-package) |

## DEK Package

The `pkg/dek/` package manages Data Encryption Keys at the cluster and tenant level. It was migrated from go-quicbaas to keep all encryption logic in the database layer, eliminating the xkms dependency for DEK operations.

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
- No xkms dependency — pure Go stdlib cryptography
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

The barrier is accessible through the Go SDK via `client.Barrier()`, which returns a `transport.BarrierClient`. All transports (REST, gRPC, UNIX, MCP, embedded) implement this interface.

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

barrier := client.Barrier()
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

#### Initialize with Shamir M-of-N

Initialize with Shamir secret sharing. The returned shares must be distributed to key holders and stored securely.

```go
resp, err := barrier.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{
    Secret:      "root-secret",
    Threshold:   3,
    TotalShares: 5,
})
if err != nil {
    return err
}
// resp.Shares contains 5 key shares; distribute to separate custodians.
// Unseal requires any 3 of the 5 shares.
for i, share := range resp.Shares {
    fmt.Printf("Share %d: %s\n", i, share)
}
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

Rekey replaces existing Shamir shares with a new set. The master key is unchanged; only the shares are rotated. Use this after a share custodian change.

```go
resp, err := barrier.BarrierRekey(ctx, &transport.BarrierRekeyRequest{
    Threshold: 2,
    Total:     3,
})
if err != nil {
    return err
}
// resp.Shares contains the new shares. Distribute and discard the old ones.
```

#### Recovery Keys

Recovery keys provide an independent Shamir split of the DEK for disaster recovery when the primary shares are unavailable.

```go
// Generate recovery keys (typically done once after initial setup).
keys, err := barrier.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{
    Threshold: 2,
    Total:     3,
})
if err != nil {
    return err
}
// Store keys.Keys offline in separate secure locations.

// Check whether recovery keys exist.
status, err := barrier.BarrierHasRecoveryKeys(ctx)
if err != nil {
    return err
}
fmt.Printf("has recovery keys: %v\n", status.HasKeys)

// Unseal using recovery keys during disaster recovery.
err = barrier.BarrierRecoverWithKeys(ctx, &transport.BarrierRecoverWithKeysRequest{
    Keys: []string{"key-A", "key-B"},
})

// Remove recovery keys when no longer needed.
err = barrier.BarrierDeleteRecoveryKeys(ctx)
```

#### Root Token Generation

Generate a one-time root token using Shamir shares. This is used to recover admin access without a stored password.

```go
resp, err := barrier.BarrierGenerateRootToken(ctx, &transport.BarrierGenerateRootTokenRequest{
    Shares: []string{"share-A", "share-B", "share-C"},
})
if err != nil {
    return err
}
fmt.Printf("root token: %s (created: %s)\n", resp.Token, resp.CreatedAt)
```

#### Seal

Seal the barrier to revoke all data access without restarting the node.

```go
err := barrier.BarrierSeal(ctx)
```

#### Shamir Share Management

```go
// List share metadata (does not expose share values).
shares, err := barrier.BarrierShamirListShares(ctx)
fmt.Printf("shares: %d/%d (threshold %d)\n", shares.Count, shares.Total, shares.Threshold)

// Verify share integrity.
err = barrier.BarrierShamirVerify(ctx)

// Delete a single share by zero-based index.
err = barrier.BarrierShamirDeleteShare(ctx, &transport.BarrierShamirDeleteShareRequest{
    Index: 2,
})

// Delete all stored shares.
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

Successful mutation endpoints return `204 No Content`. Endpoints that return data respond with `200 OK` and a JSON body. Errors follow the standard `{"error": "..."}` envelope.

## See Also

- [go-quicraft docs/06-crypto.md](https://github.com/jeremyhahn/go-quicraft/blob/main/docs/06-crypto.md) — Minimal barrier, memguard, epoch rotation
- [go-xkms docs/seal/README.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/seal/README.md) — Hardware sealing strategies
