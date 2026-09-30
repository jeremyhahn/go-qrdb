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

package spiffe

import (
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func certURIs(t *testing.T, raw ...string) *x509.Certificate {
	t.Helper()
	c := &x509.Certificate{}
	for _, r := range raw {
		u, err := url.Parse(r)
		if err != nil {
			t.Fatalf("parse %q: %v", r, err)
		}
		c.URIs = append(c.URIs, u)
	}
	return c
}

func TestTenantFromCert_DistinctTenantsAreAmbiguous(t *testing.T) {
	c := certURIs(t,
		"spiffe://example.com/tenant/acme/user/a",
		"https://example.com/not-spiffe",
		"spiffe://example.com/tenant/globex/user/a",
		"spiffe://example.com/tenant/acme/svc/b")
	for _, pin := range []string{"example.com", ""} {
		_, err := TenantFromCert(c, pin)
		var amb *AmbiguousTenantError
		if !errors.As(err, &amb) {
			t.Fatalf("pin %q: err = %v, want *AmbiguousTenantError", pin, err)
		}
		if len(amb.TenantIDs) != 2 || amb.TenantIDs[0] != "acme" || amb.TenantIDs[1] != "globex" {
			t.Fatalf("TenantIDs = %v, want [acme globex] in SAN order", amb.TenantIDs)
		}
		if msg := amb.Error(); !strings.Contains(msg, "acme") || !strings.Contains(msg, "globex") {
			t.Fatalf("Error() = %q, want both tenants named", msg)
		}
	}
}

func TestTenantFromCert_SameTenantTwiceIsOneTenant(t *testing.T) {
	c := certURIs(t, "spiffe://example.com/tenant/acme/user/a", "spiffe://example.com/tenant/acme/svc/b")
	id, err := TenantFromCert(c, "example.com")
	if err != nil || id.TenantID != "acme" || id.Name != "a" {
		t.Fatalf("got (%+v, %v), want the first acme SAN", id, err)
	}
}

func TestTenantFromCert_AnyForeignSPIFFESANUnderPinFailsClosed(t *testing.T) {
	for name, uris := range map[string][]string{
		"foreign tenant SAN last":      {"spiffe://example.com/tenant/acme/user/a", "spiffe://evil.example/tenant/acme/user/a"},
		"foreign non-tenant SAN last":  {"spiffe://example.com/tenant/acme/user/a", "spiffe://evil.example/workload/x"},
		"foreign non-tenant SAN first": {"spiffe://evil.example/workload/x", "spiffe://example.com/tenant/acme/user/a"},
		"foreign non-tenant SAN alone": {"spiffe://evil.example/workload/x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := TenantFromCert(certURIs(t, uris...), "example.com"); !errors.Is(err, ErrTrustDomainMismatch) {
				t.Fatalf("err = %v, want ErrTrustDomainMismatch", err)
			}
		})
	}
}

func TestTenantFromCert_UnpinnedIgnoresTrustDomain(t *testing.T) {
	c := certURIs(t, "spiffe://evil.example/workload/x", "spiffe://other.example/tenant/acme/user/a")
	id, err := TenantFromCert(c, "")
	if err != nil || id.TenantID != "acme" {
		t.Fatalf("got (%+v, %v), want acme with no pin", id, err)
	}
}
