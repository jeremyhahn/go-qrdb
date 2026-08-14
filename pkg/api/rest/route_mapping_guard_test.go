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

// route_mapping_guard_test.go is a structural guard against a specific class
// of security bug: a REST route that server.go registers (either as a
// literal entry in s.routes, or dynamically via a prefix/path-param handler
// invoked from dispatch()) but that has NO corresponding entry in
// pkg/auth/middleware/mapping.go's restPermissions / restPathParamPermissions.
// Under RESTMiddleware's DEFAULT-DENY posture (pkg/auth/middleware/rest.go),
// such a route is rejected with 403 for every caller -- exactly what
// happened to POST /v1/ceremony/officer-share/begin|complete before it was
// fixed: the routes were registered in server.go but absent from the
// permission map, so officer Shamir share-claim over REST was unreachable
// for anyone.
//
// The guard has two halves:
//  1. Every route NewServer() places in s.routes (once every optional
//     service is wired via newFullyWiredGuardServer) must resolve through
//     authmw.LookupREST.
//  2. Every dynamically dispatched route -- the ones dispatch() resolves by
//     path-prefix parsing rather than an s.routes lookup -- must also
//     resolve. This table (guardDynamicRoutes) was built by reading each
//     handler's own path-parsing logic directly (handlers_*.go), not by
//     copying pkg/auth/middleware/mapping.go's restPathParamPermissions, so
//     it independently corroborates that map rather than assuming it.
//
// Both halves are skipped for routes in the curated restAuthExemptRoutes
// set -- the reviewed, by-design exceptions to default-deny.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
)

// restAuthExemptRoutes is the curated, reviewed set of "METHOD /path"
// entries that are intentionally reachable WITHOUT resolving to an entry in
// the auth permission map. Every entry mirrors an explicit, named exemption
// in pkg/auth/middleware/rest.go's RESTMiddleware -- either the
// pre-authentication exact-path allowlist (checked before m.authenticate
// runs at all) or the authenticated-but-unprivileged restAuthzPublicPaths
// allowlist. Adding an entry here is a conscious, reviewed edit; it must
// never be used to silently paper over a genuinely missing mapping.
var restAuthExemptRoutes = map[string]struct{}{
	// Pre-authentication exempt (RESTMiddleware matches these exact paths
	// before authentication runs):
	"GET /health":                  {}, // load balancer / liveness probes reach it without a client cert.
	"GET /v1/ca/bootstrap":         {}, // joining nodes fetch the trust anchor before they hold credentials.
	"GET /v1/config/initialized":   {}, // first-boot setup wizard checks provisioning state pre-credential.
	"POST /v1/cluster/keydelivery": {}, // cluster peer plane: self-authorized by node cert + placement table, not RBAC.
	"GET /v1/cluster/nodekey":      {}, // cluster peer plane: self-authorized by node cert + placement table, not RBAC.

	// Authenticated-but-unprivileged (restAuthzPublicPaths in
	// pkg/auth/middleware/rest.go): any authenticated peer may reach it, no
	// specific permission is required.
	"GET /metrics": {}, // Prometheus scrape target.
}

// guardEncMigrator is a minimal no-op service.EncryptionMigrator, wired
// solely so newFullyWiredGuardServer registers POST /v1/barrier/enable-encryption
// (gated on a non-nil EncryptionMigrator; see server.go NewServer).
type guardEncMigrator struct{}

func (guardEncMigrator) EnableEncryption(context.Context, service.BarrierScope, seal.MigrationOptions) error {
	return nil
}

// newFullyWiredGuardServer builds a *Server with every optional service and
// ServerOption wired so every conditional route registration in NewServer
// fires (see server.go's "if s.xxx != nil { s.routes[...] = ... }" blocks).
// It reuses the package's existing test doubles so the guard exercises the
// same construction surface as the rest of the suite.
func newFullyWiredGuardServer(t *testing.T) *Server {
	t.Helper()

	kvSvc := newDefaultKVSvc()
	adminSvc := newDefaultAdminSvc()
	dbSvc := &mockDatabaseService{}

	return NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		// Data-plane services. /kv/range, /kv/compact, /kv/revision, /tx,
		// /batch, /watch, /index/*, /ts/*, and /vector/* register
		// unconditionally in server.go; these are wired anyway for a
		// realistic, fully populated server.
		WithMVCC(&mockMVCCService{}),
		WithTransaction(&mockTransactionService{}),
		WithBatch(&mockBatchService{}),
		WithWatch(&mockWatchService{}),
		WithLease(&mockLeaseService{}), // gates /lease/*
		WithLock(&mockLockService{}),   // gates /lock/*
		WithIndex(&mockIndexService{}),
		WithTimeSeries(&mockTimeSeriesService{}),
		WithVector(&mockVectorService{}),
		WithEmbedding(&mockEmbeddingService{}),            // gates /v1/embeddings*
		WithRAG(&mockRAGService{}),                        // gates /v1/rag/*
		WithDatabaseService(dbSvc),                        // gates /v1/databases*
		WithCollectionService(&mockCollectionService{}),   // gates /v1/collections*
		WithSQLService(sqlquery.NewService(kvSvc, dbSvc)), // gates /sql/query, /v1/sql/query
		WithUserService(&mockUserService{}),               // gates /users, /users/list
		WithRoleService(&mockRoleService{}),               // gates /roles*
		WithTenantService(&mockTenantService{}),
		WithBackupService(&stubBackupService{}),       // gates /v1/backups*
		WithRebalanceService(&stubRebalanceService{}), // gates /v1/rebalance/*
		WithAuthFactorService(&stubAuthFactorSvc{}),
		WithUserAuthFactorService(&mockUserAuthFactorService{}),
		WithCertProvider(&mockCertProvider{}),                            // gates /certs/*
		WithCABootstrap([]byte("guard-ca-cert"), []byte("guard-ca-key")), // gates /v1/ca/bootstrap
		WithClusterKeyReceiver(&fakeClusterKeyReceiver{}),                // gates /v1/cluster/*
		WithBarrierService(newExtBarrierSvc()),                           // gates /v1/barrier/*
		WithEncryptionMigrator(guardEncMigrator{}),                       // gates /v1/barrier/enable-encryption
		WithCeremonyService(newCoverageCeremonyService()),                // gates /v1/ceremony/*
		WithCeremonyCA(&coverageCeremonyCA{}),
		WithCustodianService(&stubCustodianSvc{}),             // gates /v1/ceremony/enroll-custodian etc.
		WithCredentialService(newCoverageCredentialService()), // gates /v1/credentials*
		WithAuditLogger(&stubAuditLogger{}),                   // gates /v1/audit/events
		WithGatherer(prometheus.NewRegistry()),                // gates /metrics
	)
}

// guardRoute is a concrete (method, path) pair the REST server can actually
// dispatch a request to. lookup overrides the path passed to
// authmw.LookupREST when it differs from the literal request path (this
// happens only for /kv/{key}, which the auth middleware's normalizePath
// collapses to "/kv" before consulting the permission map -- see
// pkg/auth/middleware/rest.go).
type guardRoute struct {
	method string
	path   string
	lookup string
}

func (r guardRoute) lookupPath() string {
	if r.lookup != "" {
		return r.lookup
	}
	return r.path
}

func (r guardRoute) key() string {
	return r.method + " " + r.path
}

// guardDynamicRoutes enumerates every REST route dispatch() resolves by
// path-prefix / path-parameter parsing rather than an exact s.routes lookup
// (see the long if-chain in (*Server).dispatch, server.go). Each entry was
// hand-verified against the corresponding handler's own path-parsing logic
// in handlers_*.go -- independently of, not copied from,
// pkg/auth/middleware/mapping.go's restPathParamPermissions -- so this table
// corroborates that map rather than assuming it is already complete.
var guardDynamicRoutes = []guardRoute{
	// /kv/{key}: any /kv/* path other than the literal sub-routes already in
	// s.routes (scan/range/revision/compact) reaches handleKV (handlers.go),
	// which parses the key out of the path itself. RESTMiddleware's
	// normalizePath collapses every /kv/{key} request to "/kv" before the
	// permission lookup, so the lookup path is "/kv" here, not the request
	// path.
	{method: http.MethodGet, path: "/kv/somekey", lookup: "/kv"},
	{method: http.MethodPut, path: "/kv/somekey", lookup: "/kv"},
	{method: http.MethodDelete, path: "/kv/somekey", lookup: "/kv"},

	// /users/{id} and sub-paths (handleUser, handlers_users.go).
	{method: http.MethodGet, path: "/users/abc123"},
	{method: http.MethodPut, path: "/users/abc123"},
	{method: http.MethodDelete, path: "/users/abc123"},
	{method: http.MethodPost, path: "/users/abc123/enable"},
	{method: http.MethodPost, path: "/users/abc123/disable"},
	{method: http.MethodPost, path: "/users/abc123/cert-bindings"},
	{method: http.MethodGet, path: "/users/by-cert-thumbprint/deadbeef"},
	{method: http.MethodGet, path: "/users/by-username/alice"},

	// /roles/{id} (handleRole, handlers_roles.go).
	{method: http.MethodGet, path: "/roles/role-1"},
	{method: http.MethodPut, path: "/roles/role-1"},
	{method: http.MethodDelete, path: "/roles/role-1"},

	// /admin/shards/{id} (handleAdminShardByID, handlers_admin_shards.go).
	{method: http.MethodDelete, path: "/admin/shards/1"},
	{method: http.MethodGet, path: "/admin/shards/1/state"},

	// /ts/series/{metric} (handleTSSeries, handlers_timeseries.go).
	{method: http.MethodDelete, path: "/ts/series/cpu.load"},
	{method: http.MethodGet, path: "/ts/series/cpu.load/retention"},
	{method: http.MethodPut, path: "/ts/series/cpu.load/retention"},

	// /v1/databases/{name} and sub-resources (handleDatabaseByName,
	// handlers_database.go).
	{method: http.MethodGet, path: "/v1/databases/mydb"},
	{method: http.MethodDelete, path: "/v1/databases/mydb"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/barrier"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/connection"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/etcd"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/protocol"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/raft"},
	{method: http.MethodPatch, path: "/v1/databases/mydb/scale"},
	{method: http.MethodPost, path: "/v1/databases/mydb/seal"},
	{method: http.MethodPost, path: "/v1/databases/mydb/unseal"},
	{method: http.MethodGet, path: "/v1/databases/mydb/barrier/status"},

	// /v1/collections/{database}/{name} and document sub-resources
	// (handleCollectionByPath, handlers_collection.go).
	{method: http.MethodGet, path: "/v1/collections/mydb/mycoll"},
	{method: http.MethodDelete, path: "/v1/collections/mydb/mycoll"},
	{method: http.MethodPost, path: "/v1/collections/mydb/mycoll/documents"},
	{method: http.MethodGet, path: "/v1/collections/mydb/mycoll/documents"},
	{method: http.MethodGet, path: "/v1/collections/mydb/mycoll/documents/42"},
	{method: http.MethodPut, path: "/v1/collections/mydb/mycoll/documents/42"},
	{method: http.MethodDelete, path: "/v1/collections/mydb/mycoll/documents/42"},

	// /v1/barrier/officers/{username}/claim/{begin,complete} and the
	// recovery variant (handleBarrierOfficersByPath, handlers_barrier.go).
	// Identity-gated (permission.IdentityGated), not RBAC-gated -- the
	// mapping entry existing at all is what this guard requires; the
	// handler itself enforces the actual mTLS-thumbprint/nonce check. This
	// is the same route SHAPE as the officer-share-claim bug this guard
	// exists to catch.
	{method: http.MethodPost, path: "/v1/barrier/officers/alice/claim/begin"},
	{method: http.MethodPost, path: "/v1/barrier/officers/alice/claim/complete"},
	{method: http.MethodPost, path: "/v1/barrier/recovery/officers/alice/claim/begin"},
	{method: http.MethodPost, path: "/v1/barrier/recovery/officers/alice/claim/complete"},

	// /v1/tenants/{id} and its barrier sub-routes (handleTenantByID,
	// handlers_tenants.go).
	{method: http.MethodGet, path: "/v1/tenants/tenant-1"},
	{method: http.MethodPut, path: "/v1/tenants/tenant-1"},
	{method: http.MethodDelete, path: "/v1/tenants/tenant-1"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/quotas"},
	{method: http.MethodPatch, path: "/v1/tenants/tenant-1/entitlements"},
	{method: http.MethodGet, path: "/v1/tenants/tenant-1/barrier/status"},
	{method: http.MethodGet, path: "/v1/tenants/tenant-1/barrier/officers"},
	{method: http.MethodGet, path: "/v1/tenants/tenant-1/barrier/officers/claim-status"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/init"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/unseal"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/unseal-share"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/seal"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/seal-vote"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/officers"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/officers/alice/claim/begin"},
	{method: http.MethodPost, path: "/v1/tenants/tenant-1/barrier/officers/alice/claim/complete"},

	// /v1/backups/{id} and sub-resources (handleBackupByID, handlers_backup.go).
	{method: http.MethodGet, path: "/v1/backups/backup-1"},
	{method: http.MethodDelete, path: "/v1/backups/backup-1"},
	{method: http.MethodPost, path: "/v1/backups/backup-1/verify"},

	// /v1/rebalance/shards/{id} (handleRebalanceShardStats, handlers_rebalance.go).
	{method: http.MethodGet, path: "/v1/rebalance/shards/1"},

	// /v1/credentials/{name} and sub-resources (handleCredentialByPath,
	// handlers_credentials.go).
	{method: http.MethodDelete, path: "/v1/credentials/dbpass"},
	{method: http.MethodPost, path: "/v1/credentials/dbpass/rotate"},
	{method: http.MethodPost, path: "/v1/credentials/dbpass/gc"},
	{method: http.MethodGet, path: "/v1/credentials/dbpass/versions"},
	{method: http.MethodGet, path: "/v1/credentials/dbpass/versions/3"},
	{method: http.MethodGet, path: "/v1/credentials/dbpass/value"},

	// /v1/authfactors/{factor|id}/... (handleAuthFactorByPath, handlers_authfactor.go).
	{method: http.MethodPost, path: "/v1/authfactors/totp/enroll"},
	{method: http.MethodPost, path: "/v1/authfactors/enr-1/challenge"},
	{method: http.MethodPost, path: "/v1/authfactors/enr-1/verify"},
	{method: http.MethodDelete, path: "/v1/authfactors/enr-1"},

	// /v1/users/factors/{id} and sub-resources (handleUserAuthFactorByPath,
	// handlers_user_auth_factor.go). The literal keyword routes "list",
	// "login", and "rotate" are handled via s.routes, not this prefix, and
	// are excluded here (they're covered by the s.routes half of the guard).
	{method: http.MethodGet, path: "/v1/users/factors/factor-1"},
	{method: http.MethodDelete, path: "/v1/users/factors/factor-1"},
	{method: http.MethodPut, path: "/v1/users/factors/factor-1/label"},
}

// TestRouteMappingGuard_AllRegisteredRoutesAreMapped is the structural guard.
// It builds a fully wired Server (every optional service present, so every
// conditional route registration in NewServer fires) and asserts that every
// route it can actually dispatch a request to -- whether a literal s.routes
// entry or a dynamically dispatched path-param route -- resolves to a
// permission via authmw.LookupREST, unless the route is in the curated
// restAuthExemptRoutes allowlist.
//
// A route landing here unmapped is precisely the officer-share-claim class
// of bug: registered in server.go, reachable, but default-denied for every
// caller because RESTMiddleware found no permission mapping.
func TestRouteMappingGuard_AllRegisteredRoutesAreMapped(t *testing.T) {
	srv := newFullyWiredGuardServer(t)

	checked := 0
	skippedExempt := 0

	// Half 1: every literal entry in s.routes (white-box access; this file
	// is package rest).
	for path, rt := range srv.routes {
		key := rt.method + " " + path
		if _, exempt := restAuthExemptRoutes[key]; exempt {
			skippedExempt++
			continue
		}
		checked++
		if _, ok := authmw.LookupREST(rt.method, path); !ok {
			t.Errorf("route registered but NOT in the auth permission map (default-deny will 403 every caller): %s", key)
		}
	}

	// Half 2: every dynamically dispatched (path-param/prefix) route.
	for _, dr := range guardDynamicRoutes {
		key := dr.key()
		if _, exempt := restAuthExemptRoutes[key]; exempt {
			skippedExempt++
			continue
		}
		checked++
		if _, ok := authmw.LookupREST(dr.method, dr.lookupPath()); !ok {
			t.Errorf("dynamically dispatched route registered but NOT in the auth permission map (default-deny will 403 every caller): %s (lookup path %q)", key, dr.lookupPath())
		}
	}

	if checked == 0 {
		t.Fatal("guard checked zero routes -- newFullyWiredGuardServer likely regressed and stopped registering routes")
	}
	t.Logf("route-mapping guard: checked %d routes (%d literal s.routes + %d dynamic), skipped %d exempt",
		checked, len(srv.routes), len(guardDynamicRoutes), skippedExempt)
}

// TestRouteMappingGuard_ExemptRoutesAreActuallyExempt cross-checks the other
// direction for the curated allowlist itself: every entry in
// restAuthExemptRoutes must genuinely have no permission mapping (otherwise
// it isn't an exemption at all, just an entry someone forgot to remove after
// closing a gap -- e.g. if /metrics ever gained a specific permission, the
// exempt entry would silently become a downgrade from "requires
// metrics:read" to "any authenticated caller").
func TestRouteMappingGuard_ExemptRoutesAreActuallyExempt(t *testing.T) {
	for key := range restAuthExemptRoutes {
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Fatalf("malformed restAuthExemptRoutes key %q: expected \"METHOD /path\"", key)
		}
		if perm, mapped := authmw.LookupREST(method, path); mapped {
			t.Errorf("restAuthExemptRoutes entry %q actually resolves to permission %q -- it is not exempt; remove it from the allowlist", key, perm)
		}
	}
}
