<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Multi-Tenant Barriers

The barrier supports per-tenant encryption isolation through `BarrierRegistry` and `TenantBarrier`. Each tenant gets an independent DEK derived from a unique deployment ID. Isolation is controlled via the `IsolationType` field in `TenantInfo`.

## Isolation Types

Tenants can use one of two isolation strategies, configured per tenant:

| Strategy | Keyspace Prefixing | Cryptographic Barrier | Use Case |
|----------|--------------------|-----------------------|----------|
| **IsolationBarrier** | Yes (`tenantID:key`) | Yes (per-tenant DEK) | Default. Full encryption + namespace isolation. Strongest isolation. |
| **IsolationPrefix** | Yes (`tenantID:key`) | No | Logical separation only, no at-rest encryption. Lower computational cost. |

Both modes provide separate keyspaces via namespace prefixing. The difference is whether an additional per-tenant cryptographic barrier is layered on top. When the seal subsystem is not configured, `IsolationBarrier` still enforces isolation via the namespace prefix—the crypto layer simply becomes unavailable.

### Operational requirements

A per-tenant cryptographic barrier (an `IsolationBarrier` tenant, or `POST /v1/tenants/{id}/barrier/init` for any tenant) is backed by a dedicated per-tenant store allocated from a storage **volume pool**. The daemon must therefore have `storage.volume_pools` configured; without one, barrier registration fails with `volume pool not configured on barrier registry`, and creating an `IsolationBarrier` tenant fails before the volume pool is wired.

Per-tenant barriers use the **passphrase** strategy (the software strategy qrdb owns for tenants) or an injected hardware strategy. **Shamir is not supported per-tenant** — `POST /v1/tenants/{id}/barrier/init` with `strategy: "shamir"` is rejected with `invalid seal strategy: shamir`. The passphrase is required for the passphrase strategy (there is no derived fallback).

Because barrier isolation requires this additional daemon configuration, the Tenants admin UI defaults new tenants to `prefix` isolation and exposes a passphrase-only barrier-init form.

## BarrierRegistry

`BarrierRegistry` manages multiple tenant barriers with independent lifecycles:

```go
registry := seal.NewBarrierRegistry(systemBarrier)

// Register a tenant with independent DEK
err := registry.RegisterTenantWithConfig(
    "tenant-a",
    tenantStorage,
    seal.BarrierConfig{RootKeyPath: "sys/tenant-a/root-key"},
    seal.NewSoftwareStrategy(),
)

// Initialize and unseal independently
err = registry.InitializeTenant(ctx, "tenant-a", creds)
err = registry.UnsealTenant(ctx, "tenant-a", creds)

// Check status
status, err := registry.TenantStatus("tenant-a")
```

## TenantBarrier

Each `TenantBarrier` wraps its own `*Barrier` instance with a deployment ID derived from the tenant ID. This ensures complete DEK isolation — a compromised tenant key cannot decrypt another tenant's data.

```go
type TenantBarrier struct {
    tenantID string
    barrier  *Barrier  // independent barrier (not shared with system)
}
```

## Lifecycle

| Operation | Description |
|-----------|-------------|
| `InitializeTenant` | Generate root key, derive tenant DEK, unseal |
| `UnsealTenant` | Unseal tenant barrier (supports Shamir shares) |
| `SealTenant` | Seal tenant barrier, zero DEK |
| `TenantStatus` | Report tenant seal state |

## Isolation Guarantees

- Each tenant has a unique `deploymentID` derived from `tenantID`
- DEKs are independently derived — no shared key material between tenants
- Cross-tenant access returns `ErrSealed` (tenant barrier not unsealed for the requester)
- System barrier and tenant barriers have independent lifecycles
- **Vector data is barrier-encrypted at rest**: the vector PebbleDB store is wrapped by
  the cluster `StorageBarrier`'s encrypting FS, so vectors, metadata, and index data are
  encrypted at rest under the cluster DEK — the same model as the KV/MVCC store (not a
  per-tenant vector DEK). Tenant *isolation* of vectors comes from tenant-partitioned
  namespace keys (e.g., `tenant-a`'s `embeddings` is stored separately from `tenant-b`'s),
  preventing cross-tenant collision and access.

## Per-Tenant Seal Strategy Selection (2026-06-02)

By default, a tenant barrier is sealed with the **passphrase (software)** strategy — the tenant
admin supplies a passphrase at init time and the same passphrase is used at every unseal. This
is the backward-compatible default for `TenantBarrierInit`.

To select a different strategy (e.g., a hardware or cloud KMS strategy injected from go-xkms),
call `TenantBarrierInitWithStrategy` via any transport by including a non-empty `strategy`
field in the init request:

```go
// SDK call — select "none" strategy (plaintext, test/dev only)
err := client.TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{
    TenantID:  "acme",
    Passphrase: "",        // ignored for non-passphrase strategies
    Strategy:  "none",
})

// Select hardware TPM-backed strategy (requires go-xkms injection at composition time)
err = client.TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{
    TenantID: "secure-tenant",
    Strategy: "tpm2",     // resolved from server's StrategyRegistry
})
```

### Strategy resolution order

1. When the server's `TenantService` was constructed with a `seal.StrategyRegistry`
   (injected via `WithStrategyRegistry` in lifecycle `Infra.StrategyRegistry` or the
   server option `WithStrategyRegistry`), the strategy is resolved from the registry.
   This enables hardware/cloud strategies (tpm2, pkcs11, aws-kms, gcp-kms, azure-kv,
   vault) registered by go-xkms at composition time.
2. When no registry is injected, only the built-in software strategies are accepted:
   `"passphrase"` (default) and `"none"`.
3. An unregistered strategy name returns `InvalidStrategyError` with the list of
   available strategies.

### Passphrase requirement by strategy

| Strategy | Passphrase required? | Notes |
|----------|---------------------|-------|
| `passphrase` (default) | Yes | Tenant admin's caller-supplied secret; no derived fallback. |
| `none` | No | Plaintext barrier; use only where OS-level FDE provides equivalent protection. |
| `shamir` | No (shares at unseal) | Threshold/shares configured at init time via Shamir flow. |
| `tpm2`, `pkcs11`, `aws-kms`, etc. | No | Root key wrapped by hardware/cloud KEK; operator cannot unseal without the external backend. |

### Security property: operator isolation

When a hardware or cloud-KMS strategy is selected for a tenant barrier, the tenant root key is
wrapped by a KEK that lives outside the qrdb process (TPM, HSM, cloud KMS). An infrastructure
operator with file-system access cannot unseal the tenant's barrier without the external
credential, realising **"per-tenant hardware/cloud-rooted barrier — the infrastructure
operator cannot access the tenant's keys"**.

### Wiring the StrategyRegistry into TenantService

```go
// lifecycle.Infra field (pkg/server/lifecycle)
infra := lifecycle.Infra{
    SealBarrier:      barrier,
    BarrierRegistry:  barrierRegistry,
    StrategyRegistry: myStrategyRegistry, // inject xkms-populated registry here
    ...
}
node, err := lifecycle.BuildServices(ctx, infra)

// Direct construction
svc, err := tenant.NewTenantService(store, registry, logger,
    tenant.WithStrategyRegistry(myStrategyRegistry),
)
```

## Transport Surface

Per-tenant barrier ops are exposed across **all six QRDB transports** with full client + server
implementations. Every transport implements the same methods on `transport.TenantClient`:

| Method | Purpose |
|--------|---------|
| `TenantBarrierInit(ctx, *TenantBarrierInitRequest) error` | Initialize a per-tenant barrier (passphrase strategy, backward compat). |
| `TenantBarrierUnseal(ctx, *TenantBarrierUnsealRequest) error` | Unseal with `Key` or fall back to `Share`. |
| `TenantBarrierSeal(ctx, *TenantBarrierSealRequest) error` | Seal a previously-initialized barrier. |
| `TenantBarrierStatus(ctx, *TenantBarrierStatusRequest) (*TenantBarrierStatusResponse, error)` | Report `Sealed`, `Initialized`, `Strategy`, `HardwareBacked`. |

`TenantBarrierInitRequest` carries an optional `strategy` field (empty = passphrase default):

```go
type TenantBarrierInitRequest struct {
    TenantID   string // required
    Passphrase string // required for "passphrase" strategy
    Threshold  int    // Shamir threshold
    Shares     int    // Shamir shares
    Strategy   string // seal strategy ID; empty = "passphrase"
}
```

### Per-protocol wire surface

| Protocol | Server-side | Wire format | Strategy field |
|----------|-------------|-------------|----------------|
| **REST** | `POST /v1/tenants/{id}/barrier/{init,unseal,seal}`, `GET /v1/tenants/{id}/barrier/status` | JSON | `strategy` in body |
| **gRPC** | `qrdb.v1.TenantBarrierService/{Init,Unseal,Seal,Status}` | JSON-over-gRPC | `strategy` in JSON payload |
| **QUIC** | `tenant.BarrierInit`, `tenant.BarrierUnseal`, `tenant.BarrierSeal`, `tenant.BarrierStatus` | JSON | `strategy` in JSON frame |
| **MCP** | `qrdb_tenant_barrier_init`, `qrdb_tenant_barrier_unseal`, `qrdb_tenant_barrier_seal`, `qrdb_tenant_barrier_status` | JSON-RPC 2.0 | `strategy` tool arg |
| **Unix** | gRPC-over-UDS reuses `qrdb.v1.TenantBarrierService` | JSON-over-gRPC | `strategy` in JSON payload |
| **Embedded** | Direct `tenant.TenantServicer` call | Go function call | `req.Strategy` field |

Server registration: each protocol gates on a non-nil `tenant.TenantServicer`. All transports
forward the `strategy` field to `TenantBarrierInitWithStrategy`. Omitting the field (or setting
it to `""`) selects the passphrase strategy for full backward compatibility.

`TenantBarrierStatusResponse` shape:

```go
type TenantBarrierStatusResponse struct {
    TenantID       string
    Sealed         bool
    Initialized    bool
    Strategy       string  // active StrategyID:
                            //   qrdb-owned:  "passphrase" | "shamir" | "none"
                            //   xkms-injected: "tpm2" | "pkcs11" | "aws-kms" | ...
    HardwareBacked bool
}
```
