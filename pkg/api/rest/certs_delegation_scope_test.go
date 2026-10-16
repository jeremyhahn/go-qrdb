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

package rest

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
)

// parsePEMCert decodes the one certificate in pemBytes.
func parsePEMCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatalf("no PEM block in %q", pemBytes)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return c
}

// tenantCA returns tenantID's CA certificate from the fixture's provider: the
// one certificate among every tenant's CA that the provider registers as
// tenantID's issuer.
func (f *certScopeFixture) tenantCA(t *testing.T, tenantID string) *x509.Certificate {
	t.Helper()
	ctx := context.Background()
	pems, err := f.prov.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	for _, p := range pems {
		ca := parsePEMCert(t, p)
		ok, err := f.prov.IsTenantIssuer(ctx, tenantID, ca)
		if err != nil {
			t.Fatalf("IsTenantIssuer(%s): %v", tenantID, err)
		}
		if ok {
			return ca
		}
	}
	t.Fatalf("no CA registered for tenant %s", tenantID)
	return nil
}

// TestCertsDelegate_IssuedByTheCallersOwnTenantCA drives a delegation through
// the complete REST chain against the real CA: the certificate names the
// caller's tenant in its critical extension and is signed by that tenant's
// CA, never another tenant's.
func TestCertsDelegate_IssuedByTheCallersOwnTenantCA(t *testing.T) {
	f := newCertScopeFixture(t)
	// Tenant B's CA exists too, so a wrong-issuer signature would verify
	// against something.
	f.issue(t, certScopeTenantB, "b-user")
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/delegate", map[string]any{
		"principal": "billing",
		"scope":     []string{"kv:get"},
		"csrPEM":    string(csrPEM),
		"ttl":       "15m",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
	var resp issuedDelegationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TenantID != certScopeTenantA || resp.Principal != "billing" {
		t.Fatalf("response = %+v", resp)
	}

	leaf := parsePEMCert(t, []byte(resp.CertPEM))
	g, found, err := delegation.FromCertificate(leaf)
	if !found || err != nil {
		t.Fatalf("FromCertificate = %+v, %v, %v", g, found, err)
	}
	if g.Tenant != certScopeTenantA || g.Principal != "billing" || !slices.Equal(g.Scope, []string{"kv:get"}) {
		t.Fatalf("grant = %+v", g)
	}
	if err := leaf.CheckSignatureFrom(f.tenantCA(t, certScopeTenantA)); err != nil {
		t.Fatalf("not signed by tenant A's CA: %v", err)
	}
	if err := leaf.CheckSignatureFrom(f.tenantCA(t, certScopeTenantB)); err == nil {
		t.Fatal("a tenant A delegation verifies against tenant B's CA")
	}
}

// TestCertsDelegate_BuiltinRolePrincipalIsRefused: a delegation cannot name a
// built-in role as its principal, through the real chain.
func TestCertsDelegate_BuiltinRolePrincipalIsRefused(t *testing.T) {
	f := newCertScopeFixture(t)
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	f.caller.as(certScopeTenantA, "a-admin")
	rec := f.do(t, http.MethodPost, "/certs/delegate", map[string]any{
		"principal": "admin",
		"scope":     []string{"kv:get"},
		"csrPEM":    string(csrPEM),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
}
