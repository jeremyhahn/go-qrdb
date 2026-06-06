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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// errSentinel is a generic error used in table tests to trigger error branches.
var errSentinel = errors.New("sentinel error")

// ---------------------------------------------------------------------------
// Mock: TenantServicer
// ---------------------------------------------------------------------------

type mockTenantService struct {
	createTenantFn        func(ctx context.Context, id, name string, isolationType tenant.IsolationType) (*tenant.TenantInfo, error)
	getTenantFn           func(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
	listTenantsFn         func(ctx context.Context) ([]tenant.TenantInfo, error)
	deleteTenantFn        func(ctx context.Context, tenantID string) error
	updateTenantFn        func(ctx context.Context, tenantID string, patch tenant.TenantPatch) (*tenant.TenantInfo, error)
	updateQuotasFn        func(ctx context.Context, tenantID string, quotas *quota.Quotas) (*tenant.TenantInfo, error)
	tenantBarrierInitFn   func(ctx context.Context, tenantID, passphrase string, threshold, shares int) error
	tenantBarrierUnsealFn func(ctx context.Context, tenantID string, key []byte) error
	tenantBarrierSealFn   func(ctx context.Context, tenantID string) error
	tenantBarrierStatusFn func(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error)
}

func (m *mockTenantService) CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType) (*tenant.TenantInfo, error) {
	if m.createTenantFn != nil {
		return m.createTenantFn(ctx, id, name, isolationType)
	}
	return nil, nil
}

func (m *mockTenantService) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if m.getTenantFn != nil {
		return m.getTenantFn(ctx, tenantID)
	}
	return nil, nil
}

func (m *mockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listTenantsFn != nil {
		return m.listTenantsFn(ctx)
	}
	return nil, nil
}

func (m *mockTenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	if m.deleteTenantFn != nil {
		return m.deleteTenantFn(ctx, tenantID)
	}
	return nil
}

func (m *mockTenantService) UpdateTenant(ctx context.Context, tenantID string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateTenantFn != nil {
		return m.updateTenantFn(ctx, tenantID, patch)
	}
	return nil, nil
}

func (m *mockTenantService) UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasFn != nil {
		return m.updateQuotasFn(ctx, tenantID, quotas)
	}
	return nil, nil
}

func (m *mockTenantService) TenantBarrierInit(ctx context.Context, tenantID, passphrase string, threshold, shares int) error {
	if m.tenantBarrierInitFn != nil {
		return m.tenantBarrierInitFn(ctx, tenantID, passphrase, threshold, shares)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, tenantID, passphrase string, threshold, shares int, _ seal.StrategyID) error {
	return m.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}

func (m *mockTenantService) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	if m.tenantBarrierUnsealFn != nil {
		return m.tenantBarrierUnsealFn(ctx, tenantID, key)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	if m.tenantBarrierSealFn != nil {
		return m.tenantBarrierSealFn(ctx, tenantID)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	if m.tenantBarrierStatusFn != nil {
		return m.tenantBarrierStatusFn(ctx, tenantID)
	}
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

// Compile-time interface check.
var _ tenant.TenantServicer = (*mockTenantService)(nil)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var (
	tenantTime    = time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	tenantTimeStr = tenantTime.Format(time.RFC3339)
)

func testTenantInfo() *tenant.TenantInfo {
	return &tenant.TenantInfo{
		ID:        "acme",
		Name:      "Acme Corp",
		Quotas:    nil,
		CreatedAt: tenantTime,
		UpdatedAt: tenantTime,
	}
}

func newTenantServer(svc tenant.TenantServicer) *Server {
	var opts []ServerOption
	if svc != nil {
		opts = append(opts, WithTenantService(svc))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

// ---------------------------------------------------------------------------
// POST /v1/tenants (CreateTenant)
// ---------------------------------------------------------------------------

func TestHandleCreateTenant_Success(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, id, name string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			if id != "acme" || name != "Acme Corp" {
				t.Fatalf("unexpected args: id=%q name=%q", id, name)
			}
			return testTenantInfo(), nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"acme","name":"Acme Corp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")

	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "acme" {
		t.Fatalf("id = %q, want %q", resp.ID, "acme")
	}
	if resp.Name != "Acme Corp" {
		t.Fatalf("name = %q, want %q", resp.Name, "Acme Corp")
	}
	if resp.CreatedAt != tenantTimeStr {
		t.Fatalf("createdAt = %q, want %q", resp.CreatedAt, tenantTimeStr)
	}
}

func TestHandleCreateTenant_InvalidBody(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateTenant_Conflict(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantAlreadyExists
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"acme","name":"Acme Corp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateTenant_ServiceNil(t *testing.T) {
	// POST /v1/tenants is registered unconditionally so the router always
	// returns 501 Not Implemented (route exists, service not configured)
	// rather than 404 Not Found (route missing). This satisfies the
	// non-404 regression assertion for the tenant route registration test.
	srv := newTenantServer(nil)

	body := `{"id":"acme","name":"Acme Corp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCreateTenant_EmptyID(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrEmptyTenantID
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"","name":"Acme Corp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateTenant_EmptyName(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrEmptyTenantName
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"acme","name":""}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateTenant_StoreError(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, &tenant.StoreError{Op: "put", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"acme","name":"Acme Corp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /v1/tenants/list (ListTenants)
// ---------------------------------------------------------------------------

func TestHandleListTenants_Success(t *testing.T) {
	svc := &mockTenantService{
		listTenantsFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return []tenant.TenantInfo{*testTenantInfo()}, nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listTenantsResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if len(resp.Tenants) != 1 {
		t.Fatalf("tenants len = %d, want 1", len(resp.Tenants))
	}
	if resp.Tenants[0].ID != "acme" {
		t.Fatalf("tenant id = %q, want %q", resp.Tenants[0].ID, "acme")
	}
}

func TestHandleListTenants_Empty(t *testing.T) {
	svc := &mockTenantService{
		listTenantsFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return nil, nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listTenantsResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 0 {
		t.Fatalf("total = %d, want 0", resp.Total)
	}
}

func TestHandleListTenants_StoreError(t *testing.T) {
	svc := &mockTenantService{
		listTenantsFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return nil, &tenant.StoreError{Op: "list", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleListTenants_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	// When tenantSvc is nil, the fixed routes are not registered.
	// The path /v1/tenants/list still matches the /v1/tenants/ prefix in
	// dispatch, routing to handleTenantByID which returns 501.
	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// GET /v1/tenants/{id} (GetTenant)
// ---------------------------------------------------------------------------

func TestHandleGetTenant_Success(t *testing.T) {
	svc := &mockTenantService{
		getTenantFn: func(_ context.Context, id string) (*tenant.TenantInfo, error) {
			if id != "acme" {
				t.Fatalf("unexpected id: %q", id)
			}
			return testTenantInfo(), nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "acme" {
		t.Fatalf("id = %q, want %q", resp.ID, "acme")
	}
	if resp.Name != "Acme Corp" {
		t.Fatalf("name = %q, want %q", resp.Name, "Acme Corp")
	}
}

func TestHandleGetTenant_NotFound(t *testing.T) {
	svc := &mockTenantService{
		getTenantFn: func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetTenant_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleGetTenant_StoreError(t *testing.T) {
	svc := &mockTenantService{
		getTenantFn: func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
			return nil, &tenant.StoreError{Op: "get", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// DELETE /v1/tenants/{id} (DeleteTenant)
// ---------------------------------------------------------------------------

func TestHandleDeleteTenant_Success(t *testing.T) {
	svc := &mockTenantService{
		deleteTenantFn: func(_ context.Context, id string) error {
			if id != "acme" {
				t.Fatalf("unexpected id: %q", id)
			}
			return nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleDeleteTenant_NotFound(t *testing.T) {
	svc := &mockTenantService{
		deleteTenantFn: func(_ context.Context, _ string) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleDeleteTenant_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleDeleteTenant_BarrierError(t *testing.T) {
	svc := &mockTenantService{
		deleteTenantFn: func(_ context.Context, id string) error {
			return &tenant.BarrierError{TenantID: id, Op: "unregister", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /v1/tenants/{id}/quotas (SetTenantQuotas)
// ---------------------------------------------------------------------------

func TestHandleSetTenantQuotas_Success(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, tenantID string, q *quota.Quotas) (*tenant.TenantInfo, error) {
			if tenantID != "acme" {
				t.Fatalf("unexpected tenantID: %q", tenantID)
			}
			if q.MaxKeys != 1000 {
				t.Fatalf("MaxKeys = %d, want 1000", q.MaxKeys)
			}
			info := testTenantInfo()
			info.Quotas = q
			return info, nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"max_keys":1000}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/quotas", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	if resp.Quotas == nil {
		t.Fatal("quotas should not be nil")
	}
	if resp.Quotas.MaxKeys != 1000 {
		t.Fatalf("MaxKeys = %d, want 1000", resp.Quotas.MaxKeys)
	}
}

func TestHandleSetTenantQuotas_NotFound(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	body := `{"max_keys":1000}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/missing/quotas", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleSetTenantQuotas_InvalidBody(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/quotas", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleSetTenantQuotas_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	body := `{"max_keys":1000}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/quotas", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleSetTenantQuotas_StoreError(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
			return nil, &tenant.StoreError{Op: "put", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	body := `{"max_keys":1000}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/quotas", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Method-not-allowed dispatch cases
// ---------------------------------------------------------------------------

func TestHandleTenantByID_MethodNotAllowed(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTenantServer(svc)

	// PATCH is not a registered method for /v1/tenants/{id}
	req := httptest.NewRequest(http.MethodPatch, "/v1/tenants/acme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleTenantByID_MissingID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTenantServer(svc)

	// Path ends at /v1/tenants/ with empty trailing segment
	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// mapTenantError coverage
// ---------------------------------------------------------------------------

func TestMapTenantError_AllCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"empty id", tenant.ErrEmptyTenantID, http.StatusBadRequest},
		{"empty name", tenant.ErrEmptyTenantName, http.StatusBadRequest},
		{"nil quotas", tenant.ErrNilQuotas, http.StatusBadRequest},
		{"already exists", tenant.ErrTenantAlreadyExists, http.StatusConflict},
		{"not found", tenant.ErrTenantNotFound, http.StatusNotFound},
		{"invalid id", &tenant.InvalidTenantIDError{TenantID: "x", Reason: "bad"}, http.StatusBadRequest},
		{"store error", &tenant.StoreError{Op: "op", Err: errSentinel}, http.StatusInternalServerError},
		{"barrier error", &tenant.BarrierError{TenantID: "t", Op: "init", Err: errSentinel}, http.StatusInternalServerError},
		{"unknown", errSentinel, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTenantError(tc.err)
			if got != tc.want {
				t.Fatalf("mapTenantError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PUT /v1/tenants/{id} (UpdateTenant)
// ---------------------------------------------------------------------------

func TestHandleUpdateTenant_Success(t *testing.T) {
	svc := &mockTenantService{
		getTenantFn: func(_ context.Context, id string) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          "Old Name",
				IsolationType: tenant.IsolationBarrier,
				CreatedAt:     tenantTime,
				UpdatedAt:     tenantTime,
			}, nil
		},
		updateTenantFn: func(_ context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          patch.Name,
				IsolationType: tenant.IsolationBarrier,
				CreatedAt:     tenantTime,
				UpdatedAt:     tenantTime,
			}, nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"name":"New Name"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/acme", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "New Name" {
		t.Fatalf("name = %q, want %q", resp.Name, "New Name")
	}
}

func TestHandleUpdateTenant_NotFound(t *testing.T) {
	svc := &mockTenantService{
		updateTenantFn: func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	body := `{"name":"X"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/ghost", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUpdateTenant_InvalidBody(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/acme", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateTenant_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	body := `{"name":"X"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/acme", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestMapTenantError_HasData(t *testing.T) {
	got := mapTenantError(tenant.ErrTenantHasData)
	if got != http.StatusConflict {
		t.Fatalf("mapTenantError(ErrTenantHasData) = %d, want %d", got, http.StatusConflict)
	}
}

// ---------------------------------------------------------------------------
// CreateTenant with explicit IsolationType
// ---------------------------------------------------------------------------

func TestHandleCreateTenant_WithIsolationType(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          name,
				IsolationType: iso,
				CreatedAt:     tenantTime,
				UpdatedAt:     tenantTime,
			}, nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"id":"beta","name":"Beta","isolation_type":"prefix"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	if resp.IsolationType != tenant.IsolationPrefix {
		t.Fatalf("isolation_type = %q, want %q", resp.IsolationType, tenant.IsolationPrefix)
	}
}

func TestHandleCreateTenant_DefaultIsolationType(t *testing.T) {
	svc := &mockTenantService{
		createTenantFn: func(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{
				ID:            id,
				Name:          name,
				IsolationType: iso,
				CreatedAt:     tenantTime,
				UpdatedAt:     tenantTime,
			}, nil
		},
	}
	srv := newTenantServer(svc)

	// No isolation_type field; should default to "barrier".
	body := `{"id":"acme","name":"Acme"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
}

// ---------------------------------------------------------------------------
// POST /v1/tenants/{id}/barrier/init (TenantBarrierInit)
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_Success(t *testing.T) {
	var sawTenant, sawPassphrase string
	var sawThreshold, sawShares int
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, tenantID, passphrase string, threshold, shares int) error {
			sawTenant = tenantID
			sawPassphrase = passphrase
			sawThreshold = threshold
			sawShares = shares
			return nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"passphrase":"super-secret","threshold":3,"shares":5}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if sawTenant != "acme" {
		t.Fatalf("tenantID = %q, want %q", sawTenant, "acme")
	}
	if sawThreshold != 3 {
		t.Fatalf("threshold = %d, want 3", sawThreshold)
	}
	if sawShares != 5 {
		t.Fatalf("shares = %d, want 5", sawShares)
	}
	if sawPassphrase != "super-secret" {
		t.Fatalf("passphrase = %q, want %q", sawPassphrase, "super-secret")
	}
}

func TestHandleTenantBarrierInit_EmptyBodyDefaults(t *testing.T) {
	var sawThreshold, sawShares int
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, _, _ string, threshold, shares int) error {
			sawThreshold = threshold
			sawShares = shares
			return nil
		},
	}
	srv := newTenantServer(svc)

	// Empty body — both threshold/shares must default to zero.
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if sawThreshold != 0 || sawShares != 0 {
		t.Fatalf("defaults = (%d,%d), want (0,0)", sawThreshold, sawShares)
	}
}

func TestHandleTenantBarrierInit_InvalidBody(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierInit_NotFound(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, _, _ string, _, _ int) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/ghost/barrier/init", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleTenantBarrierInit_EmptyID(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, _, _ string, _, _ int) error {
			return tenant.ErrEmptyTenantID
		},
	}
	srv := newTenantServer(svc)

	// "/v1/tenants//barrier/init" → CutSuffix yields id="" which is rejected
	// by the dispatcher's empty-id guard before reaching the handler. We
	// exercise the handler-level error instead by providing a valid id but
	// having the service return ErrEmptyTenantID.
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierInit_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTenantBarrierInit_BarrierError(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, tenantID, _ string, _, _ int) error {
			return &tenant.BarrierError{TenantID: tenantID, Op: "initialize", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /v1/tenants/{id}/barrier/unseal (TenantBarrierUnseal)
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_Success(t *testing.T) {
	want := []byte("super-secret-key")
	var sawKey []byte
	svc := &mockTenantService{
		tenantBarrierUnsealFn: func(_ context.Context, _ string, key []byte) error {
			sawKey = key
			return nil
		},
	}
	srv := newTenantServer(svc)

	// encoding/json base64-encodes []byte by default.
	payload := tenantBarrierUnsealRequest{Key: want}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/unseal", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !bytes.Equal(sawKey, want) {
		t.Fatalf("key = %q, want %q", sawKey, want)
	}
}

func TestHandleTenantBarrierUnseal_EmptyBodyFallback(t *testing.T) {
	var sawKey []byte
	svc := &mockTenantService{
		tenantBarrierUnsealFn: func(_ context.Context, _ string, key []byte) error {
			sawKey = key
			return nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/unseal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if len(sawKey) != 0 {
		t.Fatalf("key should be empty for fallback, got %q", sawKey)
	}
}

func TestHandleTenantBarrierUnseal_InvalidBody(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/unseal", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierUnseal_NotFound(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierUnsealFn: func(_ context.Context, _ string, _ []byte) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/ghost/barrier/unseal", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleTenantBarrierUnseal_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/unseal", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTenantBarrierUnseal_BarrierError(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierUnsealFn: func(_ context.Context, tenantID string, _ []byte) error {
			return &tenant.BarrierError{TenantID: tenantID, Op: "unseal", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/unseal", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /v1/tenants/{id}/barrier/seal (TenantBarrierSeal)
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_Success(t *testing.T) {
	var sawTenant string
	svc := &mockTenantService{
		tenantBarrierSealFn: func(_ context.Context, tenantID string) error {
			sawTenant = tenantID
			return nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if sawTenant != "acme" {
		t.Fatalf("tenantID = %q, want %q", sawTenant, "acme")
	}
}

func TestHandleTenantBarrierSeal_NotFound(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierSealFn: func(_ context.Context, _ string) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/ghost/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleTenantBarrierSeal_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTenantBarrierSeal_BarrierError(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierSealFn: func(_ context.Context, tenantID string) error {
			return &tenant.BarrierError{TenantID: tenantID, Op: "seal", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// GET /v1/tenants/{id}/barrier/status (TenantBarrierStatus)
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_Success(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			if tenantID != "acme" {
				t.Fatalf("unexpected tenantID: %q", tenantID)
			}
			return &tenant.TenantBarrierStatus{
				TenantID:       tenantID,
				Sealed:         false,
				Initialized:    true,
				Strategy:       "passphrase",
				HardwareBacked: false,
			}, nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp tenant.TenantBarrierStatus
	decodeJSON(t, rec, &resp)
	if resp.TenantID != "acme" {
		t.Fatalf("tenant_id = %q, want %q", resp.TenantID, "acme")
	}
	if resp.Sealed {
		t.Fatal("sealed = true, want false")
	}
	if !resp.Initialized {
		t.Fatal("initialized = false, want true")
	}
	if resp.Strategy != "passphrase" {
		t.Fatalf("strategy = %q, want %q", resp.Strategy, "passphrase")
	}
}

func TestHandleTenantBarrierStatus_NotFound(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/ghost/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleTenantBarrierStatus_ServiceNil(t *testing.T) {
	srv := newTenantServer(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTenantBarrierStatus_BarrierError(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			return nil, &tenant.BarrierError{TenantID: tenantID, Op: "status", Err: errSentinel}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}
