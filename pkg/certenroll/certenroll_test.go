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

package certenroll

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
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// ---------------------------------------------------------------------------
// test infrastructure: a real DefaultCertProvider over an in-memory store
// and a faithful AES-GCM encryption barrier (mirrors pkg/cert's own
// mockBarrier, which that package's test file does not export).
// ---------------------------------------------------------------------------

type fakeBarrier struct {
	mu  sync.RWMutex
	key []byte
}

func newFakeBarrier(t *testing.T) *fakeBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generating a test barrier key: %v", err)
	}
	return &fakeBarrier{key: key}
}

func (b *fakeBarrier) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (b *fakeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
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

func (b *fakeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *fakeBarrier) IsSealed() bool { return false }

// newCertProvider returns a real DefaultCertProvider, initialized and ready
// to issue under leafTrustDomain, over a fresh in-memory store and barrier.
func newCertProvider(t *testing.T) cert.CertProvider {
	t.Helper()
	p := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), newFakeBarrier(t), cert.WithTrustDomain(leafTrustDomain))
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// test CA/certificate helpers, independent of pkg/trustanchor's own (that
// package's are unexported to its test file).
// ---------------------------------------------------------------------------

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial int64 = 1000

func nextSerial() *big.Int { serial++; return big.NewInt(serial) }

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newRoot(t *testing.T, cn string) *testCA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          nextSerial(),
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

func (ca *testCA) issue(t *testing.T, tmpl *x509.Certificate) *x509.Certificate {
	t.Helper()
	key := newKey(t)
	tmpl.SerialNumber = nextSerial()
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
		tmpl.NotAfter = time.Now().Add(12 * time.Hour)
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

func (ca *testCA) intermediate(t *testing.T, cn string) *testCA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		Subject: pkix.Name{CommonName: cn}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	tmpl.SerialNumber = nextSerial()
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(12 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: c, key: key}
}

// leafTrustDomain is the cluster trust domain the test provider issues under;
// no tenant names it.
const leafTrustDomain = "example.com"

// clientLeaf issues a client-auth leaf naming cn/ou and carrying the SPIFFE
// ID SAN spiffe://<spiffeTenant>/user/<cn>. An empty spiffeTenant names ou --
// the agreeing SAN every tenant certificate must carry
// (auth.TenantFromClientCert).
func (ca *testCA) clientLeaf(t *testing.T, cn, ou, spiffeTenant string) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if spiffeTenant == "" {
		spiffeTenant = ou
	}
	id := spiffe.ID{TrustDomain: spiffeTenant, Kind: spiffe.KindUser, Name: cn}
	u, err := url.Parse(id.String())
	if err != nil {
		t.Fatal(err)
	}
	tmpl.URIs = []*url.URL{u}
	return ca.issue(t, tmpl)
}

func pemOf(c *x509.Certificate) []byte { return trustanchor.EncodePEM(c) }

// ---------------------------------------------------------------------------
// IssueFromCSR
// ---------------------------------------------------------------------------

func TestIssueFromCSR_BindsTheAccountNotTheCSRSubject(t *testing.T) {
	ctx := context.Background()
	prov := newCertProvider(t)

	_, csrPEM, err := certtest.NewCSR("someone-else-entirely")
	if err != nil {
		t.Fatal(err)
	}

	issued, err := IssueFromCSR(ctx, prov, "acme", "alice", csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("IssueFromCSR: %v", err)
	}
	if issued.Thumbprint == "" {
		t.Fatal("no thumbprint returned")
	}
	if issued.Serial == "" {
		t.Fatal("no serial returned")
	}
	certs, err := trustanchor.ParseCertificates(issued.CertPEM)
	if err != nil {
		t.Fatalf("the issued certificate does not decode: %v", err)
	}
	if certs[0].Subject.CommonName != "alice" {
		t.Fatalf("CN = %q, want %q (the account, not the CSR's subject)", certs[0].Subject.CommonName, "alice")
	}
	if len(certs[0].Subject.OrganizationalUnit) == 0 || certs[0].Subject.OrganizationalUnit[0] != "acme" {
		t.Fatalf("OU = %v, want [acme]", certs[0].Subject.OrganizationalUnit)
	}
	if want := "spiffe://acme/user/alice"; len(certs[0].URIs) != 1 || certs[0].URIs[0].String() != want {
		t.Fatalf("URIs = %v, want [%s] (agreeing with the OU)", certs[0].URIs, want)
	}
}

func TestIssueFromCSR_RefusesABadCSRBeforeSigning(t *testing.T) {
	ctx := context.Background()
	prov := newCertProvider(t)

	if _, err := IssueFromCSR(ctx, prov, "acme", "alice", []byte("not a csr"), time.Hour); err == nil {
		t.Fatal("IssueFromCSR accepted a CSR that does not decode")
	}
	records, err := prov.ListCerts(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused CSR signed %d certificates, want 0", len(records))
	}
}

// defectiveIssuer is a cert.CertProvider whose IssueClientCert reports
// success with a certificate that does not decode. It exists to drive
// IssueFromCSR's defence against a defective CertProvider -- a real
// DefaultCertProvider never returns one.
type defectiveIssuer struct{}

func (defectiveIssuer) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	return &cert.IssuedCert{CertPEM: []byte("not a certificate"), Serial: "1", NotAfter: time.Now()}, nil
}
func (defectiveIssuer) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, nil
}
func (defectiveIssuer) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, nil
}
func (defectiveIssuer) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (defectiveIssuer) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (defectiveIssuer) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (defectiveIssuer) CACertPEM() []byte { return nil }

func TestIssueFromCSR_ReportsAnUnreadableCertificateFromTheIssuer(t *testing.T) {
	ctx := context.Background()
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}

	var unreadable *IssuedCertificateUnreadableError
	if _, err := IssueFromCSR(ctx, defectiveIssuer{}, "acme", "alice", csrPEM, time.Hour); !errors.As(err, &unreadable) || unreadable.Unwrap() == nil {
		t.Fatalf("IssueFromCSR over a defective issuer = %v, want *IssuedCertificateUnreadableError", err)
	}
}

func TestIssueFromCSR_PropagatesAnIssuerFailure(t *testing.T) {
	ctx := context.Background()
	prov := newCertProvider(t)

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	// A negative TTL is refused by the provider's own validation -- this
	// exercises IssueFromCSR's propagation of an issuer error distinct from
	// a bad CSR.
	if _, err := IssueFromCSR(ctx, prov, "acme", "alice", csrPEM, -time.Hour); err == nil {
		t.Fatal("IssueFromCSR accepted a negative TTL")
	}
}

// ---------------------------------------------------------------------------
// ParseBYOC
// ---------------------------------------------------------------------------

func TestParseBYOC_SplitsLeafFromIntermediatesAcrossBothInputs(t *testing.T) {
	root := newRoot(t, "root")
	issuing := root.intermediate(t, "issuing")
	leaf := issuing.clientLeaf(t, "alice", "acme", "")

	gotLeaf, inter, err := ParseBYOC(pemOf(leaf), append(pemOf(issuing.cert), pemOf(root.cert)...))
	if err != nil {
		t.Fatalf("ParseBYOC: %v", err)
	}
	if gotLeaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Fatal("the leaf returned is not the certificate presented")
	}
	if len(inter) != 2 {
		t.Fatalf("len(intermediates) = %d, want 2", len(inter))
	}

	// A single certPEM bundle, no separate chain: the intermediate travels
	// with the leaf in one blob.
	gotLeaf2, inter2, err := ParseBYOC(append(pemOf(leaf), pemOf(issuing.cert)...), nil)
	if err != nil {
		t.Fatalf("ParseBYOC with an inline chain: %v", err)
	}
	if gotLeaf2.SerialNumber.Cmp(leaf.SerialNumber) != 0 || len(inter2) != 1 {
		t.Fatalf("ParseBYOC with an inline chain = %v intermediates, want 1 matching the leaf", len(inter2))
	}
}

func TestParseBYOC_RefusesUndecodableInput(t *testing.T) {
	root := newRoot(t, "root")
	var invalid *InvalidCertificateError
	if _, _, err := ParseBYOC([]byte("not a certificate"), nil); !errors.As(err, &invalid) || invalid.Unwrap() == nil {
		t.Fatalf("ParseBYOC with an undecodable cert = %v, want *InvalidCertificateError", err)
	}
	if _, _, err := ParseBYOC(pemOf(root.cert), []byte("not a certificate")); !errors.As(err, &invalid) {
		t.Fatalf("ParseBYOC with an undecodable chain = %v, want *InvalidCertificateError", err)
	}
}

// ---------------------------------------------------------------------------
// VerifyBYOC / VerifyBYOCAgainst
// ---------------------------------------------------------------------------

func TestVerifyBYOC_AcceptsAChainingCertificateForTheRightAccount(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "root")
	issuing := root.intermediate(t, "issuing")
	leaf := issuing.clientLeaf(t, "alice", "acme", "")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}

	byoc, err := VerifyBYOC(ctx, registry, "acme", "alice", pemOf(leaf), pemOf(issuing.cert), "", time.Now())
	if err != nil {
		t.Fatalf("VerifyBYOC: %v", err)
	}
	if byoc.Thumbprint == "" {
		t.Fatal("no thumbprint returned")
	}
	if byoc.Certificate.Subject.CommonName != "alice" {
		t.Fatalf("Certificate CN = %q, want alice", byoc.Certificate.Subject.CommonName)
	}
}

func TestVerifyBYOC_PropagatesAParseFailureBeforeVerifying(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())

	var invalid *InvalidCertificateError
	if _, err := VerifyBYOC(ctx, registry, "acme", "alice", []byte("not a certificate"), nil, "", time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("VerifyBYOC over an undecodable certificate = %v, want *InvalidCertificateError", err)
	}
}

func TestVerifyBYOC_RefusesAnotherTenantsAnchor(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	acmeRoot, globexRoot := newRoot(t, "acme-root"), newRoot(t, "globex-root")
	leaf := acmeRoot.clientLeaf(t, "alice", "acme", "")
	if _, err := registry.Add(ctx, "acme", pemOf(acmeRoot.cert)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(ctx, "globex", pemOf(globexRoot.cert)); err != nil {
		t.Fatal(err)
	}

	var untrusted *trustanchor.CertificateNotTrustedError
	if _, err := VerifyBYOC(ctx, registry, "globex", "alice", pemOf(leaf), nil, "", time.Now()); !errors.As(err, &untrusted) {
		t.Fatalf("a cert under acme's anchor presented to globex = %v, want *trustanchor.CertificateNotTrustedError", err)
	}
}

func TestVerifyBYOC_RefusesWhenTheTenantHasNoAnchors(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "acme", "")

	var none *trustanchor.NoAnchorsError
	if _, err := VerifyBYOC(ctx, registry, "acme", "alice", pemOf(leaf), nil, "", time.Now()); !errors.As(err, &none) {
		t.Fatalf("a tenant with no anchors = %v, want *trustanchor.NoAnchorsError", err)
	}
}

func TestVerifyBYOC_RefusesAMismatchedCommonName(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "mallory", "acme", "")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}

	var mismatch *IdentityMismatchError
	if _, err := VerifyBYOC(ctx, registry, "acme", "alice", pemOf(leaf), nil, "", time.Now()); !errors.As(err, &mismatch) {
		t.Fatalf("a cert naming mallory enrolled on alice = %v, want *IdentityMismatchError", err)
	}
}

func TestVerifyBYOC_RefusesAMismatchedOrganizationalUnit(t *testing.T) {
	ctx := context.Background()
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "globex", "")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	// Register the same root under globex too so the chain verifies there;
	// the mismatch under test is the OU naming a different tenant than the
	// one the certificate is being enrolled on, not the anchor.
	if _, err := registry.Add(ctx, "globex", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}

	var mismatch *IdentityMismatchError
	if _, err := VerifyBYOC(ctx, registry, "acme", "alice", pemOf(leaf), nil, "", time.Now()); !errors.As(err, &mismatch) {
		t.Fatalf("a cert whose OU names globex enrolled under acme = %v, want *IdentityMismatchError", err)
	}
}

func TestVerifyBYOC_RefusesAMismatchedSPIFFETenant(t *testing.T) {
	ctx := context.Background()
	const trustDomain = "qrdb.example.com"
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "root")
	// OU says acme but the pinned SPIFFE SAN says globex. The two must agree,
	// so the certificate names no tenant at all and is refused, whichever
	// tenant it is enrolled under.
	leaf := root.clientLeaf(t, "alice", "acme", "globex")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}

	var mismatch *IdentityMismatchError
	_, err := VerifyBYOC(ctx, registry, "acme", "alice", pemOf(leaf), nil, trustDomain, time.Now())
	if !errors.As(err, &mismatch) {
		t.Fatalf("a SPIFFE SAN naming globex enrolled under acme = %v, want *IdentityMismatchError", err)
	}
	var disagree *auth.TenantNameMismatchError
	if !errors.As(err, &disagree) {
		t.Fatalf("err = %v, want it to wrap *auth.TenantNameMismatchError", err)
	}
	if disagree.OU != "acme" || disagree.SPIFFETenant != "globex" {
		t.Fatalf("TenantNameMismatchError = %+v, want OU acme and SPIFFE tenant globex", disagree)
	}
}

func TestVerifyBYOCAgainst_PropagatesAParseFailureBeforeVerifying(t *testing.T) {
	root := newRoot(t, "root")
	var invalid *InvalidCertificateError
	if _, err := VerifyBYOCAgainst([]*x509.Certificate{root.cert}, "acme", "alice", []byte("not a certificate"), nil, "", time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("VerifyBYOCAgainst over an undecodable certificate = %v, want *InvalidCertificateError", err)
	}
}

func TestVerifyBYOCAgainst_VerifiesAgainstAnExplicitAnchorSetBeforeAnyWrite(t *testing.T) {
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "acme", "")

	byoc, err := VerifyBYOCAgainst([]*x509.Certificate{root.cert}, "acme", "alice", pemOf(leaf), nil, "", time.Now())
	if err != nil {
		t.Fatalf("VerifyBYOCAgainst: %v", err)
	}
	if byoc.Thumbprint == "" {
		t.Fatal("no thumbprint returned")
	}

	other := newRoot(t, "other")
	var untrusted *trustanchor.CertificateNotTrustedError
	if _, err := VerifyBYOCAgainst([]*x509.Certificate{other.cert}, "acme", "alice", pemOf(leaf), nil, "", time.Now()); !errors.As(err, &untrusted) {
		t.Fatalf("VerifyBYOCAgainst with the wrong anchor set = %v, want *trustanchor.CertificateNotTrustedError", err)
	}
}

// ---------------------------------------------------------------------------
// CheckIdentity
// ---------------------------------------------------------------------------

func TestCheckIdentity_AcceptsAMatchingCertificate(t *testing.T) {
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "acme", "")
	if err := CheckIdentity(leaf, "acme", "alice", ""); err != nil {
		t.Fatalf("CheckIdentity: %v", err)
	}
}

func TestCheckIdentity_RefusesAMismatchedUsernameOrTenant(t *testing.T) {
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "acme", "")

	var mismatch *IdentityMismatchError
	if err := CheckIdentity(leaf, "acme", "bob", ""); !errors.As(err, &mismatch) {
		t.Fatalf("a wrong username = %v, want *IdentityMismatchError", err)
	}
	if err := CheckIdentity(leaf, "globex", "alice", ""); !errors.As(err, &mismatch) {
		t.Fatalf("a wrong tenant = %v, want *IdentityMismatchError", err)
	}
}

func TestCheckIdentity_RefusesWhenDerivationErrors(t *testing.T) {
	var mismatch *IdentityMismatchError
	if err := CheckIdentity(nil, "acme", "alice", ""); !errors.As(err, &mismatch) || mismatch.Unwrap() == nil {
		t.Fatalf("CheckIdentity(nil) = %v, want *IdentityMismatchError wrapping the derivation cause", err)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestErrors_DescribeAndUnwrap(t *testing.T) {
	cause := errors.New("cause")

	ice := &InvalidCertificateError{Err: cause}
	if ice.Error() == "" || !errors.Is(ice, cause) {
		t.Fatal("InvalidCertificateError does not describe or unwrap its cause")
	}

	withCause := &IdentityMismatchError{TenantID: "acme", Username: "alice", Err: cause}
	if withCause.Error() == "" || !errors.Is(withCause, cause) {
		t.Fatal("IdentityMismatchError with a derivation cause does not describe or unwrap it")
	}
	withoutCause := &IdentityMismatchError{TenantID: "acme", Username: "alice", CertTenantID: "globex", CertUsername: "mallory"}
	if withoutCause.Error() == "" || withoutCause.Unwrap() != nil {
		t.Fatal("IdentityMismatchError without a cause misbehaves")
	}

	iue := &IssuedCertificateUnreadableError{Err: cause}
	if iue.Error() == "" || !errors.Is(iue, cause) {
		t.Fatal("IssuedCertificateUnreadableError does not describe or unwrap its cause")
	}
}
