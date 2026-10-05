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

// Parity between the embedded transport and REST over the SAME
// authorization matrix: one *authtest.Tenants service, one AuthMiddleware
// over the REAL built-in roles, driven both through an httptest REST server
// and through a direct embedded Transport call. If the two ever disagree on
// a decision, the embedded transport is no longer "the same authorization
// decision the network transports use" -- the owner's explicit requirement
// for this fix.
package embedded

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// parityGlobexUserID is the fixed user id newParityFixture's shared
// mockUserServicer resolves to a globex-tenant record, so GetUser's
// target-resolution ("which tenant does this user belong to") goes through
// the exact same path REST and embedded both use (UserTarget) rather than a
// request field naming the tenant directly, matching the "Users" client
// group the GetTenant/DeleteTenant cases don't exercise.
const parityGlobexUserID = 42

// newParityUserService returns the mockUserServicer shared by newParityFixture's
// AuthMiddleware (to resolve UserTarget AND, since a UserService is now
// wired, finalizeIdentity's own caller-enrichment lookup), REST server, and
// embedded Transport -- one record set, one resolution, consulted
// identically by both transports.
//
// It also answers GetUserByUsername for the two caller identities the test
// matrix drives (acme's "acme-admin", system's "root"): wiring a
// UserService onto AuthMiddleware makes authenticate() resolve the CALLER
// through the SAME store (middleware.go's "Enrich identity with persistent
// role assignments" step overwrites Identity.Roles from the record), so a
// caller lookup that failed would silently drop the caller's own Admin role
// and turn every op's parity check into a false denial rather than a real
// authorization comparison.
func newParityUserService() *mockUserServicer {
	adminFor := func(id uint64, tenantID, username string) *user.User {
		return &user.User{
			ID:        id,
			Username:  username,
			TenantID:  tenantID,
			RoleIDs:   []string{role.SystemRoleID(tenantID, role.RoleNameAdmin)},
			Lifecycle: user.LifecycleActive,
		}
	}
	callers := map[string]*user.User{
		tenantid.Normalize("acme") + "\x00" + "acme-admin":    adminFor(1, "acme", "acme-admin"),
		tenantid.Normalize(tenantid.System) + "\x00" + "root": adminFor(2, tenantid.System, "root"),
	}
	return &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != parityGlobexUserID {
				return nil, &user.UserNotFoundError{ID: id}
			}
			u := testUser()
			u.ID = parityGlobexUserID
			u.TenantID = "globex"
			return u, nil
		},
		getUserByUserFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			if u, ok := callers[tenantid.Normalize(tenantID)+"\x00"+username]; ok {
				return u, nil
			}
			return nil, &user.UserNotFoundError{ID: 0}
		},
	}
}

// parityFixture wires one *authtest.Tenants service and one AuthMiddleware
// into both a REST server (driven over HTTP, through an httptest recorder)
// and an embedded Transport (driven with a direct Go call), so a single
// operation can be issued against both and compared.
type parityFixture struct {
	restSrv  *rest.Server
	embedded *Transport
	caller   *authtest.FixedAuthenticator
}

func newParityFixture(t *testing.T) *parityFixture {
	t.Helper()
	tenants := authtest.NewTenants(t, tenantid.System, "acme", "globex")
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	userSvc := newParityUserService()
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller), authmw.WithUserService(userSvc))

	restSrv := rest.NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0",
		rest.WithTenantService(tenants),
		rest.WithAuthMiddleware(mw),
		rest.WithUserService(userSvc),
	)
	embeddedTr := newExtTransport(t, WithTenantService(tenants), WithAuthMiddleware(mw), WithUserService(userSvc))

	return &parityFixture{restSrv: restSrv, embedded: embeddedTr, caller: caller}
}

func (f *parityFixture) as(id *auth.Identity) { f.caller.Set(id) }

// restStatus issues method/path against the REST server and reports the HTTP
// status.
func (f *parityFixture) restStatus(method, path, body string) int {
	rec := httptest.NewRecorder()
	f.restSrv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec.Code
}

// embeddedCtx binds id as the embedded transport's caller, the same pair
// REST's finalizeIdentity binds from the authenticator f.caller stands in
// for.
func (f *parityFixture) embeddedCtx(id *auth.Identity) context.Context {
	return ctxFor(id)
}

// admitted reports whether status is a success code (REST's "admitted"
// outcome for this surface: 200/201/204 on every route under test).
func admitted(status int) bool {
	return status == http.StatusOK || status == http.StatusCreated || status == http.StatusNoContent
}

// TestEmbeddedVsREST_TenantAuthorizationParity runs the probe matrix --
// acme's Admin acting on globex, acme's Admin acting on acme, and the
// system Admin acting on globex -- through both transports and asserts they
// agree on every admission decision. GetTenant/DeleteTenant exercise the
// "tenant" client group, whose request names the target tenant directly;
// GetUser extends the matrix to the "Users" group, whose target is instead
// RESOLVED (authmw.UserTarget / RESTPathUser look up the record's owning
// tenant), the same shape every userID-addressed route on every client
// group needing target resolution (roles, factors) ultimately shares --
// see item 10 of the go-qrdb#585 security review.
func TestEmbeddedVsREST_TenantAuthorizationParity(t *testing.T) {
	type op struct {
		name       string
		restMethod string
		restPath   string
		restBody   string
		embedded   func(ctx context.Context, tr *Transport) error
	}
	ops := []op{
		{"GetTenant", http.MethodGet, "/v1/tenants/globex", "", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetTenant(ctx, "globex")
			return err
		}},
		{"DeleteTenant", http.MethodDelete, "/v1/tenants/globex", "", func(ctx context.Context, tr *Transport) error {
			return tr.DeleteTenant(ctx, "globex")
		}},
		{"GetUser", http.MethodGet, "/users/42", "", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUser(ctx, parityGlobexUserID)
			return err
		}},
	}

	identities := []struct {
		name string
		id   *auth.Identity
	}{
		{"acme-admin", authtest.AdminIdentity("acme", "acme-admin")},
		{"system-admin", authtest.AdminIdentity(tenantid.System, "root")},
	}

	for _, o := range ops {
		for _, who := range identities {
			t.Run(o.name+"/"+who.name, func(t *testing.T) {
				// Fresh fixture per case: DeleteTenant is destructive, and a
				// shared globex record must start intact for both sides.
				f := newParityFixture(t)
				f.as(who.id)

				restCode := f.restStatus(o.restMethod, o.restPath, o.restBody)
				restAdmitted := admitted(restCode)

				embErr := o.embedded(f.embeddedCtx(who.id), f.embedded)
				embAdmitted := embErr == nil

				if restAdmitted != embAdmitted {
					t.Fatalf("%s as %s: REST admitted=%v (status %d), embedded admitted=%v (err %v) -- the two transports disagree",
						o.name, who.name, restAdmitted, restCode, embAdmitted, embErr)
				}
			})
		}
	}
}
