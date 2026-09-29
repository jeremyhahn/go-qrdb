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

package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Helper: serve catch-all error to test error branches uniformly
// ===========================================================================

func errorServer(t *testing.T, status int) *Transport {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(_ *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", status)
	})
	return rt
}

func badJSONServer(t *testing.T) *Transport {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(_ *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	})
	return rt
}

// ===========================================================================
// MVCC error paths
// ===========================================================================

func TestRESTMVCC_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.PutWithLease(ctx, "k", []byte("v"), 1); err == nil {
		t.Fatal("PutWithLease: expected error")
	}
	if _, err := rt.GetRevision(ctx, "k"); err == nil {
		t.Fatal("GetRevision: expected error")
	}
	if _, err := rt.GetAtRevision(ctx, "k", 1); err == nil {
		t.Fatal("GetAtRevision: expected error")
	}
	if _, err := rt.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("Range: expected error")
	}
	if err := rt.Compact(ctx, 1); err == nil {
		t.Fatal("Compact: expected error")
	}
	if _, err := rt.CurrentRevision(ctx); err == nil {
		t.Fatal("CurrentRevision: expected error")
	}
}

func TestRESTMVCC_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.PutWithLease(ctx, "k", []byte("v"), 1); err == nil {
		t.Fatal("PutWithLease: expected error")
	}
	if _, err := rt.GetRevision(ctx, "k"); err == nil {
		t.Fatal("GetRevision: expected error")
	}
	if _, err := rt.GetAtRevision(ctx, "k", 1); err == nil {
		t.Fatal("GetAtRevision: expected error")
	}
	if _, err := rt.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("Range: expected error")
	}
	if _, err := rt.CurrentRevision(ctx); err == nil {
		t.Fatal("CurrentRevision: expected error")
	}
}

// ===========================================================================
// Vector error paths
// ===========================================================================

func TestRESTVector_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); err == nil {
		t.Fatal("CreateNamespace: expected error")
	}
	if _, err := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); err == nil {
		t.Fatal("DeleteNamespace: expected error")
	}
	if _, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); err == nil {
		t.Fatal("ListNamespaces: expected error")
	}
	if _, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); err == nil {
		t.Fatal("GetNamespaceInfo: expected error")
	}
	if _, err := rt.UpsertVector(ctx, &transport.UpsertVectorRequest{}); err == nil {
		t.Fatal("UpsertVector: expected error")
	}
	if _, err := rt.GetVector(ctx, &transport.GetVectorRequest{}); err == nil {
		t.Fatal("GetVector: expected error")
	}
	if _, err := rt.DeleteVector(ctx, &transport.DeleteVectorRequest{}); err == nil {
		t.Fatal("DeleteVector: expected error")
	}
	if _, err := rt.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); err == nil {
		t.Fatal("BatchUpsert: expected error")
	}
	if _, err := rt.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{}); err == nil {
		t.Fatal("BatchDeleteVectors: expected error")
	}
	if _, err := rt.ListVectors(ctx, &transport.ListVectorsRequest{}); err == nil {
		t.Fatal("ListVectors: expected error")
	}
	if _, err := rt.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{}); err == nil {
		t.Fatal("UpdateMetadata: expected error")
	}
	if _, err := rt.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); err == nil {
		t.Fatal("SearchSimilar: expected error")
	}
	if _, err := rt.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); err == nil {
		t.Fatal("SearchByMetadata: expected error")
	}
	if _, err := rt.HybridSearch(ctx, &transport.HybridSearchRequest{}); err == nil {
		t.Fatal("HybridSearch: expected error")
	}
	if _, err := rt.SearchByID(ctx, &transport.SearchByIDRequest{}); err == nil {
		t.Fatal("SearchByID: expected error")
	}
	if _, err := rt.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); err == nil {
		t.Fatal("RebuildIndex: expected error")
	}
	if _, err := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{}); err == nil {
		t.Fatal("GetRebuildProgress: expected error")
	}
	if _, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); err == nil {
		t.Fatal("GetIndexStats: expected error")
	}
	if _, err := rt.CompactNamespace(ctx, &transport.CompactNamespaceRequest{}); err == nil {
		t.Fatal("CompactNamespace: expected error")
	}
	if _, err := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{}); err == nil {
		t.Fatal("GetCompactionProgress: expected error")
	}
}

// ===========================================================================
// AuthFactor + Ceremony + UserAuthFactor error paths
// ===========================================================================

func TestRESTAuthFactor_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.EnrollFactor(ctx, &transport.EnrollFactorRequest{}); err == nil {
		t.Fatal("EnrollFactor: expected error")
	}
	if _, err := rt.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{}); err == nil {
		t.Fatal("ChallengeFactor: expected error")
	}
	if err := rt.VerifyFactor(ctx, &transport.VerifyFactorRequest{}); err == nil {
		t.Fatal("VerifyFactor: expected error")
	}
	if _, err := rt.ListFactors(ctx, &transport.ListFactorsRequest{}); err == nil {
		t.Fatal("ListFactors: expected error")
	}
	if err := rt.RevokeFactor(ctx, &transport.RevokeFactorRequest{}); err == nil {
		t.Fatal("RevokeFactor: expected error")
	}
}

func TestRESTUserAuthFactor_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); err == nil {
		t.Fatal("AddAuthFactor: expected error")
	}
	if _, err := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); err == nil {
		t.Fatal("ListAuthFactors: expected error")
	}
	if _, err := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); err == nil {
		t.Fatal("GetAuthFactor: expected error")
	}
	if _, err := rt.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{}); err == nil {
		t.Fatal("RemoveAuthFactor: expected error")
	}
	if _, err := rt.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{}); err == nil {
		t.Fatal("RotateAuthFactor: expected error")
	}
	if _, err := rt.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{}); err == nil {
		t.Fatal("RelabelAuthFactor: expected error")
	}
	if _, err := rt.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{}); err == nil {
		t.Fatal("LoginAuthFactor: expected error")
	}
}

// ===========================================================================
// Tenant error paths
// ===========================================================================

func TestRESTTenant_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.CreateTenant(ctx, &transport.CreateTenantRequest{}); err == nil {
		t.Fatal("CreateTenant: expected error")
	}
	if _, err := rt.GetTenant(ctx, "t1"); err == nil {
		t.Fatal("GetTenant: expected error")
	}
	if _, err := rt.ListTenants(ctx); err == nil {
		t.Fatal("ListTenants: expected error")
	}
	if err := rt.DeleteTenant(ctx, "t1"); err == nil {
		t.Fatal("DeleteTenant: expected error")
	}
	if _, err := rt.UpdateTenant(ctx, &transport.UpdateTenantRequest{}); err == nil {
		t.Fatal("UpdateTenant: expected error")
	}
	if _, err := rt.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{}); err == nil {
		t.Fatal("UpdateTenantQuotas: expected error")
	}
}

func TestRESTTenant_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.CreateTenant(ctx, &transport.CreateTenantRequest{}); err == nil {
		t.Fatal("CreateTenant: expected error")
	}
	if _, err := rt.GetTenant(ctx, "t1"); err == nil {
		t.Fatal("GetTenant: expected error")
	}
	if _, err := rt.ListTenants(ctx); err == nil {
		t.Fatal("ListTenants: expected error")
	}
	if _, err := rt.UpdateTenant(ctx, &transport.UpdateTenantRequest{}); err == nil {
		t.Fatal("UpdateTenant: expected error")
	}
	if _, err := rt.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{}); err == nil {
		t.Fatal("UpdateTenantQuotas: expected error")
	}
}

// ===========================================================================
// Vector + Tenant + UserAuthFactor BadJSON paths
// ===========================================================================

func TestRESTVector_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); err == nil {
		t.Fatal("CreateNamespace: expected JSON error")
	}
	if _, err := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); err == nil {
		t.Fatal("DeleteNamespace: expected JSON error")
	}
	if _, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); err == nil {
		t.Fatal("ListNamespaces: expected JSON error")
	}
	if _, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"}); err == nil {
		t.Fatal("GetNamespaceInfo: expected JSON error")
	}
}

func TestRESTUserAuthFactor_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); err == nil {
		t.Fatal("AddAuthFactor: expected JSON error")
	}
	if _, err := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); err == nil {
		t.Fatal("ListAuthFactors: expected JSON error")
	}
	if _, err := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); err == nil {
		t.Fatal("GetAuthFactor: expected JSON error")
	}
}

// ===========================================================================
// Audit + Batch error paths
// ===========================================================================

func TestRESTQueryAudit_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	if _, err := rt.QueryAudit(context.Background(), transport.AuditFilter{}); err == nil {
		t.Fatal("QueryAudit: expected error")
	}
}

func TestRESTQueryAudit_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	// Filter populated to cover all params branches.
	if _, err := rt.QueryAudit(context.Background(), transport.AuditFilter{
		Actor: "a", Resource: "r", Action: "x", Outcome: "ok", TenantID: "t", Limit: 10, Cursor: "c",
	}); err == nil {
		t.Fatal("QueryAudit: expected JSON error")
	}
}

func TestRESTExecuteBatch_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	if _, err := rt.ExecuteBatch(context.Background(), nil); err == nil {
		t.Fatal("ExecuteBatch: expected error")
	}
}

// ===========================================================================
// JSON encode error path (postJSONDecode / putJSONDecode / patchJSONDecode)
// ===========================================================================

func TestRESTAuthHelpers_EncodeErrors(t *testing.T) {
	// Auth helpers reject non-marshallable payloads. Construct a struct
	// containing a channel field which json.Marshal cannot serialise.
	bad := struct{ Ch chan int }{Ch: make(chan int)}

	rt, _, _ := newExtTransport(t)
	if err := rt.postJSONDecode(context.Background(), "Test", "/x", bad, nil); err == nil {
		t.Fatal("postJSONDecode: expected encode error")
	}
	if err := rt.putJSONDecode(context.Background(), "Test", "/x", bad, nil); err == nil {
		t.Fatal("putJSONDecode: expected encode error")
	}
	if err := rt.patchJSONDecode(context.Background(), "Test", "/x", bad, nil); err == nil {
		t.Fatal("patchJSONDecode: expected encode error")
	}
}

// ===========================================================================
// Cover the empty-helpers path via PATCH success
// ===========================================================================

func TestRESTPatchJSONDecode_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return r.Method == http.MethodPatch }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1})
	})
	var u transport.User
	if err := rt.patchJSONDecode(context.Background(), "Test", "/x", &transport.UpdateUserRequest{ID: 1}, &u); err != nil {
		t.Fatalf("patchJSONDecode: %v", err)
	}
	if u.ID != 1 {
		t.Fatalf("ID = %d", u.ID)
	}
}

func TestRESTPatchJSONDecode_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	if err := rt.patchJSONDecode(context.Background(), "Test", "/x", &transport.UpdateUserRequest{}, nil); err == nil {
		t.Fatal("patchJSONDecode: expected server error")
	}
}

func TestRESTPatchJSONDecode_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	var u transport.User
	if err := rt.patchJSONDecode(context.Background(), "Test", "/x", &transport.UpdateUserRequest{}, &u); err == nil {
		t.Fatal("patchJSONDecode: expected JSON error")
	}
}

// ===========================================================================
// Watch closed already covered, here ensure Watch with empty options compiles
// ===========================================================================

func TestRESTWatchTypes_Compile(t *testing.T) {
	// Just verify type construction works for completeness.
	opts := transport.WatchOptions{StartRevision: 1, PrevKV: true, ProgressNotify: true}
	if opts.StartRevision != 1 {
		t.Fatal("type mismatch")
	}
	_ = strings.Repeat("", 0)
}

// ===========================================================================
// AuthFactor + Ceremony BadJSON paths
// ===========================================================================

func TestRESTAuthFactor_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.EnrollFactor(ctx, &transport.EnrollFactorRequest{}); err == nil {
		t.Fatal("EnrollFactor: expected JSON error")
	}
	if _, err := rt.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{}); err == nil {
		t.Fatal("ChallengeFactor: expected JSON error")
	}
	if _, err := rt.ListFactors(ctx, &transport.ListFactorsRequest{Subject: "alice"}); err == nil {
		t.Fatal("ListFactors: expected JSON error")
	}
}

// ===========================================================================
// User/Role/Cert BadJSON paths
// ===========================================================================

func TestRESTUserRoleCert_AllMethods_BadJSON(t *testing.T) {
	rt := badJSONServer(t)
	ctx := context.Background()

	if _, err := rt.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true}); err == nil {
		t.Fatal("CreateUser: expected JSON error")
	}
	if _, err := rt.GetUser(ctx, 1); err == nil {
		t.Fatal("GetUser: expected JSON error")
	}
	if _, err := rt.GetUserByUsername(ctx, "t1", "alice"); err == nil {
		t.Fatal("GetUserByUsername: expected JSON error")
	}
	if _, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1}); err == nil {
		t.Fatal("UpdateUser: expected JSON error")
	}
	if _, err := rt.ListUsers(ctx, &transport.ListUsersRequest{}); err == nil {
		t.Fatal("ListUsers: expected JSON error")
	}
	if _, err := rt.CreateRole(ctx, &transport.CreateRoleRequest{}); err == nil {
		t.Fatal("CreateRole: expected JSON error")
	}
	if _, err := rt.GetRole(ctx, "r1"); err == nil {
		t.Fatal("GetRole: expected JSON error")
	}
	if _, err := rt.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r1"}); err == nil {
		t.Fatal("UpdateRole: expected JSON error")
	}
	if _, err := rt.ListRoles(ctx, &transport.ListRolesRequest{}); err == nil {
		t.Fatal("ListRoles: expected JSON error")
	}
	if _, err := rt.ListPermissions(ctx); err == nil {
		t.Fatal("ListPermissions: expected JSON error")
	}
	if _, err := rt.GenerateCert(ctx, &transport.GenerateCertRequest{}); err == nil {
		t.Fatal("GenerateCert: expected JSON error")
	}
	if _, err := rt.ExportCert(ctx, &transport.ExportCertRequest{}); err == nil {
		t.Fatal("ExportCert: expected JSON error")
	}
	if _, err := rt.ListCerts(ctx, "t1"); err == nil {
		t.Fatal("ListCerts: expected JSON error")
	}
}

// ===========================================================================
// User/Role/Cert ServerError paths
// ===========================================================================

func TestRESTUserRoleCert_AllMethods_ServerError(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true}); err == nil {
		t.Fatal("CreateUser: expected error")
	}
	if _, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1}); err == nil {
		t.Fatal("UpdateUser: expected error")
	}
	if err := rt.EnableUser(ctx, 1); err == nil {
		t.Fatal("EnableUser: expected error")
	}
	if err := rt.DisableUser(ctx, 1); err == nil {
		t.Fatal("DisableUser: expected error")
	}
	if _, err := rt.CreateRole(ctx, &transport.CreateRoleRequest{}); err == nil {
		t.Fatal("CreateRole: expected error")
	}
	if _, err := rt.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r1"}); err == nil {
		t.Fatal("UpdateRole: expected error")
	}
	if err := rt.AssignRole(ctx, 1, "r1"); err == nil {
		t.Fatal("AssignRole: expected error")
	}
	if err := rt.UnassignRole(ctx, 1, "r1"); err == nil {
		t.Fatal("UnassignRole: expected error")
	}
	if _, err := rt.GenerateCert(ctx, &transport.GenerateCertRequest{}); err == nil {
		t.Fatal("GenerateCert: expected error")
	}
	if _, err := rt.ExportCert(ctx, &transport.ExportCertRequest{}); err == nil {
		t.Fatal("ExportCert: expected error")
	}
	if _, err := rt.RevokeCert(ctx, "001"); err == nil {
		t.Fatal("RevokeCert: expected error")
	}
}

// ===========================================================================
// Barrier ServerError + BadJSON paths for items not yet covered
// ===========================================================================

// ===========================================================================
// Collection + Lease + Lock ServerError paths
// ===========================================================================

func TestRESTCollection_MoreErrors(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.CreateCollection(ctx, transport.CreateCollectionRequest{}); err == nil {
		t.Fatal("CreateCollection: expected error")
	}
	if _, err := rt.InsertDocument(ctx, transport.InsertDocumentRequest{}); err == nil {
		t.Fatal("InsertDocument: expected error")
	}
	if err := rt.UpdateDocument(ctx, transport.UpdateDocumentRequest{}); err == nil {
		t.Fatal("UpdateDocument: expected error")
	}
}

func TestRESTLease_MoreErrors(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.LeaseGrant(ctx, 60); err == nil {
		t.Fatal("LeaseGrant: expected error")
	}
	if err := rt.LeaseRevoke(ctx, 42); err == nil {
		t.Fatal("LeaseRevoke: expected error")
	}
	if _, err := rt.LeaseKeepAlive(ctx, 42); err == nil {
		t.Fatal("LeaseKeepAlive: expected error")
	}
}

func TestRESTLock_MoreErrors(t *testing.T) {
	rt := errorServer(t, http.StatusInternalServerError)
	ctx := context.Background()

	if _, err := rt.LockAcquire(ctx, &transport.LockAcquireRequest{}); err == nil {
		t.Fatal("LockAcquire: expected error")
	}
	if _, err := rt.LockTryLock(ctx, &transport.LockAcquireRequest{}); err == nil {
		t.Fatal("LockTryLock: expected error")
	}
	if err := rt.LockRelease(ctx, &transport.LockReleaseRequest{}); err == nil {
		t.Fatal("LockRelease: expected error")
	}
	if _, err := rt.LockExtend(ctx, &transport.LockExtendRequest{}); err == nil {
		t.Fatal("LockExtend: expected error")
	}
}

// ===========================================================================
// Network-error tests: use a closed httptest server to make all methods
// hit the http.client.Do error branch.
// ===========================================================================

func deadServer(t *testing.T) *Transport {
	rt, _, srv := newExtTransport(t)
	srv.Close() // shut down immediately so Do() fails
	return rt
}

func TestRESTAllMethods_NetworkError(t *testing.T) {
	rt := deadServer(t)
	ctx := context.Background()

	// MVCC
	if _, err := rt.PutWithLease(ctx, "k", []byte("v"), 1); err == nil {
		t.Fatal("PutWithLease: expected error")
	}
	if _, err := rt.GetRevision(ctx, "k"); err == nil {
		t.Fatal("GetRevision: expected error")
	}
	if _, err := rt.GetAtRevision(ctx, "k", 1); err == nil {
		t.Fatal("GetAtRevision: expected error")
	}
	if _, err := rt.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("Range: expected error")
	}
	if err := rt.Compact(ctx, 1); err == nil {
		t.Fatal("Compact: expected error")
	}
	if _, err := rt.CurrentRevision(ctx); err == nil {
		t.Fatal("CurrentRevision: expected error")
	}
	// User / Role / Cert
	if _, err := rt.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true}); err == nil {
		t.Fatal("CreateUser: expected error")
	}
	if _, err := rt.GetUser(ctx, 1); err == nil {
		t.Fatal("GetUser: expected error")
	}
	if _, err := rt.GetUserByUsername(ctx, "t1", "alice"); err == nil {
		t.Fatal("GetUserByUsername: expected error")
	}
	if _, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{}); err == nil {
		t.Fatal("UpdateUser: expected error")
	}
	if err := rt.DeleteUser(ctx, 1); err == nil {
		t.Fatal("DeleteUser: expected error")
	}
	if _, err := rt.ListUsers(ctx, &transport.ListUsersRequest{}); err == nil {
		t.Fatal("ListUsers: expected error")
	}
	if err := rt.EnableUser(ctx, 1); err == nil {
		t.Fatal("EnableUser: expected error")
	}
	if err := rt.DisableUser(ctx, 1); err == nil {
		t.Fatal("DisableUser: expected error")
	}
	if _, err := rt.CreateRole(ctx, &transport.CreateRoleRequest{}); err == nil {
		t.Fatal("CreateRole: expected error")
	}
	if _, err := rt.GetRole(ctx, "r1"); err == nil {
		t.Fatal("GetRole: expected error")
	}
	if _, err := rt.UpdateRole(ctx, &transport.UpdateRoleRequest{}); err == nil {
		t.Fatal("UpdateRole: expected error")
	}
	if err := rt.DeleteRole(ctx, "r1"); err == nil {
		t.Fatal("DeleteRole: expected error")
	}
	if _, err := rt.ListRoles(ctx, &transport.ListRolesRequest{}); err == nil {
		t.Fatal("ListRoles: expected error")
	}
	if err := rt.AssignRole(ctx, 1, "r1"); err == nil {
		t.Fatal("AssignRole: expected error")
	}
	if err := rt.UnassignRole(ctx, 1, "r1"); err == nil {
		t.Fatal("UnassignRole: expected error")
	}
	if _, err := rt.ListPermissions(ctx); err == nil {
		t.Fatal("ListPermissions: expected error")
	}
	if _, err := rt.GenerateCert(ctx, &transport.GenerateCertRequest{}); err == nil {
		t.Fatal("GenerateCert: expected error")
	}
	if _, err := rt.ExportCert(ctx, &transport.ExportCertRequest{}); err == nil {
		t.Fatal("ExportCert: expected error")
	}
	if _, err := rt.RevokeCert(ctx, "001"); err == nil {
		t.Fatal("RevokeCert: expected error")
	}
	if _, err := rt.ListCerts(ctx, "t1"); err == nil {
		t.Fatal("ListCerts: expected error")
	}
	// AuthFactor
	if _, err := rt.EnrollFactor(ctx, &transport.EnrollFactorRequest{}); err == nil {
		t.Fatal("EnrollFactor: expected error")
	}
	if _, err := rt.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{}); err == nil {
		t.Fatal("ChallengeFactor: expected error")
	}
	if err := rt.VerifyFactor(ctx, &transport.VerifyFactorRequest{}); err == nil {
		t.Fatal("VerifyFactor: expected error")
	}
	if _, err := rt.ListFactors(ctx, &transport.ListFactorsRequest{}); err == nil {
		t.Fatal("ListFactors: expected error")
	}
	if err := rt.RevokeFactor(ctx, &transport.RevokeFactorRequest{}); err == nil {
		t.Fatal("RevokeFactor: expected error")
	}
	// Barrier
	if err := rt.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{}); err == nil {
		t.Fatal("BarrierInitialize: expected error")
	}
	if _, err := rt.BarrierStatus(ctx); err == nil {
		t.Fatal("BarrierStatus: expected error")
	}
	if _, err := rt.BarrierHasRecoveryKeys(ctx); err == nil {
		t.Fatal("BarrierHasRecoveryKeys: expected error")
	}
	if err := rt.BarrierDeleteRecoveryKeys(ctx); err == nil {
		t.Fatal("BarrierDeleteRecoveryKeys: expected error")
	}
	// Collection
	if _, err := rt.CreateCollection(ctx, transport.CreateCollectionRequest{}); err == nil {
		t.Fatal("CreateCollection: expected error")
	}
	if _, err := rt.GetCollectionInfo(ctx, "db", "users"); err == nil {
		t.Fatal("GetCollectionInfo: expected error")
	}
	if _, err := rt.ListCollections(ctx, "db"); err == nil {
		t.Fatal("ListCollections: expected error")
	}
	if err := rt.DropCollection(ctx, "db", "users"); err == nil {
		t.Fatal("DropCollection: expected error")
	}
	if _, err := rt.GetDocument(ctx, "db", "users", 1); err == nil {
		t.Fatal("GetDocument: expected error")
	}
	if err := rt.DeleteDocument(ctx, "db", "users", 1); err == nil {
		t.Fatal("DeleteDocument: expected error")
	}
	if _, err := rt.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"}); err == nil {
		t.Fatal("ListDocuments: expected error")
	}
	// Lease / Lock
	if _, err := rt.LeaseTimeToLive(ctx, 1); err == nil {
		t.Fatal("LeaseTimeToLive: expected error")
	}
	if _, err := rt.LeaseList(ctx); err == nil {
		t.Fatal("LeaseList: expected error")
	}
	if _, err := rt.LockStatus(ctx, "k1"); err == nil {
		t.Fatal("LockStatus: expected error")
	}
	if _, err := rt.LockList(ctx, "prefix"); err == nil {
		t.Fatal("LockList: expected error")
	}
	// Audit
	if _, err := rt.QueryAudit(ctx, transport.AuditFilter{}); err == nil {
		t.Fatal("QueryAudit: expected error")
	}
	// Vector
	if _, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); err == nil {
		t.Fatal("ListNamespaces: expected error")
	}
	if _, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); err == nil {
		t.Fatal("GetNamespaceInfo: expected error")
	}
	if _, err := rt.GetVector(ctx, &transport.GetVectorRequest{}); err == nil {
		t.Fatal("GetVector: expected error")
	}
	if _, err := rt.ListVectors(ctx, &transport.ListVectorsRequest{}); err == nil {
		t.Fatal("ListVectors: expected error")
	}
	if _, err := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{}); err == nil {
		t.Fatal("GetRebuildProgress: expected error")
	}
	if _, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); err == nil {
		t.Fatal("GetIndexStats: expected error")
	}
	if _, err := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{}); err == nil {
		t.Fatal("GetCompactionProgress: expected error")
	}
	// Tenant
	if _, err := rt.GetTenant(ctx, "t1"); err == nil {
		t.Fatal("GetTenant: expected error")
	}
	if _, err := rt.ListTenants(ctx); err == nil {
		t.Fatal("ListTenants: expected error")
	}
	if err := rt.DeleteTenant(ctx, "t1"); err == nil {
		t.Fatal("DeleteTenant: expected error")
	}
}

// ===========================================================================
// Watch SSE success path
// ===========================================================================

func TestRESTWatch_SSE_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/watch"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Send a couple of SSE frames then a canceled frame.
		_, _ = w.Write([]byte(`data: {"watch_id":1,"events":[{"type":"PUT","kv":{"key":"YQ==","value":"dg==","mod_revision":1}}],"revision":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"watch_id":1,"events":[{"type":"DELETE","kv":{"key":"YQ=="}}],"revision":2}` + "\n"))
		_, _ = w.Write([]byte(`data: {"watch_id":1,"canceled":true,"cancel_reason":"done"}` + "\n"))
	})
	ch, err := rt.Watch(context.Background(), "k", transport.WatchOptions{StartRevision: 1, PrevKV: true})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	count := 0
	for range ch {
		count++
	}
	if count < 1 {
		t.Fatalf("got %d events", count)
	}
}

func TestRESTWatch_SSE_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/watch"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: not-json` + "\n"))
	})
	ch, err := rt.Watch(context.Background(), "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// We expect a single cancel response with bad-json error.
	got := 0
	for resp := range ch {
		got++
		if !resp.Canceled {
			t.Errorf("expected canceled, got %+v", resp)
		}
	}
	if got != 1 {
		t.Fatalf("got %d responses", got)
	}
}

func TestRESTWatch_SSE_NonDataLines(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/watch"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(": ping\n"))
		_, _ = w.Write([]byte("\n"))
		_, _ = w.Write([]byte("data: {\"watch_id\":1,\"canceled\":true}\n"))
	})
	ch, err := rt.WatchPrefix(context.Background(), "p", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	got := 0
	for range ch {
		got++
	}
	if got == 0 {
		t.Fatal("expected at least one response")
	}
}

func TestRESTWatch_SSE_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/watch"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	_, err := rt.Watch(context.Background(), "k", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}
