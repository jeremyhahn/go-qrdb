<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Multi-Tenant Barriers

The barrier supports per-tenant encryption isolation through `BarrierRegistry` and `TenantBarrier`. Each tenant gets an independent DEK derived from a unique deployment ID. Isolation is controlled via the `IsolationType` field in `TenantInfo`.

## Isolation Types

A tenant's isolation mode is set on `TenantInfo.IsolationType`. This page focuses
on the **cryptographic barrier** behavior of each mode; the full isolation ladder
(including its storage and Raft implications, quotas, and entitlements) is
documented in [../multi-tenancy.md](../multi-tenancy/architecture.md).

| Strategy | Keyspace Prefixing | Cryptographic Barrier | Notes |
|----------|--------------------|-----------------------|-------|
| **IsolationPrefix** (`prefix`, default) | Yes (`tenantID:key`) | No | Logical separation only, no at-rest encryption. Lowest cost. Shared system barrier is immutable; enable-encryption rejected via `ErrSharedBarrierImmutable`. |
| **IsolationBarrier** (`barrier`) | Yes (`tenantID:key`) | Yes (per-tenant DEK over IAM data) | Per-tenant barrier over the tenant's IAM data; KV data plane still rides the shared shard. |
| **IsolationVolume** (`volume`) | n/a (own DataRoot) | Yes (per-tenant DEK over all data) | Per-tenant local Raft host on a dedicated filesystem `DataRoot`; all data, including the WAL, encrypted under the tenant DEK. |
| **IsolationRaft** (`raft`) | n/a (own DataRoot) | Yes (per-tenant DEK over SM data) | Dedicated Raft group per database; SM data under the tenant `DataRoot` + DEK per node, raft log in the shared host LogDB (encrypted under system DEK). Multi-node replication is production-ready: replicas are placed across distinct healthy nodes, keys are securely transported to each node via HPKE+mTLS, and replicas auto-start after restart. |

The default (`IsolationPrefix`) provides separate keyspaces via namespace
prefixing with no per-tenant crypto layer. When the seal subsystem is not
configured, the dedicated-barrier modes cannot be registered (the crypto layer is
unavailable), and the daemon must have `storage.volume_pools` configured for any
of them.

### Operational requirements

A per-tenant cryptographic barrier (an `IsolationBarrier` tenant, or `POST /v1/tenants/{id}/barrier/init` for any tenant) is backed by a dedicated per-tenant store allocated from a storage **volume pool**. The daemon must therefore have `storage.volume_pools` configured; without one, barrier registration fails with `volume pool not configured on barrier registry`, and creating an `IsolationBarrier` tenant fails before the volume pool is wired.

Per the platform architecture the **tenant admin chooses the per-tenant barrier strategy** — `none` / `passphrase` / `shamir` (or an injected hardware strategy) — via `POST /v1/tenants/{id}/barrier/init` (`TenantBarrierInitWithStrategy`), defaulting to `passphrase` for back-compat. The passphrase strategy requires a passphrase (no derived fallback).

Per-tenant `shamir` is fully supported and follows the **share-holder model** of [shamir-custody](../shamir-custody/): share material is never returned from init or rekey. Tenant barriers wrap `StorageBarrier`, so they inherit **unseal-key indirection** and the **holder roster**: the tenant's holders' shares carry a random unseal key that AEAD-wraps the tenant root key (wrapped blob at `tenant/<id>/root-key.wrapped`, bound to its path and roster through the AEAD associated data), and a tenant rekey cryptographically revokes all prior tenant shares. The flow:

1. **Enrol tenant holders.** `POST /v1/tenants/{id}/barrier/officers` (`{username, role, csr_pem, …}`) has the ceremony CA issue each holder a certificate with a CA-assigned identity, `CN=<username>`, `OU=[<tenant>, <profile>]`, no SAN, which authenticates with no roles. `GET /v1/tenants/{id}/barrier/officers` lists them and their claim state.
2. **Initialize.** `POST /v1/tenants/{id}/barrier/init` with `strategy: "shamir"` (and `threshold`/`shares`, where `shares` equals the enrolled holder count) splits the tenant unseal key and seals one HPKE envelope per holder into a `secure.Secret`. The response is **share-free** (`{threshold, total_shares, share_count}`).
3. **Claim.** Each holder claims its envelope exactly once over a connection presenting its certificate: `POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin` (nonce) → sign → `.../claim/complete`. The claim routes are **identity-gated**: the handler matches the presented certificate against the **target tenant's** holder registry, so a cross-tenant claim fails.
4. **Unseal.** M holders each submit to `POST /v1/tenants/{id}/barrier/quorum/submit` with `op: unseal`, after a challenge from `.../quorum/begin`; each is counted once, authenticated against the tenant roster. Each tenant barrier owns its own quorum rounds, so quorum state is isolated per tenant. The single-credential `POST /v1/tenants/{id}/barrier/unseal` is for the credential strategies and refuses a Shamir barrier.
5. **Seal.** Sealing a Shamir tenant barrier also requires a quorum: single-admin `POST /v1/tenants/{id}/barrier/seal` is refused, and M holders submit with `op: seal` instead. `none`/`passphrase` tenant barriers seal directly.

`GET /v1/tenants/{id}/barrier/status` reports `shamir_threshold`/`shamir_total` for shamir barriers. The Tenants admin UI offers all three strategies (default `passphrase`); its Shamir holder screens have not yet moved to the share-holder API, so tenant holders use the CLI (`qrdb tenant barrier-officer …`, `barrier-quorum …`), the SDKs or the desktop application.

Because a cryptographic barrier requires the volume-pool configuration above, the Tenants admin UI defaults new tenants to `prefix` isolation (which needs no pool).

## Tenant barrier key management

A dedicated-isolation tenant (`IsolationBarrier` or `IsolationVolume`) chooses **how its barrier's key is managed** at creation time. The choice is recorded on the tenant as `TenantInfo.BarrierAutoUnseal` and does not change the cryptography of the barrier itself — only *who holds the unseal authority*.

| Mode | `barrier_auto_unseal` | Unseal authority | Lifecycle | Use case |
|------|-----------------------|------------------|-----------|----------|
| **Self-managed** (default) | `false` | The tenant admin (passphrase / Shamir / hardware strategy) | Created **sealed and uninitialized**; the tenant admin initializes and unseals it, and must re-unseal after every daemon restart | Strongest separation. The operator and the system hold no tenant key. |
| **System-managed auto-unseal** (opt-in) | `true` | The **system barrier** (strategy id `system-barrier`) | Auto-initialized at create and auto-unsealed credential-free at every boot | Convenience for low-security tenants that must come up unattended. |

### Self-managed (default, strongest separation)

When `barrier_auto_unseal` is `false` (the default), `CreateTenant` registers the per-tenant barrier but does **not** initialize it — the barrier comes up **sealed**. The tenant admin then drives the barrier lifecycle through the tenant-barrier API:

1. **Initialize** — `POST /v1/tenants/{id}/barrier/init` (`TenantBarrierInitWithStrategy`) with the chosen strategy (`passphrase` default, `none`, `shamir`, or an injected hardware strategy). See [Per-Tenant Seal Strategy Selection](#per-tenant-seal-strategy-selection-2026-06-02).
2. **Unseal** — `POST /v1/tenants/{id}/barrier/unseal` (single key) or, for Shamir, the holder quorum (`POST /v1/tenants/{id}/barrier/quorum/submit`).

The per-tenant data Host is built **lazily, only after the barrier is unsealed**. After a daemon restart the barrier comes up sealed again and the tenant's data is unreachable until it is re-unsealed. This is the strongest posture: the infrastructure operator and the system barrier hold no copy of the tenant key, so they cannot read the tenant's data at rest.

### System-managed auto-unseal (opt-in convenience)

When `barrier_auto_unseal` is `true`, the tenant root key is AEAD-wrapped under the **system barrier's** master key using the `system-barrier` sealing strategy (the locked "Single KEK/DEK" custody model — see [multi-tenant-isolation-quota.md](../design/multi-tenant-isolation-quota.md)). The barrier is **auto-initialized at create** and **auto-unsealed credential-free at boot** whenever the system barrier is unsealed (which it always is after the daemon's own unseal). No per-tenant credential is held or required.

This trades separation for convenience: the system custodies the tenant key, so a system-tenant operator can reach the tenant's data, and the barrier survives an unattended restart. **It is not suitable for high-security tenants** — those should stay on the self-managed default.

**Enable-encryption from auto-unseal:** A tenant on the `StrategySystemBarrier` strategy (auto-unseal) can enable encryption by migrating to a keyed strategy (passphrase/shamir). This is a credential-free migration source, just like `StrategyNone`, because the system barrier custody means the credential-loading path is moot. The tenant barrier transitions from system-managed to self-managed at this point, and the tenant admin becomes responsible for the new credential.

System-managed auto-unseal is gated by the `AllowSystemManagedBarrier` entitlement (feature id `system_managed_barrier`). A create request that sets `barrier_auto_unseal: true` for a tenant whose plan does not grant the entitlement is rejected with an `EntitlementError`; the self-managed default is always permitted.

### Granting the entitlement (operator)

An operator grants or denies the entitlement per tenant, or sets a deployment-wide default:

- **Per tenant** — `PATCH /v1/tenants/{id}/entitlements` with `allow_system_managed_barrier: true`, or:

  ```bash
  qrdb tenant set-entitlement acme \
    --isolation-modes prefix,barrier,volume \
    --allow-system-managed-barrier
  ```

- **Deployment default** — `tenant.default_entitlements.allow_system_managed_barrier` in the YAML config applies to any tenant whose own entitlements are unset:

  ```yaml
  tenant:
    default_entitlements:
      allowed_isolation_modes: [prefix, barrier, volume]
      allow_volume_storage: true
      allow_system_managed_barrier: false   # secure default: deny
  ```

The built-in default is `false` (deny) — a tenant must be explicitly entitled before it can opt into system-managed auto-unseal.

### Choosing the mode at tenant creation

The mode is selected on the create call and cannot be changed afterward without recreating the tenant.

- **REST** — `POST /v1/tenants` with the `barrier_auto_unseal` boolean:

  ```bash
  # Self-managed barrier tenant (default; field may be omitted)
  curl -X POST https://localhost:8443/v1/tenants \
    --cert system-admin.pem --key system-admin-key.pem \
    -H 'Content-Type: application/json' \
    -d '{"id":"acme","name":"Acme Corp","isolation_type":"barrier"}'

  # System-managed auto-unseal (requires the entitlement)
  curl -X POST https://localhost:8443/v1/tenants \
    --cert system-admin.pem --key system-admin-key.pem \
    -H 'Content-Type: application/json' \
    -d '{"id":"acme","name":"Acme Corp","isolation_type":"barrier","barrier_auto_unseal":true}'
  ```

- **CLI** — the `--barrier-auto-unseal` flag on `qrdb tenant create`:

  ```bash
  # Self-managed (default)
  qrdb tenant create --id acme --name "Acme Corp" --isolation-type barrier

  # System-managed auto-unseal
  qrdb tenant create --id acme --name "Acme Corp" --isolation-type volume --barrier-auto-unseal
  ```

- **Go SDK** — the `BarrierAutoUnseal` field on `transport.CreateTenantRequest` (carried across all transports), or the `tenant.WithBarrierAutoUnseal` option for the embedded `TenantServicer`:

  ```go
  _, err := client.Tenants().CreateTenant(ctx, &transport.CreateTenantRequest{
      ID:                "acme",
      Name:              "Acme Corp",
      IsolationType:     transport.TenantIsolationBarrier,
      BarrierAutoUnseal: true, // opt into system-managed auto-unseal
  })
  ```

- **TypeScript SDK** — the `barrierAutoUnseal` field on `CreateTenantRequest`:

  ```ts
  await client.tenants.create({
      id: 'acme',
      name: 'Acme Corp',
      isolationType: 'barrier',
      barrierAutoUnseal: true,
  });
  ```

The `barrier_auto_unseal` create field and the `allow_system_managed_barrier` entitlement field are carried identically across REST, gRPC, QUIC, MCP, Unix, and embedded transports.

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
  the system `StorageBarrier`'s encrypting FS, so vectors, metadata, and index data are
  encrypted at rest under that barrier's (per-node) DEK — the same model as the KV/MVCC
  store (not a per-tenant vector DEK). Tenant *isolation* of vectors comes from tenant-partitioned
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
| `TenantBarrierInit(ctx, *TenantBarrierInitRequest) (*TenantBarrierInitResponse, error)` | Initialize a per-tenant barrier. Share-free response: `{Threshold, TotalShares, ShareCount}` for shamir. |
| `TenantBarrierUnseal(ctx, *TenantBarrierUnsealRequest) error` | Unseal with the barrier credential (`Key`); refused for Shamir. |
| `TenantBarrierSeal(ctx, *TenantBarrierSealRequest) error` | Seal a previously-initialized barrier (`none`/`passphrase`; Shamir seals through the holder quorum). |
| `TenantBarrierStatus(ctx, *TenantBarrierStatusRequest) (*TenantBarrierStatusResponse, error)` | Report `Sealed`, `Initialized`, `Strategy`, `HardwareBacked`. |
| `TenantBarrierOfficerRegister(ctx, *TenantOfficerRegisterRequest) (*OfficerRegisterResponse, error)` | Enrol a tenant share holder (CSR → CA-issued certificate). |
| `TenantBarrierOfficerList(ctx, tenantID) ([]*OfficerStatus, error)` | List tenant holders and their claim state (never share bytes). |
| `TenantBarrierShareClaimBegin(ctx, *TenantShareClaimBeginRequest) (*ShareClaimBeginResponse, error)` | Issue the holder's claim nonce. |
| `TenantBarrierShareClaimComplete(ctx, *TenantShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)` | Verify the nonce signature; deliver the sealed envelope once. |
| `TenantBarrierQuorumBegin(ctx, *TenantQuorumBeginRequest) (*QuorumChallenge, error)` | Issue a holder's factor challenge. |
| `TenantBarrierQuorumSubmit(ctx, *TenantQuorumSubmitRequest) (*QuorumOutcome, error)` | Submit a holder's share to the open round. |
| `TenantBarrierRekeyStart(ctx, *TenantRekeyStartRequest) (*QuorumRound, error)` | Open a quorum-gated rekey round. |
| `TenantBarrierOfficerRevoke(ctx, *TenantOfficerRevokeRequest) (*QuorumRound, error)` | Revoke a holder through a quorum-gated rekey. |

`TenantBarrierInitRequest` carries an optional `strategy` field (empty = passphrase default):

```go
type TenantBarrierInitRequest struct {
    TenantID   string // required
    Passphrase []byte // required for "passphrase" strategy; base64 on the REST wire
    Threshold  int    // Shamir threshold
    Shares     int    // Shamir shares
    Strategy   string // seal strategy ID; empty = "passphrase"
}
```

`Passphrase` is `[]byte`, matching `TenantBarrierUnsealRequest.Key`. A Go
string is immutable and so cannot be zeroized; a byte slice can, and both ends
do — the caller wipes what it supplied, the daemon wipes what it decoded. The
REST and QUIC JSON encodings therefore carry it as base64.

### Per-protocol wire surface

| Protocol | Server-side | Wire format | Strategy field |
|----------|-------------|-------------|----------------|
| **REST** | `POST /v1/tenants/{id}/barrier/{init,unseal,seal}`, `GET /v1/tenants/{id}/barrier/status`, holder routes under `/v1/tenants/{id}/barrier/officers`, `/quorum/{begin,submit}`, `/rekey/start` | JSON | `strategy` in body |
| **gRPC** | `qrdb.v1.TenantBarrierService/{Init,Unseal,Seal,Status,OfficerRegister,OfficerList,ShareClaimBegin,ShareClaimComplete,QuorumBegin,QuorumSubmit,RekeyStart,OfficerRevoke}` | JSON-over-gRPC | `strategy` in JSON payload |
| **QUIC** | `tenant.BarrierInit`, `tenant.BarrierUnseal`, `tenant.BarrierSeal`, `tenant.BarrierStatus`, `tenant.BarrierOfficerRegister/OfficerList/OfficerRevoke`, `tenant.BarrierShareClaimBegin/ShareClaimComplete`, `tenant.BarrierQuorumBegin/QuorumSubmit`, `tenant.BarrierRekeyStart` | JSON | `strategy` in JSON frame |
| **MCP** | `qrdb_tenant_barrier_init`, `..._unseal`, `..._seal`, `..._status`, `..._officer_register/officer_list/officer_revoke`, `..._share_claim_begin/share_claim_complete`, `..._quorum_begin/quorum_submit`, `..._rekey_start` | JSON-RPC 2.0 | `strategy` tool arg |
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

## See Also

- [../multi-tenancy.md](../multi-tenancy/architecture.md) — isolation ladder, quotas, and entitlements
- [../scoping.md](../scoping.md) — request-time tenant + database scoping
- [shamir-custody](../shamir-custody/) — share holders, unseal-key indirection and the authenticated quorum
