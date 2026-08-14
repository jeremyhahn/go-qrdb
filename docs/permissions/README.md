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
| MVCC | `MVCCRestore` | `mvcc:restore` |
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

> **Note:** The tables above document selected permission categories. The authoritative complete list of all registered permissions is in `pkg/permission/constants.go`, which includes:
> - **Cryptographic key operations** (`keys:*`) — sign, verify, encrypt, decrypt, wrap, unwrap, create, rotate, delete, import, export, derive
> - **Certificate management** (`certs:*`) — generate, export, revoke, list, read
> - **Certificate authority** (`ca:*`) — create, read, list, update
> - **ACME server** (`acme:*`) — account and order management
> - **Seal management** (`seal:*`) — bootstrap, unseal, seal, status, rotate, rekey, recovery, shamir, officer operations
> - **Ceremony** (`ceremony:*`) — initialization, custodian enrollment, CSR signing
> - **Tenant barriers** (`tenant_barrier:*`) — per-tenant barrier operations and officer management
> - **Credentials** (`credentials:*`) — rotation, versioning, retrieval
> - **Password, SSH, PIN, PIV backends** (`passwords:*`, `ssh:*`, `pin:*`, `piv:*`)
> - **Hardware backends** (`backends:*`) — list, read
> - **Backup & restore** (`backup:*`) — create, list, read, delete, restore, verify
> - **Cluster rebalance** (`rebalance:*`) — stats, plan, execute, report
> - **Volume pools** (`volumepool:*`) — create, list, read, update, delete
> - **Registration policies** (`registrationconfig:*`) — read, update
> - **Authentication factors** (`authfactor:*`) — enroll, list, revoke
> - **User authentication factors** (`user_authfactors:*`)
> - **Database management** (`databases:*`)
> - **Collection management** (`collections:*`)
> - **Broker data-plane families** (`objstore:*`, `orchestrator:*`, `scheduler:*`, `messaging:*`, `parameters:*`, `dynamic_secrets:*`, `project:*`, `ollama:*`)

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

## Built-In Role Default Grants

Source: `pkg/permission/role_grant.go`, `pkg/permission/builtin_role_grants.go`

Role placement is metadata carried by the permission, not a parallel list maintained by `pkg/role`. Each permission that belongs on one of the five registry-derived built-in roles (Security Officer, Admin, Operator, User, Auditor) is registered once, here, with the role(s) it defaults onto:

```go
// RegisterRoleGrant records that perms are granted by default to role.
func RegisterRoleGrant(role string, perms ...Permission)

// RolePermissions returns every permission granted to role, sorted. It is
// evaluated against the live registry on every call -- not memoized at
// package-init time -- so a permission registered by a higher layer
// (go-quicbaas, go-xkms) before role seeding is reflected.
func RolePermissions(role string) []Permission

// HasRoleGrant reports whether p is granted to role by default.
func HasRoleGrant(role string, p Permission) bool
```

`pkg/role.BuiltInRoles` materializes each of the five roles by calling `RolePermissions` directly -- there is no hand-maintained permission list in `pkg/role` to keep in sync. This closed a drift hazard: previously `pkg/role/store.go` held five separate hard-coded permission lists (~219 entries) that a contributor had to update by hand whenever a permission's role placement changed, with no compiler check that the two stayed consistent.

The five canonical role names (`RoleSO`, `RoleAdmin`, `RoleOperator`, `RoleUser`, `RoleAuditor`) are untyped string constants; `pkg/role`'s `RoleNameSO` / `RoleNameAdmin` / etc. alias them directly.

### Structural separation-of-duties enforcement (FIPS 140-3 AS10.03)

When `role == RoleSO`, `RegisterRoleGrant` admits **only** permissions declared `DutyGovernance`. Everything else panics, with a distinct error for each diagnosis:

| Rejected grant | Panic | Why |
|----------------|-------|-----|
| A cryptographic permission | `SoDViolationError` | The invariant itself. Covers any permission declared `DutyCryptoUse` (`keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt`, `keys:wrap`, `keys:unwrap`, `keys:derive`, `piv:use`) **and** any resource whose action names a cryptographic primitive -- `sign`, `encrypt`, `decrypt`, `wrap`, `unwrap`, `derive`, `kdf`, `mac`, `hmac`, `attest`, `quote` -- including resources contributed by a higher layer, such as go-quicbaas's `attestation:attest`. |
| A permission whose duty nobody declared | `UndeclaredDutyError` | Fail-safe. Silence is a denial, so a new permission cannot become SO-grantable by omission. The message names the `RegisterDuty` call the author must add. |
| A whole-segment wildcard (`keys:*`) | `WildcardGrantError` | A wildcard spans verbs of different duties, so it cannot be classified, and the authorizer would expand it onto `keys:sign`. Other roles may hold wildcards; the SO must name every permission it holds. |

The classification is metadata the permission's **author** declares, exactly as role placement and tenancy reach are:

```go
// RegisterDuty declares which separated duty perms belong to. Immutable:
// a conflicting re-declaration panics with *DutyConflictError, so a
// cryptographic permission cannot later be relabelled as governance.
func RegisterDuty(duty Duty, perms ...Permission)

// LookupDuty returns p's declared duty, or DutyUndeclared.
func LookupDuty(p Permission) Duty

// IsCryptoUse reports whether p performs a cryptographic primitive with key
// material: declared DutyCryptoUse, or carrying a crypto primitive verb in
// any resource family (not overridable by a declaration).
func IsCryptoUse(p Permission) bool
```

Only two groups need declaring, in `pkg/permission/builtin_duties.go`: the permissions the SO holds (`DutyGovernance`) and the permissions that perform a cryptographic primitive (`DutyCryptoUse`). Everything else -- the whole tenant data plane -- is deliberately left undeclared, because undeclared already means "not Security-Officer business".

Why a declaration rather than a rule over names: the line is not a resource family (`keys` holds both `keys:sign` and `keys:create`, and cryptographic use is not confined to `keys` -- `piv:use`, `attestation:attest`), and it is not the action verb alone (`seal:unseal` and `ceremony:sign_csr` are cryptographic by mechanism yet are the SO's own FIPS-assigned module lifecycle, while `backup:verify` touches no key). The verb set above is a **backstop** on top of the declaration, not the classification itself: it cannot be overridden by a `DutyGovernance` declaration, so `foo:sign` can never be laundered onto the SO.

The runtime RBAC deny-list in `pkg/auth/rbac` (`soDeniedActions` + `soDeniedCryptoVerbs`) remains as defense-in-depth. It is an independent mechanism -- it classifies the requested `resource:action` itself and never consults the duty registry -- and `TestSODenyList_AgreesWithPermissionDutyRegistry` proves the two agree in both directions rather than leaving it to belief.

Custodian and Owner are **not** derived from this registry: Custodian holds a fixed, minimal share-submission set (`pkg/role.custodianPermissions`) and Owner is the standard-posture convenience superuser defined as `permission.All()` (`pkg/role.OwnerRole`).

## Cross-Layer Extension Points

Source: `sdk/go/types_permission.go`

A higher layer (go-quicbaas, go-xkms) contributing a permission to qrdb's registry has four re-exported extension points, used together at composition-root time, before role seeding:

```go
sdk.RegisterPermissions(myPerm)                                    // add to the registry
sdk.RegisterPermissionScope(sdk.PermissionScopeTenantOnly, myPerm) // tag tenancy reach
sdk.RegisterPermissionDuty(sdk.PermissionDutyCryptoUse, myPerm)    // classify the separated duty
sdk.RegisterPermissionRoleGrant(sdk.PermissionRoleAdmin, myPerm)   // place on a built-in role
```

Without the scope call a contributed permission defaults to `PermissionScopeSelf`; without the role-grant call it is reachable only through a hand-authored custom role. The duty call has no permissive default: it is required only to place a permission on the **Security Officer** role, and it is what a contributor uses to permanently bar a cryptographic permission from that role. Omitting it leaves the permission `PermissionDutyUndeclared`, which is not SO-grantable — the fail-safe answer for the many contributed permissions the Security Officer has no part in.

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
