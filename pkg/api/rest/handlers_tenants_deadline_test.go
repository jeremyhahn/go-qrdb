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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ctxRecordingTenantService records the context of every tenant service call
// the REST handlers make, so a test can assert which deadline reached the
// service. Each call succeeds with an empty result.
type ctxRecordingTenantService struct {
	mockTenantService
	mu   sync.Mutex
	ctxs []context.Context
}

func (m *ctxRecordingTenantService) record(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctxs = append(m.ctxs, ctx)
}

func (m *ctxRecordingTenantService) recorded() []context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]context.Context(nil), m.ctxs...)
}

func (m *ctxRecordingTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	m.record(ctx)
	return nil, nil
}

func (m *ctxRecordingTenantService) ListDeletedTenants(ctx context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	m.record(ctx)
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *ctxRecordingTenantService) ReconcileDeletedTenants(ctx context.Context) (*tenant.ReconcileReport, error) {
	m.record(ctx)
	return &tenant.ReconcileReport{}, nil
}

func (m *ctxRecordingTenantService) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	m.record(ctx)
	return &tenant.TenantInfo{ID: tenantID}, nil
}

func (m *ctxRecordingTenantService) DeleteTenant(ctx context.Context, _ string) error {
	m.record(ctx)
	return nil
}

func (m *ctxRecordingTenantService) UpdateTenant(ctx context.Context, tenantID string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
	m.record(ctx)
	return &tenant.TenantInfo{ID: tenantID}, nil
}

func (m *ctxRecordingTenantService) UpdateQuotas(ctx context.Context, tenantID string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
	m.record(ctx)
	return &tenant.TenantInfo{ID: tenantID}, nil
}

func (m *ctxRecordingTenantService) UpdateEntitlements(ctx context.Context, tenantID string, _ *quota.Entitlements) (*tenant.TenantInfo, error) {
	m.record(ctx)
	return &tenant.TenantInfo{ID: tenantID}, nil
}

func (m *ctxRecordingTenantService) TenantBarrierUnseal(ctx context.Context, _ string, _ []byte) error {
	m.record(ctx)
	return nil
}

func (m *ctxRecordingTenantService) TenantBarrierSeal(ctx context.Context, _ string) error {
	m.record(ctx)
	return nil
}

func (m *ctxRecordingTenantService) TenantBarrierStatus(ctx context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
	m.record(ctx)
	return &tenant.TenantBarrierStatus{}, nil
}

func (m *ctxRecordingTenantService) TenantBarrierOfficerList(ctx context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	m.record(ctx)
	return nil, nil
}

var _ tenant.TenantServicer = (*ctxRecordingTenantService)(nil)

// tenantDeadlineRoutes is every tenant endpoint whose handler once bounded
// the request with a fixed per-request timeout; create and barrier init are
// pinned by their own tests.
var tenantDeadlineRoutes = []struct {
	name, method, path, body string
}{
	{"list", http.MethodGet, "/v1/tenants/list", ""},
	{"list deleted", http.MethodGet, "/v1/tenants/deleted/list", ""},
	{"reconcile deleted", http.MethodPost, "/v1/tenants/deleted/reconcile", ""},
	{"get", http.MethodGet, "/v1/tenants/acme", ""},
	{"delete", http.MethodDelete, "/v1/tenants/acme", ""},
	{"update", http.MethodPut, "/v1/tenants/acme", `{"name":"Acme"}`},
	{"set quotas", http.MethodPost, "/v1/tenants/acme/quotas", `{}`},
	{"set entitlements", http.MethodPatch, "/v1/tenants/acme/entitlements", `{}`},
	{"barrier unseal", http.MethodPost, "/v1/tenants/acme/barrier/unseal", `{}`},
	{"barrier seal", http.MethodPost, "/v1/tenants/acme/barrier/seal", ""},
	{"barrier status", http.MethodGet, "/v1/tenants/acme/barrier/status", ""},
	{"barrier officer list", http.MethodGet, "/v1/tenants/acme/barrier/officers", ""},
}

// dispatchTenantRoute sends one request carrying ctx to a server backed by a
// recording tenant service and returns the contexts the service saw.
func dispatchTenantRoute(t *testing.T, ctx context.Context, method, path, body string) []context.Context {
	t.Helper()
	svc := &ctxRecordingTenantService{}
	srv := newTenantServer(svc)

	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequestWithContext(ctx, method, path, rd))

	if rec.Code >= http.StatusBadRequest {
		t.Fatalf("%s %s: status %d, body %s", method, path, rec.Code, rec.Body.String())
	}
	ctxs := svc.recorded()
	if len(ctxs) == 0 {
		t.Fatalf("%s %s: the tenant service was never called", method, path)
	}
	return ctxs
}

// TestTenantHandlers_LeaveDeadlineToCaller pins that no tenant handler cuts
// the request short with a deadline of its own: a request without a deadline
// reaches the tenant service without one, as on every other transport.
func TestTenantHandlers_LeaveDeadlineToCaller(t *testing.T) {
	for _, rt := range tenantDeadlineRoutes {
		t.Run(rt.name, func(t *testing.T) {
			for _, ctx := range dispatchTenantRoute(t, context.Background(), rt.method, rt.path, rt.body) {
				if d, ok := ctx.Deadline(); ok {
					t.Fatalf("tenant service ran under a handler deadline %v, want the request's own context", d)
				}
			}
		})
	}
}

// TestTenantHandlers_KeepCallerDeadline pins the other half: a caller's
// deadline, however long, reaches the tenant service unshortened.
func TestTenantHandlers_KeepCallerDeadline(t *testing.T) {
	for _, rt := range tenantDeadlineRoutes {
		t.Run(rt.name, func(t *testing.T) {
			want := time.Now().Add(time.Minute)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			for _, got := range dispatchTenantRoute(t, ctx, rt.method, rt.path, rt.body) {
				if d, _ := got.Deadline(); !d.Equal(want) {
					t.Fatalf("tenant service deadline = %v, want the caller's %v", d, want)
				}
			}
		})
	}
}
