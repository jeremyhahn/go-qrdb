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
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// spiffeCert builds a self-signed leaf carrying the given SPIFFE URI SAN.
func spiffeCert(t *testing.T, cn string, ou []string, spiffeURI string) *x509.Certificate {
	t.Helper()
	if spiffeURI == "" {
		return spiffeCertURIs(t, cn, ou)
	}
	return spiffeCertURIs(t, cn, ou, spiffeURI)
}

// spiffeCertURIs builds a self-signed leaf carrying every given URI SAN.
func spiffeCertURIs(t *testing.T, cn string, ou []string, uris ...string) *x509.Certificate {
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
	for _, raw := range uris {
		u, perr := url.Parse(raw)
		if perr != nil {
			t.Fatalf("parse uri: %v", perr)
		}
		tmpl.URIs = append(tmpl.URIs, u)
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
	cert := spiffeCert(t, "alice", []string{"tenant-acme"}, "spiffe://tenant-acme/user/alice")

	tenantID, err := a.TenantFromCert(cert)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tenantID != "tenant-acme" {
		t.Fatalf("tenant = %q, want tenant-acme", tenantID)
	}
}

// TestTenantFromCert_WrongTrustDomain_Rejected: a SPIFFE SAN under a foreign
// trust domain fails closed, even with an agreeing OU.
func TestTenantFromCert_WrongTrustDomain_Rejected(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "mallory", []string{"tenant-acme"}, "spiffe://evil.example/user/mallory")

	var mismatch *TenantNameMismatchError
	if _, err := a.TenantFromCert(cert); !errors.As(err, &mismatch) || mismatch.SPIFFETenant != "evil.example" {
		t.Fatalf("err = %v, want *TenantNameMismatchError naming evil.example (fail-closed)", err)
	}
}

// TestTenantFromCert_NoSPIFFE_NoOU: a certificate naming no tenant at all is
// refused; it is not folded onto the system tenant.
func TestTenantFromCert_NoSPIFFE_NoOU(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "browser-user", nil, "")

	tenantID, err := a.TenantFromCert(cert)
	if !errors.Is(err, ErrInvalidCertificate) || !errors.Is(err, spiffe.ErrNoTenantSPIFFEID) || tenantID != "" {
		t.Fatalf("got (%q, %v), want a refusal carrying spiffe.ErrNoTenantSPIFFEID", tenantID, err)
	}
}

// TestTenantFromCert_OUOnlyRefused: the OU alone names no tenant; there is no
// OU fallback.
func TestTenantFromCert_OUOnlyRefused(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "legacy", []string{"legacy-tenant"}, "")

	tenantID, err := a.TenantFromCert(cert)
	if !errors.Is(err, ErrInvalidCertificate) || !errors.Is(err, spiffe.ErrNoTenantSPIFFEID) || tenantID != "" {
		t.Fatalf("got (%q, %v), want a refusal carrying spiffe.ErrNoTenantSPIFFEID", tenantID, err)
	}
}

// TestTenantFromCert_SPIFFEOnlyRefused: the SPIFFE ID alone names no tenant;
// there is no SAN-only path.
func TestTenantFromCert_SPIFFEOnlyRefused(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "alice", nil, "spiffe://example.com/user/alice")

	tenantID, err := a.TenantFromCert(cert)
	if !errors.Is(err, ErrInvalidCertificate) || !errors.Is(err, ErrNoTenantOU) || tenantID != "" {
		t.Fatalf("got (%q, %v), want a refusal carrying ErrNoTenantOU", tenantID, err)
	}

	// Naming no tenant, it must be under the cluster trust domain.
	cert = spiffeCert(t, "alice", nil, "spiffe://tenant-acme/user/alice")
	tenantID, err = a.TenantFromCert(cert)
	if !errors.Is(err, ErrInvalidCertificate) || !errors.Is(err, spiffe.ErrTrustDomainMismatch) || tenantID != "" {
		t.Fatalf("got (%q, %v), want a refusal carrying spiffe.ErrTrustDomainMismatch", tenantID, err)
	}
}

// TestTenantFromCert_SystemBootstrap: the system tenant is named in both
// fields like any other.
func TestTenantFromCert_SystemBootstrap(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "security-officer", []string{"system"}, "spiffe://system/user/security-officer")

	tenantID, err := a.TenantFromCert(cert)
	if err != nil || tenantID != "system" {
		t.Fatalf("system bootstrap cert: tenant=%q err=%v", tenantID, err)
	}
}

// TestAuthenticate_SPIFFETenant: Authenticate binds the tenant both fields
// name.
func TestAuthenticate_SPIFFETenant(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "alice", []string{"tenant-acme"}, "spiffe://tenant-acme/user/alice")
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
}

// Authenticate fails closed when the peer cert carries a foreign trust domain.
func TestAuthenticate_ForeignTrustDomain_FailsClosed(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	cert := spiffeCert(t, "mallory", []string{"tenant-acme"}, "spiffe://evil.example/user/mallory")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})

	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected fail-closed on foreign trust domain")
	}
}

// Sanity check that the verifier package agrees on the canonical string form.
func TestSPIFFEStringRoundTrip(t *testing.T) {
	id := spiffe.ID{TrustDomain: "t1", Kind: spiffe.KindUser, Name: "bob"}
	if got, want := id.String(), "spiffe://t1/user/bob"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
