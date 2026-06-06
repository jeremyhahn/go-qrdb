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
| `UsersGetByCertThumbprint` | `users:get_by_cert_thumbprint` |
| `UsersAddCertBinding` | `users:add_cert_binding` |
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
| `DatabasesCreate` | `databases:create` |
| `DatabasesRead` | `databases:read` |
| `DatabasesList` | `databases:list` |
| `DatabasesDelete` | `databases:delete` |
| `DatabasesUpdate` | `databases:update` |
| `CollectionsCreate` | `collections:create` |
| `CollectionsRead` | `collections:read` |
| `CollectionsList` | `collections:list` |
| `CollectionsDrop` | `collections:drop` |
| `CollectionsInsert` | `collections:insert` |
| `CollectionsUpdate` | `collections:update` |
| `CollectionsDelete` | `collections:delete` |
| `CredentialsSubmit` | `credentials:submit` |
| `CredentialsStrategy` | `credentials:strategy` |
| `CredentialsList` | `credentials:list` |
| `CredentialsDelete` | `credentials:delete` |
| `CredentialsRotate` | `credentials:rotate` |
| `CredentialsGCExpired` | `credentials:gc_expired` |
| `CredentialsRetrieveVersion` | `credentials:retrieve_version` |
| `CredentialsListVersions` | `credentials:list_versions` |
| `UserAuthFactorsAdd` | `user_authfactors:add` |
| `UserAuthFactorsGet` | `user_authfactors:get` |
| `UserAuthFactorsList` | `user_authfactors:list` |
| `UserAuthFactorsRemove` | `user_authfactors:remove` |
| `UserAuthFactorsRotate` | `user_authfactors:rotate` |
| `UserAuthFactorsRelabel` | `user_authfactors:relabel` |
| `UserAuthFactorsLogin` | `user_authfactors:login` |

### Data Services (locks, index list, AI)

| Constant | Permission String |
|----------|-------------------|
| `LocksAcquire` | `locks:acquire` |
| `LocksRelease` | `locks:release` |
| `LocksExtend` | `locks:extend` |
| `LocksStatus` | `locks:status` |
| `LocksList` | `locks:list` |
| `IndexList` | `index:list` |
| `EmbeddingEmbed` | `embedding:embed` |
| `EmbeddingListModels` | `embedding:list_models` |
| `RAGIngest` | `rag:ingest` |
| `RAGQuery` | `rag:query` |
| `RAGListDocuments` | `rag:list_documents` |
| `RAGDeleteDocument` | `rag:delete_document` |

### Admin Services (shard control)

| Constant | Permission String |
|----------|-------------------|
| `AdminStartShard` | `admin:start_shard` |
| `AdminStopShard` | `admin:stop_shard` |
| `AdminShardState` | `admin:shard_state` |
| `AdminTopology` | `admin:topology` |

## Authorization posture (DEFAULT-DENY)

Every protocol transport enforces authorization with a **default-deny** posture:
a request that reaches a service handler/tool with **no permission mapping** is
**rejected**, never treated as public. This holds for REST, gRPC, QUIC, and MCP.

- **gRPC** (`UnaryInterceptor`/`StreamInterceptor`): an unmapped `QRDBService`
  method is denied with `PermissionDenied`; only infrastructure services
  (`grpc.health.v1.Health`, `grpc.reflection.*`) are exempt.
- **QUIC** (`AuthorizeQUIC`): an unmapped dispatch method is denied.
- **REST** (`RESTMiddleware`): an unmapped route on the API surface (paths under
  `/kv`, `/v1/`, `/users`, `/certs`, … — see `restAPIPrefixes`) is denied.
  Pre-authentication-exempt public endpoints are `/health`, `/v1/ca/bootstrap`,
  and `/v1/config/initialized`; `/metrics` requires authentication but no
  specific permission; the embedded web UI / static assets (outside the API
  prefixes) pass through.
- **MCP**: every tool call is authenticated (mTLS peer identity) and authorized
  against the tool's permission via `mcpPermissions` — also default-deny (an
  unmapped tool is rejected). The MCP surface exposes the full administrative
  API, so it must never be reachable without an authorized identity.

The mappings are validated by `TestGRPCPermissions_EveryMethodMapped` (every
generated `QRDBService` RPC must have a permission) so a newly-added endpoint
cannot silently ship unauthorized.

## Category Groups

Categories organize permissions into logical groups for UI rendering (e.g., a permission tree picker in a role editor).

| Group | Categories |
|-------|------------|
| Data Services | KV, MVCC, Transaction, Batch, Watch, Lease, Index Service, Secondary Index (list), TimeSeries, Vector, Lock Management |
| Admin Services | Admin Service, Seal Management, Shard Control, Cross-Tenant Audit |
| Management Services | User Management, Role Management, Certificate Management, Tenant Management, Database Management, Collection Management, Volume Pool Management, Registration Configuration |
| Security Services | Credential Management, User Authentication Factors |
| AI Services | Embedding, RAG |

## Global Registry

All permissions are registered into a global `sync.RWMutex`-protected map during `init()`. The registry enables runtime validation -- the role store calls `IsRegistered()` before accepting a permission assignment.

```go
// Registration flow:
// constants.go init() → Register(cat.Permissions...) for each category
// → registry map[Permission]struct{}
```

## Protocol Endpoints

Two distinct operations are exposed:

- **Catalog** — lists every *registered* permission grouped by category (for UI
  rendering). It is static and identity-independent.
- **Resolve** — computes the *effective* permission set (sorted, deduplicated
  union) for a given set of role IDs, delegating to the role service's
  `ResolvePermissions`. Requires `permissions:resolve`.

| Operation | REST | gRPC | QUIC | MCP |
|-----------|------|------|------|-----|
| Catalog | `GET /permissions` | `ListPermissions` | `role.ListPermissions` | `qrdb_permissions_list` |
| Resolve | `POST /roles/resolve-permissions` | `ResolveRolePermissions` | `role.ResolvePermissions` | `qrdb_permissions_resolve` |

Both are also available on the Unix and embedded transports and via the
`qrdb roles permissions` / `qrdb roles resolve-permissions` CLI commands.

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
