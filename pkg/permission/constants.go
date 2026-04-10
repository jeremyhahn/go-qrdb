// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package permission

// ---------------------------------------------------------------------------
// KV permissions
// ---------------------------------------------------------------------------

const (
	// KVPut grants write access to key-value pairs.
	KVPut Permission = "kv:put"
	// KVGet grants read access to key-value pairs.
	KVGet Permission = "kv:get"
	// KVDelete grants delete access to key-value pairs.
	KVDelete Permission = "kv:delete"
	// KVList grants list access to key-value pairs.
	KVList Permission = "kv:list"
	// KVExists grants existence check access to key-value pairs.
	KVExists Permission = "kv:exists"
	// KVRange grants range scan access to key-value pairs.
	KVRange Permission = "kv:range"
)

// ---------------------------------------------------------------------------
// MVCC permissions
// ---------------------------------------------------------------------------

const (
	// MVCCPutWithLease grants write access with lease attachment.
	MVCCPutWithLease Permission = "mvcc:put_with_lease"
	// MVCCGetRevision grants read access to a specific revision.
	MVCCGetRevision Permission = "mvcc:get_revision"
	// MVCCGetAtRevision grants read access at a historical revision.
	MVCCGetAtRevision Permission = "mvcc:get_at_revision"
	// MVCCRange grants range scan access over MVCC data.
	MVCCRange Permission = "mvcc:range"
	// MVCCCompact grants compaction access to MVCC data.
	MVCCCompact Permission = "mvcc:compact"
	// MVCCCurrentRevision grants access to query the current revision.
	MVCCCurrentRevision Permission = "mvcc:current_revision"
)

// ---------------------------------------------------------------------------
// Transaction permissions
// ---------------------------------------------------------------------------

const (
	// TransactionExecute grants access to execute compare-and-swap transactions.
	TransactionExecute Permission = "transaction:execute"
)

// ---------------------------------------------------------------------------
// Batch permissions
// ---------------------------------------------------------------------------

const (
	// BatchExecute grants access to execute batch operations.
	BatchExecute Permission = "batch:execute"
)

// ---------------------------------------------------------------------------
// Watch permissions
// ---------------------------------------------------------------------------

const (
	// WatchWatch grants access to watch a single key for changes.
	WatchWatch Permission = "watch:watch"
	// WatchWatchPrefix grants access to watch a key prefix for changes.
	WatchWatchPrefix Permission = "watch:watch_prefix"
)

// ---------------------------------------------------------------------------
// Lease permissions
// ---------------------------------------------------------------------------

const (
	// LeaseGrant grants access to create new leases.
	LeaseGrant Permission = "lease:grant"
	// LeaseRevoke grants access to revoke existing leases.
	LeaseRevoke Permission = "lease:revoke"
	// LeaseKeepAlive grants access to refresh lease TTL.
	LeaseKeepAlive Permission = "lease:keepalive"
	// LeaseTTL grants access to query lease time-to-live.
	LeaseTTL Permission = "lease:ttl"
	// LeaseList grants access to list all active leases.
	LeaseList Permission = "lease:list"
)

// ---------------------------------------------------------------------------
// Index permissions
// ---------------------------------------------------------------------------

const (
	// IndexCreate grants access to create secondary indexes.
	IndexCreate Permission = "index:create"
	// IndexDrop grants access to drop secondary indexes.
	IndexDrop Permission = "index:drop"
	// IndexQuery grants access to query secondary indexes.
	IndexQuery Permission = "index:query"
	// IndexScan grants access to scan secondary indexes.
	IndexScan Permission = "index:scan"
)

// ---------------------------------------------------------------------------
// TimeSeries permissions
// ---------------------------------------------------------------------------

const (
	// TSWrite grants access to write time-series data points.
	TSWrite Permission = "timeseries:write"
	// TSWriteBatch grants access to write time-series data points in batches.
	TSWriteBatch Permission = "timeseries:write_batch"
	// TSQuery grants access to query time-series data.
	TSQuery Permission = "timeseries:query"
	// TSAggregate grants access to aggregate time-series data.
	TSAggregate Permission = "timeseries:aggregate"
	// TSWritePoint grants access to write a single validated data point.
	TSWritePoint Permission = "timeseries:write_point"
	// TSRichQuery grants access to perform rich queries with aggregation and grouping.
	TSRichQuery Permission = "timeseries:rich_query"
	// TSDeleteSeries grants access to delete time-series data.
	TSDeleteSeries Permission = "timeseries:delete_series"
	// TSGetRetention grants access to read retention policies.
	TSGetRetention Permission = "timeseries:get_retention"
	// TSSetRetention grants access to set retention policies.
	TSSetRetention Permission = "timeseries:set_retention"
	// TSListMetrics grants access to list metric names.
	TSListMetrics Permission = "timeseries:list_metrics"
)

// ---------------------------------------------------------------------------
// Vector permissions
// ---------------------------------------------------------------------------

const (
	// VectorNamespaceCreate grants access to create vector namespaces.
	VectorNamespaceCreate Permission = "vector:namespace_create"
	// VectorNamespaceDelete grants access to delete vector namespaces.
	VectorNamespaceDelete Permission = "vector:namespace_delete"
	// VectorNamespaceList grants access to list vector namespaces.
	VectorNamespaceList Permission = "vector:namespace_list"
	// VectorNamespaceInfo grants access to query vector namespace info.
	VectorNamespaceInfo Permission = "vector:namespace_info"
	// VectorUpsert grants access to upsert vectors.
	VectorUpsert Permission = "vector:upsert"
	// VectorGet grants access to get vectors by ID.
	VectorGet Permission = "vector:get"
	// VectorDelete grants access to delete vectors.
	VectorDelete Permission = "vector:delete"
	// VectorBatchUpsert grants access to batch upsert vectors.
	VectorBatchUpsert Permission = "vector:batch_upsert"
	// VectorBatchDelete grants access to batch delete vectors.
	VectorBatchDelete Permission = "vector:batch_delete"
	// VectorList grants access to list vectors.
	VectorList Permission = "vector:list"
	// VectorUpdateMetadata grants access to update vector metadata.
	VectorUpdateMetadata Permission = "vector:update_metadata"
	// VectorSearchSimilar grants access to search for similar vectors.
	VectorSearchSimilar Permission = "vector:search_similar"
	// VectorSearchByMetadata grants access to search vectors by metadata.
	VectorSearchByMetadata Permission = "vector:search_by_metadata"
	// VectorHybridSearch grants access to hybrid vector/metadata search.
	VectorHybridSearch Permission = "vector:hybrid_search"
	// VectorSearchByID grants access to search vectors by ID.
	VectorSearchByID Permission = "vector:search_by_id"
	// VectorRebuildIndex grants access to rebuild vector indexes.
	VectorRebuildIndex Permission = "vector:rebuild_index"
	// VectorGetRebuildProg grants access to query vector index rebuild progress.
	VectorGetRebuildProg Permission = "vector:get_rebuild_progress"
	// VectorGetIndexStats grants access to query vector index statistics.
	VectorGetIndexStats Permission = "vector:get_index_stats"
	// VectorCompact grants access to compact vector storage.
	VectorCompact Permission = "vector:compact"
	// VectorGetCompactProg grants access to query vector compaction progress.
	VectorGetCompactProg Permission = "vector:get_compaction_progress"
)

// ---------------------------------------------------------------------------
// Admin permissions
// ---------------------------------------------------------------------------

const (
	// AdminHealth grants access to health check endpoints.
	AdminHealth Permission = "admin:health"
	// AdminStatus grants access to cluster status endpoints.
	AdminStatus Permission = "admin:status"
	// AdminPeers grants access to peer listing endpoints.
	AdminPeers Permission = "admin:peers"
	// AdminAddMember grants access to add cluster members.
	AdminAddMember Permission = "admin:add_member"
	// AdminRemoveMember grants access to remove cluster members.
	AdminRemoveMember Permission = "admin:remove_member"
	// AdminSnapshot grants access to trigger cluster snapshots.
	AdminSnapshot Permission = "admin:snapshot"
	// AdminDrain grants access to drain a node for maintenance.
	AdminDrain Permission = "admin:drain"
	// AdminCapabilities grants access to query node capabilities.
	AdminCapabilities Permission = "admin:capabilities"
)

// ---------------------------------------------------------------------------
// User management permissions
// ---------------------------------------------------------------------------

const (
	// UsersCreate grants access to create user accounts.
	UsersCreate Permission = "users:create"
	// UsersRead grants access to read user account details.
	UsersRead Permission = "users:read"
	// UsersUpdate grants access to update user account details.
	UsersUpdate Permission = "users:update"
	// UsersDelete grants access to delete user accounts.
	UsersDelete Permission = "users:delete"
	// UsersList grants access to list user accounts.
	UsersList Permission = "users:list"
	// UsersEnable grants access to enable user accounts.
	UsersEnable Permission = "users:enable"
	// UsersDisable grants access to disable user accounts.
	UsersDisable Permission = "users:disable"
)

// ---------------------------------------------------------------------------
// Role management permissions
// ---------------------------------------------------------------------------

const (
	// RolesCreate grants access to create roles.
	RolesCreate Permission = "roles:create"
	// RolesRead grants access to read role details.
	RolesRead Permission = "roles:read"
	// RolesUpdate grants access to update role details.
	RolesUpdate Permission = "roles:update"
	// RolesDelete grants access to delete roles.
	RolesDelete Permission = "roles:delete"
	// RolesList grants access to list roles.
	RolesList Permission = "roles:list"
	// RolesAssign grants access to assign roles to users.
	RolesAssign Permission = "roles:assign"
	// RolesUnassign grants access to unassign roles from users.
	RolesUnassign Permission = "roles:unassign"
	// PermissionsList grants access to list available permission categories.
	PermissionsList Permission = "permissions:list"
)

// ---------------------------------------------------------------------------
// Certificate management permissions
// ---------------------------------------------------------------------------

const (
	// CertsGenerate grants access to generate TLS certificates.
	CertsGenerate Permission = "certs:generate"
	// CertsExport grants access to export TLS certificates as PKCS#12.
	CertsExport Permission = "certs:export"
	// CertsRevoke grants access to revoke TLS certificates.
	CertsRevoke Permission = "certs:revoke"
	// CertsList grants access to list TLS certificates.
	CertsList Permission = "certs:list"
)

// ---------------------------------------------------------------------------
// Seal management permissions
// ---------------------------------------------------------------------------

const (
	// SealBootstrap grants access to bootstrap the seal subsystem.
	SealBootstrap Permission = "seal:bootstrap"
	// SealUnseal grants access to unseal the node.
	SealUnseal Permission = "seal:unseal"
	// SealSeal grants access to seal the node.
	SealSeal Permission = "seal:seal"
	// SealStatus grants access to query seal status.
	SealStatus Permission = "seal:status"
)

// ---------------------------------------------------------------------------
// Tenant management permissions
// ---------------------------------------------------------------------------

const (
	// TenantsCreate grants access to create tenants.
	TenantsCreate Permission = "tenants:create"
	// TenantsRead grants access to read tenant details.
	TenantsRead Permission = "tenants:read"
	// TenantsUpdate grants access to update tenant details.
	TenantsUpdate Permission = "tenants:update"
	// TenantsDelete grants access to delete tenants.
	TenantsDelete Permission = "tenants:delete"
	// TenantsList grants access to list tenants.
	TenantsList Permission = "tenants:list"
)

// ---------------------------------------------------------------------------
// Categories (for UI rendering)
// ---------------------------------------------------------------------------

// allVectorPermissions returns all vector permissions in a single slice.
func allVectorPermissions() []Permission {
	return []Permission{
		VectorNamespaceCreate, VectorNamespaceDelete, VectorNamespaceList, VectorNamespaceInfo,
		VectorUpsert, VectorGet, VectorDelete, VectorBatchUpsert, VectorBatchDelete, VectorList,
		VectorUpdateMetadata, VectorSearchSimilar, VectorSearchByMetadata, VectorHybridSearch,
		VectorSearchByID, VectorRebuildIndex, VectorGetRebuildProg, VectorGetIndexStats,
		VectorCompact, VectorGetCompactProg,
	}
}

// categories is the authoritative grouping of permissions for UI rendering.
var categories = []Category{
	{
		Name:  "KV Service",
		Group: "Data Services",
		Permissions: []Permission{
			KVPut, KVGet, KVDelete, KVList, KVExists, KVRange,
		},
	},
	{
		Name:  "MVCC Service",
		Group: "Data Services",
		Permissions: []Permission{
			MVCCPutWithLease, MVCCGetRevision, MVCCGetAtRevision,
			MVCCRange, MVCCCompact, MVCCCurrentRevision,
		},
	},
	{
		Name:  "Transaction Service",
		Group: "Data Services",
		Permissions: []Permission{
			TransactionExecute,
		},
	},
	{
		Name:  "Batch Service",
		Group: "Data Services",
		Permissions: []Permission{
			BatchExecute,
		},
	},
	{
		Name:  "Watch Service",
		Group: "Data Services",
		Permissions: []Permission{
			WatchWatch, WatchWatchPrefix,
		},
	},
	{
		Name:  "Lease Service",
		Group: "Data Services",
		Permissions: []Permission{
			LeaseGrant, LeaseRevoke, LeaseKeepAlive, LeaseTTL, LeaseList,
		},
	},
	{
		Name:  "Index Service",
		Group: "Data Services",
		Permissions: []Permission{
			IndexCreate, IndexDrop, IndexQuery, IndexScan,
		},
	},
	{
		Name:  "TimeSeries Service",
		Group: "Data Services",
		Permissions: []Permission{
			TSWrite, TSWriteBatch, TSQuery, TSAggregate,
			TSWritePoint, TSRichQuery, TSDeleteSeries,
			TSGetRetention, TSSetRetention, TSListMetrics,
		},
	},
	{
		Name:        "Vector Service",
		Group:       "Data Services",
		Permissions: allVectorPermissions(),
	},
	{
		Name:  "Admin Service",
		Group: "Admin Services",
		Permissions: []Permission{
			AdminHealth, AdminStatus, AdminPeers, AdminAddMember,
			AdminRemoveMember, AdminSnapshot, AdminDrain, AdminCapabilities,
		},
	},
	{
		Name:  "User Management",
		Group: "Management Services",
		Permissions: []Permission{
			UsersCreate, UsersRead, UsersUpdate, UsersDelete,
			UsersList, UsersEnable, UsersDisable,
		},
	},
	{
		Name:  "Role Management",
		Group: "Management Services",
		Permissions: []Permission{
			RolesCreate, RolesRead, RolesUpdate, RolesDelete,
			RolesList, RolesAssign, RolesUnassign, PermissionsList,
		},
	},
	{
		Name:  "Certificate Management",
		Group: "Management Services",
		Permissions: []Permission{
			CertsGenerate, CertsExport, CertsRevoke, CertsList,
		},
	},
	{
		Name:  "Seal Management",
		Group: "Admin Services",
		Permissions: []Permission{
			SealBootstrap, SealUnseal, SealSeal, SealStatus,
		},
	},
	{
		Name:  "Tenant Management",
		Group: "Management Services",
		Permissions: []Permission{
			TenantsCreate, TenantsRead, TenantsUpdate, TenantsDelete, TenantsList,
		},
	},
}

func init() {
	// Register all permissions from all categories into the global registry.
	for _, cat := range categories {
		Register(cat.Permissions...)
	}
}
