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

	// issuerVerifier, when non-nil, is consulted on EVERY authentication to
	// bind the certificate's claimed tenant to the CA that actually verified
	// it. Nil means the check is not enforced -- the posture a deployment
	// with no dynamic Key B trust pool wired has. *clienttrust.Pool
	// implements it: the pool and the binding are the SAME object, by
	// design (go-qrdb#560 review, B2-2), so Key B is never trusted at the
	// TLS layer without this check also being enforceable here.
	issuerVerifier IssuerVerifier
}

// IssuerVerifier binds a verified client certificate's claimed tenant
// (MTLSAuthenticator's own TenantFromCert derivation) to the CA that
// actually verified it: the cluster CA (Key A, any tenant -- holder and
// node certificates are legitimately signed by it regardless of what a
// Subject OU says) or the Key B CA registered for the certificate's OWN
// claimed tenant. See docs/certificate-authority/architecture.md,
// "Issuer-tenant binding". *clienttrust.Pool implements it.
//
// Without this, every tenant's Key B CA being trusted on one shared
// client-facing listener would let tenant A's CA mint a credential that
// authenticates as tenant B simply by setting its Subject OU to B: the
// dynamic trust pool verifies the SIGNATURE, not which tenant the signing
// CA was issued for.
type IssuerVerifier interface {
	// VerifyPeerChain verifies leaf (with optional intermediates) against
	// the dynamic trust pool's current widened set, returning the verified
	// chain (leaf first) on success. ResolveVerifiedChain calls this
	// instead of trusting state.VerifiedChains when the latter is empty:
	// pkg/clienttrust.Pool.Wire remaps a verifying ClientAuth mode to a
	// non-verifying one so crypto/tls's own CertificateRequest never
	// advertises every tenant's Key B CA subject (go-qrdb#591's review,
	// B2), which means crypto/tls itself never builds VerifiedChains for a
	// connection Wire touched -- this is the ONE place that verification
	// still happens, now explicit rather than automatic.
	VerifyPeerChain(ctx context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error)

	// IsClusterCA reports whether issuer is the CURRENT cluster CA
	// certificate (Key A), resolved fresh on every call. Unknown (no
	// cluster CA configured, or a transient failure resolving it) is never
	// a match: fail closed.
	IsClusterCA(issuer *x509.Certificate) bool

	// VerifyIssuer reports whether issuer may authenticate as tenantID: the
	// cluster CA (any tenant, via IsClusterCA) or tenantID's registered Key
	// B CA.
	VerifyIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error
}

// WithIssuerVerifier enables the issuer-tenant binding check: see
// IssuerVerifier. A nil verifier (the default) leaves the check
// unenforced.
func WithIssuerVerifier(v IssuerVerifier) MTLSOption {
	return func(a *MTLSAuthenticator) {
		a.issuerVerifier = v
	}
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

// ResolveVerifiedChains returns every candidate verified chain for the TLS
// peer (leaf first in each), from state.VerifiedChains when crypto/tls
// already built one, or a single-chain slice derived by calling
// verifier.VerifyPeerChain when state.VerifiedChains is empty -- which it
// always is on a connection pkg/clienttrust.Pool.Wire touched, since Wire
// remaps a verifying ClientAuth mode to a non-verifying one specifically
// so crypto/tls never advertises every tenant's Key B CA subject in its
// CertificateRequest (go-qrdb#591's review, B2). verifier is typically the
// SAME IssuerVerifier an authenticator already holds; nil (no dynamic
// trust pool wired) preserves the original behavior exactly: an empty
// VerifiedChains with no verifier to ask is ErrInvalidCertificate, same as
// it always was before Wire could ever leave it empty.
//
// crypto/tls can report MORE THAN ONE verified chain for the same leaf
// when multiple roots in a pool could validate it; verifyIssuer's own
// contract is to accept as soon as ANY one's direct issuer satisfies the
// binding, not only the first, so every candidate crypto/tls built is
// preserved here rather than collapsed to one.
//
// Every caller of this function MUST be reachable only on a connection
// whose listener either wired Wire (so a non-nil verifier is exactly
// correct and Wire already vetted the chain's trust during the
// handshake) or never calls it at all (nil verifier, original strict
// behavior unchanged) -- never a THIRD case where ClientAuth was
// independently relaxed without Wire's own VerifyConnection hook also
// being installed, which would make this function trust an unverified
// certificate. No code in this tree creates that case: ClientAuth's
// remap and VerifyConnection's installation happen together, in
// Pool.configForClient, with no seam for one without the other.
func ResolveVerifiedChains(ctx context.Context, state *tls.ConnectionState, verifier IssuerVerifier) ([][]*x509.Certificate, error) {
	if state == nil {
		return nil, ErrNoPeerCertificate
	}
	if len(state.VerifiedChains) > 0 && len(state.VerifiedChains[0]) > 0 {
		return state.VerifiedChains, nil
	}
	if len(state.PeerCertificates) == 0 {
		return nil, ErrNoPeerCertificate
	}
	if verifier == nil {
		return nil, ErrInvalidCertificate
	}
	chain, err := verifier.VerifyPeerChain(ctx, state.PeerCertificates[0], state.PeerCertificates[1:])
	if err != nil {
		return nil, ErrInvalidCertificate
	}
	return [][]*x509.Certificate{chain}, nil
}

// Authenticate extracts identity from the TLS peer certificate.
// The TLS connection state must be stored in context via WithTLSState.
func (a *MTLSAuthenticator) Authenticate(ctx context.Context) (*Identity, error) {
	state := TLSStateFromContext(ctx)
	if state == nil {
		return nil, ErrNoPeerCertificate
	}

	chains, err := ResolveVerifiedChains(ctx, state, a.issuerVerifier)
	if err != nil {
		return nil, err
	}

	cert := chains[0][0]

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

	// The issuer-tenant binding: the CA that DIRECTLY signed this leaf must
	// be either the cluster CA (Key A, which legitimately signs holder and
	// node certificates for any tenant) or the Key B CA registered for
	// THIS certificate's own claimed tenant. Every tenant's Key B CA is
	// trusted on one shared listener (pkg/clienttrust), so without this
	// check tenant A's CA could mint a leaf that authenticates as tenant B
	// simply by setting its Subject OU to B.
	if a.issuerVerifier != nil {
		if err := a.verifyIssuer(ctx, tenantID, chains); err != nil {
			return nil, err
		}
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

// verifyIssuer checks every verified chain against a.issuerVerifier, using
// EACH chain's DIRECT issuer (chain[1], the certificate that signed the
// leaf) rather than whatever happens to be the last entry -- today's CAs
// are all self-signed roots so the two coincide, but the direct issuer is
// the precise thing being asked about. Succeeds if ANY verified chain
// satisfies the binding; a TLS stack can present more than one verified
// chain when multiple roots in the pool could validate the same leaf.
func (a *MTLSAuthenticator) verifyIssuer(ctx context.Context, tenantID string, chains [][]*x509.Certificate) error {
	for _, chain := range chains {
		if len(chain) < 2 {
			continue
		}
		if err := a.issuerVerifier.VerifyIssuer(ctx, tenantID, chain[1]); err == nil {
			return nil
		}
	}
	return &IssuerTenantMismatchError{TenantID: tenantID}
}
