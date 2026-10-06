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
	tenant.TenantServicer
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

	officerRegisterFn func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn     func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn      func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn   func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
	quorumBeginFn     func(ctx context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error)
	quorumSubmitFn    func(ctx context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error)
	rekeyStartFn      func(ctx context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error)
	officerRevokeFn   func(ctx context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error)
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

func (m *mockTenantService) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *mockTenantService) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
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

// errTenantMockUnset is returned by a holder method whose fn the test did
// not set, so an unexpected call fails loudly rather than answering zero.
var errTenantMockUnset = errors.New("mockTenantService: holder method not configured by this test")

func (m *mockTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, tenantID, req)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, tenantID)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if m.claimBeginFn != nil {
		return m.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if m.claimCompleteFn != nil {
		return m.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierQuorumBegin(ctx context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error) {
	if m.quorumBeginFn != nil {
		return m.quorumBeginFn(ctx, tenantID, op, username, factor)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierQuorumSubmit(ctx context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	if m.quorumSubmitFn != nil {
		return m.quorumSubmitFn(ctx, tenantID, sub)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierRekeyStart(ctx context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if m.rekeyStartFn != nil {
		return m.rekeyStartFn(ctx, tenantID, threshold, policy)
	}
	return nil, errTenantMockUnset
}
func (m *mockTenantService) TenantBarrierOfficerRevoke(ctx context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error) {
	if m.officerRevokeFn != nil {
		return m.officerRevokeFn(ctx, tenantID, username, threshold)
	}
	return nil, errTenantMockUnset
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
		opts = append(opts, WithTenantService(svc), WithTenantOnboarding(&fakeTenantOnboarding{tenants: svc}))
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
