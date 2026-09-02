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
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
)

// TestMTLSAuthenticate_NoTenantBindingMintsAnEmptyTenant answers the
// reachability caveat #188 carried from go-quicbaas#348 — "which token shapes
// are minted without a tenant claim" — for the authenticator go-qrdb ships.
//
// A client certificate that chains to the trusted CA but carries neither a
// SPIFFE tenant SAN nor a Subject OU authenticates SUCCESSFULLY, with
// TenantID "". No error, no metadata marker: TenantFromCert's ErrNoTenantSPIFFEID
// branch falls back to the OU, and an absent OU returns "", false, nil.
//
// go-qrdb's own issuance cannot produce such a certificate — pkg/cert rejects
// an empty TenantID (validateGenerateRequest) and cmd/qrdbd issues its admin
// cert with OU=system — so this shape only arrives from a CA-signed
// certificate minted outside qrdb's issuance path. That is the deployment
// question the issue said was unestablished; this test establishes what the
// code does when it happens, which is the half that lives here.
//
// The value of pinning it: this "" is the input to the boundary fold in
// auth/middleware.finalizeIdentity. If this authenticator ever starts failing
// closed instead, that is a deliberate policy change and this test is where it
// is recorded.
func TestMTLSAuthenticate_NoTenantBindingMintsAnEmptyTenant(t *testing.T) {
	cert := testCert(t, "orphan-client", nil, []string{"TestOrg"}, nil)

	ctx := WithTLSState(context.Background(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})

	id, err := NewMTLSAuthenticator().Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate returned %v; the tenantless certificate now fails closed, which is a policy change worth reviewing", err)
	}
	if id.Subject != "orphan-client" {
		t.Fatalf("Subject = %q, want %q", id.Subject, "orphan-client")
	}
	if id.TenantID != "" {
		t.Fatalf("TenantID = %q, want \"\": this test exists to pin that the authenticator mints an unbound tenant", id.TenantID)
	}
	if src, ok := id.Metadata["tenant_source"]; ok {
		t.Fatalf("tenant_source = %q, want it absent: no tenant was derived, so none may be attributed", src)
	}
}

// TestTenantFromCert_EmptyOUElementIsStillNoTenant covers the near-miss: an OU
// present but empty (which pkix encodes for OrganizationalUnit: []string{""})
// is len()>0 and so takes the OU branch, yielding "" all the same. Both
// spellings of "no tenant" must reach the boundary as "", so the single fold
// there covers both.
func TestTenantFromCert_EmptyOUElementIsStillNoTenant(t *testing.T) {
	for _, tc := range []struct {
		name string
		ou   []string
	}{
		{"no OU at all", nil},
		{"one empty OU element", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := testCert(t, "orphan-client", tc.ou, nil, nil)
			got, fromSPIFFE, err := NewMTLSAuthenticator().TenantFromCert(cert)
			if err != nil {
				t.Fatalf("TenantFromCert returned %v", err)
			}
			if fromSPIFFE {
				t.Fatal("fromSPIFFE = true for a certificate with no SPIFFE SAN")
			}
			if got != "" {
				t.Fatalf("TenantFromCert = %q, want \"\"", got)
			}
		})
	}
}
