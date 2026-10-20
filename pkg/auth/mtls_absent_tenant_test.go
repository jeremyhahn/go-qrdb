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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// TestMTLSAuthenticate_NoTenantBindingIsRefused pins the policy change #777
// made: a client certificate that chains to the trusted CA but carries
// neither a SPIFFE tenant SAN nor a Subject OU used to authenticate with
// TenantID "" (folded onto the system tenant at the middleware boundary). It
// now fails closed as an invalid certificate, on every transport, because
// the authenticator is the one place each of them derives the tenant.
func TestMTLSAuthenticate_NoTenantBindingIsRefused(t *testing.T) {
	cert := testCert(t, "orphan-client", nil, []string{"TestOrg"}, nil)

	ctx := WithTLSState(context.Background(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})

	id, err := NewMTLSAuthenticator().Authenticate(ctx)
	if !errors.Is(err, ErrInvalidCertificate) || id != nil {
		t.Fatalf("Authenticate = (%v, %v), want a refusal matching ErrInvalidCertificate", id, err)
	}
	var cte *CertificateTenantError
	if !errors.As(err, &cte) || !errors.Is(err, spiffe.ErrNoTenantSPIFFEID) {
		t.Fatalf("err = %v, want a *CertificateTenantError carrying spiffe.ErrNoTenantSPIFFEID", err)
	}
}

// TestTenantFromCert_EmptyOUElementIsRefused covers the near-miss: an OU
// present but empty (which pkix encodes for OrganizationalUnit: []string{""})
// names no tenant, so it never agrees with a SPIFFE ID, whose trust domain
// cannot be empty.
func TestTenantFromCert_EmptyOUElementIsRefused(t *testing.T) {
	cert := spiffeCert(t, "orphan-client", []string{""}, "spiffe://acme/user/orphan-client")
	got, err := NewMTLSAuthenticator().TenantFromCert(cert)
	var mismatch *TenantNameMismatchError
	if !errors.Is(err, ErrInvalidCertificate) || !errors.As(err, &mismatch) || got != "" {
		t.Fatalf("TenantFromCert = (%q, %v), want a *TenantNameMismatchError refusal", got, err)
	}
	if mismatch.OU != "" || mismatch.SPIFFETenant != "acme" {
		t.Fatalf("mismatch = %+v, want OU \"\" against SPIFFE tenant acme", mismatch)
	}
}
