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

package authtest

import (
	"crypto/x509"
	"net/url"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// ClientTrustDomain is the cluster trust domain every client-certificate
// fixture's authenticator is pinned to. No tenant may be named by it: a
// tenant's SPIFFE IDs are under the tenant's own trust domain.
const ClientTrustDomain = "example.com"

// ClientSPIFFEURI returns the SPIFFE ID URI SAN spiffe://<tenant>/user/<name>:
// the SAN auth.TenantFromClientCert requires to agree with a tenant
// certificate's first subject OU.
func ClientSPIFFEURI(t testing.TB, tenant, name string) *url.URL {
	t.Helper()
	uris, err := auth.TenantPrincipalURIs(ClientTrustDomain, tenant, spiffe.KindUser, name)
	if err != nil {
		t.Fatalf("authtest: spiffe id for %q in tenant %q: %v", name, tenant, err)
	}
	return uris[0]
}

// SetClientTenant makes tmpl name tenant the way auth.TenantFromClientCert
// requires: the first subject OU is tenant, and a SPIFFE ID URI SAN
// (ClientSPIFFEURI) names the same tenant and name. It replaces any OU and
// URI SANs tmpl already carried. A test of a refusal (no OU, no SAN, the two
// disagreeing) edits the template after this call, so the certificate
// differs from an accepted one in exactly the field under test.
func SetClientTenant(t testing.TB, tmpl *x509.Certificate, tenant, name string) {
	t.Helper()
	tmpl.Subject.OrganizationalUnit = []string{tenant}
	tmpl.URIs = []*url.URL{ClientSPIFFEURI(t, tenant, name)}
}
