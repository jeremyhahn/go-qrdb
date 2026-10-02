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

// fakeTenantIssuerChecker answers IsTenantIssuer by comparing the candidate
// issuer's raw DER against whichever CA certificate is registered for each
// tenant in registry.
type fakeTenantIssuerChecker struct {
	registry map[string]*x509.Certificate // tenantID -> registered Key B CA
	err      error
}

func (f *fakeTenantIssuerChecker) IsTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	registered, ok := f.registry[tenantID]
	if !ok || issuer == nil {
		return false, nil
	}
	return registered.Raw != nil && issuer.Raw != nil && string(registered.Raw) == string(issuer.Raw), nil
}

func TestMTLSAuthenticator_IssuerTenantBinding_OwnTenantCAAccepted(t *testing.T) {
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"A"})

	binding := &IssuerTenantBinding{
		KeyB: &fakeTenantIssuerChecker{registry: map[string]*x509.Certificate{"A": keyBCA_A}},
	}
	authn := NewMTLSAuthenticator(WithIssuerTenantBinding(binding))

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
	// Tenant A's Key B CA signs a leaf claiming to be tenant B.
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	keyBCA_B, _ := testCA(t, "tenant-B-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "mallory", []string{"B"})

	binding := &IssuerTenantBinding{
		KeyB: &fakeTenantIssuerChecker{registry: map[string]*x509.Certificate{
			"A": keyBCA_A,
			"B": keyBCA_B,
		}},
	}
	authn := NewMTLSAuthenticator(WithIssuerTenantBinding(binding))

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

func TestMTLSAuthenticator_IssuerTenantBinding_ClusterCAExempt(t *testing.T) {
	clusterCA, clusterKey := testCA(t, "cluster-ca")
	keyBCA_B, _ := testCA(t, "tenant-B-key-b")
	// A holder/node leaf signed by the CLUSTER CA, naming tenant B's profile
	// in its OU (the holder-profile convention) -- must authenticate
	// regardless of what tenant B's own registered Key B CA is.
	leaf := testLeafSignedBy(t, clusterCA, clusterKey, "officer", []string{"B"})

	binding := &IssuerTenantBinding{
		ClusterCA: clusterCA,
		KeyB:      &fakeTenantIssuerChecker{registry: map[string]*x509.Certificate{"B": keyBCA_B}},
	}
	authn := NewMTLSAuthenticator(WithIssuerTenantBinding(binding))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, clusterCA}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected a cluster-CA-signed leaf to authenticate regardless of OU, got: %v", err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_CheckerErrorRefuses(t *testing.T) {
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"A"})

	binding := &IssuerTenantBinding{
		KeyB: &fakeTenantIssuerChecker{err: errors.New("barrier is sealed")},
	}
	authn := NewMTLSAuthenticator(WithIssuerTenantBinding(binding))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	_, err := authn.Authenticate(ctx)
	if err == nil {
		t.Fatal("expected an error when the tenant issuer checker itself fails")
	}
	var mismatch *IssuerTenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerTenantMismatchError, got %T: %v", err, err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_NilBindingUnenforced(t *testing.T) {
	// No WithIssuerTenantBinding: behavior must be identical to before the
	// check existed, even with a 2-entry chain whose issuer names no tenant
	// at all.
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"B"}) // mismatched OU, no binding configured
	authn := NewMTLSAuthenticator()

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected no issuer-tenant enforcement without WithIssuerTenantBinding, got: %v", err)
	}
}

func TestMTLSAuthenticator_IssuerTenantBinding_NoKeyBCheckerUnenforced(t *testing.T) {
	// A binding with ClusterCA but no KeyB checker: ClusterCA exemption
	// still applies, but a non-ClusterCA issuer is never refused either
	// (nothing to check against).
	clusterCA, _ := testCA(t, "cluster-ca")
	keyBCA_A, keyBKeyA := testCA(t, "tenant-A-key-b")
	leaf := testLeafSignedBy(t, keyBCA_A, keyBKeyA, "alice", []string{"B"})

	binding := &IssuerTenantBinding{ClusterCA: clusterCA}
	authn := NewMTLSAuthenticator(WithIssuerTenantBinding(binding))

	state := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf, keyBCA_A}}}
	ctx := WithTLSState(context.Background(), state)

	if _, err := authn.Authenticate(ctx); err != nil {
		t.Fatalf("expected no enforcement when KeyB is nil, got: %v", err)
	}
}
