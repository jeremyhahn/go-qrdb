// Copyright 2026 Jeremy Hahn
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

// Round-trip tests covering the remaining unix-transport service surfaces:
// tenant management, collection CRUD, document CRUD. Each test wires a real
// gRPC-over-UDS server with a stub service implementation and asserts the
// SDK transport correctly marshals + unmarshals across the boundary.

package unix

import (
	"context"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockTenantSvc — full tenant.TenantServicer stub for tenant CRUD tests
// ---------------------------------------------------------------------------

type mockTenantSvc struct {
	createFn func(ctx context.Context, id, name string, iso tenantpkg.IsolationType) (*tenantpkg.TenantInfo, error)
	getFn    func(ctx context.Context, id string) (*tenantpkg.TenantInfo, error)
	listFn   func(ctx context.Context) ([]tenantpkg.TenantInfo, error)
	deleteFn func(ctx context.Context, id string) error
	updateFn func(ctx context.Context, id string, patch tenantpkg.TenantPatch) (*tenantpkg.TenantInfo, error)
	quotasFn func(ctx context.Context, id string, q *quota.Quotas) (*tenantpkg.TenantInfo, error)
}

func (m *mockTenantSvc) CreateTenant(ctx context.Context, id, name string, iso tenantpkg.IsolationType, _ ...tenantpkg.CreateTenantOption) (*tenantpkg.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, iso)
	}
	return &tenantpkg.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
}
func (m *mockTenantSvc) GetTenant(ctx context.Context, id string) (*tenantpkg.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &tenantpkg.TenantInfo{ID: id, Name: "stub"}, nil
}
func (m *mockTenantSvc) ListTenants(ctx context.Context) ([]tenantpkg.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []tenantpkg.TenantInfo{{ID: "t1", Name: "alpha"}, {ID: "t2", Name: "beta"}}, nil
}
func (m *mockTenantSvc) DeleteTenant(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}
func (m *mockTenantSvc) UpdateTenant(ctx context.Context, id string, patch tenantpkg.TenantPatch) (*tenantpkg.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, patch)
	}
	return &tenantpkg.TenantInfo{ID: id, Name: patch.Name, IsolationType: patch.IsolationType}, nil
}
func (m *mockTenantSvc) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenantpkg.TenantInfo, error) {
	if m.quotasFn != nil {
		return m.quotasFn(ctx, id, q)
	}
	return &tenantpkg.TenantInfo{ID: id, Quotas: q}, nil
}

func (m *mockTenantSvc) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Entitlements: e}, nil
}
func (m *mockTenantSvc) TenantBarrierInit(_ context.Context, id string, _ []byte, _, _ int) (*tenantpkg.TenantBarrierInitResult, error) {
	return &tenantpkg.TenantBarrierInitResult{TenantID: id}, nil
}
func (m *mockTenantSvc) TenantBarrierInitWithStrategy(_ context.Context, id string, _ []byte, _, _ int, _ seal.StrategyID) (*tenantpkg.TenantBarrierInitResult, error) {
	return &tenantpkg.TenantBarrierInitResult{TenantID: id}, nil
}
func (m *mockTenantSvc) TenantBarrierUnsealShare(_ context.Context, id, _ string) (*tenantpkg.TenantBarrierUnsealProgress, error) {
	return &tenantpkg.TenantBarrierUnsealProgress{TenantID: id}, nil
}
func (m *mockTenantSvc) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (m *mockTenantSvc) TenantBarrierSeal(_ context.Context, _ string) error { return nil }
func (m *mockTenantSvc) TenantBarrierStatus(_ context.Context, tenantID string) (*tenantpkg.TenantBarrierStatus, error) {
	return &tenantpkg.TenantBarrierStatus{TenantID: tenantID, Initialized: true}, nil
}
func (m *mockTenantSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantSvc) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantSvc) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}
func (m *mockTenantSvc) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*tenantpkg.TenantBarrierUnsealProgress, error) {
	return &tenantpkg.TenantBarrierUnsealProgress{}, nil
}

var _ tenantpkg.TenantServicer = (*mockTenantSvc)(nil)

// ---------------------------------------------------------------------------
// Tenant round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateTenant(t *testing.T) {
	svc := &mockTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:            "t1",
		Name:          "acme",
		IsolationType: transport.TenantIsolationType("barrier"),
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if resp.Tenant.ID != "t1" || resp.Tenant.Name != "acme" {
		t.Errorf("got %+v, want id=t1 name=acme", resp.Tenant)
	}
}

func TestUnixRT_GetTenant(t *testing.T) {
	svc := &mockTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	resp, err := tr.GetTenant(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if resp.Tenant.ID != "t1" {
		t.Errorf("got %+v, want id=t1", resp.Tenant)
	}
}

func TestUnixRT_ListTenants(t *testing.T) {
	svc := &mockTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	resp, err := tr.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Errorf("len = %d, want 2", len(resp.Tenants))
	}
}

func TestUnixRT_DeleteTenant(t *testing.T) {
	svc := &mockTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	if err := tr.DeleteTenant(context.Background(), "t1"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
}

func TestUnixRT_UpdateTenant(t *testing.T) {
	svc := &mockTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	resp, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID:      "t1",
		Name:          "renamed",
		IsolationType: transport.TenantIsolationType("prefix"),
	})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if resp.Tenant.Name != "renamed" {
		t.Errorf("got %+v, want name=renamed", resp.Tenant)
	}
}

func TestUnixRT_UpdateTenantQuotas(t *testing.T) {
	svc := &mockTenantSvc{
		quotasFn: func(_ context.Context, id string, q *quota.Quotas) (*tenantpkg.TenantInfo, error) {
			if id != "t1" || q.MaxDatabases != 10 {
				t.Errorf("unexpected args: id=%q max=%d", id, q.MaxDatabases)
			}
			return &tenantpkg.TenantInfo{ID: id, Quotas: q}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	resp, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{
		TenantID: "t1",
		Quotas: transport.TenantQuotas{
			MaxDatabases:    10,
			MaxRaftGroups:   5,
			MaxCollections:  20,
			MaxKeys:         1000,
			MaxStorageBytes: 1 << 30,
		},
	})
	if err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
	if resp.Tenant.Quotas == nil || resp.Tenant.Quotas.MaxDatabases != 10 {
		t.Errorf("got %+v, want MaxDatabases=10", resp.Tenant.Quotas)
	}
}

// ---------------------------------------------------------------------------
// mockCollectionSvc — service.CollectionServicer stub
// ---------------------------------------------------------------------------

type mockCollectionSvc struct {
	createFn  func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropFn    func(ctx context.Context, req *service.DropCollectionRequest) error
	listFn    func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getFn     func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertFn  func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocFn  func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateFn  func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteFn  func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocFn func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

func (m *mockCollectionSvc) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, IDStrategy: req.IDStrategy}, nil
}
func (m *mockCollectionSvc) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, req)
	}
	return nil
}
func (m *mockCollectionSvc) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{{Name: "users", Database: req.Database}},
	}, nil
}
func (m *mockCollectionSvc) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, database, name)
	}
	return &service.CollectionInfo{Name: name, Database: database, IDStrategy: "auto", DocumentCount: 7}, nil
}
func (m *mockCollectionSvc) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertFn != nil {
		return m.insertFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 99}, nil
}
func (m *mockCollectionSvc) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocFn != nil {
		return m.getDocFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"k": "v"}}, nil
}
func (m *mockCollectionSvc) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}
func (m *mockCollectionSvc) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}
func (m *mockCollectionSvc) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocFn != nil {
		return m.listDocFn(ctx, req)
	}
	return &service.ListDocumentsResponse{
		Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"a": "b"}}},
		TotalCount: 1,
	}, nil
}

var _ service.CollectionServicer = (*mockCollectionSvc)(nil)

// ---------------------------------------------------------------------------
// Collection round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateCollection(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{
		Database:   "main",
		Name:       "users",
		IDStrategy: "auto",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if got.Name != "users" || got.Database != "main" {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_GetCollectionInfo(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.GetCollectionInfo(context.Background(), "main", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if got.Name != "users" || got.DocumentCount != 7 {
		t.Errorf("got %+v, want Name=users DocumentCount=7", got)
	}
}

func TestUnixRT_ListCollections(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.ListCollections(context.Background(), "main")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(got.Collections) != 1 {
		t.Errorf("got %d, want 1", len(got.Collections))
	}
}

func TestUnixRT_DropCollection(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	if err := tr.DropCollection(context.Background(), "main", "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
}

func TestUnixRT_InsertDocument(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{
		Database:   "main",
		Collection: "users",
		Document:   map[string]interface{}{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if got.ID != 99 {
		t.Errorf("got ID=%d, want 99", got.ID)
	}
}

func TestUnixRT_GetDocument(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.GetDocument(context.Background(), "main", "users", 5)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.ID != 5 {
		t.Errorf("got ID=%d, want 5", got.ID)
	}
}

func TestUnixRT_UpdateDocument(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{
		Database:   "main",
		Collection: "users",
		ID:         5,
		Document:   map[string]interface{}{"name": "bob"},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

func TestUnixRT_DeleteDocument(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	if err := tr.DeleteDocument(context.Background(), "main", "users", 5); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestUnixRT_ListDocuments(t *testing.T) {
	svc := &mockCollectionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	got, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{
		Database:   "main",
		Collection: "users",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(got.Documents) != 1 {
		t.Errorf("got %d docs, want 1", len(got.Documents))
	}
}

// ---------------------------------------------------------------------------
// CeremonyInitialize round-trip — Ceremony is registered separately
// ---------------------------------------------------------------------------

func TestUnixRT_CeremonyOps_WithoutService(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	// Without a ceremony service, the server returns Unimplemented; the SDK
	// surfaces that as a transport.RequestError. We just verify that errors
	// flow correctly across the wire for an unconfigured service.
	if _, err := tr.CeremonyStatus(context.Background()); err == nil {
		t.Error("expected error when ceremony service is not configured")
	}
}

func (m *mockTenantSvc) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
