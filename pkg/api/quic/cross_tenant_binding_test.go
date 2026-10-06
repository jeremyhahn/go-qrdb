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

package quic

import (
	"context"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// bindingCall runs method as a QUIC frame does -- the authorize step, then
// the handler -- and returns the status the frame would carry.
func bindingCall(t *testing.T, srv *Server, ctx context.Context, method, payload string) uint16 {
	t.Helper()
	if err := srv.authorize(ctx, method); err != nil {
		return authzStatus(err)
	}
	h, ok := srv.handlers[method]
	if !ok {
		t.Fatalf("no handler for %s", method)
	}
	if _, err := h(ctx, []byte(payload)); err != nil {
		code, _ := mapServiceError(err)
		return code
	}
	return wire.StatusOK
}

// TestTenantAdmin_ActsInOwnTenantOnly: acme's administrator updates acme's
// user, and an update of globex's user is answered not-found and reaches no
// store.
func TestTenantAdmin_ActsInOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name   string
		caller *auth.Identity
		id     uint64
		want   uint16
	}{
		{name: "tenant admin, own tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInBindingTenant, want: wire.StatusOK},
		{name: "tenant admin, another tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInOtherTenant, want: wire.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users, ctx := bindingServer(t, tt.caller)
			got := bindingCall(t, srv, ctx, "user.Update", `{"id":`+strconv.FormatUint(tt.id, 10)+`,"name":"renamed"}`)
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			if reached := len(users.Updated()) > 0; reached != (tt.want == wire.StatusOK) {
				t.Fatalf("store reached = %v, want %v", reached, tt.want == wire.StatusOK)
			}
		})
	}
}

// TestCrossTenantBinding_WriteRefused: writing a RoleBinding that names any
// tenant other than the user's own is refused with StatusInvalidArgument
// (*user.RoleBindingTenantError) and stored nowhere, while an own-tenant
// binding is written.
func TestCrossTenantBinding_WriteRefused(t *testing.T) {
	tests := []struct {
		name    string
		binding string
		want    uint16
	}{
		{name: "another tenant", binding: `{"roleID":"` + role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin) + `","tenantID":"` + authtest.OtherTenant + `"}`, want: wire.StatusInvalidArgument},
		{name: "the system tenant", binding: `{"roleID":"admin","tenantID":"system"}`, want: wire.StatusInvalidArgument},
		{name: "own tenant", binding: `{"roleID":"` + role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser) + `","tenantID":"` + authtest.BindingTenant + `"}`, want: wire.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users, ctx := bindingServer(t, authtest.BindingTenantAdmin())
			got := bindingCall(t, srv, ctx, "user.Update", `{"id":`+strconv.FormatUint(authtest.UserInBindingTenant, 10)+`,"roleBindings":[`+tt.binding+`]}`)
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			stored := len(users.User(authtest.UserInBindingTenant).RoleBindings) > 0
			if stored != (tt.want == wire.StatusOK) {
				t.Fatalf("binding stored = %v, want %v", stored, tt.want == wire.StatusOK)
			}
		})
	}
}
