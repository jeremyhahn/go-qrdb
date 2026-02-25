<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Multi-Tenant Barriers

The barrier supports per-tenant encryption isolation through `BarrierRegistry` and `TenantBarrier`. Each tenant gets an independent DEK derived from a unique deployment ID.

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
