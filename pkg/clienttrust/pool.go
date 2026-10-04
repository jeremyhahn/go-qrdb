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
type ClusterCA interface {
	CACertificate() (*x509.Certificate, error)
}

// Pool holds the dynamic Key B trust state, and the issuer-tenant binding,
// behind a tls.Config.GetConfigForClient hook installed by Wire. The zero
// value is not usable; construct with New.
type Pool struct {
	provider   atomic.Pointer[providerBox]
	clusterCA  atomic.Pointer[clusterCABox]
	cache      atomic.Pointer[poolCache]
	staticBase atomic.Pointer[x509.CertPool]
}

type providerBox struct{ prov cert.CertProvider }
type clusterCABox struct{ ca ClusterCA }
type poolCache struct {
	built time.Time
	pool  *x509.CertPool
	// keyBOnly holds ONLY the Key B certificates fetched to build pool --
	// never the static base, never the cluster CA -- so
	// isRegisteredKeyBCA can ask "is this issuer SOME tenant's Key B CA"
	// without re-fetching CACertPEMs on every authentication check. Built
	// from the SAME src.CACertPEMs call pool is, at zero extra cost.
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
// fresh (never from the pool's own cache -- see ClusterCA).
func (p *Pool) resolveClusterCA() (*x509.Certificate, error) {
	box := p.clusterCA.Load()
	if box == nil || box.ca == nil {
		return nil, &ClusterCANotConfiguredError{}
	}
	cert, err := box.ca.CACertificate()
	if err != nil {
		return nil, &ClusterCAUnavailableError{Err: err}
	}
	return cert, nil
}

// IsClusterCA reports whether cert is byte-identical to the CURRENT cluster
// CA certificate. Unknown -- no ClusterCA configured, or a transient
// failure resolving it -- is never a match: fail closed.
func (p *Pool) IsClusterCA(cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	ca, err := p.resolveClusterCA()
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
	cached := p.cache.Load()
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
	if p.IsClusterCA(issuer) {
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
// Returning (nil, nil) from the installed hook keeps crypto/tls on the
// original, unmodified cfg -- the correct behavior before SetKeyBProvider
// has been called, on a reseal (CACertPEMs then fails: see
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
func (p *Pool) Wire(cfg *tls.Config) {
	// Captured ONCE, here, for IsStaticallyTrusted's exemption -- see its
	// own doc comment for why this must be a fixed, Wire-time snapshot
	// rather than resampled per connection.
	p.staticBase.Store(cfg.ClientCAs)
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return p.configForClient(cfg)
	}
}

// configForClient builds the per-connection config GetConfigForClient
// returns, or (nil, nil) to keep base unchanged.
func (p *Pool) configForClient(base *tls.Config) (*tls.Config, error) {
	pool, err := p.resolveClientCAs(context.Background(), base.ClientCAs)
	if err != nil {
		return nil, nil
	}
	clone := base.Clone()
	clone.ClientCAs = pool
	// crypto/tls invokes GetConfigForClient at most once per handshake, so
	// this is not re-entered for the connection being set up; nil it anyway
	// so the clone is never mistaken for a second, live hook.
	clone.GetConfigForClient = nil
	return clone, nil
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
//     sealed, a transient store failure): fail closed, no widening at all.
//     A resolver that answered before and is answering differently now is
//     a signal this function must not paper over by falling back to
//     whatever was cached, which is why this check runs before the cache
//     read below, every time.
func (p *Pool) resolveClientCAs(ctx context.Context, baseClientCAs *x509.CertPool) (*x509.CertPool, error) {
	// The cluster CA is resolved FIRST, on every call -- but its absence
	// splits into two different outcomes. Never configured at all
	// (*ClusterCANotConfiguredError) is a deployment shape with no cluster
	// CA concept, same as this composer never calling SetClusterCA: Key B
	// still widens, gated purely by the issuer-tenant binding's per-tenant
	// check (VerifyIssuer's IsTenantIssuer branch), which needs no cluster
	// CA to operate. Configured but CURRENTLY failing (e.g. a reseal) is
	// fail-closed: that resolver answered once and is answering differently
	// now, which must withdraw Key B trust immediately, not merely stop
	// granting the cluster-CA exemption.
	clusterCACert, err := p.resolveClusterCA()
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

	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return nil, &KeyBUnavailableError{Reason: "no Key B provider installed"}
	}
	src, ok := box.prov.(CertSource)
	if !ok {
		return nil, &KeyBUnavailableError{Reason: "the installed CertProvider does not implement CertSource"}
	}
	pems, err := src.CACertPEMs(ctx)
	if err != nil {
		// Sealed (the ordinary cause in Raft mode, where the replicated
		// store cannot be opened while sealed, and now also checked
		// directly by CACertPEMs itself) is not reported as a distinct
		// case: any failure here means "fall back", which configForClient
		// already does for every KeyBUnavailableError/*.
		return nil, &KeyBUnavailableError{Reason: "CACertPEMs failed", Err: err}
	}

	pool := x509.NewCertPool()
	if baseClientCAs != nil {
		pool = baseClientCAs.Clone()
	}
	if clusterCACert != nil {
		pool.AddCert(clusterCACert)
	}
	// keyBOnly holds ONLY the certificates just fetched -- never the static
	// base, never the cluster CA -- so isRegisteredKeyBCA can ask "is this
	// issuer SOME tenant's Key B CA" without re-fetching CACertPEMs.
	keyBOnly := x509.NewCertPool()
	for _, certPEM := range pems {
		pool.AppendCertsFromPEM(certPEM)
		keyBOnly.AppendCertsFromPEM(certPEM)
	}

	p.cache.Store(&poolCache{built: time.Now(), pool: pool, keyBOnly: keyBOnly})
	return pool, nil
}
