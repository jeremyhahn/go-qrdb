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
