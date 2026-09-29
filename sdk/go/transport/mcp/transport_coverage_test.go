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

// MCP SDK transport coverage tests. These exercise:
//   1. Closed-state shortcuts for methods missing from transport_closed_test.go
//   2. Pure conversion helpers (serverUserToTransport, serverRoleToTransport,
//      roleIDsToCSV)
//   3. Typed error formatting
//   4. NotImplemented stubs (backup, share, collection, custodian-group, seal_store)

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// transport_auth.go — pure conversion helpers
// ===========================================================================

func TestMCPServerUserToTransport(t *testing.T) {
	in := &serverUser{
		ID:        1,
		Username:  "alice",
		TenantID:  "t1",
		RoleIDs:   []string{"r1", "r2"},
		Lifecycle: "active",
		System:    false,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-02-01T00:00:00Z",
	}
	out := serverUserToTransport(in)
	if out == nil {
		t.Fatal("nil")
	}
	if out.ID != 1 || out.Username != "alice" || out.TenantID != "t1" {
		t.Errorf("identity: %+v", out)
	}
	if len(out.RoleIDs) != 2 || out.Lifecycle != "active" || out.System {
		t.Errorf("flags: %+v", out)
	}
}

func TestMCPServerRoleToTransport(t *testing.T) {
	in := &serverRole{
		ID:          "r1",
		Name:        "admin",
		TenantID:    "t1",
		Permissions: []string{"kv.read", "kv.write"},
		System:      true,
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-02-01T00:00:00Z",
	}
	out := serverRoleToTransport(in)
	if out == nil {
		t.Fatal("nil")
	}
	if out.ID != "r1" || out.Name != "admin" {
		t.Errorf("identity: %+v", out)
	}
	if len(out.Permissions) != 2 || !out.System {
		t.Errorf("perms: %+v", out)
	}
}

func TestMCPRoleIDsToCSV(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"r1"}, "r1"},
		{[]string{"r1", "r2"}, "r1,r2"},
		{[]string{"r1", "r2", "r3"}, "r1,r2,r3"},
	}
	for _, tc := range cases {
		if got := roleIDsToCSV(tc.in); got != tc.want {
			t.Errorf("roleIDsToCSV(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ===========================================================================
// errors.go — typed error formatting and unwrap
// ===========================================================================

func TestMCPJSONDecodeError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &jsonDecodeError{Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty message")
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap chain broken")
	}
}

// ===========================================================================
// transport.go — Scan / Exists / Capabilities closed paths
// ===========================================================================

func TestMCPScan_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Scan(context.Background(), "p")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("Scan: %v", err)
	}
}

func TestMCPExists_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Exists(context.Background(), "k")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("Exists: %v", err)
	}
}

func TestMCPCapabilities_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Capabilities(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("Capabilities: %v", err)
	}
}

// ===========================================================================
// transport_auth.go — Cert/Role assignment closed paths
// ===========================================================================

func TestMCPAuthExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.GetUserByUsername(ctx, "t", "u"); !errors.As(err, &ce) {
		t.Errorf("GetUserByUsername: %v", err)
	}
	if err := tr.AssignRole(ctx, 1, "r"); !errors.As(err, &ce) {
		t.Errorf("AssignRole: %v", err)
	}
	if err := tr.UnassignRole(ctx, 1, "r"); !errors.As(err, &ce) {
		t.Errorf("UnassignRole: %v", err)
	}
	if _, err := tr.GenerateCert(ctx, &transport.GenerateCertRequest{}); !errors.As(err, &ce) {
		t.Errorf("GenerateCert: %v", err)
	}
	if _, err := tr.ExportCert(ctx, &transport.ExportCertRequest{}); !errors.As(err, &ce) {
		t.Errorf("ExportCert: %v", err)
	}
	if _, err := tr.RevokeCert(ctx, "s"); !errors.As(err, &ce) {
		t.Errorf("RevokeCert: %v", err)
	}
	if _, err := tr.ListCerts(ctx, "t"); !errors.As(err, &ce) {
		t.Errorf("ListCerts: %v", err)
	}
}

// transport_collection.go is now fully implemented over MCP (callTool-based
// client wiring); its NotImplemented stub test was removed. The MCP collection
// surface is exercised end-to-end in test/integration/collection_cli_test.go.

// ===========================================================================
// transport_ceremony.go — closed paths
// ===========================================================================

// ===========================================================================
// transport_barrier.go — extra closed paths
// ===========================================================================

// ===========================================================================
// transport_user_auth_factor.go — closed paths
// ===========================================================================

func TestMCPUserAuthFactor_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); !errors.As(err, &ce) {
		t.Errorf("AddAuthFactor: %v", err)
	}
	if _, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListAuthFactors: %v", err)
	}
	if _, err := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetAuthFactor: %v", err)
	}
	if _, err := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{}); !errors.As(err, &ce) {
		t.Errorf("RemoveAuthFactor: %v", err)
	}
	if _, err := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("RotateAuthFactor: %v", err)
	}
	if _, err := tr.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{}); !errors.As(err, &ce) {
		t.Errorf("RelabelAuthFactor: %v", err)
	}
	if _, err := tr.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{}); !errors.As(err, &ce) {
		t.Errorf("LoginAuthFactor: %v", err)
	}
}

// ===========================================================================
// transport_vector.go — extra closed paths
// ===========================================================================

func TestMCPVectorExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetNamespaceInfo: %v", err)
	}
	if _, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpsertVector: %v", err)
	}
	if _, err := tr.GetVector(ctx, &transport.GetVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetVector: %v", err)
	}
	if _, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("DeleteVector: %v", err)
	}
	if _, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); !errors.As(err, &ce) {
		t.Errorf("BatchUpsert: %v", err)
	}
	if _, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("BatchDeleteVectors: %v", err)
	}
	if _, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListVectors: %v", err)
	}
	if _, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); !errors.As(err, &ce) {
		t.Errorf("SearchSimilar: %v", err)
	}
	if _, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); !errors.As(err, &ce) {
		t.Errorf("SearchByMetadata: %v", err)
	}
	if _, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{}); !errors.As(err, &ce) {
		t.Errorf("HybridSearch: %v", err)
	}
}

// ===========================================================================
// transport_custodian_group.go — NotImplemented stubs
// ===========================================================================

// ===========================================================================
// transport_seal_store.go — NotImplemented stubs
// ===========================================================================

func TestMCPSealStore_NotImplemented(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	var nie *transport.NotImplementedError
	ctx := context.Background()

	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStorePut: %v", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreGet: %v", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreDelete: %v", err)
	}
	if _, err := tr.SealStoreList(ctx); !errors.As(err, &nie) {
		t.Errorf("SealStoreList: %v", err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreReseal: %v", err)
	}
	if _, err := tr.SealStoreStatus(ctx); !errors.As(err, &nie) {
		t.Errorf("SealStoreStatus: %v", err)
	}
}
