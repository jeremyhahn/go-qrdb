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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestDecodeIAMRequest covers both bounded decoders: a body that decodes is
// admitted, an empty one only where the request may omit it, one that does
// not decode is 400 and one past maxIAMRequestBytes is 413.
func TestDecodeIAMRequest(t *testing.T) {
	type payload struct {
		TenantID string `json:"tenant_id"`
	}
	oversize := `{"tenant_id":"` + strings.Repeat("x", int(maxIAMRequestBytes)) + `"}`
	tests := []struct {
		name     string
		body     io.Reader
		optional bool
		ok       bool
		status   int
		tenant   string
	}{
		{name: "decodes", body: strings.NewReader(`{"tenant_id":"acme"}`), ok: true, tenant: "acme"},
		{name: "decodes, optional", body: strings.NewReader(`{"tenant_id":"acme"}`), optional: true, ok: true, tenant: "acme"},
		{name: "empty, required", body: strings.NewReader(""), status: http.StatusBadRequest},
		{name: "empty, optional", body: strings.NewReader(""), optional: true, ok: true},
		{name: "absent, optional", optional: true, ok: true},
		{name: "undecodable", body: strings.NewReader(`{"tenant_id":`), status: http.StatusBadRequest},
		{name: "undecodable, optional", body: strings.NewReader(`{"tenant_id":`), optional: true, status: http.StatusBadRequest},
		{name: "oversize", body: strings.NewReader(oversize), status: http.StatusRequestEntityTooLarge},
		{name: "oversize, optional", body: strings.NewReader(oversize), optional: true, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/roles/reconcile", tt.body)
			if tt.body == nil {
				r.Body = nil
			}
			w := httptest.NewRecorder()
			var got payload
			decode := decodeIAMRequest
			if tt.optional {
				decode = decodeOptionalIAMRequest
			}
			if ok := decode(w, r, &got); ok != tt.ok {
				t.Fatalf("ok = %v, want %v (status %d: %s)", ok, tt.ok, w.Code, w.Body.String())
			}
			if !tt.ok && w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if got.TenantID != tt.tenant {
				t.Fatalf("decoded tenant %q, want %q", got.TenantID, tt.tenant)
			}
		})
	}
}

// TestIAMRoutes_BodyIsBounded drives every user and role administration route
// whose body the handler decodes with a body past maxIAMRequestBytes (413) and
// one that does not decode (400). Neither reaches a store.
func TestIAMRoutes_BodyIsBounded(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	pad := strings.Repeat("x", int(maxIAMRequestBytes))
	routes := []struct {
		name, method, path, oversize string
	}{
		{"update user", http.MethodPut, "/users/200", `{"name":"` + pad + `"}`},
		{"add cert binding", http.MethodPost, "/users/200/cert-bindings", `{"thumbprint":"` + pad + `"}`},
		{"update role", http.MethodPut, "/roles/" + acmeUser, `{"name":"` + pad + `"}`},
		{"resolve permissions", http.MethodPost, "/roles/resolve-permissions", `{"role_ids":["` + pad + `"]}`},
	}
	bodies := []struct {
		name   string
		body   func(oversize string) string
		status int
	}{
		{"oversize", func(o string) string { return o }, http.StatusRequestEntityTooLarge},
		{"undecodable", func(string) string { return `{"name":` }, http.StatusBadRequest},
	}
	for _, rt := range routes {
		for _, b := range bodies {
			t.Run(rt.name+"/"+b.name, func(t *testing.T) {
				users := authtest.NewBindingUsers()
				roles := authtest.BindingRoles(t)
				srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
					WithUserService(users), WithRoleService(roles))
				rec := serve(srv, rt.method, rt.path, b.body(rt.oversize))
				if rec.Code != b.status {
					t.Fatalf("status = %d, want %d: %.200s", rec.Code, b.status, rec.Body.String())
				}
				if got := users.Updated(); len(got) != 0 {
					t.Fatalf("updates reached the store: %v", got)
				}
				r, err := roles.GetRole(tenant.WithTenantID(context.Background(), authtest.BindingTenant), acmeUser)
				if err != nil {
					t.Fatalf("GetRole: %v", err)
				}
				if strings.Contains(r.Name, "xxxx") {
					t.Fatal("the oversize role update reached the store")
				}
			})
		}
	}
}
