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
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNodeOfficerScope: the node barrier is a platform singleton, so only the
// system tenant may register its unseal or recovery officers or list them.
// These routes name no tenant, and under ScopeSelf the authorizer compared the
// caller's tenant with itself, so a tenant's SO -- or a tenant Admin its own
// tenant granted the officer permissions -- could plant officers that every
// later rekey or recovery-key generation hands a share of the node key.
func TestNodeOfficerScope(t *testing.T) {
	const register = `{"username":"planted","csr_pem":"csr"}`
	ops := []struct {
		method, path, body string
		hits               func(b *authtest.Barrier) int64
	}{
		{http.MethodPost, "/v1/barrier/officers", register, (*authtest.Barrier).OfficerRegistrations},
		{http.MethodPost, "/v1/barrier/recovery/officers", register, (*authtest.Barrier).RecoveryOfficerRegistrations},
		{http.MethodGet, "/v1/barrier/officers", "", (*authtest.Barrier).OfficerReads},
		{http.MethodGet, "/v1/barrier/officers/claim-status", "", (*authtest.Barrier).OfficerReads},
	}
	for _, op := range ops {
		roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, authtest.NodeOfficerTenant)
		for _, c := range authtest.NodeOfficerCallers(t, roles) {
			t.Run(op.method+" "+op.path+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				barrier := &authtest.Barrier{}
				srv := NewServer(nil, nil, "127.0.0.1:0",
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))),
					WithBarrierService(barrier))
				req := httptest.NewRequest(op.method, op.path, strings.NewReader(op.body))
				req.TLS = &tls.ConnectionState{}
				rec := httptest.NewRecorder()
				srv.server.Handler.ServeHTTP(rec, req)

				want, calls := http.StatusForbidden, int64(0)
				if c.Admitted {
					want, calls = http.StatusOK, 1
				}
				if rec.Code != want {
					t.Fatalf("status=%d body=%q, want %d", rec.Code, rec.Body.String(), want)
				}
				if got := op.hits(barrier); got != calls {
					t.Fatalf("barrier calls = %d, want %d", got, calls)
				}
			})
		}
	}
}
