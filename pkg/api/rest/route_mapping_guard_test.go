// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package rest

// route_mapping_guard_test.go is the structural guard for the REST
// authorization surface. Since the unified-authz cutover, every route is
// registered natively on the chi router through ONE registration helper that
// simultaneously declares the route's permission (or an explicit
// public/authenticated marker) in the server's RESTRouteRegistry — and
// NewServer's DEFAULT-DENY startup assertion walks the chi tree and panics if
// any route escaped the registry. This file guards that machinery:
//
//  1. A fully wired server (every optional service present, so every
//     conditional registration fires) constructs successfully — i.e. the
//     startup assertion holds for the complete surface — and every walked
//     route resolves through the registry.
//  2. Every permission recorded in the registry is registered in the global
//     permission catalog (except the deliberately unregistered
//     permission.IdentityGated sentinel).
//  3. A curated table of concrete request paths — static routes, /kv/{key},
//     and every path-parameter family — resolves to the exact permission the
//     old restPermissions/restPathParamPermissions tables declared, so the
//     migration to registration-site permissions cannot silently change a
//     route's required permission.
//  4. The reviewed public/authenticated exemptions are exactly the five
//     pre-authentication public routes plus /metrics, and nothing else.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// guardEncMigrator is a minimal no-op service.EncryptionMigrator, wired
// solely so newFullyWiredGuardServer registers POST /v1/barrier/enable-encryption
// (gated on a non-nil EncryptionMigrator; see server.go registerRoutes).
type guardEncMigrator struct{}

func (guardEncMigrator) EnableEncryption(context.Context, service.BarrierScope, seal.MigrationOptions) error {
	return nil
}

// guardCACertPEM is a CERTIFICATE block: WithCABootstrap keeps certificate
// blocks only, so anything else would leave /v1/ca/bootstrap unregistered.
const guardCACertPEM = "-----BEGIN CERTIFICATE-----\nZ3VhcmQ=\n-----END CERTIFICATE-----\n"

// newFullyWiredGuardServer builds a *Server with every optional service and
// ServerOption wired so every conditional route registration in
// registerRoutes fires. It reuses the package's existing test doubles so the
// guard exercises the same construction surface as the rest of the suite.
// guardEntitlementLookup is a minimal no-op service.AuditEntitlementLookup,
// wired solely so newFullyWiredGuardServer can register
// GET /v1/audit/replicated/verify (gated on a non-nil replicatedAudit; see
// server.go registerRoutes). The structural guard never calls through it.
type guardEntitlementLookup struct{}

func (guardEntitlementLookup) ReplicatedAuditEnabled(context.Context, string) (bool, error) {
	return false, nil
}

func newFullyWiredGuardServer(t *testing.T) *Server {
	t.Helper()

	kvSvc := newDefaultKVSvc()
	adminSvc := newDefaultAdminSvc()
	dbSvc := &mockDatabaseService{}

	opts := []ServerOption{
		WithMVCC(&mockMVCCService{}),
		WithTransaction(&mockTransactionService{}),
		WithBatch(&mockBatchService{}),
		WithWatch(&mockWatchService{}),
		WithLease(&mockLeaseService{}), // gates /lease/*
		WithLock(&mockLockService{}),   // gates /lock/*
		WithIndex(&mockIndexService{}),
		WithTimeSeries(&mockTimeSeriesService{}),
		WithVector(&mockVectorService{}),
		WithEmbedding(&mockEmbeddingService{}),          // gates /v1/embeddings*
		WithRAG(&mockRAGService{}),                      // gates /v1/rag/*
		WithDatabaseService(dbSvc),                      // gates /v1/databases*
		WithCollectionService(&mockCollectionService{}), // gates /v1/collections*
		WithUserService(&mockUserService{}),             // gates /users, /users/list
		WithRoleService(&mockRoleService{}),             // gates /roles*
		WithTenantService(&mockTenantService{}),
		WithBackupService(&stubBackupService{}),       // gates /v1/backups*
		WithRebalanceService(&stubRebalanceService{}), // gates /v1/rebalance/*
		WithAuthFactorService(&stubAuthFactorSvc{}),
		WithUserAuthFactorService(&mockUserAuthFactorService{}),
		WithCertProvider(&mockCertProvider{}),             // gates /certs/*
		WithCertEnrollment(&fakeCertEnroll{}),             // gates /certs/issue, /certs/enroll, trust-anchors
		WithCABootstrap([]byte(guardCACertPEM)),           // gates /v1/ca/bootstrap
		WithClusterKeyReceiver(&fakeClusterKeyReceiver{}), // gates /v1/cluster/*
		WithBarrierService(&mockBarrierService{}),         // gates /v1/barrier/*
		WithEncryptionMigrator(guardEncMigrator{}),        // gates /v1/barrier/enable-encryption
		WithCeremonyService(newCoverageCeremonyService()), // gates /v1/ceremony/*
		WithCeremonyCA(&coverageCeremonyCA{}),
		WithCredentialService(newCoverageCredentialService()), // gates /v1/credentials*
		WithAuditLogger(&stubAuditLogger{}),                   // gates /v1/audit/events
		WithReplicatedAuditLogger(service.NewOptInAuditLogger(&stubAuditLogger{}, &guardEntitlementLookup{}, false)), // gates /v1/audit/replicated/verify
		WithGatherer(prometheus.NewRegistry()), // gates /metrics
	}
	opts = append(opts, sqlGuardOptions(kvSvc, dbSvc)...) // gates /sql/query, /v1/sql/query (qrdb_sql only)

	return NewServer(kvSvc, adminSvc, "127.0.0.1:0", opts...)
}

// guardProbePath derives a concrete request path from a chi route pattern by
// replacing path parameters and wildcards with a literal segment.
func guardProbePath(pattern string) string {
	return strings.NewReplacer("{id}", "x", "{name}", "x", "{username}", "x",
		"{metric}", "x", "{factor}", "x", "{thumbprint}", "x", "{database}", "x",
		"{version}", "x", "*", "x").Replace(pattern)
}

// TestRouteMappingGuard_EveryRouteCovered walks the fully wired server's chi
// routing tree and asserts every registered route is covered by the
// routePerms registry. NewServer already panics on a violation (the startup
// assertion), so a failure here means the assertion itself regressed.
func TestRouteMappingGuard_EveryRouteCovered(t *testing.T) {
	srv := newFullyWiredGuardServer(t)

	walked := 0
	err := chi.Walk(srv.router, func(method, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked++
		// chi.Walk emits mounted patterns with a trailing slash; a route may
		// be registered either with or without one, so accept both forms.
		trimmed := routePattern
		if len(trimmed) > 1 {
			trimmed = strings.TrimSuffix(trimmed, "/")
		}
		if !srv.routePerms.Covered(method, routePattern) && !srv.routePerms.Covered(method, trimmed) {
			t.Errorf("route registered but NOT covered by the permission registry (default-deny assertion regressed): %s %s", method, trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if walked < 200 {
		t.Fatalf("guard walked only %d routes -- newFullyWiredGuardServer likely regressed and stopped registering routes", walked)
	}
	t.Logf("route-mapping guard: walked %d routes, all covered by the registry", walked)
}

// TestRouteMappingGuard_PermissionsRegistered asserts every permission a
// route resolves to is registered in the global permission catalog, except
// the deliberately unregistered permission.IdentityGated sentinel (handler-
// enforced binding authorization).
func TestRouteMappingGuard_PermissionsRegistered(t *testing.T) {
	srv := newFullyWiredGuardServer(t)

	err := chi.Walk(srv.router, func(method, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		pattern := routePattern
		// Resolve through the registry with a concrete probe path derived
		// from the pattern (parameters replaced with a literal segment).
		probe := guardProbePath(pattern)
		perm, kind, ok := srv.routePerms.Lookup(method, probe)
		if !ok {
			t.Errorf("route %s %s (probe %s) does not resolve through the registry", method, pattern, probe)
			return nil
		}
		if kind != authmw.RESTRoutePermission {
			return nil // public / authenticated markers carry no permission
		}
		if perm == permission.IdentityGated {
			return nil // sentinel: deliberately unregistered
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("route %s %s maps to unregistered permission %q", method, pattern, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}

// TestRouteMappingGuard_RoutePermissions pins the exact permission each
// route family requires, driven through the real registry with concrete
// request paths. This table carries forward the guarantees of the deleted
// restPermissions / restPathParamPermissions tables (and their tests in
// pkg/auth/middleware/mapping_test.go): a migration or refactor cannot
// silently change a route's required permission.
func TestRouteMappingGuard_RoutePermissions(t *testing.T) {
	srv := newFullyWiredGuardServer(t)

	cases := []struct {
		method string
		path   string
		want   permission.Permission
	}{
		// KV data plane (the /kv/{key} wildcard, method-dispatched).
		{"PUT", "/kv/somekey", permission.KVPut},
		{"GET", "/kv/somekey", permission.KVGet},
		{"DELETE", "/kv/somekey", permission.KVDelete},
		{"GET", "/kv", permission.KVList},
		{"GET", "/kv/scan", permission.KVRange},
		{"GET", "/kv/range", permission.KVRange},
		{"GET", "/kv/revision", permission.MVCCCurrentRevision},
		{"POST", "/kv/compact", permission.MVCCCompact},
		{"POST", "/kv/restore", permission.MVCCRestore},

		// Core data plane.
		{"POST", "/tx", permission.TransactionExecute},
		{"POST", "/batch", permission.BatchExecute},
		{"POST", "/batch/import/begin", permission.BatchExecute},
		{"POST", "/batch/import/sess1/append", permission.BatchExecute},
		{"POST", "/batch/import/sess1/commit", permission.BatchExecute},
		{"POST", "/batch/import/sess1/abort", permission.BatchExecute},
		{"GET", "/watch", permission.WatchWatch},

		// Lease + lock.
		{"POST", "/lease/grant", permission.LeaseGrant},
		{"POST", "/lease/revoke", permission.LeaseRevoke},
		{"POST", "/lease/keepalive", permission.LeaseKeepAlive},
		{"GET", "/lease/ttl", permission.LeaseTTL},
		{"GET", "/lease/list", permission.LeaseList},
		{"POST", "/lock/acquire", permission.LocksAcquire},
		{"POST", "/lock/trylock", permission.LocksAcquire},
		{"POST", "/lock/release", permission.LocksRelease},
		{"POST", "/lock/extend", permission.LocksExtend},
		{"GET", "/lock/status", permission.LocksStatus},
		{"GET", "/lock/list", permission.LocksList},

		// Index.
		{"POST", "/index/create", permission.IndexCreate},
		{"POST", "/index/drop", permission.IndexDrop},
		{"GET", "/index/query", permission.IndexQuery},
		{"GET", "/index/scan", permission.IndexScan},
		{"GET", "/index/list", permission.IndexList},

		// TimeSeries, including the per-metric family.
		{"POST", "/ts/write", permission.TSWrite},
		{"POST", "/ts/write-batch", permission.TSWriteBatch},
		{"POST", "/ts/write-point", permission.TSWritePoint},
		{"GET", "/ts/query", permission.TSQuery},
		{"POST", "/ts/query-rich", permission.TSRichQuery},
		{"GET", "/ts/aggregate", permission.TSAggregate},
		{"GET", "/ts/metrics", permission.TSListMetrics},
		{"DELETE", "/ts/series/cpu.load", permission.TSDeleteSeries},
		{"GET", "/ts/series/cpu.load/retention", permission.TSGetRetention},
		{"PUT", "/ts/series/cpu.load/retention", permission.TSSetRetention},

		// Admin, including the per-shard family.
		{"GET", "/status", permission.AdminStatus},
		{"GET", "/peers", permission.AdminPeers},
		{"POST", "/membership/add", permission.AdminAddMember},
		{"POST", "/membership/remove", permission.AdminRemoveMember},
		{"POST", "/membership/promote", permission.AdminAddMember},
		{"POST", "/membership/transfer-leadership", permission.AdminRemoveMember},
		{"POST", "/snapshot", permission.AdminSnapshot},
		{"POST", "/drain", permission.AdminDrain},
		{"GET", "/capabilities", permission.AdminCapabilities},
		{"POST", "/admin/shards", permission.AdminStartShard},
		{"GET", "/admin/topology", permission.AdminTopology},
		{"GET", "/admin/replica-states", permission.AdminReplicaState},
		{"GET", "/admin/node-compaction", permission.AdminNodeCompaction},
		{"PUT", "/admin/node-compaction", permission.AdminNodeCompaction},
		{"GET", "/admin/state-hashes", permission.AdminStateHash},
		{"DELETE", "/admin/shards/1", permission.AdminStopShard},
		{"GET", "/admin/shards/1/state", permission.AdminShardState},

		// Vector.
		{"POST", "/vector/namespace", permission.VectorNamespaceCreate},
		{"POST", "/vector/namespace/delete", permission.VectorNamespaceDelete},
		{"GET", "/vector/namespaces", permission.VectorNamespaceList},
		{"GET", "/vector/namespace/info", permission.VectorNamespaceInfo},
		{"POST", "/vector/upsert", permission.VectorUpsert},
		{"GET", "/vector/get", permission.VectorGet},
		{"POST", "/vector/delete", permission.VectorDelete},
		{"POST", "/vector/batch/upsert", permission.VectorBatchUpsert},
		{"POST", "/vector/batch/delete", permission.VectorBatchDelete},
		{"GET", "/vector/list", permission.VectorList},
		{"POST", "/vector/metadata", permission.VectorUpdateMetadata},
		{"POST", "/vector/search/similar", permission.VectorSearchSimilar},
		{"POST", "/vector/search/metadata", permission.VectorSearchByMetadata},
		{"POST", "/vector/search/hybrid", permission.VectorHybridSearch},
		{"POST", "/vector/search/by-id", permission.VectorSearchByID},
		{"POST", "/vector/index/rebuild", permission.VectorRebuildIndex},
		{"GET", "/vector/index/rebuild/progress", permission.VectorGetRebuildProg},
		{"GET", "/vector/index/stats", permission.VectorGetIndexStats},
		{"POST", "/vector/compact", permission.VectorCompact},
		{"GET", "/vector/compact/progress", permission.VectorGetCompactProg},

		// Embedding + RAG.
		{"POST", "/v1/embeddings", permission.EmbeddingEmbed},
		{"POST", "/v1/embeddings/batch", permission.EmbeddingEmbed},
		{"GET", "/v1/embedding/models", permission.EmbeddingListModels},
		{"POST", "/v1/rag/ingest", permission.RAGIngest},
		{"POST", "/v1/rag/query", permission.RAGQuery},
		{"POST", "/v1/rag/documents/delete", permission.RAGDeleteDocument},
		{"GET", "/v1/rag/documents", permission.RAGListDocuments},

		// SQL (read-through over the KV range surface).
		{"POST", "/sql/query", permission.KVRange},
		{"POST", "/v1/sql/query", permission.KVRange},

		// Users, including the per-user family.
		{"POST", "/users", permission.UsersCreate},
		{"GET", "/users/list", permission.UsersList},
		{"GET", "/users/abc123", permission.UsersRead},
		{"PUT", "/users/abc123", permission.UsersUpdate},
		{"DELETE", "/users/abc123", permission.UsersDelete},
		{"POST", "/users/abc123/enable", permission.UsersEnable},
		{"POST", "/users/abc123/disable", permission.UsersDisable},
		{"POST", "/users/abc123/cert-bindings", permission.UsersAddCertBinding},
		{"DELETE", "/users/abc123/cert-bindings/AAAA", permission.UsersRemoveCertBinding},
		{"GET", "/users/by-cert-thumbprint/AbC-_123", permission.UsersGetByCertThumbprint},
		{"GET", "/users/by-username/admin", permission.UsersRead},

		// Roles, including the per-role family.
		{"POST", "/roles", permission.RolesCreate},
		{"GET", "/roles/list", permission.RolesList},
		{"GET", "/roles/by-name", permission.RolesRead},
		{"POST", "/roles/resolve-permissions", permission.PermissionsResolve},
		{"POST", "/roles/reconcile", permission.RolesCreate},
		{"POST", "/roles/assign", permission.RolesAssign},
		{"POST", "/roles/unassign", permission.RolesUnassign},
		{"GET", "/permissions", permission.PermissionsList},
		{"GET", "/roles/r-1", permission.RolesRead},
		{"PUT", "/roles/r-1", permission.RolesUpdate},
		{"DELETE", "/roles/r-1", permission.RolesDelete},

		// Certificates.
		{"POST", "/certs/issue", permission.CertsIssue},
		{"POST", "/certs/enroll", permission.CertsEnroll},
		{"POST", "/certs/delegate", permission.CertsDelegate},
		{"POST", "/certs/revoke", permission.CertsRevoke},
		{"GET", "/certs/list", permission.CertsList},

		// Trust anchors, tenant-scoped.
		{"GET", "/v1/tenants/acme/trust-anchors", permission.TrustAnchorsList},
		{"POST", "/v1/tenants/acme/trust-anchors", permission.TrustAnchorsAdd},
		{"DELETE", "/v1/tenants/acme/trust-anchors/fp-1", permission.TrustAnchorsRemove},

		// Tenants, including the per-tenant barrier plane.
		{"POST", "/v1/tenants", permission.TenantsCreate},
		{"GET", "/v1/tenants/list", permission.TenantsList},
		{"GET", "/v1/tenants/deleted/list", permission.TenantsListDeleted},
		{"POST", "/v1/tenants/deleted/reconcile", permission.TenantsReconcileDeleted},
		// A tenant NAMED "deleted" still resolves through the {id} route.
		{"GET", "/v1/tenants/deleted", permission.TenantsRead},
		{"GET", "/v1/tenants/t-1", permission.TenantsRead},
		{"PUT", "/v1/tenants/t-1", permission.TenantsUpdate},
		{"DELETE", "/v1/tenants/t-1", permission.TenantsDelete},
		{"POST", "/v1/tenants/t-1/quotas", permission.TenantsSetQuotas},
		{"PATCH", "/v1/tenants/t-1/entitlements", permission.TenantsSetEntitlements},
		// status/init/unseal are IdentityGated at the route: a self-managed
		// tenant's IAM is encrypted behind the very barrier these operate
		// on, so the RBAC check runs inside the handlers with a
		// sealed-window identity fallback (authorizeTenantBarrierSealedOp /
		// authorizeTenantBarrierInit).
		{"GET", "/v1/tenants/t-1/barrier/status", permission.IdentityGated},
		{"GET", "/v1/tenants/t-1/barrier/officers", permission.TenantBarrierOfficerList},
		{"POST", "/v1/tenants/t-1/barrier/init/begin", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/init/complete", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/unseal", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/seal", permission.TenantBarrierSeal},
		{"POST", "/v1/tenants/t-1/barrier/officers", permission.TenantBarrierOfficerRegister},
		// A share holder's claim and quorum steps are identity-gated: the
		// holder's certificate and factor proof are the credential.
		{"POST", "/v1/tenants/t-1/barrier/officers/alice/claim/begin", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/officers/alice/claim/complete", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/quorum/begin", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/quorum/submit", permission.IdentityGated},
		{"POST", "/v1/tenants/t-1/barrier/rekey/start", permission.TenantBarrierRekey},
		{"POST", "/v1/tenants/t-1/barrier/officers/revoke", permission.TenantBarrierOfficerRevoke},

		// Backups + rebalance.
		{"POST", "/v1/backups", permission.BackupCreate},
		{"GET", "/v1/backups/list", permission.BackupList},
		{"POST", "/v1/backups/restore", permission.BackupRestore},
		{"GET", "/v1/backups/backup-1", permission.BackupRead},
		{"DELETE", "/v1/backups/backup-1", permission.BackupDelete},
		{"POST", "/v1/backups/backup-1/verify", permission.BackupVerify},
		{"POST", "/v1/rebalance/plan", permission.RebalancePlan},
		{"POST", "/v1/rebalance/execute", permission.RebalanceExecute},
		{"GET", "/v1/rebalance/report", permission.RebalanceReport},
		{"GET", "/v1/rebalance/shards/1", permission.RebalanceStats},

		// Databases (config sub-resources are updated with PATCH).
		{"POST", "/v1/databases", permission.DatabasesCreate},
		{"GET", "/v1/databases/list", permission.DatabasesList},
		{"GET", "/v1/databases/mydb", permission.DatabasesRead},
		{"DELETE", "/v1/databases/mydb", permission.DatabasesDelete},
		{"PATCH", "/v1/databases/mydb/barrier", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/mydb/connection", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/mydb/etcd", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/mydb/protocol", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/mydb/raft", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/mydb/scale", permission.DatabasesScale},
		{"POST", "/v1/databases/mydb/seal", permission.DatabasesSeal},
		{"POST", "/v1/databases/mydb/unseal", permission.DatabasesUnseal},
		{"GET", "/v1/databases/mydb/barrier/status", permission.DatabasesBarrierStatus},
		// Shamir custody of a database barrier.
		{"POST", "/v1/databases/mydb/barrier/officers", permission.DatabasesOfficerRegister},
		{"GET", "/v1/databases/mydb/barrier/officers", permission.DatabasesOfficerList},
		{"POST", "/v1/databases/mydb/barrier/shamir/initialize", permission.DatabasesInitializeShamir},
		{"POST", "/v1/databases/mydb/barrier/officers/alice/claim/begin", permission.IdentityGated},
		{"POST", "/v1/databases/mydb/barrier/officers/alice/claim/complete", permission.IdentityGated},
		{"POST", "/v1/databases/mydb/barrier/quorum/begin", permission.IdentityGated},
		{"POST", "/v1/databases/mydb/barrier/quorum/submit", permission.IdentityGated},
		{"POST", "/v1/databases/mydb/barrier/rekey/start", permission.DatabasesRekey},
		{"POST", "/v1/databases/mydb/barrier/officers/revoke", permission.DatabasesOfficerRevoke},
		{"POST", "/v1/databases/mydb/barrier/officers/unenrol", permission.DatabasesOfficerUnenrol},

		// Collections + documents.
		{"POST", "/v1/collections", permission.CollectionsCreate},
		{"GET", "/v1/collections/list", permission.CollectionsList},
		{"GET", "/v1/collections/mydb/mycoll", permission.CollectionsRead},
		{"DELETE", "/v1/collections/mydb/mycoll", permission.CollectionsDrop},
		{"POST", "/v1/collections/mydb/mycoll/documents", permission.CollectionsInsert},
		{"GET", "/v1/collections/mydb/mycoll/documents", permission.CollectionsList},
		{"GET", "/v1/collections/mydb/mycoll/documents/42", permission.CollectionsRead},
		{"PUT", "/v1/collections/mydb/mycoll/documents/42", permission.CollectionsUpdate},
		{"DELETE", "/v1/collections/mydb/mycoll/documents/42", permission.CollectionsDelete},

		// Barrier lifecycle + Security Officer surface.
		{"POST", "/v1/barrier/initialize", permission.SealBootstrap},
		{"POST", "/v1/barrier/unseal", permission.SealUnseal},
		{"POST", "/v1/barrier/seal", permission.SealSeal},
		{"GET", "/v1/barrier/status", permission.SealStatus},
		{"POST", "/v1/barrier/shamir/initialize", permission.SealInitializeShamir},
		{"POST", "/v1/barrier/enable-encryption", permission.SealEnableEncryption},
		{"POST", "/v1/barrier/recovery/generate", permission.SealGenerateRecovery},
		{"DELETE", "/v1/barrier/recovery", permission.SealGenerateRecovery},
		{"GET", "/v1/barrier/recovery/status", permission.SealStatus},
		{"GET", "/v1/barrier/officers", permission.SealOfficerList},
		{"POST", "/v1/barrier/officers", permission.SealOfficerRegister},
		{"POST", "/v1/barrier/recovery/officers", permission.SealOfficerRegister},
		// A share holder's claim and quorum steps are identity-gated: the
		// holder's certificate and factor proof are the credential. Starting a
		// rekey and revoking a holder are RBAC operations (plus the SO PIN).
		{"POST", "/v1/barrier/officers/alice/claim/begin", permission.IdentityGated},
		{"POST", "/v1/barrier/officers/alice/claim/complete", permission.IdentityGated},
		{"POST", "/v1/barrier/recovery/officers/alice/claim/begin", permission.IdentityGated},
		{"POST", "/v1/barrier/recovery/officers/alice/claim/complete", permission.IdentityGated},
		{"POST", "/v1/barrier/quorum/begin", permission.IdentityGated},
		{"POST", "/v1/barrier/quorum/submit", permission.IdentityGated},
		{"POST", "/v1/barrier/rekey/start", permission.SealRekey},
		{"POST", "/v1/barrier/officers/revoke", permission.SealOfficerRevoke},

		// Ceremony.
		{"POST", "/v1/ceremony/initialize", permission.CeremonyInitialize},
		{"GET", "/v1/ceremony/status", permission.CeremonyStatus},
		{"POST", "/v1/ceremony/sign-csr", permission.CeremonySignCSR},

		// Credentials, including the per-credential family.
		{"POST", "/v1/credentials/submit", permission.CredentialsSubmit},
		{"POST", "/v1/credentials/store", permission.CredentialsStore},
		{"GET", "/v1/credentials/strategy", permission.CredentialsStrategy},
		{"GET", "/v1/credentials", permission.CredentialsList},
		{"DELETE", "/v1/credentials/dbpass", permission.CredentialsDelete},
		{"POST", "/v1/credentials/dbpass/rotate", permission.CredentialsRotate},
		{"POST", "/v1/credentials/dbpass/gc", permission.CredentialsGCExpired},
		{"GET", "/v1/credentials/dbpass/versions", permission.CredentialsListVersions},
		{"GET", "/v1/credentials/dbpass/versions/3", permission.CredentialsRetrieveVersion},
		{"GET", "/v1/credentials/dbpass/value", permission.CredentialsRetrieve},

		// Audit.
		{"GET", "/v1/audit/events", permission.AuditQuery},
		{"GET", "/v1/audit/replicated/verify", permission.AuditVerifyReplicated},

		// Auth factors (factor-type lifecycle + per-user model).
		{"GET", "/v1/authfactors/list", permission.AuthFactorList},
		{"POST", "/v1/authfactors/totp/enroll", permission.AuthFactorEnroll},
		{"POST", "/v1/authfactors/enr-1/challenge", permission.AuthFactorChallenge},
		{"POST", "/v1/authfactors/enr-1/verify", permission.AuthFactorVerify},
		{"DELETE", "/v1/authfactors/enr-1", permission.AuthFactorRevoke},
		{"POST", "/v1/users/factors", permission.UserAuthFactorsAdd},
		{"GET", "/v1/users/factors/list", permission.UserAuthFactorsList},
		{"POST", "/v1/users/factors/login", permission.UserAuthFactorsLogin},
		{"POST", "/v1/users/factors/rotate", permission.UserAuthFactorsRotate},
		{"POST", "/v1/users/factors/delete", permission.UserAuthFactorsAdd},
		{"GET", "/v1/users/factors/factor-1", permission.UserAuthFactorsGet},
		{"DELETE", "/v1/users/factors/factor-1", permission.UserAuthFactorsRemove},
		{"PUT", "/v1/users/factors/factor-1/label", permission.UserAuthFactorsRelabel},

		// Tenant DEK keyring.
		{"GET", "/v1/dek/status", permission.DEKStatus},
		{"POST", "/v1/dek/rotate", permission.DEKRotate},
		{"POST", "/v1/dek/retire", permission.DEKRetire},
		{"POST", "/v1/dek/encrypt", permission.DEKEncrypt},
		{"POST", "/v1/dek/decrypt", permission.DEKDecrypt},
		{"POST", "/v1/dek/rewrap", permission.DEKRewrap},
	}

	for _, tc := range cases {
		got, kind, ok := srv.routePerms.Lookup(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s: expected a permission mapping, got none (route would be unauthorized)", tc.method, tc.path)
			continue
		}
		if kind != authmw.RESTRoutePermission {
			t.Errorf("%s %s: expected a permission-mapped route, got kind %v", tc.method, tc.path, kind)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %s: got permission %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// TestRouteMappingGuard_ExemptRoutesAreExactlyDeclared cross-checks the
// curated public/authenticated exemptions. Every pre-authentication public
// route must be marked RESTRoutePublic (and mirrored by RESTMiddleware's
// exact-path allowlist); /metrics must be RESTRouteAuthenticated; and no
// other route in the registry may carry either marker.
func TestRouteMappingGuard_ExemptRoutesAreExactlyDeclared(t *testing.T) {
	srv := newFullyWiredGuardServer(t)

	public := map[string]struct{}{
		"GET /health":                  {}, // load balancer / liveness probes reach it without a client cert.
		"GET /v1/ca/bootstrap":         {}, // joining nodes fetch the trust anchor before they hold credentials.
		"GET /v1/config/initialized":   {}, // first-boot setup wizard checks provisioning state pre-credential.
		"POST /v1/cluster/keydelivery": {}, // cluster peer plane: self-authorized by node cert + placement table.
		"GET /v1/cluster/nodekey":      {}, // cluster peer plane: self-authorized by node cert + placement table.
	}
	authenticated := map[string]struct{}{
		"GET /metrics": {}, // Prometheus scrape target: any authenticated peer.
	}

	for key := range public {
		method, path, _ := strings.Cut(key, " ")
		if _, kind, ok := srv.routePerms.Lookup(method, path); !ok || kind != authmw.RESTRoutePublic {
			t.Errorf("%s: expected RESTRoutePublic marker, got ok=%v kind=%v", key, ok, kind)
		}
	}
	for key := range authenticated {
		method, path, _ := strings.Cut(key, " ")
		if _, kind, ok := srv.routePerms.Lookup(method, path); !ok || kind != authmw.RESTRouteAuthenticated {
			t.Errorf("%s: expected RESTRouteAuthenticated marker, got ok=%v kind=%v", key, ok, kind)
		}
	}

	// The other direction: walking the tree, no OTHER route may be public or
	// authenticated-only. A new exemption is a conscious, reviewed edit here.
	err := chi.Walk(srv.router, func(method, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		pattern := routePattern
		if len(pattern) > 1 {
			pattern = strings.TrimSuffix(pattern, "/")
		}
		probe := guardProbePath(routePattern)
		_, kind, ok := srv.routePerms.Lookup(method, probe)
		if !ok {
			return nil // covered by TestRouteMappingGuard_PermissionsRegistered
		}
		key := method + " " + pattern
		switch kind {
		case authmw.RESTRoutePublic:
			if _, want := public[key]; !want {
				t.Errorf("route %s is marked public but is not in the curated public allowlist", key)
			}
		case authmw.RESTRouteAuthenticated:
			if _, want := authenticated[key]; !want {
				t.Errorf("route %s is marked authenticated-only but is not in the curated allowlist", key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}
