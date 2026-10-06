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

// Which tenant a certs RPC is allowed to act on — the gRPC half of the same
// rule pkg/api/rest/certs_tenant_scope_test.go pins for REST.
//
// Both surfaces reach ONE process-wide CertProvider, so a rule enforced on one
// of them is a rule with a door beside it. These tests exist so the two cannot
// drift: they assert the same four outcomes against the same real
// rbac.RBACAuthorizer, the same real permission registry and a real
// cert.DefaultCertProvider over a shared kvstore.MemoryKVStore.
//
// The context is built the way the auth middleware builds it — identity
// attached, tenant bound FROM that identity (pkg/auth/middleware/middleware.go
// finalizeIdentity). That equality is the premise of the whole defect, so
// reproducing it is the point rather than a shortcut.
//
// No build constraint: mockRoleService and mockCertProvider (grpc_auth_test.go)
// are unconstrained, and this mapping ships in every build.

package grpc

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// certScopeUsers is the minimal certEnrollmentUsers double: accounts live
// strictly in the tenant they were seeded under, so a lookup for the same
// username in a different tenant always misses -- the whole premise of
// TestGRPCCertsIssue_AccountOutsideCallerTenantIsRefused.
type certScopeUsers struct {
	byKey map[string]*user.User
}

func newCertScopeUsers() *certScopeUsers {
	return &certScopeUsers{byKey: make(map[string]*user.User)}
}

func (u *certScopeUsers) seed(tenantID, username string, id uint64) {
	u.byKey[tenantID+"/"+username] = &user.User{ID: id, Username: username, TenantID: tenantID}
}

func (u *certScopeUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if usr, ok := u.byKey[tenantID+"/"+username]; ok {
		return usr, nil
	}
	return nil, &user.UserNotFoundError{Lookup: tenantID + "/" + username}
}

func (u *certScopeUsers) AddCertBinding(_ context.Context, userID uint64, _ string, _ int64) (*user.User, error) {
	return &user.User{ID: userID}, nil
}

const (
	certScopeTenantA = "acme"
	certScopeTenantB = "globex"
)

// certScopeBarrier is a real AES-GCM seal.EncryptionBarrier: the CA key really
// round-trips through it.
type certScopeBarrier struct{ key []byte }

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

// certScopeGRPCFixture is one node: one shared store, one CA, two tenants.
type certScopeGRPCFixture struct {
	svc     *qrdbServiceServer
	prov    *cert.DefaultCertProvider
	anchors trustanchor.Registry
	users   *certScopeUsers
}

// newCertScopeGRPCFixture wires the real provider behind the real middleware.
// The grant list mirrors the built-in Admin role — including CertsRevokeCross,
// which every tenant admin really does hold — so what refuses a tenant-local
// caller is the ScopeCross rule and not an absent grant.
func newCertScopeGRPCFixture(t *testing.T) *certScopeGRPCFixture {
	t.Helper()

	prov := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), newCertScopeBarrier(t))
	if err := prov.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize CA: %v", err)
	}
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	users := newCertScopeUsers()

	roles := &mockRoleService{
		resolveResult: []permission.Permission{
			permission.CertsIssue,
			permission.CertsEnroll,
			permission.CertsRevoke,
			permission.CertsRevokeCross,
			permission.CertsList,
			permission.CertsRead,
			permission.TrustAnchorsAdd,
			permission.TrustAnchorsList,
			permission.TrustAnchorsRemove,
		},
	}

	return &certScopeGRPCFixture{
		svc: &qrdbServiceServer{
			certProv:   prov,
			certEnroll: service.NewCertEnrollmentService(prov, users, anchors),
			authMW:     authmw.New(rbac.New(roles)),
		},
		prov:    prov,
		anchors: anchors,
		users:   users,
	}
}

// newTestCAPEM returns a fresh self-signed CA certificate, PEM-encoded, for
// trust-anchor registration tests.
func newTestCAPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// as builds the context the auth middleware would have built: the identity
// attached, and the tenant bound FROM that identity. The two agreeing is
// exactly why the route-level ScopeSelf check says nothing.
func (f *certScopeGRPCFixture) as(tenantID, subject string) context.Context {
	id := &auth.Identity{Subject: subject, TenantID: tenantID, Roles: []string{"admin"}}
	ctx := auth.WithIdentity(context.Background(), id)
	return tenant.WithTenantID(ctx, tenantID)
}

func (f *certScopeGRPCFixture) issue(t *testing.T, tenantID, username string) string {
	t.Helper()
	bundle, err := certtest.Generate(context.Background(), f.prov, &certtest.Request{
		Username: username,
		TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("issue cert for %s/%s: %v", tenantID, username, err)
	}
	return bundle.Serial
}

func (f *certScopeGRPCFixture) revoked(t *testing.T, serial string) bool {
	t.Helper()
	got, err := f.prov.IsRevoked(context.Background(), serial)
	if err != nil {
		t.Fatalf("IsRevoked(%s): %v", serial, err)
	}
	return got
}

// assertPermissionDenied fails unless err is a PermissionDenied status.
func assertPermissionDenied(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: succeeded, want PermissionDenied", what)
	}
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("%s: code = %s, want PermissionDenied (%v)", what, got, err)
	}
}

// ---------------------------------------------------------------------------
// RevokeCert
// ---------------------------------------------------------------------------

// TestGRPCCertsRevoke_CrossTenantSerialIsRefused is the gRPC twin of the REST
// headline case.
func TestGRPCCertsRevoke_CrossTenantSerialIsRefused(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	victim := f.issue(t, certScopeTenantB, "b-user")

	_, err := f.svc.RevokeCert(f.as(certScopeTenantA, "a-admin"), &pb.RevokeCertRequest{Serial: victim})
	assertPermissionDenied(t, err, "tenant A revoking tenant B's serial")

	if f.revoked(t, victim) {
		t.Fatal("tenant B's certificate was revoked by tenant A over gRPC")
	}
}

// TestGRPCCertsRevoke_OwnTenantSerialStillSucceeds is the control.
func TestGRPCCertsRevoke_OwnTenantSerialStillSucceeds(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	mine := f.issue(t, certScopeTenantA, "a-user")

	resp, err := f.svc.RevokeCert(f.as(certScopeTenantA, "a-admin"), &pb.RevokeCertRequest{Serial: mine})
	if err != nil {
		t.Fatalf("own-tenant revocation must not be collateral damage: %v", err)
	}
	if !resp.GetRecordFound() {
		t.Error("recordFound = false for a serial this node issued")
	}
	if !f.revoked(t, mine) {
		t.Fatal("own-tenant revocation wrote no marker")
	}
}

// TestGRPCCertsRevoke_UnknownSerialRefusedToTenantLocalCaller pins
// deny-by-serial as a CA-operator act on this transport too.
func TestGRPCCertsRevoke_UnknownSerialRefusedToTenantLocalCaller(t *testing.T) {
	f := newCertScopeGRPCFixture(t)

	_, err := f.svc.RevokeCert(f.as(certScopeTenantA, "a-admin"),
		&pb.RevokeCertRequest{Serial: "0123456789abcdef0123456789abcdef"})
	assertPermissionDenied(t, err, "tenant-local deny-by-serial")
}

// TestGRPCCertsRevoke_UnknownSerialPermittedToSystemTenant is what keeps
// deny-by-serial alive rather than deleted.
func TestGRPCCertsRevoke_UnknownSerialPermittedToSystemTenant(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	const peerSerial = "fedcba9876543210fedcba9876543210"

	resp, err := f.svc.RevokeCert(f.as(tenantid.System, "platform-op"),
		&pb.RevokeCertRequest{Serial: peerSerial})
	if err != nil {
		t.Fatalf("deny-by-serial must survive for the CA operator: %v", err)
	}
	if resp.GetRecordFound() {
		t.Error("recordFound = true for a serial this node holds no record for")
	}
	if !f.revoked(t, peerSerial) {
		t.Fatal("the CA operator's deny-by-serial revocation wrote no marker")
	}
}

// ---------------------------------------------------------------------------
// TrustAnchorAdd
//
// GenerateCert is retired (server-side keygen is gone); the cross-tenant mint
// it used to cover is replaced by the trust-anchor verbs, which are the only
// certs-adjacent RPCs that still carry a tenant_id field of their own. IssueCert
// and EnrollCert act on the caller's own tenant unconditionally (callerTenant),
// so they have no cross-tenant request field to refuse in the first place; see
// TestGRPCCertsIssue_AccountOutsideCallerTenantIsRefused for how THEY stay
// tenant-confined.
// ---------------------------------------------------------------------------

// TestGRPCTrustAnchorAdd_CrossTenantTargetIsRefused covers the cross-tenant
// case: trust_anchors:add is ScopeSelfStrict, so a request naming another
// tenant must be refused before any anchor is registered.
func TestGRPCTrustAnchorAdd_CrossTenantTargetIsRefused(t *testing.T) {
	f := newCertScopeGRPCFixture(t)

	_, err := f.svc.TrustAnchorAdd(f.as(certScopeTenantA, "a-admin"), &pb.TrustAnchorAddRequest{
		TenantId: certScopeTenantB,
		CertsPem: string(newTestCAPEM(t, "globex-root")),
	})
	assertPermissionDenied(t, err, "tenant A registering an anchor for tenant B")

	anchors, lerr := f.anchors.List(context.Background(), certScopeTenantB)
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(anchors) != 0 {
		t.Fatalf("the refusal left %d anchor(s) in tenant %s", len(anchors), certScopeTenantB)
	}
}

// TestGRPCTrustAnchorAdd_OwnTenantStillSucceeds is the control.
func TestGRPCTrustAnchorAdd_OwnTenantStillSucceeds(t *testing.T) {
	f := newCertScopeGRPCFixture(t)

	resp, err := f.svc.TrustAnchorAdd(f.as(certScopeTenantA, "a-admin"), &pb.TrustAnchorAddRequest{
		TenantId: certScopeTenantA,
		CertsPem: string(newTestCAPEM(t, "acme-root")),
	})
	if err != nil {
		t.Fatalf("own-tenant trust anchor add must keep working: %v", err)
	}
	if len(resp.GetAnchors()) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(resp.GetAnchors()))
	}
}

// TestGRPCCertsIssue_AccountOutsideCallerTenantIsRefused is IssueCert's
// tenant-confinement case: the request carries no tenant_id field at all
// (unlike GenerateCert's predecessor), and SignCertificate scopes the account
// lookup to callerTenant(ctx) -- so a CSR naming a username that exists only
// in ANOTHER tenant is refused as not found rather than minted against it.
func TestGRPCCertsIssue_AccountOutsideCallerTenantIsRefused(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	f.users.seed(certScopeTenantA, "shared-name", 1)
	_, csrPEM, err := certtest.NewCSR("shared-name")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	// "shared-name" exists in tenant A, not in tenant B: tenant B's caller
	// must not be able to mint a certificate for it.
	_, err = f.svc.IssueCert(f.as(certScopeTenantB, "b-admin"), &pb.IssueCertRequest{
		Username: "shared-name",
		CsrPem:   string(csrPEM),
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("IssueCert across tenants: code = %s (%v), want NotFound", got, err)
	}
}

// TestGRPCCertsIssue_OwnTenantAccountSucceeds is the control: the same
// username, looked up in the caller's own tenant, issues normally.
func TestGRPCCertsIssue_OwnTenantAccountSucceeds(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	f.users.seed(certScopeTenantA, "a-user", 7)
	_, csrPEM, err := certtest.NewCSR("a-user")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	resp, err := f.svc.IssueCert(f.as(certScopeTenantA, "a-admin"), &pb.IssueCertRequest{
		Username: "a-user",
		CsrPem:   string(csrPEM),
	})
	if err != nil {
		t.Fatalf("own-tenant issue must keep working: %v", err)
	}
	if resp.GetSerial() == "" {
		t.Error("own-tenant issue returned no serial")
	}
}

// ---------------------------------------------------------------------------
// ListCerts
// ---------------------------------------------------------------------------

// TestGRPCCertsList_CrossTenantTargetIsRefused closes the path by which a
// tenant obtains another tenant's serials in the first place.
func TestGRPCCertsList_CrossTenantTargetIsRefused(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	victim := f.issue(t, certScopeTenantB, "b-user")

	resp, err := f.svc.ListCerts(f.as(certScopeTenantA, "a-admin"),
		&pb.ListCertsRequest{TenantId: certScopeTenantB})
	if err == nil {
		for _, rec := range resp.GetCerts() {
			if rec.GetSerial() == victim {
				t.Fatalf("tenant %s read tenant %s's inventory and obtained serial %s",
					certScopeTenantA, certScopeTenantB, victim)
			}
		}
	}
	assertPermissionDenied(t, err, "tenant A listing tenant B")
}

// TestGRPCCertsList_OwnTenantStillSucceeds is the control.
func TestGRPCCertsList_OwnTenantStillSucceeds(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	mine := f.issue(t, certScopeTenantA, "a-user")

	resp, err := f.svc.ListCerts(f.as(certScopeTenantA, "a-admin"),
		&pb.ListCertsRequest{TenantId: certScopeTenantA})
	if err != nil {
		t.Fatalf("own-tenant listing must keep working: %v", err)
	}
	if len(resp.GetCerts()) != 1 || resp.GetCerts()[0].GetSerial() != mine {
		t.Fatalf("own-tenant listing = %+v, want exactly %s", resp.GetCerts(), mine)
	}
}
