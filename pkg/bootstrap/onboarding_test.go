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

package bootstrap

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// test infrastructure: a real cert.CertProvider, test CA helpers, and the
// extra doubles onboarding.go's seams need beyond bootstrap_test.go's.
// ---------------------------------------------------------------------------

// onboardingFakeBarrier is a faithful AES-GCM seal.EncryptionBarrier, the
// same shape every package in this tree reproduces because pkg/cert's own
// is unexported to its _test.go file.
type onboardingFakeBarrier struct {
	mu  sync.RWMutex
	key []byte
}

func newOnboardingFakeBarrier(t *testing.T) *onboardingFakeBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generating a test barrier key: %v", err)
	}
	return &onboardingFakeBarrier{key: key}
}

func (b *onboardingFakeBarrier) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (b *onboardingFakeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- cipher.NewGCMWithRandomNonce samples its own nonce and
	// prepends it; passing anything but nil here makes Seal panic.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *onboardingFakeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *onboardingFakeBarrier) IsSealed() bool { return false }

// newRealCertProvider returns a real, initialized DefaultCertProvider over a
// fresh in-memory store -- what KeyBSigner wraps in production.
// onboardingTrustDomain is the SPIFFE trust domain the onboarding tests issue
// and verify under; a provider refuses to issue a tenant credential without one.
const onboardingTrustDomain = "db.example.com"

func newRealCertProvider(t *testing.T) cert.CertProvider {
	t.Helper()
	p := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), newOnboardingFakeBarrier(t),
		cert.WithTrustDomain(onboardingTrustDomain))
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p
}

// onboardingTestCA is a CA certificate and its key, for building BYOC
// admin certificates and trust anchor bundles.
type onboardingTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var onboardingSerial int64 = 9000

func nextOnboardingSerial() *big.Int {
	onboardingSerial++
	return big.NewInt(onboardingSerial)
}

func newOnboardingKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newOnboardingRoot(t *testing.T, cn string) *onboardingTestCA {
	t.Helper()
	key := newOnboardingKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          nextOnboardingSerial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &onboardingTestCA{cert: c, key: key}
}

func (ca *onboardingTestCA) clientLeaf(t *testing.T, cn, ou string) *x509.Certificate {
	t.Helper()
	key := newOnboardingKey(t)
	uris, err := auth.TenantPrincipalURIs(onboardingTrustDomain, ou, spiffe.KindUser, cn)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: nextOnboardingSerial(),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}},
		URIs:         uris,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func onboardingPEM(c *x509.Certificate) []byte { return trustanchor.EncodePEM(c) }

// fakeCertBindingUsers wraps fakeUsers (CreateUser, DeleteUser) with
// AddCertBinding, so it satisfies certBindingAdder the way qrdb's real user
// store does -- used wherever a test needs the direct (non-Binder) binding
// seam, optionally failing.
type fakeCertBindingUsers struct {
	fakeUsers
	bindCalls []uint64
	bindErr   error
}

func (f *fakeCertBindingUsers) AddCertBinding(_ context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	f.bindCalls = append(f.bindCalls, userID)
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	return &user.User{ID: userID}, nil
}

// fakeRevokingSigner wraps fakeSigner with RevokeCert, so it satisfies
// certRevoker the way qrdb's KeyBSigner does over a real cert.CertProvider.
type fakeRevokingSigner struct {
	fakeSigner
	revokeCalls []string
	revokeErr   error
}

func (f *fakeRevokingSigner) RevokeCert(_ context.Context, serial string) (*cert.RevokeResult, error) {
	f.revokeCalls = append(f.revokeCalls, serial)
	if f.revokeErr != nil {
		return nil, f.revokeErr
	}
	return &cert.RevokeResult{Serial: serial}, nil
}

// tenantCreatorOnly is a firstboot.TenantCreator with no delete capability,
// for the Exclusive seam-missing tests.
type tenantCreatorOnly struct {
	createCalls []createTenantCall
}

func (t *tenantCreatorOnly) CreateTenant(_ context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	t.createCalls = append(t.createCalls, createTenantCall{id: id, name: name, isolationType: isolationType})
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType}, nil
}

// userCreatorOnly is a firstboot.UserCreator with no delete capability, for
// the Exclusive seam-missing tests.
type userCreatorOnly struct {
	nextID      uint64
	createCalls []user.CreateUserRequest
}

func (u *userCreatorOnly) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	u.createCalls = append(u.createCalls, *req)
	u.nextID++
	return &user.User{ID: u.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs}, nil
}

// pendingAdminsFaulting wraps a real tenantadmin.Store, injecting errors on
// Get/Put while delegating Delete and everything else.
type pendingAdminsFaulting struct {
	inner  tenantadmin.Store
	getErr error
	putErr error
}

func (p *pendingAdminsFaulting) Create(ctx context.Context, tenantID string, rec *tenantadmin.Pending) error {
	if p.putErr != nil {
		return p.putErr
	}
	return p.inner.Create(ctx, tenantID, rec)
}

func (p *pendingAdminsFaulting) Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error) {
	if p.getErr != nil {
		return nil, p.getErr
	}
	return p.inner.Get(ctx, tenantID)
}

func (p *pendingAdminsFaulting) Delete(ctx context.Context, tenantID string, read *tenantadmin.Pending) (bool, error) {
	return p.inner.Delete(ctx, tenantID, read)
}

var _ tenantadmin.Store = (*pendingAdminsFaulting)(nil)

// ---------------------------------------------------------------------------
// Dependencies.now / planAdmin -- direct coverage of branches ProvisionTenant
// exercises too lightly from the outside.
// ---------------------------------------------------------------------------

func TestPlanAdmin_RefusesInvalidTrustAnchors(t *testing.T) {
	_, err := planAdmin(TenantProvisionConfig{
		TenantID:        "acme",
		TrustAnchorsPEM: []byte("not a certificate"),
	}, Dependencies{TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())}, AdminEnrollCert, "admin", false)
	var want *InvalidTrustAnchorsError
	if !errors.As(err, &want) || want.Unwrap() == nil {
		t.Fatalf("planAdmin with an undecodable anchor bundle = %v, want *InvalidTrustAnchorsError", err)
	}
}

func TestPlanAdmin_RefusesACSRThatDoesNotParse(t *testing.T) {
	_, err := planAdmin(TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: []byte("not a csr"),
	}, Dependencies{Signer: &fakeSigner{}, TrustDomain: onboardingTrustDomain}, AdminEnrollCert, "admin", false)
	var want *AdminCredentialRejectedError
	if !errors.As(err, &want) || want.Unwrap() == nil {
		t.Fatalf("planAdmin with an undecodable CSR = %v, want *AdminCredentialRejectedError", err)
	}
}

func TestProvisionTenant_RefusesACSRThatDoesNotParse(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: []byte("not a csr"),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *AdminCredentialRejectedError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *AdminCredentialRejectedError", err)
	}
}

func TestProvisionTenant_UsesTheConfiguredClockForAnchorValidity(t *testing.T) {
	root := newOnboardingRoot(t, "root")
	// now is after the anchor's validity window: ParseAnchors must refuse it.
	future := root.cert.NotAfter.Add(time.Hour)
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		Isolation:       tenant.IsolationPrefix,
		AdminCSRPEM:     validAdminCSR(t),
		TrustAnchorsPEM: onboardingPEM(root.cert),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()),
		Signer:       &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Now:         func() time.Time { return future },
		Logger:      discardLogger(),
	})
	var want *InvalidTrustAnchorsError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *InvalidTrustAnchorsError (the configured clock put the anchor outside its window)", err)
	}
}

func TestProvisionPendingAdmin_IssueErrorWrapped(t *testing.T) {
	signerErr := errors.New("key b ca unavailable")
	var undo undoStack
	fail := func(cause error) (*TenantProvisionResult, error) { return nil, cause }
	plan := &adminPlan{csrPEM: validAdminCSR(t), signer: &fakeSignerErr{err: signerErr}}

	_, err := provisionPendingAdmin(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()),
		Logger:        discardLogger(),
	}, plan, "admin", &undo, fail)
	var want *TenantAdminCertError
	if !errors.As(err, &want) || !errors.Is(err, signerErr) {
		t.Fatalf("err = %v, want *TenantAdminCertError wrapping the signer failure", err)
	}
}

// TestProvisionPendingAdmin_RefusesAnInvalidUsernameBeforeSigning pins the
// owner finding: a malformed admin username on the pending-admin path is
// refused BEFORE a certificate is signed or anything is recorded -- this
// path has no later user-store validation to catch it, unlike the
// non-pending path's immediate CreateUser.
func TestProvisionPendingAdmin_RefusesAnInvalidUsernameBeforeSigning(t *testing.T) {
	ctx := context.Background()
	signer := &fakeSigner{cert: realCertPEM(t)}
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	var undo undoStack
	fail := func(cause error) (*TenantProvisionResult, error) { return nil, cause }
	plan := &adminPlan{csrPEM: validAdminCSR(t), signer: signer}

	for _, bad := range []string{"", "has a space", strings.Repeat("a", 65)} {
		_, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme"}, Dependencies{
			TrustDomain:   onboardingTrustDomain,
			PendingAdmins: pending,
			Logger:        discardLogger(),
		}, plan, bad, &undo, fail)
		var want *InvalidAdminUsernameError
		if !errors.As(err, &want) {
			t.Fatalf("username %q: err = %v, want *InvalidAdminUsernameError", bad, err)
		}
		if n := signer.called.Load(); n != 0 {
			t.Fatalf("username %q: signer was called %d times, want 0 (refused before signing)", bad, n)
		}
		if rec, _ := pending.Get(ctx, "acme"); rec != nil {
			t.Fatalf("username %q: a pending admin record was stored despite the refusal", bad)
		}
	}
}

func TestProvisionPendingAdmin_UndoRemovesTheStoredRecord(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	var undo undoStack
	fail := func(cause error) (*TenantProvisionResult, error) { return nil, cause }
	plan := &adminPlan{csrPEM: validAdminCSR(t), signer: &fakeSigner{cert: realCertPEM(t)}}

	result, err := provisionPendingAdmin(ctx, TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending,
		Logger:        discardLogger(),
	}, plan, "admin", &undo, fail)
	if err != nil {
		t.Fatalf("provisionPendingAdmin: %v", err)
	}
	if !result.AdminAwaitsBarrierInit {
		t.Fatal("result.AdminAwaitsBarrierInit = false, want true")
	}
	if rec, _ := pending.Get(ctx, "acme"); rec == nil {
		t.Fatal("provisionPendingAdmin did not store a pending record")
	}

	// Directly drive the undo stack it built, the way an Exclusive
	// ProvisionTenant whose LATER step failed would -- there is no later
	// step inside provisionPendingAdmin itself (it returns success right
	// after recording the pending admin), so this is the only way to prove
	// the pushed "delete pending admin" step actually removes the record.
	if failed := undo.run(ctx, discardLogger(), "acme"); len(failed) != 0 {
		t.Fatalf("undo.run failed: %v", failed)
	}
	if rec, _ := pending.Get(ctx, "acme"); rec != nil {
		t.Fatal("undo did not remove the pending record")
	}
}

// ---------------------------------------------------------------------------
// KeyBSigner
// ---------------------------------------------------------------------------

func TestKeyBSigner_SignAdminCSR_DelegatesToIssueFromCSR(t *testing.T) {
	ctx := context.Background()
	prov := newRealCertProvider(t)
	signer := &KeyBSigner{Certs: prov}
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	certPEM, err := signer.SignAdminCSR(ctx, "acme", "admin", csrPEM)
	if err != nil {
		t.Fatalf("SignAdminCSR: %v", err)
	}
	certs, err := trustanchor.ParseCertificates(certPEM)
	if err != nil {
		t.Fatalf("the signed certificate does not decode: %v", err)
	}
	if certs[0].Subject.CommonName != "admin" {
		t.Fatalf("CN = %q, want admin", certs[0].Subject.CommonName)
	}
	if len(certs[0].Subject.OrganizationalUnit) == 0 || certs[0].Subject.OrganizationalUnit[0] != "acme" {
		t.Fatalf("OU = %v, want [acme]", certs[0].Subject.OrganizationalUnit)
	}
}

func TestKeyBSigner_SignAdminCSR_PropagatesAnIssueFailure(t *testing.T) {
	ctx := context.Background()
	signer := &KeyBSigner{Certs: newRealCertProvider(t)}
	if _, err := signer.SignAdminCSR(ctx, "acme", "admin", []byte("not a csr")); err == nil {
		t.Fatal("SignAdminCSR accepted a CSR that does not decode")
	}
}

func TestKeyBSigner_RevokeCert_Delegates(t *testing.T) {
	ctx := context.Background()
	prov := newRealCertProvider(t)
	signer := &KeyBSigner{Certs: prov}
	_, csrPEM, err := certtest.NewCSR("irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := signer.SignAdminCSR(ctx, "acme", "admin", csrPEM)
	if err != nil {
		t.Fatal(err)
	}
	certs, err := trustanchor.ParseCertificates(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	serial := cert.FormatSerial(certs[0].SerialNumber)

	if _, err := signer.RevokeCert(ctx, serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	revoked, err := prov.IsRevoked(ctx, serial)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("RevokeCert did not delegate to the underlying provider")
	}
}

// ---------------------------------------------------------------------------
// BYOC admin path
// ---------------------------------------------------------------------------

func TestProvisionTenant_BYOCPath_VerifiesAgainstRequestAnchorsAndRegistersThem(t *testing.T) {
	ctx := context.Background()
	root := newOnboardingRoot(t, "acme-root")
	leaf := root.clientLeaf(t, "admin", "acme")
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())

	result, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:        "acme",
		Isolation:       tenant.IsolationPrefix,
		AdminCertPEM:    onboardingPEM(leaf),
		TrustAnchorsPEM: onboardingPEM(root.cert),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain:  onboardingTrustDomain,
		TrustAnchors: anchors, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if result.AdminCertSerial != "" {
		t.Error("a bring-your-own certificate must carry no serial: this cluster did not issue it")
	}
	if result.AdminCertThumbprint == "" || result.AdminCertPEM == nil {
		t.Error("BYOC admin result missing cert material")
	}
	list, err := anchors.List(ctx, "acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("anchors.List(acme) = %v, %v; want the one registered anchor", list, err)
	}
}

func TestProvisionTenant_BYOCPath_NotChainingRefusedBeforeCreatingTheTenant(t *testing.T) {
	ctx := context.Background()
	root := newOnboardingRoot(t, "acme-root")
	other := newOnboardingRoot(t, "other-root")
	// Signed by a different root than the one registered as the trust anchor.
	leaf := other.clientLeaf(t, "admin", "acme")
	tenants := &fakeTenants{}

	_, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:        "acme",
		AdminCertPEM:    onboardingPEM(leaf),
		TrustAnchorsPEM: onboardingPEM(root.cert),
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain:  onboardingTrustDomain,
		TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()),
		Binder:       &fakeBinder{}, Logger: discardLogger(),
	})
	var rejected *AdminCredentialRejectedError
	if !errors.As(err, &rejected) || rejected.Unwrap() == nil {
		t.Fatalf("a non-chaining BYOC cert = %v, want *AdminCredentialRejectedError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("the tenant was created despite a refused admin credential")
	}
}

func TestProvisionTenant_BYOCPath_WrongIdentityRefusedBeforeCreatingTheTenant(t *testing.T) {
	ctx := context.Background()
	root := newOnboardingRoot(t, "acme-root")
	// Names "mallory", not the configured admin username ("admin" default).
	leaf := root.clientLeaf(t, "mallory", "acme")
	tenants := &fakeTenants{}

	_, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:        "acme",
		AdminCertPEM:    onboardingPEM(leaf),
		TrustAnchorsPEM: onboardingPEM(root.cert),
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain:  onboardingTrustDomain,
		TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()),
		Binder:       &fakeBinder{}, Logger: discardLogger(),
	})
	var rejected *AdminCredentialRejectedError
	var mismatch *certenroll.IdentityMismatchError
	if !errors.As(err, &rejected) || !errors.As(err, &mismatch) {
		t.Fatalf("a BYOC cert naming the wrong admin = %v, want *AdminCredentialRejectedError wrapping *certenroll.IdentityMismatchError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("the tenant was created despite a refused admin credential")
	}
}

// ---------------------------------------------------------------------------
// Seam validation
// ---------------------------------------------------------------------------

func TestProvisionTenant_MissingSignerForCSRRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "Signer") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming Signer", err)
	}
}

// A certificate-enrolled admin is named by a SPIFFE ID under the node's trust
// domain, so provisioning one without a trust domain is a missing seam, refused
// before the tenant exists -- whether the admin brings a CSR or a certificate.
func TestProvisionTenant_MissingTrustDomainRejectedBeforeCreatingTheTenant(t *testing.T) {
	cases := map[string]TenantProvisionConfig{
		"csr":  {TenantID: "acme", AdminCSRPEM: validAdminCSR(t)},
		"byoc": {TenantID: "acme", AdminCertPEM: realCertPEM(t)},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			tenants := &fakeTenants{}
			_, err := ProvisionTenant(context.Background(), cfg, Dependencies{
				Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
				TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()),
				Signer:       &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
				Logger: discardLogger(),
			})
			var want *MissingProvisioningSeamError
			if !errors.As(err, &want) || !strings.Contains(want.Seam, "TrustDomain") {
				t.Fatalf("err = %v, want *MissingProvisioningSeamError naming TrustDomain", err)
			}
			if len(tenants.createCalls) != 0 {
				t.Fatal("the tenant was created before the missing trust domain was refused")
			}
		})
	}
}

func TestProvisionTenant_MissingTrustAnchorsRegistryRejected(t *testing.T) {
	root := newOnboardingRoot(t, "root")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminCSRPEM:     validAdminCSR(t),
		TrustAnchorsPEM: onboardingPEM(root.cert),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "TrustAnchors") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming TrustAnchors", err)
	}
}

func TestProvisionTenant_MissingBinderRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer:      &fakeSigner{cert: realCertPEM(t)},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "binder") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming a certificate binder", err)
	}
}

func TestProvisionTenant_MissingPendingAdminsForBarrierIsolationRejected(t *testing.T) {
	// Default isolation keeps IAM behind the tenant's own barrier.
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "PendingAdmins") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming PendingAdmins", err)
	}
}

func TestProvisionTenant_ExclusiveWithoutUndoCreateTenantSeamRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: &tenantCreatorOnly{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "UndoCreateTenant") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming UndoCreateTenant", err)
	}
}

func TestProvisionTenant_ExclusiveWithoutDeleteUserSeamRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &userCreatorOnly{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) || !strings.Contains(want.Seam, "DeleteUser") {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError naming DeleteUser", err)
	}
}

// ---------------------------------------------------------------------------
// Exclusive provisioning: refusal and compensation
// ---------------------------------------------------------------------------

func TestProvisionTenant_ExclusiveRefusesAnExistingTenantWithNothingUndone(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	// Nothing was created yet, so fail() returns the bare cause -- not a
	// ProvisioningUndoneError -- and TenantRecordCreationError unwraps to
	// tenant.ErrTenantAlreadyExists.
	var undone *ProvisioningUndoneError
	if errors.As(err, &undone) {
		t.Fatalf("err = %v, want no ProvisioningUndoneError wrapper (nothing was created)", err)
	}
	var want *TenantRecordCreationError
	if !errors.As(err, &want) || !errors.Is(err, tenant.ErrTenantAlreadyExists) {
		t.Fatalf("err = %v, want *TenantRecordCreationError wrapping ErrTenantAlreadyExists", err)
	}
}

func TestProvisionTenant_ExclusiveCompensatesOnBindingFailure(t *testing.T) {
	tenants := &fakeTenants{}
	bindErr := errors.New("binding store down")
	users := &fakeCertBindingUsers{bindErr: bindErr}
	signer := &fakeSigner{cert: realCertPEM(t)} // does not implement certRevoker.

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		Signer: signer, Logger: discardLogger(),
		TrustDomain: onboardingTrustDomain,
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) || !errors.Is(undone, bindErr) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError wrapping the binding failure", err)
	}
	if len(undone.UndoFailures) != 0 {
		t.Fatalf("UndoFailures = %v, want none (every undo step succeeded)", undone.UndoFailures)
	}
	if len(tenants.undoCalls) != 1 || tenants.undoCalls[0] != "acme" {
		t.Errorf("tenant deleteCalls = %v, want [acme]", tenants.undoCalls)
	}
	if len(users.deleteCalls) != 1 {
		t.Errorf("user deleteCalls = %v, want the one created admin undone", users.deleteCalls)
	}
	if undone.Error() == "" {
		t.Fatal("ProvisioningUndoneError has empty text")
	}
}

func TestProvisionTenant_ExclusiveCompensatesOnSignerFailureAfterUserCreation(t *testing.T) {
	tenants := &fakeTenants{}
	users := &fakeCertBindingUsers{} // binding would succeed; never reached.
	signerErr := errors.New("key b ca unavailable")
	signer := &fakeSignerErr{err: signerErr}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		Signer: signer, Logger: discardLogger(),
		TrustDomain: onboardingTrustDomain,
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) || !errors.Is(undone, signerErr) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError wrapping the signer failure", err)
	}
	if len(tenants.undoCalls) != 1 {
		t.Errorf("tenant deleteCalls = %v, want the tenant undone", tenants.undoCalls)
	}
	if len(users.deleteCalls) != 1 {
		t.Errorf("user deleteCalls = %v, want the admin undone", users.deleteCalls)
	}
	if len(users.bindCalls) != 0 {
		t.Error("AddCertBinding was called despite the signer failing first")
	}
}

func TestProvisionTenant_ExclusiveRevokesASignedCertificateOnBindingFailure(t *testing.T) {
	tenants := &fakeTenants{}
	bindErr := errors.New("binding store down")
	users := &fakeCertBindingUsers{bindErr: bindErr}
	signer := &fakeRevokingSigner{fakeSigner: fakeSigner{cert: realCertPEM(t)}}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		Signer: signer, Logger: discardLogger(),
		TrustDomain: onboardingTrustDomain,
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError", err)
	}
	if len(signer.revokeCalls) != 1 {
		t.Fatalf("revokeCalls = %v, want the signed certificate revoked", signer.revokeCalls)
	}
	if len(undone.UndoFailures) != 0 {
		t.Fatalf("UndoFailures = %v, want none", undone.UndoFailures)
	}
}

func TestProvisionTenant_ExclusiveReportsAFailedUndoStep(t *testing.T) {
	tenants := &fakeTenants{}
	bindErr := errors.New("binding store down")
	undeleteErr := errors.New("user store also down")
	users := &fakeCertBindingUsers{bindErr: bindErr}
	users.deleteErr = undeleteErr
	signer := &fakeSigner{cert: realCertPEM(t)}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		Signer: signer, Logger: discardLogger(),
		TrustDomain: onboardingTrustDomain,
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError", err)
	}
	if len(undone.UndoFailures) != 1 {
		t.Fatalf("UndoFailures = %v, want exactly one failed step", undone.UndoFailures)
	}
	var stepErr *UndoStepError
	if !errors.As(undone.UndoFailures[0], &stepErr) || !errors.Is(stepErr, undeleteErr) {
		t.Fatalf("UndoFailures[0] = %v, want *UndoStepError wrapping the delete failure", undone.UndoFailures[0])
	}
	if !strings.Contains(stepErr.Error(), "delete tenant admin") {
		t.Errorf("UndoStepError.Error() = %q, want it to name the failed step", stepErr.Error())
	}
	// The tenant delete still ran and succeeded despite the user delete failing.
	if len(tenants.undoCalls) != 1 {
		t.Errorf("tenant deleteCalls = %v, want the tenant still undone", tenants.undoCalls)
	}
	if !strings.Contains(undone.Error(), "undo steps that failed") {
		t.Errorf("ProvisioningUndoneError.Error() = %q, want it to mention the failed undo step", undone.Error())
	}
}

func TestProvisionTenant_ExclusiveCompensatesOnAnchorRegistrationFailure(t *testing.T) {
	ctx := context.Background()
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	// Fill "acme" to the registration limit so the NEXT Add -- the one this
	// provisioning attempt performs -- fails with *trustanchor.TooManyAnchorsError.
	var preexisting []byte
	for range trustanchor.MaxAnchorsPerTenant {
		preexisting = append(preexisting, onboardingPEM(newOnboardingRoot(t, "filler").cert)...)
	}
	if _, err := anchors.Add(ctx, "acme", preexisting); err != nil {
		t.Fatalf("pre-seeding anchors: %v", err)
	}

	tenants := &fakeTenants{}
	oneMore := newOnboardingRoot(t, "one-more")

	_, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:        "acme",
		Isolation:       tenant.IsolationPrefix,
		AdminCSRPEM:     validAdminCSR(t),
		TrustAnchorsPEM: onboardingPEM(oneMore.cert),
		Exclusive:       true,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain:  onboardingTrustDomain,
		TrustAnchors: anchors, Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		Logger: discardLogger(),
	})
	var undone *ProvisioningUndoneError
	var tooMany *trustanchor.TooManyAnchorsError
	if !errors.As(err, &undone) || !errors.As(err, &tooMany) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError wrapping *trustanchor.TooManyAnchorsError", err)
	}
	if len(tenants.undoCalls) != 1 {
		t.Errorf("tenant deleteCalls = %v, want the tenant undone", tenants.undoCalls)
	}
}

func TestProvisionTenant_ExclusiveUndoesRegisteredAnchorsOnALaterFailure(t *testing.T) {
	ctx := context.Background()
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newOnboardingRoot(t, "root")
	tenants := &fakeTenants{}
	signerErr := errors.New("key b ca unavailable")

	_, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:        "acme",
		Isolation:       tenant.IsolationPrefix,
		AdminCSRPEM:     validAdminCSR(t),
		TrustAnchorsPEM: onboardingPEM(root.cert),
		Exclusive:       true,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		TrustDomain:  onboardingTrustDomain,
		TrustAnchors: anchors, Signer: &fakeSignerErr{err: signerErr}, Binder: &fakeBinder{},
		Logger: discardLogger(),
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError", err)
	}
	if len(undone.UndoFailures) != 0 {
		t.Fatalf("UndoFailures = %v, want none", undone.UndoFailures)
	}
	if list, _ := anchors.List(ctx, "acme"); len(list) != 0 {
		t.Errorf("anchors.List(acme) = %v, want the registered anchor undone", list)
	}
	if len(tenants.undoCalls) != 1 {
		t.Errorf("tenant deleteCalls = %v, want the tenant undone", tenants.undoCalls)
	}
}

// ---------------------------------------------------------------------------
// Barrier-isolation (pending-admin) path
// ---------------------------------------------------------------------------

func TestProvisionTenant_BarrierIsolation_StoresAPendingRecordAndCreatesNoUser(t *testing.T) {
	ctx := context.Background()
	roles := &fakeRoles{}
	users := &fakeUsers{}
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())

	result, err := ProvisionTenant(ctx, TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: roles,
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if !result.AdminAwaitsBarrierInit {
		t.Error("result.AdminAwaitsBarrierInit = false, want true")
	}
	if result.AdminUserID != 0 {
		t.Errorf("result.AdminUserID = %d, want 0 (no account yet)", result.AdminUserID)
	}
	if len(users.createCalls) != 0 {
		t.Errorf("user createCalls = %d, want 0", len(users.createCalls))
	}
	if len(roles.createCalls) != 0 {
		t.Errorf("role createCalls = %d, want 0 (roles seed only once the barrier exists)", len(roles.createCalls))
	}
	rec, err := pending.Get(ctx, "acme")
	if err != nil || rec == nil {
		t.Fatalf("pending.Get(acme) = %v, %v; want a stored record", rec, err)
	}
	if rec.Thumbprint != result.AdminCertThumbprint {
		t.Errorf("recorded thumbprint = %q, want %q", rec.Thumbprint, result.AdminCertThumbprint)
	}
}

// TestProvisionTenant_BarrierIsolation_SignsWithThePendingAdminSigner: the
// tenant's own CA is behind the barrier the pending admin has yet to
// initialize, so the injected PendingAdminSigner signs, never Signer.
func TestProvisionTenant_BarrierIsolation_SignsWithThePendingAdminSigner(t *testing.T) {
	keyB := &fakeSigner{cert: realCertPEM(t)}
	cluster := &fakeSigner{cert: realCertPEM(t)}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: keyB, PendingAdminSigner: cluster, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if n := cluster.called.Load(); n != 1 {
		t.Errorf("PendingAdminSigner called %d times, want 1", n)
	}
	if n := keyB.called.Load(); n != 0 {
		t.Errorf("Signer called %d times, want 0 (the tenant's CA is behind its barrier)", n)
	}
	if !bytes.Equal(result.AdminCertPEM, cluster.cert) {
		t.Error("result.AdminCertPEM is not the certificate PendingAdminSigner issued")
	}
}

// TestProvisionTenant_PrefixIsolation_SignsWithSigner: a tenant whose
// identity records are not behind its own barrier creates its admin at once,
// and its CSR goes to Signer even when a PendingAdminSigner is composed.
func TestProvisionTenant_PrefixIsolation_SignsWithSigner(t *testing.T) {
	keyB := &fakeSigner{cert: realCertPEM(t)}
	cluster := &fakeSigner{cert: realCertPEM(t)}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: keyB, PendingAdminSigner: cluster, Binder: &fakeBinder{},
		TrustDomain: onboardingTrustDomain,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if n := keyB.called.Load(); n != 1 {
		t.Errorf("Signer called %d times, want 1", n)
	}
	if n := cluster.called.Load(); n != 0 {
		t.Errorf("PendingAdminSigner called %d times, want 0", n)
	}
}

// TestProvisionTenant_BarrierIsolation_UndoRevokesThroughTheSignerThatSigned:
// an undone exclusive provisioning revokes the admin's certificate with the
// signer that issued it, not with Signer.
func TestProvisionTenant_BarrierIsolation_UndoRevokesThroughTheSignerThatSigned(t *testing.T) {
	keyB := &fakeRevokingSigner{fakeSigner: fakeSigner{cert: realCertPEM(t)}}
	cluster := &fakeRevokingSigner{fakeSigner: fakeSigner{cert: realCertPEM(t)}}
	pending := &pendingAdminsFaulting{inner: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), putErr: errors.New("store unavailable")}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: keyB, PendingAdminSigner: cluster, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	})
	var undone *ProvisioningUndoneError
	if !errors.As(err, &undone) {
		t.Fatalf("err = %v, want *ProvisioningUndoneError", err)
	}
	if len(cluster.revokeCalls) != 1 {
		t.Errorf("PendingAdminSigner revokeCalls = %v, want one", cluster.revokeCalls)
	}
	if len(keyB.revokeCalls) != 0 {
		t.Errorf("Signer revokeCalls = %v, want none", keyB.revokeCalls)
	}
}

// TestProvisionTenant_BarrierIsolation_CSRWithoutPendingSignerRefusedBeforeCreate:
// with no cluster CA to sign a pending admin's CSR (a BYO-TLS node), the CSR
// is refused as system first boot refuses it, and before the tenant record is
// written, so the refusal does not retire the tenant id.
func TestProvisionTenant_BarrierIsolation_CSRWithoutPendingSignerRefusedBeforeCreate(t *testing.T) {
	tenants := &fakeTenants{}
	keyB := &fakeSigner{cert: realCertPEM(t)}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
		Exclusive:   true,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: keyB, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), Logger: discardLogger(),
	})
	var want *ceremony.AdminCredentialRequiresCAError
	if !errors.As(err, &want) || want.Path != "csr-sign" {
		t.Fatalf("err = %v, want *ceremony.AdminCredentialRequiresCAError{Path: csr-sign}", err)
	}
	if n := len(tenants.createCalls); n != 0 {
		t.Errorf("tenant createCalls = %d, want 0 (refused before the tenant exists)", n)
	}
	if n := keyB.called.Load(); n != 0 {
		t.Errorf("Signer called %d times, want 0", n)
	}
}

func TestProvisionTenant_BarrierIsolation_NonExclusiveReRunKeepsTheRecordedCredential(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	signer := &fakeSigner{cert: realCertPEM(t)}
	deps := Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: signer, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	}
	cfg := TenantProvisionConfig{TenantID: "acme", AdminCSRPEM: validAdminCSR(t)}

	first, err := ProvisionTenant(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("first ProvisionTenant: %v", err)
	}
	if signer.called.Load() != 1 {
		t.Fatalf("SignAdminCSR called %d times on first run, want 1", signer.called.Load())
	}

	second, err := ProvisionTenant(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("second ProvisionTenant: %v", err)
	}
	if signer.called.Load() != 1 {
		t.Errorf("SignAdminCSR called %d times after the re-run, want still 1 (no rotation)", signer.called.Load())
	}
	if second.AdminCertThumbprint != first.AdminCertThumbprint {
		t.Errorf("re-run thumbprint = %q, want the recorded one %q", second.AdminCertThumbprint, first.AdminCertThumbprint)
	}
	if !second.AdminAwaitsBarrierInit {
		t.Error("second.AdminAwaitsBarrierInit = false, want true")
	}
}

// provisionAutoUnsealTenant provisions "acme" as a system-managed (auto-unseal)
// barrier tenant with users as its user seam, and returns the result and the
// dependencies a caller completes the pending admin with.
func provisionAutoUnsealTenant(t *testing.T, users firstboot.UserCreator) (*TenantProvisionResult, Dependencies, tenantadmin.Store) {
	t.Helper()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	deps := Dependencies{
		Tenants: &fakeTenants{autoUnseal: true}, Users: users, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	}
	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, deps)
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	return result, deps, pending
}

// testAdminCompletionBounds returns bounds for CompletePendingTenantAdminWithin
// and CompletePendingTenantAdminOnce that run on a fresh fake clock: no
// attempt and no budget ends unless the test advances it, so an in-memory
// attempt on a loaded machine cannot outlast its bound. The retry loop and its
// short backoff waits stay real.
func testAdminCompletionBounds() (AdminCompletionBounds, *clocktest.Fake) {
	clk := clocktest.New()
	return AdminCompletionBounds{
		Budget: 2 * time.Second, AttemptTimeout: 50 * time.Millisecond,
		Delay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Clock: clk,
	}, clk
}

// countingPendingAdmins is a tenantadmin.Store that counts its reads, one per
// completion attempt, and runs onGet with the count as each starts, so a test
// can advance the clock at a given attempt rather than at a wall-clock instant.
type countingPendingAdmins struct {
	tenantadmin.Store
	gets  int
	onGet func(n int)
}

func (s *countingPendingAdmins) Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error) {
	s.gets++
	if s.onGet != nil {
		s.onGet(s.gets)
	}
	return s.Store.Get(ctx, tenantID)
}

// TestProvisionTenant_BarrierIsolation_AutoUnseal_LeavesAdminCompletionDue
// proves ProvisionTenant does not complete a system-managed (auto-unseal)
// barrier tenant's admin inside the create: the account step proposes through
// the tenant's own shard and can outlast the create's request budget, so the
// result reports it due and the caller completes it after answering. Nothing
// else fires tenant.BarrierReadyHook for such a tenant, which is why the
// completion has to be reported rather than left to the hook.
func TestProvisionTenant_BarrierIsolation_AutoUnseal_LeavesAdminCompletionDue(t *testing.T) {
	users := &fakeUsers{}
	result, _, pending := provisionAutoUnsealTenant(t, users)

	if !result.AdminCompletionDue {
		t.Error("result.AdminCompletionDue = false, want true (the caller completes the admin after answering)")
	}
	if result.AdminAwaitsBarrierInit {
		t.Error("result.AdminAwaitsBarrierInit = true, want false (the auto-unseal barrier is already ready)")
	}
	if result.Tenant == nil || result.Tenant.ID != "acme" {
		t.Errorf("result.Tenant = %+v, want the committed acme record", result.Tenant)
	}
	if len(users.createCalls) != 0 {
		t.Errorf("user createCalls = %d, want 0 (the admin account is not created inside the create)", len(users.createCalls))
	}
	rec, err := pending.Get(context.Background(), "acme")
	if err != nil || rec == nil {
		t.Fatalf("pending.Get(acme) = %v, %v; want the pending record left for the completion", rec, err)
	}
}

// TestProvisionTenant_BarrierIsolation_SelfManaged_NoAdminCompletionDue proves
// a self-managed barrier tenant's admin stays with the barrier-ready hook: its
// barrier is not initialized yet, so nothing can complete the admin now.
func TestProvisionTenant_BarrierIsolation_SelfManaged_NoAdminCompletionDue(t *testing.T) {
	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if result.AdminCompletionDue {
		t.Error("result.AdminCompletionDue = true, want false (the hook completes a self-managed barrier's admin)")
	}
	if !result.AdminAwaitsBarrierInit {
		t.Error("result.AdminAwaitsBarrierInit = false, want true")
	}
}

// TestCompletePendingTenantAdminWithin_Completes proves the bounded completion
// creates the pending admin's account, seeds the tenant's roles and deletes
// the pending record.
func TestCompletePendingTenantAdminWithin_Completes(t *testing.T) {
	users := &fakeUsers{}
	_, deps, pending := provisionAutoUnsealTenant(t, users)

	bounds, _ := testAdminCompletionBounds()
	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	if err != nil || !done {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want true, nil", done, err)
	}
	if len(users.createCalls) != 1 || users.createCalls[0].Username != "admin" {
		t.Fatalf("user createCalls = %+v, want one call for the admin account", users.createCalls)
	}
	if got := deps.Roles.(*fakeRoles).createCalls; len(got) == 0 {
		t.Error("role createCalls = 0, want the tenant's built-in roles seeded")
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec != nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record deleted", rec, err)
	}
}

// TestCompletePendingTenantAdminWithin_NothingPending proves a tenant with no
// pending record -- completed already, or never pending -- reports not done
// without an error.
func TestCompletePendingTenantAdminWithin_NothingPending(t *testing.T) {
	deps := Dependencies{
		Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdmins: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), Logger: discardLogger(),
	}
	bounds, _ := testAdminCompletionBounds()
	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	if err != nil || done {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false, nil", done, err)
	}
}

// stallFirstCreateUsers is a fakeUsers whose first CreateUser hangs until its
// context ends, the way a proposal forwarded to a leader that is still
// settling goes unanswered. onStall, when set, runs as that call starts to
// stall, so a test can pass the attempt's deadline on its fake clock.
type stallFirstCreateUsers struct {
	*fakeUsers
	stalled bool
	onStall func()
}

func (s *stallFirstCreateUsers) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if !s.stalled {
		s.stalled = true
		if s.onStall != nil {
			s.onStall()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.fakeUsers.CreateUser(ctx, req)
}

// TestCompletePendingTenantAdminWithin_RetriesAStalledAttempt proves an
// attempt that stalls is cut off at the attempt bound and the next attempt
// completes the admin, well inside the overall budget. The stalled attempt's
// deadline passes on the fake clock, advanced as the attempt starts to stall.
func TestCompletePendingTenantAdminWithin_RetriesAStalledAttempt(t *testing.T) {
	bounds, clk := testAdminCompletionBounds()
	users := &stallFirstCreateUsers{fakeUsers: &fakeUsers{}, onStall: func() { clk.Advance(bounds.AttemptTimeout) }}
	_, deps, pending := provisionAutoUnsealTenant(t, users)

	start := clk.Now()
	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	if err != nil || !done {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want true, nil", done, err)
	}
	if elapsed := clk.Since(start); elapsed >= bounds.Budget {
		t.Errorf("completion took %v on its clock, want it inside the %v budget (the stalled attempt cut off at %v)",
			elapsed, bounds.Budget, bounds.AttemptTimeout)
	}
	if !users.stalled || len(users.createCalls) != 1 {
		t.Errorf("stalled = %v, completed createCalls = %d; want the stalled attempt then one completing call",
			users.stalled, len(users.createCalls))
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec != nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record deleted", rec, err)
	}
}

// TestCompletePendingTenantAdminWithin_StopsWhileSealed proves a sealed tenant
// ends the completion after one attempt and leaves the pending record for the
// barrier-ready hook, which completes it once the barrier unseals.
func TestCompletePendingTenantAdminWithin_StopsWhileSealed(t *testing.T) {
	users := &fakeUsers{createErr: tenant.ErrTenantSealed}
	_, deps, pending := provisionAutoUnsealTenant(t, users)

	bounds, _ := testAdminCompletionBounds()
	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	if done || !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false, ErrTenantSealed", done, err)
	}
	if len(users.createCalls) != 1 {
		t.Errorf("user createCalls = %d, want 1 (a sealed tenant is not retried)", len(users.createCalls))
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec == nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record left for the hook", rec, err)
	}
}

// TestCompletePendingTenantAdminWithin_StopsOnAMissingSeam proves a permanent
// error -- here a composition with no user seam -- is not retried.
func TestCompletePendingTenantAdminWithin_StopsOnAMissingSeam(t *testing.T) {
	bounds, clk := testAdminCompletionBounds()
	_, deps, _ := provisionAutoUnsealTenant(t, &fakeUsers{})
	deps.Users = nil
	// A second attempt would be the error retried: the budget runs out there,
	// so a regression fails this test rather than retrying on a clock that
	// never moves.
	reads := &countingPendingAdmins{Store: deps.PendingAdmins, onGet: func(n int) {
		if n == 2 {
			clk.Advance(bounds.Budget)
		}
	}}
	deps.PendingAdmins = reads

	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	var seam *MissingProvisioningSeamError
	if done || !errors.As(err, &seam) {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false, *MissingProvisioningSeamError", done, err)
	}
	if reads.gets != 1 {
		t.Errorf("attempts = %d, want 1: a permanent error stops at once rather than retrying to the budget", reads.gets)
	}
}

// TestCompletePendingTenantAdminWithin_GivesUpAtItsBudget proves a failure
// that persists is retried until the budget runs out and then reported, with
// the pending record left for the hook. The budget runs out on the fake clock
// as the third attempt starts, after two have failed.
func TestCompletePendingTenantAdminWithin_GivesUpAtItsBudget(t *testing.T) {
	boom := errors.New("boom")
	users := &fakeUsers{createErr: boom}
	_, deps, pending := provisionAutoUnsealTenant(t, users)
	bounds, clk := testAdminCompletionBounds()
	// The attempt bound outlasts the budget, so advancing to the budget passes
	// that one deadline alone: the attempt then ends because the budget did.
	bounds.AttemptTimeout = 2 * bounds.Budget
	reads := &countingPendingAdmins{Store: deps.PendingAdmins, onGet: func(n int) {
		if n == 3 {
			clk.Advance(bounds.Budget)
		}
	}}
	deps.PendingAdmins = reads

	done, err := CompletePendingTenantAdminWithin(context.Background(), "acme", deps, bounds)
	if done || !errors.Is(err, boom) {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false and the last attempt's error", done, err)
	}
	if reads.gets < 3 {
		t.Errorf("attempts = %d, want the failure retried until the budget ran out on the third", reads.gets)
	}
	if len(users.createCalls) < 2 {
		t.Errorf("user createCalls = %d, want the failure retried", len(users.createCalls))
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec == nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record left for the hook", rec, err)
	}
}

// TestCompletePendingTenantAdminOnce_Completes proves the single attempt
// completes the admin as the retrying form does.
func TestCompletePendingTenantAdminOnce_Completes(t *testing.T) {
	users := &fakeUsers{}
	_, deps, pending := provisionAutoUnsealTenant(t, users)

	bounds, _ := testAdminCompletionBounds()
	done, err := CompletePendingTenantAdminOnce(context.Background(), "acme", deps, bounds)
	if err != nil || !done {
		t.Fatalf("CompletePendingTenantAdminOnce = %v, %v; want true, nil", done, err)
	}
	if len(users.createCalls) != 1 || users.createCalls[0].Username != "admin" {
		t.Fatalf("user createCalls = %+v, want one call for the admin account", users.createCalls)
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec != nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record deleted", rec, err)
	}
}

// TestCompletePendingTenantAdminOnce_ReportsAnAttemptPastItsDeadline proves an
// attempt that outlasts its bound fails with the deadline inside the typed
// error and leaves the record for the barrier-ready hook. The deadline passes
// on the fake clock, advanced as the account step starts to stall.
func TestCompletePendingTenantAdminOnce_ReportsAnAttemptPastItsDeadline(t *testing.T) {
	bounds, clk := testAdminCompletionBounds()
	users := &stallFirstCreateUsers{fakeUsers: &fakeUsers{}, onStall: func() { clk.Advance(bounds.AttemptTimeout) }}
	_, deps, pending := provisionAutoUnsealTenant(t, users)

	done, err := CompletePendingTenantAdminOnce(context.Background(), "acme", deps, bounds)
	var create *TenantAdminUserCreationError
	if done || !errors.As(err, &create) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CompletePendingTenantAdminOnce = %v, %v; want false and *TenantAdminUserCreationError wrapping context.DeadlineExceeded", done, err)
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec == nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record left for the hook", rec, err)
	}
}

// TestCompletePendingTenantAdminOnce_DoesNotRetry proves a failed attempt is
// reported at once, with the record left for the barrier-ready hook, where
// the retrying form would spend its budget.
func TestCompletePendingTenantAdminOnce_DoesNotRetry(t *testing.T) {
	boom := errors.New("boom")
	users := &fakeUsers{createErr: boom}
	_, deps, pending := provisionAutoUnsealTenant(t, users)
	bounds, _ := testAdminCompletionBounds()
	bounds.Budget = time.Hour

	done, err := CompletePendingTenantAdminOnce(context.Background(), "acme", deps, bounds)
	if done || !errors.Is(err, boom) {
		t.Fatalf("CompletePendingTenantAdminOnce = %v, %v; want false and the attempt's error", done, err)
	}
	if len(users.createCalls) != 1 {
		t.Errorf("user createCalls = %d, want one attempt", len(users.createCalls))
	}
	if rec, err := pending.Get(context.Background(), "acme"); err != nil || rec == nil {
		t.Errorf("pending.Get(acme) = %v, %v; want the record left for the hook", rec, err)
	}
}

// failThenStallUsers fails its first CreateUser with err and stalls every
// later one until its context ends. onStall, when set, runs as a call starts
// to stall, so a test can end the context at that point rather than at a
// wall-clock instant.
type failThenStallUsers struct {
	*fakeUsers
	err     error
	calls   int
	onStall func()
}

func (s *failThenStallUsers) CreateUser(ctx context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	s.calls++
	if s.calls == 1 {
		return nil, s.err
	}
	if s.onStall != nil {
		s.onStall()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// expiringContext is a context whose deadline passes when expire is called
// rather than at a wall-clock instant: from then on it is done and reports
// context.DeadlineExceeded, as a context past its deadline does. It carries
// no values and no Deadline, so a timeout derived from it keeps its own.
type expiringContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newExpiringContext() *expiringContext {
	return &expiringContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiringContext) expire() { c.once.Do(func() { close(c.done) }) }

func (c *expiringContext) Done() <-chan struct{} { return c.done }

func (c *expiringContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// TestCompletePendingTenantAdminWithin_BudgetCutOffKeepsTheCause proves that
// when the budget runs out part-way through an attempt, the error returned is
// the one that kept the admin pending, not the cut-off. The deadline the
// budget runs under passes once the second attempt has started, by ordering
// rather than by a race against a short wall clock: the budget's context is
// derived from the caller's, which expires as that attempt starts to stall,
// and the budget and the attempt bound run on a fake clock that never moves,
// so the second
// attempt is ended by that deadline alone.
func TestCompletePendingTenantAdminWithin_BudgetCutOffKeepsTheCause(t *testing.T) {
	boom := errors.New("boom")
	ctx := newExpiringContext()
	users := &failThenStallUsers{fakeUsers: &fakeUsers{}, err: boom, onStall: ctx.expire}
	_, deps, _ := provisionAutoUnsealTenant(t, users)
	bounds, _ := testAdminCompletionBounds()

	done, err := CompletePendingTenantAdminWithin(ctx, "acme", deps, bounds)
	if done || !errors.Is(err, boom) {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false and the failure before the cut-off", done, err)
	}
	if users.calls != 2 {
		t.Errorf("CreateUser calls = %d, want the failed attempt then the one the budget cut off", users.calls)
	}
}

// TestCompletePendingTenantAdminWithin_FirstAttemptCutOffReportsTheBudget
// proves the other side: with no earlier failure to report, an attempt the
// budget cuts off reports the deadline. As above, the deadline passes once
// the first attempt has started, not at a wall-clock instant.
func TestCompletePendingTenantAdminWithin_FirstAttemptCutOffReportsTheBudget(t *testing.T) {
	ctx := newExpiringContext()
	users := &failThenStallUsers{fakeUsers: &fakeUsers{}, onStall: ctx.expire}
	users.calls = 1 // every call stalls
	_, deps, _ := provisionAutoUnsealTenant(t, users)
	bounds, _ := testAdminCompletionBounds()

	done, err := CompletePendingTenantAdminWithin(ctx, "acme", deps, bounds)
	if done || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CompletePendingTenantAdminWithin = %v, %v; want false and the budget's deadline", done, err)
	}
	if users.calls != 2 {
		t.Errorf("CreateUser calls = %d, want the one attempt the budget cut off", users.calls-1)
	}
}

// TestCompletePendingTenantAdminWithin_RequiresLogger proves the bounded
// completion refuses a composition with no logger, as the unbounded one does.
func TestCompletePendingTenantAdminWithin_RequiresLogger(t *testing.T) {
	bounds, _ := testAdminCompletionBounds()
	_, err := CompletePendingTenantAdminWithin(context.Background(), "acme", Dependencies{}, bounds)
	var missing *MissingLoggerError
	if !errors.As(err, &missing) {
		t.Fatalf("CompletePendingTenantAdminWithin(no logger) = %v, want *MissingLoggerError", err)
	}
}

// TestAdminCompletionBounds_Defaults proves a zero bound takes its default and
// a set one is kept.
func TestAdminCompletionBounds_Defaults(t *testing.T) {
	got := AdminCompletionBounds{AttemptTimeout: time.Second}.withDefaults()
	if got.Budget <= 0 || got.Delay <= 0 || got.MaxDelay <= 0 || got.Clock == nil {
		t.Errorf("withDefaults() = %+v, want every zero bound defaulted", got)
	}
	if got.AttemptTimeout != time.Second {
		t.Errorf("withDefaults().AttemptTimeout = %v, want the set 1s kept", got.AttemptTimeout)
	}
	clk := clocktest.New()
	if got := (AdminCompletionBounds{Clock: clk}).withDefaults(); got.Clock != clk {
		t.Errorf("withDefaults().Clock = %v, want the set fake kept", got.Clock)
	}
}

func TestProvisionTenant_BarrierIsolation_PendingAdminsReadErrorWrapped(t *testing.T) {
	sentinel := errors.New("store unavailable")
	pending := &pendingAdminsFaulting{inner: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), getErr: sentinel}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	})
	var want *PendingTenantAdminError
	if !errors.As(err, &want) || want.Op != "read" || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *PendingTenantAdminError{Op: read} wrapping the sentinel", err)
	}
}

func TestProvisionTenant_BarrierIsolation_PendingAdminsPutErrorWrapped(t *testing.T) {
	sentinel := errors.New("store unavailable")
	pending := &pendingAdminsFaulting{inner: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), putErr: sentinel}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		PendingAdminSigner: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain:   onboardingTrustDomain,
		PendingAdmins: pending, Logger: discardLogger(),
	})
	var want *PendingTenantAdminError
	if !errors.As(err, &want) || want.Op != "record" || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *PendingTenantAdminError{Op: record} wrapping the sentinel", err)
	}
}

// ---------------------------------------------------------------------------
// CompletePendingTenantAdmin
// ---------------------------------------------------------------------------

func TestCompletePendingTenantAdmin_MissingLoggerRejected(t *testing.T) {
	_, err := CompletePendingTenantAdmin(context.Background(), "acme", Dependencies{})
	var want *MissingLoggerError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
}

func TestCompletePendingTenantAdmin_NilPendingAdminsIsANoOp(t *testing.T) {
	ok, err := CompletePendingTenantAdmin(context.Background(), "acme", Dependencies{Logger: discardLogger()})
	if err != nil || ok {
		t.Fatalf("CompletePendingTenantAdmin with no PendingAdmins = %v, %v; want false, nil", ok, err)
	}
}

func TestCompletePendingTenantAdmin_ReadErrorWrapped(t *testing.T) {
	sentinel := errors.New("store unavailable")
	pending := &pendingAdminsFaulting{inner: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), getErr: sentinel}
	_, err := CompletePendingTenantAdmin(context.Background(), "acme", Dependencies{
		PendingAdmins: pending, Users: &fakeUsers{}, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *PendingTenantAdminError
	if !errors.As(err, &want) || want.Op != "read" || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *PendingTenantAdminError{Op: read} wrapping the sentinel", err)
	}
}

func TestCompletePendingTenantAdmin_MissingUsersOrRolesRejected(t *testing.T) {
	ctx := context.Background()
	newPending := func(t *testing.T) tenantadmin.Store {
		t.Helper()
		s := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
		if err := s.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc"}); err != nil {
			t.Fatal(err)
		}
		return s
	}

	var want *MissingProvisioningSeamError
	if _, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: newPending(t), Roles: &fakeRoles{}, Logger: discardLogger(),
	}); !errors.As(err, &want) {
		t.Fatalf("missing Users: err = %v, want *MissingProvisioningSeamError", err)
	}
	if _, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: newPending(t), Users: &fakeUsers{}, Logger: discardLogger(),
	}); !errors.As(err, &want) {
		t.Fatalf("missing Roles: err = %v, want *MissingProvisioningSeamError", err)
	}
}

func TestCompletePendingTenantAdmin_RoleSeedErrorWrapped(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc"}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("role write failed")
	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: &fakeUsers{}, Roles: &fakeRoles{createErr: sentinel}, Logger: discardLogger(),
	})
	var want *TenantAdminRoleCreationError
	if !errors.As(err, &want) || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *TenantAdminRoleCreationError wrapping the sentinel", err)
	}
}

func TestCompletePendingTenantAdmin_MissingBindingSeamRejected(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc"}); err != nil {
		t.Fatal(err)
	}
	// fakeUsers implements neither AddCertBinding nor is a Binder supplied.
	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: &fakeUsers{}, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *MissingProvisioningSeamError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingProvisioningSeamError", err)
	}
}

func TestCompletePendingTenantAdmin_CreatesBindsAndDeletesTheRecord(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{
		Username: "admin", Thumbprint: "abc123", NotAfter: time.Now().Add(time.Hour).UTC(), Serial: "1",
	}); err != nil {
		t.Fatal(err)
	}
	users := &fakeCertBindingUsers{}
	roles := &fakeRoles{}

	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: roles, Logger: discardLogger(),
	})
	if err != nil || !ok {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v; want true, nil", ok, err)
	}
	if len(users.createCalls) != 1 || users.createCalls[0].Username != "admin" {
		t.Fatalf("user createCalls = %+v, want one for admin", users.createCalls)
	}
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	if len(users.createCalls[0].RoleIDs) != 1 || users.createCalls[0].RoleIDs[0] != wantRoleID {
		t.Errorf("admin RoleIDs = %v, want [%s]", users.createCalls[0].RoleIDs, wantRoleID)
	}
	if len(users.bindCalls) != 1 {
		t.Fatalf("bindCalls = %v, want one binding", users.bindCalls)
	}
	if roles.createCount.Load() != int32(len(role.BuiltInRoles("acme"))) {
		t.Errorf("roles seeded = %d, want %d", roles.createCount.Load(), len(role.BuiltInRoles("acme")))
	}
	rec, err := pending.Get(ctx, "acme")
	if err != nil || rec != nil {
		t.Fatalf("pending.Get(acme) after completion = %v, %v; want nil, nil (record deleted)", rec, err)
	}
}

func TestCompletePendingTenantAdmin_AdoptsAnExistingUserOnReRun(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	users := &fakeUsersWithLookup{
		fakeUsers:  fakeUsers{exists: true},
		lookupUser: &user.User{ID: 42, Username: "admin", TenantID: "acme", RoleIDs: []string{wantRoleID}},
	}
	binder := &fakeBinder{}

	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Binder: binder, Logger: discardLogger(),
	})
	if err != nil || !ok {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v; want true, nil", ok, err)
	}
	if binder.userID != 42 {
		t.Errorf("bound userID = %d, want the adopted existing user 42", binder.userID)
	}
	if rec, _ := pending.Get(ctx, "acme"); rec != nil {
		t.Error("pending record not deleted after adopting the existing user")
	}
}

func TestCompletePendingTenantAdmin_CreateUserErrorWrapped(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("user store down")
	users := &fakeUsers{createErr: sentinel}

	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError wrapping the sentinel", err)
	}
}

func TestCompletePendingTenantAdmin_UsernameExistsWithoutLookupFails(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	users := &fakeUsers{exists: true} // no AdminUserLookup / GetUserByUsername.

	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError", err)
	}
}

func TestCompletePendingTenantAdmin_LookupErrorWrapped(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("lookup store down")
	users := &fakeUsersWithLookup{fakeUsers: fakeUsers{exists: true}, lookupErr: sentinel}

	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError wrapping the sentinel", err)
	}
}

func TestCompletePendingTenantAdmin_BindAdminErrorWrapped(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	users := &fakeUsers{}
	binder := &fakeBinder{err: errors.New("bind store down")}

	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Binder: binder, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
}

func TestCompletePendingTenantAdmin_DeleteRecordErrorWrapped(t *testing.T) {
	ctx := context.Background()
	inner := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := inner.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("delete failed")
	pending := deleteFailingStore{inner: inner, err: sentinel}
	users := &fakeCertBindingUsers{}

	_, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *PendingTenantAdminError
	if !errors.As(err, &want) || want.Op != "delete" || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *PendingTenantAdminError{Op: delete} wrapping the sentinel", err)
	}
}

// deleteFailingStore wraps a tenantadmin.Store, failing only Delete.
type deleteFailingStore struct {
	inner tenantadmin.Store
	err   error
}

func (d deleteFailingStore) Create(ctx context.Context, tenantID string, p *tenantadmin.Pending) error {
	return d.inner.Create(ctx, tenantID, p)
}
func (d deleteFailingStore) Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error) {
	return d.inner.Get(ctx, tenantID)
}
func (d deleteFailingStore) Delete(ctx context.Context, tenantID string, read *tenantadmin.Pending) (bool, error) {
	return false, d.err
}

var _ tenantadmin.Store = deleteFailingStore{}

// ---------------------------------------------------------------------------
// userResolver
// ---------------------------------------------------------------------------

func TestUserResolver_PrefersTheExplicitDependencyOverUsers(t *testing.T) {
	explicit := &fakeUsersWithLookup{lookupUser: &user.User{ID: 1}}
	usersAlsoResolving := &fakeUsersWithLookup{lookupUser: &user.User{ID: 2}}
	got := userResolver(Dependencies{UserResolver: explicit, Users: usersAlsoResolving})
	if got != explicit {
		t.Error("userResolver did not prefer the explicit UserResolver dependency")
	}
}

// ---------------------------------------------------------------------------
// resolveExistingTenantAdmin -- deferred re-run lookup failure
// ---------------------------------------------------------------------------

func TestProvisionTenant_DeferredReRunLookupErrorWrapped(t *testing.T) {
	sentinel := errors.New("lookup store down")
	users := &fakeUsersWithLookup{fakeUsers: fakeUsers{exists: true}, lookupErr: sentinel}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{},
		AdminFactors: &fakeFactorChecker{}, Logger: discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError wrapping the sentinel", err)
	}
}

// ---------------------------------------------------------------------------
// onboarding_errors.go -- text and Unwrap
// ---------------------------------------------------------------------------

func TestOnboardingErrors_DescribeAndUnwrap(t *testing.T) {
	cause := errors.New("cause")

	textOnly := []error{
		&MissingAdminCredentialError{},
		&ConflictingAdminCredentialError{},
	}
	for _, err := range textOnly {
		if err.Error() == "" {
			t.Errorf("%T has empty text", err)
		}
	}

	wrapped := []interface {
		error
		Unwrap() error
	}{
		&AdminCredentialRejectedError{Cause: cause},
		&InvalidTrustAnchorsError{Cause: cause},
		&TrustAnchorRegistrationError{Cause: cause},
		&PendingTenantAdminError{Op: "read", Cause: cause},
		&ProvisioningUndoneError{TenantID: "acme", Cause: cause},
	}
	for _, err := range wrapped {
		if err.Error() == "" {
			t.Errorf("%T has empty text", err)
		}
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}

	seam := &MissingProvisioningSeamError{Seam: "Dependencies.Signer"}
	if !strings.Contains(seam.Error(), "Dependencies.Signer") {
		t.Errorf("MissingProvisioningSeamError.Error() = %q, want it to name the seam", seam.Error())
	}

	withUndoFailures := &ProvisioningUndoneError{
		TenantID: "acme", Cause: cause,
		UndoFailures: []error{&UndoStepError{Step: "delete tenant", Cause: cause}},
	}
	if !strings.Contains(withUndoFailures.Error(), "undo steps that failed") {
		t.Errorf("ProvisioningUndoneError.Error() = %q, want it to mention failed undo steps", withUndoFailures.Error())
	}

	step := &UndoStepError{Step: "delete tenant", Cause: cause}
	if step.Error() == "" || !errors.Is(step, cause) {
		t.Error("UndoStepError does not describe or unwrap its cause")
	}
}

// TestCompletePendingTenantAdmin_RefusesToAdoptAnAccountWithoutTheAdminRole
// proves an existing account of the admin's name is adopted only when it
// holds the tenant's Admin role: anyone else's account is never handed the
// onboarded admin's key, and the pending record survives for a retry.
func TestCompletePendingTenantAdmin_RefusesToAdoptAnAccountWithoutTheAdminRole(t *testing.T) {
	ctx := context.Background()
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	if err := pending.Create(ctx, "acme", &tenantadmin.Pending{Username: "admin", Thumbprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	users := &fakeUsersWithLookup{
		fakeUsers:  fakeUsers{exists: true},
		lookupUser: &user.User{ID: 43, Username: "admin", TenantID: "acme", RoleIDs: []string{role.SystemRoleID("acme", role.RoleNameUser)}},
	}
	binder := &fakeBinder{}
	ok, err := CompletePendingTenantAdmin(ctx, "acme", Dependencies{
		PendingAdmins: pending, Users: users, Roles: &fakeRoles{}, Binder: binder, Logger: discardLogger(),
	})
	var conflict *PendingAdminAccountConflictError
	if ok || !errors.As(err, &conflict) {
		t.Fatalf("CompletePendingTenantAdmin = %v, %v; want false and *PendingAdminAccountConflictError", ok, err)
	}
	if binder.userID != 0 {
		t.Fatalf("the admin's key was bound to account %d", binder.userID)
	}
	if rec, _ := pending.Get(ctx, "acme"); rec == nil {
		t.Fatal("the pending record was deleted although no admin was created")
	}
	if conflict.Error() == "" {
		t.Fatal("empty error message")
	}
}

// TestProvisionTenant_RefusesAnUncertifiableAdminNameBeforeAnyWrite proves
// the admin's name is checked against the SPIFFE round trip before the
// tenant exists: a name the CA would refuse to certify creates nothing, so
// the tenant id is never retired by a compensating delete.
func TestProvisionTenant_RefusesAnUncertifiableAdminNameBeforeAnyWrite(t *testing.T) {
	tenants := &fakeTenants{}
	users := &fakeUsers{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:      "acme",
		Isolation:     tenant.IsolationPrefix,
		AdminUsername: "bad/name",
		AdminCSRPEM:   validAdminCSR(t),
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		TrustDomain: "db.example.com",
		Logger:      discardLogger(),
	})
	var rejected *AdminCredentialRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *AdminCredentialRejectedError", err)
	}
	if len(tenants.createCalls) != 0 || len(users.createCalls) != 0 {
		t.Fatalf("a write happened before the name was refused: tenants %d, users %d", len(tenants.createCalls), len(users.createCalls))
	}
}
