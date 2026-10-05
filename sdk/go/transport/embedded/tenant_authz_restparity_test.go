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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

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
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))

	restSrv := rest.NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0",
		rest.WithTenantService(tenants),
		rest.WithAuthMiddleware(mw),
	)
	embeddedTr := newExtTransport(t, WithTenantService(tenants), WithAuthMiddleware(mw))

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
// agree on every admission decision.
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
