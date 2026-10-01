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
// Self-scoped permissions
//
// Every permission registered here is tagged ScopeSelf at init time: admitted
// when the identity's tenant is the request's target tenant, with the
// system-tenant rule lifting it across all tenants. That is the weakest check
// in the scope vocabulary, and until this file existed it was also the one a
// permission received by OMISSION -- LookupScope answered ScopeSelf for any
// permission nobody had tagged, so a permission whose tenancy reach had never
// been considered was indistinguishable from one deliberately confined to its
// own tenant, and the unconsidered one is the one that admits.
//
// That default is gone. LookupScope now refuses an unregistered permission,
// the authorizer denies on that refusal, and TestEveryPermissionDeclaresItsScope
// fails the build if any permission in All() has no scope. So a permission
// added without a scope is a red test, not a quiet admit.
//
// PIN EACH MEMBER, NOT THE FAMILY. Every permission below is named
// individually rather than pulled in through a family helper, because a
// helper that later gains a member would tag that member silently -- the
// exact failure docs/identity-rbac/engineering.md records, where a family
// registration swept orchestrator:health along and locked out the operator
// persona. A permission added to a family does not appear here until someone
// writes its name, and until then the gate is red.
//
// Every entry here carries the scope it had before this file was written:
// this change declares 220 decisions, it does not alter any. A permission
// that should be narrower than ScopeSelf -- and the day-one measurement that
// motivated this file suggests several are worth asking about -- moves out of
// this list and into tenant_scope.go or cross_scope.go as its own reviewed
// change, one permission at a time.
//
// The cert-binding pair (users:add_cert_binding, users:remove_cert_binding)
// is registered ScopeSelf beside its definitions in constants.go rather than
// here; it was the first pair declared deliberately, and its rationale stays
// with it.
// ---------------------------------------------------------------------------

// selfScopedPermissions returns every permission confined to the caller's own
// tenant (with the system-tenant lift), as a single slice. The init() function
// below registers each in the scope registry.
func selfScopedPermissions() []Permission {
	return []Permission{
		// acme_accounts
		ACMEAccountsDisable,
		ACMEAccountsList,
		ACMEAccountsRead,

		// acme_eab
		ACMEEABCreate,
		ACMEEABDelete,
		ACMEEABList,
		ACMEEABRead,

		// acme_orders
		ACMEOrdersList,
		ACMEOrdersRead,

		// admin
		//
		// AdminAddMember, AdminRemoveMember, AdminDrain, AdminSnapshot,
		// AdminStartShard, AdminStopShard, AdminShardState, AdminTopology and
		// AdminNodeCompaction are ScopeCross (cluster_admin_scope.go): they
		// change or expose the node's Raft membership, its shards or its node
		// keyspace, which no tenant owns.
		// AdminPeers stays here because the etcd listener's Cluster.MemberList
		// is a tenant-facing read authorized against the database's owning
		// tenant.
		AdminCapabilities,
		AdminHealth,
		AdminPeers,
		AdminStatus,

		// audit
		//
		// AuditVerify is NOT here: it is registered ScopeCross in
		// cross_scope.go instead, because the hash chain it verifies covers
		// the whole node's audit trail, not a tenant-filtered slice of it.
		AuditQuery,
		AuditRead,
		AuditStream,

		// authfactor
		AuthFactorChallenge,
		AuthFactorEnroll,
		AuthFactorList,
		AuthFactorRevoke,
		AuthFactorVerify,

		// backends
		BackendsList,
		BackendsRead,

		// backup
		//
		// backup:* is ScopeCross (cluster_admin_scope.go): a backup is of
		// shards, holding every tenant's data on them.

		// batch
		BatchExecute,

		// ca
		CACreate,
		CAList,
		CARead,
		CAUpdate,

		// ceremony
		//
		// CeremonyInitialize and CeremonySignCSR are ScopeCross
		// (node_barrier_scope.go): they decide who holds a share of the node
		// barrier's key, and sign with the cluster CA.
		CeremonyStatus,

		// certs: ScopeSelfStrict (self_strict_scope.go), except
		// CertsRevokeCross, which is ScopeCross.

		// collections
		CollectionsCreate,
		CollectionsDelete,
		CollectionsDrop,
		CollectionsInsert,
		CollectionsList,
		CollectionsRead,
		CollectionsUpdate,

		// databases
		DatabasesBarrierStatus,
		DatabasesCreate,
		DatabasesCreateRaft,
		DatabasesCreateVolume,
		DatabasesDelete,
		DatabasesList,
		DatabasesRead,
		DatabasesScale,
		DatabasesSeal,
		DatabasesUnseal,
		DatabasesUpdate,
		DatabasesOfficerRegister,
		DatabasesOfficerList,
		DatabasesOfficerRevoke,
		DatabasesRekey,
		DatabasesInitializeShamir,

		// dek. ScopeSelf lets a system-tenant identity pass the authorizer for
		// another tenant; the keyring service closes that by acting on the
		// caller's own tenant only.
		DEKEncrypt, DEKDecrypt, DEKRewrap, DEKRotate, DEKRetire, DEKStatus,
		// embedding
		EmbeddingEmbed,
		EmbeddingListModels,

		// index
		IndexCreate,
		IndexDrop,
		IndexList,
		IndexQuery,
		IndexScan,

		// keys
		KeysCreate,
		KeysDecrypt,
		KeysDelete,
		KeysDerive,
		KeysEncrypt,
		KeysExport,
		KeysGet,
		KeysImport,
		KeysList,
		KeysRotate,
		KeysSign,
		KeysUnwrap,
		KeysVerify,
		KeysWrap,

		// kv
		KVDelete,
		KVExists,
		KVGet,
		KVList,
		KVPut,
		KVRange,

		// lease
		LeaseGrant,
		LeaseKeepAlive,
		LeaseList,
		LeaseRevoke,
		LeaseTTL,

		// locks
		LocksAcquire,
		LocksExtend,
		LocksList,
		LocksRelease,
		LocksStatus,

		// mvcc
		MVCCCurrentRevision,
		MVCCGetAtRevision,
		MVCCGetRevision,
		MVCCPutWithLease,
		MVCCRange,
		MVCCRestore,

		// orchestrator
		OrchestratorHealth,

		// passwords
		PasswordsDelete,
		PasswordsGenerate,
		PasswordsLock,
		PasswordsRead,
		PasswordsUnlock,
		PasswordsWrite,

		// permissions
		PermissionsList,
		PermissionsResolve,

		// pin
		PINRead,
		PINWrite,

		// piv
		PIVDelete,
		PIVRead,
		PIVUse,
		PIVWrite,

		// rag
		RAGDeleteDocument,
		RAGIngest,
		RAGListDocuments,
		RAGQuery,

		// rebalance
		//
		// rebalance:* is ScopeCross (cluster_admin_scope.go): it moves and
		// describes the cluster's shards.

		// roles
		RolesAssign,
		RolesCreate,
		RolesDelete,
		RolesList,
		RolesRead,
		RolesUnassign,
		RolesUpdate,

		// seal
		//
		// Every other seal:* permission is ScopeCross
		// (node_barrier_scope.go): it changes the node barrier, a platform
		// singleton no tenant owns, or decides who holds a share of its key.
		// SealStatus stays here: it reads the node's sealed state, strategy
		// and Shamir threshold, and names no share holder.
		SealStatus,

		// ssh
		SSHDelete,
		SSHRead,
		SSHWrite,

		// tenant_barrier
		//
		// Carried at the scope it had (ScopeSelf), which is what this file
		// declares and nothing more: its doc comment in constants.go says
		// "Tenant-scoped (ScopeTenantOnly)" like its eight tenant_barrier
		// siblings in tenant_scope.go, but it was never registered there, so
		// it defaulted to ScopeSelf and the system tenant could hold it. No
		// handler enforces it yet. Whether it joins its siblings is an owner
		// decision to make as its own change, not a side effect of this file.
		TenantBarrierEnableEncryption,

		// tenants
		//
		// tenants:create, tenants:set_quotas, tenants:set_entitlements and
		// tenants:set_isolation are NOT here: they are platform-operator
		// operations, ScopeCross, declared in tenant_provisioning_scope.go.
		TenantsDelete,
		TenantsList,
		TenantsRead,
		TenantsUpdate,

		// timeseries
		TSAggregate,
		TSDeleteSeries,
		TSGetRetention,
		TSListMetrics,
		TSQuery,
		TSRichQuery,
		TSSetRetention,
		TSWrite,
		TSWriteBatch,
		TSWritePoint,

		// transaction
		TransactionExecute,

		// user_authfactors
		UserAuthFactorsAdd,
		UserAuthFactorsGet,
		UserAuthFactorsList,
		UserAuthFactorsLogin,
		UserAuthFactorsRelabel,
		UserAuthFactorsRemove,
		UserAuthFactorsRotate,

		// users
		UsersCreate,
		UsersDelete,
		UsersDisable,
		UsersEnable,
		UsersGetByCertThumbprint,
		UsersList,
		UsersRead,
		UsersUpdate,

		// vector
		VectorBatchDelete,
		VectorBatchUpsert,
		VectorCompact,
		VectorDelete,
		VectorGet,
		VectorGetCompactProg,
		VectorGetIndexStats,
		VectorGetRebuildProg,
		VectorHybridSearch,
		VectorList,
		VectorNamespaceCreate,
		VectorNamespaceDelete,
		VectorNamespaceInfo,
		VectorNamespaceList,
		VectorRebuildIndex,
		VectorSearchByID,
		VectorSearchByMetadata,
		VectorSearchSimilar,
		VectorUpdateMetadata,
		VectorUpsert,

		// watch
		WatchWatch,
		WatchWatchPrefix,
	}
}

func init() {
	RegisterScope(ScopeSelf, selfScopedPermissions()...)
}
