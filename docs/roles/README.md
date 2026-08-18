<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Role & RBAC System

The `pkg/role/` package provides role management, and `pkg/auth/rbac/` implements the authorizer that resolves permissions through the role service. Together they form a complete role-based access control system with fine-grained `resource:action` permissions.

## Role Entity

Source: `pkg/role/types.go`

```go
type Role struct {
    ID          string                  `json:"id"`          // Globally unique UUID
    Name        string                  `json:"name"`        // Unique within tenant
    TenantID    string                  `json:"tenantID"`    // Tenant isolation boundary
    Permissions []permission.Permission `json:"permissions"` // Fine-grained permissions
    System      bool                    `json:"system"`      // Built-in, cannot be deleted
    CreatedAt   time.Time               `json:"createdAt"`   // UTC creation time
    UpdatedAt   time.Time               `json:"updatedAt"`   // UTC last modification time
}
```

### Request Types

```go
type CreateRoleRequest struct {
    Name        string                  // Required, must not be empty
    TenantID    string                  // Defaults to DefaultTenantID when empty
    Permissions []permission.Permission // Must all be registered in the global registry
}

type UpdateRoleRequest struct {
    ID          string                  // Required, role UUID
    Name        string                  // Replaces current name when non-empty
    Permissions []permission.Permission // Replaces current set when non-nil
}

type ListRolesRequest struct {
    TenantID string // Filters by tenant
    Page     int    // 1-based page number (default: 1)
    PageSize int    // Max roles per page (default: 100)
}

type ListRolesResponse struct {
    Roles   []*Role `json:"roles"`
    Total   int     `json:"total"`
    HasMore bool    `json:"hasMore"`
}
```

## Service Interface

Source: `pkg/role/service.go`

```go
type RoleServicer interface {
    CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error)
    GetRole(ctx context.Context, id string) (*Role, error)
    GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error)
    UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error)
    DeleteRole(ctx context.Context, id string) error
    ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error)
    ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error)
}
```

`ResolvePermissions` returns the sorted, deduplicated union of all permissions for the given role IDs. It is the primary entry point for the RBAC authorizer.

## Storage Schema

Source: `pkg/role/store.go`

| Key Pattern | Value | Purpose |
|-------------|-------|---------|
| `__sys:roles/id/{roleID}` | Role JSON | Primary record by UUID |
| `__sys:roles/tenant/{tenantID}/{roleID}` | roleID bytes | Tenant-scoped listing index |
| `__sys:role_idx/name/{tenantID}/{roleName}` | roleID bytes | Name uniqueness index |

The name index enforces uniqueness within a tenant. Role IDs for built-in roles
are deterministic UUIDs derived as RFC 9562 §5.8 **UUIDv8 over SHA-256** of a
fixed `"go-qrdb:role-namespace/v8/"` prefix plus the length-framed tenant ID and
role name (`pkg/role.SystemRoleID`).

The derivation was UUIDv5 (name-based, SHA-1) until the FIPS 140-3 migration.
SHA-1 does not exist under FIPS 140-3 — a binary built with
`GODEBUG=fips140=only` panics the moment it is called — and RFC 4122 fixes v3 to
MD5 and v5 to SHA-1, so v8, the version reserved for application-defined
derivations, is the only name-based form an approved hash can take. The
36-character UUID text shape is unchanged, so no index key or wire format
changed with it.

Because these identifiers are persisted data — role primary keys, both secondary
indexes, and every user record's `RoleIDs` — the change is versioned rather than
a flag day: **writes emit only the v8 form, reads accept both.** A cluster seeded
before the migration keeps its roles under their UUIDv5 identifiers (seeding is
name-unique and tolerates already-exists, so those records are never replaced),
and nothing already persisted becomes unresolvable:

| Direction | How it stays correct |
|-----------|----------------------|
| Identifier → role | Unchanged: a stored identifier is looked up verbatim, so `GetRole`, `ListRoles`, and `ResolvePermissions` resolve a legacy record as they always did. |
| Tenant + name → identifier | `role.ResolveSystemRoleID` answers from the name index rather than from a hash, so a grant binds to the record that exists. Bootstrap and first-boot use it when assigning system roles. |
| "Is this identifier role X?" | `role.SystemRoleIDs` returns both the canonical and the persisted identifier. The RBAC authorizer matches against the whole set, so the SO deny-list (FIPS 140-3 AS10.03) and the strict-separation-of-duties Owner strip still void a grant made before the migration. |

No capability is absent from a FIPS build as a result: the v8 derivation is the
only one compiled, in every build.

## Permission Validation

All permissions assigned to a role are validated against the global permission registry (`permission.IsRegistered()`). Attempting to create or update a role with an unregistered permission returns an `InvalidPermissionError`.

## Built-In Roles

Source: `pkg/role/store.go` -- `BuiltInRoles(tenantID string) []*Role`

Built-in roles are created at bootstrap for each tenant. They use deterministic UUIDs so the same role always has the same ID within a given tenant. All built-in roles are marked `System: true` and cannot be deleted.

The canonical set is the six FIPS 140-3 / PCI-DSS v4 roles from [auth/roles.md](../auth/roles.md): SO, Admin, Operator, User, Auditor, Custodian. A seventh, `Owner`, is a standard-posture convenience superuser -- see `pkg/role.OwnerRole` -- that is inert whenever strict separation of duties is enforced.

| Role | Description |
|------|-------------|
| `so` | Security Officer -- module lifecycle, PIN/PIV governance, CA and ACME administration, role/user/tenant management. **No** cryptographic key-use permissions (FIPS 140-3 AS10.03). |
| `admin` | Operational key lifecycle, certificate/password/SSH vault administration, tenant configuration, and (system-tenant only) cross-tenant provisioning + the broker data-plane admin wildcards. |
| `operator` | Routine cryptographic operations, all data-service verbs (KV/MVCC/Vector/TimeSeries/...), and the day-to-day (non-admin) broker data-plane verbs. |
| `user` | Cryptographic operations using existing keys, plus the broker orchestrator consumer slice. No key or configuration management. |
| `auditor` | Read-only visibility into audit logs, user/role/cert/tenant metadata, and orchestrator metadata (FIPS 140-3 AS10.10 -- never payload data). |
| `custodian` | Shamir share holder. Holds only `barrier:unseal-share` and the tenant-barrier share/vote equivalents (FIPS 140-3 AS10.06). |
| `owner` | Full `permission.All()`. Standard-posture only; the RBAC authorizer strips this grant under strict separation of duties. |

### Registry-derived permission sets

The SO, Admin, Operator, User, and Auditor permission sets are **not** hand-maintained lists inside `pkg/role`. Each is a live projection of `permission.RolePermissions(role)` -- see [permissions/README.md](../permissions/README.md#built-in-role-default-grants). Every permission declares its own default role placement, once, in `pkg/permission/builtin_role_grants.go`; `BuiltInRoles` only materializes the projection. This means:

- There is exactly one place to change a permission's role placement.
- A higher layer (go-quicbaas, go-xkms) can place a permission it contributes onto a built-in role via the `sdk/go` re-export (`RegisterPermissionRoleGrant`) instead of being limited to a hand-authored custom role.
- The projection is evaluated at role-seed time, not cached at package-init time, so a permission registered late (e.g. by a higher layer's composition root, before role seeding) is still included.

`custodian` and `owner` are the two exceptions: Custodian's set is a fixed, minimal literal (`pkg/role.custodianPermissions`) unrelated to the registry, and Owner is `permission.All()`.

## RBAC Authorizer

Source: `pkg/auth/rbac/rbac.go`

```go
func New(roles role.RoleServicer, opts ...Option) *RBACAuthorizer

func (a *RBACAuthorizer) Authorize(
    ctx context.Context,
    identity *auth.Identity,
    resource, action string,
) error
```

Options set the security posture:

- `WithStrictSeparation(bool)` -- when strict (the daemon sets this from `crypto/fips140.Enabled()` or the separation-of-duties config flag), the `owner` role is stripped from every identity before permissions are resolved, so no one inherits `permission.All()`.
- `WithScopeResolver(...)` -- resolves each permission's scope in the identity's own tenant instead of the process-global default. A resolution error (for example, a sealed barrier tenant) denies the request.
- `WithRoleServiceFallback(...)` -- a StaleRead-backed role store consulted only when the primary linearizable read fails or times out (cold ReadIndex, draining host). Role data is replicated before servers accept traffic, so the stale read is safe for resolving what a role **grants**. It is not, on its own, safe for deciding whether an identifier **denotes** a system role: the replica can be behind a role that was deleted and recreated under a new identifier, and a stale "that identifier is not the SO" would silently switch off a control that exists to void a grant. The two grant-voiding steps below therefore treat a stale answer as unable to clear a grant. Each identifier the stale name lookup does not account for is re-checked from the ID side, and anything the replica cannot rule out -- no record for it, or a record carrying the system role's name -- is treated as denoting the role. A role the replica knows, and knows is something else, is left alone, so a cold node still authorizes ordinary work.
- `WithCacheTTL(...)` -- TTL for the per-identity permission cache (default 30s).

### Authorization flow

There is no superadmin or god-mode role. Assigning a role that does not exist grants nothing, so the request is denied by default. The one special case runs the other way: an explicit deny that no grant can override.

1. **Nil identity** -- returns `auth.ErrUnauthenticated`.
2. **SO deny-list (FIPS 140-3 AS10.03)** -- if the identity holds the `so` role and the request is a cryptographic operation, it is denied here, before anything else. A Security Officer administers the module but must not use the keys. The deny-list has two halves in `rbac.go`: `soDeniedActions` pins the operations whose verb alone is not conclusive (`keys:sign`, `encrypt`, `decrypt`, `verify`, `wrap`, `unwrap`, `derive`, and `piv:use`), and `soDeniedCryptoVerbs` denies any resource whose action names a cryptographic primitive (`sign`, `encrypt`, `decrypt`, `wrap`, `unwrap`, `derive`, `kdf`, `mac`, `hmac`, `attest`, `quote`) so that a permission contributed by a higher layer -- go-quicbaas's `attestation:attest`, for example -- is covered without qrdb knowing the resource exists. `seal:unseal`, `seal:seal` and `ceremony:sign_csr` are deliberately **not** denied: they are the SO's own module-lifecycle duty. The check precedes permission resolution so an erroneous grant cannot re-open it.
3. **Effective roles** -- under strict separation of duties, the `owner` role (by name, by per-tenant system ID, or by any identifier the store says denotes it) is removed from the identity's role list. When that lookup was answered by the StaleRead store, an identifier the replica cannot account for is stripped too.
4. **Permission resolution** -- the union of the effective roles' permissions is resolved through the role service and cached per `(tenantID, subject)`. A transient failure of the primary linearizable read falls back to the StaleRead store when one is configured.
5. **Permission check** -- the request is allowed only when `resource:action` is in the resolved set. Whole-segment wildcards match too: `resource:*`, `*:action`, and `*:*`. The built-in roles use concrete permissions.
6. **Scope / system-tenant rule** -- a `ScopeCross` permission requires the identity to belong to the system tenant; a `ScopeSelf` permission requires the identity's tenant to equal the request's target tenant (the system tenant may target any). The scope is read live from the identity's own tenant, and a scope-resolution error denies.
7. **Default deny** -- anything not granted returns `PermissionDeniedError` (wrapping `auth.ErrUnauthorized`).

### Permission Cache

The cache uses a configurable TTL (default: 30 seconds). Each entry maps `(tenantID, subject)` to a `map[Permission]struct{}` set.

```go
// Configure cache TTL
authorizer := rbac.New(roleService, rbac.WithCacheTTL(60 * time.Second))

// Invalidate specific identity
authorizer.Invalidate("default", "alice")

// Invalidate all cached permissions
authorizer.InvalidateAll()
```

Cache invalidation should be triggered after role updates or role assignment changes.

## CLI Commands

Source: `cmd/qrdb/roles.go`

```
qrdb roles list [--tenant-id <id>]
qrdb roles create --name <name> --permissions <perm1,perm2>
qrdb roles get <id>
qrdb roles update <id> [--name <name>] [--permissions <perm1,perm2>]
qrdb roles delete <id>
qrdb roles assign --user <uid> --role <rid>
qrdb roles unassign --user <uid> --role <rid>
qrdb roles permissions
```

The `roles permissions` subcommand lists all registered permissions grouped by category.

## API Endpoints

### REST

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/roles` | Create role |
| `GET` | `/v1/roles` | List roles (query: `tenantId`, `page`, `pageSize`) |
| `GET` | `/v1/roles/{id}` | Get role by ID |
| `GET` | `/roles/by-name` | Get role by tenant-scoped name (query: `tenant_id`, `name`) |
| `PUT` | `/v1/roles/{id}` | Update role |
| `DELETE` | `/v1/roles/{id}` | Delete role |
| `POST` | `/v1/roles/assign` | Assign role to user |
| `POST` | `/v1/roles/unassign` | Unassign role from user |
| `POST` | `/roles/resolve-permissions` | Resolve the effective (unioned) permissions for a set of role IDs (`{"role_ids":[...]}`) |

### gRPC

```protobuf
rpc CreateRole(CreateRoleRequest) returns (Role);
rpc GetRole(GetRoleRequest) returns (Role);
rpc GetRoleByName(GetRoleByNameRequest) returns (RoleResponse);
rpc UpdateRole(UpdateRoleRequest) returns (Role);
rpc DeleteRole(DeleteRoleRequest) returns (google.protobuf.Empty);
rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
rpc AssignRole(AssignRoleRequest) returns (google.protobuf.Empty);
rpc UnassignRole(UnassignRoleRequest) returns (google.protobuf.Empty);
rpc ResolveRolePermissions(ResolveRolePermissionsRequest) returns (ResolveRolePermissionsResponse);
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `qrdb_role_create` | Create a new role |
| `qrdb_role_get` | Get role by ID |
| `qrdb_role_get_by_name` | Get role by tenant-scoped name |
| `qrdb_role_update` | Update role fields |
| `qrdb_role_delete` | Delete a role |
| `qrdb_role_list` | List roles for a tenant |
| `qrdb_role_assign` | Assign role to user |
| `qrdb_role_unassign` | Unassign role from user |
| `qrdb_permissions_resolve` | Resolve the effective permissions for a set of role IDs |

All operations are exposed with full parity across REST, gRPC, QUIC, MCP, Unix, and the embedded transport, plus the `qrdb roles` CLI (`get-by-name`, `resolve-permissions`).

## Required Permissions

| Operation | Permission |
|-----------|------------|
| Create role | `roles:create` |
| Read role (by ID or name) | `roles:read` |
| Update role | `roles:update` |
| Delete role | `roles:delete` |
| List roles | `roles:list` |
| Assign role | `roles:assign` |
| Unassign role | `roles:unassign` |
| Resolve effective permissions | `permissions:resolve` |

## Error Types

Source: `pkg/role/errors.go`

| Error Type | Condition |
|------------|-----------|
| `RoleNotFoundError` | Role ID does not exist in the store |
| `RoleNameExistsError` | Role name already taken within the tenant |
| `SystemRoleImmutableError` | Attempted deletion of a system role |
| `InvalidPermissionError` | Permission string not registered in the global registry |
| `StoreError` | Underlying KVStore operation failure (wraps cause) |
| `ValidationError` | Request field validation failure |

Source: `pkg/auth/rbac/rbac.go`

| Error Type | Condition |
|------------|-----------|
| `PermissionDeniedError` | Identity lacks the required permission |
| `ResolveError` | Failed to resolve permissions through the role service |

## SDK Usage

```go
import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

client, err := sdk.NewClient(
    sdk.WithTransport("grpc"),
    sdk.WithAddress("localhost:63003"),
    sdk.WithCertFile("/etc/qrdb/admin-cert.pem"),
    sdk.WithKeyFile("/etc/qrdb/admin-key.pem"),
)
defer client.Close()

// Create a custom role
role, err := client.Roles().Create(ctx, &transport.CreateRoleRequest{
    Name:        "data-analyst",
    Permissions: []string{"kv:get", "kv:list", "kv:range", "mvcc:range"},
})

// Assign role to user
err = client.Roles().Assign(ctx, &transport.AssignRoleRequest{
    UserID: userID,
    RoleID: role.ID,
})
```

## See Also

- [permissions/README.md](../permissions/README.md) -- Fine-grained permission constants and categories
- [users/README.md](../users/README.md) -- User entities assigned to roles
- [auth/README.md](../auth/README.md) -- Pluggable authentication and authorization interfaces
- [api.md](../api.md) -- Full API endpoint reference
