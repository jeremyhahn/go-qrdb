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
	"net/http"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestTenantAdmin_ActsInOwnTenantOnly: acme's administrator updates acme's
// user, and an update of globex's user is answered not-found and reaches no
// store.
func TestTenantAdmin_ActsInOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name   string
		caller *auth.Identity
		id     uint64
		status int
	}{
		{name: "tenant admin, own tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInBindingTenant, status: http.StatusOK},
		{name: "tenant admin, another tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInOtherTenant, status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t, tt.caller)
			rec := serve(srv, http.MethodPut, "/users/"+strconv.FormatUint(tt.id, 10), `{"name":"renamed"}`)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if reached := len(users.Updated()) > 0; reached != (tt.status == http.StatusOK) {
				t.Fatalf("store reached = %v, want %v", reached, tt.status == http.StatusOK)
			}
		})
	}
}

// TestCrossTenantBinding_WriteRefused: writing a RoleBinding that names any
// tenant other than the user's own is refused with 400
// (*user.RoleBindingTenantError) and stored nowhere -- by acme's
// administrator, who holds roles:assign over the user, as by anyone -- while
// an own-tenant binding is written.
func TestCrossTenantBinding_WriteRefused(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	globexAdmin := role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin)
	tests := []struct {
		name    string
		binding string
		status  int
	}{
		{name: "another tenant", binding: `{"role_id":"` + globexAdmin + `","tenant_id":"` + authtest.OtherTenant + `"}`, status: http.StatusBadRequest},
		{name: "the system tenant", binding: `{"role_id":"admin","tenant_id":"system"}`, status: http.StatusBadRequest},
		{name: "own tenant", binding: `{"role_id":"` + acmeUser + `","tenant_id":"` + authtest.BindingTenant + `"}`, status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t, authtest.BindingTenantAdmin())
			rec := serve(srv, http.MethodPut, "/users/"+strconv.FormatUint(authtest.UserInBindingTenant, 10), `{"role_bindings":[`+tt.binding+`]}`)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			stored := len(users.User(authtest.UserInBindingTenant).RoleBindings) > 0
			if stored != (tt.status == http.StatusOK) {
				t.Fatalf("binding stored = %v, want %v", stored, tt.status == http.StatusOK)
			}
		})
	}
}
