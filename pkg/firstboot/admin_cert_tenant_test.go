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

package firstboot

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/url"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// presentedTrustDomain is the SPIFFE trust domain the Path C tests pin.
const presentedTrustDomain = "example.com"

// presentedIdentity returns the subject and URI SANs of an admin
// certificate whose first OU is ouTenant and whose tenant-scoped SPIFFE ID
// names sanTenant in trustDomain. An empty sanTenant yields no URI SAN.
func presentedIdentity(t *testing.T, ouTenant, sanTenant, trustDomain string) (pkix.Name, []*url.URL) {
	t.Helper()
	return presentedIdentityOf(t, adminUsername, ouTenant, sanTenant, trustDomain)
}

// presentedIdentityOf is presentedIdentity for the user name.
func presentedIdentityOf(t *testing.T, name, ouTenant, sanTenant, trustDomain string) (pkix.Name, []*url.URL) {
	t.Helper()
	subject := pkix.Name{CommonName: name, OrganizationalUnit: []string{ouTenant}}
	if sanTenant == "" {
		return subject, nil
	}
	uris, err := auth.TenantPrincipalURIs(trustDomain, sanTenant, spiffe.KindUser, name)
	if err != nil {
		t.Fatalf("TenantPrincipalURIs: %v", err)
	}
	return subject, uris
}

// initializePresented runs first boot over Path C with the presented
// certificate, pinning presentedTrustDomain.
func initializePresented(t *testing.T, ca *testLeafCAForMultiNode, certPEM []byte, binder *fakeBinder) (*Result, error) {
	t.Helper()
	return initializePresentedUnder(t, ca, certPEM, binder, presentedTrustDomain)
}

// initializePresentedUnder is initializePresented on a cluster whose SPIFFE
// trust domain is trustDomain.
func initializePresentedUnder(t *testing.T, ca *testLeafCAForMultiNode, certPEM []byte, binder *fakeBinder, trustDomain string) (*Result, error) {
	t.Helper()
	return Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminCertPEM:  certPEM,
		AdminCertPool: ca.pool,
		TrustDomain:   trustDomain,
		Binder:        binder,
		Logger:        discardLogger(),
	})
}

// TestInitialize_PathC_RefusesSystemOUWithoutSPIFFEID covers go-qrdb#777: a
// chain-trusted OU=system certificate with no SPIFFE ID can never
// authenticate, so first boot refuses it and binds nothing.
func TestInitialize_PathC_RefusesSystemOUWithoutSPIFFEID(t *testing.T) {
	ca := newTestLeafCA(t)
	subject, uris := presentedIdentity(t, tenantid.System, "", presentedTrustDomain)
	binder := &fakeBinder{}

	_, err := initializePresented(t, ca, ca.issueLeafAs(t, 201, subject, uris), binder)

	var rejected *AdminCertRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *AdminCertRejectedError", err)
	}
	var tenantErr *auth.CertificateTenantError
	if !errors.As(err, &tenantErr) {
		t.Fatalf("err = %v, want the authenticator's *auth.CertificateTenantError", err)
	}
	if !errors.Is(err, spiffe.ErrNoTenantSPIFFEID) || !errors.Is(err, auth.ErrInvalidCertificate) {
		t.Fatalf("err = %v, want ErrNoTenantSPIFFEID and ErrInvalidCertificate", err)
	}
	if binder.calls != 0 {
		t.Fatalf("binder called %d times; a refused certificate must not be bound", binder.calls)
	}
}

// TestInitialize_PathC_RefusesSPIFFEIDDisagreeingWithOU: OU=system with a
// SPIFFE ID naming another tenant is refused by the same tenant rule.
func TestInitialize_PathC_RefusesSPIFFEIDDisagreeingWithOU(t *testing.T) {
	ca := newTestLeafCA(t)
	subject, uris := presentedIdentity(t, tenantid.System, "acme", presentedTrustDomain)
	binder := &fakeBinder{}

	_, err := initializePresented(t, ca, ca.issueLeafAs(t, 202, subject, uris), binder)

	var rejected *AdminCertRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *AdminCertRejectedError", err)
	}
	var mismatch *auth.TenantNameMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *auth.TenantNameMismatchError", err)
	}
	if binder.calls != 0 {
		t.Fatalf("binder called %d times; a refused certificate must not be bound", binder.calls)
	}
}

// TestInitialize_PathC_RefusesForeignTrustDomain: a SPIFFE ID outside the
// pinned trust domain is refused, as the pinned authenticator refuses it.
func TestInitialize_PathC_RefusesTenantNamedAsClusterTrustDomain(t *testing.T) {
	ca := newTestLeafCA(t)
	// A tenant is the trust domain of its principal, so a cluster whose own
	// trust domain is the system tenant's name cannot tell the two apart.
	u, err := spiffe.ID{TrustDomain: tenantid.System, Kind: spiffe.KindUser, Name: adminUsername}.URI()
	if err != nil {
		t.Fatalf("URI: %v", err)
	}
	subject := pkix.Name{CommonName: adminUsername, OrganizationalUnit: []string{tenantid.System}}
	binder := &fakeBinder{}

	_, err = initializePresentedUnder(t, ca, ca.issueLeafAs(t, 203, subject, []*url.URL{u}), binder, tenantid.System)

	var tenantErr *auth.CertificateTenantError
	if !errors.As(err, &tenantErr) {
		t.Fatalf("err = %v, want *auth.CertificateTenantError", err)
	}
	if !errors.Is(err, auth.ErrTenantIsClusterTrustDomain) {
		t.Fatalf("err = %v, want auth.ErrTenantIsClusterTrustDomain", err)
	}
	if binder.calls != 0 {
		t.Fatalf("binder called %d times; a refused certificate must not be bound", binder.calls)
	}
}

// TestInitialize_PathC_RefusesNonSystemTenant: a certificate that agrees
// with itself but names a tenant other than the admin's is refused.
func TestInitialize_PathC_RefusesNonSystemTenant(t *testing.T) {
	ca := newTestLeafCA(t)
	subject, uris := presentedIdentity(t, "acme", "acme", presentedTrustDomain)
	binder := &fakeBinder{}

	_, err := initializePresented(t, ca, ca.issueLeafAs(t, 204, subject, uris), binder)

	var rejected *AdminCertRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *AdminCertRejectedError", err)
	}
	var wrongTenant *AdminCertTenantError
	if !errors.As(err, &wrongTenant) {
		t.Fatalf("err = %v, want *AdminCertTenantError", err)
	}
	if wrongTenant.CertTenant != "acme" || wrongTenant.AdminTenant != tenantid.System {
		t.Fatalf("AdminCertTenantError = %+v", wrongTenant)
	}
	if binder.calls != 0 {
		t.Fatalf("binder called %d times; a refused certificate must not be bound", binder.calls)
	}
}

// TestInitialize_PathC_AdoptedCertAuthenticates: a certificate that passes
// the tenant rule is bound, and the pinned mTLS authenticator accepts it.
func TestInitialize_PathC_AdoptedCertAuthenticates(t *testing.T) {
	ca := newTestLeafCA(t)
	subject, uris := presentedIdentity(t, tenantid.System, tenantid.System, presentedTrustDomain)
	binder := &fakeBinder{}

	result, err := initializePresented(t, ca, ca.issueLeafAs(t, 205, subject, uris), binder)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if binder.calls != 1 || binder.thumbprint != result.AdminCertThumbprint {
		t.Fatalf("adopted cert was not bound: calls=%d bound=%q result=%q",
			binder.calls, binder.thumbprint, result.AdminCertThumbprint)
	}

	block, _ := pem.Decode(result.AdminCertPEM)
	if block == nil {
		t.Fatal("adopted certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	})
	identity, err := auth.NewMTLSAuthenticatorWithTrustDomain(presentedTrustDomain).Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if identity.TenantID != tenantid.System {
		t.Fatalf("authenticated tenant = %q, want %q", identity.TenantID, tenantid.System)
	}
}
