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

package service

import (
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
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// test infrastructure
// ---------------------------------------------------------------------------

// certEnrollmentFakeBarrier is a faithful AES-GCM seal.EncryptionBarrier, the
// same shape pkg/cert's own test file uses, reproduced here because that
// package's double is unexported to its _test.go file.
type certEnrollmentFakeBarrier struct {
	mu  sync.RWMutex
	key []byte
}

func newCertEnrollmentFakeBarrier(t *testing.T) *certEnrollmentFakeBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generating a test barrier key: %v", err)
	}
	return &certEnrollmentFakeBarrier{key: key}
}

func (b *certEnrollmentFakeBarrier) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (b *certEnrollmentFakeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
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

func (b *certEnrollmentFakeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *certEnrollmentFakeBarrier) IsSealed() bool { return false }

// newCertEnrollmentProvider returns a real, initialized DefaultCertProvider
// over a fresh in-memory store.
func newCertEnrollmentProvider(t *testing.T) cert.CertProvider {
	t.Helper()
	p := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), newCertEnrollmentFakeBarrier(t))
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p
}

// newCertEnrollmentUserStore returns a real user.UserServicer over a fresh
// in-memory store.
func newCertEnrollmentUserStore(t *testing.T) user.UserServicer {
	t.Helper()
	svc, err := user.NewStore(kvstore.NewMemoryKVStore(), dao.NewAtomicIncrementGenerator(1))
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// createTestAccount creates an account with no email login key (certificate-
// only identities are a legitimate use, per CreateUserRequest.WithoutEmailLoginKey).
func createTestAccount(t *testing.T, users user.UserServicer, tenantID, username string) *user.User {
	t.Helper()
	u, err := users.CreateUser(context.Background(), &user.CreateUserRequest{
		Username:             username,
		TenantID:             tenantID,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u
}

// failingAddBindingUsers wraps a real UserServicer, delegating every method
// except AddCertBinding -- which it faithfully fails, including against the
// real store's error contract for every OTHER call, so GetUserByUsername
// still resolves real accounts.
type failingAddBindingUsers struct {
	user.UserServicer
	err error
}

func (f *failingAddBindingUsers) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	return nil, f.err
}

// failingRevokeCertProvider wraps a real cert.CertProvider, delegating every
// method except RevokeCert, which it faithfully fails.
type failingRevokeCertProvider struct {
	cert.CertProvider
	err error
}

func (f *failingRevokeCertProvider) RevokeCert(ctx context.Context, serial string) (*cert.RevokeResult, error) {
	return nil, f.err
}

// ---------------------------------------------------------------------------
// test CA/certificate helpers for BYOC enrollment
// ---------------------------------------------------------------------------

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var certEnrollmentSerial int64 = 5000

func nextCertEnrollmentSerial() *big.Int {
	certEnrollmentSerial++
	return big.NewInt(certEnrollmentSerial)
}

func newTestCAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newTestRoot(t *testing.T, cn string) *testCA {
	t.Helper()
	key := newTestCAKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          nextCertEnrollmentSerial(),
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
	return &testCA{cert: c, key: key}
}

func (ca *testCA) clientLeaf(t *testing.T, cn, ou string) *x509.Certificate {
	t.Helper()
	key := newTestCAKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: nextCertEnrollmentSerial(),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}},
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

func pemOf(c *x509.Certificate) []byte { return trustanchor.EncodePEM(c) }

// ---------------------------------------------------------------------------
// SignCertificate
// ---------------------------------------------------------------------------

func TestSignCertificate_BindsAnExistingAccount(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	acct := createTestAccount(t, users, "acme", "alice")
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: time.Hour})
	if err != nil {
		t.Fatalf("SignCertificate: %v", err)
	}
	if result.UserID != acct.ID || result.Username != "alice" || result.Thumbprint == "" {
		t.Fatalf("result = %+v, want it bound to %d/alice with a thumbprint", result, acct.ID)
	}

	got, err := users.GetUser(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CertBindings) != 1 {
		t.Fatalf("CertBindings = %v, want exactly one", got.CertBindings)
	}
	if got.CertBindings[0].NotAfter.Unix() != result.NotAfter.Unix() {
		t.Fatalf("binding NotAfter = %v, want %v", got.CertBindings[0].NotAfter, result.NotAfter)
	}
	certs, err := trustanchor.ParseCertificates(result.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MatchesCert(certs[0]) {
		t.Fatal("the account does not match the certificate SignCertificate issued")
	}
}

func TestSignCertificate_RefusesANonexistentAccountSigningNothing(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	_, csrPEM, err := certtest.NewCSR("bob")
	if err != nil {
		t.Fatal(err)
	}

	var notFound *user.UserNotFoundError
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "bob", CSRPEM: csrPEM}); !errors.As(err, &notFound) {
		t.Fatalf("SignCertificate for a nonexistent account = %v, want *user.UserNotFoundError", err)
	}
	records, err := provider.ListCerts(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused SignCertificate signed %d certificates, want 0", len(records))
	}
}

func TestSignCertificate_RefusesABadCSRBeforeAnyAccountLookup(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	// "nobody" has no account either, so a wrong error here would be
	// *user.UserNotFoundError -- distinguishing that from the CSR error
	// confirms the CSR is judged first.
	var invalidCSR *cert.InvalidCSRError
	_, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "nobody", CSRPEM: []byte("not a csr")})
	if !errors.As(err, &invalidCSR) {
		t.Fatalf("SignCertificate with a bad CSR = %v, want *cert.InvalidCSRError", err)
	}
}

func TestSignCertificate_RefusesAMissingCSR(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), newCertEnrollmentUserStore(t), nil)

	var field *CertEnrollmentFieldError
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice"}); !errors.As(err, &field) || field.Field != "csr_pem" {
		t.Fatalf("SignCertificate with no CSR = %v, want *CertEnrollmentFieldError{csr_pem}", err)
	}
	if _, err := svc.SignCertificate(ctx, nil); !errors.As(err, &field) || field.Field != "csr_pem" {
		t.Fatalf("SignCertificate(nil) = %v, want *CertEnrollmentFieldError{csr_pem}", err)
	}
}

func TestSignCertificate_RefusesANegativeTTL(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), newCertEnrollmentUserStore(t), nil)
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}

	var field *CertEnrollmentFieldError
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: -time.Hour}); !errors.As(err, &field) || field.Field != "ttl" {
		t.Fatalf("SignCertificate with a negative TTL = %v, want *CertEnrollmentFieldError{ttl}", err)
	}
}

func TestSignCertificate_NotConfiguredWithoutACertProvider(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(nil, newCertEnrollmentUserStore(t), nil)
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: []byte("x")}); !errors.As(err, &notConfigured) || notConfigured.Capability != "certificate issuance" {
		t.Fatalf("SignCertificate without a cert provider = %v, want *CertEnrollmentNotConfiguredError{certificate issuance}", err)
	}
}

func TestSignCertificate_NotConfiguredWithoutUsers(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), nil, nil)
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM}); !errors.As(err, &notConfigured) || notConfigured.Capability != "user accounts" {
		t.Fatalf("SignCertificate without users configured = %v, want *CertEnrollmentNotConfiguredError{user accounts}", err)
	}
}

func TestSignCertificate_RevokesTheIssuedCertificateWhenBindingFails(t *testing.T) {
	ctx := context.Background()
	realUsers := newCertEnrollmentUserStore(t)
	createTestAccount(t, realUsers, "acme", "alice")
	users := &failingAddBindingUsers{UserServicer: realUsers, err: errors.New("binding boom")}
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil)

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}

	var bindingFailed *CertBindingFailedError
	_, err = svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM})
	if !errors.As(err, &bindingFailed) || bindingFailed.Serial == "" || bindingFailed.RevokeErr != nil {
		t.Fatalf("SignCertificate over a failing binding = %v, want *CertBindingFailedError naming a serial with no RevokeErr", err)
	}
	revoked, err := provider.IsRevoked(ctx, bindingFailed.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatalf("serial %s was not revoked after its binding failed", bindingFailed.Serial)
	}
	if bindingFailed.Error() == "" || !errors.Is(bindingFailed, bindingFailed.Err) {
		t.Fatal("CertBindingFailedError does not describe or unwrap its cause")
	}
}

func TestSignCertificate_ReportsWhenRevocationAlsoFails(t *testing.T) {
	ctx := context.Background()
	realUsers := newCertEnrollmentUserStore(t)
	createTestAccount(t, realUsers, "acme", "alice")
	users := &failingAddBindingUsers{UserServicer: realUsers, err: errors.New("binding boom")}
	realProvider := newCertEnrollmentProvider(t)
	revokeErr := errors.New("revoke boom")
	provider := &failingRevokeCertProvider{CertProvider: realProvider, err: revokeErr}
	svc := NewCertEnrollmentService(provider, users, nil)

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}

	var bindingFailed *CertBindingFailedError
	_, err = svc.SignCertificate(ctx, &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM})
	if !errors.As(err, &bindingFailed) || bindingFailed.Serial == "" || !errors.Is(bindingFailed.RevokeErr, revokeErr) {
		t.Fatalf("SignCertificate when revocation also fails = %v, want *CertBindingFailedError carrying RevokeErr", err)
	}
	if bindingFailed.Error() == "" {
		t.Fatal("CertBindingFailedError has empty text")
	}
}

// ---------------------------------------------------------------------------
// EnrollCertificate
// ---------------------------------------------------------------------------

func TestEnrollCertificate_VerifiesAgainstTenantAnchorsAndBinds(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	acct := createTestAccount(t, users, "acme", "alice")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	leaf := root.clientLeaf(t, "alice", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	result, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)})
	if err != nil {
		t.Fatalf("EnrollCertificate: %v", err)
	}
	if result.UserID != acct.ID || result.Thumbprint == "" || result.Serial != "" {
		t.Fatalf("result = %+v, want it bound to %d/alice with no serial (not issued by this cluster)", result, acct.ID)
	}
	got, err := users.GetUser(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MatchesCert(leaf) {
		t.Fatal("the account does not match the enrolled certificate")
	}
}

func TestEnrollCertificate_RefusesAnotherTenantsAnchor(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	acmeRoot, globexRoot := newTestRoot(t, "acme-root"), newTestRoot(t, "globex-root")
	if _, err := registry.Add(ctx, "acme", pemOf(acmeRoot.cert)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(ctx, "globex", pemOf(globexRoot.cert)); err != nil {
		t.Fatal(err)
	}
	// Chains to globex's anchor, not acme's.
	leaf := globexRoot.clientLeaf(t, "alice", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	var untrusted *trustanchor.CertificateNotTrustedError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)}); !errors.As(err, &untrusted) {
		t.Fatalf("EnrollCertificate under another tenant's anchor = %v, want *trustanchor.CertificateNotTrustedError", err)
	}
}

func TestEnrollCertificate_RefusesAnIdentityMismatch(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	// Names mallory, not alice.
	leaf := root.clientLeaf(t, "mallory", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	var mismatch *certenroll.IdentityMismatchError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)}); !errors.As(err, &mismatch) {
		t.Fatalf("EnrollCertificate naming mallory for alice = %v, want *certenroll.IdentityMismatchError", err)
	}
}

func TestEnrollCertificate_RefusesANonexistentAccount(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	leaf := root.clientLeaf(t, "bob", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	var notFound *user.UserNotFoundError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "bob", CertPEM: pemOf(leaf)}); !errors.As(err, &notFound) {
		t.Fatalf("EnrollCertificate for a nonexistent account = %v, want *user.UserNotFoundError", err)
	}
}

func TestEnrollCertificate_RefusesAMissingCertPEM(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(nil, newCertEnrollmentUserStore(t), trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore()))
	var field *CertEnrollmentFieldError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice"}); !errors.As(err, &field) || field.Field != "cert_pem" {
		t.Fatalf("EnrollCertificate with no cert = %v, want *CertEnrollmentFieldError{cert_pem}", err)
	}
	if _, err := svc.EnrollCertificate(ctx, nil); !errors.As(err, &field) || field.Field != "cert_pem" {
		t.Fatalf("EnrollCertificate(nil) = %v, want *CertEnrollmentFieldError{cert_pem}", err)
	}
}

func TestEnrollCertificate_NotConfiguredWithoutAnchors(t *testing.T) {
	ctx := context.Background()
	svc := NewCertEnrollmentService(nil, newCertEnrollmentUserStore(t), nil)
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: []byte("x")}); !errors.As(err, &notConfigured) || notConfigured.Capability != "trust anchors" {
		t.Fatalf("EnrollCertificate without anchors = %v, want *CertEnrollmentNotConfiguredError{trust anchors}", err)
	}
}

func TestEnrollCertificate_ReportsABindingFailureWithoutRevocation(t *testing.T) {
	ctx := context.Background()
	realUsers := newCertEnrollmentUserStore(t)
	createTestAccount(t, realUsers, "acme", "alice")
	users := &failingAddBindingUsers{UserServicer: realUsers, err: errors.New("binding boom")}
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	leaf := root.clientLeaf(t, "alice", "acme")
	svc := NewCertEnrollmentService(nil, users, registry)

	var bindingFailed *CertBindingFailedError
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)}); !errors.As(err, &bindingFailed) || bindingFailed.Serial != "" {
		t.Fatalf("EnrollCertificate over a failing binding = %v, want *CertBindingFailedError with no serial (never revocable)", err)
	}
}

// ---------------------------------------------------------------------------
// Trust anchor verbs
// ---------------------------------------------------------------------------

func TestAddTrustAnchors_DelegatesAndRefusesWithoutAnchors(t *testing.T) {
	ctx := anchorCaller("acme", "acme-admin")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	svc := NewCertEnrollmentService(nil, nil, registry)
	root := newTestRoot(t, "root")

	added, err := svc.AddTrustAnchors(ctx, "acme", pemOf(root.cert))
	if err != nil || len(added) != 1 {
		t.Fatalf("AddTrustAnchors = %v, %v; want one anchor added", added, err)
	}

	unconfigured := NewCertEnrollmentService(nil, nil, nil)
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := unconfigured.AddTrustAnchors(ctx, "acme", pemOf(root.cert)); !errors.As(err, &notConfigured) {
		t.Fatalf("AddTrustAnchors without anchors = %v, want *CertEnrollmentNotConfiguredError", err)
	}
}

func TestListTrustAnchors_DelegatesAndRefusesWithoutAnchors(t *testing.T) {
	ctx := anchorCaller("acme", "acme-admin")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	svc := NewCertEnrollmentService(nil, nil, registry)

	list, err := svc.ListTrustAnchors(ctx, "acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTrustAnchors = %v, %v; want one anchor", list, err)
	}

	unconfigured := NewCertEnrollmentService(nil, nil, nil)
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := unconfigured.ListTrustAnchors(ctx, "acme"); !errors.As(err, &notConfigured) {
		t.Fatalf("ListTrustAnchors without anchors = %v, want *CertEnrollmentNotConfiguredError", err)
	}
}

func TestRemoveTrustAnchor_DelegatesAndRefusesWithoutAnchors(t *testing.T) {
	ctx := anchorCaller("acme", "acme-admin")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	svc := NewCertEnrollmentService(nil, nil, registry)

	if err := svc.RemoveTrustAnchor(ctx, "acme", trustanchor.Fingerprint(root.cert)); err != nil {
		t.Fatalf("RemoveTrustAnchor: %v", err)
	}
	var notFound *trustanchor.AnchorNotFoundError
	if err := svc.RemoveTrustAnchor(ctx, "acme", trustanchor.Fingerprint(root.cert)); !errors.As(err, &notFound) {
		t.Fatalf("removing it again = %v, want *trustanchor.AnchorNotFoundError (delegated, not swallowed)", err)
	}

	unconfigured := NewCertEnrollmentService(nil, nil, nil)
	var notConfigured *CertEnrollmentNotConfiguredError
	if err := unconfigured.RemoveTrustAnchor(ctx, "acme", "fp"); !errors.As(err, &notConfigured) {
		t.Fatalf("RemoveTrustAnchor without anchors = %v, want *CertEnrollmentNotConfiguredError", err)
	}
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

func TestWithEnrollmentClock_SetsTheClock(t *testing.T) {
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	svc := NewCertEnrollmentService(nil, nil, nil, WithEnrollmentClock(func() time.Time { return fixed }))
	if got := svc.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %v, want %v", got, fixed)
	}
}

func TestWithEnrollmentClock_NilLeavesTheDefault(t *testing.T) {
	svc := NewCertEnrollmentService(nil, nil, nil, WithEnrollmentClock(nil))
	if svc.now == nil || svc.now().IsZero() {
		t.Fatal("WithEnrollmentClock(nil) cleared the default clock")
	}
}

func TestWithEnrollmentTrustDomain_SetsTheTrustDomain(t *testing.T) {
	svc := NewCertEnrollmentService(nil, nil, nil, WithEnrollmentTrustDomain("qrdb.example.com"))
	if svc.trustDomain != "qrdb.example.com" {
		t.Fatalf("trustDomain = %q, want qrdb.example.com", svc.trustDomain)
	}
}

func TestWithEnrollmentTrustDomain_DefaultsToEmpty(t *testing.T) {
	svc := NewCertEnrollmentService(nil, nil, nil)
	if svc.trustDomain != "" {
		t.Fatalf("trustDomain = %q, want empty by default", svc.trustDomain)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestCertEnrollmentErrors_DescribeAndUnwrap(t *testing.T) {
	if (&CertEnrollmentNotConfiguredError{Capability: "trust anchors"}).Error() == "" {
		t.Fatal("CertEnrollmentNotConfiguredError has empty text")
	}
	if (&CertEnrollmentFieldError{Field: "csr_pem"}).Error() == "" {
		t.Fatal("CertEnrollmentFieldError has empty text")
	}

	cause := errors.New("cause")
	revokeCause := errors.New("revoke cause")

	plain := &CertBindingFailedError{Err: cause}
	if plain.Error() == "" || !errors.Is(plain, cause) {
		t.Fatal("CertBindingFailedError without a serial misbehaves")
	}
	revoked := &CertBindingFailedError{Serial: "42", Err: cause}
	if revoked.Error() == "" {
		t.Fatal("CertBindingFailedError with a revoked serial has empty text")
	}
	revokeFailed := &CertBindingFailedError{Serial: "42", Err: cause, RevokeErr: revokeCause}
	if revokeFailed.Error() == "" {
		t.Fatal("CertBindingFailedError with a failed revocation has empty text")
	}
}
