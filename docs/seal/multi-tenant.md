# Multi-Tenant Barriers

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-22
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

The barrier supports per-tenant encryption isolation through `BarrierRegistry` and `TenantBarrier`. Each tenant gets its own root key (`tenant/<id>/root-key`), DEK and `{tenantID}/` storage namespace. Isolation is controlled via the `IsolationType` field in `TenantInfo`.

## Isolation Types

A tenant's isolation mode is set on `TenantInfo.IsolationType`. This page focuses
on the **cryptographic barrier** behavior of each mode; the full isolation ladder
(including its storage and Raft implications, quotas, and entitlements) is
documented in [multi-tenancy/architecture.md](../multi-tenancy/architecture.md).

| Strategy | Keyspace Prefixing | Cryptographic Barrier | Notes |
|----------|--------------------|-----------------------|-------|
| **IsolationPrefix** (`prefix`, default) | Yes (`tenantID:key`) | No | Logical separation only, no at-rest encryption. Lowest cost. Shared system barrier is immutable; enable-encryption rejected via `ErrSharedBarrierImmutable`. |
| **IsolationBarrier** (`barrier`) | Yes (`tenantID:key`) | Yes (per-tenant DEK over IAM data) | Per-tenant barrier over the tenant's IAM data; KV data plane still rides the shared shard. |
| **IsolationVolume** (`volume`) | n/a (own DataRoot) | Yes (per-tenant DEK over all data) | Per-tenant local Raft host on a dedicated filesystem `DataRoot`; all data, including the WAL, encrypted under the tenant DEK. |
| **IsolationRaft** (`raft`) | n/a (own DataRoot) | Yes (per-tenant DEK over SM data) | Dedicated Raft group per database; SM data under the tenant `DataRoot` + DEK per node, raft log in the shared host LogDB (encrypted under system DEK). Replicas are placed across distinct healthy nodes, the tenant root key reaches each replica node over HPKE+mTLS, and replicas start again after a restart. |

The default (`IsolationPrefix`) provides separate keyspaces via namespace
prefixing with no per-tenant crypto layer. When the seal subsystem is not
configured, the dedicated-barrier modes cannot be registered (the crypto layer is
unavailable), and the daemon must have `storage.volume_pools` configured for any
of them.

### Operational requirements

A per-tenant cryptographic barrier (an `IsolationBarrier` tenant, or `POST /v1/tenants/{id}/barrier/init/begin` + `/init/complete` for any tenant) is backed by a dedicated per-tenant store allocated from a storage **volume pool**. The daemon must therefore have `storage.volume_pools` configured; without one, barrier registration fails with `seal.ErrPoolNotConfigured` (`volume pool not configured on barrier registry`), so creating an `IsolationBarrier` tenant fails.

The **tenant admin chooses the per-tenant barrier strategy** — `none` / `passphrase` / `shamir` (or an injected hardware strategy) — via `POST /v1/tenants/{id}/barrier/init/begin` + `/init/complete` (`TenantBarrierInitWithStrategy`); an empty strategy means `passphrase`. The passphrase strategy requires a passphrase (no derived fallback).

Per-tenant `shamir` is fully supported and follows the **share-holder model** of [shamir-custody](../shamir-custody/): share material is never returned from init or rekey. Tenant barriers wrap `StorageBarrier`, so they inherit **unseal-key indirection** and the **holder roster**: the tenant's holders' shares carry a random unseal key that AEAD-wraps the tenant root key (wrapped blob at `tenant/<id>/root-key.wrapped`, bound to its path and roster through the AEAD associated data), and a tenant rekey cryptographically revokes all prior tenant shares. A tenant seal reaches every replica once the cluster has activated `cluster-seal`; an unseal acts on the node that receives it — see [../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster). The flow:

1. **Enrol tenant holders.** `POST /v1/tenants/{id}/barrier/officers` (`{username, role, csr_pem, …}`) has the ceremony CA issue each holder a certificate with a CA-assigned identity, `CN=<username>`, `OU=[<tenant>, <profile>]`, no SAN, which authenticates with no roles. `GET /v1/tenants/{id}/barrier/officers` lists them and their claim state.
2. **Initialize.** `POST /v1/tenants/{id}/barrier/init/begin` + `/init/complete` with `strategy: "shamir"` (and `threshold`/`shares`, where `shares` equals the enrolled holder count) splits the tenant unseal key and seals one HPKE envelope per holder into a `secure.Secret`. The response is **share-free** (`{threshold, total_shares, share_count}`).
3. **Claim.** Each holder claims its envelope exactly once over a connection presenting its certificate: `POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin` (nonce) → sign → `.../claim/complete`. The claim routes are **identity-gated**: the handler matches the presented certificate against the **target tenant's** holder registry, so a cross-tenant claim fails.
4. **Unseal.** M holders each submit to `POST /v1/tenants/{id}/barrier/quorum/submit` with `op: unseal`, after a challenge from `.../quorum/begin`; each is counted once, authenticated against the tenant roster. Each tenant barrier owns its own quorum rounds, so quorum state is isolated per tenant. The single-credential `POST /v1/tenants/{id}/barrier/unseal` is for the credential strategies and refuses a Shamir barrier.
5. **Seal.** Sealing a Shamir tenant barrier also requires a quorum: single-admin `POST /v1/tenants/{id}/barrier/seal` is refused, and M holders submit with `op: seal` instead. `none`/`passphrase` tenant barriers seal directly.

`GET /v1/tenants/{id}/barrier/status` reports `shamir_threshold`/`shamir_total` for shamir barriers. The Tenants admin UI offers all three strategies (default `passphrase`). Tenant holders enrol, claim and submit quorum shares with the CLI (`qrdb tenant barrier-officer …` and `qrdb tenant barrier-quorum …`, both taking `--tenant-id`), the SDKs or the desktop application.

Because a cryptographic barrier requires the volume-pool configuration above, the Tenants admin UI defaults new tenants to `prefix` isolation (which needs no pool).

## Tenant barrier key management

A dedicated-isolation tenant (`IsolationBarrier` or `IsolationVolume`) chooses **how its barrier's key is managed** at creation time. The choice is recorded on the tenant as `TenantInfo.BarrierAutoUnseal` and does not change the cryptography of the barrier itself — only *who holds the unseal authority*.

| Mode | `barrier_auto_unseal` | Unseal authority | Lifecycle | Use case |
|------|-----------------------|------------------|-----------|----------|
| **Self-managed** (default) | `false` | The tenant admin (passphrase / Shamir / hardware strategy) | Created **sealed and uninitialized**; the tenant admin initializes and unseals it, and must re-unseal after every daemon restart | Strongest separation. The operator and the system hold no tenant key. |
| **System-managed auto-unseal** (opt-in) | `true` | The **system barrier** (strategy id `system-barrier`) | Auto-initialized at create and auto-unsealed credential-free at every boot | Convenience for low-security tenants that must come up unattended. |

### Self-managed (default, strongest separation)

When `barrier_auto_unseal` is `false` (the default), `CreateTenant` registers the per-tenant barrier but does **not** initialize it — the barrier comes up **sealed**. The tenant admin then drives the barrier lifecycle through the tenant-barrier API:

1. **Initialize** — `POST /v1/tenants/{id}/barrier/init/begin` + `/init/complete` (`TenantBarrierInitWithStrategy`) with the chosen strategy (`passphrase` default, `none`, `shamir`, or an injected hardware strategy). See [Per-Tenant Seal Strategy Selection](#per-tenant-seal-strategy-selection).
2. **Unseal** — `POST /v1/tenants/{id}/barrier/unseal` (single key) or, for Shamir, the holder quorum (`POST /v1/tenants/{id}/barrier/quorum/submit`).

The per-tenant data Host is built **lazily, only after the barrier is unsealed**. After a daemon restart the barrier comes up sealed again and the tenant's data is unreachable until it is re-unsealed. This is the strongest posture: the infrastructure operator and the system barrier hold no copy of the tenant key, so they cannot read the tenant's data at rest.

### System-managed auto-unseal (opt-in convenience)

When `barrier_auto_unseal` is `true`, the tenant root key is AEAD-wrapped under the **system barrier's** master key using the `system-barrier` sealing strategy (the single KEK/DEK custody model; see [multi-tenancy/](../multi-tenancy/)). The barrier is **auto-initialized at create** and **auto-unsealed credential-free at boot** whenever the system barrier is unsealed (which it always is after the daemon's own unseal). No per-tenant credential is held or required.

This trades separation for convenience: the system custodies the tenant key, so a system-tenant operator can reach the tenant's data, and the barrier survives an unattended restart. **It is not suitable for high-security tenants** — those should stay on the self-managed default.

**Enable-encryption from auto-unseal:** A tenant on the `StrategySystemBarrier` strategy (auto-unseal) can enable encryption by migrating to a keyed strategy (passphrase/shamir). Like `StrategyNone`, it is a credential-free migration source: the system barrier holds the key, so no credential is loaded. The tenant barrier transitions from system-managed to self-managed at this point, and the tenant admin becomes responsible for the new credential.

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

- **REST** — `POST /v1/tenants` with the `barrier_auto_unseal` boolean. Every create names the first admin (`admin_username`) and carries exactly one of `admin_csr_pem` or `admin_cert_pem`:

  ```bash
  # Self-managed barrier tenant (default; field may be omitted)
  curl -X POST https://localhost:63002/v1/tenants \
    --cert system-admin.pem --key system-admin-key.pem \
    -H 'Content-Type: application/json' \
    -d '{"id":"acme","name":"Acme Corp","isolation_type":"barrier","admin_username":"alice","admin_csr_pem":"<PEM CSR>"}'

  # System-managed auto-unseal (requires the entitlement)
  curl -X POST https://localhost:63002/v1/tenants \
    --cert system-admin.pem --key system-admin-key.pem \
    -H 'Content-Type: application/json' \
    -d '{"id":"acme","name":"Acme Corp","isolation_type":"barrier","barrier_auto_unseal":true,"admin_username":"alice","admin_csr_pem":"<PEM CSR>"}'
  ```

- **CLI** — the `--barrier-auto-unseal` flag on `qrdb tenant create`, which also requires `--admin-username` and one of `--admin-csr` or `--admin-cert`:

  ```bash
  # Self-managed (default)
  qrdb tenant create --id acme --name "Acme Corp" --isolation-type barrier \
    --admin-username alice --admin-csr alice.csr

  # System-managed auto-unseal
  qrdb tenant create --id acme --name "Acme Corp" --isolation-type volume --barrier-auto-unseal \
    --admin-username alice --admin-csr alice.csr
  ```

- **Go SDK** — the `BarrierAutoUnseal` field on `transport.CreateTenantRequest` (carried across all transports), or the `tenant.WithBarrierAutoUnseal` option for the embedded `TenantServicer`:

  ```go
  _, err := client.Tenants().CreateTenant(ctx, &transport.CreateTenantRequest{
      ID:                "acme",
      Name:              "Acme Corp",
      IsolationType:     transport.TenantIsolationBarrier,
      BarrierAutoUnseal: true, // opt into system-managed auto-unseal
      AdminUsername:     "alice",
      AdminCSRPEM:       csrPEM,
  })
  ```

- **TypeScript SDK** — the `barrierAutoUnseal` field on `CreateTenantRequest`:

  ```ts
  await client.tenants.create({
      id: 'acme',
      name: 'Acme Corp',
      isolationType: 'barrier',
      barrierAutoUnseal: true,
      adminUsername: 'alice',
      adminCsrPem: csrPem,
  });
  ```

The `barrier_auto_unseal` create field and the `allow_system_managed_barrier` entitlement field are carried identically across REST, gRPC, QUIC, MCP, Unix, and embedded transports.

## BarrierRegistry

`BarrierRegistry` manages multiple tenant barriers with independent lifecycles:

```go
registry, err := seal.NewBarrierRegistry(logger, systemBarrier)

// Register a tenant with its own root key and DEK
strategy, err := seal.NewSoftwareStrategy(ctx, passphrase)
tb, err := registry.RegisterTenant(ctx, "tenant-a", tenantStorage,
    seal.BarrierConfig{RootKeyPath: "tenant/tenant-a/root-key"},
    strategy,
)

// Initialize and unseal independently
err = registry.InitializeTenant(ctx, "tenant-a", creds)
err = registry.UnsealTenant(ctx, "tenant-a", creds)

// Check status
status, err := registry.TenantStatus("tenant-a")
```

## TenantBarrier

Each `TenantBarrier` wraps its own `*StorageBarrier` over a `NamespacedBackend` that prefixes every key with `<tenantID>/`. Its root key lives at `tenant/<tenantID>/root-key` unless `BarrierConfig.RootKeyPath` names another path, so a compromised tenant key cannot decrypt another tenant's data.

```go
type TenantBarrier struct {
    tenantID string
    barrier  *StorageBarrier // independent barrier (not shared with system)
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

- Each tenant has its own root key and a `<tenantID>/` storage namespace
- DEKs are independently derived — no shared key material between tenants
- A read or write through a sealed tenant barrier returns `ErrSealed`
- System barrier and tenant barriers have independent lifecycles
- **Vector data is barrier-encrypted at rest**: the vector PebbleDB store is wrapped by
  the node's at-rest encryptor over the system `StorageBarrier` (`atrest.New`), so vectors,
  metadata, and index data are encrypted at rest under that barrier's (per-node) DEK — the
  same model as the KV/MVCC store, not a per-tenant vector DEK. Tenant *isolation* of vectors
  comes from tenant-partitioned namespace keys (`tenant-a`'s `embeddings` is stored apart
  from `tenant-b`'s).

## Per-Tenant Seal Strategy Selection

By default, a tenant barrier is sealed with the **passphrase (software)** strategy — the tenant
admin supplies a passphrase at init time and the same passphrase is used at every unseal. An
empty `Strategy` on `TenantBarrierInit` selects it.

To select a different strategy (e.g., a hardware or cloud KMS strategy injected from go-xkms),
set a non-empty `strategy` field in the init request on any transport; the service resolves
it in `TenantBarrierInitWithStrategy`:

```go
// SDK call — select "none" strategy (plaintext, test/dev only). adminKey is
// the first admin's crypto.Signer: over a network transport the SDK proves
// possession of it on the connection (init/begin + init/complete).
_, err := client.Tenants().TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{
    TenantID: "acme",
    Strategy: "none",
    Signer:   adminKey,
})

// Select hardware TPM-backed strategy (requires go-xkms injection at composition time)
_, err = client.Tenants().TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{
    TenantID: "secure-tenant",
    Strategy: "tpm2", // resolved from server's StrategyRegistry
    Signer:   adminKey,
})
```

### Strategy resolution order

The tenant service resolves every strategy through one seam,
`seal.BarrierKeyProvider` (see
[barrier-seal architecture](../barrier-seal/architecture.md#the-barrier-key-seam--barrierkeyprovider)).
It asks for this node's key for `tenant/<id>`, passing the node's identity and
the tenant's opaque `KeyRef` (`tenant.WithBarrierKeyRef`).

1. A provider injected with `WithBarrierKeyProvider` (server option, or
   `lifecycle.Infra.BarrierKeyProvider`) answers. go-xkms supplies hardware- and
   KMS-backed providers; go-qrdb ships none.
2. Otherwise the default provider, `seal.NewBarrierKeyProvider`, resolves the
   name from the `StrategyRegistry` injected with `WithStrategyRegistry`
   (`lifecycle.Infra.StrategyRegistry`), or from the default registry when none
   is: `"passphrase"` (default) and `"none"`.
3. A name the provider does not know returns `InvalidStrategyError` with the
   list of available strategies. Any other provider failure returns
   `BarrierError{Op: "strategy"}` carrying the cause.

### Passphrase requirement by strategy

The provider declares what a client must supply (`CredentialKind`); the tenant
service requires a passphrase exactly when it says `CredentialPassphrase`.

| Strategy | Credential | Notes |
|----------|------------|-------|
| `passphrase` (default) | passphrase | Tenant admin's caller-supplied secret; no derived fallback. |
| `none` | none | Plaintext barrier; use only where OS-level FDE provides equivalent protection. |
| `shamir` | shares | Threshold/shares configured at init time via Shamir flow. |
| injected (`tpm2`, a KMS key, …) | as the provider declares | Root key wrapped by the provider's key; go-qrdb never parses the `KeyRef`. |

### Who can release the key

The provider also declares `Custody`: who can release the key without the
tenant present. When the tenant's strategy wraps under a key the tenant itself
controls, the provider declares `owner`, and an operator with file-system
access cannot unseal the barrier without that key. When the tenant opts into
auto-unseal — `system-barrier`, or a key held on the node — the provider
declares `platform`. That is the tenant's choice to make. The default provider
reports `platform` for every registered strategy it cannot see into; only a
composer that knows the key is the tenant's declares `owner`.

### Wiring the provider into TenantService

```go
// lifecycle.Infra fields (pkg/server/lifecycle)
infra := lifecycle.Infra{
    SealBarrier:        barrier,
    BarrierRegistry:    barrierRegistry,
    BarrierKeyProvider: myProvider,         // xkms-supplied; wins over the registry
    StrategyRegistry:   myStrategyRegistry, // used by the default provider otherwise
    ...
}
node, err := lifecycle.BuildServices(ctx, infra)

// Direct construction
svc, err := tenant.NewTenantService(store, registry, logger,
    tenant.WithBarrierKeyProvider(seal.NewBarrierKeyProvider(myStrategyRegistry)),
    tenant.WithNodeID(nodeID),
)
```

## Transport Surface

Per-tenant barrier ops are exposed across **all six QRDB transports**. Every transport implements the same methods on `transport.TenantClient`:

| Method | Purpose |
|--------|---------|
| `TenantBarrierInit(ctx, *TenantBarrierInitRequest) (*TenantBarrierInitResponse, error)` | Initialize a per-tenant barrier. Share-free response: `{Threshold, TotalShares, ShareCount}` for shamir. |
| `TenantBarrierUnseal(ctx, *TenantBarrierUnsealRequest) error` | Unseal with the barrier credential (`Key`); refused for Shamir. |
| `TenantBarrierSeal(ctx, *TenantBarrierSealRequest) error` | Seal a previously-initialized barrier (`none`/`passphrase`; Shamir seals through the holder quorum). |
| `TenantBarrierStatus(ctx, *TenantBarrierStatusRequest) (*TenantBarrierStatusResponse, error)` | Report `Sealed`, `Initialized`, `Strategy`, `HardwareBacked`, the Shamir parameters, the cluster seal state, this node's key fingerprint and the replica nodes. |
| `TenantBarrierOfficerRegister(ctx, *TenantOfficerRegisterRequest) (*OfficerRegisterResponse, error)` | Enrol a tenant share holder (CSR → CA-issued certificate). |
| `TenantBarrierOfficerList(ctx, tenantID) ([]*OfficerStatus, error)` | List tenant holders and their claim state (never share bytes). |
| `TenantBarrierShareClaimBegin(ctx, *TenantShareClaimBeginRequest) (*ShareClaimBeginResponse, error)` | Issue the holder's claim nonce. |
| `TenantBarrierShareClaimComplete(ctx, *TenantShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)` | Verify the nonce signature; deliver the sealed envelope once. |
| `TenantBarrierQuorumBegin(ctx, *TenantQuorumBeginRequest) (*QuorumChallenge, error)` | Issue a holder's factor challenge. |
| `TenantBarrierQuorumSubmit(ctx, *TenantQuorumSubmitRequest) (*QuorumOutcome, error)` | Submit a holder's share to the open round. The share that completes an unseal quorum leaves the barrier usable and runs the tenant service's barrier-ready hook, as `TenantBarrierUnseal` does. |
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
    Officers   []OfficerRegisterRequest // inline Shamir roster
    Policy     string                   // factor policy for Officers
    Signer     crypto.Signer            // first admin's key; never serialized
}
```

`Signer` is how a caller proves it holds the pending first admin's key. A
network transport asks the node for a single-use nonce (`init/begin`), signs
it bound to the connection's TLS 1.3 exporter, and sends the signature with
the init (`init/complete`), on one connection. A `crypto.Signer` may be
device-held, so the private key never leaves its device; the embedded
transport is in process and does not use it. See
[../multi-tenancy/api.md](../multi-tenancy/api.md#barrier-init-proof-of-possession).

`Passphrase` is `[]byte`, matching `TenantBarrierUnsealRequest.Key`. A Go
string is immutable and so cannot be zeroized; a byte slice can, and both ends
do — the caller wipes what it supplied, the daemon wipes what it decoded. The
REST and QUIC JSON encodings therefore carry it as base64.

### Per-protocol wire surface

| Protocol | Server-side | Wire format | Strategy field |
|----------|-------------|-------------|----------------|
| **REST** | `POST /v1/tenants/{id}/barrier/{init/begin,init/complete,unseal,seal}`, `GET /v1/tenants/{id}/barrier/status`, holder routes under `/v1/tenants/{id}/barrier/officers`, `/quorum/{begin,submit}`, `/rekey/start` | JSON | `strategy` in body |
| **gRPC** | `qrdb.v1.TenantBarrierService/{InitBegin,InitComplete,Unseal,Seal,Status,OfficerRegister,OfficerList,ShareClaimBegin,ShareClaimComplete,QuorumBegin,QuorumSubmit,RekeyStart,OfficerRevoke}` | JSON-over-gRPC | `strategy` in JSON payload |
| **QUIC** | `tenant.BarrierInitBegin`, `tenant.BarrierInitComplete`, `tenant.BarrierUnseal`, `tenant.BarrierSeal`, `tenant.BarrierStatus`, `tenant.BarrierOfficerRegister/OfficerList/OfficerRevoke`, `tenant.BarrierShareClaimBegin/ShareClaimComplete`, `tenant.BarrierQuorumBegin/QuorumSubmit`, `tenant.BarrierRekeyStart` | JSON | `strategy` in JSON frame |
| **MCP** | `qrdb_tenant_barrier_init_begin`, `..._init_complete`, `..._unseal`, `..._seal`, `..._status`, `..._officer_register/officer_list/officer_revoke`, `..._share_claim_begin/share_claim_complete`, `..._quorum_begin/quorum_submit`, `..._rekey_start` | JSON-RPC 2.0 | `strategy` tool arg |
| **Unix** | gRPC-over-UDS reuses `qrdb.v1.TenantBarrierService` | JSON-over-gRPC | `strategy` in JSON payload |
| **Embedded** | Direct `tenant.TenantServicer` call | Go function call | `req.Strategy` field |

Server registration: each protocol gates on a non-nil `tenant.TenantServicer`. All transports
forward the `strategy` field to `TenantBarrierInitWithStrategy`. Omitting the field (or setting
it to `""`) selects the passphrase strategy.

`TenantBarrierStatusResponse` shape:

```go
type TenantBarrierStatusResponse struct {
    TenantID        string
    Sealed          bool
    Initialized     bool
    Strategy        string // active StrategyID:
                           //   qrdb-owned:    "passphrase" | "shamir" | "none"
                           //   xkms-injected: "tpm2" | "pkcs11" | "aws-kms" | ...
    HardwareBacked  bool
    ShamirThreshold int                 // zero unless Shamir
    ShamirTotal     int                 // zero unless Shamir
    ClusterSeal     *ClusterSeal        // the barrier's cluster seal state
    KeyFingerprint  string              // the answering node's own key; empty while sealed
    Nodes           []TenantBarrierNode // replica nodes; empty on a standalone node
}
```

## See Also

- [multi-tenancy/architecture.md](../multi-tenancy/architecture.md) — isolation ladder, quotas, and entitlements
- [../scoping.md](../scoping.md) — request-time tenant + database scoping
- [shamir-custody](../shamir-custody/) — share holders, unseal-key indirection and the authenticated quorum
