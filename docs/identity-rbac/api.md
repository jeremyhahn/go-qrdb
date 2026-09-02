# Identity and RBAC — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix — users

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Users()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /users` | `CreateUser` | `user.Create` | `qrdb_user_create` | `CreateUser` | `users:create` |
| Get by ID | `GET /users/{id}` | `GetUser` | `user.Get` | `qrdb_user_get` | `GetUser` | `users:read` |
| Get by username | `GET /users/by-username/{username}` | `GetUserByUsername` | `user.GetByUsername` | `qrdb_user_get_by_username` | `GetUserByUsername` | `users:read` |
| Get by email (canonical login key) | `POST /users/lookup` | `GetUserByEmail` | `user.GetByEmail` | `qrdb_user_get_by_email` | `GetUserByEmail` | `users:read` |
| Get by cert thumbprint | `GET /users/by-cert-thumbprint/{thumbprint}` | `GetUserByCertThumbprint` | `user.GetByCertThumbprint` | `qrdb_user_get_by_cert_thumbprint` | `GetUserByCertThumbprint` | `users:get_by_cert_thumbprint` |
| Add cert binding | `POST /users/{id}/cert-bindings` | `AddCertBinding` | `user.AddCertBinding` | `qrdb_user_add_cert_binding` | `AddCertBinding` | `users:add_cert_binding` |
| Update | `PUT /users/{id}` | `UpdateUser` | `user.Update` | `qrdb_user_update` | `UpdateUser` | `users:update` |
| Delete | `DELETE /users/{id}` | `DeleteUser` | `user.Delete` | `qrdb_user_delete` | `DeleteUser` | `users:delete` |
| List | `GET /users` | `ListUsers` | `user.List` | `qrdb_user_list` | `ListUsers` | `users:list` |
| Enable | `POST /users/{id}/enable` | `EnableUser` | `user.Enable` | `qrdb_user_enable` | `EnableUser` | `users:enable` |
| Disable | `POST /users/{id}/disable` | `DisableUser` | `user.Disable` | `qrdb_user_disable` | `DisableUser` | `users:disable` |

## Capability matrix — roles and permissions

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Roles()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create role | `POST /roles` | `CreateRole` | `role.Create` | `qrdb_role_create` | `CreateRole` | `roles:create` |
| Get role | `GET /roles/{id}` | `GetRole` | `role.Get` | `qrdb_role_get` | `GetRole` | `roles:read` |
| Get role by name | `GET /roles/by-name/{name}` | `GetRoleByName` | `role.GetByName` | `qrdb_role_get_by_name` | `GetRoleByName` | `roles:read` |
| Update role | `PUT /roles/{id}` | `UpdateRole` | `role.Update` | `qrdb_role_update` | `UpdateRole` | `roles:update` |
| Delete role | `DELETE /roles/{id}` | `DeleteRole` | `role.Delete` | `qrdb_role_delete` | `DeleteRole` | `roles:delete` |
| List roles | `GET /roles` | `ListRoles` | `role.List` | `qrdb_role_list` | `ListRoles` | `roles:list` |
| Assign role | `POST /roles/assign` | `AssignRole` | `role.Assign` | `qrdb_role_assign` | `AssignRole` | `roles:assign` |
| Unassign role | `POST /roles/unassign` | `UnassignRole` | `role.Unassign` | `qrdb_role_unassign` | `UnassignRole` | `roles:unassign` |
| List permissions | `GET /permissions` | `ListPermissions` | `permission.List` | `qrdb_permissions_list` | `Client.Permissions()` | `permissions:list` |
| Resolve role permissions | `POST /permissions/resolve` | `ResolveRolePermissions` | `permission.Resolve` | `qrdb_permissions_resolve` | `Client.RBAC()` | `permissions:resolve` |

`Client.Permissions()` and `Client.RBAC()` return **nil** on a transport that
does not implement the corresponding provider. Nil-check them; the nil is not
an error value.

## Authentication

**mTLS, passwordless, on every transport.** There is no login endpoint that
takes a password, and no password field anywhere in the user model.

| Certificate field | Maps to |
|---|---|
| Subject CN | `Username` |
| Subject OU | `TenantID` |
| base64url SHA-256 of `SubjectPublicKeyInfo` | `CertBindings[].Thumbprint` |

**The tenant comes from the certificate, never from a request field.** That is
the root of tenant isolation in every other feature.

`GET /users/by-cert-thumbprint/{thumbprint}` is the lookup that turns a
presented certificate into a user. Because the thumbprint is over the public
key, a renewed certificate for the same key resolves to the same user.

## Schemas

`User`:

```json
{ "id": "…", "username": "alice", "tenantID": "acme",
  "roleIDs": ["…"], "enabled": true, "system": false,
  "roleBindings": [ { "roleID": "…", "tenantID": "acme" } ],
  "attributes": { "department": "eng" },
  "certBindings": [ { "thumbprint": "…", "kind": 0 } ],
  "factors": [ { "…": "opaque" } ],
  "createdAt": "…", "updatedAt": "…" }
```

`certBindings[].kind`: `0` SPKI thumbprint (**canonical default**) ·
`1` full-certificate fingerprint (exact pinning) · `2` PIV-attested.

`roleBindings` are **additive** to `roleIDs`; effective permissions are the
union. `attributes` are the ABAC inputs. `factors` are **opaque** — qrdb stores
the data and never interprets it.

Role creation takes a name, a tenant and a permission list of `resource:action`
strings, each validated against the registry.

`ListPermissions` returns the registry, grouped into categories with display
names for UI rendering.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| User or role not found | 404 | `NOT_FOUND` |
| Duplicate username within a tenant | 409 | `ALREADY_EXISTS` |
| Duplicate email within a tenant | 409 | `ALREADY_EXISTS` |
| Empty email without `without_email_login_key` | 400 | `INVALID_ARGUMENT` |
| Lost a concurrent update (re-read and retry) | 409 | `ABORTED` |
| Commit outcome unknown (reconcile, then retry) | 503 | `UNAVAILABLE` |
| Unregistered permission string | 400 | `INVALID_ARGUMENT` |
| Deleting a `system` user or a built-in role | 403 | `PERMISSION_DENIED` |
| Certificate with no matching binding | 401 | `UNAUTHENTICATED` |
| Disabled user | 401 | `UNAUTHENTICATED` |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Pagination

`ListUsers` and `ListRoles` are **page-based** (`page`, `page_size`).
`ListPermissions` returns the whole registry and is not paginated.

## Versioning

Identity REST paths are unversioned (`/users/...`, `/roles/...`,
`/permissions/...`). Nothing here is deprecated.

## The email login key

`Email` is the **canonical login key**: unique within a tenant, and what every
layer above qrdb resolves a principal by — the WebAuthn credential handle, the
session subject, the CLI's user identifier.

- **Tenant-scoped, not global.** The same person may hold an account in two
  tenants, and every login path supplies the tenant alongside the handle.
- **Normalized** by `sdk.NormalizeEmail` (lowercase, trimmed) on both the stored
  record and its index key. Comparison is case-insensitive, so `Op@x.com` and
  `op@x.com` are one account rather than two.
- **Required on create** unless the caller sets `without_email_login_key`, which
  creates a certificate-only or bootstrap identity that cannot be resolved by
  email. The omission must be declared; it is never a default.

### Why the lookup is a POST

`POST /users/lookup` takes `{"email": "...", "tenant_id": "..."}` in the body
rather than `GET /users/by-email/{email}`. Email is the canonical login
identifier, and a path parameter is captured by reverse-proxy access logs, load
balancers, tracing middleware and browser history on every successful lookup.
The gRPC, QUIC and MCP surfaces carry it in the payload; REST does too.

## Index consistency

Secondary indexes (username, email, certificate thumbprint) are maintained with
atomic claims rather than read-then-write:

- A handle is **claimed** with `PutIfAbsent` and **released** with
  `CompareAndDelete` against the owning id, so a claim cannot be taken twice and
  a release cannot evict an entry another account has since taken.
- `Update`, `Enable`, `Disable` and `Delete` **commit conditionally** against the
  record version they read. Losing yields `ConcurrentUpdateError` — nothing is
  wrong; re-read and retry.
- A store error from a conditional commit is **ambiguous**, not a failure: the
  record is read back to decide. Only an inconclusive read yields
  `CommitOutcomeUnknownError`, which asks the caller to reconcile before
  retrying rather than assuming either outcome.
- Compensation undoes only what its own invocation did, and a losing update
  restores exactly the handles the winning record still relies on.

A store that cannot claim atomically is refused (`ErrStoreNotAtomic`) rather
than silently degraded to a check-then-write, which would be a uniqueness
guarantee that is not one.
