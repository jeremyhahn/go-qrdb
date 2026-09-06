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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestHandleReconcileRoles_Success covers the whole report crossing the JSON
// boundary: a response that lost its added/removed lists would still read as a
// success and tell an operator nothing about what changed.
func TestHandleReconcileRoles_Success(t *testing.T) {
	var gotTenant string
	roleSvc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			gotTenant = tenantID
			return &role.ReconcileReport{
				TenantID: tenantID,
				Roles: []role.RoleReconcileResult{
					{
						RoleID:  "r-admin",
						Name:    role.RoleNameAdmin,
						Added:   []permission.Permission{"sdlc:bind"},
						Removed: []permission.Permission{"sdlc:register"},
					},
					{
						RoleID:  "r-operator",
						Name:    role.RoleNameOperator,
						Created: true,
						Added:   []permission.Permission{"orchestrator:task_block"},
					},
				},
			}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile", strings.NewReader(`{"tenant_id":"t1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	if gotTenant != "t1" {
		t.Errorf("service saw tenant %q, want t1", gotTenant)
	}

	var resp reconcileRolesResponse
	decodeJSON(t, rec, &resp)
	if resp.TenantID != "t1" {
		t.Errorf("tenant_id = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("roles length = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if len(resp.Roles[0].Added) != 1 || resp.Roles[0].Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", resp.Roles[0].Added)
	}
	if len(resp.Roles[0].Removed) != 1 || resp.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", resp.Roles[0].Removed)
	}
	if !resp.Roles[1].Created {
		t.Error("operator not reported as created")
	}
}

// TestHandleReconcileRoles_EmptyBody: an empty body means "my own tenant",
// which is the common case for a tenant administrator and must not be a 400.
func TestHandleReconcileRoles_EmptyBody(t *testing.T) {
	roleSvc := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID != "" {
				t.Errorf("tenantID = %q, want empty", tenantID)
			}
			return &role.ReconcileReport{TenantID: "own"}, nil
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile", http.NoBody)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp reconcileRolesResponse
	decodeJSON(t, rec, &resp)
	if resp.TenantID != "own" {
		t.Errorf("tenant_id = %q, want own", resp.TenantID)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("roles = %+v, want empty", resp.Roles)
	}
}

func TestHandleReconcileRoles_InvalidJSON(t *testing.T) {
	srv := newAuthServer(nil, &mockRoleService{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleReconcileRoles_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
		},
	}
	srv := newAuthServer(nil, roleSvc, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile", strings.NewReader(`{"tenant_id":"t1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// TestHandleReconcileRoles_NoRoleService reports 501 rather than 404 when the
// role service is not wired, matching every other role route.
func TestHandleReconcileRoles_NoRoleService(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/roles/reconcile", http.NoBody)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// TestReconcileReportToResponse_Nil covers the nil guard in the converter.
func TestReconcileReportToResponse_Nil(t *testing.T) {
	got := reconcileReportToResponse(nil)
	if got.TenantID != "" || len(got.Roles) != 0 {
		t.Errorf("reconcileReportToResponse(nil) = %+v, want the zero value", got)
	}
}
