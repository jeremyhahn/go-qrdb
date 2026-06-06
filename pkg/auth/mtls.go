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

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// Compile-time interface check.
var _ Authenticator = (*MTLSAuthenticator)(nil)

// tlsContextKey is used to extract TLS connection state from context.
type tlsContextKey struct{}

// WithTLSState stores TLS connection state in the context.
func WithTLSState(ctx context.Context, state *tls.ConnectionState) context.Context {
	return context.WithValue(ctx, tlsContextKey{}, state)
}

// TLSStateFromContext retrieves TLS connection state from context.
func TLSStateFromContext(ctx context.Context) *tls.ConnectionState {
	state, _ := ctx.Value(tlsContextKey{}).(*tls.ConnectionState)
	return state
}

// MTLSAuthenticator extracts identity from mTLS peer certificates.
//
// The Subject CN becomes the identity Subject. The TenantID is derived, in
// priority order:
//
//  1. The tenant-scoped SPIFFE URI SAN
//     (spiffe://<trust-domain>/tenant/<tenantID>/<kind>/<name>). When a
//     trust domain is configured it is PINNED: a SPIFFE SAN under a foreign
//     trust domain is rejected (fail-closed) rather than silently ignored.
//  2. The first Subject OU, as a legacy fallback for certificates issued
//     before SPIFFE-SAN tenant binding (and for internal/system paths).
//
// This authenticator never invents a tenant: a SPIFFE SAN with a mismatched
// trust domain is a hard rejection.
type MTLSAuthenticator struct {
	// pinnedTrustDomain, when non-empty, is the operator-configured SPIFFE
	// trust domain. Any SPIFFE tenant SAN under a different trust domain is
	// rejected.
	pinnedTrustDomain string
}

// NewMTLSAuthenticator creates a new mTLS authenticator with no SPIFFE trust
// domain pin. SPIFFE SANs (if present) are still honored for tenant
// derivation, but no trust-domain pinning is enforced. Use
// NewMTLSAuthenticatorWithTrustDomain to pin a trust domain (recommended).
func NewMTLSAuthenticator() *MTLSAuthenticator {
	return &MTLSAuthenticator{}
}

// NewMTLSAuthenticatorWithTrustDomain creates an mTLS authenticator that pins
// the given SPIFFE trust domain. A SPIFFE tenant SAN under a different trust
// domain is rejected (fail-closed).
func NewMTLSAuthenticatorWithTrustDomain(trustDomain string) *MTLSAuthenticator {
	return &MTLSAuthenticator{pinnedTrustDomain: trustDomain}
}

// Name returns "mtls".
func (a *MTLSAuthenticator) Name() string {
	return "mtls"
}

// TenantFromCert derives the tenant from a verified client certificate using
// the SPIFFE SAN (pinned trust domain) with an OU fallback. It returns the
// derived tenant ID and reports whether the tenant came from a SPIFFE SAN.
//
// A SPIFFE SAN under a mismatched trust domain returns a non-nil error and
// must cause the login to fail closed.
func (a *MTLSAuthenticator) TenantFromCert(cert *x509.Certificate) (tenantID string, fromSPIFFE bool, err error) {
	if cert == nil {
		return "", false, ErrInvalidCertificate
	}

	id, serr := spiffe.TenantFromCert(cert, a.pinnedTrustDomain)
	switch {
	case serr == nil:
		return id.TenantID, true, nil
	case errors.Is(serr, spiffe.ErrTrustDomainMismatch):
		// Foreign trust domain: fail closed, do not fall back.
		return "", false, ErrInvalidCertificate
	case errors.Is(serr, spiffe.ErrNoTenantSPIFFEID):
		// No SPIFFE SAN: legacy OU fallback.
		if len(cert.Subject.OrganizationalUnit) > 0 {
			return cert.Subject.OrganizationalUnit[0], false, nil
		}
		return "", false, nil
	default:
		// Malformed SPIFFE SAN: fail closed.
		return "", false, ErrInvalidCertificate
	}
}

// Authenticate extracts identity from the TLS peer certificate.
// The TLS connection state must be stored in context via WithTLSState.
func (a *MTLSAuthenticator) Authenticate(ctx context.Context) (*Identity, error) {
	state := TLSStateFromContext(ctx)
	if state == nil {
		return nil, ErrNoPeerCertificate
	}

	if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		if len(state.PeerCertificates) == 0 {
			return nil, ErrNoPeerCertificate
		}
		return nil, ErrInvalidCertificate
	}

	cert := state.VerifiedChains[0][0]

	id := &Identity{
		Subject:  cert.Subject.CommonName,
		Metadata: make(map[string]string),
	}

	// Derive the tenant from the SPIFFE SAN (pinned) with an OU fallback. A
	// trust-domain mismatch or malformed SPIFFE SAN fails closed.
	tenantID, fromSPIFFE, err := a.TenantFromCert(cert)
	if err != nil {
		return nil, err
	}
	id.TenantID = tenantID
	if fromSPIFFE {
		id.Metadata["tenant_source"] = "spiffe"
	} else if tenantID != "" {
		id.Metadata["tenant_source"] = "ou"
	}

	// Organization becomes a metadata field.
	if len(cert.Subject.Organization) > 0 {
		id.Metadata["organization"] = cert.Subject.Organization[0]
	}

	// DNS SANs as metadata.
	if len(cert.DNSNames) > 0 {
		id.Metadata["dns_san"] = cert.DNSNames[0]
	}

	return id, nil
}
