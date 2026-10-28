# Identity / authorization layering

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-26
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

go-qrdb's auth subsystem is **identity-layer**. It owns tenants, users,
roles, role memberships, and the permission registry. That registry carries
the stack's shared permission vocabulary: besides the identity-domain
operations it declares the families downstream services check — cryptographic
keys, the broker data-plane families (`objstore:*`, `orchestrator:*`) and
`passwords:*` — and `pkg/permission/builtin_role_grants.go` grants them to the
built-in roles.

Downstream consumers (go-xkms, go-quicbaas, ...) read identity / role
membership from qrdb via the SDK and decide over their own resources with
those permission names.

## What lives in qrdb

- **Tenants** (`pkg/tenant`) — including the reserved `system` tenant.
- **Users** (`pkg/user`) — passwordless principals scoped to a tenant.
- **Roles** (`pkg/role`) — name + permission list, scoped to a tenant.
- **Role memberships** — `User.RoleIDs` (roles of the user's own tenant) and
  `User.RoleBindings` (tenant-scoped grants) are the assignment edges.
- **Permission registry** (`pkg/permission/constants.go`) — comprehensive permission
  set including:
  - tenant CRUD, its cross-tenant variants (`tenants:*:cross`), `tenants:approve`, and tenant barriers
  - user CRUD and authentication factors
  - role CRUD and permission listing
  - audit log access (read, query, stream)
  - KV / MVCC / transaction / batch / watch / lease / index / time-series / vector / lock operations
  - cryptographic keys (sign, verify, encrypt, decrypt, wrap, unwrap, create, rotate, delete, derive)
  - certificate management (issue, enroll, delegate, revoke, list, read) and trust anchors (add, list, remove)
  - certificate authority (CA) and ACME operations
  - seal/barrier operations and recovery
  - ceremony operations
  - broker data-plane operations (`objstore:*`, `orchestrator:*`) and static passwords (`passwords:*`)
- **System-tenant rule** — a cross-scope permission (`:cross`, tagged
  `ScopeCross`) takes effect only for an identity in the `system` tenant;
  every other identity is bound to its own tenant.

## What does NOT live in qrdb

- Hardware-specific authentication factor implementations (FIDO2, PIV, PKCS#11, TPM2). These live in go-xkms and implement the `authfactors.Factor` interface, registered in an `authfactors.FactorRegistry`.
- Hardware seal strategy implementations (TPM2, PKCS#11, cloud KMS, Vault). These live in go-xkms and register into qrdb's `seal.StrategyRegistry`.
- Application-specific resource models beyond the core qrdb / go-xkms domains (e.g., quicbaas's static-password / team domain). qrdb's registry declares the permission names such a domain checks (`passwords:*`), not the domain's model.
- Policies over an application's own resources. qrdb implements role-based access control, with an optional ABAC decorator over it ([abac.md](abac.md)); a downstream application's resource policies belong to that application.

The split respects module boundaries: qrdb owns core identity, cryptographic operations, and seal/barrier lifecycle. go-xkms and go-quicbaas inject hardware backends and application-specific domains via composition.

## Pattern for downstream applications

```go
// Look up identity / role membership through qrdb (sdk/go Client).
perms, err := qrdbClient.RBAC().ListPermissions(ctx, identity)
// or:
user, err := qrdbClient.Users().GetUserByUsername(ctx, tenantID, username)

// Then make the permission decision in the application's own domain.
allowed := app.CheckPermission(user.RoleIDs, "keys", "sign")
```

The downstream app owns the permission semantics; qrdb owns the membership.

## Why the split

- **Cohesion.** qrdb holds one shared permission vocabulary for the stack, so
  a new permission family is a change to qrdb's registry; how a downstream
  application models the resources behind it stays in that application.
- **Multi-tenant security.** A bug in xkms's permission map cannot leak
  cross-tenant identity data, because the identity layer is upstream of
  the permission decision.
- **Multiple consumers.** Multiple apps can share one qrdb identity service
  without colliding on each other's permission names.

## Cross-cutting authorization in qrdb

Authorization in qrdb itself uses qrdb's permission registry (e.g.
`tenants:list:cross`, `certs:revoke:cross`). Downstream apps that need to
perform identity-layer operations (e.g. an admin UI listing all tenants)
go through qrdb's RBACClient:

```go
err := qrdbClient.RBAC().Authorize(ctx, identity, "tenants", "list:cross")
```

Authorization for the app's *own* domain stays inside the app.

## See also

- `docs/auth/README.md` — pluggable authenticator interfaces.
- `docs/auth/roles.md` — qrdb's built-in roles: the five FIPS 140-3 roles and the
  standard-posture Owner.
- `pkg/permission/constants.go` — the identity-domain permission registry.
- xkms's `docs/auth/layering.md` — the same boundary documented from
  xkms's side.
