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

package middleware

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1" // registers qrdb.v1 services
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// TestGRPCPermissions_EveryMethodMapped is the default-deny regression guard:
// EVERY RPC on the generated QRDBService descriptor must have a permission
// mapping. A method without one is denied at runtime (default-deny), so an
// unmapped method is a bug — either the mapping was forgotten (the method
// becomes unreachable) or, before default-deny, it was silently public. This
// test fails the build if a new RPC is added without a corresponding entry in
// grpcPermissions.
func TestGRPCPermissions_EveryMethodMapped(t *testing.T) {
	// Enumerate EVERY qrdb.v1 service in the global proto registry (the
	// qrdbv1 import registers them all), so a newly generated service can
	// never silently escape the guard — which is exactly how the
	// TenantBarrierService/UnsealShare + AuthFactorService +
	// UserAuthFactorService gap shipped when only QRDBService_ServiceDesc
	// was enumerated here.
	var services int
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "qrdb.v1" {
			return true
		}
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			services++
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				full := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
				if _, ok := LookupGRPC(full); !ok {
					t.Errorf("gRPC method %q has NO permission mapping (unauthorized/denied at runtime); add it to grpcPermissions", full)
				}
			}
		}
		return true
	})
	// Guard the guard: all five qrdb.v1 services (QRDB, TenantBarrier,
	// Audit, AuthFactor, UserAuthFactor) must have been seen.
	if services < 5 {
		t.Fatalf("expected at least 5 qrdb.v1 services in the proto registry, saw %d", services)
	}
}

// ---------------------------------------------------------------------------
// LookupGRPC tests
// ---------------------------------------------------------------------------

func TestLookupGRPC_Found(t *testing.T) {
	tests := []struct {
		method string
		want   permission.Permission
	}{
		{"/qrdb.v1.QRDBService/Put", permission.KVPut},
		{"/qrdb.v1.QRDBService/Get", permission.KVGet},
		{"/qrdb.v1.QRDBService/Delete", permission.KVDelete},
		{"/qrdb.v1.QRDBService/List", permission.KVList},
		{"/qrdb.v1.QRDBService/Scan", permission.KVRange},
		{"/qrdb.v1.QRDBService/PutWithLease", permission.MVCCPutWithLease},
		{"/qrdb.v1.QRDBService/GetRevision", permission.MVCCGetRevision},
		{"/qrdb.v1.QRDBService/GetAtRevision", permission.MVCCGetAtRevision},
		{"/qrdb.v1.QRDBService/Range", permission.MVCCRange},
		{"/qrdb.v1.QRDBService/Compact", permission.MVCCCompact},
		{"/qrdb.v1.QRDBService/CurrentRevision", permission.MVCCCurrentRevision},
		{"/qrdb.v1.QRDBService/Transaction", permission.TransactionExecute},
		{"/qrdb.v1.QRDBService/Batch", permission.BatchExecute},
		{"/qrdb.v1.QRDBService/Watch", permission.WatchWatch},
		{"/qrdb.v1.QRDBService/LeaseGrant", permission.LeaseGrant},
		{"/qrdb.v1.QRDBService/LeaseRevoke", permission.LeaseRevoke},
		{"/qrdb.v1.QRDBService/LeaseKeepAlive", permission.LeaseKeepAlive},
		{"/qrdb.v1.QRDBService/LeaseTimeToLive", permission.LeaseTTL},
		{"/qrdb.v1.QRDBService/LeaseList", permission.LeaseList},
		{"/qrdb.v1.QRDBService/IndexCreate", permission.IndexCreate},
		{"/qrdb.v1.QRDBService/IndexDrop", permission.IndexDrop},
		{"/qrdb.v1.QRDBService/IndexQuery", permission.IndexQuery},
		{"/qrdb.v1.QRDBService/IndexScan", permission.IndexScan},
		{"/qrdb.v1.QRDBService/TSWrite", permission.TSWrite},
		{"/qrdb.v1.QRDBService/TSWriteBatch", permission.TSWriteBatch},
		{"/qrdb.v1.QRDBService/TSQuery", permission.TSQuery},
		{"/qrdb.v1.QRDBService/TSAggregate", permission.TSAggregate},
		{"/qrdb.v1.QRDBService/VectorCreateNamespace", permission.VectorNamespaceCreate},
		{"/qrdb.v1.QRDBService/VectorDeleteNamespace", permission.VectorNamespaceDelete},
		{"/qrdb.v1.QRDBService/VectorListNamespaces", permission.VectorNamespaceList},
		{"/qrdb.v1.QRDBService/VectorGetNamespaceInfo", permission.VectorNamespaceInfo},
		{"/qrdb.v1.QRDBService/VectorUpsert", permission.VectorUpsert},
		{"/qrdb.v1.QRDBService/VectorGet", permission.VectorGet},
		{"/qrdb.v1.QRDBService/VectorDelete", permission.VectorDelete},
		{"/qrdb.v1.QRDBService/VectorBatchUpsert", permission.VectorBatchUpsert},
		{"/qrdb.v1.QRDBService/VectorBatchDelete", permission.VectorBatchDelete},
		{"/qrdb.v1.QRDBService/VectorList", permission.VectorList},
		{"/qrdb.v1.QRDBService/VectorUpdateMetadata", permission.VectorUpdateMetadata},
		{"/qrdb.v1.QRDBService/VectorSearchSimilar", permission.VectorSearchSimilar},
		{"/qrdb.v1.QRDBService/VectorSearchByMetadata", permission.VectorSearchByMetadata},
		{"/qrdb.v1.QRDBService/VectorHybridSearch", permission.VectorHybridSearch},
		{"/qrdb.v1.QRDBService/VectorSearchByID", permission.VectorSearchByID},
		{"/qrdb.v1.QRDBService/VectorRebuildIndex", permission.VectorRebuildIndex},
		{"/qrdb.v1.QRDBService/VectorGetRebuildProgress", permission.VectorGetRebuildProg},
		{"/qrdb.v1.QRDBService/VectorGetIndexStats", permission.VectorGetIndexStats},
		{"/qrdb.v1.QRDBService/VectorCompactNamespace", permission.VectorCompact},
		{"/qrdb.v1.QRDBService/VectorGetCompactionProgress", permission.VectorGetCompactProg},
		{"/qrdb.v1.QRDBService/Health", permission.AdminHealth},
		{"/qrdb.v1.QRDBService/Status", permission.AdminStatus},
		{"/qrdb.v1.QRDBService/Peers", permission.AdminPeers},
		{"/qrdb.v1.QRDBService/AddMember", permission.AdminAddMember},
		{"/qrdb.v1.QRDBService/RemoveMember", permission.AdminRemoveMember},
		{"/qrdb.v1.QRDBService/RequestSnapshot", permission.AdminSnapshot},
		{"/qrdb.v1.QRDBService/Drain", permission.AdminDrain},
		{"/qrdb.v1.QRDBService/Capabilities", permission.AdminCapabilities},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := LookupGRPC(tt.method)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.method)
			}
			if got != tt.want {
				t.Errorf("LookupGRPC(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestLookupGRPC_NotFound(t *testing.T) {
	methods := []string{
		"/qrdb.v1.QRDBService/UnknownMethod",
		"/other.Service/Put",
		"",
		"/",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, ok := LookupGRPC(method)
			if ok {
				t.Errorf("expected no mapping for %q", method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LookupREST tests
// ---------------------------------------------------------------------------

func TestLookupREST_Found(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   permission.Permission
	}{
		{"PUT", "/kv", permission.KVPut},
		{"GET", "/kv", permission.KVGet},
		{"DELETE", "/kv", permission.KVDelete},
		{"GET", "/kv/scan", permission.KVList},
		{"GET", "/kv/range", permission.KVRange},
		{"PUT", "/kv/lease", permission.MVCCPutWithLease},
		{"GET", "/kv/revision", permission.MVCCCurrentRevision},
		{"POST", "/kv/compact", permission.MVCCCompact},
		{"POST", "/tx", permission.TransactionExecute},
		{"POST", "/batch", permission.BatchExecute},
		{"GET", "/watch", permission.WatchWatch},
		{"POST", "/lease/grant", permission.LeaseGrant},
		{"POST", "/lease/revoke", permission.LeaseRevoke},
		{"POST", "/lease/keepalive", permission.LeaseKeepAlive},
		{"GET", "/lease/ttl", permission.LeaseTTL},
		{"GET", "/lease/list", permission.LeaseList},
		{"POST", "/index/create", permission.IndexCreate},
		{"POST", "/index/drop", permission.IndexDrop},
		{"GET", "/index/query", permission.IndexQuery},
		{"GET", "/index/scan", permission.IndexScan},
		{"POST", "/ts/write", permission.TSWrite},
		{"POST", "/ts/write-batch", permission.TSWriteBatch},
		{"POST", "/ts/write-point", permission.TSWritePoint},
		{"GET", "/ts/query", permission.TSQuery},
		{"POST", "/ts/query-rich", permission.TSRichQuery},
		{"GET", "/ts/aggregate", permission.TSAggregate},
		// /health is public (exempt from auth in RESTMiddleware).
		{"GET", "/status", permission.AdminStatus},
		{"GET", "/peers", permission.AdminPeers},
		{"POST", "/membership/add", permission.AdminAddMember},
		{"POST", "/membership/remove", permission.AdminRemoveMember},
		{"POST", "/snapshot", permission.AdminSnapshot},
		{"POST", "/drain", permission.AdminDrain},
		{"GET", "/capabilities", permission.AdminCapabilities},
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
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got, ok := LookupREST(tt.method, tt.path)
			if !ok {
				t.Fatalf("expected mapping for %s %s", tt.method, tt.path)
			}
			if got != tt.want {
				t.Errorf("LookupREST(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestLookupREST_NotFound(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/unknown"},
		{"POST", "/kv"},
		{"DELETE", "/health"},
		{"GET", "/admin"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			_, ok := LookupREST(tt.method, tt.path)
			if ok {
				t.Errorf("expected no mapping for %s %s", tt.method, tt.path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LookupQUIC tests
// ---------------------------------------------------------------------------

func TestLookupQUIC_Found(t *testing.T) {
	tests := []struct {
		method string
		want   permission.Permission
	}{
		{"kv.Put", permission.KVPut},
		{"kv.Get", permission.KVGet},
		{"kv.Delete", permission.KVDelete},
		{"kv.List", permission.KVList},
		{"kv.Scan", permission.KVRange},
		{"mvcc.PutWithLease", permission.MVCCPutWithLease},
		{"mvcc.GetRevision", permission.MVCCGetRevision},
		{"mvcc.GetAtRevision", permission.MVCCGetAtRevision},
		{"mvcc.Range", permission.MVCCRange},
		{"mvcc.Compact", permission.MVCCCompact},
		{"mvcc.CurrentRevision", permission.MVCCCurrentRevision},
		{"tx.Transaction", permission.TransactionExecute},
		{"batch.Batch", permission.BatchExecute},
		{"watch.Watch", permission.WatchWatch},
		{"watch.WatchPrefix", permission.WatchWatch},
		{"lease.Grant", permission.LeaseGrant},
		{"lease.Revoke", permission.LeaseRevoke},
		{"lease.KeepAlive", permission.LeaseKeepAlive},
		{"lease.TimeToLive", permission.LeaseTTL},
		{"lease.List", permission.LeaseList},
		{"index.Create", permission.IndexCreate},
		{"index.Drop", permission.IndexDrop},
		{"index.Query", permission.IndexQuery},
		{"index.Scan", permission.IndexScan},
		{"ts.Write", permission.TSWrite},
		{"ts.WriteBatch", permission.TSWriteBatch},
		{"ts.Query", permission.TSQuery},
		{"ts.Aggregate", permission.TSAggregate},
		{"vector.CreateNamespace", permission.VectorNamespaceCreate},
		{"vector.DeleteNamespace", permission.VectorNamespaceDelete},
		{"vector.ListNamespaces", permission.VectorNamespaceList},
		{"vector.GetNamespaceInfo", permission.VectorNamespaceInfo},
		{"vector.Upsert", permission.VectorUpsert},
		{"vector.Get", permission.VectorGet},
		{"vector.Delete", permission.VectorDelete},
		{"vector.BatchUpsert", permission.VectorBatchUpsert},
		{"vector.BatchDelete", permission.VectorBatchDelete},
		{"vector.List", permission.VectorList},
		{"vector.UpdateMetadata", permission.VectorUpdateMetadata},
		{"vector.SearchSimilar", permission.VectorSearchSimilar},
		{"vector.SearchByMetadata", permission.VectorSearchByMetadata},
		{"vector.HybridSearch", permission.VectorHybridSearch},
		{"vector.SearchByID", permission.VectorSearchByID},
		{"vector.RebuildIndex", permission.VectorRebuildIndex},
		{"vector.GetRebuildProgress", permission.VectorGetRebuildProg},
		{"vector.GetIndexStats", permission.VectorGetIndexStats},
		{"vector.CompactNamespace", permission.VectorCompact},
		{"vector.GetCompactionProgress", permission.VectorGetCompactProg},
		{"admin.Health", permission.AdminHealth},
		{"admin.Status", permission.AdminStatus},
		{"admin.Peers", permission.AdminPeers},
		{"admin.AddMember", permission.AdminAddMember},
		{"admin.RemoveMember", permission.AdminRemoveMember},
		{"admin.RequestSnapshot", permission.AdminSnapshot},
		{"admin.Drain", permission.AdminDrain},
		{"admin.Capabilities", permission.AdminCapabilities},
		{"user.Create", permission.UsersCreate},
		{"user.Get", permission.UsersRead},
		{"user.Update", permission.UsersUpdate},
		{"user.Delete", permission.UsersDelete},
		{"user.List", permission.UsersList},
		{"user.Enable", permission.UsersEnable},
		{"user.Disable", permission.UsersDisable},
		{"role.Create", permission.RolesCreate},
		{"role.Get", permission.RolesRead},
		{"role.Update", permission.RolesUpdate},
		{"role.Delete", permission.RolesDelete},
		{"role.List", permission.RolesList},
		{"role.Assign", permission.RolesAssign},
		{"role.Unassign", permission.RolesUnassign},
		{"cert.Generate", permission.CertsGenerate},
		{"cert.Revoke", permission.CertsRevoke},
		{"cert.List", permission.CertsList},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := LookupQUIC(tt.method)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.method)
			}
			if got != tt.want {
				t.Errorf("LookupQUIC(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestLookupQUIC_NotFound(t *testing.T) {
	methods := []string{
		"unknown.Method",
		"kv.Unknown",
		"",
		".",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, ok := LookupQUIC(method)
			if ok {
				t.Errorf("expected no mapping for %q", method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Map integrity tests
// ---------------------------------------------------------------------------

// TestGRPCPermissions_PermissionsAreRegistered verifies that every permission
// referenced by a gRPC method exists in the global registry. Multiple gRPC
// methods CAN legitimately share one permission (e.g. the five
// DatabaseUpdate* RPCs all require databases:update, and GetDocument /
// GetCollectionInfo both require collections:read), so a uniqueness invariant
// no longer holds — registration validity is the meaningful check.
func TestGRPCPermissions_PermissionsAreRegistered(t *testing.T) {
	for method, perm := range grpcPermissions {
		// permission.IdentityGated is a deliberately unregistered sentinel:
		// claim routes are authorized at the handler layer, and the sentinel
		// must never be grantable to a role.
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("gRPC method %q maps to unregistered permission %q", method, perm)
		}
	}
}

// TestRESTPermissions_PermissionsAreRegistered verifies that every permission
// referenced by a REST route exists in the global registry. Multiple REST
// routes CAN legitimately share the same permission (e.g., begin and complete
// ceremony steps both require ceremony:claim_cert), so no duplicate-value check
// is applied here.
func TestRESTPermissions_PermissionsAreRegistered(t *testing.T) {
	for route, perm := range restPermissions {
		// Skip the deliberately unregistered identity-gated sentinel.
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("REST route %q maps to unregistered permission %q", route, perm)
		}
	}
}

// pendingGRPCPermissions is the set of permissions added in Phase 2 whose
// gRPC proto stubs have not yet been generated. These are tracked here so
// that TestMappingConsistency_RESTPermissionsHaveGRPCCounterparts can skip
// them rather than fail — parallel agents will add the proto methods and
// remove these entries.
var pendingGRPCPermissions = map[permission.Permission]struct{}{
	permission.SealBootstrap:           {}, // shared: barrier/initialize + root-token
	permission.SealUnseal:              {},
	permission.SealSeal:                {},
	permission.SealStatus:              {}, // shared: status + recovery/status + shamir/verify
	permission.SealInitializeShamir:    {},
	permission.BarrierUnsealShare:      {}, // shared: unseal-share + unseal-shares
	permission.SealRekey:               {},
	permission.SealGenerateRecovery:    {}, // shared: recovery/generate + DELETE recovery
	permission.SealRecoverWithKeys:     {},
	permission.CeremonyInitialize:      {},
	permission.CeremonyStatus:          {},
	permission.CeremonyClaimCert:       {}, // shared: claim-cert/begin + claim-cert/complete
	permission.CeremonySignCSR:         {},
	permission.CeremonyEnrollCustodian: {}, // Phase 4: gRPC proto stubs pending
	permission.CeremonyRevokeCustodian: {}, // Phase 4: gRPC proto stubs pending
	permission.AuditQuery:              {},
	permission.AuthFactorEnroll:        {},
	permission.AuthFactorChallenge:     {},
	permission.AuthFactorVerify:        {},
	permission.AuthFactorList:          {},
	permission.AuthFactorRevoke:        {},

	// Transport-specific by design (no QRDBService gRPC method exists for
	// these subsystems): the embedding/RAG HTTP API and the per-user
	// auth-factor lifecycle are exposed on REST/MCP (and QUIC for user
	// factors), not gRPC.
	permission.EmbeddingEmbed:         {},
	permission.EmbeddingListModels:    {},
	permission.RAGIngest:              {},
	permission.RAGQuery:               {},
	permission.RAGListDocuments:       {},
	permission.RAGDeleteDocument:      {},
	permission.UserAuthFactorsAdd:     {},
	permission.UserAuthFactorsGet:     {},
	permission.UserAuthFactorsList:    {},
	permission.UserAuthFactorsRemove:  {},
	permission.UserAuthFactorsRotate:  {},
	permission.UserAuthFactorsRelabel: {},
	permission.UserAuthFactorsLogin:   {},
}

func TestMappingConsistency_RESTPermissionsHaveGRPCCounterparts(t *testing.T) {
	// Build a set of all gRPC permissions.
	grpcPerms := make(map[permission.Permission]struct{}, len(grpcPermissions))
	for _, perm := range grpcPermissions {
		grpcPerms[perm] = struct{}{}
	}

	// Every REST permission should have a corresponding gRPC permission,
	// except for those in pendingGRPCPermissions which are awaiting proto stubs.
	for route, perm := range restPermissions {
		if _, pending := pendingGRPCPermissions[perm]; pending {
			continue
		}
		if _, ok := grpcPerms[perm]; !ok {
			t.Errorf("REST route %q maps to %q which has no gRPC counterpart", route, perm)
		}
	}
}

func TestGRPCPermissions_AllKeysUseCorrectPrefix(t *testing.T) {
	// Keys belong to one of the canonical generated qrdb.v1 gRPC services.
	prefixes := []string{
		"/qrdb.v1.QRDBService/",
		"/qrdb.v1.TenantBarrierService/",
		"/qrdb.v1.AuditService/",
		"/qrdb.v1.AuthFactorService/",
		"/qrdb.v1.UserAuthFactorService/",
	}
	for method := range grpcPermissions {
		matched := false
		for _, prefix := range prefixes {
			if len(method) > len(prefix) && method[:len(prefix)] == prefix {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("gRPC method key %q does not start with one of %v", method, prefixes)
		}
	}
}

func TestGRPCPermissions_AllPermissionsRegistered(t *testing.T) {
	for method, perm := range grpcPermissions {
		// Skip the deliberately unregistered identity-gated sentinel.
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("gRPC %q maps to unregistered permission %q", method, perm)
		}
	}
}

func TestQUICPermissions_AllPermissionsRegistered(t *testing.T) {
	for method, perm := range quicPermissions {
		// Skip the deliberately unregistered identity-gated sentinel.
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("QUIC %q maps to unregistered permission %q", method, perm)
		}
	}
}

func TestQUICPermissions_AllKeysUseDotNotation(t *testing.T) {
	for method := range quicPermissions {
		found := false
		for i := range method {
			if method[i] == '.' {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("QUIC method key %q does not use dot notation", method)
		}
	}
}

func TestMappingConsistency_QUICPermissionsHaveGRPCCounterparts(t *testing.T) {
	grpcPerms := make(map[permission.Permission]struct{}, len(grpcPermissions))
	for _, perm := range grpcPermissions {
		grpcPerms[perm] = struct{}{}
	}

	// QUIC-only permissions that have no gRPC proto counterpart.
	quicOnly := map[permission.Permission]struct{}{
		// IAM permissions (users, roles, certs) are QUIC-only for now.
		permission.UsersCreate:   {},
		permission.UsersRead:     {},
		permission.UsersUpdate:   {},
		permission.UsersDelete:   {},
		permission.UsersList:     {},
		permission.UsersEnable:   {},
		permission.UsersDisable:  {},
		permission.RolesCreate:   {},
		permission.RolesRead:     {},
		permission.RolesUpdate:   {},
		permission.RolesDelete:   {},
		permission.RolesList:     {},
		permission.RolesAssign:   {},
		permission.RolesUnassign: {},
		permission.CertsGenerate: {},
		permission.CertsRevoke:   {},
		permission.CertsList:     {},
		// Rich TS operations use JSON encoding (no proto messages).
		permission.TSWritePoint:   {},
		permission.TSRichQuery:    {},
		permission.TSDeleteSeries: {},
		permission.TSGetRetention: {},
		permission.TSSetRetention: {},
		permission.TSListMetrics:  {},
		// Phase 2 compliance permissions — gRPC proto stubs pending.
		// These appear in both REST and QUIC but not yet in gRPC.
		permission.SealBootstrap:        {},
		permission.SealUnseal:           {},
		permission.SealSeal:             {},
		permission.SealStatus:           {},
		permission.SealInitializeShamir: {},
		permission.BarrierUnsealShare:   {},
		permission.SealRekey:            {},
		permission.SealGenerateRecovery: {},
		permission.SealRecoverWithKeys:  {},
		permission.CeremonyInitialize:   {},
		permission.CeremonyStatus:       {},
		permission.CeremonyClaimCert:    {},
		permission.CeremonySignCSR:      {},
		permission.AuditQuery:           {},
		permission.AuthFactorEnroll:     {},
		permission.AuthFactorChallenge:  {},
		permission.AuthFactorVerify:     {},
		permission.AuthFactorList:       {},
		permission.AuthFactorRevoke:     {},
		// Per-user auth-factor lifecycle: QUIC/REST/MCP-only (no gRPC method).
		permission.UserAuthFactorsAdd:     {},
		permission.UserAuthFactorsGet:     {},
		permission.UserAuthFactorsList:    {},
		permission.UserAuthFactorsRemove:  {},
		permission.UserAuthFactorsRotate:  {},
		permission.UserAuthFactorsRelabel: {},
		permission.UserAuthFactorsLogin:   {},
	}

	for method, perm := range quicPermissions {
		if _, ok := grpcPerms[perm]; !ok {
			if _, skip := quicOnly[perm]; skip {
				continue
			}
			t.Errorf("QUIC method %q maps to %q which has no gRPC counterpart", method, perm)
		}
	}
}

// ---------------------------------------------------------------------------
// LookupREST path-parameter pattern matching
//
// These guard the authorization fix for per-resource routes: the REST server
// dispatches path-parameter routes by prefix, and a literal-only lookup left
// them unmapped (treated as public). LookupREST now matches templated routes,
// so /users/{id}, the cert-binding routes, and tenant CRUD resolve to their
// required permission instead of falling through to default-allow.
// ---------------------------------------------------------------------------

func TestLookupREST_PathParamRoutes(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   permission.Permission
	}{
		{"GET", "/users/abc123", permission.UsersRead},
		{"PUT", "/users/abc123", permission.UsersUpdate},
		{"DELETE", "/users/abc123", permission.UsersDelete},
		{"POST", "/users/abc123/enable", permission.UsersEnable},
		{"POST", "/users/abc123/disable", permission.UsersDisable},
		{"POST", "/users/abc123/cert-bindings", permission.UsersAddCertBinding},
		{"GET", "/users/by-cert-thumbprint/AbC-_123", permission.UsersGetByCertThumbprint},
		{"GET", "/users/by-username/admin", permission.UsersRead},
		{"GET", "/roles/r-1", permission.RolesRead},
		{"PUT", "/roles/r-1", permission.RolesUpdate},
		{"DELETE", "/roles/r-1", permission.RolesDelete},
		{"GET", "/v1/tenants/t-1", permission.TenantsRead},
		{"PUT", "/v1/tenants/t-1", permission.TenantsUpdate},
		{"DELETE", "/v1/tenants/t-1", permission.TenantsDelete},
		{"POST", "/v1/tenants/t-1/quotas", permission.TenantsUpdate},
		{"PATCH", "/v1/tenants/t-1/entitlements", permission.TenantsSetEntitlements},
		// Database config sub-resources are updated with PATCH (not POST).
		{"PATCH", "/v1/databases/db1/etcd", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/db1/raft", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/db1/protocol", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/db1/barrier", permission.DatabasesUpdate},
		{"PATCH", "/v1/databases/db1/connection", permission.DatabasesUpdate},
		// Time-series per-metric routes (dispatched via /ts/series/).
		{"DELETE", "/ts/series/m1", permission.TSDeleteSeries},
		{"GET", "/ts/series/m1/retention", permission.TSGetRetention},
		{"PUT", "/ts/series/m1/retention", permission.TSSetRetention},
	}
	for _, tc := range cases {
		got, ok := LookupREST(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s: expected a permission mapping, got none (route would be unauthorized)", tc.method, tc.path)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %s: got permission %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestLookupREST_ExactWinsOverPattern(t *testing.T) {
	// /users/list has an exact mapping and must not be shadowed by the
	// /users/{id} pattern.
	got, ok := LookupREST("GET", "/users/list")
	if !ok || got != permission.UsersList {
		t.Fatalf("GET /users/list: got (%q, %v), want (%q, true)", got, ok, permission.UsersList)
	}
}

func TestLookupREST_UnmappedRemainsPublic(t *testing.T) {
	// A genuinely unmapped path must still report no mapping (public), so the
	// pattern layer does not accidentally capture unrelated routes.
	if _, ok := LookupREST("GET", "/health"); ok {
		t.Error("GET /health must remain unmapped (public)")
	}
	if _, ok := LookupREST("GET", "/users/abc/unknown/deep/path"); ok {
		t.Error("unrelated deep path must not match a templated route")
	}
}

// TestMultiTenantQuotaEntitlementPerms_Registered verifies the four Phase-1
// multi-tenant quota/entitlement permissions are registered in the permission
// catalog so they can be granted to roles and pass grant validation.
func TestMultiTenantQuotaEntitlementPerms_Registered(t *testing.T) {
	perms := []permission.Permission{
		permission.TenantsSetIsolation,
		permission.TenantsSetEntitlements,
		permission.DatabasesCreateRaft,
		permission.DatabasesCreateVolume,
	}
	for _, p := range perms {
		if !permission.IsRegistered(p) {
			t.Errorf("permission %q is not registered in the catalog", p)
		}
	}
}

// TestUpdateEntitlements_MappedAcrossTransports verifies the new
// UpdateEntitlements endpoints map to tenants:set_entitlements on every
// transport (default-deny: an unmapped endpoint is denied at runtime).
func TestUpdateEntitlements_MappedAcrossTransports(t *testing.T) {
	// REST
	if got, ok := LookupREST("PATCH", "/v1/tenants/t-1/entitlements"); !ok || got != permission.TenantsSetEntitlements {
		t.Errorf("REST PATCH entitlements: got %q ok=%v, want %q", got, ok, permission.TenantsSetEntitlements)
	}
	// QUIC
	if got, ok := quicPermissions["tenant.UpdateEntitlements"]; !ok || got != permission.TenantsSetEntitlements {
		t.Errorf("QUIC tenant.UpdateEntitlements: got %q ok=%v, want %q", got, ok, permission.TenantsSetEntitlements)
	}
	// MCP
	if got, ok := mcpPermissions["qrdb_tenant_update_entitlements"]; !ok || got != permission.TenantsSetEntitlements {
		t.Errorf("MCP qrdb_tenant_update_entitlements: got %q ok=%v, want %q", got, ok, permission.TenantsSetEntitlements)
	}
}
