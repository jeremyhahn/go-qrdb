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

package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredRESTTenants returns the REAL seal-disabled tenant service with the
// named tenants created and deleted through it.
func retiredRESTTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

type failingRESTDeletedSvc struct {
	tenant.TenantServicer
	report *tenant.ReconcileReport
	err    error
}

func (f failingRESTDeletedSvc) ListDeletedTenants(context.Context, string, int) (*tenant.DeletedTenantsPage, error) {
	return nil, f.err
}

func (f failingRESTDeletedSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	return f.report, f.err
}

func TestHandleListDeletedTenants_PagesThroughRetiredIds(t *testing.T) {
	srv := newTenantServer(retiredRESTTenants(t, "bravo", "alpha", "charlie"))

	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/v1/tenants/deleted/list?page_size=2", nil))
	assertStatus(t, rec, http.StatusOK)
	var first tenant.DeletedTenantsPage
	decodeJSON(t, rec, &first)
	if len(first.Tombstones) != 2 || first.Tombstones[0].ID != "alpha" || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}

	rec = httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tenants/deleted/list?page_size=2&page_token="+url.QueryEscape(first.NextPageToken), nil))
	assertStatus(t, rec, http.StatusOK)
	var second tenant.DeletedTenantsPage
	decodeJSON(t, rec, &second)
	if len(second.Tombstones) != 1 || second.Tombstones[0].ID != "charlie" || second.NextPageToken != "" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestHandleListDeletedTenants_RefusesBadPaging(t *testing.T) {
	srv := newTenantServer(retiredRESTTenants(t))
	for _, q := range []string{"page_size=abc", "page_size=-1", "page_token=%25%25"} {
		rec := httptest.NewRecorder()
		srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/v1/tenants/deleted/list?"+q, nil))
		assertStatus(t, rec, http.StatusBadRequest)
	}
}

// TestTenantNamedDeleted_StillResolves: the two static routes shadow no {id}
// route, so a tenant literally named "deleted" is still reachable.
func TestTenantNamedDeleted_StillResolves(t *testing.T) {
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "deleted", "Named Deleted", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	srv := newTenantServer(svc)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/v1/tenants/deleted", nil))
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleReconcileDeletedTenants_ReportsWhatItInspected(t *testing.T) {
	srv := newTenantServer(retiredRESTTenants(t, "alpha", "bravo"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodPost, "/v1/tenants/deleted/reconcile", nil))
	assertStatus(t, rec, http.StatusOK)
	var report tenant.ReconcileReport
	decodeJSON(t, rec, &report)
	if report.Inspected != 2 {
		t.Fatalf("inspected = %d; want 2", report.Inspected)
	}
}

func TestHandleDeletedTenantVerbs_NilServiceAndErrors(t *testing.T) {
	srv := newTenantServer(nil)
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/tenants/deleted/list", nil),
		httptest.NewRequest(http.MethodPost, "/v1/tenants/deleted/reconcile", nil),
	} {
		rec := httptest.NewRecorder()
		srv.dispatch(rec, r)
		assertStatus(t, rec, http.StatusNotImplemented)
	}

	srv = newTenantServer(failingRESTDeletedSvc{err: &tenant.ResidueReconcileError{Op: "remove", Err: errors.New("busy")}})
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/tenants/deleted/list", nil),
		httptest.NewRequest(http.MethodPost, "/v1/tenants/deleted/reconcile", nil),
	} {
		rec := httptest.NewRecorder()
		srv.dispatch(rec, r)
		assertStatus(t, rec, http.StatusInternalServerError)
	}
}
