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
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestRouteCheck_BindingGrantsInItsOwnTenantOnly drives PUT /users/{id}
// through the full HTTP handler chain as a system-tenant operator whose only
// grant is a binding for tenant acme. The route-level check must resolve the
// user's tenant before authorizing: the acme user is updated, the globex user
// is refused and never reaches the store.
func TestRouteCheck_BindingGrantsInItsOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name   string
		id     uint64
		status int
	}{
		{name: "user in the bound tenant", id: authtest.UserInBindingTenant, status: http.StatusOK},
		{name: "user in another tenant", id: authtest.UserInOtherTenant, status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
				WithUserService(users),
				WithAuthMiddleware(authmw.New(rbac.New(authtest.BindingRoles(t)),
					authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
					authmw.WithUserService(users))),
			)
			req := httptest.NewRequest(http.MethodPut, "/users/"+strconv.FormatUint(tt.id, 10), strings.NewReader(`{"name":"renamed"}`))
			rec := httptest.NewRecorder()
			srv.server.Handler.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if reached := len(users.Updated()) > 0; reached != (tt.status == http.StatusOK) {
				t.Fatalf("store reached = %v, want %v", reached, tt.status == http.StatusOK)
			}
		})
	}
}
