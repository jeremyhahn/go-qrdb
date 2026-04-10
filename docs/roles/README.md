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

The name index enforces uniqueness within a tenant. Role IDs for built-in roles use deterministic UUIDs generated via UUID v5 (SHA-1 namespace derived from `"go-qrdb:role-namespace"` + tenantID + name).

## Permission Validation

All permissions assigned to a role are validated against the global permission registry (`permission.IsRegistered()`). Attempting to create or update a role with an unregistered permission returns an `InvalidPermissionError`.

## Built-In Roles

Source: `pkg/role/store.go` -- `BuiltInRoles(tenantID string) []*Role`

Built-in roles are created at bootstrap for each tenant. They use deterministic UUIDs so the same role always has the same ID within a given tenant. All built-in roles are marked `System: true` and cannot be deleted.

| Role | Description | Permission Scope |
|------|-------------|-----------------|
| `superadmin` | Full access, bypasses all authorization checks | All registered permissions |
| `admin` | Administrative operations without data access | Admin, Users, Roles, Certs, Seal, Tenants |
| `operator` | Data operations plus limited admin visibility | All data services + `admin:health`, `admin:status` |
| `readwrite` | Data read and write without admin or index/TS/vector mutation | KV, MVCC (no compact), Transaction, Batch, Watch, Lease (no revoke) |
| `readonly` | Data read-only access | KV read, MVCC read, Lease read |

### Permission Details

**superadmin**: Receives `permission.All()` -- every permission in the registry. The RBAC authorizer also has a fast-path bypass: if any role name equals `"superadmin"`, authorization succeeds without resolving permissions.

**admin**: All permissions from Admin Services and Management Services groups (see [permissions/README.md](../permissions/README.md)). No data service permissions.

**operator**: All data service permissions (KV, MVCC, Transaction, Batch, Watch, Lease, Index, TimeSeries, Vector) plus `admin:health` and `admin:status`.

**readwrite**: Core data read/write permissions. Excludes `mvcc:compact`, `lease:revoke`, Index, TimeSeries, and Vector.

**readonly**: Read-only subset: `kv:get`, `kv:list`, `kv:exists`, `kv:range`, MVCC read operations, `lease:ttl`, `lease:list`.

## RBAC Authorizer

Source: `pkg/auth/rbac/rbac.go`

```go
type RBACAuthorizer struct {
    roles role.RoleServicer
    cache *permissionCache
}

func New(roles role.RoleServicer, opts ...Option) *RBACAuthorizer

func (a *RBACAuthorizer) Authorize(
    ctx context.Context,
    identity *auth.Identity,
    resource, action string,
) error
```

### Authorization Flow

1. **Nil identity check** -- Returns `auth.ErrUnauthenticated`
2. **Superadmin bypass** -- If any role in `identity.Roles` equals `"superadmin"`, return `nil` immediately
3. **Cache lookup** -- Check `sync.Map`-based permission cache keyed by `(tenantID, subject)`
4. **Cache miss** -- Call `roles.ResolvePermissions(ctx, identity.Roles)` to resolve all permissions, then cache the result
5. **Permission check** -- Construct `Permission(resource + ":" + action)` and check membership in the resolved set
6. **Denied** -- Return `PermissionDeniedError` (wraps `auth.ErrUnauthorized`)

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
| `PUT` | `/v1/roles/{id}` | Update role |
| `DELETE` | `/v1/roles/{id}` | Delete role |
| `POST` | `/v1/roles/assign` | Assign role to user |
| `POST` | `/v1/roles/unassign` | Unassign role from user |

### gRPC

```protobuf
rpc CreateRole(CreateRoleRequest) returns (Role);
rpc GetRole(GetRoleRequest) returns (Role);
rpc UpdateRole(UpdateRoleRequest) returns (Role);
rpc DeleteRole(DeleteRoleRequest) returns (google.protobuf.Empty);
rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
rpc AssignRole(AssignRoleRequest) returns (google.protobuf.Empty);
rpc UnassignRole(UnassignRoleRequest) returns (google.protobuf.Empty);
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `qrdb_role_create` | Create a new role |
| `qrdb_role_get` | Get role by ID |
| `qrdb_role_update` | Update role fields |
| `qrdb_role_delete` | Delete a role |
| `qrdb_role_list` | List roles for a tenant |
| `qrdb_role_assign` | Assign role to user |
| `qrdb_role_unassign` | Unassign role from user |

## Required Permissions

| Operation | Permission |
|-----------|------------|
| Create role | `roles:create` |
| Read role | `roles:read` |
| Update role | `roles:update` |
| Delete role | `roles:delete` |
| List roles | `roles:list` |
| Assign role | `roles:assign` |
| Unassign role | `roles:unassign` |

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
