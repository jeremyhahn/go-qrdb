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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// go-qrdb#1042: the SO tenant create enrolls is a real Security Officer
//
// The tenant, its built-in roles, its admin and its SO are written by
// bootstrap.ProvisionTenant into the production role and user stores, and the
// SO's PIN is committed through the production SOAuthenticator. Only the CSR
// signer is a double, issuing each account a certificate minted for its name.
// ---------------------------------------------------------------------------

const onboardedSOTenant = "acme"

// onboardedTenants is the tenant-creation seam: it records nothing a gate
// reads, so it creates the tenant by answering for it.
type onboardedTenants struct{}

func (onboardedTenants) CreateTenant(_ context.Context, id, _ string, isolation tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationType: isolation}, nil
}

// mintingSigner signs each account's CSR with a certificate minted for the
// account's name in the tenant.
type mintingSigner struct {
	t *testing.T
}

func (s *mintingSigner) SignAdminCSR(_ context.Context, tenantID, subject string, _ []byte) ([]byte, error) {
	ck := mintCert(s.t, subject, tenantID, nil, "")
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ck.cert.Raw}), nil
}

// newCSRPEM is a PEM CSR over a fresh P-256 key.
func newCSRPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatalf("create CSR for %s: %v", cn, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// onboardedSOFixture is a tenant provisioned with an admin and an SO.
type onboardedSOFixture struct {
	*soRealFixture
	result *bootstrap.TenantProvisionResult
}

func newOnboardedSOFixture(t *testing.T) *onboardedSOFixture {
	t.Helper()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), discardLogger())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	f := &onboardedSOFixture{
		soRealFixture: &soRealFixture{roles: roles, users: newRealUserStore(t), verifier: verifier},
	}
	ctx := tenant.WithTenantID(context.Background(), onboardedSOTenant)
	f.result, err = bootstrap.ProvisionTenant(ctx, bootstrap.TenantProvisionConfig{
		TenantID:    onboardedSOTenant,
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: newCSRPEM(t, "admin"),
		SO:          &bootstrap.TenantSOConfig{Username: "officer", CSRPEM: newCSRPEM(t, "officer"), PIN: []byte(soGatePIN)},
	}, bootstrap.Dependencies{
		Tenants:     onboardedTenants{},
		Users:       f.users,
		Roles:       roles,
		Signer:      &mintingSigner{t: t},
		SOPINs:      verifier,
		TrustDomain: authtest.ClientTrustDomain,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	return f
}

// caller is the identity authentication builds for the named account: its
// tenant and the role ids on its record.
func (f *onboardedSOFixture) caller(t *testing.T, id uint64) context.Context {
	t.Helper()
	u, err := f.users.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: u.Username, TenantID: u.TenantID, Roles: u.RoleIDs,
	})
	return tenant.WithTenantID(ctx, u.TenantID)
}

// TestOnboardedSO_PassesTheSOGate: the SO gate (so_dual_factor) admits the
// SO tenant create enrolled, presenting the PIN it was created with, and
// refuses the admin presenting the same PIN. The gate is driven on its own:
// the SO-gated permissions it is asked about are the node barrier's, which
// RBAC grants to no tenant identity (ScopeCross), so the outer RBAC layer
// would refuse both before the gate is reached.
func TestOnboardedSO_PassesTheSOGate(t *testing.T) {
	f := newOnboardedSOFixture(t)
	gated := func(ctx context.Context) (int, bool) {
		var ran bool
		h := SOMiddleware(testRESTRoutes(), f.roles, f.verifier, nil, audit.FailClosed, discardLogger(),
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				ran = true
				w.WriteHeader(http.StatusOK)
			}))
		req := httptest.NewRequest(http.MethodPost, soGateRoute, nil).WithContext(ctx)
		req.TLS = &tls.ConnectionState{}
		req.Header.Set(SOPINHeader, soGatePIN)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, ran
	}
	if code, ran := gated(f.caller(t, f.result.SOUserID)); code != http.StatusOK || !ran {
		t.Fatalf("SO: status = %d, ran = %v; want 200 and the handler run", code, ran)
	}
	if code, ran := gated(f.caller(t, f.result.AdminUserID)); code != http.StatusForbidden || ran {
		t.Fatalf("admin: status = %d, ran = %v; want 403 and the handler not run", code, ran)
	}
}

// TestOnboardedSO_GrantsTheSORoleAndTheAdminCannot: the onboarded SO may
// grant its tenant's so role; the onboarded admin is refused the same grant.
func TestOnboardedSO_GrantsTheSORoleAndTheAdminCannot(t *testing.T) {
	f := newOnboardedSOFixture(t)
	m := newTestMiddleware(rbac.New(f.roles), WithUserService(f.users), WithRoleResolver(f.roles), WithStrictSeparation(true))
	grant := []string{role.SystemRoleID(onboardedSOTenant, role.RoleNameSO)}

	if err := m.authorizeSORoleChange(f.caller(t, f.result.SOUserID), onboardedSOTenant, nil, grant); err != nil {
		t.Fatalf("SO granting so = %v, want admitted", err)
	}
	var refused *SORoleRequiredError
	if err := m.authorizeSORoleChange(f.caller(t, f.result.AdminUserID), onboardedSOTenant, nil, grant); !errors.As(err, &refused) {
		t.Fatalf("admin granting so = %v, want *SORoleRequiredError", err)
	}
}
