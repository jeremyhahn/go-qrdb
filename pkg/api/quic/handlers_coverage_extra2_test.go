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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// mockTenantSvc — local version without extra build tags
// ---------------------------------------------------------------------------

type mockTenantSvc struct {
	tenant.TenantServicer
	createFn        func(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error)
	getFn           func(ctx context.Context, id string) (*tenant.TenantInfo, error)
	listFn          func(ctx context.Context) ([]tenant.TenantInfo, error)
	updateFn        func(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error)
	deleteFn        func(ctx context.Context, id string) error
	updateQuotasFn  func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error)
	barrierInitFn   func(ctx context.Context, id string, passphrase []byte, threshold, total int) error
	barrierUnsealFn func(ctx context.Context, id string, share []byte) error
	barrierSealFn   func(ctx context.Context, id string) error
	barrierStatusFn func(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error)
}

var _ tenant.TenantServicer = (*mockTenantSvc)(nil)

func (m *mockTenantSvc) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, iso)
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantSvc) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &tenant.TenantInfo{ID: id, Name: "test", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantSvc) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *mockTenantSvc) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
}

func (m *mockTenantSvc) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []tenant.TenantInfo{
		{ID: "t1", Name: "Tenant One", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}, nil
}

func (m *mockTenantSvc) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, p)
	}
	return &tenant.TenantInfo{ID: id, Name: p.Name, IsolationType: p.IsolationType, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantSvc) DeleteTenant(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockTenantSvc) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasFn != nil {
		return m.updateQuotasFn(ctx, id, q)
	}
	return &tenant.TenantInfo{ID: id, Name: "test", Quotas: q, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantSvc) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantSvc) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return &quota.Quotas{}, nil
}

func (m *mockTenantSvc) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	if m.barrierInitFn != nil {
		return nil, m.barrierInitFn(ctx, id, passphrase, threshold, total)
	}
	return nil, nil
}

func (m *mockTenantSvc) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}

func (m *mockTenantSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantSvc) TenantBarrierUnseal(ctx context.Context, id string, share []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, id, share)
	}
	return nil
}

func (m *mockTenantSvc) TenantBarrierSeal(ctx context.Context, id string) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx, id)
	}
	return nil
}

func (m *mockTenantSvc) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx, id)
	}
	return &tenant.TenantBarrierStatus{TenantID: id, Sealed: false, Initialized: true, Strategy: "passphrase"}, nil
}
func (m *mockTenantSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantSvc) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

func newTenantServer() *Server {
	svc := &mockTenantSvc{}
	return newTestServer(&mockKVService{}, &mockAdminService{},
		WithTenantService(svc), WithTenantOnboarding(&fakeTenantOnboarding{tenants: svc}))
}

// ---------------------------------------------------------------------------
// handleTenantCreate
// ---------------------------------------------------------------------------

func TestHandleTenantCreate_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantCreateRequest{Id: "acme", Name: "ACME"})
	_, err := srv.handleTenantCreate(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantCreate_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := proto.Marshal(&pb.TenantCreateRequest{Id: "acme", Name: "ACME Corp", IsolationType: "barrier"})
	resp, err := srv.handleTenantCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TenantCreateResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetTenant().GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", out.GetTenant().GetId())
	}
}

func TestHandleTenantCreate_ServiceError_Extra(t *testing.T) {
	svcErr := tenant.ErrTenantAlreadyExists
	svc := &mockTenantSvc{
		createFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithTenantService(svc), WithTenantOnboarding(&fakeTenantOnboarding{tenants: svc}))
	payload, _ := proto.Marshal(&pb.TenantCreateRequest{Id: "dup", Name: "Dup"})
	_, err := srv.handleTenantCreate(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestHandleTenantCreate_UnmarshalError_Extra(t *testing.T) {
	srv := newTenantServer()
	_, err := srv.handleTenantCreate(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantGet
// ---------------------------------------------------------------------------

func TestHandleTenantGet_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "acme"})
	_, err := srv.handleTenantGet(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantGet_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "acme"})
	resp, err := srv.handleTenantGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TenantResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", out.GetId())
	}
}

func TestHandleTenantGet_NotFound_Extra(t *testing.T) {
	svc := &mockTenantSvc{getFn: func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantGetRequest{Id: "missing"})
	_, err := srv.handleTenantGet(context.Background(), payload)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestHandleTenantGet_UnmarshalError_Extra(t *testing.T) {
	srv := newTenantServer()
	_, err := srv.handleTenantGet(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantList
// ---------------------------------------------------------------------------

func TestHandleTenantList_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleTenantList(context.Background(), nil)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantList_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	resp, err := srv.handleTenantList(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TenantListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetTenants()) != 1 {
		t.Errorf("expected 1 tenant, got %d", len(out.GetTenants()))
	}
}

func TestHandleTenantList_ServiceError_Extra(t *testing.T) {
	svcErr := errors.New("list failed")
	svc := &mockTenantSvc{listFn: func(_ context.Context) ([]tenant.TenantInfo, error) { return nil, svcErr }}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	_, err := srv.handleTenantList(context.Background(), nil)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdate
// ---------------------------------------------------------------------------

func TestHandleTenantUpdate_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{Id: "acme", Name: "New"})
	_, err := srv.handleTenantUpdate(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantUpdate_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{Id: "acme", Name: "ACME Updated"})
	resp, err := srv.handleTenantUpdate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TenantResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetName() != "ACME Updated" {
		t.Errorf("expected name 'ACME Updated', got %q", out.GetName())
	}
}

func TestHandleTenantUpdate_ServiceError_Extra(t *testing.T) {
	svc := &mockTenantSvc{updateFn: func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantHasData
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantUpdateRequest{Id: "acme", IsolationType: "prefix"})
	_, err := srv.handleTenantUpdate(context.Background(), payload)
	if !errors.Is(err, tenant.ErrTenantHasData) {
		t.Fatalf("expected ErrTenantHasData, got %v", err)
	}
}

func TestHandleTenantUpdate_UnmarshalError_Extra(t *testing.T) {
	srv := newTenantServer()
	_, err := srv.handleTenantUpdate(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantDelete
// ---------------------------------------------------------------------------

func TestHandleTenantDelete_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantDeleteRequest{Id: "acme"})
	_, err := srv.handleTenantDelete(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantDelete_Success_Extra(t *testing.T) {
	called := false
	svc := &mockTenantSvc{deleteFn: func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			return errors.New("wrong id")
		}
		return nil
	}}
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

func TestHandleTenantDelete_NotFound_Extra(t *testing.T) {
	svc := &mockTenantSvc{deleteFn: func(_ context.Context, _ string) error { return tenant.ErrTenantNotFound }}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := proto.Marshal(&pb.TenantDeleteRequest{Id: "missing"})
	_, err := srv.handleTenantDelete(context.Background(), payload)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestHandleTenantDelete_UnmarshalError_Extra(t *testing.T) {
	srv := newTenantServer()
	_, err := srv.handleTenantDelete(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdateQuotas
// ---------------------------------------------------------------------------

func TestHandleTenantUpdateQuotas_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{Id: "acme", Quotas: &pb.TenantQuotas{MaxDatabases: 5}})
	_, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantUpdateQuotas_NilQuotas_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{Id: "acme"})
	_, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	var qe *tenantQuotaError
	if !errors.As(err, &qe) {
		t.Fatalf("expected *tenantQuotaError, got %T", err)
	}
}

func TestHandleTenantUpdateQuotas_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: &pb.TenantQuotas{MaxDatabases: 5, MaxKeys: 1000},
	})
	resp, err := srv.handleTenantUpdateQuotas(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TenantResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", out.GetId())
	}
}

func TestHandleTenantUpdateQuotas_UnmarshalError_Extra(t *testing.T) {
	srv := newTenantServer()
	_, err := srv.handleTenantUpdateQuotas(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// tenantInfoToProto
// ---------------------------------------------------------------------------

func TestTenantInfoToProto_Nil_Extra(t *testing.T) {
	if got := tenantInfoToProto(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTenantInfoToProto_WithQuotas_Extra(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &tenant.TenantInfo{
		ID:            "t1",
		Name:          "Test Corp",
		IsolationType: tenant.IsolationBarrier,
		Quotas: &quota.Quotas{
			MaxDatabases: 3,
			MaxKeys:      1000,
			MaxTSMetrics: 50,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	got := tenantInfoToProto(info)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.GetId() != "t1" {
		t.Errorf("expected 't1', got %q", got.GetId())
	}
	if got.GetQuotas() == nil {
		t.Fatal("expected non-nil quotas")
	}
	if got.GetQuotas().GetMaxDatabases() != 3 {
		t.Errorf("expected MaxDatabases 3, got %d", got.GetQuotas().GetMaxDatabases())
	}
}

func TestTenantQuotaError_Message_Extra(t *testing.T) {
	e := &tenantQuotaError{}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_MissingTenantID_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{})
	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_Success_Extra(t *testing.T) {
	called := false
	svc := &mockTenantSvc{barrierInitFn: func(_ context.Context, id string, _ []byte, _, _ int) error {
		called = true
		if id != "acme" {
			return errors.New("wrong id")
		}
		return nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Passphrase: []byte("secret"), Threshold: 2, Shares: 3})
	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected TenantBarrierInit to be called")
	}
}

func TestHandleTenantBarrierInit_ServiceError_Extra(t *testing.T) {
	svc := &mockTenantSvc{barrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return tenant.ErrTenantNotFound
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "missing"})
	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_MissingTenantID_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{Key: []byte("k")})
	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_Success_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("key")})
	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_MissingTenantID_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{})
	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_Success_Extra(t *testing.T) {
	called := false
	svc := &mockTenantSvc{barrierSealFn: func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			return errors.New("wrong id")
		}
		return nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected TenantBarrierSeal to be called")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_MissingTenantID_Extra(t *testing.T) {
	srv := newTenantServer()
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{})
	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_Success_Extra(t *testing.T) {
	svc := &mockTenantSvc{barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{TenantID: id, Sealed: false, Initialized: true, Strategy: "shamir"}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})
	resp, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got quicTenantBarrierStatusResponse
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", got.TenantID)
	}
	if got.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", got.Strategy)
	}
}

func TestHandleTenantBarrierStatus_ServiceError_Extra(t *testing.T) {
	svc := &mockTenantSvc{barrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		return nil, tenant.ErrTenantNotFound
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "missing"})
	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestMissingTenantIDError_Message_Extra(t *testing.T) {
	e := &missingTenantIDError{}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// handleIndexList (was in quic_coverage_test.go but that needs extra tags)
// ---------------------------------------------------------------------------

func TestHandleIndexList_Success_Extra(t *testing.T) {
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return []string{"email_idx", "name_idx"}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	resp, err := srv.handleIndexList(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.IndexListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetNames()) != 2 {
		t.Errorf("expected 2 names, got %d", len(out.GetNames()))
	}
}

func TestHandleIndexList_ServiceError_Extra(t *testing.T) {
	svcErr := errors.New("list indexes failed")
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	_, err := srv.handleIndexList(context.Background(), nil)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

func TestHandleIndexList_NilService_Extra(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleIndexList(context.Background(), nil)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Server option coverage: WithPINBackend, WithTenantService (already through
// newTenantServer), WithAuthFactorService, WithUserAuthFactorService,
// WithAuditLogger, WithKVService, WithAdminService
// ---------------------------------------------------------------------------

// mockPINBackend is a minimal pin.PINBackend for coverage.
type mockPINBackend struct{}

var _ pin.PINBackend = (*mockPINBackend)(nil)

func (m *mockPINBackend) Strategy() pin.StrategyID             { return pin.StrategySoftware }
func (m *mockPINBackend) SetSOPIN(_, _ string) error           { return nil }
func (m *mockPINBackend) SetUserPIN(_, _ string) error         { return nil }
func (m *mockPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (m *mockPINBackend) ChangeUserPIN(_, _ string) error      { return nil }
func (m *mockPINBackend) VerifySOPIN(_ string) error           { return nil }
func (m *mockPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (m *mockPINBackend) SOPINSet() bool                       { return false }
func (m *mockPINBackend) UserPINSet() bool                     { return false }
func (m *mockPINBackend) ClearPINs() error                     { return nil }
func (m *mockPINBackend) IsInitialized() bool                  { return true }
func (m *mockPINBackend) GetLockoutStatus() *pin.LockoutStatus { return &pin.LockoutStatus{} }
func (m *mockPINBackend) ResetLockout(_ string) error          { return nil }

func TestWithPINBackend_SetsField(t *testing.T) {
	backend := &mockPINBackend{}
	srv := NewServer(nil, nil, "localhost:0", WithPINBackend(backend))
	if srv.pinBackend == nil {
		t.Error("expected pinBackend to be set")
	}
}

func TestWithKVService_SetsField(t *testing.T) {
	kv := &mockKVService{}
	srv := NewServer(nil, nil, "localhost:0", WithKVService(kv))
	if srv.kvSvc == nil {
		t.Error("expected kvSvc to be set")
	}
}

func TestWithAdminService_SetsField(t *testing.T) {
	admin := &mockAdminService{}
	srv := NewServer(nil, nil, "localhost:0", WithAdminService(admin))
	if srv.adminSvc == nil {
		t.Error("expected adminSvc to be set")
	}
}

// ---------------------------------------------------------------------------
// mockAuthFactorSvc for WithAuthFactorService coverage
// ---------------------------------------------------------------------------

type mockAuthFactorSvc struct{}

var _ service.AuthFactorServicer = (*mockAuthFactorSvc)(nil)

func (m *mockAuthFactorSvc) EnrollFactor(_ context.Context, _ string, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return nil, errors.New("not implemented")
}

func (m *mockAuthFactorSvc) ChallengeFactor(_ context.Context, _ string) (*authfactors.Challenge, error) {
	return nil, errors.New("not implemented")
}

func (m *mockAuthFactorSvc) VerifyFactor(_ context.Context, _ string, _ string, _ *authfactors.Response) error {
	return nil
}

func (m *mockAuthFactorSvc) ListFactors(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
	return nil, nil
}

func (m *mockAuthFactorSvc) RevokeFactor(_ context.Context, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// mockUserAuthFactorSvc for WithUserAuthFactorService coverage
// ---------------------------------------------------------------------------

type mockUserAuthFactorSvc struct{}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorSvc)(nil)

func (m *mockUserAuthFactorSvc) Add(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
	return nil
}
func (m *mockUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) Remove(_ context.Context, _ string, _ bool) error { return nil }
func (m *mockUserAuthFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return nil
}
func (m *mockUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error     { return nil }
func (m *mockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error { return nil }

func TestWithUserAuthFactorService_SetsField(t *testing.T) {
	svc := &mockUserAuthFactorSvc{}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	if srv.userAuthFactorSvc == nil {
		t.Error("expected userAuthFactorSvc to be set")
	}
}

// ---------------------------------------------------------------------------
// handleUserAuthFactorAdd — NilService covers the service dispatch path
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorAdd_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorAddRequest{UserID: 1, Type: "totp", Credential: []byte("cred")})
	_, err := srv.handleUserAuthFactorAdd(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorAdd_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorAdd(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorAdd_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorAddRequest{UserID: 1, Type: "totp", Credential: []byte("cred")})
	resp, err := srv.handleUserAuthFactorAdd(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorList_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorListRequest{UserID: 1})
	_, err := srv.handleUserAuthFactorList(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorList_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorListRequest{UserID: 1})
	resp, err := srv.handleUserAuthFactorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorGet_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorGetRequest{FactorID: "f1"})
	_, err := srv.handleUserAuthFactorGet(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorGet_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorGetRequest{FactorID: "f1"})
	resp, err := srv.handleUserAuthFactorGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorRemove_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRemoveRequest{FactorID: "f1"})
	_, err := srv.handleUserAuthFactorRemove(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRemove_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorRemoveRequest{FactorID: "f1"})
	resp, err := srv.handleUserAuthFactorRemove(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorRotate_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRotateRequest{OldFactorID: "old"})
	_, err := srv.handleUserAuthFactorRotate(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRotate_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorRotateRequest{
		OldFactorID: "old",
		NewFactor:   quicUserFactorAddRequest{UserID: 1, Type: "totp", Credential: []byte("cred")},
	})
	resp, err := srv.handleUserAuthFactorRotate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorRelabel_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRelabelRequest{FactorID: "f1", Label: "new-label"})
	_, err := srv.handleUserAuthFactorRelabel(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRelabel_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	payload, _ := json.Marshal(quicUserFactorRelabelRequest{FactorID: "f1", Label: "new-label"})
	resp, err := srv.handleUserAuthFactorRelabel(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleUserAuthFactorLogin_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorLoginRequest{Type: "totp", LookupKey: []byte("key")})
	_, err := srv.handleUserAuthFactorLogin(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorLogin_ServiceError(t *testing.T) {
	// Test the service-error path via a custom service that returns an error.
	svcErr := errors.New("lookup failed")
	svc := &loginErrUserAuthFactorSvc{err: svcErr}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorLoginRequest{Type: "mtls", LookupKey: []byte("k")})
	_, err := srv.handleUserAuthFactorLogin(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

// loginErrUserAuthFactorSvc returns an error from LookupByCredentialKey.
type loginErrUserAuthFactorSvc struct {
	mockUserAuthFactorSvc
	err error
}

func (s *loginErrUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return nil, s.err
}

// ---------------------------------------------------------------------------
// userFactorToQUIC / userFactorFromQUIC helpers
// ---------------------------------------------------------------------------

func TestUserFactorToQUIC_Nil(t *testing.T) {
	got := userFactorToQUIC(nil)
	if got.ID != "" {
		t.Errorf("expected empty ID, got %q", got.ID)
	}
}

func TestUserFactorToQUIC_WithLastUsedAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &auth.AuthFactor{
		ID:           "f1",
		UserID:       42,
		Type:         auth.AuthFactorMTLS,
		Label:        "My mTLS",
		Credential:   []byte("secret"),
		RegisteredAt: now,
		LastUsedAt:   &now,
	}
	got := userFactorToQUIC(f)
	if got.ID != "f1" {
		t.Errorf("expected 'f1', got %q", got.ID)
	}
	if got.LastUsedAt == "" {
		t.Error("expected non-empty LastUsedAt")
	}
	if got.RevokedAt != "" {
		t.Errorf("expected empty RevokedAt, got %q", got.RevokedAt)
	}
}

func TestUserFactorToQUIC_WithRevokedAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &auth.AuthFactor{
		ID:           "f2",
		RegisteredAt: now,
		RevokedAt:    &now,
	}
	got := userFactorToQUIC(f)
	if got.RevokedAt == "" {
		t.Error("expected non-empty RevokedAt")
	}
}

func TestUserFactorFromQUIC_Fields(t *testing.T) {
	req := &quicUserFactorAddRequest{
		UserID:     10,
		Type:       "totp",
		Label:      "test",
		Credential: []byte("secret"),
		Metadata:   map[string]string{"k": "v"},
		CreatedBy:  "admin",
	}
	got := userFactorFromQUIC(req)
	if got.UserID != 10 {
		t.Errorf("expected UserID=10, got %d", got.UserID)
	}
	if string(got.Type) != "totp" {
		t.Errorf("expected type 'totp', got %q", got.Type)
	}
}

func (m *mockTenantSvc) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
