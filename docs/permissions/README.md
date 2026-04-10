<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Permission System

The `pkg/permission/` package defines fine-grained access control permissions in `resource:action` format, a global registry for runtime validation, and category groupings for UI rendering. Permissions are the atomic unit of authorization -- roles aggregate them, and the RBAC authorizer checks them.

## Core Types

Source: `pkg/permission/permission.go`

```go
// Permission represents a fine-grained access control permission in
// "resource:action" format (e.g., "kv:put", "admin:health").
type Permission string

// Category groups related permissions under a display name for UI rendering.
type Category struct {
    Name        string       // Display name (e.g., "KV Service")
    Group       string       // Logical grouping (e.g., "Data Services")
    Permissions []Permission // Permissions belonging to this category
}
```

## Package API

```go
// Register adds one or more permissions to the global registry.
func Register(perms ...Permission)

// All returns every registered permission as a sorted slice.
func All() []Permission

// IsRegistered reports whether p exists in the global registry.
func IsRegistered(p Permission) bool

// Parse validates a raw string and returns the corresponding Permission.
func Parse(s string) (Permission, error)

// Resource returns the resource component of p (before the colon).
func Resource(p Permission) string

// Action returns the action component of p (after the colon).
func Action(p Permission) string

// Categories returns all permission categories for UI rendering.
func Categories() []Category

// ByResource returns all registered permissions matching the given resource.
func ByResource(resource string) []Permission
```

## Permission Constants

Source: `pkg/permission/constants.go`

All permissions are registered automatically via the `init()` function in `constants.go`, which iterates the category definitions and calls `Register()` for each permission.

### Data Services

| Category | Constant | Permission String |
|----------|----------|-------------------|
| KV | `KVPut` | `kv:put` |
| KV | `KVGet` | `kv:get` |
| KV | `KVDelete` | `kv:delete` |
| KV | `KVList` | `kv:list` |
| KV | `KVExists` | `kv:exists` |
| KV | `KVRange` | `kv:range` |
| MVCC | `MVCCPutWithLease` | `mvcc:put_with_lease` |
| MVCC | `MVCCGetRevision` | `mvcc:get_revision` |
| MVCC | `MVCCGetAtRevision` | `mvcc:get_at_revision` |
| MVCC | `MVCCRange` | `mvcc:range` |
| MVCC | `MVCCCompact` | `mvcc:compact` |
| MVCC | `MVCCCurrentRevision` | `mvcc:current_revision` |
| Transaction | `TransactionExecute` | `transaction:execute` |
| Batch | `BatchExecute` | `batch:execute` |
| Watch | `WatchWatch` | `watch:watch` |
| Watch | `WatchWatchPrefix` | `watch:watch_prefix` |
| Lease | `LeaseGrant` | `lease:grant` |
| Lease | `LeaseRevoke` | `lease:revoke` |
| Lease | `LeaseKeepAlive` | `lease:keepalive` |
| Lease | `LeaseTTL` | `lease:ttl` |
| Lease | `LeaseList` | `lease:list` |
| Index | `IndexCreate` | `index:create` |
| Index | `IndexDrop` | `index:drop` |
| Index | `IndexQuery` | `index:query` |
| Index | `IndexScan` | `index:scan` |
| TimeSeries | `TSWrite` | `timeseries:write` |
| TimeSeries | `TSWriteBatch` | `timeseries:write_batch` |
| TimeSeries | `TSQuery` | `timeseries:query` |
| TimeSeries | `TSAggregate` | `timeseries:aggregate` |

### Vector Service (20 permissions)

| Constant | Permission String |
|----------|-------------------|
| `VectorNamespaceCreate` | `vector:namespace_create` |
| `VectorNamespaceDelete` | `vector:namespace_delete` |
| `VectorNamespaceList` | `vector:namespace_list` |
| `VectorNamespaceInfo` | `vector:namespace_info` |
| `VectorUpsert` | `vector:upsert` |
| `VectorGet` | `vector:get` |
| `VectorDelete` | `vector:delete` |
| `VectorBatchUpsert` | `vector:batch_upsert` |
| `VectorBatchDelete` | `vector:batch_delete` |
| `VectorList` | `vector:list` |
| `VectorUpdateMetadata` | `vector:update_metadata` |
| `VectorSearchSimilar` | `vector:search_similar` |
| `VectorSearchByMetadata` | `vector:search_by_metadata` |
| `VectorHybridSearch` | `vector:hybrid_search` |
| `VectorSearchByID` | `vector:search_by_id` |
| `VectorRebuildIndex` | `vector:rebuild_index` |
| `VectorGetRebuildProg` | `vector:get_rebuild_progress` |
| `VectorGetIndexStats` | `vector:get_index_stats` |
| `VectorCompact` | `vector:compact` |
| `VectorGetCompactProg` | `vector:get_compaction_progress` |

### Admin Services

| Constant | Permission String |
|----------|-------------------|
| `AdminHealth` | `admin:health` |
| `AdminStatus` | `admin:status` |
| `AdminPeers` | `admin:peers` |
| `AdminAddMember` | `admin:add_member` |
| `AdminRemoveMember` | `admin:remove_member` |
| `AdminSnapshot` | `admin:snapshot` |
| `AdminDrain` | `admin:drain` |
| `AdminCapabilities` | `admin:capabilities` |
| `SealBootstrap` | `seal:bootstrap` |
| `SealUnseal` | `seal:unseal` |
| `SealSeal` | `seal:seal` |
| `SealStatus` | `seal:status` |

### Management Services

| Constant | Permission String |
|----------|-------------------|
| `UsersCreate` | `users:create` |
| `UsersRead` | `users:read` |
| `UsersUpdate` | `users:update` |
| `UsersDelete` | `users:delete` |
| `UsersList` | `users:list` |
| `UsersEnable` | `users:enable` |
| `UsersDisable` | `users:disable` |
| `RolesCreate` | `roles:create` |
| `RolesRead` | `roles:read` |
| `RolesUpdate` | `roles:update` |
| `RolesDelete` | `roles:delete` |
| `RolesList` | `roles:list` |
| `RolesAssign` | `roles:assign` |
| `RolesUnassign` | `roles:unassign` |
| `CertsGenerate` | `certs:generate` |
| `CertsRevoke` | `certs:revoke` |
| `CertsList` | `certs:list` |
| `TenantsCreate` | `tenants:create` |
| `TenantsRead` | `tenants:read` |
| `TenantsUpdate` | `tenants:update` |
| `TenantsDelete` | `tenants:delete` |
| `TenantsList` | `tenants:list` |

## Category Groups

Categories organize permissions into logical groups for UI rendering (e.g., a permission tree picker in a role editor).

| Group | Categories |
|-------|------------|
| Data Services | KV Service, MVCC Service, Transaction Service, Batch Service, Watch Service, Lease Service, Index Service, TimeSeries Service, Vector Service |
| Admin Services | Admin Service, Seal Management |
| Management Services | User Management, Role Management, Certificate Management, Tenant Management |

## Global Registry

All permissions are registered into a global `sync.RWMutex`-protected map during `init()`. The registry enables runtime validation -- the role store calls `IsRegistered()` before accepting a permission assignment.

```go
// Registration flow:
// constants.go init() → Register(cat.Permissions...) for each category
// → registry map[Permission]struct{}
```

## Protocol Endpoints

| Protocol | Endpoint | Response |
|----------|----------|----------|
| REST | `GET /v1/permissions` | All permissions grouped by category |
| MCP | `qrdb_permissions_list` tool | Same structure as REST |

## Error Types

Source: `pkg/permission/errors.go`

| Error Type | Condition |
|------------|-----------|
| `InvalidPermissionError` | String does not match `resource:action` format |
| `UnregisteredPermissionError` | Permission not found in the global registry |

```go
type InvalidPermissionError struct {
    Value string
}

type UnregisteredPermissionError struct {
    Permission Permission
}
```

## Thread Safety

The global registry uses `sync.RWMutex`. `Register()` acquires a write lock; all read operations (`All()`, `IsRegistered()`, `ByResource()`) acquire read locks. Since registration occurs during `init()`, runtime access is contention-free.

## See Also

- [roles/README.md](../roles/README.md) -- Role-based access control using permissions
- [users/README.md](../users/README.md) -- User entities assigned to roles
- [auth/README.md](../auth/README.md) -- Pluggable authentication and authorization interfaces
- [api.md](../api.md) -- Full API endpoint reference
