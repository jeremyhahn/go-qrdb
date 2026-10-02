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

	// revocation, when non-nil, is consulted on EVERY authentication. Nil
	// means no revocation checker was wired and revocation is not enforced —
	// which is the posture a deployment with no certificate provider has, and
	// it is visible here rather than implied.
	revocation RevocationChecker
}

// MTLSOption configures an MTLSAuthenticator.
type MTLSOption func(*MTLSAuthenticator)

// WithRevocationChecker enables per-request certificate-revocation
// enforcement.
//
// Without it a revoked certificate keeps authenticating until it expires: the
// revocation is recorded, listed and displayed, and enforced nowhere. See
// RevocationChecker for why this belongs on the authentication path rather
// than in a TLS handshake callback.
func WithRevocationChecker(rc RevocationChecker) MTLSOption {
	return func(a *MTLSAuthenticator) {
		a.revocation = rc
	}
}

// NewMTLSAuthenticator creates a new mTLS authenticator with no SPIFFE trust
// domain pin. SPIFFE SANs (if present) are still honored for tenant
// derivation, but no trust-domain pinning is enforced. Use
// NewMTLSAuthenticatorWithTrustDomain to pin a trust domain (recommended).
func NewMTLSAuthenticator(opts ...MTLSOption) *MTLSAuthenticator {
	return newMTLSAuthenticator("", opts)
}

// NewMTLSAuthenticatorWithTrustDomain creates an mTLS authenticator that pins
// the given SPIFFE trust domain. A SPIFFE tenant SAN under a different trust
// domain is rejected (fail-closed).
func NewMTLSAuthenticatorWithTrustDomain(trustDomain string, opts ...MTLSOption) *MTLSAuthenticator {
	return newMTLSAuthenticator(trustDomain, opts)
}

// newMTLSAuthenticator is the single constructor both exported forms delegate
// to, so an option added here cannot reach one of them and miss the other.
func newMTLSAuthenticator(trustDomain string, opts []MTLSOption) *MTLSAuthenticator {
	a := &MTLSAuthenticator{pinnedTrustDomain: trustDomain}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(a)
	}
	return a
}

// MethodMTLS is the name MTLSAuthenticator reports, and therefore the token a
// user's permitted-method policy (user.User.AuthMethods) spells to allow an
// mTLS login.
//
// It is a constant rather than a literal because two things now compare
// against it — the permitted-method policy and the certificate-binding gate in
// auth/middleware — and a second copy of the string is a second place for the
// two to disagree.
const MethodMTLS = "mtls"

// Name returns MethodMTLS.
func (a *MTLSAuthenticator) Name() string {
	return MethodMTLS
}

// TenantFromCert derives the tenant from a verified client certificate with
// TenantFromClientCert under this authenticator's pinned trust domain.
func (a *MTLSAuthenticator) TenantFromCert(cert *x509.Certificate) (tenantID string, fromSPIFFE bool, err error) {
	return TenantFromClientCert(cert, a.pinnedTrustDomain)
}

// TenantFromClientCert is qrdb's certificate-to-tenant rule, the one the mTLS
// authenticator applies to every login. It returns the tenant a verified
// client certificate names and reports whether it came from a SPIFFE SAN:
//
//   - a tenant-scoped SPIFFE URI SAN wins. When pinnedTrustDomain is
//     non-empty, ANY SPIFFE SAN under another trust domain fails closed and
//     is never overridden by the OU;
//   - SANs naming more than one distinct tenant fail closed
//     (spiffe.AmbiguousTenantError), as does a malformed SPIFFE SAN;
//   - every fail-closed answer is a *CertificateTenantError, which matches
//     ErrInvalidCertificate under errors.Is and carries the spiffe cause;
//   - with pinnedTrustDomain empty, any trust domain is accepted. That is
//     for tests and single-domain labs: a composer must pin its domain;
//   - with no SPIFFE tenant SAN, the first Subject OU is the tenant;
//   - with neither, the tenant is "" and err is nil.
//
// "" is not an error: the authentication middleware folds it onto the system
// tenant (tenant.Normalize) before anything reads it. A composing host that
// resolves a certificate's user outside the middleware -- before any tenant
// is bound, on a public route -- applies this rule, normalizes the result the
// same way, binds that tenant into the context, and looks the thumbprint up
// within it. It never searches other tenants for the thumbprint.
//
// cert must already be verified against the CA; this reads names, it does
// not check signatures.
func TenantFromClientCert(cert *x509.Certificate, pinnedTrustDomain string) (tenantID string, fromSPIFFE bool, err error) {
	if cert == nil {
		return "", false, ErrInvalidCertificate
	}

	id, serr := spiffe.TenantFromCert(cert, pinnedTrustDomain)
	switch {
	case serr == nil:
		return id.TenantID, true, nil
	case errors.Is(serr, spiffe.ErrTrustDomainMismatch):
		// Foreign trust domain: fail closed, do not fall back.
		return "", false, &CertificateTenantError{Err: serr}
	case errors.Is(serr, spiffe.ErrNoTenantSPIFFEID):
		// No SPIFFE SAN: legacy OU fallback.
		if len(cert.Subject.OrganizationalUnit) > 0 {
			return cert.Subject.OrganizationalUnit[0], false, nil
		}
		return "", false, nil
	default:
		// Malformed SPIFFE SAN, or SANs naming more than one tenant: fail
		// closed.
		return "", false, &CertificateTenantError{Err: serr}
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

	// Revocation is enforced HERE, on every request, before any identity is
	// derived from the certificate. A TLS handshake callback would not do: it
	// fires once per connection, so on QUIC a peer revoked mid-session would
	// stay authorised for the life of its connection.
	//
	// The three-way policy — refuse, fail closed, or admit when there is no
	// state to read — lives in EnforceRevocation, because the middleware's
	// on-behalf forwarding path applies the same one.
	if err := EnforceRevocation(ctx, a.revocation, cert); err != nil {
		return nil, err
	}

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

	// A holder certificate (an officer's or custodian's) is marked, so the
	// authentication middleware never resolves it to a user account.
	if profile, ok := HolderProfile(cert); ok {
		id.Metadata[HolderProfileMetadataKey] = profile
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
