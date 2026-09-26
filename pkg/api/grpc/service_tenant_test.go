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

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// mockTenantService is a table-driven fake TenantServicer for gRPC tests.
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

func newMockTenantService() *mockTenantService {
	return &mockTenantService{}
}

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

func (m *mockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []tenant.TenantInfo{
		{ID: "t1", Name: "tenant-one", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
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
	return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, Initialized: false}, nil
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
// TenantCreate
// ---------------------------------------------------------------------------

func TestGRPC_TenantCreate_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{
		Id:   "acme",
		Name: "ACME Corp",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantCreate_Success(t *testing.T) {
	svc := newMockTenantService()
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	resp, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{
		Id:            "acme",
		Name:          "ACME Corp",
		IsolationType: "barrier",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", resp.GetId())
	}
	if resp.GetName() != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", resp.GetName())
	}
}

func TestGRPC_TenantCreate_AlreadyExists(t *testing.T) {
	svc := newMockTenantService()
	svc.createFn = func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantAlreadyExists
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{Id: "dup", Name: "Dup"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_TenantCreate_StoreError(t *testing.T) {
	svc := newMockTenantService()
	svc.createFn = func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
		return nil, &tenant.StoreError{Op: "put", Err: errors.New("disk full")}
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{Id: "t1", Name: "T1"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantGet
// ---------------------------------------------------------------------------

func TestGRPC_TenantGet_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantGet(context.Background(), &pb.TenantGetRequest{Id: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantGet_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.getFn = func(_ context.Context, id string) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:            id,
			Name:          "ACME Corp",
			IsolationType: tenant.IsolationBarrier,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	resp, err := client.TenantGet(context.Background(), &pb.TenantGetRequest{Id: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", resp.GetId())
	}
	if resp.GetName() != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", resp.GetName())
	}
}

func TestGRPC_TenantGet_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.getFn = func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantGet(context.Background(), &pb.TenantGetRequest{Id: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantList
// ---------------------------------------------------------------------------

func TestGRPC_TenantList_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantList_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.listFn = func(_ context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			{ID: "t1", Name: "Tenant One", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: "t2", Name: "Tenant Two", IsolationType: tenant.IsolationPrefix, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	resp, err := client.TenantList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetTenants()) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(resp.GetTenants()))
	}
	if resp.GetTenants()[0].GetId() != "t1" {
		t.Errorf("expected first tenant id 't1', got %q", resp.GetTenants()[0].GetId())
	}
}

func TestGRPC_TenantList_StoreError(t *testing.T) {
	svc := newMockTenantService()
	svc.listFn = func(_ context.Context) ([]tenant.TenantInfo, error) {
		return nil, &tenant.StoreError{Op: "list", Err: errors.New("io error")}
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantUpdate
// ---------------------------------------------------------------------------

func TestGRPC_TenantUpdate_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{Id: "acme", Name: "New Name"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantUpdate_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{ID: id, Name: p.Name, IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	resp, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{
		Id:   "acme",
		Name: "ACME Updated",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetName() != "ACME Updated" {
		t.Errorf("expected name 'ACME Updated', got %q", resp.GetName())
	}
}

func TestGRPC_TenantUpdate_HasData(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantHasData
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{
		Id:            "acme",
		IsolationType: "prefix",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestGRPC_TenantUpdate_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{Id: "missing", Name: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantDelete
// ---------------------------------------------------------------------------

func TestGRPC_TenantDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantDelete(context.Background(), &pb.TenantDeleteRequest{Id: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantDelete_Success(t *testing.T) {
	svc := newMockTenantService()
	called := false
	svc.deleteFn = func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			t.Errorf("expected id 'acme', got %q", id)
		}
		return nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantDelete(context.Background(), &pb.TenantDeleteRequest{Id: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected DeleteTenant to be called")
	}
}

func TestGRPC_TenantDelete_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.deleteFn = func(_ context.Context, _ string) error {
		return tenant.ErrTenantNotFound
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantDelete(context.Background(), &pb.TenantDeleteRequest{Id: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantUpdateQuotas
// ---------------------------------------------------------------------------

func TestGRPC_TenantUpdateQuotas_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: &pb.TenantQuotas{MaxDatabases: 10},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TenantUpdateQuotas_NilQuotas(t *testing.T) {
	svc := newMockTenantService()
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: nil,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TenantUpdateQuotas_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.updateQuotasFn = func(_ context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:        id,
			Name:      "ACME Corp",
			Quotas:    q,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	resp, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id: "acme",
		Quotas: &pb.TenantQuotas{
			MaxDatabases: 5,
			MaxKeys:      1000,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", resp.GetId())
	}
	if resp.GetQuotas() == nil {
		t.Fatal("expected non-nil quotas in response")
	}
	if resp.GetQuotas().GetMaxDatabases() != 5 {
		t.Errorf("expected MaxDatabases 5, got %d", resp.GetQuotas().GetMaxDatabases())
	}
}

func TestGRPC_TenantUpdateQuotas_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.updateQuotasFn = func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithTenantService(svc))
	defer cleanup()

	_, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id:     "missing",
		Quotas: &pb.TenantQuotas{MaxDatabases: 1},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// mapServiceError
// ---------------------------------------------------------------------------

func TestMapTenantError_SentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"empty id", tenant.ErrEmptyTenantID, codes.InvalidArgument},
		{"empty name", tenant.ErrEmptyTenantName, codes.InvalidArgument},
		{"empty share", tenant.ErrEmptyShare, codes.InvalidArgument},
		{"system tenant prefix", tenant.ErrSystemTenantMustBePrefix, codes.InvalidArgument},
		{"invalid strategy", &tenant.InvalidStrategyError{Strategy: "bogus"}, codes.InvalidArgument},
		{"tenant scope denied", &tenant.TenantScopeError{CallerTenantID: "a", TargetTenantID: "b", Op: "seal"}, codes.PermissionDenied},
		{"nil quotas", tenant.ErrNilQuotas, codes.InvalidArgument},
		{"barrier isolation unavailable", tenant.ErrBarrierIsolationUnavailable, codes.FailedPrecondition},
		{"barrier not enabled", tenant.ErrBarrierNotEnabled, codes.FailedPrecondition},
		{"already exists", tenant.ErrTenantAlreadyExists, codes.AlreadyExists},
		{"has data", tenant.ErrTenantHasData, codes.FailedPrecondition},
		{"not found", tenant.ErrTenantNotFound, codes.NotFound},
		{"invalid id", &tenant.InvalidTenantIDError{TenantID: "bad!", Reason: "special chars"}, codes.InvalidArgument},
		{"store error", &tenant.StoreError{Op: "get", Err: errors.New("io")}, codes.Internal},
		{"barrier error", &tenant.BarrierError{TenantID: "t1", Op: "init", Err: errors.New("key missing")}, codes.Internal},
		{"unknown error", errors.New("something else"), codes.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServiceError(tc.err)
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T", got)
			}
			if st.Code() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, st.Code())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// tenantToProto / quotaToProto helpers
// ---------------------------------------------------------------------------

func TestTenantToProto_NilInfo(t *testing.T) {
	if got := tenantToProto(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestTenantToProto_WithQuotas(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &tenant.TenantInfo{
		ID:            "t1",
		Name:          "Test",
		IsolationType: tenant.IsolationBarrier,
		Quotas:        &quota.Quotas{MaxDatabases: 10, MaxKeys: 500},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	got := tenantToProto(info)
	if got == nil {
		t.Fatal("expected non-nil response")
	}
	if got.GetId() != "t1" {
		t.Errorf("expected id 't1', got %q", got.GetId())
	}
	if got.GetName() != "Test" {
		t.Errorf("expected name 'Test', got %q", got.GetName())
	}
	if got.GetIsolationType() != "barrier" {
		t.Errorf("expected isolation_type 'barrier', got %q", got.GetIsolationType())
	}
	if got.GetQuotas() == nil {
		t.Fatal("expected non-nil quotas")
	}
	if got.GetQuotas().GetMaxDatabases() != 10 {
		t.Errorf("expected MaxDatabases 10, got %d", got.GetQuotas().GetMaxDatabases())
	}
}

func TestTenantToProto_DefaultIsolationType(t *testing.T) {
	info := &tenant.TenantInfo{
		ID:        "t2",
		Name:      "No Isolation",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	got := tenantToProto(info)
	if got.GetIsolationType() != "prefix" {
		t.Errorf("expected default isolation_type 'prefix', got %q", got.GetIsolationType())
	}
}

func TestQuotaToProto_Nil(t *testing.T) {
	if got := quotaToProto(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestQuotaToProto_AllFields(t *testing.T) {
	q := &quota.Quotas{
		MaxDatabases:    1,
		MaxRaftGroups:   2,
		MaxCollections:  3,
		MaxKeys:         4,
		MaxStorageBytes: 5,
		MaxUsers:        6,
		MaxRoles:        7,
		MaxCertificates: 8,
		MaxBackups:      9,
		MaxVectorDims:   10,
		MaxTSMetrics:    11,
	}
	got := quotaToProto(q)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.GetMaxDatabases() != 1 {
		t.Errorf("MaxDatabases: expected 1, got %d", got.GetMaxDatabases())
	}
	if got.GetMaxTsMetrics() != 11 {
		t.Errorf("MaxTsMetrics: expected 11, got %d", got.GetMaxTsMetrics())
	}
}

func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
