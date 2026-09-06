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
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTReconcileBuiltInRoles_RoundTrip proves the request reaches
// POST /roles/reconcile carrying the tenant, and the report comes back intact.
func TestRESTReconcileBuiltInRoles_RoundTrip(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var gotTenant string
	rs.handle(
		pathAndQuery(http.MethodPost, "/roles/reconcile", ""),
		func(w http.ResponseWriter, r *http.Request) {
			var body transport.ReconcileRolesRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			gotTenant = body.TenantID
			writeJSON(w, &transport.ReconcileRolesResponse{
				TenantID: body.TenantID,
				Roles: []transport.RoleReconcileResult{
					{
						RoleID:  "r-admin",
						Name:    "admin",
						Added:   []string{"sdlc:bind"},
						Removed: []string{"sdlc:register"},
					},
					{
						RoleID:  "r-operator",
						Name:    "operator",
						Created: true,
						Added:   []string{"orchestrator:task_block"},
					},
				},
			})
		},
	)

	resp, err := rt.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if gotTenant != "t1" {
		t.Errorf("server saw tenant %q, want t1", gotTenant)
	}
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("Roles len = %d, want 2: %+v", len(resp.Roles), resp.Roles)
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

// TestRESTReconcileBuiltInRoles_NilRequest covers the "my own tenant" call.
func TestRESTReconcileBuiltInRoles_NilRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodPost, "/roles/reconcile", ""),
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.ReconcileRolesResponse{TenantID: "own"})
		},
	)

	resp, err := rt.ReconcileBuiltInRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles(nil): %v", err)
	}
	if resp.TenantID != "own" {
		t.Errorf("TenantID = %q, want own", resp.TenantID)
	}
}

func TestRESTReconcileBuiltInRoles_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodPost, "/roles/reconcile", ""),
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		},
	)

	resp, err := rt.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	if err == nil {
		t.Fatalf("succeeded against a failing server: %+v", resp)
	}
}

func TestRESTReconcileBuiltInRoles_ClosedTransport(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()

	_, err := rt.ReconcileBuiltInRoles(context.Background(), &transport.ReconcileRolesRequest{TenantID: "t1"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
