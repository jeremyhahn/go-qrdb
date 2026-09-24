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
	"errors"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestRouteCheck_BindingGrantsInItsOwnTenantOnly drives user.Update through
// this server's own authorization step (authorize with the frame payload) as
// a system-tenant operator whose only grant is a binding for tenant acme: the
// acme user's update is authorized and reaches the store, the globex user's
// is refused.
func TestRouteCheck_BindingGrantsInItsOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name  string
		id    uint64
		allow bool
	}{
		{name: "user in the bound tenant", id: authtest.UserInBindingTenant, allow: true},
		{name: "user in another tenant", id: authtest.UserInOtherTenant, allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			mw := authmw.New(rbac.New(authtest.BindingRoles(t)),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
				authmw.WithUserService(users))
			srv := NewServer(nil, nil, "localhost:0", WithUserService(users), WithAuthMiddleware(mw))
			ctx, err := mw.AuthenticateQUIC(context.Background())
			if err != nil {
				t.Fatalf("AuthenticateQUIC: %v", err)
			}
			payload := []byte(`{"id":` + strconv.FormatUint(tt.id, 10) + `,"name":"renamed"}`)

			err = srv.authorize(ctx, "user.Update", payload)
			if !tt.allow {
				var authzErr *AuthorizationError
				if !errors.As(err, &authzErr) || !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatalf("authorize = %v, want an authorization denial", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorize = %v, want allowed", err)
			}
			if _, err := srv.handleUpdateUser(ctx, payload); err != nil {
				t.Fatalf("handleUpdateUser: %v", err)
			}
			if len(users.Updated()) != 1 {
				t.Fatalf("store updates = %v, want the acme user once", users.Updated())
			}
		})
	}
}

// TestIdentityTargets_ReadThePayload asserts the payload readers fall back to
// the caller's own tenant on a payload they cannot read.
func TestIdentityTargets_ReadThePayload(t *testing.T) {
	targets := identityTargets()
	for _, method := range []string{"user.Update", "user.Create", "role.Get", "tenant.BarrierSeal"} {
		if got := targets[method]([]byte("not json")); got != authmw.SelfTarget() {
			t.Errorf("%s on an unreadable payload = %+v, want the caller's own tenant", method, got)
		}
	}
	if got := jsonUser("id")([]byte(`{"id":"7"}`)); got != authmw.SelfTarget() {
		t.Errorf("a string id = %+v, want the caller's own tenant", got)
	}
}
