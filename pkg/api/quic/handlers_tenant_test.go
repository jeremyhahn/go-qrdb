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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// mockTenantService is a table-driven fake TenantServicer for QUIC tests.
// ---------------------------------------------------------------------------

type mockTenantService struct {
	createFn        func(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error)
	getFn           func(ctx context.Context, id string) (*tenant.TenantInfo, error)
	listFn          func(ctx context.Context) ([]tenant.TenantInfo, error)
	updateFn        func(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error)
	deleteFn        func(ctx context.Context, id string) error
	updateQuotasFn  func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error)
	quotasFn        func(ctx context.Context, id string) (*quota.Quotas, error)
	barrierInitFn   func(ctx context.Context, id string, passphrase []byte, threshold, total int) error
	barrierUnsealFn func(ctx context.Context, id string, share []byte) error
	barrierSealFn   func(ctx context.Context, id string) error
	barrierStatusFn func(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error)
}

var _ tenant.TenantServicer = (*mockTenantService)(nil)

func (m *mockTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, iso)
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &tenant.TenantInfo{ID: id, Name: "test-tenant", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}

func (m *mockTenantService) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *mockTenantService) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
}

func (m *mockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []tenant.TenantInfo{
		{ID: "t1", Name: "Tenant One", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}, nil
}

func (m *mockTenantService) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, p)
	}
	return &tenant.TenantInfo{ID: id, Name: p.Name, IsolationType: p.IsolationType, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantService) DeleteTenant(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasFn != nil {
		return m.updateQuotasFn(ctx, id, q)
	}
	return &tenant.TenantInfo{ID: id, Name: "test", Quotas: q, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantService) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantService) Quotas(ctx context.Context, id string) (*quota.Quotas, error) {
	if m.quotasFn != nil {
		return m.quotasFn(ctx, id)
	}
	return &quota.Quotas{}, nil
}

func (m *mockTenantService) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	if m.barrierInitFn != nil {
		return nil, m.barrierInitFn(ctx, id, passphrase, threshold, total)
	}
	return nil, nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}

func (m *mockTenantService) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}

func (m *mockTenantService) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantService) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantService) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantService) TenantBarrierUnseal(ctx context.Context, id string, share []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, id, share)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx, id)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx, id)
	}
	return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, Initialized: true, Strategy: "passphrase"}, nil
}
func (m *mockTenantService) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantService) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantService) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantService) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}
func (m *mockTenantService) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantService) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return &tenant.TenantBarrierUnsealProgress{}, nil
}

// ---------------------------------------------------------------------------
// handleTenantCreate
// ---------------------------------------------------------------------------

func TestHandleTenantCreate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantCreateRequest{Id: "acme", Name: "ACME"})

	_, err := srv.handleTenantCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantCreate_Success(t *testing.T) {
	svc := &mockTenantService{
		createFn: func(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          name,
				IsolationType: iso,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := proto.Marshal(&pb.TenantCreateRequest{
		Id:            "acme",
		Name:          "ACME Corp",
		IsolationType: "barrier",
	})
	resp, err := srv.handleTenantCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", tenantResp.GetId())
	}
	if tenantResp.GetName() != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", tenantResp.GetName())
	}
}

func TestHandleTenantCreate_ServiceError(t *testing.T) {
	svcErr := tenant.ErrTenantAlreadyExists
	svc := &mockTenantService{
		createFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantCreateRequest{Id: "dup", Name: "Dup"})

	_, err := srv.handleTenantCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantAlreadyExists) {
		t.Fatalf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestHandleTenantCreate_UnmarshalError(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantCreate(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantGet
// ---------------------------------------------------------------------------

func TestHandleTenantGet_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "acme"})

	_, err := srv.handleTenantGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantGet_Success(t *testing.T) {
	svc := &mockTenantService{
		getFn: func(_ context.Context, id string) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          "ACME Corp",
				IsolationType: tenant.IsolationBarrier,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "acme"})
	resp, err := srv.handleTenantGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", tenantResp.GetId())
	}
	if tenantResp.GetName() != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", tenantResp.GetName())
	}
}

func TestHandleTenantGet_NotFound(t *testing.T) {
	svc := &mockTenantService{
		getFn: func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "missing"})

	_, err := srv.handleTenantGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestHandleTenantGet_UnmarshalError(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantGet(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantList
// ---------------------------------------------------------------------------

func TestHandleTenantList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleTenantList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantList_Success(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return []tenant.TenantInfo{
				{ID: "t1", Name: "Tenant One", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
				{ID: "t2", Name: "Tenant Two", IsolationType: tenant.IsolationPrefix, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	resp, err := srv.handleTenantList(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var listResp pb.TenantListResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.GetTenants()) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(listResp.GetTenants()))
	}
	if listResp.GetTenants()[0].GetId() != "t1" {
		t.Errorf("expected first id 't1', got %q", listResp.GetTenants()[0].GetId())
	}
}

func TestHandleTenantList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list failed"}
	svc := &mockTenantService{
		listFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdate
// ---------------------------------------------------------------------------

func TestHandleTenantUpdate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{Id: "acme", Name: "New Name"})

	_, err := srv.handleTenantUpdate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantUpdate_Success(t *testing.T) {
	svc := &mockTenantService{
		updateFn: func(_ context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          p.Name,
				IsolationType: tenant.IsolationBarrier,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{
		Id:   "acme",
		Name: "ACME Updated",
	})
	resp, err := srv.handleTenantUpdate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetName() != "ACME Updated" {
		t.Errorf("expected name 'ACME Updated', got %q", tenantResp.GetName())
	}
}

func TestHandleTenantUpdate_HasData(t *testing.T) {
	svc := &mockTenantService{
		updateFn: func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantHasData
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{Id: "acme", IsolationType: "prefix"})

	_, err := srv.handleTenantUpdate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantHasData) {
		t.Fatalf("expected ErrTenantHasData, got %v", err)
	}
}

func TestHandleTenantUpdate_UnmarshalError(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantUpdate(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantDelete
// ---------------------------------------------------------------------------

func TestHandleTenantDelete_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantDeleteRequest{Id: "acme"})

	_, err := srv.handleTenantDelete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantDelete_Success(t *testing.T) {
	called := false
	svc := &mockTenantService{
		deleteFn: func(_ context.Context, id string) error {
			called = true
			if id != "acme" {
				t.Errorf("expected id 'acme', got %q", id)
			}
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantDeleteRequest{Id: "acme"})

	result, err := srv.handleTenantDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected DeleteTenant to be called")
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestHandleTenantDelete_NotFound(t *testing.T) {
	svc := &mockTenantService{
		deleteFn: func(_ context.Context, _ string) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantDeleteRequest{Id: "missing"})

	_, err := srv.handleTenantDelete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestHandleTenantDelete_UnmarshalError(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantDelete(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdateQuotas
// ---------------------------------------------------------------------------

func TestHandleTenantUpdateQuotas_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: &pb.TenantQuotas{MaxDatabases: 5},
	})

	_, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantUpdateQuotas_NilQuotas(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{Id: "acme"})

	_, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var qe *tenantQuotaError
	if !errors.As(err, &qe) {
		t.Fatalf("expected *tenantQuotaError, got %T", err)
	}
}

func TestHandleTenantUpdateQuotas_Success(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:        id,
				Name:      "ACME Corp",
				Quotas:    q,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id: "acme",
		Quotas: &pb.TenantQuotas{
			MaxDatabases: 5,
			MaxKeys:      1000,
		},
	})
	resp, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", tenantResp.GetId())
	}
	if tenantResp.GetQuotas() == nil {
		t.Fatal("expected non-nil quotas in response")
	}
	if tenantResp.GetQuotas().GetMaxDatabases() != 5 {
		t.Errorf("expected MaxDatabases 5, got %d", tenantResp.GetQuotas().GetMaxDatabases())
	}
}

// TestHandleTenantUpdateQuotas_ExtendedFieldsRoundTrip is a regression test
// for the QUIC quota decode gap: SystemBarrierQuota, MaxDedicatedWALGroups,
// and MaxDatabaseSizeBytes were silently dropped when decoding
// pb.TenantQuotas into quota.Quotas, even though the gRPC/REST equivalents
// carried the full field set.
func TestHandleTenantUpdateQuotas_ExtendedFieldsRoundTrip(t *testing.T) {
	var captured *quota.Quotas
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
			captured = q
			return &tenant.TenantInfo{ID: id, Quotas: q, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id: "acme",
		Quotas: &pb.TenantQuotas{
			MaxDatabaseSizeBytes:  1 << 30,
			SystemBarrierQuota:    2048,
			MaxDedicatedWalGroups: 4,
		},
	})
	resp, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured == nil {
		t.Fatal("expected UpdateQuotas to be called with non-nil quotas")
	}
	if captured.MaxDatabaseSizeBytes != 1<<30 {
		t.Errorf("expected MaxDatabaseSizeBytes %d, got %d", int64(1<<30), captured.MaxDatabaseSizeBytes)
	}
	if captured.SystemBarrierQuota != 2048 {
		t.Errorf("expected SystemBarrierQuota 2048, got %d", captured.SystemBarrierQuota)
	}
	if captured.MaxDedicatedWALGroups != 4 {
		t.Errorf("expected MaxDedicatedWALGroups 4, got %d", captured.MaxDedicatedWALGroups)
	}

	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetQuotas().GetSystemBarrierQuota() != 2048 {
		t.Errorf("expected response SystemBarrierQuota 2048, got %d", tenantResp.GetQuotas().GetSystemBarrierQuota())
	}
	if tenantResp.GetQuotas().GetMaxDedicatedWalGroups() != 4 {
		t.Errorf("expected response MaxDedicatedWalGroups 4, got %d", tenantResp.GetQuotas().GetMaxDedicatedWalGroups())
	}
}

func TestHandleTenantUpdateQuotas_NotFound(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id:     "missing",
		Quotas: &pb.TenantQuotas{MaxDatabases: 1},
	})

	_, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestHandleTenantUpdateQuotas_UnmarshalError(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantUpdateQuotas(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// tenantInfoToProto (unexported converter)
// ---------------------------------------------------------------------------

func TestTenantInfoToProto_NilInfo(t *testing.T) {
	if got := tenantInfoToProto(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestTenantInfoToProto_WithQuotas(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &tenant.TenantInfo{
		ID:            "t1",
		Name:          "Test Corp",
		IsolationType: tenant.IsolationBarrier,
		Quotas: &quota.Quotas{
			MaxDatabases:    3,
			MaxRaftGroups:   6,
			MaxCollections:  9,
			MaxKeys:         1000,
			MaxStorageBytes: 1048576,
			MaxUsers:        50,
			MaxRoles:        10,
			MaxCertificates: 20,
			MaxBackups:      5,
			MaxVectorDims:   512,
			MaxTSMetrics:    100,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	got := tenantInfoToProto(info)
	if got == nil {
		t.Fatal("expected non-nil response")
	}
	if got.GetId() != "t1" {
		t.Errorf("expected id 't1', got %q", got.GetId())
	}
	if got.GetName() != "Test Corp" {
		t.Errorf("expected name 'Test Corp', got %q", got.GetName())
	}
	if got.GetIsolationType() != "barrier" {
		t.Errorf("expected isolation_type 'barrier', got %q", got.GetIsolationType())
	}
	if got.GetQuotas() == nil {
		t.Fatal("expected non-nil quotas")
	}
	if got.GetQuotas().GetMaxDatabases() != 3 {
		t.Errorf("expected MaxDatabases 3, got %d", got.GetQuotas().GetMaxDatabases())
	}
	if got.GetQuotas().GetMaxTsMetrics() != 100 {
		t.Errorf("expected MaxTsMetrics 100, got %d", got.GetQuotas().GetMaxTsMetrics())
	}
}

// TestTenantInfoToProto_WithEntitlementsAndExtendedQuotas is a regression
// test for the QUIC tenant response encoder dropping
// Entitlements.AllowedBarrierModes, Entitlements.AllowDedicatedWal,
// Quotas.SystemBarrierQuota, and Quotas.MaxDedicatedWalGroups. These fields
// are part of the gRPC/REST canon (see grpc.entitlementsToProto /
// grpc.quotaToProto) and must round-trip identically on QUIC.
func TestTenantInfoToProto_WithEntitlementsAndExtendedQuotas(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &tenant.TenantInfo{
		ID:            "t1",
		Name:          "Test Corp",
		IsolationType: tenant.IsolationBarrier,
		Quotas: &quota.Quotas{
			MaxDatabases:          3,
			SystemBarrierQuota:    4096,
			MaxDedicatedWALGroups: 2,
		},
		Entitlements: &quota.Entitlements{
			AllowedIsolationModes: []string{"barrier"},
			AllowDedicatedRaft:    true,
			AllowedBarrierModes:   []string{"none", "passphrase", "shamir"},
			AllowDedicatedWAL:     true,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	got := tenantInfoToProto(info)
	if got == nil {
		t.Fatal("expected non-nil response")
	}
	if got.GetQuotas().GetSystemBarrierQuota() != 4096 {
		t.Errorf("expected SystemBarrierQuota 4096, got %d", got.GetQuotas().GetSystemBarrierQuota())
	}
	if got.GetQuotas().GetMaxDedicatedWalGroups() != 2 {
		t.Errorf("expected MaxDedicatedWalGroups 2, got %d", got.GetQuotas().GetMaxDedicatedWalGroups())
	}
	if got.GetEntitlements() == nil {
		t.Fatal("expected non-nil entitlements")
	}
	gotModes := got.GetEntitlements().GetAllowedBarrierModes()
	wantModes := []string{"none", "passphrase", "shamir"}
	if len(gotModes) != len(wantModes) {
		t.Fatalf("expected AllowedBarrierModes %v, got %v", wantModes, gotModes)
	}
	for i := range wantModes {
		if gotModes[i] != wantModes[i] {
			t.Fatalf("expected AllowedBarrierModes %v, got %v", wantModes, gotModes)
		}
	}
	if !got.GetEntitlements().GetAllowDedicatedWal() {
		t.Error("expected AllowDedicatedWal true")
	}
}

func TestTenantInfoToProto_DefaultIsolationType(t *testing.T) {
	info := &tenant.TenantInfo{
		ID:        "t2",
		Name:      "No Explicit Isolation",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	got := tenantInfoToProto(info)
	if got.GetIsolationType() != "prefix" {
		t.Errorf("expected default isolation_type 'prefix', got %q", got.GetIsolationType())
	}
}

// ---------------------------------------------------------------------------
// tenantQuotaError
// ---------------------------------------------------------------------------

func TestTenantQuotaError_Message(t *testing.T) {
	e := &tenantQuotaError{}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}
