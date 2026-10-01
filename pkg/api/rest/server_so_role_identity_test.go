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

package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// go-qrdb#569 on the transport the SO gate guards
//
// The SO dual-factor gate is installed on the REST handler only (HTTP/2 and
// HTTP/3 serve the same handler). These drive the REST server's full chain --
// real mTLS authenticator, real user store, real RBAC authorizer over the real
// role store, real SOAuthenticator -- with a real SO user whose record carries
// the SO role's persisted identifier.
// ---------------------------------------------------------------------------

const (
	soRESTTenant = tenant.SystemTenantID
	soRESTPIN    = "correct horse battery staple"
)

type soRESTFixture struct {
	srv   *Server
	roles role.RoleServicer
	users user.UserServicer
}

func newSORESTFixture(t *testing.T) *soRESTFixture {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), soRESTTenant)
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	if _, err := roles.ReconcileBuiltInRoles(ctx, soRESTTenant); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	sink := audit.NewRingBuffer(64)
	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), sink, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := verifier.SetSOPIN(ctx, soRESTTenant, []byte(soRESTPIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	mw := authmw.New(rbac.New(roles),
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
		authmw.WithUserService(users),
	)
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithAuthMiddleware(mw),
		WithRoleService(roles),
		WithAuditLogger(sink),
		WithSOAuthenticator(verifier),
		WithSODualFactor(),
		WithBarrierService(newExtBarrierSvc()),
	)
	return &soRESTFixture{srv: srv, roles: roles, users: users}
}

// user creates username in the tenant holding the named built-in role, by the
// identifier the role store persisted it under, and returns a client
// certificate naming that user.
func (f *soRESTFixture) user(t *testing.T, username, roleName string) *x509.Certificate {
	t.Helper()
	r, err := f.roles.GetRoleByName(tenant.WithTenantID(context.Background(), soRESTTenant), soRESTTenant, roleName)
	if err != nil {
		t.Fatalf("GetRoleByName(%s): %v", roleName, err)
	}
	if _, err := f.users.CreateUser(context.Background(), &user.CreateUserRequest{
		Username:             username,
		TenantID:             soRESTTenant,
		WithoutEmailLoginKey: true,
		RoleIDs:              []string{r.ID},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: username, OrganizationalUnit: []string{soRESTTenant}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func (f *soRESTFixture) post(cert *x509.Certificate, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	req.Header.Set(authmw.SOPINHeader, soRESTPIN)
	rec := httptest.NewRecorder()
	f.srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// soRESTRoute is mapped to permission.SealRekey, which is in SOPermissions.
const soRESTRoute = "/v1/barrier/rekey"

// TestSODualFactor_RealSOPassesGate: a real officer, over mTLS with the
// right PIN, passes the SO gate and reaches the rekey handler.
func TestSODualFactor_RealSOPassesGate(t *testing.T) {
	f := newSORESTFixture(t)
	rec := f.post(f.user(t, "officer", role.RoleNameSO), soRESTRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("real SO refused: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestSODualFactor_RealAdminRefused: a real non-SO user is refused on the
// same route with the same PIN.
func TestSODualFactor_RealAdminRefused(t *testing.T) {
	f := newSORESTFixture(t)
	if rec := f.post(f.user(t, "boss", role.RoleNameAdmin), soRESTRoute); rec.Code != http.StatusForbidden {
		t.Fatalf("admin status=%d body=%q, want 403", rec.Code, rec.Body.String())
	}
}

// TestSODualFactor_AuthorizedNonSOCallerRefusedByTheGate: a caller RBAC admits
// to seal:rekey without the SO role -- a token principal granted it -- is
// refused by the gate, and served with the gate off. The same scenario runs on
// gRPC, Unix, QUIC and MCP (their so_dual_factor_test.go).
func TestSODualFactor_AuthorizedNonSOCallerRefusedByTheGate(t *testing.T) {
	for _, gated := range []bool{true, false} {
		caller := authtest.NewFixedAuthenticator(authtest.GrantedNonSOIdentity(soRESTTenant, "token", permission.SealRekey))
		roles := authtest.BuiltInRoles(t, soRESTTenant)
		opts := []ServerOption{
			WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))),
			WithRoleService(roles),
			WithBarrierService(newExtBarrierSvc()),
		}
		if gated {
			opts = append(opts, WithSOAuthenticator(authtest.SOVerifier(t, soRESTPIN, soRESTTenant)), WithSODualFactor())
		}
		srv := NewServer(nil, nil, "127.0.0.1:0", opts...)
		req := httptest.NewRequest(http.MethodPost, soRESTRoute, strings.NewReader("{}"))
		req.TLS = &tls.ConnectionState{}
		req.Header.Set(authmw.SOPINHeader, soRESTPIN)
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, req)
		want := http.StatusOK
		if gated {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Fatalf("gated=%v: status=%d body=%q, want %d", gated, rec.Code, rec.Body.String(), want)
		}
	}
}

// TestSODualFactor_TenantSORefusedOnTheNodeBarrier: the node barrier is a
// platform singleton, so an SO of tenant acme presenting acme's own valid PIN
// must not rekey it -- with the gate on or off (go-qrdb#569 N2).
func TestSODualFactor_TenantSORefusedOnTheNodeBarrier(t *testing.T) {
	for _, gated := range []bool{true, false} {
		caller := authtest.NewFixedAuthenticator(authtest.SOIdentity("acme", "acme-officer"))
		roles := authtest.BuiltInRoles(t, "acme")
		opts := []ServerOption{
			WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))),
			WithRoleService(roles),
			WithBarrierService(newExtBarrierSvc()),
		}
		if gated {
			opts = append(opts, WithSOAuthenticator(authtest.SOVerifier(t, soRESTPIN, "acme")), WithSODualFactor())
		}
		srv := NewServer(nil, nil, "127.0.0.1:0", opts...)
		req := httptest.NewRequest(http.MethodPost, soRESTRoute, strings.NewReader("{}"))
		req.TLS = &tls.ConnectionState{}
		req.Header.Set(authmw.SOPINHeader, soRESTPIN)
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("gated=%v: status=%d body=%q, want 403", gated, rec.Code, rec.Body.String())
		}
	}
}

// TestBarrierSeal_TenantAdminRefused: the node barrier is platform scope, so
// a tenant's Admin -- who holds seal:seal in its own tenant -- may not seal
// it, while the system tenant's Admin may.
func TestBarrierSeal_TenantAdminRefused(t *testing.T) {
	for _, tc := range []struct {
		id   *auth.Identity
		want int
	}{
		{authtest.AdminIdentity("acme", "acme-admin"), http.StatusForbidden},
		{authtest.AdminIdentity(tenant.SystemTenantID, "root"), http.StatusNoContent},
	} {
		caller := authtest.NewFixedAuthenticator(tc.id)
		roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, "acme")
		srv := NewServer(nil, nil, "127.0.0.1:0",
			WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))),
			WithRoleService(roles), WithBarrierService(newExtBarrierSvc()))
		req := httptest.NewRequest(http.MethodPost, "/v1/barrier/seal", nil)
		req.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		srv.server.Handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s/%s seal: status=%d body=%q, want %d", tc.id.TenantID, tc.id.Subject, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

// TestSODualFactor_GateWithoutAuthFailsClosed: with the gate on and no auth
// middleware there is no SO to admit; the gate refuses, as on the other four
// transports (go-qrdb#569 N5).
func TestSODualFactor_GateWithoutAuthFailsClosed(t *testing.T) {
	roles := authtest.BuiltInRoles(t, soRESTTenant)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithRoleService(roles), WithBarrierService(newExtBarrierSvc()),
		WithSOAuthenticator(authtest.SOVerifier(t, soRESTPIN, soRESTTenant)), WithSODualFactor())
	req := httptest.NewRequest(http.MethodPost, soRESTRoute, strings.NewReader("{}"))
	req.TLS = &tls.ConnectionState{}
	req.Header.Set(authmw.SOPINHeader, soRESTPIN)
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%q, want 403", rec.Code, rec.Body.String())
	}
}
