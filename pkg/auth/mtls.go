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
	"net/url"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
// The Subject CN becomes the identity Subject. The TenantID is the tenant
// TenantFromClientCert reads: the trust domain of the workload SPIFFE URI SAN
// (spiffe://<tenantID>/<kind>/<name>), which the first Subject OU must name
// too. The configured trust domain is the cluster's: a certificate naming no
// tenant must be under it, and no tenant may be it.
//
// This authenticator never invents a tenant: a SPIFFE SAN whose trust domain
// is not the OU's tenant is a hard rejection.
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

	// delegatedPrincipal is the service principal this node accepts
	// tenant-issued delegations for (WithDelegatedPrincipal). Empty refuses
	// every delegation.
	delegatedPrincipal string

	// tenants, when non-nil, is consulted on EVERY authentication naming a
	// tenant other than the system tenant: the tenant must exist
	// (WithTenantDirectory). Nil means the check is not enforced.
	tenants TenantDirectory
}

// TenantDirectory reports whether a tenant exists. It is the seam through
// which the mTLS authenticator holds a certificate's trust domain to the
// tenants the cluster actually has: a SPIFFE ID names its tenant as the trust
// domain, and a trust domain no tenant was created for names no one.
type TenantDirectory interface {
	// TenantExists reports whether tenantID names a tenant. An error is a
	// failed lookup, never an absent tenant.
	TenantExists(ctx context.Context, tenantID string) (bool, error)
}

// WithTenantDirectory makes the authenticator refuse a certificate naming a
// tenant the directory does not know, with a *CertificateTenantError carrying
// *UnknownTenantError, and a certificate whose tenant the directory could not
// look up with a *TenantLookupError. The system tenant is never looked up: it
// always exists, and it is the tenant a cluster bootstraps through.
func WithTenantDirectory(d TenantDirectory) MTLSOption {
	return func(a *MTLSAuthenticator) {
		a.tenants = d
	}
}

// checkTenantExists applies WithTenantDirectory's rule to tenantID.
func (a *MTLSAuthenticator) checkTenantExists(ctx context.Context, tenantID string) error {
	if a.tenants == nil || tenantid.IsSystem(tenantID) {
		return nil
	}
	exists, err := a.tenants.TenantExists(ctx, tenantID)
	if err != nil {
		return &TenantLookupError{TenantID: tenantID, Err: err}
	}
	if !exists {
		return &CertificateTenantError{Err: &UnknownTenantError{TenantID: tenantID}}
	}
	return nil
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
	IsClusterCA(ctx context.Context, issuer *x509.Certificate) bool

	// VerifyIssuer reports whether a verified chain (leaf first) may
	// authenticate as tenantID: its direct issuer, chain[1], is the cluster
	// CA (any tenant, via IsClusterCA) or tenantID's registered Key B CA, or
	// the chain contains one of tenantID's registered trust anchors above
	// the leaf. It takes the whole chain because an anchor -- an enterprise
	// root, typically -- may stand several intermediates above the leaf.
	VerifyIssuer(ctx context.Context, tenantID string, chain []*x509.Certificate) error
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
func (a *MTLSAuthenticator) TenantFromCert(cert *x509.Certificate) (string, error) {
	return TenantFromClientCert(cert, a.pinnedTrustDomain)
}

// TenantFromClientCert is qrdb's certificate-to-tenant rule, the one the mTLS
// authenticator applies to every login. A tenant is a SPIFFE trust domain, so
// a tenant's principal is spiffe://<tenant>/<kind>/<name>, and a verified
// client certificate names a tenant only when two independent fields carry it
// and agree:
//
//   - the first Subject OU, and
//   - the trust domain of its workload SPIFFE ID URI SAN.
//
// clusterTrustDomain is the cluster's own trust domain, the one a certificate
// naming no tenant is under. It is never a tenant: a certificate whose OU
// names it is refused, so no tenant shares the cluster's trust bundle.
//
// Every refusal is a *CertificateTenantError, which matches
// ErrInvalidCertificate under errors.Is and carries the cause:
//
//   - no workload SPIFFE ID SAN: spiffe.ErrNoTenantSPIFFEID;
//   - a SPIFFE SAN that is not a SPIFFE ID, or SANs under more than one trust
//     domain (spiffe.AmbiguousTenantError): the spiffe cause;
//   - no Subject OU: ErrNoTenantOU under the cluster trust domain, and
//     spiffe.ErrTrustDomainMismatch under any other;
//   - an OU naming the cluster trust domain: ErrTenantIsClusterTrustDomain;
//   - an OU naming a different tenant from the trust domain, which is how a
//     foreign trust domain presents: a *TenantNameMismatchError.
//
// There is no fallback in either direction: an OU alone, or a SPIFFE ID
// alone, names no tenant. With clusterTrustDomain empty the two cluster
// checks are skipped; that is for tests, and a composer must pin its domain.
//
// The system tenant is named like any other ("system" in both fields). A
// composing host that resolves a certificate's user outside the middleware --
// before any tenant is bound, on a public route -- applies this rule, binds
// that tenant into the context, and looks the thumbprint up within it. It
// never searches other tenants for the thumbprint.
//
// cert must already be verified against the CA; this reads names, it does
// not check signatures.
func TenantFromClientCert(cert *x509.Certificate, clusterTrustDomain string) (string, error) {
	id, err := clientWorkload(cert, clusterTrustDomain)
	if err != nil {
		return "", err
	}
	return id.TrustDomain, nil
}

// clientWorkload applies TenantFromClientCert's rule and returns the whole
// workload identity, whose TrustDomain is the tenant.
func clientWorkload(cert *x509.Certificate, clusterTrustDomain string) (spiffe.ID, error) {
	if cert == nil {
		return spiffe.ID{}, ErrInvalidCertificate
	}
	id, err := spiffe.WorkloadFromCert(cert)
	if err != nil {
		return spiffe.ID{}, &CertificateTenantError{Err: err}
	}
	if len(cert.Subject.OrganizationalUnit) == 0 {
		if clusterTrustDomain != "" && id.TrustDomain != clusterTrustDomain {
			return spiffe.ID{}, &CertificateTenantError{Err: spiffe.ErrTrustDomainMismatch}
		}
		return spiffe.ID{}, &CertificateTenantError{Err: ErrNoTenantOU}
	}
	ou := cert.Subject.OrganizationalUnit[0]
	if clusterTrustDomain != "" && ou == clusterTrustDomain {
		return spiffe.ID{}, &CertificateTenantError{Err: ErrTenantIsClusterTrustDomain}
	}
	if ou != id.TrustDomain {
		return spiffe.ID{}, &CertificateTenantError{Err: &TenantNameMismatchError{OU: ou, SPIFFETenant: id.TrustDomain}}
	}
	return id, nil
}

// TenantPrincipalURIs is the URI SAN of a certificate issued to a tenant
// principal: exactly one SPIFFE ID, spiffe://<tenantID>/<kind>/<name>, under
// the tenant's own trust domain. The issuer pairs it with tenantID as the
// Subject's first OU, and TenantFromClientCert then accepts the certificate as
// naming tenantID. Every go-qrdb issuer builds the SAN here, so the half of
// the rule an issuer writes is the half it reads.
//
// kind is spiffe.KindUser for an account named by the certificate's CN and
// spiffe.KindService for a delegated service principal. A refusal is a
// *spiffe.IDError: a tenant that is not a SPIFFE trust domain name, a name
// that is not one SPIFFE ID path segment, an unknown kind, or a tenant equal
// to clusterTrustDomain (ErrTenantIsClusterTrustDomain), which the rule would
// refuse. A clusterTrustDomain that is not a trust domain name is a
// *spiffe.TrustDomainError: without it the issuer cannot tell a tenant from
// the cluster. No certificate is signed without the SAN.
func TenantPrincipalURIs(clusterTrustDomain, tenantID, kind, name string) ([]*url.URL, error) {
	if err := spiffe.ValidateTrustDomain(clusterTrustDomain); err != nil {
		return nil, err
	}
	id := spiffe.ID{TrustDomain: tenantID, Kind: kind, Name: name}
	if tenantID == clusterTrustDomain {
		return nil, &spiffe.IDError{ID: id, Err: ErrTenantIsClusterTrustDomain}
	}
	u, err := id.URI()
	if err != nil {
		return nil, err
	}
	return []*url.URL{u}, nil
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

	// A certificate carrying a tenant-issued delegation is judged as a
	// delegation or refused; it is never read as the user login it would
	// otherwise look like. That is what its critical extension promises, and
	// what lets the trust pool hand crypto/x509 a copy with the extension
	// marked handled (delegation.Verifiable).
	if g, found, derr := delegation.FromCertificate(cert); found {
		if derr != nil {
			return nil, &DelegationRefusedError{Reason: "malformed delegation", Err: derr}
		}
		return a.authenticateDelegation(ctx, chains[0], g)
	}

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

	// The tenant is named by the Subject OU and the (pinned) SPIFFE ID SAN
	// together; a certificate lacking either, or whose two disagree, is
	// refused.
	tenantID, err := a.TenantFromCert(cert)
	if err != nil {
		return nil, err
	}
	// The tenant they name must exist.
	if err := a.checkTenantExists(ctx, tenantID); err != nil {
		return nil, err
	}
	id.TenantID = tenantID

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

// verifyIssuer checks every verified chain against a.issuerVerifier, handing
// it the WHOLE chain: Key A and Key B are judged on the leaf's DIRECT issuer
// (chain[1]), a registered trust anchor on any certificate above the leaf.
// Succeeds if ANY verified chain satisfies the binding; a TLS stack can
// present more than one verified chain when multiple roots in the pool could
// validate the same leaf.
func (a *MTLSAuthenticator) verifyIssuer(ctx context.Context, tenantID string, chains [][]*x509.Certificate) error {
	for _, chain := range chains {
		if len(chain) < 2 {
			continue
		}
		if err := a.issuerVerifier.VerifyIssuer(ctx, tenantID, chain); err == nil {
			return nil
		}
	}
	return &IssuerTenantMismatchError{TenantID: tenantID}
}
