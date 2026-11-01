# Bootstrap Initialization

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Pointer table to the feature folders that own each startup step.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

This document describes the startup and initialization sequence for a fresh go-qrdb daemon (`qrdbd`).

go-qrdb is the **single owner** of bootstrap, first-boot, ceremony, the user/identity model, and the barrier. Higher-level projects (go-xkms, go-quicbaas) drive these only through `go-qrdb/sdk/go` — never by importing `pkg/*` directly or reimplementing them.

## Barrier protections (canonical)

A barrier is protected by exactly one of three core strategies:

| Protection | Credential | Notes |
|------------|-----------|-------|
| `none` | none | Identity passthrough; data stored in cleartext. Dev/test or OS-level FDE. |
| `passphrase` | admin-supplied passphrase | KDF'd (Argon2id, or PBKDF2-SHA256 in FIPS 140 mode — see `seal.FIPSEnabled`). Supplied at `Initialize` and every `Unseal`; **never stored in config**. A `seal.SoftwareStrategy` built by `seal.NewDeferredSoftwareStrategy`. |
| `shamir` | M-of-N shares | Shares enrolled via the M-of-N ceremony. |

Hardware and KMS strategies are not core strategies: go-xkms provides them and registers them into qrdb's `StrategyRegistry` at composition time (`server.WithStrategyRegistry`). See [seal/README.md](seal/README.md).

### System vs per-tenant barriers

- **System barrier** holds the **system tenant** (control plane: tenant records, and the identities of every tenant that does not keep them behind its own barrier, `tenant.IAMBehindTenantBarrier`). `bootstrap.BootstrapSystem` initializes it per `SystemConfig.Strategy`; for `passphrase` and `shamir` it is initialized and unsealed beforehand, through the admin barrier API or the ceremony.
- **Per-tenant barriers** are registered when a tenant is created with `barrier`, `volume` or `raft` isolation (`tenant.TenantService`, `seal.BarrierRegistry.RegisterBarrierTenant`). A system-managed (auto-unseal) tenant's DEK is wrapped under the system barrier and its barrier is initialized at once. A self-managed tenant's barrier is registered sealed and uninitialized: the tenant admin initializes it with `none` or `passphrase` (`bootstrap.InitializeTenantBarrier`), or with `shamir` over the tenant's enrolled share holders. The system admin holds no key for a self-managed tenant's barrier.

## Bootstrap entry points (`pkg/bootstrap`)

System admin enrollment is certificate-only: the ceremony (`ceremony.CeremonyService.Initialize`) signs the admin's CSR, adopts the admin's own certificate or generates one, and first boot binds its SPKI thumbprint to the admin (`SystemConfig.AdminCertPEM`; a composer that drives the CA itself sets `SystemConfig.AdminCSRPEM`). Tenant admin enrollment follows `TenantProvisionConfig.AdminEnrollment`: `cert` (the default) signs the admin's CSR or verifies its own certificate against the tenant's trust anchors and binds its SPKI thumbprint; `deferred` creates the admin pending, for an invitation (`pkg/invite`) that the composing host layers on top. No key is generated for a tenant admin. The control-plane (system) and data-plane (tenant) scopes are orchestrated through the `pkg/bootstrap` entry points:

- **System bootstrap** (`bootstrap.BootstrapSystem`) — system barrier + system tenant + system admin + admin-mTLS.
- **Provision tenant** (`bootstrap.ProvisionTenant`) — tenant record, tenant-scoped built-in roles, tenant admin and its credential. It initializes no tenant barrier.
- **Initialize tenant barrier** (`bootstrap.InitializeTenantBarrier`) — tenant-admin self-service: register (if needed) and initialize THIS tenant's barrier. The caller's authorization is checked at the API layer, not here.

See [identity-rbac/architecture.md](identity-rbac/architecture.md) for the user model and SPKI binding.

## Startup sequence

`qrdbd start` runs one sequence on first boot and on every restart; only the
first run initializes. The steps, in order: configuration loading, TLS
certificates, barrier initialization and unsealing (while the barrier is sealed
an API-only listener serves the seal endpoints and the data plane waits),
storage and Raft initialization, role and user bootstrap, multi-protocol
servers start. Each step belongs to a feature folder, which is the contract for it:

| Step | Owner |
|------|-------|
| Configuration, listeners and flags | [12-configuration.md](12-configuration.md), [api-server/cli.md](api-server/cli.md) |
| Barrier initialization and unsealing | [barrier-seal/architecture.md](barrier-seal/architecture.md), [seal/README.md](seal/README.md) |
| M-of-N share enrollment and claim | [ceremony/architecture.md](ceremony/architecture.md), [shamir-custody/architecture.md](shamir-custody/architecture.md) |
| First admin, built-in roles, idempotency | [firstboot/architecture.md](firstboot/architecture.md) |
| Joining an existing cluster | [clustering/architecture.md](clustering/architecture.md), [clustering/cli.md](clustering/cli.md) |

First-boot orchestration is idempotent: roles and the admin user are created
once, and a restart skips every step that already completed.
