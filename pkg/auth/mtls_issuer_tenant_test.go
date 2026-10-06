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

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

// These tests close the issuer-tenant binding requirement: every tenant's
// Key B CA is trusted on one shared client-facing listener
// (pkg/clienttrust), so the authenticator must refuse a leaf whose issuing
// CA is NOT the tenant its own Subject OU names (unless that issuer is the
// cluster CA, Key A, which legitimately signs holder and node certificates
// for any tenant). See docs/certificate-authority/architecture.md,
// "Issuer-tenant binding".
//
// The binding is exercised here through the auth.IssuerVerifier interface
// (fakeIssuerVerifier below) rather than a concrete struct: in production
// *clienttrust.Pool is the SAME object that widens the TLS ClientCAs pool
// and answers these two methods (go-qrdb#560 review, B2-2) -- this package
// only depends on the interface, never on clienttrust, to avoid an import
// cycle.

// testCA generates a self-signed CA certificate with the given CN, and
// returns it alongside the signing key so testLeafSignedBy can issue leaves
// under it.
func testCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (CA): %v", err)
	}
	return ca, key
}

// testLeafSignedBy issues a leaf certificate under ca/caKey, naming ou as
// its Subject Organizational Unit (the tenant, under this package's legacy
// OU-fallback rule).
func testLeafSignedBy(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, ou []string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: ou},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (leaf): %v", err)
	}
	return leaf
}

// fakeIssuerNotTrustedError is the typed error fakeIssuerVerifier returns
// for every refusal; the authenticator never inspects its value -- it only
// cares whether VerifyIssuer returned nil -- but a typed struct, not a bare
// errors.New sentinel, keeps the double honest to this project's
// typed-errors-only convention.
type fakeIssuerNotTrustedError struct{ TenantID string }

func (e *fakeIssuerNotTrustedError) Error() string {
	return "auth: issuer not trusted for tenant " + e.TenantID
}

// fakeIssuerVerifier implements auth.IssuerVerifier the way *clienttrust.Pool
// does in production: ONE object answers both IsClusterCA and VerifyIssuer,
// so a test can never construct the "Key B trusted but binding unenforced"
// state B2-2 ruled out. staticallyTrusted mirrors
// clienttrust.Pool.IsStaticallyTrusted's exemption -- a CA the composer
// already trusted via the listener's own static ClientCAs, exempt from the
// per-tenant check for a DIFFERENT reason than the cluster CA is -- so a
// test here can exercise that shape too, not only the cluster-CA one.
type fakeIssuerVerifier struct {
	clusterCA         *x509.Certificate            // Key A; nil means none configured
	staticallyTrusted *x509.Certificate            // exempt via static ClientCAs; nil means none configured
	registry          map[string]*x509.Certificate // tenantID -> registered Key B CA
	err               error                        // when set, VerifyIssuer always fails with it
}

func (f *fakeIssuerVerifier) IsClusterCA(_ context.Context, issuer *x509.Certificate) bool {
	if f.clusterCA == nil || issuer == nil {
		return false
	}
	return bytes.Equal(f.clusterCA.Raw, issuer.Raw)
}

// VerifyPeerChain is never exercised by this file's tests: every one of
// them constructs tls.ConnectionState.VerifiedChains directly and
// non-empty, so ResolveVerifiedChain never reaches its fallback branch.
// It errors unconditionally rather than fabricating a chain, so a future
// test that DOES rely on the fallback fails loudly here instead of
// silently asserting a defect is correct.
func (f *fakeIssuerVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, errors.New("fakeIssuerVerifier: VerifyPeerChain not implemented; this fake's tests supply VerifiedChains directly")
}

func (f *fakeIssuerVerifier) isStaticallyTrusted(issuer *x509.Certificate) bool {
	if f.staticallyTrusted == nil || issuer == nil {
		return false
	}
	return bytes.Equal(f.staticallyTrusted.Raw, issuer.Raw)
}

func (f *fakeIssuerVerifier) VerifyIssuer(ctx context.Context, tenantID string, chain []*x509.Certificate) error {
	// Like the real Pool, the Key A and Key B checks judge the direct issuer.
	var issuer *x509.Certificate
	if len(chain) > 1 {
		issuer = chain[1]
	}
	if f.err != nil {
		return f.err
	}
	if f.IsClusterCA(ctx, issuer) || f.isStaticallyTrusted(issuer) {
		return nil
	}
	registered, ok := f.registry[tenantID]
	if !ok || issuer == nil || registered.Raw == nil || issuer.Raw == nil {
		return &fakeIssuerNotTrustedError{TenantID: tenantID}
	}
	if !bytes.Equal(registered.Raw, issuer.Raw) {
		return &fakeIssuerNotTrustedError{TenantID: tenantID}
	}
	return nil
}

func TestMTLSAuthenticator_IssuerTenantBinding_OwnTenantCAAccepted(t *testing.T) {
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"A"})

	verifier := &fakeIssuerVerifier{registry: map[string]*x509.Certificate{"A": keyBCA_A}}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	id, err := authn.Authenticate(ctx)
	if err != nil {
		t.Fatalf("expected a leaf with OU=A signed by tenant A's own Key B CA to be accepted, got: %v", err)
	}
	if id.TenantID != "A" {
		t.Errorf("TenantID = %q, want %q", id.TenantID, "A")
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_ForeignTenantCARefused(t *testing.T) {
	// Tenant A's Key B CA signs a leaf claiming to be tenant B -- the
	// cross-tenant case the review flagged by name.
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	keyBCA_B, _ := testCA(t, "tenant-B-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "mallory", []string{"B"})

	verifier := &fakeIssuerVerifier{registry: map[string]*x509.Certificate{
		"A": keyBCA_A,
		"B": keyBCA_B,
	}}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	_, err := authn.Authenticate(ctx)
	if err == nil {
		t.Fatal("expected a leaf naming tenant B but signed by tenant A's Key B CA to be refused")
	}
	var mismatch *IssuerTenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerTenantMismatchError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrInvalidCertificate) {
		t.Error("expected IssuerTenantMismatchError to match ErrInvalidCertificate")
	}
	if mismatch.TenantID != "B" {
		t.Errorf("TenantID = %q, want %q", mismatch.TenantID, "B")
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_SystemOUFromTenantCARefused(t *testing.T) {
	// A leaf claiming the system OU, signed by an ordinary tenant's Key B
	// CA -- never the cluster CA -- must be refused: a tenant CA has no
	// business minting a system-scoped credential.
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "faux-admin", []string{"system"})

	verifier := &fakeIssuerVerifier{registry: map[string]*x509.Certificate{"A": keyBCA_A}}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	_, err := authn.Authenticate(ctx)
	if err == nil {
		t.Fatal("expected a system-OU leaf signed by a tenant Key B CA to be refused")
	}
	var mismatch *IssuerTenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerTenantMismatchError, got %T: %v", err, err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_ClusterCAExempt(t *testing.T) {
	clusterCA, clusterKey := testCA(t, "cluster-ca")
	keyBCA_B, _ := testCA(t, "tenant-B-key-b")
	// A holder/node leaf signed by the CLUSTER CA, naming tenant B's profile
	// in its OU (the holder-profile convention) -- must authenticate
	// regardless of what tenant B's own registered Key B CA is.
	leaf := testLeafSignedBy(t, clusterCA, clusterKey, "officer", []string{"B"})

	verifier := &fakeIssuerVerifier{
		clusterCA: clusterCA,
		registry:  map[string]*x509.Certificate{"B": keyBCA_B},
	}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, clusterCA}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected a cluster-CA-signed leaf to authenticate regardless of OU, got: %v", err)
	}
}

// TestMTLSAuthenticator_IssuerTenantBinding_StaticallyTrustedCAAccepted
// mirrors ClusterCAExempt for the OTHER exemption clienttrust.Pool grants:
// a CA already trusted via the listener's own static ClientCAs (an
// operator-supplied additional client CA, or a test harness establishing
// mTLS identity independent of Key A/Key B), which is exempt from the
// per-tenant check for a different reason than the cluster CA is.
func TestMTLSAuthenticator_IssuerTenantBinding_StaticallyTrustedCAAccepted(t *testing.T) {
	staticCA, staticKey := testCA(t, "operator-static-ca")
	keyBCA_B, _ := testCA(t, "tenant-B-key-b")
	leaf := testLeafSignedBy(t, staticCA, staticKey, "officer", []string{"B"})

	verifier := &fakeIssuerVerifier{
		staticallyTrusted: staticCA,
		registry:          map[string]*x509.Certificate{"B": keyBCA_B},
	}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, staticCA}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected a statically-trusted CA's leaf to authenticate regardless of OU, got: %v", err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_CheckerErrorRefuses(t *testing.T) {
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"A"})

	verifier := &fakeIssuerVerifier{err: errors.New("barrier is sealed")}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	_, err := authn.Authenticate(ctx)
	if err == nil {
		t.Fatal("expected an error when the issuer verifier itself fails -- fail closed, never open")
	}
	var mismatch *IssuerTenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerTenantMismatchError, got %T: %v", err, err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_NilVerifierUnenforced(t *testing.T) {
	// No WithIssuerVerifier: behavior must be identical to before the check
	// existed, even with a chain whose issuer names no tenant at all.
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"B"}) // mismatched OU, no verifier configured
	authn := NewMTLSAuthenticator()

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected no issuer-tenant enforcement without WithIssuerVerifier, got: %v", err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_AnySatisfyingChainAccepted(t *testing.T) {
	// A TLS stack can report more than one verified chain for the same leaf
	// when multiple roots in the pool could validate it. The binding must
	// accept as soon as ANY chain's direct issuer (chain[1]) satisfies it,
	// not only the first.
	clusterCA, _ := testCA(t, "cluster-ca")
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"A"})

	verifier := &fakeIssuerVerifier{
		clusterCA: clusterCA,
		registry:  map[string]*x509.Certificate{"A": keyBCA_A},
	}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	// First chain's direct issuer is bogus (neither cluster CA nor A's Key
	// B CA); the second chain's direct issuer is A's own Key B CA.
	otherCA, otherKey := testCA(t, "unrelated-ca")
	bogusChain := []*x509.Certificate{leaf, otherCA}
	_ = otherKey
	goodChain := []*x509.Certificate{leaf, keyBCA_A}
	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{bogusChain, goodChain}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected acceptance when a LATER verified chain satisfies the binding, got: %v", err)
	}
}

// TestMTLSAuthenticator_IssuerTenantBinding_ShortChainSkipped proves
// verifyIssuer skips any verified chain with no direct issuer entry
// (len(chain) < 2 -- a bare leaf, which crypto/tls can report when the leaf
// itself is in the root pool) rather than indexing into it, and still
// accepts once a LATER, well-formed chain satisfies the binding.
func TestMTLSAuthenticator_IssuerTenantBinding_ShortChainSkipped(t *testing.T) {
	clusterCA, _ := testCA(t, "cluster-ca")
	keyBCA, keyBKey := testCA(t, "tenant-a-key-b")
	leaf := testLeafSignedBy(t, keyBCA, keyBKey, "alice", []string{"A"})

	verifier := &fakeIssuerVerifier{
		clusterCA: clusterCA,
		registry:  map[string]*x509.Certificate{"A": keyBCA},
	}
	authn := NewMTLSAuthenticator(WithIssuerVerifier(verifier))

	shortChain := []*x509.Certificate{leaf} // no direct issuer entry
	goodChain := []*x509.Certificate{leaf, keyBCA}
	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{shortChain, goodChain}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected the short chain to be skipped and the good chain to satisfy the binding, got: %v", err)
	}
}
