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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestNodeBarrierRoutes_TenantHolderRefused pins the route-to-permission
// wiring of the node barrier's lifecycle routes. For each route, a tenant's
// identity is given the route's permission -- its built-in SO role, plus a
// custom role of its own tenant granting the permission, so the
// grant is never what is missing -- and is refused with 403 before the
// service is reached. The same identity in the system tenant is not refused,
// so the 403 is the scope rule on the permission this route maps to, not a
// missing grant or an unregistered route.
func TestNodeBarrierRoutes_TenantHolderRefused(t *testing.T) {
	type holder func(tenantID string) *auth.Identity
	so := func(tenantID string) *auth.Identity { return authtest.SOIdentity(tenantID, "officer") }
	routes := []struct {
		path string
		perm permission.Permission
		who  holder
	}{
		{"/v1/barrier/initialize", permission.SealBootstrap, so},
		{"/v1/barrier/unseal", permission.SealUnseal, so},
		{"/v1/barrier/enable-encryption", permission.SealEnableEncryption, so},
		{"/v1/ceremony/sign-csr", permission.CeremonySignCSR, so},
	}
	for _, rt := range routes {
		for _, tid := range []string{authtest.NodeOfficerTenant, tenantid.System} {
			t.Run(rt.path+"/"+tid, func(t *testing.T) {
				roles := authtest.BuiltInRoles(t, tenantid.System, authtest.NodeOfficerTenant)
				id := authtest.Grant(t, roles, rt.who(tid), rt.perm)
				barrier := &authtest.Barrier{}
				ceremony := &authtest.Ceremony{}
				srv := NewServer(nil, nil, "127.0.0.1:0",
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))),
					WithBarrierService(barrier), WithCeremonyService(ceremony))
				req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(`{}`))
				req.TLS = &tls.ConnectionState{}
				rec := httptest.NewRecorder()
				srv.server.Handler.ServeHTTP(rec, req)

				if tid == tenantid.System {
					if rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
						t.Fatalf("system-tenant holder of %s: status=%d body=%q; the route must exist and admit it", rt.perm, rec.Code, rec.Body.String())
					}
					return
				}
				if rec.Code != http.StatusForbidden {
					t.Fatalf("tenant holder of %s: status=%d body=%q, want 403", rt.perm, rec.Code, rec.Body.String())
				}
				if barrier.Transitions() != 0 || len(ceremony.SOPINs()) != 0 {
					t.Fatalf("a refused %s reached the service", rt.path)
				}
			})
		}
	}
}

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
		// Revocation is a rekey that removes a holder: cross-scope, and
		// SO-PIN-gated by the service.
		{http.MethodPost, "/v1/barrier/officers/revoke", `{"domain":"unseal","username":"bob","so_pin":"so-pin-1234"}`, (*authtest.Barrier).Revocations},
	}
	for _, op := range ops {
		roles := authtest.BuiltInRoles(t, tenantid.System, authtest.NodeOfficerTenant)
		for _, c := range authtest.NodeOfficerCallers(t, roles) {
			t.Run(op.method+" "+op.path+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				barrier := &authtest.Barrier{PINs: authtest.BarrierPINBackend(t, "so-pin-1234")}
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
