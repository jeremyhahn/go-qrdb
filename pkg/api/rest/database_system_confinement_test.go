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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestDatabaseRoutes_SystemTenantGetsNotFoundForAnotherTenantsDatabase: over
// REST, with the real RBAC and built-in roles, a system-tenant Admin, Security
// Officer or Auditor holding every database permission is answered 404 on
// another tenant's database, on every database verb -- the data verbs, seal,
// unseal and the share-holder routes -- exactly as for a missing database,
// and the database is left as it was.
func TestDatabaseRoutes_SystemTenantGetsNotFoundForAnotherTenantsDatabase(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/databases/vault", ""},
		{http.MethodDelete, "/v1/databases/vault", ""},
		{http.MethodPatch, "/v1/databases/vault/sql", `{"sql":{}}`},
		{http.MethodPost, "/v1/databases/vault/seal", ""},
		{http.MethodPost, "/v1/databases/vault/unseal", `{"secret":"c2VjcmV0"}`},
		{http.MethodGet, "/v1/databases/vault/barrier/status", ""},
		{http.MethodGet, "/v1/databases/vault/barrier/officers", ""},
		{http.MethodPost, "/v1/databases/vault/barrier/quorum/begin", `{"op":"unseal","username":"alice"}`},
		{http.MethodPost, "/v1/databases/vault/barrier/rekey/start", `{"threshold":2}`},
	}
	for name, id := range authtest.SystemDatabaseCallers() {
		t.Run(name, func(t *testing.T) {
			fx := servicetest.NewShamirDatabaseOwnedBy(t, "acme", "vault", 2, 2)
			roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
			mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
			srv := NewServer(nil, nil, "127.0.0.1:0",
				WithTenantService(authtest.NewTenants(t, tenantid.System, "acme")),
				WithDatabaseService(fx.Service),
				WithAuthMiddleware(mw))
			for _, r := range routes {
				rec := httptest.NewRecorder()
				srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, strings.NewReader(r.body)))
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s %s = %d %s; want 404, as for a missing database", r.method, r.path, rec.Code, rec.Body.String())
				}
			}
			if _, err := fx.Service.Get(fx.Name); err != nil {
				t.Fatalf("acme's database was changed: %v", err)
			}
		})
	}
}
