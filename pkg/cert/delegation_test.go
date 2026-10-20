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

package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// dlgCertGrant is the acme delegation these tests issue.
var dlgCertGrant = delegation.Grant{Tenant: sanTestTenant, Principal: "billing", Scope: []string{"kv:get", "kv:put"}}

// delegationRequest is an acme request for g, with a fresh key.
func delegationRequest(t *testing.T, g delegation.Grant) *IssueRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &IssueRequest{
		Username:   auth.ServicePrincipalSubjectPrefix + g.Principal,
		TenantID:   g.Tenant,
		PublicKey:  &key.PublicKey,
		Delegation: &g,
	}
}

// issueDelegation issues req and returns the certificate parsed.
func issueDelegation(t *testing.T, p *DefaultCertProvider, req *IssueRequest) (*IssuedCert, *x509.Certificate) {
	t.Helper()
	issued, err := p.IssueClientCert(context.Background(), req)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	return issued, parsePEMCert(t, issued.CertPEM)
}

// providerTenantCA is a node's trust in the provider's own per-tenant CAs:
// the chain verifies to the leaf's tenant CA, and that CA is the tenant's
// issuer. It is what pkg/clienttrust holds for the same provider.
type providerTenantCA struct{ p *DefaultCertProvider }

var _ auth.TenantIssuerVerifier = providerTenantCA{}

func (v providerTenantCA) ca(ctx context.Context, tenantID string) (*x509.Certificate, error) {
	_, ca, err := v.p.getCAMaterial(ctx, tenantID)
	return ca, err
}

func (v providerTenantCA) VerifyPeerChain(ctx context.Context, leaf *x509.Certificate, _ []*x509.Certificate) ([]*x509.Certificate, error) {
	ca, err := v.ca(ctx, leaf.Subject.OrganizationalUnit[0])
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	chains, err := delegation.Verifiable(leaf).Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		return nil, err
	}
	return chains[0], nil
}

func (providerTenantCA) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v providerTenantCA) VerifyIssuer(ctx context.Context, tenantID string, chain []*x509.Certificate) error {
	return v.VerifyTenantIssuer(ctx, tenantID, chain[len(chain)-1])
}

func (v providerTenantCA) VerifyTenantIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error {
	ca, err := v.ca(ctx, tenantID)
	if err != nil {
		return err
	}
	if !issuer.Equal(ca) {
		return &ValidationError{Field: "issuer", Reason: "not tenant " + tenantID + "'s CA"}
	}
	return nil
}

type dlgNothingRevoked struct{}

func (dlgNothingRevoked) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	return false, nil
}

// TestIssueClientCert_Delegation_CarriesTheGrant: the certificate carries the
// grant as a critical extension that reads back as the grant, names the
// principal as a service in the tenant, and its record keeps the grant.
func TestIssueClientCert_Delegation_CarriesTheGrant(t *testing.T) {
	p, kv, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	issued, leaf := issueDelegation(t, p, delegationRequest(t, dlgCertGrant))

	var critical bool
	for _, e := range leaf.Extensions {
		if e.Id.Equal(delegation.OID) {
			critical = e.Critical
		}
	}
	if !critical {
		t.Fatal("the delegation extension is missing or not critical; an unaware verifier would accept the certificate")
	}
	g, ok, err := delegation.FromCertificate(leaf)
	if err != nil || !ok || g.Tenant != dlgCertGrant.Tenant || g.Principal != dlgCertGrant.Principal || !slices.Equal(g.Scope, dlgCertGrant.Scope) {
		t.Fatalf("FromCertificate = %+v, %v, %v; want %+v", g, ok, err, dlgCertGrant)
	}
	want := "spiffe://" + sanTestTenant + "/svc/billing"
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != want {
		t.Fatalf("URIs = %v, want [%s]", leaf.URIs, want)
	}
	if leaf.Subject.CommonName != auth.ServicePrincipalSubjectPrefix+"billing" {
		t.Fatalf("CN = %q", leaf.Subject.CommonName)
	}

	raw, err := kv.Get(context.Background(), prefixCerts+sanTestTenant+"/"+issued.Serial)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	var rec CertRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Delegation == nil || rec.Delegation.Principal != "billing" || !slices.Equal(rec.Delegation.Scope, dlgCertGrant.Scope) {
		t.Fatalf("record delegation = %+v, want the grant", rec.Delegation)
	}
}

// TestIssueClientCert_Delegation_Lifetime: the default is the delegation's
// own, and no request outlives delegation.MaxLifetime.
func TestIssueClientCert_Delegation_Lifetime(t *testing.T) {
	p, _, _ := initializeProvider(t)
	for name, tc := range map[string]struct {
		ttl, want time.Duration
	}{
		"default":         {0, delegation.DefaultLifetime},
		"shorter":         {10 * time.Minute, 10 * time.Minute},
		"above the limit": {30 * 24 * time.Hour, delegation.MaxLifetime},
	} {
		t.Run(name, func(t *testing.T) {
			req := delegationRequest(t, dlgCertGrant)
			req.TTL = tc.ttl
			_, leaf := issueDelegation(t, p, req)
			if got := leaf.NotAfter.Sub(leaf.NotBefore); got != tc.want {
				t.Fatalf("lifetime = %v, want %v", got, tc.want)
			}
			if err := delegation.CheckLifetime(leaf); err != nil {
				t.Fatalf("CheckLifetime: %v", err)
			}
		})
	}
}

// TestIssueClientCert_Delegation_Refused: a grant the tenant cannot issue is
// refused before anything is signed or stored.
func TestIssueClientCert_Delegation_Refused(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(r *IssueRequest)
		want   any
	}{
		"another tenant's grant": {func(r *IssueRequest) { r.Delegation.Tenant = "globex" }, new(*ValidationError)},
		"the system tenant": {func(r *IssueRequest) {
			r.TenantID, r.Delegation.Tenant = tenantid.System, tenantid.System
		}, new(*ValidationError)},
		"subject not the principal's": {func(r *IssueRequest) { r.Username = "billing" }, new(*ValidationError)},
		"invalid principal":           {func(r *IssueRequest) { r.Delegation.Principal = "bill/ing" }, new(*delegation.GrantError)},
		"empty scope":                 {func(r *IssueRequest) { r.Delegation.Scope = nil }, new(*delegation.GrantError)},
	} {
		t.Run(name, func(t *testing.T) {
			p, kv, _ := initializeProvider(t)
			req := delegationRequest(t, dlgCertGrant)
			g := *req.Delegation
			g.Scope = slices.Clone(g.Scope)
			req.Delegation = &g
			tc.mutate(req)
			before := kvLen(kv)

			issued, err := p.IssueClientCert(context.Background(), req)
			if issued != nil || err == nil || !errors.As(err, tc.want) {
				t.Fatalf("IssueClientCert = %v, %v; want refused with %T", issued, err, tc.want)
			}
			if kvLen(kv) != before {
				t.Fatal("a refused delegation wrote state")
			}
		})
	}
}

func kvLen(kv *mockKVStore) int {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	return len(kv.data)
}

// TestIssueClientCert_Delegation_Authenticates: what the CA issues, the
// authenticator of a node that accepts the principal admits as the delegated
// identity -- scope from the certificate, tenant from its SAN.
func TestIssueClientCert_Delegation_Authenticates(t *testing.T) {
	p, _, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	_, leaf := issueDelegation(t, p, delegationRequest(t, dlgCertGrant))
	a := auth.NewMTLSAuthenticatorWithTrustDomain(sanTestDomain,
		auth.WithIssuerVerifier(providerTenantCA{p}),
		auth.WithRevocationChecker(dlgNothingRevoked{}),
		auth.WithDelegatedPrincipal("billing"),
	)
	id, err := a.Authenticate(auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	g, ok := auth.DelegationOf(id)
	if !ok || g.Principal != "billing" || id.TenantID != sanTestTenant || id.Metadata["tenant_source"] != "spiffe" {
		t.Fatalf("identity = %+v; want billing delegated in acme from its SAN", id)
	}
}

// TestIssueClientCert_NoDelegation_Unchanged: an ordinary certificate carries
// no delegation extension and its record no grant.
func TestIssueClientCert_NoDelegation_Unchanged(t *testing.T) {
	p, _, _ := initializeProvider(t, WithTrustDomain(sanTestDomain))
	leaf := issueParsed(t, p, sanTestTenant, sanTestSubject)
	if _, ok, err := delegation.FromCertificate(leaf); ok || err != nil {
		t.Fatalf("FromCertificate = %v, %v; an ordinary certificate is not a delegation", ok, err)
	}
}
