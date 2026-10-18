// Copyright (c) 2026 Jeremy Hahn
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
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// getOwnPermissions dispatches GET /permissions/own with ctx as the request
// context.
func getOwnPermissions(ctx context.Context, t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/permissions/own", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

func TestHandleListOwnPermissions_ReturnsTheCallersOwnGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := newAuthServer(nil, roles, nil)
	id := &auth.Identity{
		Subject:  "alice",
		TenantID: authtest.BindingTenant,
		Roles:    []string{role.SystemRoleID(authtest.BindingTenant, role.RoleNameAuditor)},
	}
	ctx := auth.WithIdentity(context.Background(), id)

	rec := getOwnPermissions(ctx, t, srv)
	assertStatus(t, rec, http.StatusOK)
	var resp listOwnPermissionsResponse
	decodeJSON(t, rec, &resp)

	want, err := role.ListOwnPermissions(ctx, roles)
	if err != nil {
		t.Fatalf("role.ListOwnPermissions: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the auditor role resolved to no permissions")
	}
	if !reflect.DeepEqual(resp.Permissions, want) {
		t.Fatalf("permissions = %+v, want %+v", resp.Permissions, want)
	}
}

func TestHandleListOwnPermissions_NeverReturnsAnotherTenantsGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := newAuthServer(nil, roles, nil)
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))
	ctx = tenant.WithTenantID(ctx, authtest.OtherTenant)

	rec := getOwnPermissions(ctx, t, srv)
	assertStatus(t, rec, http.StatusOK)
	if got := roles.Resolved(); !reflect.DeepEqual(got, []string{authtest.BindingTenant}) {
		t.Fatalf("resolved tenants %v, want only the caller's own %q", got, authtest.BindingTenant)
	}
}

func TestHandleListOwnPermissions_RefusesAnUnauthenticatedCaller(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant)
	srv := newAuthServer(nil, roles, nil)

	rec := getOwnPermissions(context.Background(), t, srv)
	assertStatus(t, rec, http.StatusUnauthorized)
	if got := roles.Resolved(); len(got) != 0 {
		t.Fatalf("an unauthenticated call resolved tenants %v", got)
	}
}

func TestHandleListOwnPermissions_NotRoutedWithoutARoleService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))

	rec := getOwnPermissions(ctx, t, srv)
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d without a role service, want a refusal", rec.Code)
	}
}
