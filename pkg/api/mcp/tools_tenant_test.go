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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ---------------------------------------------------------------------------
// mockTenantService is a table-driven fake TenantServicer for MCP tests.
// ---------------------------------------------------------------------------

type mockTenantService struct {
	tenant.TenantServicer
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

// UndoCreateTenant undoes a create the way this double deletes one.
func (m *mockTenantService) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return m.DeleteTenant(ctx, tenantID)
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
		if err := m.barrierInitFn(ctx, id, passphrase, threshold, total); err != nil {
			return nil, err
		}
	}
	return &tenant.TenantBarrierInitResult{TenantID: id, Strategy: string(seal.StrategyPassphrase)}, nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster, _ ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}

func (m *mockTenantService) TenantBarrierInitBegin(context.Context, *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	return nil, tenant.ErrBarrierNotEnabled
}

func (m *mockTenantService) TenantBarrierInitComplete(context.Context, *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	return nil, tenant.ErrBarrierNotEnabled
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

// ---------------------------------------------------------------------------
// handleTenantCreate
// ---------------------------------------------------------------------------

func TestHandleTenantCreate_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantCreate(&fakeTenantOnboarding{tenants: svc})

	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"name": "ACME Corp",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleTenantCreate_EmptyName(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantCreate(&fakeTenantOnboarding{tenants: svc})

	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleTenantCreate_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.createFn = func(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:            id,
			Name:          name,
			IsolationType: iso,
			CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	}
	handler := handleTenantCreate(&fakeTenantOnboarding{tenants: svc})

	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"id":             "acme",
		"name":           "ACME Corp",
		"isolation_type": "barrier",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}

	text := resultText(t, result)
	var out tenantCreateResult
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out.Tenant.ID != "acme" {
		t.Errorf("expected id 'acme', got %q", out.Tenant.ID)
	}
	if out.Tenant.Name != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", out.Tenant.Name)
	}
}

func TestHandleTenantCreate_AlreadyExists(t *testing.T) {
	svc := newMockTenantService()
	svc.createFn = func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantAlreadyExists
	}
	handler := handleTenantCreate(&fakeTenantOnboarding{tenants: svc})

	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"id":   "dup",
		"name": "Dup Corp",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_tenant_create" {
		t.Errorf("expected tool 'qrdb_tenant_create', got %q", te.Tool)
	}
}

func TestHandleTenantCreate_StoreError(t *testing.T) {
	svc := newMockTenantService()
	svc.createFn = func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
		return nil, &tenant.StoreError{Op: "put", Err: errors.New("disk full")}
	}
	handler := handleTenantCreate(&fakeTenantOnboarding{tenants: svc})

	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"id":   "t1",
		"name": "T1",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantGet
// ---------------------------------------------------------------------------

func TestHandleTenantGet_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantGet(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleTenantGet_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.getFn = func(_ context.Context, id string) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:            id,
			Name:          "ACME Corp",
			IsolationType: tenant.IsolationBarrier,
			CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	}
	handler := handleTenantGet(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var info tenant.TenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.ID != "acme" {
		t.Errorf("expected id 'acme', got %q", info.ID)
	}
	if info.Name != "ACME Corp" {
		t.Errorf("expected name 'ACME Corp', got %q", info.Name)
	}
}

// TestHandleTenantGet_CarriesRefusal proves qrdb_tenant_get's wire output
// carries a tenant's refusal. tenant.TenantInfo.Refusal is tagged
// json:"-" (an error has no fixed marshal shape), so json.Marshal(info)
// alone drops it -- tenantInfoToJSON rebuilds it from tenant.RefusalOf,
// the same way pkg/api/rest/handlers_tenants.go's tenantToResponse does
// for REST.
func TestHandleTenantGet_CarriesRefusal(t *testing.T) {
	svc := newMockTenantService()
	svc.getFn = func(_ context.Context, id string) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:         id,
			Name:       "bad",
			VolumePool: "broken",
			Refusal:    &volume.PoolUnavailableError{Pool: "broken", NodeID: "1"},
		}, nil
	}
	handler := handleTenantGet(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "bad"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var wire struct {
		Refusal *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"refusal"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.Refusal == nil {
		t.Fatalf("wire output dropped the refusal: %s", text)
	}
	if wire.Refusal.Code != "pool_unavailable" {
		t.Errorf("refusal.code = %q, want %q", wire.Refusal.Code, "pool_unavailable")
	}
}

func TestHandleTenantGet_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.getFn = func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantGet(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantList
// ---------------------------------------------------------------------------

func TestHandleTenantList_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.listFn = func(_ context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			{ID: "t1", Name: "Tenant One", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: "t2", Name: "Tenant Two", IsolationType: tenant.IsolationPrefix, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		}, nil
	}
	handler := handleTenantList(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var tenants []tenant.TenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &tenants); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(tenants))
	}
	if tenants[0].ID != "t1" {
		t.Errorf("expected first id 't1', got %q", tenants[0].ID)
	}
}

func TestHandleTenantList_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.listFn = func(_ context.Context) ([]tenant.TenantInfo, error) {
		return nil, &tenant.StoreError{Op: "list", Err: errors.New("io error")}
	}
	handler := handleTenantList(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdate
// ---------------------------------------------------------------------------

func TestHandleTenantUpdate_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantUpdate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{"name": "New Name"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleTenantUpdate_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:            id,
			Name:          p.Name,
			IsolationType: tenant.IsolationBarrier,
			CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	}
	handler := handleTenantUpdate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{
		"id":   "acme",
		"name": "ACME Updated",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var info tenant.TenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "ACME Updated" {
		t.Errorf("expected name 'ACME Updated', got %q", info.Name)
	}
}

func TestHandleTenantUpdate_HasData(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantHasData
	}
	handler := handleTenantUpdate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{
		"id":             "acme",
		"isolation_type": "prefix",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleTenantUpdate_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.updateFn = func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantUpdate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{
		"id":   "missing",
		"name": "x",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantDelete
// ---------------------------------------------------------------------------

func TestHandleTenantDelete_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantDelete(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleTenantDelete_Success(t *testing.T) {
	called := false
	svc := newMockTenantService()
	svc.deleteFn = func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			t.Errorf("expected id 'acme', got %q", id)
		}
		return nil
	}
	handler := handleTenantDelete(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{"id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	if !called {
		t.Error("expected DeleteTenant to be called")
	}

	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty response")
	}
}

func TestHandleTenantDelete_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.deleteFn = func(_ context.Context, _ string) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantDelete(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{"id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdateQuotas
// ---------------------------------------------------------------------------

func TestHandleTenantUpdateQuotas_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantUpdateQuotas(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{
		"max_databases": 5,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleTenantUpdateQuotas_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.updateQuotasFn = func(_ context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
		return &tenant.TenantInfo{
			ID:        id,
			Name:      "ACME Corp",
			Quotas:    q,
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	}
	handler := handleTenantUpdateQuotas(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{
		"id":            "acme",
		"max_databases": float64(5),
		"max_keys":      float64(1000),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var info tenant.TenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.ID != "acme" {
		t.Errorf("expected id 'acme', got %q", info.ID)
	}
}

func TestHandleTenantUpdateQuotas_NotFound(t *testing.T) {
	svc := newMockTenantService()
	svc.updateQuotasFn = func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantUpdateQuotas(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{
		"id":            "missing",
		"max_databases": float64(1),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleTenantUpdateQuotas_StoreError(t *testing.T) {
	svc := newMockTenantService()
	svc.updateQuotasFn = func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
		return nil, &tenant.StoreError{Op: "update", Err: errors.New("io error")}
	}
	handler := handleTenantUpdateQuotas(svc, nil)

	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{
		"id": "acme",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// tenantTools registration
// ---------------------------------------------------------------------------

func TestWithTenantService_ToolsRegistered(t *testing.T) {
	svc := newMockTenantService()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithTenantService(svc))

	if s.tenantSvc == nil {
		t.Fatal("expected tenantSvc to be set on server")
	}

	tools := s.MCPServer().ListTools()
	expected := []string{
		"qrdb_tenant_create",
		"qrdb_tenant_get",
		"qrdb_tenant_list",
		"qrdb_tenant_update",
		"qrdb_tenant_delete",
		"qrdb_tenant_update_quotas",
		"qrdb_tenant_barrier_init_begin",
		"qrdb_tenant_barrier_init_complete",
		"qrdb_tenant_barrier_unseal",
		"qrdb_tenant_barrier_seal",
		"qrdb_tenant_barrier_status",
	}
	for _, name := range expected {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}
