// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// No build constraint, like certs_tenant_scope_test.go whose fixture it uses.

package grpc

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
)

// certFromPEM decodes the one certificate in pemBytes.
func certFromPEM(t *testing.T, pemBytes []byte) *x509.Certificate {
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
func (f *certScopeGRPCFixture) tenantCA(t *testing.T, tenantID string) *x509.Certificate {
	t.Helper()
	ctx := context.Background()
	pems, err := f.prov.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	for _, p := range pems {
		ca := certFromPEM(t, p)
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

// TestGRPCCertsDelegate_IssuedByTheCallersOwnTenantCA: against the real CA,
// a delegation names the caller's tenant and is signed by that tenant's CA,
// never another tenant's.
func TestGRPCCertsDelegate_IssuedByTheCallersOwnTenantCA(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	ctx := context.Background()
	// Tenant B's CA exists too, so a wrong-issuer signature would verify
	// against something.
	if _, err := certtest.Generate(ctx, f.prov, &certtest.Request{Username: "b-user", TenantID: certScopeTenantB}); err != nil {
		t.Fatalf("issue in tenant B: %v", err)
	}
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	resp, err := f.svc.IssueDelegation(f.as(certScopeTenantA, "a-admin"), &pb.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CsrPem: string(csrPEM), Ttl: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if resp.GetTenantId() != certScopeTenantA {
		t.Fatalf("tenant = %q, want %q", resp.GetTenantId(), certScopeTenantA)
	}

	leaf := certFromPEM(t, []byte(resp.GetCertPem()))
	g, found, err := delegation.FromCertificate(leaf)
	if !found || err != nil || g.Tenant != certScopeTenantA || g.Principal != "billing" || !slices.Equal(g.Scope, []string{"kv:get"}) {
		t.Fatalf("FromCertificate = %+v, %v, %v", g, found, err)
	}
	for tenantID, wantSigned := range map[string]bool{certScopeTenantA: true, certScopeTenantB: false} {
		if signed := leaf.CheckSignatureFrom(f.tenantCA(t, tenantID)) == nil; signed != wantSigned {
			t.Fatalf("signed by %s's CA = %v, want %v", tenantID, signed, wantSigned)
		}
	}
}

// TestGRPCCertsDelegate_BuiltinRolePrincipalIsRefused: a delegation cannot
// name a built-in role as its principal.
func TestGRPCCertsDelegate_BuiltinRolePrincipalIsRefused(t *testing.T) {
	f := newCertScopeGRPCFixture(t)
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}

	_, err = f.svc.IssueDelegation(f.as(certScopeTenantA, "a-admin"), &pb.IssueDelegationRequest{
		Principal: "admin", Scope: []string{"kv:get"}, CsrPem: string(csrPEM),
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %s (%v), want InvalidArgument", got, err)
	}
}
