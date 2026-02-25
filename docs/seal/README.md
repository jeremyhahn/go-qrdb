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
        ↑
go-xkms/pkg/seal/              Hardware strategies: TPM2, PKCS#11, cloud KMS
```

- **go-quicraft** defines the barrier interfaces (`Barrier`, `SnapshotBarrier`, `EpochTracker`) and a minimal default implementation with AES-256-GCM, epoch rotation, deployment ID isolation, brute-force protection, and memguard memory protection.
- **go-qrdb** embeds go-quicraft's barrier in `ExtendedBarrier`, adding all database-layer features.
- **go-xkms** adds hardware `SealingStrategy` implementations that plug into any layer.

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

## See Also

- [go-quicraft docs/06-crypto.md](https://github.com/jeremyhahn/go-quicraft/blob/main/docs/06-crypto.md) — Minimal barrier, memguard, epoch rotation
- [go-xkms docs/seal/README.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/seal/README.md) — Hardware sealing strategies
