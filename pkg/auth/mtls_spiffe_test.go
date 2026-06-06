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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// spiffeCert builds a self-signed leaf carrying the given SPIFFE URI SAN.
func spiffeCert(t *testing.T, cn string, ou []string, spiffeURI string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: ou},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if spiffeURI != "" {
		u, perr := url.Parse(spiffeURI)
		if perr != nil {
			t.Fatalf("parse uri: %v", perr)
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// Scenario (b): TenantFromCert extracts the tenant from the SPIFFE SAN.
func TestTenantFromCert_SPIFFE(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "alice", nil, "spiffe://example.com/tenant/tenant-acme/user/alice")

	tenantID, fromSPIFFE, err := a.TenantFromCert(cert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromSPIFFE {
		t.Fatal("expected tenant derived from SPIFFE SAN")
	}
	if tenantID != "tenant-acme" {
		t.Fatalf("tenant = %q, want tenant-acme", tenantID)
	}
}

// Scenario (b): a SPIFFE SAN under a foreign trust domain is REJECTED.
func TestTenantFromCert_WrongTrustDomain_Rejected(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "mallory", nil, "spiffe://evil.example/tenant/tenant-acme/user/mallory")

	_, _, err := a.TenantFromCert(cert)
	if err == nil {
		t.Fatal("expected rejection of foreign trust domain (fail-closed)")
	}
}

// A SPIFFE-less certificate with no OU yields an empty tenant (no fabrication),
// leaving the higher layer to run HRD or fail closed.
func TestTenantFromCert_NoSPIFFE_NoOU(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "browser-user", nil, "")

	tenantID, fromSPIFFE, err := a.TenantFromCert(cert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fromSPIFFE || tenantID != "" {
		t.Fatalf("expected empty tenant, got %q (spiffe=%v)", tenantID, fromSPIFFE)
	}
}

// Legacy OU fallback still works for SPIFFE-less certs.
func TestTenantFromCert_OUFallback(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "legacy", []string{"legacy-tenant"}, "")

	tenantID, fromSPIFFE, err := a.TenantFromCert(cert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fromSPIFFE {
		t.Fatal("OU fallback must not report fromSPIFFE")
	}
	if tenantID != "legacy-tenant" {
		t.Fatalf("tenant = %q, want legacy-tenant", tenantID)
	}
}

// The system bootstrap cert carries tenant/system and resolves to the system
// tenant so the control plane works before tenant barriers unseal.
func TestTenantFromCert_SystemBootstrap(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "security-officer", nil, "spiffe://example.com/tenant/system/user/security-officer")

	tenantID, fromSPIFFE, err := a.TenantFromCert(cert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromSPIFFE || tenantID != "system" {
		t.Fatalf("system bootstrap cert: tenant=%q spiffe=%v", tenantID, fromSPIFFE)
	}
}

// Authenticate end-to-end derives the SPIFFE tenant onto the Identity.
func TestAuthenticate_SPIFFETenant(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "alice", nil, "spiffe://example.com/tenant/tenant-acme/user/alice")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})

	id, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if id.TenantID != "tenant-acme" {
		t.Fatalf("tenant = %q", id.TenantID)
	}
	if id.Metadata["tenant_source"] != "spiffe" {
		t.Fatalf("tenant_source = %q", id.Metadata["tenant_source"])
	}
}

// Authenticate fails closed when the peer cert carries a foreign trust domain.
func TestAuthenticate_ForeignTrustDomain_FailsClosed(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "mallory", nil, "spiffe://evil.example/tenant/tenant-acme/user/mallory")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})

	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected fail-closed on foreign trust domain")
	}
}

// Sanity check that the verifier package agrees on the canonical string form.
func TestSPIFFEStringRoundTrip(t *testing.T) {
	id := spiffe.ID{TrustDomain: "example.com", TenantID: "t1", Kind: spiffe.KindUser, Name: "bob"}
	if got, want := id.String(), "spiffe://example.com/tenant/t1/user/bob"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
