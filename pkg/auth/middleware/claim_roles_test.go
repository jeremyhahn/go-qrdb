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

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// withFixedIdentity authenticates every request as id through
// authtest.FixedAuthenticator and resolves it through the authenticator's
// own user record: the chain takes roles only from a record (SPEC
// authz-claim-roles), so a fixed identity's roles need one.
func withFixedIdentity(id *auth.Identity) Option {
	fa := authtest.NewFixedAuthenticator(id)
	return func(m *AuthMiddleware) {
		WithAuthenticator(fa)(m)
		WithUserService(fa.Users())(m)
	}
}

// claimRolesTenant is the tenant of every claimed-role test.
const claimRolesTenant = "acme"

// claimedAdmin is an authenticator-produced identity claiming the tenant's
// Admin role, with RoleBindings riding along: none of it is user-record
// state, so none of it may survive the chain.
func claimedAdmin() *auth.Identity {
	admin := role.SystemRoleID(claimRolesTenant, role.RoleNameAdmin)
	return &auth.Identity{
		Subject:      "carol",
		TenantID:     claimRolesTenant,
		Roles:        []string{admin},
		RoleBindings: []auth.RoleBinding{{RoleID: admin, TenantID: claimRolesTenant}},
	}
}

// carolRecord is carol's user record holding roleIDs.
func carolRecord(roleIDs ...string) *user.User {
	return &user.User{ID: 7, Username: "carol", TenantID: claimRolesTenant, Lifecycle: user.LifecycleActive, RoleIDs: roleIDs}
}

// TestAuthenticate_ClaimedRolesNeverSurvive: on every path through
// finalizeIdentity -- record found, record not found, tenant sealed and no
// user store -- the authenticator's claimed roles are discarded; only a
// found record's RoleIDs reach the identity. Every transport (REST, gRPC,
// Unix, QUIC, MCP) authenticates through this one authenticate() call.
func TestAuthenticate_ClaimedRolesNeverSurvive(t *testing.T) {
	operator := role.SystemRoleID(claimRolesTenant, role.RoleNameOperator)
	cases := []struct {
		name   string
		users  user.UserServicer
		want   []string
		sealed bool
	}{
		{name: "record found with different roles", users: &mockUserServicer{user: carolRecord(operator)}, want: []string{operator}},
		{name: "record not found", users: &mockUserServicer{err: &user.UserNotFoundError{Lookup: "carol"}}},
		{name: "tenant sealed", users: &mockUserServicer{err: tenant.ErrTenantSealed}, sealed: true},
		{name: "no user store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: claimedAdmin()})}
			if tc.users != nil {
				opts = append(opts, WithUserService(tc.users))
			}
			m := newTestMiddleware(&mockAuthorizer{}, opts...)
			_, id, err := m.authenticate(context.Background())
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if !slices.Equal(id.Roles, tc.want) {
				t.Fatalf("Roles = %v, want %v (claimed roles must never survive)", id.Roles, tc.want)
			}
			if id.RoleBindings != nil {
				t.Fatalf("RoleBindings = %v, want none from the authenticator", id.RoleBindings)
			}
			if got := IdentityIAMSealed(id); got != tc.sealed {
				t.Fatalf("IdentityIAMSealed = %v, want %v", got, tc.sealed)
			}
		})
	}
}

// TestRESTMiddleware_Fallback_ClaimedRolesIgnored: a capability module's
// fallback identity (a token go-xkms minted) claiming Admin for a subject
// with no go-qrdb record is refused by the real RBAC authorizer and the
// handler never runs; the same subject with a record holding Admin passes.
func TestRESTMiddleware_Fallback_ClaimedRolesIgnored(t *testing.T) {
	admin := role.SystemRoleID(claimRolesTenant, role.RoleNameAdmin)
	cases := []struct {
		name  string
		users user.UserServicer
		want  int
	}{
		{name: "no record", users: &mockUserServicer{err: &user.UserNotFoundError{Lookup: "carol"}}, want: http.StatusForbidden},
		{name: "no user store", want: http.StatusForbidden},
		{name: "record holds admin", users: &mockUserServicer{user: carolRecord(admin)}, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithAuthenticator(&mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated})}
			if tc.users != nil {
				opts = append(opts, WithUserService(tc.users))
			}
			m := newTestMiddleware(rbac.New(authtest.BuiltInRoles(t, claimRolesTenant)), opts...)
			ran := false
			h := m.RESTMiddleware(fallbackRoutes(&mockFallback{identity: claimedAdmin()}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				ran = true
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/keys", nil))
			if rec.Code != tc.want || ran != (tc.want == http.StatusOK) {
				t.Fatalf("status = %d, handler ran = %v; want %d", rec.Code, ran, tc.want)
			}
		})
	}
}

// TestUnaryInterceptor_ClaimedRolesIgnored: the gRPC and Unix interceptor
// refuses an authenticator's claimed Admin role for a subject with no
// record (PermissionDenied, no handler) and admits the record's Admin.
func TestUnaryInterceptor_ClaimedRolesIgnored(t *testing.T) {
	admin := role.SystemRoleID(claimRolesTenant, role.RoleNameAdmin)
	cases := []struct {
		name  string
		users user.UserServicer
		want  codes.Code
	}{
		{name: "no record", users: &mockUserServicer{err: &user.UserNotFoundError{Lookup: "carol"}}, want: codes.PermissionDenied},
		{name: "no user store", want: codes.PermissionDenied},
		{name: "record holds admin", users: &mockUserServicer{user: carolRecord(admin)}, want: codes.OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: claimedAdmin()})}
			if tc.users != nil {
				opts = append(opts, WithUserService(tc.users))
			}
			m := newTestMiddleware(rbac.New(authtest.BuiltInRoles(t, claimRolesTenant)), opts...)
			ran := false
			handler := func(context.Context, any) (any, error) { ran = true; return nil, nil }
			info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/ListUsers"}
			_, err := m.UnaryInterceptor()(context.Background(), nil, info, handler)
			if status.Code(err) != tc.want || ran != (tc.want == codes.OK) {
				t.Fatalf("err = %v, handler ran = %v; want %v", err, ran, tc.want)
			}
		})
	}
}
