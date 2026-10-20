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

func TestWorkloadFromCert_DistinctTrustDomainsAreAmbiguous(t *testing.T) {
	c := certURIs(t,
		"spiffe://acme/user/a",
		"https://example.com/not-spiffe",
		"spiffe://globex/user/a",
		"spiffe://acme/svc/b")
	_, err := WorkloadFromCert(c)
	var amb *AmbiguousTenantError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *AmbiguousTenantError", err)
	}
	if len(amb.TrustDomains) != 2 || amb.TrustDomains[0] != "acme" || amb.TrustDomains[1] != "globex" {
		t.Fatalf("TrustDomains = %v, want [acme globex] in SAN order", amb.TrustDomains)
	}
	if msg := amb.Error(); !strings.Contains(msg, "acme") || !strings.Contains(msg, "globex") {
		t.Fatalf("Error() = %q, want both trust domains named", msg)
	}
}

func TestWorkloadFromCert_SameTrustDomainTwiceIsOneTenant(t *testing.T) {
	c := certURIs(t, "spiffe://acme/user/a", "spiffe://acme/svc/b")
	id, err := WorkloadFromCert(c)
	if err != nil || id.TrustDomain != "acme" || id.Name != "a" {
		t.Fatalf("got (%+v, %v), want the first acme SAN", id, err)
	}
}

func TestWorkloadFromCert_OrderIndependent(t *testing.T) {
	for name, uris := range map[string][]string{
		"foreign SAN last":  {"spiffe://acme/user/a", "spiffe://evil.example/user/a"},
		"foreign SAN first": {"spiffe://evil.example/user/a", "spiffe://acme/user/a"},
	} {
		t.Run(name, func(t *testing.T) {
			var amb *AmbiguousTenantError
			if _, err := WorkloadFromCert(certURIs(t, uris...)); !errors.As(err, &amb) {
				t.Fatalf("err = %v, want *AmbiguousTenantError", err)
			}
		})
	}
}
