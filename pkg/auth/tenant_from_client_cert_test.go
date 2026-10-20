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
// certificate, the free function and the method agree exactly. The cluster
// trust domain is example.com; a tenant is its own trust domain.
func TestTenantFromClientCert_IsTheAuthenticatorsRule(t *testing.T) {
	var mismatch *TenantNameMismatchError
	cases := []struct {
		name   string
		ou     []string
		uri    string
		tenant string
		cause  func(error) bool
	}{
		{"OU and trust domain present and equal", []string{"acme"}, "spiffe://acme/user/alice", "acme", nil},
		{"service principal", []string{"acme"}, "spiffe://acme/svc/billing", "acme", nil},
		{"system tenant named in both", []string{"system"}, "spiffe://system/user/alice", "system", nil},
		{"OU missing under the cluster trust domain", nil, "spiffe://example.com/user/alice", "",
			func(err error) bool { return errors.Is(err, ErrNoTenantOU) }},
		{"OU missing under a foreign trust domain", nil, "spiffe://acme/user/alice", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrTrustDomainMismatch) }},
		{"spiffe SAN missing", []string{"acme"}, "", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrNoTenantSPIFFEID) }},
		{"OU and trust domain disagree", []string{"globex"}, "spiffe://acme/user/alice", "",
			func(err error) bool {
				return errors.As(err, &mismatch) && mismatch.OU == "globex" && mismatch.SPIFFETenant == "acme"
			}},
		{"foreign trust domain fails closed", []string{"acme"}, "spiffe://evil.example/user/m", "",
			func(err error) bool {
				return errors.As(err, &mismatch) && mismatch.OU == "acme" && mismatch.SPIFFETenant == "evil.example"
			}},
		{"tenant is the cluster trust domain", []string{"example.com"}, "spiffe://example.com/user/alice", "",
			func(err error) bool { return errors.Is(err, ErrTenantIsClusterTrustDomain) }},
		{"neither", nil, "", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrNoTenantSPIFFEID) }},
		{"old grammar fails closed", []string{"acme"}, "spiffe://example.com/tenant/acme/user/alice", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrPathInvalid) }},
		{"malformed path fails closed", []string{"acme"}, "spiffe://acme/user//x", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrPathInvalid) }},
		{"query fails closed", []string{"acme"}, "spiffe://acme/user/alice?x=1", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrURIInvalid) }},
		{"uppercase trust domain fails closed", []string{"Acme"}, "spiffe://Acme/user/alice", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrTrustDomainInvalid) }},
		{"unknown kind fails closed", []string{"acme"}, "spiffe://acme/robot/alice", "",
			func(err error) bool { return errors.Is(err, spiffe.ErrKindInvalid) }},
	}
	a := NewMTLSAuthenticatorWithTrustDomain("example.com")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := spiffeCert(t, "alice", tc.ou, tc.uri)
			tenantID, err := TenantFromClientCert(c, "example.com")
			if tc.cause != nil {
				var cte *CertificateTenantError
				if !errors.Is(err, ErrInvalidCertificate) || !errors.As(err, &cte) || !tc.cause(err) {
					t.Fatalf("err = %v, want a *CertificateTenantError with the case's cause", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tenantID != tc.tenant {
				t.Fatalf("tenant = %q, want %q", tenantID, tc.tenant)
			}
			mt, merr := a.TenantFromCert(c)
			if mt != tenantID || fmt.Sprint(merr) != fmt.Sprint(err) {
				t.Fatalf("authenticator answered (%q, %v), the exported rule (%q, %v)", mt, merr, tenantID, err)
			}
		})
	}
}

// TestTenantFromClientCert_MultipleTrustDomainsFailClosed: a certificate
// naming two tenants names no tenant. Answering with whichever SAN happens to
// come first would let the issuer's SAN order choose the tenant.
func TestTenantFromClientCert_MultipleTrustDomainsFailClosed(t *testing.T) {
	for name, uris := range map[string][]string{
		"other tenant second": {"spiffe://acme/user/alice", "spiffe://globex/user/alice"},
		"other tenant first":  {"spiffe://globex/user/alice", "spiffe://acme/user/alice"},
		"foreign domain":      {"spiffe://acme/user/alice", "spiffe://evil.example/user/alice"},
	} {
		t.Run(name, func(t *testing.T) {
			c := spiffeCertURIs(t, "alice", []string{"acme"}, uris...)
			for _, pin := range []string{"example.com", ""} {
				tenantID, err := TenantFromClientCert(c, pin)
				if !errors.Is(err, ErrInvalidCertificate) || tenantID != "" {
					t.Fatalf("pin %q: got (%q, %v), want a fail-closed refusal", pin, tenantID, err)
				}
				var amb *spiffe.AmbiguousTenantError
				if !errors.As(err, &amb) || len(amb.TrustDomains) != 2 {
					t.Fatalf("pin %q: err = %v, want *spiffe.AmbiguousTenantError naming both", pin, err)
				}
			}
		})
	}
}

// TestTenantFromClientCert_RepeatedSameTenantIsNotAmbiguous: two SANs under
// the same tenant's trust domain name one tenant, and a node identity beside
// them is not a workload SAN.
func TestTenantFromClientCert_RepeatedSameTenantIsNotAmbiguous(t *testing.T) {
	c := spiffeCertURIs(t, "alice", []string{"acme"},
		"spiffe://acme/user/alice",
		"spiffe://example.com/node/n1",
		"spiffe://acme/svc/agent")
	tenantID, err := TenantFromClientCert(c, "example.com")
	if err != nil || tenantID != "acme" {
		t.Fatalf("got (%q, %v), want (acme, nil)", tenantID, err)
	}
}

func TestCertificateTenantError_MatchesInvalidCertificateAndCause(t *testing.T) {
	cause := &spiffe.AmbiguousTenantError{TrustDomains: []string{"a", "b"}}
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
		"spiffe://acme/user/alice",
		"spiffe://globex/user/alice")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{c}}})
	if _, err := NewMTLSAuthenticatorWithTrustDomain("example.com").Authenticate(ctx); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("Authenticate = %v, want ErrInvalidCertificate", err)
	}
}

func TestTenantFromClientCert_NilCertificateFailsClosed(t *testing.T) {
	if _, err := TenantFromClientCert(nil, ""); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("err = %v, want ErrInvalidCertificate", err)
	}
}

// TestTenantFromClientCert_NoClusterTrustDomain: with no cluster trust domain
// the cluster checks are skipped, and the OU must still name the trust domain.
func TestTenantFromClientCert_NoClusterTrustDomain(t *testing.T) {
	c := spiffeCert(t, "alice", []string{"acme"}, "spiffe://acme/user/alice")
	if tenantID, err := TenantFromClientCert(c, ""); err != nil || tenantID != "acme" {
		t.Fatalf("got (%q, %v), want (acme, nil)", tenantID, err)
	}
	if _, err := TenantFromClientCert(spiffeCert(t, "alice", nil, "spiffe://acme/user/alice"), ""); !errors.Is(err, ErrNoTenantOU) {
		t.Fatalf("no OU: err = %v, want ErrNoTenantOU", err)
	}
	var mismatch *TenantNameMismatchError
	if _, err := TenantFromClientCert(spiffeCert(t, "alice", []string{"globex"}, "spiffe://acme/user/alice"), ""); !errors.As(err, &mismatch) {
		t.Fatalf("disagreeing OU: err = %v, want *TenantNameMismatchError", err)
	}
}

// TestTenantPrincipalURIs_IsAcceptedByTheRule pins the issuers' helper to the
// rule: a certificate carrying the helper's SAN beside the tenant as its first
// OU resolves to that tenant under the cluster trust domain, for both kinds.
func TestTenantPrincipalURIs_IsAcceptedByTheRule(t *testing.T) {
	for _, tc := range []struct {
		kind, name, want string
	}{
		{spiffe.KindUser, "alice", "spiffe://acme/user/alice"},
		{spiffe.KindService, "billing", "spiffe://acme/svc/billing"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			uris, err := TenantPrincipalURIs("td.example", "acme", tc.kind, tc.name)
			if err != nil {
				t.Fatalf("TenantPrincipalURIs: %v", err)
			}
			if len(uris) != 1 || uris[0].String() != tc.want {
				t.Fatalf("TenantPrincipalURIs = %v, want [%s]", uris, tc.want)
			}
			c := &x509.Certificate{URIs: uris}
			c.Subject.CommonName = tc.name
			c.Subject.OrganizationalUnit = []string{"acme"}
			tenantID, err := TenantFromClientCert(c, "td.example")
			if err != nil || tenantID != "acme" {
				t.Fatalf("TenantFromClientCert = (%q, %v), want (acme, nil)", tenantID, err)
			}
		})
	}
}

// TestTenantPrincipalURIs_RefusesWhatTheRuleCannotRead pins that the helper
// returns no SAN, and a *spiffe.IDError naming the cause, for every identity
// the rule could not read back or would refuse -- so no issuer signs a
// certificate the rule would refuse.
func TestTenantPrincipalURIs_RefusesWhatTheRuleCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		trustDomain, tenant, kind string
		principal                 string
		want                      error
	}{
		{"no tenant", "td.example", "", spiffe.KindUser, "alice", spiffe.ErrTrustDomainEmpty},
		{"tenant is the cluster trust domain", "td.example", "td.example", spiffe.KindUser, "alice", ErrTenantIsClusterTrustDomain},
		{"tenant not a trust domain name", "td.example", "Acme", spiffe.KindUser, "alice", spiffe.ErrTrustDomainInvalid},
		{"tenant with a path separator", "td.example", "ac/me", spiffe.KindUser, "alice", spiffe.ErrTrustDomainInvalid},
		{"unknown kind", "td.example", "acme", "node", "alice", spiffe.ErrKindInvalid},
		{"no name", "td.example", "acme", spiffe.KindService, "", spiffe.ErrNameEmpty},
		{"name not a path segment", "td.example", "acme", spiffe.KindUser, "alice@acme.example", spiffe.ErrPathInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uris, err := TenantPrincipalURIs(tc.trustDomain, tc.tenant, tc.kind, tc.principal)
			var idErr *spiffe.IDError
			if uris != nil || !errors.As(err, &idErr) || !errors.Is(err, tc.want) {
				t.Fatalf("TenantPrincipalURIs = (%v, %v), want (nil, *spiffe.IDError wrapping %v)", uris, err, tc.want)
			}
		})
	}
}

func TestTenantPrincipalURIs_RequiresAClusterTrustDomain(t *testing.T) {
	// Without the cluster trust domain the issuer cannot tell a tenant from
	// the cluster, so it signs nothing.
	for td, want := range map[string]error{
		"":           spiffe.ErrTrustDomainEmpty,
		"TD.example": spiffe.ErrTrustDomainInvalid,
	} {
		uris, err := TenantPrincipalURIs(td, "acme", spiffe.KindUser, "alice")
		var tdErr *spiffe.TrustDomainError
		if uris != nil || !errors.As(err, &tdErr) || tdErr.Value != td || !errors.Is(err, want) {
			t.Errorf("TenantPrincipalURIs(%q, ...) = (%v, %v), want (nil, *spiffe.TrustDomainError wrapping %v)", td, uris, err, want)
		}
	}
}
