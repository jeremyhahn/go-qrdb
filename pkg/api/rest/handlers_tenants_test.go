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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
	tenantBarrierInitFn   func(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) error
	tenantBarrierUnsealFn func(ctx context.Context, tenantID string, key []byte) error
	tenantBarrierSealFn   func(ctx context.Context, tenantID string) error
	tenantBarrierStatusFn func(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error)
}

func (m *mockTenantService) CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
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

func (m *mockTenantService) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantService) TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*tenant.TenantBarrierInitResult, error) {
	if m.tenantBarrierInitFn != nil {
		return nil, m.tenantBarrierInitFn(ctx, tenantID, passphrase, threshold, shares)
	}
	return nil, nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}

func (m *mockTenantService) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
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

// Compile-time interface check.
var _ tenant.TenantServicer = (*mockTenantService)(nil)

// ---------------------------------------------------------------------------
// Mock: service.Registry (for the UpdateTenant isolation-change data probe)
// ---------------------------------------------------------------------------

// mockServiceRegistry is a service.Registry whose KVService result is
// controllable; every other resolver returns (nil, nil). It drives the
// fail-closed isolation-type precondition in handleUpdateTenant.
type mockServiceRegistry struct {
	kvSvc service.KVServicer
	kvErr error

	// mvccSvc and mvccErr drive the unfiltered data probe tenantHasData
	// prefers. The zero value returns (nil, nil) -- a legitimate registry
	// outcome meaning "nothing to hand back for this scope" -- which is
	// exactly the shape that must not be dereferenced.
	mvccSvc service.MVCCKVServicer
	mvccErr error
}

func (m *mockServiceRegistry) KVService(_, _ string) (service.KVServicer, error) {
	return m.kvSvc, m.kvErr
}
func (m *mockServiceRegistry) MVCCService(_, _ string) (service.MVCCKVServicer, error) {
	return m.mvccSvc, m.mvccErr
}
func (m *mockServiceRegistry) WatchService(_, _ string) (service.WatchServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) LeaseService(_, _ string) (service.LeaseServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) IndexService(_, _ string) (service.IndexServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) TimeSeriesService(_, _ string) (service.TimeSeriesServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) VectorService(_, _ string) (service.VectorServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) TransactionService(_, _ string) (service.TransactionServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) BatchService(_, _ string) (service.BatchServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) TableService(_, _ string) (service.TableServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) SequenceService(_, _ string) (service.SequenceServicer, error) {
	return nil, nil
}
func (m *mockServiceRegistry) PrivilegeService(_, _ string) (service.PrivilegeServicer, error) {
	return nil, nil
}

func (m *mockServiceRegistry) CollectionService(_, _ string) (service.CollectionServicer, error) {
	return nil, nil
}

func (m *mockServiceRegistry) LockService(_, _ string) (service.LockServicer, error) {
	return nil, nil
}

var _ service.Registry = (*mockServiceRegistry)(nil)

// newTenantServerWithRegistry builds a tenant REST server wired with a service
// registry so the isolation-change data probe can be exercised.
func newTenantServerWithRegistry(svc tenant.TenantServicer, reg ServiceRegistry) *Server {
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithTenantService(svc), WithServiceRegistry(reg))
}

// probedTenantSvc returns a REAL tenant service holding tenant "acme" on
// prefix isolation, with its isolation-change precondition wired to the
// service.TenantDataProbe over reg -- the probe the node wires. The
// precondition lives in the tenant service, so these REST tests exercise the
// same check every transport reaches.
func probedTenantSvc(t *testing.T, reg ServiceRegistry) *authtest.Tenants {
	t.Helper()
	svc := authtest.NewTenants(t, "acme")
	w, ok := svc.TenantServicer.(tenant.IsolationChangeWiring)
	if !ok {
		t.Fatal("the tenant service does not accept a data probe")
	}
	w.SetTenantDataProbe(service.NewTenantDataProbe(reg))
	return svc
}

// assertIsolation fails unless acme's effective isolation is want.
func assertIsolation(t *testing.T, svc tenant.TenantServicer, want tenant.IsolationType) {
	t.Helper()
	info, err := svc.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got := info.EffectiveIsolationType(); got != want {
		t.Fatalf("acme isolation = %s, want %s", got, want)
	}
}

// isolationChange drives PUT /v1/tenants/acme to volume isolation against a
// real tenant service probing reg, and returns the recorder and the service.
func isolationChange(t *testing.T, reg *mockServiceRegistry) (*httptest.ResponseRecorder, *authtest.Tenants) {
	t.Helper()
	svc := probedTenantSvc(t, reg)
	srv := newTenantServerWithRegistry(svc, reg)
	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/acme", strings.NewReader(`{"isolation_type":"volume"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec, svc
}

// TestHandleUpdateTenant_IsolationChange_FailsClosedOnResolveError proves the
// isolation-type change is REJECTED (503) when the tenant's KV service cannot be
// resolved — the data precondition fails closed rather than silently allowing
// the change on an unverified tenant.
func TestHandleUpdateTenant_IsolationChange_FailsClosedOnResolveError(t *testing.T) {
	reg := &mockServiceRegistry{kvErr: errors.New("leader unavailable")}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusServiceUnavailable)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_FailsClosedOnListError proves the change
// is rejected (503) when the data probe (List) errors.
func TestHandleUpdateTenant_IsolationChange_FailsClosedOnListError(t *testing.T) {
	reg := &mockServiceRegistry{kvSvc: &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) { return nil, errors.New("list timeout") },
	}}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusServiceUnavailable)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_ConflictWhenTenantHasOnlyRelationalData
// is the regression guard for the probe's real purpose. Relational tables live
// under the reserved __tbl:/__idx:/__seq: prefixes, which KVServicer.List
// filters out, so a tenant whose ONLY content is relational tables lists as
// empty. Probing with List alone would let that tenant flip isolation and
// orphan every table it owns; the unfiltered MVCC count is what sees them.
func TestHandleUpdateTenant_IsolationChange_ConflictWhenTenantHasOnlyRelationalData(t *testing.T) {
	reg := &mockServiceRegistry{
		// The KV list is empty -- exactly what a relational-only tenant
		// reports once the internal-key filter runs.
		kvSvc: &mockKVService{
			listFn: func(_ context.Context, _ string) ([]string, error) { return nil, nil },
		},
		mvccSvc: &mockMVCCService{
			rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
				return nil, 3, 0, nil // three reserved keys: a row, its index entry, a sequence
			},
		},
	}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusConflict)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_FailsClosedOnRangeError proves an
// erroring MVCC probe rejects the change rather than falling through to the
// filtered KV list, which would silently answer a question the probe just
// failed to answer.
func TestHandleUpdateTenant_IsolationChange_FailsClosedOnRangeError(t *testing.T) {
	reg := &mockServiceRegistry{
		kvSvc: &mockKVService{
			listFn: func(_ context.Context, _ string) ([]string, error) { return nil, nil },
		},
		mvccSvc: &mockMVCCService{
			rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
				return nil, 0, 0, errors.New("range timeout")
			},
		},
	}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusServiceUnavailable)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_NonMVCCShardUsesKVList proves that a
// shard with no MVCC backend answers from the KV list instead. That is not a
// fallback to a weaker probe: relational tables require the MVCC state
// machine, so a shard that cannot serve Range cannot hold relational data and
// the list is exact for it.
func TestHandleUpdateTenant_IsolationChange_NonMVCCShardUsesKVList(t *testing.T) {
	reg := &mockServiceRegistry{
		kvSvc: &mockKVService{
			listFn: func(_ context.Context, _ string) ([]string, error) { return []string{"user-key"}, nil },
		},
		mvccSvc: &mockMVCCService{
			rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
				return nil, 0, 0, &service.MVCCBackendRequiredError{}
			},
		},
	}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusConflict)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_ConflictWhenTenantHasData proves the
// change is rejected (409) when the tenant still holds data.
func TestHandleUpdateTenant_IsolationChange_ConflictWhenTenantHasData(t *testing.T) {
	reg := &mockServiceRegistry{kvSvc: &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) { return []string{"existing-key"}, nil },
	}}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusConflict)
	assertIsolation(t, svc, tenant.IsolationPrefix)
}

// TestHandleUpdateTenant_IsolationChange_AllowedWhenNoData proves the change
// proceeds (200) once the tenant is verified data-free.
func TestHandleUpdateTenant_IsolationChange_AllowedWhenNoData(t *testing.T) {
	reg := &mockServiceRegistry{kvSvc: &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) { return nil, nil },
	}}
	rec, svc := isolationChange(t, reg)
	assertStatus(t, rec, http.StatusOK)
	assertIsolation(t, svc, tenant.IsolationVolume)
}

// A patch naming the tenant's CURRENT isolation type changes nothing, so it
// consults no probe: it succeeds even when the data plane is unreachable.
func TestHandleUpdateTenant_SameIsolationIsANoOp(t *testing.T) {
	reg := &mockServiceRegistry{kvErr: errors.New("leader unavailable")}
	svc := probedTenantSvc(t, reg)
	srv := newTenantServerWithRegistry(svc, reg)
	req := httptest.NewRequest(http.MethodPut, "/v1/tenants/acme", strings.NewReader(`{"isolation_type":"prefix"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

// TestHandleSetTenantQuotas_BoundsRequestContext proves the quotas handler
// passes a deadline-bounded context to the service (regression for the handlers
// that previously forwarded the unbounded r.Context()).
func TestHandleSetTenantQuotas_BoundsRequestContext(t *testing.T) {
	var hadDeadline bool
	svc := &mockTenantService{
		updateQuotasFn: func(ctx context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
			_, hadDeadline = ctx.Deadline()
			return testTenantInfo(), nil
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/quotas", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !hadDeadline {
		t.Fatal("UpdateQuotas received an unbounded context — request not time-limited")
	}
}

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
// mapHTTPError coverage
// ---------------------------------------------------------------------------

func TestMapTenantError_AllCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"empty id", tenant.ErrEmptyTenantID, http.StatusBadRequest},
		{"empty name", tenant.ErrEmptyTenantName, http.StatusBadRequest},
		{"empty share", tenant.ErrEmptyShare, http.StatusBadRequest},
		{"system tenant prefix", tenant.ErrSystemTenantMustBePrefix, http.StatusBadRequest},
		{"invalid strategy", &tenant.InvalidStrategyError{Strategy: "bogus"}, http.StatusBadRequest},
		{"tenant scope denied", &tenant.TenantScopeError{CallerTenantID: "a", TargetTenantID: "b", Op: "seal"}, http.StatusForbidden},
		{"nil quotas", tenant.ErrNilQuotas, http.StatusBadRequest},
		{"barrier isolation unavailable", tenant.ErrBarrierIsolationUnavailable, http.StatusUnprocessableEntity},
		{"barrier not enabled", tenant.ErrBarrierNotEnabled, http.StatusUnprocessableEntity},
		{"already exists", tenant.ErrTenantAlreadyExists, http.StatusConflict},
		{"not found", tenant.ErrTenantNotFound, http.StatusNotFound},
		{"invalid id", &tenant.InvalidTenantIDError{TenantID: "x", Reason: "bad"}, http.StatusBadRequest},
		{"store error", &tenant.StoreError{Op: "op", Err: errSentinel}, http.StatusInternalServerError},
		{"barrier error", &tenant.BarrierError{TenantID: "t", Op: "init", Err: errSentinel}, http.StatusInternalServerError},
		{"unknown", errSentinel, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.want {
				t.Fatalf("mapHTTPError(%v) = %d, want %d", tc.err, got, tc.want)
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
	got := mapHTTPError(tenant.ErrTenantHasData)
	if got != http.StatusConflict {
		t.Fatalf("mapHTTPError(ErrTenantHasData) = %d, want %d", got, http.StatusConflict)
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
		tenantBarrierInitFn: func(_ context.Context, tenantID string, passphrase []byte, threshold, shares int) error {
			sawTenant = tenantID
			sawPassphrase = string(passphrase)
			sawThreshold = threshold
			sawShares = shares
			return nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"passphrase":"c3VwZXItc2VjcmV0","threshold":3,"shares":5}` // passphrase is []byte: base64 on the wire
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
		tenantBarrierInitFn: func(_ context.Context, _ string, _ []byte, threshold, shares int) error {
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
		tenantBarrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
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
		tenantBarrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
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
		tenantBarrierInitFn: func(_ context.Context, tenantID string, _ []byte, _, _ int) error {
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

// ---------------------------------------------------------------------------
// extMockTenantService — wraps mockTenantService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockTenantService struct {
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
	shamirSealVoteFn     func(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error)
}

func newExtTenantSvc() *extMockTenantService {
	return &extMockTenantService{mockTenantService: &mockTenantService{}}
}

var _ tenant.TenantServicer = (*extMockTenantService)(nil)

func (e *extMockTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, tenantID, req)
	}
	return e.mockTenantService.TenantBarrierOfficerRegister(ctx, tenantID, req)
}

func (e *extMockTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerList(ctx, tenantID)
}

func (e *extMockTenantService) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerClaimStatusFn != nil {
		return e.officerClaimStatusFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerClaimStatus(ctx, tenantID)
}

func (e *extMockTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if e.claimBeginFn != nil {
		return e.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
}

func (e *extMockTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if e.claimCompleteFn != nil {
		return e.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
}

func (e *extMockTenantService) TenantBarrierShamirSealVote(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
	if e.shamirSealVoteFn != nil {
		return e.shamirSealVoteFn(ctx, tenantID, share)
	}
	return e.mockTenantService.TenantBarrierShamirSealVote(ctx, tenantID, share)
}

// Delegation pass-through methods (all other TenantServicer methods).

func (e *extMockTenantService) CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return e.mockTenantService.CreateTenant(ctx, id, name, isolationType)
}
func (e *extMockTenantService) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	return e.mockTenantService.GetTenant(ctx, tenantID)
}
func (e *extMockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return e.mockTenantService.ListTenants(ctx)
}
func (e *extMockTenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	return e.mockTenantService.DeleteTenant(ctx, tenantID)
}
func (e *extMockTenantService) UpdateTenant(ctx context.Context, tenantID string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateTenant(ctx, tenantID, patch)
}
func (e *extMockTenantService) UpdateQuotas(ctx context.Context, tenantID string, quotas *quota.Quotas) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateQuotas(ctx, tenantID, quotas)
}
func (e *extMockTenantService) TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}
func (e *extMockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int, sid seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInitWithStrategy(ctx, tenantID, passphrase, threshold, shares, sid)
}
func (e *extMockTenantService) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	return e.mockTenantService.TenantBarrierUnseal(ctx, tenantID, key)
}
func (e *extMockTenantService) TenantBarrierUnsealShare(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
	return e.mockTenantService.TenantBarrierUnsealShare(ctx, tenantID, share)
}
func (e *extMockTenantService) TenantBarrierSeal(ctx context.Context, tenantID string) error {
	return e.mockTenantService.TenantBarrierSeal(ctx, tenantID)
}
func (e *extMockTenantService) TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	return e.mockTenantService.TenantBarrierStatus(ctx, tenantID)
}
func (e *extMockTenantService) IncrementQuotaUsage(ctx context.Context, tenantID string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.IncrementQuotaUsage(ctx, tenantID, r, delta)
}
func (e *extMockTenantService) DecrementQuotaUsage(ctx context.Context, tenantID string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.DecrementQuotaUsage(ctx, tenantID, r, delta)
}
func (e *extMockTenantService) GetQuotaUsage(ctx context.Context, tenantID string, r quota.Resource) (int64, error) {
	return e.mockTenantService.GetQuotaUsage(ctx, tenantID, r)
}

// ---------------------------------------------------------------------------
// tenantOfficerStatusesToResponse — zero / non-zero ClaimedAt paths
// ---------------------------------------------------------------------------

func TestTenantOfficerStatusesToResponse_Fields(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	officers := []*ceremony.OfficerStatus{
		{
			Username:       "carol",
			CertThumbprint: "ghi789",
			ShareIndex:     1,
			AuthFactor:     "software",
			SharePending:   true,
			EnrolledAt:     now,
			ClaimedAt:      time.Time{}, // zero — must NOT appear
		},
		{
			Username:       "dave",
			CertThumbprint: "jkl012",
			ShareIndex:     2,
			AuthFactor:     "piv",
			SharePending:   false,
			EnrolledAt:     now,
			ClaimedAt:      now, // non-zero — must appear
		},
	}

	resp := tenantOfficerStatusesToResponse(officers)
	if len(resp) != 2 {
		t.Fatalf("len = %d, want 2", len(resp))
	}
	if _, ok := resp[0]["claimed_at"]; ok {
		t.Fatal("claimed_at present for unclaimed officer")
	}
	if resp[0]["username"] != "carol" {
		t.Fatalf("username = %v, want carol", resp[0]["username"])
	}
	if _, ok := resp[1]["claimed_at"]; !ok {
		t.Fatal("claimed_at missing for claimed officer")
	}
	if resp[1]["username"] != "dave" {
		t.Fatalf("username = %v, want dave", resp[1]["username"])
	}
}

// ---------------------------------------------------------------------------
// Tenant barrier SO handlers
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierOfficerRegister_Success(t *testing.T) {
	svc := newExtTenantSvc()
	svc.officerRegisterFn = func(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
		if tenantID != "acme" {
			t.Fatalf("tenantID = %q, want acme", tenantID)
		}
		return &ceremony.RegisterOfficerResult{CertPEM: []byte("tenant-cert"), ShareIndex: 1}, nil
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers",
		strings.NewReader(`{"username":"officer1","csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["cert_pem"] != "tenant-cert" {
		t.Fatalf("cert_pem = %v, want tenant-cert", resp["cert_pem"])
	}
}

// TestHandleTenantBarrierOfficerRegister_ForwardsAuthenticatorRef is the
// regression guard for the bug where the tenant officer registration
// handler's local wire struct omitted authenticator_ref, silently dropping
// it even though ceremony.RegisterOfficerRequest and the system-barrier
// variant both carry it.
func TestHandleTenantBarrierOfficerRegister_ForwardsAuthenticatorRef(t *testing.T) {
	svc := newExtTenantSvc()
	var captured *ceremony.RegisterOfficerRequest
	svc.officerRegisterFn = func(_ context.Context, _ string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
		captured = req
		return &ceremony.RegisterOfficerResult{CertPEM: []byte("tenant-cert"), ShareIndex: 1}, nil
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers",
		strings.NewReader(`{"username":"officer1","csr_pem":"csr-data","authenticator_type":"piv","authenticator_ref":"slot-9a"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil {
		t.Fatal("expected TenantBarrierOfficerRegister to be called")
	}
	if captured.AuthenticatorRef != "slot-9a" {
		t.Errorf("AuthenticatorRef = %q, want %q", captured.AuthenticatorRef, "slot-9a")
	}
	if string(captured.CSRPEM) != "csr-data" {
		t.Errorf("CSRPEM = %q, want plain text %q (not base64-decoded)", captured.CSRPEM, "csr-data")
	}
}

func TestHandleTenantBarrierOfficerRegister_MissingUsername(t *testing.T) {
	srv := newTenantServer(newExtTenantSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers",
		strings.NewReader(`{"csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierOfficerRegister_MissingCSR(t *testing.T) {
	srv := newTenantServer(newExtTenantSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers",
		strings.NewReader(`{"username":"officer1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierOfficerRegister_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.officerRegisterFn = func(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
		return nil, service.ErrCeremonyNotConfigured
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers",
		strings.NewReader(`{"username":"officer1","csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnprocessableEntity)
}

func TestHandleTenantBarrierOfficerList_Success(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)
	svc := newExtTenantSvc()
	svc.officerListFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return []*ceremony.OfficerStatus{
			{Username: "officer1", ShareIndex: 1, EnrolledAt: now},
		}, nil
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	officers, ok := resp["officers"].([]interface{})
	if !ok || len(officers) != 1 {
		t.Fatalf("expected 1 officer, got %v", resp["officers"])
	}
}

func TestHandleTenantBarrierOfficerList_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.officerListFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return nil, ceremony.ErrInsufficientOfficers
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnprocessableEntity)
}

func TestHandleTenantBarrierOfficerClaimStatus_Success(t *testing.T) {
	now := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	svc := newExtTenantSvc()
	svc.officerClaimStatusFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return []*ceremony.OfficerStatus{
			{Username: "officer1", ClaimedAt: now},
		}, nil
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers/claim-status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleTenantBarrierOfficerClaimStatus_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.officerClaimStatusFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers/claim-status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// handleTenantBarrierShareClaimBegin — missing cert path (no TLS).

func TestHandleTenantBarrierShareClaimBegin_MissingCert(t *testing.T) {
	srv := newTenantServer(newExtTenantSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers/officer1/claim/begin", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleTenantBarrierShareClaimBegin_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.claimBeginFn = func(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := &Server{tenantSvc: svc}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimBegin(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleTenantBarrierShareClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	svc := newExtTenantSvc()
	svc.claimBeginFn = func(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
		return &ceremony.ChallengeResponse{Nonce: []byte{0xde, 0xad, 0xbe, 0xef}, ExpiresAt: expires}, nil
	}
	srv := &Server{tenantSvc: svc}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimBegin(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["nonce_hex"] == nil {
		t.Fatal("nonce_hex missing from tenant claim-begin response")
	}
}

// handleTenantBarrierShareClaimComplete tests.

func TestHandleTenantBarrierShareClaimComplete_MissingCert(t *testing.T) {
	srv := newTenantServer(newExtTenantSvc())

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"nonce_hex":"aabb","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/officers/officer1/claim/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleTenantBarrierShareClaimComplete_MissingNonce(t *testing.T) {
	srv := &Server{tenantSvc: newExtTenantSvc()}

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimComplete(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierShareClaimComplete_MissingSignature(t *testing.T) {
	srv := &Server{tenantSvc: newExtTenantSvc()}

	body := `{"nonce_hex":"aabb"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimComplete(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierShareClaimComplete_BadBase64(t *testing.T) {
	srv := &Server{tenantSvc: newExtTenantSvc()}

	body := `{"nonce_hex":"aabb","signature":"!!!bad-base64!!!"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimComplete(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierShareClaimComplete_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.claimCompleteFn = func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	srv := &Server{tenantSvc: svc}

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"nonce_hex":"aabb","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimComplete(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandleTenantBarrierShareClaimComplete_Success(t *testing.T) {
	shareBytes := []byte("tenant-share-bytes")
	svc := newExtTenantSvc()
	svc.claimCompleteFn = func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
		return &ceremony.ShareClaimResult{ShareIndex: 2, ShareBytes: shareBytes}, nil
	}
	srv := &Server{tenantSvc: svc}

	sig := base64.StdEncoding.EncodeToString([]byte("valid-sig"))
	body := `{"nonce_hex":"aabb","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleTenantBarrierShareClaimComplete(rec, req, "acme", "officer1")

	assertStatus(t, rec, http.StatusOK)
	// INV-11: share_bytes must appear exactly here in the claiming officer's response.
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if _, ok := resp["share_bytes"]; !ok {
		t.Fatal("share_bytes missing from tenant claim-complete response")
	}
	// Regression guard: share_index was previously dropped entirely, unlike
	// the system-barrier handleBarrierShareClaimComplete, silently zeroing
	// every REST-transport caller's ShareClaimCompleteResponse.ShareIndex.
	shareIndex, ok := resp["share_index"].(float64)
	if !ok {
		t.Fatalf("share_index missing or wrong type in tenant claim-complete response: %#v", resp["share_index"])
	}
	if shareIndex != 2 {
		t.Errorf("share_index = %v, want 2", shareIndex)
	}
}

// handleTenantBarrierSealVote tests.

func TestHandleTenantBarrierSealVote_MissingShare(t *testing.T) {
	srv := newTenantServer(newExtTenantSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal-vote",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTenantBarrierSealVote_Success(t *testing.T) {
	svc := newExtTenantSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
		return &tenant.TenantBarrierUnsealProgress{Required: 3, Submitted: 1, Complete: false}, nil
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal-vote",
		strings.NewReader(`{"share":"shamir-share-abc"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleTenantBarrierSealVote_ServiceError(t *testing.T) {
	svc := newExtTenantSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/seal-vote",
		strings.NewReader(`{"share":"abc"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// SealVote service errors route through mapHTTPError (non-standard errors → 500).
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 status on seal vote service error, got %d", rec.Code)
	}
}

func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}

// TestMapTenantError_TeardownRefusals pins the statuses for the three
// refusals the tenant teardown added (go-qrdb#413).
//
// ReservedTenantIDError is in the table because it had NO mapping at all: it
// fell through to 500, reporting a client naming a reserved id as a server
// fault. That was latent on the create path and became reachable on the
// delete path when the reserved-id guard was hoisted there.
func TestMapTenantError_TeardownRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "deleted id is not available",
			err:  &tenant.TenantDeletedError{TenantID: "acme", DeletedAt: time.Now().UTC()},
			want: http.StatusConflict,
		},
		{
			name: "tenant still owns databases",
			err:  &tenant.TenantHasDatabasesError{TenantID: "acme", Databases: []string{"ledger"}},
			want: http.StatusConflict,
		},
		{
			name: "reserved id is a client error, not a server fault",
			err:  &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"},
			want: http.StatusBadRequest,
		},
		{
			name: "reserved id wrapped by a caller",
			err:  fmt.Errorf("delete: %w", &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"}),
			want: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapHTTPError(tc.err); got != tc.want {
				t.Errorf("mapHTTPError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
