<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Identity / authorization layering

go-qrdb's auth subsystem is **identity-layer**. It owns tenants, users,
roles, role memberships, and the permission registry for identity-domain
operations. It deliberately does **not** own permissions for downstream
applications' domains.

Downstream consumers (go-xkms, go-quicbaas, ...) maintain their own
role -> permission maps for their own domains and read identity / role
membership from qrdb via the SDK.

## What lives in qrdb

- **Tenants** (`pkg/tenant`) — including the reserved `system` tenant.
- **Users** (`pkg/user`) — passwordless principals scoped to a tenant.
- **Roles** (`pkg/role`) — name + permission list, scoped to a tenant.
- **Role memberships** — `User.RoleIDs` is the assignment edge.
- **Permission registry** (`pkg/permission/constants.go`) — identity-domain
  permissions only:
  - tenant CRUD (create, read, update, delete, list:cross, approve)
  - user CRUD
  - role CRUD
  - permission listing
  - audit log access (read, read:cross)
  - KV / MVCC database operations (kv:get, kv:put, mvcc:read, mvcc:txn, ...)
- **System-tenant rule** — an identity rooted in the `system` tenant gets
  cross-tenant scope; everything else is bound to its own tenant.

## What does NOT live in qrdb

- Cryptographic / KMS permissions (sign, encrypt, issue-cert, generate-key).
- Application-specific resource models (xkms's `keys`, `certificates`,
  `secrets`, `barrier`; quicbaas's static-password / team domain).
- Per-key or per-resource policies.

These belong to the consuming application. xkms maintains them in
`pkg/keyrbac` and `pkg/keypolicy`; quicbaas maintains its own equivalents.

## Pattern for downstream applications

```go
// Look up identity / role membership through qrdb.
roles, err := xkms.Auth(qrdbClient).RBAC().ListPermissions(ctx, identity.Subject)
// or:
user, err := xkms.Auth(qrdbClient).Users().GetUserByUsername(ctx, tenantID, username)

// Then make the permission decision in the application's domain.
allowed, err := keyrbac.CheckPermission(user.Roles, "keys", "sign")
```

The downstream app owns the permission semantics; qrdb owns the membership.

## Why the split

- **Cohesion.** qrdb's release cadence is independent of xkms's permission
  vocabulary. New crypto operations don't churn qrdb's API surface.
- **Multi-tenant security.** A bug in xkms's permission map cannot leak
  cross-tenant identity data, because the identity layer is upstream of
  the permission decision.
- **Multiple consumers.** Multiple apps can share one qrdb identity service
  without colliding on each other's permission names.

## Cross-cutting authorization in qrdb

Authorization in qrdb itself uses qrdb's permission registry (e.g.
`tenant:list:cross`, `audit:read:cross`). Downstream apps that need to
perform identity-layer operations (e.g. an admin UI listing all tenants)
go through qrdb's RBACClient:

```go
err := xkms.Auth(qrdbClient).RBAC().Authorize(ctx, identity, "tenant", "list:cross")
```

Authorization for the app's *own* domain stays inside the app.

## See also

- `docs/auth/README.md` — pluggable authenticator interfaces.
- `docs/auth/roles.md` — qrdb's six-role identity model.
- `pkg/permission/constants.go` — the identity-domain permission registry.
- xkms's `docs/auth/layering.md` — the same boundary documented from
  xkms's side.
