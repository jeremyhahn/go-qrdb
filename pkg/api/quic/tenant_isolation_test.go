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

// Tenant isolation on the tenant-administration surface, over a real QUIC
// connection.
//
// Every test drives a real QUIC server and client: the dispatch permission
// check, a REAL rbac.RBACAuthorizer over the REAL built-in roles and a REAL
// in-memory tenant service. The only double is the authenticator, standing in
// for the mTLS handshake.
//
// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) are unconstrained, and this surface ships
// in every build.

package quic

import (
	"crypto/tls"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// isolationFixture is one started QUIC server holding the system tenant, acme
// and globex, with an acme Admin as the caller until as() rebinds it.
type isolationFixture struct {
	addr      string
	clientCfg *tls.Config
	tenants   *authtest.Tenants
	caller    *authtest.FixedAuthenticator
}

func newIsolationFixture(t *testing.T, opts ...authmw.Option) *isolationFixture {
	t.Helper()
	tenants := authtest.NewTenants(t, tenantid.System, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	mw := authmw.New(rbac.New(roles), append([]authmw.Option{authmw.WithAuthenticator(caller)}, opts...)...)

	// Mutual TLS, as in production: the QUIC server refuses a connection with
	// no client certificate before the authenticator is consulted.
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(serverCfg), WithTenantService(tenants), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return &isolationFixture{addr: srv.Addr(), clientCfg: clientCfg, tenants: tenants, caller: caller}
}

func (f *isolationFixture) as(id *auth.Identity) { f.caller.Set(id) }

func (f *isolationFixture) operator() { f.as(authtest.AdminIdentity(tenantid.System, "root")) }

// call sends one frame and returns its status and payload.
func (f *isolationFixture) call(t *testing.T, method string, req proto.Message) (uint16, []byte) {
	t.Helper()
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	return credentialE2ERoundTrip(t, f.addr, f.clientCfg, method, payload)
}

func wantStatus(t *testing.T, what string, got, want uint16) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = status %d, want %d", what, got, want)
	}
}

// byIDFrames returns the tenant-by-id frames against id.
func byIDFrames(id string) []struct {
	name, method string
	req          proto.Message
} {
	return []struct {
		name, method string
		req          proto.Message
	}{
		{"get", "tenant.Get", &pb.TenantGetRequest{Id: id}},
		{"update", "tenant.Update", &pb.TenantUpdateRequest{Id: id, Name: "pwned"}},
		{"quotas", "tenant.UpdateQuotas", &pb.TenantUpdateQuotasRequest{Id: id, Quotas: &pb.TenantQuotas{MaxDatabases: 1000}}},
		{"entitlements", "tenant.UpdateEntitlements", &pb.TenantUpdateEntitlementsRequest{Id: id, Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}}},
		{"delete", "tenant.Delete", &pb.TenantDeleteRequest{Id: id}},
	}
}

// unchanged fails unless id still exists with its original name and no plan.
func (f *isolationFixture) unchanged(t *testing.T, id string) {
	t.Helper()
	got, err := f.tenants.GetTenant(t.Context(), id)
	if err != nil {
		t.Fatalf("%s after a refused frame: %v", id, err)
	}
	if got.Name != id || got.Quotas != nil || got.Entitlements != nil || got.IsolationType != tenant.IsolationPrefix {
		t.Fatalf("a refused frame changed %s: %+v", id, got)
	}
}

func TestTenantIsolation_TenantAdminCannotActOnAnotherTenant(t *testing.T) {
	for _, fr := range byIDFrames("globex") {
		t.Run(fr.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			status, _ := f.call(t, fr.method, fr.req)
			wantStatus(t, "acme admin "+fr.name+" globex", status, wire.StatusPermissionDenied)
			f.unchanged(t, "globex")
		})
	}
}

func TestTenantIsolation_TenantAdminCannotChangeItsOwnPlan(t *testing.T) {
	for _, fr := range []struct {
		name, method string
		req          proto.Message
	}{
		{"quotas", "tenant.UpdateQuotas", &pb.TenantUpdateQuotasRequest{Id: "acme", Quotas: &pb.TenantQuotas{MaxDatabases: 1000}}},
		{"entitlements", "tenant.UpdateEntitlements", &pb.TenantUpdateEntitlementsRequest{Id: "acme", Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}}},
		{"isolation", "tenant.Update", &pb.TenantUpdateRequest{Id: "acme", IsolationType: string(tenant.IsolationBarrier)}},
	} {
		t.Run(fr.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			status, _ := f.call(t, fr.method, fr.req)
			wantStatus(t, "acme admin setting acme's "+fr.name, status, wire.StatusPermissionDenied)
			f.unchanged(t, "acme")
		})
	}
}

func TestTenantIsolation_TenantAdminServedOnItsOwnTenant(t *testing.T) {
	f := newIsolationFixture(t)
	status, _ := f.call(t, "tenant.Get", &pb.TenantGetRequest{Id: "acme"})
	wantStatus(t, "acme admin get acme", status, wire.StatusOK)
	status, _ = f.call(t, "tenant.Update", &pb.TenantUpdateRequest{Id: "acme", Name: "Acme Corp"})
	wantStatus(t, "acme admin renaming acme", status, wire.StatusOK)
}

func TestTenantIsolation_PlatformOperatorSetsAPlanAndCreates(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	status, _ := f.call(t, "tenant.UpdateQuotas", &pb.TenantUpdateQuotasRequest{Id: "acme", Quotas: &pb.TenantQuotas{MaxDatabases: 1000}})
	wantStatus(t, "operator setting acme's quotas", status, wire.StatusOK)
	status, _ = f.call(t, "tenant.UpdateEntitlements", &pb.TenantUpdateEntitlementsRequest{Id: "acme", Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}})
	wantStatus(t, "operator granting acme entitlements", status, wire.StatusOK)
	status, _ = f.call(t, "tenant.Create", &pb.TenantCreateRequest{Id: "initech", Name: "initech"})
	wantStatus(t, "operator creating a tenant", status, wire.StatusOK)

	got, err := f.tenants.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Quotas == nil || got.Quotas.MaxDatabases != 1000 || got.Entitlements == nil || !got.Entitlements.AllowDedicatedWAL {
		t.Fatalf("the operator's plan did not reach acme: %+v", got)
	}
}

func TestTenantIsolation_IsolationChangeRefusedWhileTenantHoldsData(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	req := &pb.TenantUpdateRequest{Id: "acme", IsolationType: string(tenant.IsolationVolume)}
	f.tenants.HoldsData(t, true)
	status, _ := f.call(t, "tenant.Update", req)
	wantStatus(t, "operator changing a data-bearing tenant's isolation", status, wire.StatusFailedPrecondition)
	f.unchanged(t, "acme")
	f.tenants.HoldsData(t, false)
	status, _ = f.call(t, "tenant.Update", req)
	wantStatus(t, "operator changing an empty tenant's isolation", status, wire.StatusOK)
}

func TestTenantIsolation_TenantAdminCannotCreateATenant(t *testing.T) {
	f := newIsolationFixture(t)
	status, _ := f.call(t, "tenant.Create", &pb.TenantCreateRequest{Id: "initech", Name: "initech"})
	wantStatus(t, "acme admin create", status, wire.StatusPermissionDenied)
	if _, err := f.tenants.GetTenant(t.Context(), "initech"); err == nil {
		t.Fatal("a refused create left a tenant behind")
	}
}

func listedTenantIDs(t *testing.T, f *isolationFixture) []string {
	t.Helper()
	status, payload := credentialE2ERoundTrip(t, f.addr, f.clientCfg, "tenant.List", nil)
	wantStatus(t, "list", status, wire.StatusOK)
	var resp pb.TenantListResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]string, len(resp.GetTenants()))
	for i, tr := range resp.GetTenants() {
		ids[i] = tr.GetId()
	}
	return ids
}

func TestTenantIsolation_ListShowsATenantAdminOnlyItsOwnTenant(t *testing.T) {
	f := newIsolationFixture(t)
	if ids := listedTenantIDs(t, f); len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("acme admin listed %v, want [acme]", ids)
	}
	f.operator()
	if ids := listedTenantIDs(t, f); len(ids) != 3 {
		t.Fatalf("operator listed %v, want all three tenants", ids)
	}
}

func TestTenantIsolation_SlashInTenantIDIsInvalidArgument(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	for _, fr := range byIDFrames("acme/evil") {
		status, _ := f.call(t, fr.method, fr.req)
		wantStatus(t, fr.name+" acme/evil", status, wire.StatusInvalidArgument)
	}
	status, _ := f.call(t, "tenant.Create", &pb.TenantCreateRequest{Id: "acme/evil", Name: "x"})
	wantStatus(t, "create acme/evil", status, wire.StatusInvalidArgument)
}

// TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit: the sealed
// window admits a tenant's own role-less identity to unseal, but it must not
// admit it past the limiter. Before the fix the second unseal reached the
// service.
func TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	f := newIsolationFixture(t,
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(permission.TenantBarrierUnseal)))
	f.as(authtest.SealedIdentity("acme", "acme-admin"))

	payload, err := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("key")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, _ := credentialE2ERoundTrip(t, f.addr, f.clientCfg, "tenant.BarrierUnseal", payload)
	wantStatus(t, "first unseal", status, wire.StatusOK)
	status, _ = credentialE2ERoundTrip(t, f.addr, f.clientCfg, "tenant.BarrierUnseal", payload)
	wantStatus(t, "second unseal", status, wire.StatusResourceExhausted)
	if n := f.tenants.Unseals(); n != 1 {
		t.Fatalf("unseals reaching the service = %d, want 1", n)
	}
}
