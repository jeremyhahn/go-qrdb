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

// Package clienttrust is the single implementation of the dynamic
// client-auth trust pool every client-facing listener composer (the qrdbd
// daemon and the SDK node builders) wires onto its own *tls.Config, rather
// than each keeping its own copy of this logic.
//
// See docs/certificate-authority/architecture.md, "Key B trust on the
// client-facing listeners": a presented client certificate must verify
// against Key A (the cluster CA) plus every tenant's Key B CA certificate
// (the login CA, read fresh on every connection so a CA created -- or
// rotated to an HSM-backed one -- after the listener started is picked up
// without a restart).
//
// Pool is ALSO the issuer-tenant binding (IsClusterCA / VerifyIssuer): the
// pool and the binding are the SAME object, deliberately, so Key B can
// never be trusted at the TLS layer without the binding also being
// enforceable at the authentication layer. See SetClusterCA.
package clienttrust

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// poolCacheTTL bounds how long a widened ClientCAs pool is reused before
// CACertPEMs is consulted again. It is the current mitigation for the
// per-handshake cost of scanning and parsing every tenant's CA certificate
// (go-qrdb#560 review, B2-4): a newly created or rotated Key B CA is picked
// up within this window, not instantly -- there is no push notification
// (generation counter or watch) from pkg/cert yet. SetKeyBProvider and
// SetClusterCA both invalidate the cache immediately, so a provider or
// cluster-CA swap is never subject to this staleness window.
const poolCacheTTL = 2 * time.Second

// handshakeTrustTimeout bounds how long a single TLS handshake's trust
// resolution (configForClient's ClientCAs widening, by extension
// VerifyConnection's peer check against the pool that resolution produces)
// may take. hello.Context() is canceled when the handshake's own connection
// deadline fires or the connection closes early, but carries no deadline of
// its own, so configForClient always derives a bounded one from it, never
// context.Background() (go-qrdb#591's review, B5): an unbounded resolve on
// the handshake path would let one slow storage read hang that connection's
// handshake indefinitely, and context.Background() can never be canceled by
// anything, including the listener shutting down.
//
// A var, not a const: pool_test.go shortens it for
// TestPool_ConfigForClient_HandshakeBoundedNotBackground, which proves the
// bound is real by making a resolver block until canceled -- at the
// production value that test would have to wait out the whole 5 seconds to
// observe the deadline firing.
var handshakeTrustTimeout = 5 * time.Second

// CertSource is the capability DefaultCertProvider provides: every tenant's
// Key B CA certificate (or, with an injected cakey.Provider, the single one
// that replaces them), and a load-only check of whether a given certificate
// is a specific tenant's registered issuer. It is declared here, rather
// than added to cert.CertProvider, because that interface is what an
// embedder's own CertProvider replacement (server.WithCertProvider) must
// satisfy, and multi-tenant enumeration is not part of its contract -- a
// type assertion against this shape is how Pool asks for the capability
// without widening the interface every CertProvider implementation must
// carry.
type CertSource interface {
	CACertPEMs(ctx context.Context) ([][]byte, error)
	IsTenantIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) (bool, error)
}

// ClusterCA resolves the cluster CA (Key A) certificate, fresh on every
// call -- never cached by the caller, because a reseal, rotation, or
// bootstrap-to-persisted transition must be visible on the very next call.
// ceremony.CeremonyCA satisfies this structurally.
//
// ctx bounds the resolution. A handshake-time caller (configForClient, via
// resolveClientCAs/resolveClusterCA) derives it from the handshake's own
// *tls.ClientHelloInfo.Context() with a bounded timeout, never
// context.Background() -- see handshakeTrustTimeout.
type ClusterCA interface {
	CACertificate(ctx context.Context) (*x509.Certificate, error)
}

// StaticClusterCA wraps a fixed certificate as a ClusterCA, for a
// deployment with no cakey.Provider to resolve Key A from at all: a
// BYO-TLS node (an operator-supplied --tls-ca-file/--tls-cert-file/
// --tls-key-file trust anchor qrdb was never handed the private key for)
// has no cluster CA KEY, but the operator's own peer CA is still the
// cluster's identity anchor for node-to-node Raft trust, and the owner's
// ruling is that it is ALSO the anchor IsClusterCA and on-behalf
// verification check against on such a node -- the same role a
// self-issued cluster's cakey.Provider-backed ClusterCA plays, just
// without a key this process ever holds.
type StaticClusterCA struct {
	cert *x509.Certificate
}

// NewStaticClusterCA returns a ClusterCA that always resolves to cert.
// Returns an error when cert is nil: unlike a cakey.Provider-backed
// ClusterCA, which can legitimately resolve to "not yet established" and
// is handled by SetClusterCA's own nil-ClusterCA case, a StaticClusterCA
// exists only to wrap a certificate a caller already has in hand, so a
// nil one is the caller's own construction error, not a deployment state
// this type should paper over by resolving to "no cluster CA" silently.
func NewStaticClusterCA(cert *x509.Certificate) (*StaticClusterCA, error) {
	if cert == nil {
		return nil, &StaticClusterCANilCertificateError{}
	}
	return &StaticClusterCA{cert: cert}, nil
}

// CACertificate returns the wrapped certificate. It is already resolved and
// held in memory, so ctx is unused -- there is nothing here to cancel or
// bound, and nothing this call could ever generate.
func (s *StaticClusterCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return s.cert, nil
}

// Pool holds the dynamic Key B trust state, and the issuer-tenant binding,
// behind a tls.Config.GetConfigForClient hook installed by Wire. The zero
// value is not usable; construct with New.
type Pool struct {
	provider   atomic.Pointer[providerBox]
	clusterCA  atomic.Pointer[clusterCABox]
	cache      atomic.Pointer[poolCache]
	keyBCache  atomic.Pointer[keyBCacheEntry]
	staticBase atomic.Pointer[x509.CertPool]
}

type providerBox struct{ prov cert.CertProvider }
type clusterCABox struct{ ca ClusterCA }
type poolCache struct {
	built time.Time
	pool  *x509.CertPool
}

// keyBCacheEntry holds every tenant's Key B certificate CACertPEMs
// returned, refreshed INDEPENDENTLY of the cluster CA's own health
// (go-qrdb#591's review, B3): isRegisteredKeyBCA -- and through it,
// IsStaticallyTrusted, the exemption Key A and every operator/harness
// login the composer trusted before Key B ever existed relies on -- must
// never depend on whether the cluster CA currently resolves. Before this
// split, both read the SAME cache resolveClientCAs built, and
// resolveClientCAs returned early, before ever reaching CACertPEMs,
// whenever an installed cluster CA resolver was merely failing right now
// (a corrupt record, a reseal): the Key-B-only cache then never
// refreshed, isRegisteredKeyBCA answered "uncertain" forever, and
// IsStaticallyTrusted refused the exemption for every issuer, including
// ones with nothing to do with Key B at all.
//
// pems is kept alongside keyBOnly (not reconstructed from it -- CertPool
// exposes no enumeration) so resolveClientCAs can merge the identical set
// into the full widened pool without a second CACertPEMs call.
type keyBCacheEntry struct {
	built    time.Time
	pems     [][]byte
	keyBOnly *x509.CertPool
}

// New returns a Pool with neither a Key B provider nor a cluster CA
// installed yet: until SetKeyBProvider is called, every connection sees the
// listener's static ClientCAs pool unchanged (Key A only, whatever the
// composer set there directly), which is the correct pre-unseal state.
// SetClusterCA is independent of that: a composer with no cluster-CA
// concept at all (SetClusterCA never called, or called with nil) still
// gets Key B trust once SetKeyBProvider is called -- the issuer-tenant
// binding's per-tenant check (VerifyIssuer's IsTenantIssuer branch) needs
// no cluster CA to operate, only the cluster-CA EXEMPTION does. What
// SetClusterCA's absence can never do is grant that exemption: see
// IsClusterCA, VerifyIssuer, resolveClientCAs.
func New() *Pool {
	return &Pool{}
}

// SetKeyBProvider installs the CertProvider the pool reads Key B's CA
// certificates from, and invalidates the cached widened pool so the next
// connection reflects it immediately. Call it once, as soon as the
// CertProvider exists. A nil prov (or one that does not implement
// CertSource) means Key B is never trusted.
//
// The composer calling this MUST ALSO pass this SAME Pool as the
// auth.IssuerVerifier wired into the authentication layer (mTLS
// authenticator and on-behalf forwarding path) -- unconditionally,
// regardless of whether SetClusterCA is ever called. The pool widening TLS
// trust without that binding also being enforced is exactly the gap
// go-qrdb#560's review (B2-2) closed; Pool cannot verify this invariant
// for itself, because it has no visibility into whether some OTHER
// component installed it as the auth layer's verifier, so the composer's
// wiring discipline is where it is actually enforced.
func (p *Pool) SetKeyBProvider(prov cert.CertProvider) {
	p.provider.Store(&providerBox{prov: prov})
	p.cache.Store(nil)
	p.keyBCache.Store(nil)
}

// SetClusterCA installs the resolver the pool checks every issuer against,
// for the cluster-CA EXEMPTION on both the TLS-layer widening decision
// (the cluster CA certificate is added to the widened pool) and the
// issuer-tenant binding (IsClusterCA, VerifyIssuer's cluster-CA branch).
// Invalidates the cached widened pool.
//
// Never calling this (or calling it with a nil ca) is a deployment shape
// with no cluster-CA concept at all -- the exemption never applies, but
// Key B is STILL trusted once SetKeyBProvider is called, gated purely by
// the per-tenant binding (see resolveClientCAs, VerifyIssuer). This is
// different from a ca that resolves to an ERROR once installed (a reseal,
// a transient failure): that withdraws Key B trust entirely until the
// resolver succeeds again, because an installed resolver answering
// differently now is a signal, where "no resolver was ever installed" is
// not.
func (p *Pool) SetClusterCA(ca ClusterCA) {
	p.clusterCA.Store(&clusterCABox{ca: ca})
	p.cache.Store(nil)
}

// resolveClusterCA returns the current cluster CA certificate, resolved
// fresh (never from the pool's own cache -- see ClusterCA). ctx bounds the
// resolution; see ClusterCA's own doc comment.
func (p *Pool) resolveClusterCA(ctx context.Context) (*x509.Certificate, error) {
	box := p.clusterCA.Load()
	if box == nil || box.ca == nil {
		return nil, &ClusterCANotConfiguredError{}
	}
	cert, err := box.ca.CACertificate(ctx)
	if err != nil {
		return nil, &ClusterCAUnavailableError{Err: err}
	}
	return cert, nil
}

// IsClusterCA reports whether cert is byte-identical to the CURRENT cluster
// CA certificate. Unknown -- no ClusterCA configured, or a transient
// failure resolving it -- is never a match: fail closed.
func (p *Pool) IsClusterCA(ctx context.Context, cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	ca, err := p.resolveClusterCA(ctx)
	if err != nil || ca == nil {
		return false
	}
	return bytes.Equal(ca.Raw, cert.Raw)
}

// IsStaticallyTrusted reports whether issuer verifies against the
// listener's OWN static ClientCAs pool -- whatever the composer put in
// cfg.ClientCAs directly, BEFORE Key B widening, via its own WithTLS
// (an operator-supplied additional client CA, or -- in a test harness --
// the harness's own CA used to establish a real-mTLS identity independent
// of Key A/Key B entirely). This is pre-existing trust the composer
// already explicitly granted; Key B's dynamic widening is additive to it,
// and the issuer-tenant binding exists to police THAT addition, not to
// retroactively impose tenant-matching on a CA the composer trusted before
// Key B was ever in the picture.
//
// Reflects the static pool captured ONCE, at Wire time -- this listener's
// OWN configuration, fixed for this Pool's lifetime -- never resampled
// per-connection or per-handshake. Re-reading cfg.ClientCAs on every call
// would let one connection's view of a concurrently-mutated config leak
// into another's authentication decision; capturing it once at Wire makes
// the exemption's scope a property of THIS listener, not of whichever
// connection's handshake happened to run most recently. nil (Wire was
// never called) is never a match: fail closed, same as every other
// resolution failure in this type.
//
// Refuses the exemption outright when issuer is ALSO a registered Key B
// CA for some tenant (isRegisteredKeyBCA), uncertain or not: an operator
// (or a test harness) that accidentally adds a tenant's Key B CA to the
// static ClientCAs must not thereby grant that CA a blanket, any-tenant
// pass -- it stays scoped to its own tenant via the ordinary per-tenant
// check below.
func (p *Pool) IsStaticallyTrusted(issuer *x509.Certificate) bool {
	if issuer == nil {
		return false
	}
	pool := p.staticBase.Load()
	if pool == nil {
		return false
	}
	if _, err := issuer.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return false
	}
	return !p.isRegisteredKeyBCA(issuer)
}

// isRegisteredKeyBCA reports whether issuer verifies against the Key-B-only
// portion of the most recently cached widened pool (see poolCache.keyBOnly)
// -- i.e. whether it is SOME tenant's registered Key B CA, regardless of
// which.
//
// No Key B provider installed at all (SetKeyBProvider never called) answers
// false unconditionally: with no Key B concept in play for this Pool, there
// is no Key B CA issuer could "accidentally" be. Otherwise, uncertain (a
// provider IS installed but no handshake has built the cache yet) answers
// true: IsStaticallyTrusted's caller treats "might be a Key B CA" the same
// as "is one" and refuses the static exemption, because granting a blanket
// pass on an unproven negative is the wrong direction to be wrong in.
func (p *Pool) isRegisteredKeyBCA(issuer *x509.Certificate) bool {
	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return false
	}
	cached := p.keyBCache.Load()
	if cached == nil || cached.keyBOnly == nil {
		return true
	}
	_, err := issuer.Verify(x509.VerifyOptions{Roots: cached.keyBOnly, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

// VerifyIssuer reports whether issuer may authenticate as tenantID: the
// cluster CA (Key A, any tenant), a CA already in the listener's own
// static ClientCAs (see IsStaticallyTrusted), or tenantID's own registered
// Key B CA (CertSource.IsTenantIssuer, a load-only, byte-identical-DER
// check). Returns *IssuerNotTrustedError when it is none of those, or when
// the check itself could not be completed.
func (p *Pool) VerifyIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error {
	if p.IsClusterCA(ctx, issuer) {
		return nil
	}
	if p.IsStaticallyTrusted(issuer) {
		return nil
	}
	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return &IssuerNotTrustedError{TenantID: tenantID}
	}
	src, ok := box.prov.(CertSource)
	if !ok {
		return &IssuerNotTrustedError{TenantID: tenantID}
	}
	ok2, err := src.IsTenantIssuer(ctx, tenantID, issuer)
	if err != nil {
		return &IssuerNotTrustedError{TenantID: tenantID, Err: err}
	}
	if !ok2 {
		return &IssuerNotTrustedError{TenantID: tenantID}
	}
	return nil
}

// Wire installs cfg.GetConfigForClient, so that every client-facing
// listener using cfg verifies a presented client certificate against Key A
// (cfg.ClientCAs, read fresh on every connection via the closure below --
// the WIDENING decision reads the live field, so an operator rotating
// additional client CA files is picked up without re-wiring) plus every
// tenant's Key B CA certificate, read from whatever provider
// SetKeyBProvider most recently installed -- see resolveClientCAs for
// exactly how SetClusterCA's presence, absence, or failure each affect
// this. cfg.ClientCAs is ALSO captured once, right here, as a fixed
// snapshot for IsStaticallyTrusted's exemption specifically -- see its own
// doc comment for why that check needs a stable, Wire-time value rather
// than the live field the widening decision reads.
//
// Call once per *tls.Config, after cfg.ClientCAs already holds its Key-A
// baseline, and before any listener sharing it starts.
// A failed Key B resolution makes the installed hook return the base config
// unmodified -- cfg itself (equivalent to crypto/tls's own "no override"
// convention) when Wire composed with no prior hook, or whatever a prior
// composed hook itself returned otherwise -- the correct behavior before
// SetKeyBProvider has been called, on a reseal (CACertPEMs then fails: see
// docs/certificate-authority/architecture.md's "while sealed, Key B isn't
// available" rule), and for a CertProvider that does not implement
// CertSource.
//
// This hook does not reassert any OTHER field a listener-specific wrapper
// tunes (REST/gRPC/Unix/MCP's TLS 1.3 floor, QUIC's exclusive ALPN
// protocol): see ReassertListenerTLS, which every such wrapper must compose
// with its own override, in that order, for its tuning to survive a
// connection that takes this hook's widened-pool path.
//
// The peer/Raft/enrollment transport must never call Wire on its own TLS
// config: it is meant to trust the cluster CA (Key A) only, never Key B, so
// its trust pool is built independently, directly from the cluster CA
// certificate.
// Composes with, rather than overwrites, a GetConfigForClient hook cfg
// already carries: a composer that wires its own dynamic hook (a cert
// reloader's own per-connection override, say) before calling Wire would
// otherwise have that hook silently discarded, with no error -- the
// listener would keep working, just without whatever the lost hook did,
// which is the kind of regression that is invisible until it is a reported
// behavior gap (go-qrdb#591's review, non-blocking). The existing hook, if
// any, runs FIRST on every handshake, and its own returned config (when
// non-nil) becomes the base Pool's OWN widening decision applies on top of,
// so neither hook's effect is lost.
func (p *Pool) Wire(cfg *tls.Config) {
	// Captured ONCE, here, for IsStaticallyTrusted's exemption -- see its
	// own doc comment for why this must be a fixed, Wire-time snapshot
	// rather than resampled per connection.
	p.staticBase.Store(cfg.ClientCAs)
	existing := cfg.GetConfigForClient
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		base := cfg
		if existing != nil {
			got, err := existing(hello)
			if err != nil {
				return nil, err
			}
			if got != nil {
				base = got
			}
		}
		return p.configForClient(hello, base)
	}
}

// configForClient builds the per-connection config GetConfigForClient
// returns, or (base, nil) to keep base unchanged on a failed resolution.
//
// clone.ClientCAs is deliberately left as base's own static baseline --
// NEVER the widened pool -- because crypto/tls derives the
// CertificateRequest message's advertised acceptable-CA list directly
// from ClientCAs, with no way to use a pool for verification without
// also advertising it (go-qrdb#591's review, B2): every tenant's Key B
// CA subject would otherwise be sent, in the clear, to any client that
// merely initiates a handshake, and past roughly 1,500 tenants the
// message exceeds the TLS 64 KiB limit, failing EVERY mTLS login on the
// listener, Key A and the operator's own static CAs included.
//
// Verification against the full widened pool moves to VerifyConnection
// (below) instead, which crypto/tls runs for every connection regardless
// of ClientAuth. ClientAuth itself is remapped so crypto/tls does not
// ALSO attempt its own verification against the now-deliberately-narrow
// ClientCAs and reject a Key-B-issued cert before VerifyConnection ever
// sees it: a mode requiring verification becomes the matching mode that
// requires a certificate but performs none, preserving whether a
// certificate is required at all -- the operator's own
// mtls_binding_policy choice -- while moving ALL verification to one
// place for both Key A and Key B.
//
// The trust resolution below is bounded by a context derived from hello's
// own Context(), never context.Background() -- see handshakeTrustTimeout.
func (p *Pool) configForClient(hello *tls.ClientHelloInfo, base *tls.Config) (*tls.Config, error) {
	// A real crypto/tls handshake always supplies a non-nil Context()
	// (since Go 1.17); nil is reachable only from a caller that built a
	// *tls.ClientHelloInfo directly rather than through an actual
	// handshake, which several packages' own tests do to exercise
	// GetConfigForClient without a real TLS connection. Falling back to
	// context.Background() only in that case keeps those tests a safe
	// no-op rather than a context.WithTimeout panic ("cannot create
	// context from nil parent") -- the real handshake path above always
	// has a genuine one to derive from.
	parent := hello.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, handshakeTrustTimeout)
	defer cancel()
	pool, err := p.resolveClientCAs(ctx, base.ClientCAs)
	if err != nil {
		// base, not nil: base is cfg itself when Wire's caller installed no
		// prior GetConfigForClient hook, so returning it is equivalent to
		// the original (nil, nil) "keep crypto/tls on the unmodified cfg"
		// behavior -- but when Wire composed with an existing hook, base is
		// THAT hook's own returned config, which must survive a failure
		// here untouched rather than being silently discarded.
		return base, nil
	}
	clone := base.Clone()
	switch clone.ClientAuth {
	case tls.RequireAndVerifyClientCert:
		clone.ClientAuth = tls.RequireAnyClientCert
	case tls.VerifyClientCertIfGiven:
		clone.ClientAuth = tls.RequestClientCert
	}
	clone.VerifyConnection = func(cs tls.ConnectionState) error {
		return p.verifyPeerConnection(cs, pool)
	}
	// crypto/tls invokes GetConfigForClient at most once per handshake, so
	// this is not re-entered for the connection being set up; nil it anyway
	// so the clone is never mistaken for a second, live hook.
	clone.GetConfigForClient = nil
	return clone, nil
}

// verifyPeerConnection is Wire's VerifyConnection hook: the manual
// verification configForClient's ClientAuth remap makes crypto/tls skip
// automatically. A connection with no presented certificate is left to
// whatever remapped ClientAuth mode decided (an optional mode lets it
// through; a required one never reaches here with an empty list at all,
// since crypto/tls refuses the handshake before VerifyConnection runs).
// ExtKeyUsageAny, not ExtKeyUsageClientAuth specifically, matches
// IsStaticallyTrusted's own verification above for the same reason: a
// leaf this codebase issues for client authentication is not guaranteed
// to carry that EKU explicitly, and this is the TRUST decision, not an
// EKU policy one.
func (p *Pool) verifyPeerConnection(cs tls.ConnectionState, pool *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return nil
	}
	intermediates := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return &PeerCertificateNotTrustedError{Err: err}
	}
	return nil
}

// VerifyPeerChain verifies leaf (with optional intermediates) against
// this Pool's current widened trust pool -- the SAME verification
// verifyPeerConnection performs during the TLS handshake -- for a caller
// that needs to re-derive the chain crypto/tls itself no longer builds:
// ClientAuth's remap in configForClient means tls.ConnectionState.
// VerifiedChains is never populated for a connection this Pool wired,
// so pkg/auth's authenticator (every request, not merely the handshake --
// see its own doc comment on why) calls this instead whenever
// VerifiedChains comes back empty on a connection Wire touched, rather
// than treating an unverified PeerCertificates list as good enough.
// Returns the verified chain (leaf first) on success.
func (p *Pool) VerifyPeerChain(ctx context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	if leaf == nil {
		return nil, &PeerCertificateNotTrustedError{}
	}
	pool, err := p.resolveClientCAs(ctx, p.staticBase.Load())
	if err != nil {
		return nil, &PeerCertificateNotTrustedError{Err: err}
	}
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return nil, &PeerCertificateNotTrustedError{Err: err}
	}
	return chains[0], nil
}

// resolveClientCAs returns the widened ClientCAs pool (the cluster CA, when
// one is configured and resolves, plus every tenant's Key B CA
// certificate), consulting a short-lived cache (see poolCacheTTL), or an
// error when Key B must not be trusted right now.
//
// The cluster CA is resolved FIRST, unconditionally, on every call, but its
// two failure shapes are NOT treated the same (B2-2's actual requirement is
// that the issuer-tenant binding is always enforced alongside Key B trust,
// which SetKeyBProvider's doc comment states as a composer-wiring
// obligation -- it is not "Key B requires a cluster CA to exist"):
//   - *ClusterCANotConfiguredError (SetClusterCA never called, or called
//     with nil): a deployment shape with no cluster-CA concept at all. Key
//     B still widens; VerifyIssuer's per-tenant IsTenantIssuer branch gates
//     it, and needs no cluster CA to operate -- only the exemption does.
//   - Any other error (an installed resolver that is CURRENTLY failing --
//     sealed, a transient store failure): the WIDENED pool this call
//     returns fails closed -- a resolver that answered before and is
//     answering differently now is a signal this function must not paper
//     over. This withdraws the cluster-CA entry and exemption from THIS
//     call's pool, never the Key-B-only cache refresh below (B3, see
//     refreshKeyBOnlyCache's own doc comment): a broken cluster CA must
//     not also make isRegisteredKeyBCA -- and through it,
//     IsStaticallyTrusted's exemption for Key A and every operator/harness
//     login -- degrade to "uncertain" forever.
func (p *Pool) resolveClientCAs(ctx context.Context, baseClientCAs *x509.CertPool) (*x509.CertPool, error) {
	// Refreshed FIRST, and unconditionally of the cluster CA's own state
	// below: see refreshKeyBOnlyCache's own doc comment for why.
	p.refreshKeyBOnlyCache(ctx)

	clusterCACert, err := p.resolveClusterCA(ctx)
	if err != nil {
		var notConfiguredErr *ClusterCANotConfiguredError
		if !errors.As(err, &notConfiguredErr) {
			return nil, err
		}
		clusterCACert = nil
	}

	if cached := p.cache.Load(); cached != nil && time.Since(cached.built) < poolCacheTTL {
		return cached.pool, nil
	}

	keyB := p.keyBCache.Load()
	if keyB == nil {
		box := p.provider.Load()
		if box == nil || box.prov == nil {
			return nil, &KeyBUnavailableError{Reason: "no Key B provider installed"}
		}
		if _, ok := box.prov.(CertSource); !ok {
			return nil, &KeyBUnavailableError{Reason: "the installed CertProvider does not implement CertSource"}
		}
		// A provider IS installed and does implement CertSource, but
		// refreshKeyBOnlyCache has never once succeeded (every attempt
		// so far, including the one just above, failed): sealed is the
		// ordinary cause in Raft mode.
		return nil, &KeyBUnavailableError{Reason: "CACertPEMs has not yet succeeded"}
	}

	pool := x509.NewCertPool()
	if baseClientCAs != nil {
		pool = baseClientCAs.Clone()
	}
	if clusterCACert != nil {
		pool.AddCert(clusterCACert)
	}
	for _, certPEM := range keyB.pems {
		pool.AppendCertsFromPEM(certPEM)
	}

	p.cache.Store(&poolCache{built: time.Now(), pool: pool})
	return pool, nil
}

// refreshKeyBOnlyCache rebuilds p.keyBCache from the installed Key B
// provider's CACertPEMs, when the current entry (if any) is older than
// poolCacheTTL. It never returns an error: a failure (no provider, no
// CertSource, CACertPEMs itself failing -- sealed is the ordinary cause
// in Raft mode) simply leaves whatever is already cached in place,
// stale or nil, rather than tearing it down -- isRegisteredKeyBCA's
// "uncertain" answer for a nil/never-built cache is the conservative
// default for a Key B CA it cannot rule out, but a cache that WAS built
// and is merely aging is still the best available answer, not something
// a transient CACertPEMs failure should discard.
//
// Called from resolveClientCAs UNCONDITIONALLY, before that function's
// own cluster-CA resolution: a broken cluster CA resolver must never
// prevent this refresh (go-qrdb#591's review, B3), because
// isRegisteredKeyBCA reads ONLY this cache, and a cache that stops
// refreshing degrades to "uncertain" forever -- which IsStaticallyTrusted
// then treats as "assume yes, refuse the exemption" for every issuer,
// including the cluster CA's own operator-configured static anchors and
// a test harness's ad-hoc CA, neither of which has anything to do with
// Key B.
func (p *Pool) refreshKeyBOnlyCache(ctx context.Context) {
	if cached := p.keyBCache.Load(); cached != nil && time.Since(cached.built) < poolCacheTTL {
		return
	}
	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return
	}
	src, ok := box.prov.(CertSource)
	if !ok {
		return
	}
	pems, err := src.CACertPEMs(ctx)
	if err != nil {
		return
	}
	keyBOnly := x509.NewCertPool()
	for _, certPEM := range pems {
		keyBOnly.AppendCertsFromPEM(certPEM)
	}
	p.keyBCache.Store(&keyBCacheEntry{built: time.Now(), pems: pems, keyBOnly: keyBOnly})
}
