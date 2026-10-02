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

// Which tenant a /certs/* call is allowed to act on.
//
// Every test here drives the FULL middleware chain (srv.server.Handler) with a
// REAL rbac.RBACAuthorizer, a REAL permission registry, a REAL
// cert.DefaultCertProvider and a REAL kvstore.MemoryKVStore shared by both
// tenants. The only doubles are the authenticator (which stands in for an mTLS
// handshake) and the role service (which stands in for the role store): neither
// participates in the decision under test. A narrower double would have hidden
// the defect, because the defect is that the real scope check is vacuous.
//
// No build constraint: newDefaultKVSvc and newDefaultAdminSvc
// (rest_auth_test.go) are unconstrained, and this mapping ships in every build.

package rest

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The two tenants every test in this file uses. Neither is the system tenant,
// which is the whole point: the system-tenant rule must not be what carries the
// decision.
const (
	certScopeTenantA = "acme"
	certScopeTenantB = "globex"
)

// certScopeAuthenticator stands in for the mTLS handshake, returning a fixed
// identity. It is a double for AUTHENTICATION only — every authorization step
// downstream of it is the real one.
type certScopeAuthenticator struct {
	mu sync.RWMutex
	id auth.Identity
}

func (a *certScopeAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	clone := a.id
	return &clone, nil
}

func (a *certScopeAuthenticator) Name() string { return "certscope-static" }

// as rebinds the authenticated caller for the next request.
func (a *certScopeAuthenticator) as(tenantID, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.id = auth.Identity{Subject: subject, TenantID: tenantID, Roles: []string{"admin"}}
}

var _ auth.Authenticator = (*certScopeAuthenticator)(nil)

// certScopeBarrier is a real AES-GCM seal.EncryptionBarrier. It really
// encrypts and really decrypts, so the CA key genuinely round-trips through
// it; nothing about the tenancy decision under test depends on it.
type certScopeBarrier struct {
	key []byte
}

func newCertScopeBarrier(t *testing.T) *certScopeBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("barrier key: %v", err)
	}
	return &certScopeBarrier{key: key}
}

func (b *certScopeBarrier) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (b *certScopeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- NewGCMWithRandomNonce samples and prepends its own nonce; a non-nil nonce here panics.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *certScopeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *certScopeBarrier) IsSealed() bool { return false }

// certScopeFixture is one node: one shared store, one CA, two tenants.
type certScopeFixture struct {
	srv    *Server
	prov   *cert.DefaultCertProvider
	store  *kvstore.MemoryKVStore
	caller *certScopeAuthenticator
}

// newCertScopeFixture wires the real cert provider behind the real REST
// middleware chain. Every caller it produces holds the FULL certs:* grant in
// its OWN tenant — the premise of the defect is that holding certs:revoke
// somewhere is enough to act everywhere, so withholding the grant would test
// nothing.
//
// The grant deliberately includes CertsRevokeCross, exactly as the built-in
// Admin role does. A tenant-local caller therefore HOLDS the CA-operator
// permission, and what refuses it is the ScopeCross rule rather than a missing
// grant — which is the property worth pinning, since the built-in role really
// does hand that permission to every tenant admin.
func newCertScopeFixture(t *testing.T) *certScopeFixture {
	t.Helper()

	store := kvstore.NewMemoryKVStore()
	prov := cert.NewDefaultProvider(store, newCertScopeBarrier(t))
	if err := prov.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize CA: %v", err)
	}

	roles := &mockRoleService{
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{
				permission.CertsGenerate,
				permission.CertsExport,
				permission.CertsRevoke,
				permission.CertsRevokeCross,
				permission.CertsList,
				permission.CertsRead,
			}, nil
		},
	}

	caller := &certScopeAuthenticator{}
	caller.as(certScopeTenantA, "a-admin")

	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCertProvider(prov),
		WithAuthMiddleware(mw),
	)

	return &certScopeFixture{srv: srv, prov: prov, store: store, caller: caller}
}

// do drives one request through the complete chain: authenticate, bind the
// tenant context from the identity, authorize the route permission, handle.
func (f *certScopeFixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	f.srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// issue mints a certificate directly through the provider, bypassing the REST
// surface, so a test can establish a victim tenant's certificate without
// depending on the very authorization path it is about to exercise.
func (f *certScopeFixture) issue(t *testing.T, tenantID, username string) string {
	t.Helper()
	bundle, err := f.prov.GenerateClientCert(context.Background(), &cert.GenerateRequest{
		Username: username,
		TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("issue cert for %s/%s: %v", tenantID, username, err)
	}
	return bundle.Serial
}

func (f *certScopeFixture) revoked(t *testing.T, serial string) bool {
	t.Helper()
	got, err := f.prov.IsRevoked(context.Background(), serial)
	if err != nil {
		t.Fatalf("IsRevoked(%s): %v", serial, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// POST /certs/revoke
// ---------------------------------------------------------------------------

// TestCertsRevoke_CrossTenantSerialIsRefused is the issue's headline case.
//
// Tenant A holds certs:revoke in its OWN tenant and nothing more. It calls
// revoke with a serial belonging to tenant B. Before the fix this returned 200
// and tenant B's certificate stopped authenticating cluster-wide, because
// certs:revoke resolves to ScopeSelf and ScopeSelf compares the caller's tenant
// against a context tenant that was set FROM the caller's identity.
func TestCertsRevoke_CrossTenantSerialIsRefused(t *testing.T) {
	f := newCertScopeFixture(t)
	victim := f.issue(t, certScopeTenantB, "b-user")

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/revoke", map[string]string{"serial": victim})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: tenant %s must not revoke a serial owned by tenant %s (body %q)",
			rec.Code, certScopeTenantA, certScopeTenantB, rec.Body.String())
	}
	if f.revoked(t, victim) {
		t.Fatal("tenant B's certificate was revoked by tenant A; the refusal must also leave no marker behind")
	}
}

// TestCertsRevoke_OwnTenantSerialStillSucceeds is the control that keeps the
// fix from being "deny everything". A tenant admin revoking a certificate its
// own tenant holds is the ordinary case and must keep working.
func TestCertsRevoke_OwnTenantSerialStillSucceeds(t *testing.T) {
	f := newCertScopeFixture(t)
	mine := f.issue(t, certScopeTenantA, "a-user")

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/revoke", map[string]string{"serial": mine})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an own-tenant revocation must not be collateral damage (body %q)",
			rec.Code, rec.Body.String())
	}
	if !f.revoked(t, mine) {
		t.Fatal("own-tenant revocation reported success but wrote no marker")
	}
}

// TestCertsRevoke_UnknownSerialRefusedToTenantLocalCaller covers deny-by-serial
// — the operation docs/certificate-authority/architecture.md:199 guarantees.
//
// A serial this node holds no record for names no tenant, so no tenant-local
// caller can prove it is theirs; "revoke serial S of tenant B" and "revoke an
// unknown serial S" are the same request. The only safe reading is that writing
// an unattributable, cluster-wide deny marker is a CA-OPERATOR act.
func TestCertsRevoke_UnknownSerialRefusedToTenantLocalCaller(t *testing.T) {
	f := newCertScopeFixture(t)

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/revoke", map[string]string{"serial": "0123456789abcdef0123456789abcdef"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: an unattributable deny-by-serial marker is a CA-operator act (body %q)",
			rec.Code, rec.Body.String())
	}
}

// TestCertsRevoke_UnknownSerialPermittedToSystemTenant is the other half of the
// same rule, and it is what preserves deny-by-serial rather than deleting it.
// The operator who owns the CA may still revoke a peer-issued serial.
func TestCertsRevoke_UnknownSerialPermittedToSystemTenant(t *testing.T) {
	f := newCertScopeFixture(t)
	const peerSerial = "fedcba9876543210fedcba9876543210"

	f.caller.as(tenant.SystemTenantID, "platform-op")
	rec := f.do(t, http.MethodPost, "/certs/revoke", map[string]string{"serial": peerSerial})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: deny-by-serial must survive for the CA operator (body %q)",
			rec.Code, rec.Body.String())
	}
	if !f.revoked(t, peerSerial) {
		t.Fatal("the CA operator's deny-by-serial revocation wrote no marker")
	}
	var body revokeCertResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RecordFound {
		t.Error("recordFound = true for a serial this node holds no record for")
	}
}

// TestCertsRevoke_SystemTenantCannotRevokeAnotherTenantsSerial: on the
// known-serial path the platform operator is confined to its own tenant like
// every other caller (certs:revoke is ScopeSelfStrict), and no marker is
// written.
func TestCertsRevoke_SystemTenantCannotRevokeAnotherTenantsSerial(t *testing.T) {
	f := newCertScopeFixture(t)
	target := f.issue(t, certScopeTenantB, "b-user")

	f.caller.as(tenant.SystemTenantID, "platform-op")
	rec := f.do(t, http.MethodPost, "/certs/revoke", map[string]string{"serial": target})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
	if f.revoked(t, target) {
		t.Fatal("a refused revocation of another tenant's certificate still wrote a marker")
	}
}

// ---------------------------------------------------------------------------
// POST /certs/generate
// ---------------------------------------------------------------------------

// TestCertsGenerate_CrossTenantTargetIsRefused covers the sibling permission
// the issue asked to audit, and it is the more serious of the two.
//
// The handler passes the request body's tenantID straight into
// GenerateClientCert, which writes it as the certificate's Subject OU. The mTLS
// authenticator derives the identity's tenant from exactly that OU
// (pkg/auth/mtls.go, the legacy fallback). So an unchecked cross-tenant mint is
// not a bookkeeping error — it hands the caller a CA-signed credential that
// authenticates as the victim tenant.
func TestCertsGenerate_CrossTenantTargetIsRefused(t *testing.T) {
	f := newCertScopeFixture(t)

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/generate", map[string]string{
		"username": "b-user",
		"tenantID": certScopeTenantB,
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: tenant %s must not mint a certificate bound to tenant %s (body %q)",
			rec.Code, certScopeTenantA, certScopeTenantB, rec.Body.String())
	}

	records, err := f.prov.ListCerts(context.Background(), certScopeTenantB)
	if err != nil {
		t.Fatalf("ListCerts(%s): %v", certScopeTenantB, err)
	}
	if len(records) != 0 {
		t.Fatalf("the refusal left %d record(s) in tenant %s", len(records), certScopeTenantB)
	}
}

// TestCertsGenerate_CrossTenantMintCarriesVictimTenantInOU makes the
// consequence explicit rather than asserted in prose: if the mint is allowed,
// the resulting certificate's OU — and therefore the tenant every mTLS
// authentication derives from it — is the victim's.
func TestCertsGenerate_CrossTenantMintCarriesVictimTenantInOU(t *testing.T) {
	f := newCertScopeFixture(t)

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/generate", map[string]string{
		"username": "b-user",
		"tenantID": certScopeTenantB,
	})

	if rec.Code == http.StatusCreated {
		var bundle certBundleResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
			t.Fatalf("decode: %v", err)
		}
		block, _ := pem.Decode([]byte(bundle.CertPEM))
		if block == nil {
			t.Fatal("minted bundle carried no PEM")
		}
		minted, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse minted cert: %v", err)
		}
		ou := ""
		if len(minted.Subject.OrganizationalUnit) > 0 {
			ou = minted.Subject.OrganizationalUnit[0]
		}
		t.Fatalf("tenant %s minted a CA-signed certificate whose OU is %q; the mTLS authenticator reads that OU as the identity's tenant, so this is a credential for tenant %s",
			certScopeTenantA, ou, certScopeTenantB)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestCertsGenerate_OwnTenantStillSucceeds is the control.
func TestCertsGenerate_OwnTenantStillSucceeds(t *testing.T) {
	f := newCertScopeFixture(t)

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/generate", map[string]string{
		"username": "a-user",
		"tenantID": certScopeTenantA,
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: an own-tenant mint must keep working (body %q)",
			rec.Code, rec.Body.String())
	}
}

// TestCertsGenerate_SystemTenantCannotTargetAnotherTenant: the system tenant
// mints for itself only. A certificate it minted for tenant B's user would
// authenticate as that user, so certs:generate is ScopeSelfStrict.
func TestCertsGenerate_SystemTenantCannotTargetAnotherTenant(t *testing.T) {
	f := newCertScopeFixture(t)

	f.caller.as(tenant.SystemTenantID, "platform-op")
	rec := f.do(t, http.MethodPost, "/certs/generate", map[string]string{
		"username": "b-user",
		"tenantID": certScopeTenantB,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: the platform operator minted for another tenant (body %q)",
			rec.Code, rec.Body.String())
	}

	rec = f.do(t, http.MethodPost, "/certs/generate", map[string]string{
		"username": "op",
		"tenantID": tenant.SystemTenantID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: the platform operator mints for its own tenant (body %q)",
			rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /certs/list
// ---------------------------------------------------------------------------

// TestCertsList_CrossTenantTargetIsRefused settles the severity question the
// issue left open.
//
// ListCerts does scope on its tenantID argument — but the handler takes that
// argument from the QUERY STRING and never compares it to the caller. So a
// tenant does not have to guess another tenant's 128-bit serial: it asks for
// them. That composes directly with the revoke defect above, which is why this
// is the test that decides how severe the pair is.
func TestCertsList_CrossTenantTargetIsRefused(t *testing.T) {
	f := newCertScopeFixture(t)
	victim := f.issue(t, certScopeTenantB, "b-user")

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodGet, "/certs/list?tenantID="+certScopeTenantB, nil)

	if rec.Code == http.StatusOK {
		var body listCertsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, rec := range body.Certs {
			if rec.Serial == victim {
				t.Fatalf("tenant %s read tenant %s's certificate inventory and obtained serial %s; the serial a cross-tenant revoke needs is not guessed, it is served",
					certScopeTenantA, certScopeTenantB, victim)
			}
		}
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestCertsList_OwnTenantStillSucceeds is the control.
func TestCertsList_OwnTenantStillSucceeds(t *testing.T) {
	f := newCertScopeFixture(t)
	mine := f.issue(t, certScopeTenantA, "a-user")

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodGet, "/certs/list?tenantID="+certScopeTenantA, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var body listCertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, r := range body.Certs {
		if r.Serial == mine {
			found = true
		}
	}
	if !found {
		t.Fatalf("own-tenant listing did not return the tenant's own certificate %s", mine)
	}
}

// TestCertsList_SystemTenantCannotListAnotherTenant: the platform operator
// reads its own certificate inventory only.
func TestCertsList_SystemTenantCannotListAnotherTenant(t *testing.T) {
	f := newCertScopeFixture(t)
	f.issue(t, certScopeTenantB, "b-user")

	f.caller.as(tenant.SystemTenantID, "platform-op")
	rec := f.do(t, http.MethodGet, "/certs/list?tenantID="+certScopeTenantB, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
}
