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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// TestTenantFromClientCert_IsTheAuthenticatorsRule pins the exported rule to
// the one the authenticator applies (go-qrdb#575): for every shape of
// certificate, the free function and the method agree exactly.
func TestTenantFromClientCert_IsTheAuthenticatorsRule(t *testing.T) {
	cases := []struct {
		name   string
		ou     []string
		uri    string
		tenant string
		spiffe bool
		fails  bool
	}{
		{"spiffe under the pinned domain", nil, "spiffe://example.com/tenant/acme/user/alice", "acme", true, false},
		{"spiffe wins over the OU", []string{"globex"}, "spiffe://example.com/tenant/acme/user/alice", "acme", true, false},
		{"foreign trust domain fails closed", []string{"acme"}, "spiffe://evil.example/tenant/acme/user/m", "", false, true},
		{"malformed spiffe fails closed", []string{"acme"}, "spiffe://example.com/tenant//user/x", "", false, true},
		{"OU fallback", []string{"acme"}, "", "acme", false, false},
		{"neither is no tenant", nil, "", "", false, false},
	}
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := spiffeCert(t, "alice", tc.ou, tc.uri)
			tenantID, fromSPIFFE, err := TenantFromClientCert(c, "example.com")
			if tc.fails {
				if !errors.Is(err, ErrInvalidCertificate) {
					t.Fatalf("err = %v, want ErrInvalidCertificate", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tenantID != tc.tenant || fromSPIFFE != tc.spiffe {
				t.Fatalf("got (%q, %v), want (%q, %v)", tenantID, fromSPIFFE, tc.tenant, tc.spiffe)
			}
			mt, ms, merr := a.TenantFromCert(c)
			if mt != tenantID || ms != fromSPIFFE || errors.Is(merr, ErrInvalidCertificate) != errors.Is(err, ErrInvalidCertificate) || fmt.Sprint(merr) != fmt.Sprint(err) {
				t.Fatalf("authenticator answered (%q, %v, %v), the exported rule (%q, %v, %v)",
					mt, ms, merr, tenantID, fromSPIFFE, err)
			}
		})
	}
}

// TestTenantFromClientCert_MultipleTenantSANsFailClosed: a certificate naming
// two tenants names no tenant. Answering with whichever SAN happens to come
// first would let the issuer's SAN order choose the tenant.
func TestTenantFromClientCert_MultipleTenantSANsFailClosed(t *testing.T) {
	c := spiffeCertURIs(t, "alice", nil,
		"spiffe://example.com/tenant/acme/user/alice",
		"spiffe://example.com/tenant/globex/user/alice")
	for _, pin := range []string{"example.com", ""} {
		tenantID, _, err := TenantFromClientCert(c, pin)
		if !errors.Is(err, ErrInvalidCertificate) || tenantID != "" {
			t.Fatalf("pin %q: got (%q, %v), want a fail-closed refusal", pin, tenantID, err)
		}
		var amb *spiffe.AmbiguousTenantError
		if !errors.As(err, &amb) || len(amb.TenantIDs) != 2 {
			t.Fatalf("pin %q: err = %v, want *spiffe.AmbiguousTenantError naming both tenants", pin, err)
		}
	}
}

// TestTenantFromClientCert_RepeatedSameTenantIsNotAmbiguous: two SANs naming
// the same tenant under the pinned domain name one tenant.
func TestTenantFromClientCert_RepeatedSameTenantIsNotAmbiguous(t *testing.T) {
	c := spiffeCertURIs(t, "alice", nil,
		"spiffe://example.com/tenant/acme/user/alice",
		"spiffe://example.com/tenant/acme/svc/agent")
	tenantID, fromSPIFFE, err := TenantFromClientCert(c, "example.com")
	if err != nil || tenantID != "acme" || !fromSPIFFE {
		t.Fatalf("got (%q, %v, %v), want (acme, true, nil)", tenantID, fromSPIFFE, err)
	}
}

// TestTenantFromClientCert_AnySANOutsidePinnedDomainFailsClosed: under a pin,
// a SPIFFE SAN from another trust domain refuses the certificate wherever it
// appears -- not only when it is the first one, and not only when it is a
// tenant path.
func TestTenantFromClientCert_AnySANOutsidePinnedDomainFailsClosed(t *testing.T) {
	for name, uris := range map[string][]string{
		"foreign tenant SAN after a good one":     {"spiffe://example.com/tenant/acme/user/alice", "spiffe://evil.example/tenant/acme/user/alice"},
		"foreign non-tenant SAN after a good one": {"spiffe://example.com/tenant/acme/user/alice", "spiffe://evil.example/workload/x"},
		"foreign non-tenant SAN before one":       {"spiffe://evil.example/workload/x", "spiffe://example.com/tenant/acme/user/alice"},
	} {
		t.Run(name, func(t *testing.T) {
			c := spiffeCertURIs(t, "alice", []string{"acme"}, uris...)
			tenantID, _, err := TenantFromClientCert(c, "example.com")
			if !errors.Is(err, ErrInvalidCertificate) || tenantID != "" {
				t.Fatalf("got (%q, %v), want a fail-closed refusal", tenantID, err)
			}
			if !errors.Is(err, spiffe.ErrTrustDomainMismatch) {
				t.Fatalf("err = %v, want it to carry spiffe.ErrTrustDomainMismatch", err)
			}
		})
	}
}

func TestCertificateTenantError_MatchesInvalidCertificateAndCause(t *testing.T) {
	cause := &spiffe.AmbiguousTenantError{TenantIDs: []string{"a", "b"}}
	err := error(&CertificateTenantError{Err: cause})
	var ae *AuthError
	if !errors.As(err, &ae) || ae != ErrInvalidCertificate {
		t.Fatalf("errors.As(*AuthError) = %v, want ErrInvalidCertificate (transport classification)", ae)
	}
	var amb *spiffe.AmbiguousTenantError
	if !errors.As(err, &amb) || amb != cause {
		t.Fatal("the spiffe cause is not reachable")
	}
	if msg := err.Error(); msg != ErrInvalidCertificate.Error()+": "+cause.Error() {
		t.Fatalf("Error() = %q", msg)
	}
}

// TestAuthenticate_AmbiguousTenantCertIsRefused: the authenticator itself
// refuses the certificate, so a login cannot land in either tenant.
func TestAuthenticate_AmbiguousTenantCertIsRefused(t *testing.T) {
	c := spiffeCertURIs(t, "alice", nil,
		"spiffe://example.com/tenant/acme/user/alice",
		"spiffe://example.com/tenant/globex/user/alice")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{c}}})
	if _, err := NewMTLSAuthenticatorWithTrustDomain("example.com").Authenticate(ctx); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("Authenticate = %v, want ErrInvalidCertificate", err)
	}
}

func TestTenantFromClientCert_NilCertificateFailsClosed(t *testing.T) {
	if _, _, err := TenantFromClientCert(nil, ""); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("err = %v, want ErrInvalidCertificate", err)
	}
}

// TestTenantFromClientCert_UnpinnedAcceptsAnyTrustDomain: with no pin the
// SPIFFE SAN is honoured whatever its trust domain, as the unpinned
// authenticator does.
func TestTenantFromClientCert_UnpinnedAcceptsAnyTrustDomain(t *testing.T) {
	c := spiffeCert(t, "alice", nil, "spiffe://other.example/tenant/acme/user/alice")
	tenantID, fromSPIFFE, err := TenantFromClientCert(c, "")
	if err != nil || tenantID != "acme" || !fromSPIFFE {
		t.Fatalf("got (%q, %v, %v), want (acme, true, nil)", tenantID, fromSPIFFE, err)
	}
}
