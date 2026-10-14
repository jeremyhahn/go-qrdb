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
// EVERY RPC on the generated qrdb.v1 descriptors must carry a
// (qrdb.v1.permission) method option. A method without one is denied at
// runtime (default-deny), so a missing option is a bug — either the
// annotation was forgotten (the method becomes unreachable) or, before
// default-deny, it was silently public. This test fails the build if a new
// RPC is added to qrdb.proto without a permission option.
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
				if _, ok := LookupGRPCFromDescriptor(full); !ok {
					t.Errorf("gRPC method %q has NO (qrdb.v1.permission) option (unauthorized/denied at runtime); annotate it in qrdb.proto", full)
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
// LookupGRPCFromDescriptor tests
// ---------------------------------------------------------------------------

// grpcDescriptorMethodPermissions enumerates every RPC on every qrdb.v1
// service registered in the global proto registry and resolves its permission
// through LookupGRPCFromDescriptor. It is the test-side reconstruction of the
// full gRPC permission surface (fullMethod -> permission) now that the
// permissions are declared as proto method options rather than a Go table.
func grpcDescriptorMethodPermissions(t *testing.T) map[string]permission.Permission {
	t.Helper()
	perms := make(map[string]permission.Permission)
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "qrdb.v1" {
			return true
		}
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				full := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
				if perm, ok := LookupGRPCFromDescriptor(full); ok {
					perms[full] = perm
				}
			}
		}
		return true
	})
	if len(perms) == 0 {
		t.Fatal("no qrdb.v1 method carries a (qrdb.v1.permission) option; descriptor resolution is broken")
	}
	return perms
}

// TestLookupGRPCFromDescriptor_DefaultDenyRegression is the cutover
// regression guard: an arbitrary unknown method resolves to not-found (and is
// therefore denied by the interceptors), while a known annotated method
// resolves to exactly the permission the deleted grpcPermissions table
// carried, and the identity-gated sentinel string maps back to
// permission.IdentityGated so authorize()'s handler-layer short-circuit
// still fires.
func TestLookupGRPCFromDescriptor_DefaultDenyRegression(t *testing.T) {
	if _, ok := LookupGRPCFromDescriptor("/qrdb.v1.QRDBService/TotallyUnknownMethod"); ok {
		t.Error("unknown method must resolve to not-found (default-deny)")
	}
	if got, ok := LookupGRPCFromDescriptor("/qrdb.v1.QRDBService/Put"); !ok || got != permission.KVPut {
		t.Errorf("Put: got (%q, %v), want (%q, true)", got, ok, permission.KVPut)
	}
	// Memoized second lookup returns the identical result.
	if got, ok := LookupGRPCFromDescriptor("/qrdb.v1.QRDBService/Put"); !ok || got != permission.KVPut {
		t.Errorf("Put (cached): got (%q, %v), want (%q, true)", got, ok, permission.KVPut)
	}
	// Identity-gated routes resolve to the sentinel: the share-claim
	// bootstrap handshakes, plus the tenant barrier bootstrap surface
	// (init/status/unseal) whose RBAC check runs in the handlers with the
	// shared trust-on-first-init / sealed-window fallbacks
	// (middleware.AuthorizeTenantBarrierInit / AuthorizeTenantBarrierSealedOp)
	// so a self-managed tenant can drive its own unseal while its IAM is
	// encrypted behind the sealed barrier.
	for _, method := range []string{
		"/qrdb.v1.QRDBService/BarrierShareClaimBegin",
		"/qrdb.v1.QRDBService/BarrierShareClaimComplete",
		"/qrdb.v1.TenantBarrierService/ShareClaimBegin",
		"/qrdb.v1.TenantBarrierService/ShareClaimComplete",
		"/qrdb.v1.TenantBarrierService/InitBegin",
		"/qrdb.v1.TenantBarrierService/InitComplete",
		"/qrdb.v1.TenantBarrierService/Unseal",
		"/qrdb.v1.TenantBarrierService/QuorumBegin",
		"/qrdb.v1.TenantBarrierService/QuorumSubmit",
		"/qrdb.v1.QRDBService/BarrierQuorumBegin",
		"/qrdb.v1.QRDBService/BarrierQuorumSubmit",
		"/qrdb.v1.TenantBarrierService/Status",
	} {
		if got, ok := LookupGRPCFromDescriptor(method); !ok || got != permission.IdentityGated {
			t.Errorf("%s: got (%q, %v), want (%q, true)", method, got, ok, permission.IdentityGated)
		}
	}
}

func TestLookupGRPCFromDescriptor_Found(t *testing.T) {
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
		{"/qrdb.v1.QRDBService/RedeemInvitation", permission.Public},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := LookupGRPCFromDescriptor(tt.method)
			if !ok {
				t.Fatalf("expected a permission option for %q", tt.method)
			}
			if got != tt.want {
				t.Errorf("LookupGRPCFromDescriptor(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestLookupGRPCFromDescriptor_NotFound(t *testing.T) {
	methods := []string{
		"/qrdb.v1.QRDBService/UnknownMethod",
		"/other.Service/Put",
		"",
		"/",
		"/grpc.health.v1.Health/Check",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, ok := LookupGRPCFromDescriptor(method)
			if ok {
				t.Errorf("expected no permission option for %q", method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REST route permissions are declared at the registration site (pkg/api/rest
// registerRoutes) and recorded in a RESTRouteRegistry; the exhaustive
// route -> permission expectations live in pkg/api/rest's
// route_mapping_guard_test.go, which drives the real registry built by a
// fully wired server. Registry matching semantics are covered by the
// RESTRouteRegistry tests in this package.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// QUIC method permissions are declared at the registration site (pkg/api/quic
// buildHandlers, via the s.handle/s.handleIf helpers) and recorded in a
// QUICMethodRegistry; the exhaustive method -> permission expectations live
// in pkg/api/quic's method_mapping_guard_test.go, which drives the real
// registry built by a fully wired server. MCP tool permissions are likewise
// declared with each tool (pkg/api/mcp addTools) and guarded by
// pkg/api/mcp's tool_mapping_guard_test.go. Registry matching semantics are
// covered by the perm_registry tests in this package.
// ---------------------------------------------------------------------------

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
	for method, perm := range grpcDescriptorMethodPermissions(t) {
		// permission.IdentityGated and permission.Public are deliberately
		// unregistered sentinels: claim routes are authorized at the handler
		// layer, and RedeemInvitation bypasses authorization entirely. Neither
		// may ever be grantable to a role.
		if perm == permission.IdentityGated || perm == permission.Public {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("gRPC method %q declares unregistered permission %q", method, perm)
		}
	}
}

// TestGRPCPermissions_AllCanonicalServicesAnnotated verifies each of the five
// canonical qrdb.v1 gRPC services contributes at least one permission-carrying
// method to the descriptor surface, so a service whose annotations were lost
// in a proto regeneration cannot slip through (its methods would all be
// default-denied at runtime).
func TestGRPCPermissions_AllCanonicalServicesAnnotated(t *testing.T) {
	prefixes := []string{
		"/qrdb.v1.QRDBService/",
		"/qrdb.v1.TenantBarrierService/",
		"/qrdb.v1.AuditService/",
		"/qrdb.v1.AuthFactorService/",
		"/qrdb.v1.UserAuthFactorService/",
	}
	methodPerms := grpcDescriptorMethodPermissions(t)
	for _, prefix := range prefixes {
		found := false
		for method := range methodPerms {
			if len(method) > len(prefix) && method[:len(prefix)] == prefix {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no annotated method found for service prefix %q", prefix)
		}
	}
}

// The registered-permission, dot-notation, and QUIC->gRPC parity invariants
// over the full QUIC surface moved with the table: they now iterate the real
// QUICMethodRegistry in pkg/api/quic's method_mapping_guard_test.go.

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

// The cross-transport UpdateEntitlements -> tenants:set_entitlements pinning
// (default-deny: an unmapped endpoint is denied at runtime) is asserted per
// transport against the real registries/descriptors:
//   - REST: pkg/api/rest route_mapping_guard_test.go
//     (PATCH /v1/tenants/{id}/entitlements)
//   - gRPC: the (qrdb.v1.permission) proto option, guarded by
//     TestGRPCPermissions_EveryMethodMapped above
//   - QUIC: pkg/api/quic method_mapping_guard_test.go (tenant.UpdateEntitlements)
//   - MCP:  pkg/api/mcp tool_mapping_guard_test.go (qrdb_tenant_update_entitlements)
