# Bootstrap Initialization

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
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
| `passphrase` | admin-supplied passphrase | KDF'd (Argon2id, or PBKDF2-SHA256 in FIPS 140 mode — see `seal.FIPSEnabled`). Supplied at `Initialize` and every `Unseal`; **never stored in config and never derived from a deployment ID**. Backed by `DeferredSoftwareStrategy`. |
| `shamir` | M-of-N shares | Shares enrolled via the M-of-N ceremony. |

Hardware strategies (`tpm2` / `pkcs11` / cloud KMS / Vault) are xkms-provided extensions of `passphrase`, registered into qrdb's `StrategyRegistry` at composition time — not new core options. See [seal/README.md](seal/README.md).

### System vs per-tenant barriers

- **System barrier** holds the **system tenant** (control plane: tenant records and tenant-admin identities). Created at admin bootstrap per config.
- **Per-tenant barriers** are registered via `seal.BarrierRegistry.RegisterTenant(...)` as a sealed, uninitialized shell when the system admin provisions a tenant. The **tenant admin** later initializes their own barrier (their choice of `none` / `passphrase` / `shamir`, their passphrase/shares). The system admin holds no tenant barrier key.

## Bootstrap entry points (`pkg/bootstrap`)

Admin enrollment is cert-based (CSR-sign or generate-and-distribute -> SPKI-thumbprint binding); there are no invite tokens. The control-plane (system) and data-plane (tenant) scopes are orchestrated through the `pkg/bootstrap` entry points:

- **System bootstrap** (`bootstrap.BootstrapSystem`) — system barrier + system tenant + system admin + admin-mTLS.
- **Provision tenant** (`bootstrap.ProvisionTenant`) — minimal control-plane tenant record + tenant-admin cert enrollment (no tenant barrier key held by the system admin).
- **Initialize tenant barrier** (`bootstrap.InitializeTenantBarrier`) — tenant-admin self-service (authenticated): initialize THIS tenant's barrier and scope.

See [identity-rbac/architecture.md](identity-rbac/architecture.md) for the user model and SPKI binding.

## Startup sequence

`qrdbd start` runs one sequence on first boot and on every restart; only the
first run initializes. The steps, in order: configuration loading, TLS
certificate generation, storage initialization, Raft initialization, barrier
initialization (if needed), role and user bootstrap, multi-protocol servers
start. Each step belongs to a feature folder, which is the contract for it:

| Step | Owner |
|------|-------|
| Configuration, listeners and flags | [12-configuration.md](12-configuration.md), [api-server/cli.md](api-server/cli.md) |
| Barrier initialization and unsealing | [barrier-seal/architecture.md](barrier-seal/architecture.md), [seal/README.md](seal/README.md) |
| M-of-N share enrollment and claim | [ceremony/architecture.md](ceremony/architecture.md), [shamir-custody/architecture.md](shamir-custody/architecture.md) |
| First admin, built-in roles, idempotency | [firstboot/architecture.md](firstboot/architecture.md) |
| Joining an existing cluster | [clustering/architecture.md](clustering/architecture.md) |

First-boot orchestration is idempotent: roles and the admin user are created
once, and a restart skips every step that already completed.

## See Also

- [ceremony/architecture.md](ceremony/architecture.md) — Ceremony state machine and share holder enrollment
- [seal/README.md](seal/README.md) — Barrier architecture and unsealing
- [12-configuration.md](12-configuration.md) — Full YAML configuration reference
- [clustering/cli.md](clustering/cli.md) — Monitoring, health checks, and deployment
