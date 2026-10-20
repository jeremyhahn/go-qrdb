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
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// testCert generates a self-signed test certificate with the given CN and OUs.
func testCert(t *testing.T, cn string, ou []string, org []string, dnsNames []string) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:         cn,
			OrganizationalUnit: ou,
			Organization:       org,
		},
		DNSNames:  dnsNames,
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
	}
	template.URIs = agreeingSPIFFEURIs(t, ou, cn)

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create test certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("failed to parse test certificate: %v", err)
	}

	return cert
}

// agreeingSPIFFEURIs returns the SPIFFE ID SAN that agrees with a fixture's
// first OU: a tenant certificate names its tenant in both fields
// (TenantFromClientCert). An absent or empty OU gets no SAN, so a fixture
// naming no tenant stays one.
func agreeingSPIFFEURIs(t *testing.T, ou []string, cn string) []*url.URL {
	t.Helper()
	if len(ou) == 0 || ou[0] == "" {
		return nil
	}
	u, err := spiffe.ID{TrustDomain: ou[0], Kind: spiffe.KindUser, Name: cn}.URI()
	if err != nil {
		t.Fatalf("spiffe id: %v", err)
	}
	return []*url.URL{u}
}

func TestMTLSAuthenticator_Name(t *testing.T) {
	authn := NewMTLSAuthenticator()
	if authn.Name() != "mtls" {
		t.Fatalf("expected Name() to return 'mtls', got %q", authn.Name())
	}
}

func TestMTLSAuthenticator_Authenticate_Success(t *testing.T) {
	cert := testCert(t, "test-node", []string{"tenant-xyz"}, []string{"TestOrg"}, []string{"test.example.com"})

	state := &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	id, err := authn.Authenticate(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if id.Subject != "test-node" {
		t.Fatalf("expected Subject 'test-node', got %q", id.Subject)
	}
	if id.TenantID != "tenant-xyz" {
		t.Fatalf("expected TenantID 'tenant-xyz', got %q", id.TenantID)
	}
	if id.Metadata["organization"] != "TestOrg" {
		t.Fatalf("expected organization 'TestOrg', got %q", id.Metadata["organization"])
	}
	if id.Metadata["dns_san"] != "test.example.com" {
		t.Fatalf("expected dns_san 'test.example.com', got %q", id.Metadata["dns_san"])
	}
}

func TestMTLSAuthenticator_Authenticate_NoTLSState(t *testing.T) {
	authn := NewMTLSAuthenticator()

	_, err := authn.Authenticate(context.Background())
	if err != ErrNoPeerCertificate {
		t.Fatalf("expected ErrNoPeerCertificate, got: %v", err)
	}
}

func TestMTLSAuthenticator_Authenticate_NoPeerCerts(t *testing.T) {
	state := &tls.ConnectionState{
		// No VerifiedChains and no PeerCertificates.
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	_, err := authn.Authenticate(ctx)
	if err != ErrNoPeerCertificate {
		t.Fatalf("expected ErrNoPeerCertificate, got: %v", err)
	}
}

func TestMTLSAuthenticator_Authenticate_UnverifiedCert(t *testing.T) {
	cert := testCert(t, "unverified-node", nil, nil, nil)

	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		// No VerifiedChains - cert present but not verified.
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	_, err := authn.Authenticate(ctx)
	if err != ErrInvalidCertificate {
		t.Fatalf("expected ErrInvalidCertificate, got: %v", err)
	}
}

func TestMTLSAuthenticator_Authenticate_NoOU(t *testing.T) {
	cert := testCert(t, "plain-node", nil, []string{"PlainOrg"}, []string{"plain.example.com"})

	state := &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	id, err := authn.Authenticate(ctx)
	if !errors.Is(err, ErrInvalidCertificate) || id != nil {
		t.Fatalf("Authenticate = (%v, %v), want a refusal for a cert naming no tenant", id, err)
	}
}

func TestMTLSAuthenticator_Authenticate_NoDNSSAN(t *testing.T) {
	cert := testCert(t, "no-san-node", []string{"tenant-1"}, []string{"SomeOrg"}, nil)

	state := &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	id, err := authn.Authenticate(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := id.Metadata["dns_san"]; ok {
		t.Fatalf("expected no dns_san metadata for cert without DNS SANs, got %q", id.Metadata["dns_san"])
	}
}

func TestMTLSAuthenticator_Authenticate_NoOrganization(t *testing.T) {
	cert := testCert(t, "no-org-node", []string{"tenant-2"}, nil, []string{"noorg.example.com"})

	state := &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	id, err := authn.Authenticate(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := id.Metadata["organization"]; ok {
		t.Fatalf("expected no organization metadata, got %q", id.Metadata["organization"])
	}
}

func TestMTLSAuthenticator_Authenticate_EmptyVerifiedChains(t *testing.T) {
	cert := testCert(t, "empty-chain-node", nil, nil, nil)

	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{}}, // Chain present but empty.
	}

	ctx := WithTLSState(context.Background(), state)
	authn := NewMTLSAuthenticator()

	_, err := authn.Authenticate(ctx)
	if err != ErrInvalidCertificate {
		t.Fatalf("expected ErrInvalidCertificate for empty chain, got: %v", err)
	}
}

func TestTLSState_ContextRoundTrip(t *testing.T) {
	state := &tls.ConnectionState{
		ServerName: "test-server.example.com",
	}

	ctx := WithTLSState(context.Background(), state)
	got := TLSStateFromContext(ctx)

	if got == nil {
		t.Fatal("expected non-nil TLS state from context")
	}
	if got.ServerName != "test-server.example.com" {
		t.Fatalf("expected ServerName 'test-server.example.com', got %q", got.ServerName)
	}
}

func TestTLSStateFromContext_Empty(t *testing.T) {
	got := TLSStateFromContext(context.Background())
	if got != nil {
		t.Fatalf("expected nil TLS state from empty context, got %+v", got)
	}
}
