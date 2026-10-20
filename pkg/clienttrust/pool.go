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
// (the login CA). The handshake never reads storage: it loads an immutable
// snapshot that Run rebuilds in the background on every change event and
// every poolCacheTTL, so a CA created -- or rotated to an HSM-backed one --
// after the listener started is picked up without a restart, and a store
// that stalls (no Raft leader, a reseal) can never hold a handshake past its
// deadline.
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
	"encoding/pem"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// poolCacheTTL is the interval at which Run rebuilds the trust snapshot
// when no change event has asked for a rebuild sooner. The handshake never
// reads storage: it loads the snapshot Run last built (go-qrdb#713). On the
// node where it happens, a change is pushed: a tenant's Key B CA coming
// into use (cert.WithNewCAHook), a watched tenant or CA deletion
// (InvalidationSource) and a trust anchor added or removed all reach
// Invalidate, which asks Run to rebuild at once. A tenant create does not
// wait on that rebuild: it calls Refresh before answering, so the admin
// certificate it returns is admitted on this node's next handshake. This
// interval bounds
// only what no local event announces: a CA first used on ANOTHER node, and
// a reseal.
const poolCacheTTL = 2 * time.Second

// refreshTimeout bounds one rebuild's storage reads. A rebuild that runs
// out of time has failed, and a failed rebuild withdraws Key B trust
// (go-qrdb#591, blocker 2), so a store that stops answering costs Key B
// logins until it answers again, never the handshake itself. It sits well
// inside the 5 s handshake deadline the listeners set, as a second line of
// defence: the handshake does not wait on a rebuild at all.
//
// A var, not a const: pool_test.go shortens it to observe the bound
// without waiting out the production value.
var refreshTimeout = 2 * time.Second

// snapshotMaxAge is how old a snapshot may grow before the handshake
// treats it as withdrawn. Run replaces it every poolCacheTTL, so one three
// intervals old means rebuilds have stopped landing -- Run never started,
// or a store read that ignores its deadline -- and Key B trust must not
// outlive its source of truth without bound.
const snapshotMaxAge = 3 * poolCacheTTL

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

// InvalidationSource is the capability DefaultCertProvider provides
// alongside CertSource: a way for Pool to learn, immediately, when the
// provider's own Key B cache changes outside of any handshake -- a
// watched tenant deletion (tombstone) or a watched CA material/certificate
// deletion, see DefaultCertProvider.WatchForDeletions -- so Run rebuilds
// the snapshot without that tenant's CA right away instead of waiting out
// poolCacheTTL. Declared here, not added to cert.CertProvider, for the
// same reason CertSource is: a type assertion against this shape is how
// Pool asks for the capability without widening the interface every
// CertProvider implementation must carry.
type InvalidationSource interface {
	// OnInvalidate registers fn to be called whenever this provider's Key
	// B cache is invalidated for some tenant. May be called more than
	// once, for different subscribers; a provider that supports it calls
	// every registered fn.
	OnInvalidate(fn func())
}

// ClusterCA resolves the cluster CA (Key A) certificate, fresh on every
// call -- never cached by the caller beyond one snapshot, because a reseal,
// rotation, or bootstrap-to-persisted transition must be visible to the
// authentication layer (IsClusterCA) on the very next call, and to the
// handshake on the next rebuild. ceremony.CeremonyCA satisfies this
// structurally.
//
// ctx bounds the resolution. The rebuild (Refresh) bounds it by
// refreshTimeout; the handshake never calls it.
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
// value is not usable; construct with New, and start Run.
//
// The configuration (provider, clusterCA, anchors, staticBase) and the
// trust built from it (snapshot) are separate: the setters replace the
// configuration and withdraw the snapshot, Refresh reads storage and
// publishes a new one, and the handshake only loads it (go-qrdb#713).
type Pool struct {
	provider   atomic.Pointer[providerBox]
	clusterCA  atomic.Pointer[clusterCABox]
	anchors    atomic.Pointer[anchorsBox]
	staticBase atomic.Pointer[x509.CertPool]
	snapshot   atomic.Pointer[trustSnapshot]
	clock      atomic.Pointer[clockBox]

	// kick asks Run to rebuild now. Capacity one: any number of Invalidate
	// calls before Run gets to it collapse into a single rebuild, and
	// Invalidate never blocks.
	kick chan struct{}
}

// TenantLister enumerates the tenants whose trust anchors join the widened
// pool. The composer adapts its tenant service to it.
type TenantLister interface {
	TenantIDs(ctx context.Context) ([]string, error)
}

// TenantListerFunc adapts a function to TenantLister.
type TenantListerFunc func(ctx context.Context) ([]string, error)

// TenantIDs implements TenantLister.
func (f TenantListerFunc) TenantIDs(ctx context.Context) ([]string, error) { return f(ctx) }

type providerBox struct{ prov cert.CertProvider }
type clusterCABox struct{ ca ClusterCA }
type clockBox struct{ clk clock.Clock }
type anchorsBox struct {
	src     trustanchor.Source
	tenants TenantLister
}

// trustSnapshot is one immutable result of Refresh: everything the
// handshake and VerifyPeerChain need, built from storage once and then
// only read. Never modified after it is published, except for derived,
// which is itself a write-once cache of a pure function of the rest.
//
// keyBOnly is filled whenever the Key B read succeeded, independently of
// the cluster CA's own health (go-qrdb#591's review, B3): isRegisteredKeyBCA
// -- and through it IsStaticallyTrusted, the exemption Key A and every
// operator/harness login relies on -- must never depend on whether the
// cluster CA currently resolves.
type trustSnapshot struct {
	built time.Time

	// err is why this snapshot grants no widening: Key B unreadable, an
	// installed cluster CA resolver failing (a reseal), or a configuration
	// change whose rebuild has not landed yet. widened is nil exactly when
	// err is non-nil.
	err error

	// base is the static pool widened was built over (staticBase when the
	// rebuild ran); extra is every certificate added to it.
	base    *x509.CertPool
	extra   []*x509.Certificate
	widened *x509.CertPool

	keyBOnly  *x509.CertPool
	clusterCA *x509.Certificate

	// derived holds the widened pool over a base OTHER than base -- a
	// listener whose cfg.ClientCAs was replaced after Wire, or a composed
	// hook returning its own config -- built on first use and kept for
	// this snapshot's lifetime, so a handshake clones at most once per
	// rebuild.
	derived atomic.Pointer[derivedPool]
}

type derivedPool struct {
	base *x509.CertPool
	pool *x509.CertPool
}

// New returns a Pool with neither a Key B provider nor a cluster CA
// installed yet: until SetKeyBProvider is called and a rebuild has run,
// every connection sees the listener's static ClientCAs pool unchanged
// (Key A only, whatever the composer set there directly), which is the
// correct pre-unseal state. SetClusterCA is independent of that: a
// composer with no cluster-CA concept at all (SetClusterCA never called,
// or called with nil) still gets Key B trust once SetKeyBProvider is
// called -- the issuer-tenant binding's per-tenant check (VerifyIssuer's
// IsTenantIssuer branch) needs no cluster CA to operate, only the
// cluster-CA EXEMPTION does. What SetClusterCA's absence can never do is
// grant that exemption: see IsClusterCA, VerifyIssuer, build.
//
// The composer must start Run for the pool to ever widen: nothing else
// builds a snapshot.
func New() *Pool {
	return &Pool{kick: make(chan struct{}, 1)}
}

// SetKeyBProvider installs the CertProvider the pool reads Key B's CA
// certificates from, withdraws the current snapshot -- trust read from the
// previous provider is not this one's -- and asks Run to rebuild. Call it
// once, as soon as the CertProvider exists. A nil prov (or one that does
// not implement CertSource) means Key B is never trusted.
//
// When prov also implements InvalidationSource (DefaultCertProvider, when
// WatchForDeletions is running), Pool subscribes Invalidate so a deleted
// tenant's CA leaves the handshake's pool as soon as one rebuild lands --
// on every node the watched deletion replicates to, not only the one that
// issued it -- rather than up to poolCacheTTL later. The authentication
// layer does not wait for that rebuild: VerifyIssuer's IsTenantIssuer
// re-checks the issuer on every request.
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
	p.withdraw(false)
	if inv, ok := prov.(InvalidationSource); ok {
		inv.OnInvalidate(p.Invalidate)
	}
}

// SetClusterCA installs the resolver the pool checks every issuer against,
// for the cluster-CA EXEMPTION on both the TLS-layer widening decision
// (the cluster CA certificate is added to the widened pool) and the
// issuer-tenant binding (IsClusterCA, VerifyIssuer's cluster-CA branch).
// Withdraws the current snapshot and asks Run to rebuild.
//
// Never calling this (or calling it with a nil ca) is a deployment shape
// with no cluster-CA concept at all -- the exemption never applies, but
// Key B is STILL trusted once SetKeyBProvider is called, gated purely by
// the per-tenant binding (see build, VerifyIssuer). This is different
// from a ca that resolves to an ERROR once installed (a reseal, a
// transient failure): that withdraws Key B trust entirely until the
// resolver succeeds again, because an installed resolver answering
// differently now is a signal, where "no resolver was ever installed" is
// not.
func (p *Pool) SetClusterCA(ca ClusterCA) {
	p.clusterCA.Store(&clusterCABox{ca: ca})
	p.withdraw(true)
}

// SetClock sets the time VerifyPeerChain checks a certificate's lifetime
// against: the node's security time source (sdk/go/node WithClock), so a
// certificate that a request presented, or that a delegated context holds,
// expires on the same clock as every other security time gate. A nil c
// keeps the real clock. It does not change the handshake, which crypto/tls
// verifies on its own clock.
func (p *Pool) SetClock(c clock.Clock) {
	if c == nil {
		p.clock.Store(nil)
		return
	}
	p.clock.Store(&clockBox{clk: c})
}

// now is the time on the clock SetClock installed, or the real time.
func (p *Pool) now() time.Time {
	if b := p.clock.Load(); b != nil {
		return b.clk.Now()
	}
	return time.Now()
}

// SetTrustAnchors installs the source of every tenant's registered trust
// anchors (pkg/trustanchor) and the lister that enumerates the tenants,
// withdraws the current snapshot and asks Run to rebuild.
//
// Each tenant's anchors then join the widened ClientCAs pool, so a
// bring-your-own certificate chaining to one completes a handshake, and
// VerifyIssuer admits a chain containing one of a tenant's anchors as that
// tenant's -- and only that tenant's. A nil src removes anchor trust.
//
// Install the Registry through trustanchor.Observe with Invalidate as the
// callback, so a tenant adding or withdrawing an anchor on this node is
// reflected as soon as one rebuild lands rather than after poolCacheTTL.
func (p *Pool) SetTrustAnchors(src trustanchor.Source, tenants TenantLister) {
	if src == nil {
		p.anchors.Store(nil)
	} else {
		p.anchors.Store(&anchorsBox{src: src, tenants: tenants})
	}
	p.withdraw(true)
}

// withdraw publishes a snapshot that grants no widening, so trust built
// from the configuration a setter just replaced is never served again,
// and asks Run to rebuild from the new one. keepKeyBOnly carries the
// Key-B-only pool over when the provider did not change: what it confirms
// about Key B is still true, and dropping it would only re-open
// IsStaticallyTrusted's exemption to a registered Key B CA until the
// rebuild lands.
func (p *Pool) withdraw(keepKeyBOnly bool) {
	next := &trustSnapshot{
		built: time.Now(),
		err:   &KeyBUnavailableError{Reason: "trust configuration changed; rebuild pending"},
	}
	if prev := p.snapshot.Load(); prev != nil && keepKeyBOnly {
		next.keyBOnly = prev.keyBOnly
	}
	p.snapshot.Store(next)
	p.Invalidate()
}

// Invalidate asks Run to rebuild the snapshot now rather than at the next
// poolCacheTTL tick. It never reads storage and never blocks: it is called
// from a CA-creation hook, a watch callback and the trust-anchor observer,
// none of which may stall on the store. Calls made before Run gets to the
// first collapse into one rebuild. Until that rebuild lands the handshake
// keeps the last snapshot -- a caller that must not wait for it calls
// Refresh instead -- and a deleted tenant's CA is still refused at once by VerifyIssuer's
// per-request IsTenantIssuer check.
func (p *Pool) Invalidate() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run rebuilds the snapshot whenever Invalidate asks and every
// poolCacheTTL otherwise, until ctx is done. The composer starts it once,
// in its own goroutine, for the lifetime of the listeners Wire touched;
// the first rebuild runs at once.
func (p *Pool) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		case <-timer.C:
		}
		_ = p.Refresh(ctx)
		timer.Reset(poolCacheTTL)
	}
}

// Refresh reads the current trust state from storage, bounded by
// refreshTimeout, and publishes it as the snapshot the handshake loads.
// It returns the reason the new snapshot grants no widening, or nil.
//
// When Refresh returns, the published snapshot was built after the call
// began. A change that lands while the read is in flight -- a setter's
// withdrawal, or a concurrent rebuild that read the store earlier --
// supersedes this rebuild: its result, read partly from the state that
// change replaced, is discarded rather than published over it, and Refresh
// builds again from the current state. A rebuild superseded on every
// attempt until refreshTimeout runs out returns *RefreshSupersededError.
//
// Run is the caller in production. A caller that has just changed trust and
// hands out a certificate depending on it (a tenant create answering with a
// certificate its new CA signed) calls it so this node's next handshake
// admits that certificate.
func (p *Pool) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	for {
		prev := p.snapshot.Load()
		snap := p.build(ctx)
		if p.snapshot.CompareAndSwap(prev, snap) {
			return snap.err
		}
		if err := ctx.Err(); err != nil {
			return &RefreshSupersededError{Err: err}
		}
	}
}

// build reads one snapshot. The static pool, the cluster CA, every tenant's
// Key B CA and every trust anchor join the widened pool, but the cluster
// CA's two failure shapes are NOT treated the same (B2-2's actual
// requirement is that the issuer-tenant binding is always enforced
// alongside Key B trust, which SetKeyBProvider's doc comment states as a
// composer-wiring obligation -- it is not "Key B requires a cluster CA to
// exist"):
//   - *ClusterCANotConfiguredError (SetClusterCA never called, or called
//     with nil): a deployment shape with no cluster-CA concept at all. Key
//     B still widens; VerifyIssuer's per-tenant IsTenantIssuer branch gates
//     it, and needs no cluster CA to operate -- only the exemption does.
//   - Any other error (an installed resolver that is CURRENTLY failing --
//     sealed, a transient store failure): the snapshot grants no widening
//     -- a resolver that answered before and is answering differently now
//     is a signal this function must not paper over. keyBOnly is still
//     filled (B3, see trustSnapshot).
//
// A failed Key B read also grants no widening (go-qrdb#591, blocker 2):
// Key B stops being trusted once its source of truth stops answering, at
// the latest one rebuild later -- a reseal is the ordinary cause in Raft
// mode (docs/certificate-authority/architecture.md's "a runtime reseal must
// make the dynamic client-auth trust pool drop Key B immediately" rule).
func (p *Pool) build(ctx context.Context) *trustSnapshot {
	snap := &trustSnapshot{built: time.Now(), base: p.staticBase.Load()}
	keyBCerts, keyBErr := p.readKeyB(ctx)
	if keyBErr == nil {
		snap.keyBOnly = x509.NewCertPool()
		for _, c := range keyBCerts {
			snap.keyBOnly.AddCert(c)
		}
	}
	clusterCACert, err := p.resolveClusterCA(ctx)
	switch {
	case err == nil:
		snap.clusterCA = clusterCACert
	case !errors.As(err, new(*ClusterCANotConfiguredError)):
		snap.err = err
	}
	if snap.err == nil {
		snap.err = keyBErr
	}
	if snap.err != nil {
		return snap
	}
	if snap.clusterCA != nil {
		snap.extra = append(snap.extra, snap.clusterCA)
	}
	snap.extra = append(snap.extra, keyBCerts...)
	for _, bundle := range p.anchorBundles(ctx) {
		snap.extra = append(snap.extra, parsePEMCertificates(bundle)...)
	}
	snap.widened = widen(snap.base, snap.extra)
	return snap
}

// readKeyB returns every tenant's Key B CA certificate, or why Key B
// cannot be trusted right now.
func (p *Pool) readKeyB(ctx context.Context) ([]*x509.Certificate, error) {
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
		return nil, &KeyBUnavailableError{Reason: "CACertPEMs failed", Err: err}
	}
	var certs []*x509.Certificate
	for _, certPEM := range pems {
		certs = append(certs, parsePEMCertificates(certPEM)...)
	}
	return certs, nil
}

// parsePEMCertificates returns every certificate in data, skipping what
// x509.CertPool.AppendCertsFromPEM skips: blocks that are not
// CERTIFICATE, blocks carrying headers, and certificates that do not
// parse. Parsed once per rebuild, so deriving a pool over another base
// costs AddCert calls, not a second parse.
func parsePEMCertificates(data []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	for len(data) > 0 {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		certs = append(certs, c)
	}
	return certs
}

// widen returns a copy of base (an empty pool when base is nil) with every
// certificate in extra added.
func widen(base *x509.CertPool, extra []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	if base != nil {
		pool = base.Clone()
	}
	for _, c := range extra {
		pool.AddCert(c)
	}
	return pool
}

// current returns the snapshot when it grants widening and is younger
// than snapshotMaxAge, or nil.
func (p *Pool) current() *trustSnapshot {
	snap := p.snapshot.Load()
	if snap == nil || snap.widened == nil || time.Since(snap.built) >= snapshotMaxAge {
		return nil
	}
	return snap
}

// widenedFor returns the current widened pool over base, or nil when no
// current snapshot grants widening. It loads atomics and does CPU work
// only: this is the handshake path, and it never reads storage.
func (p *Pool) widenedFor(base *x509.CertPool) *x509.CertPool {
	snap := p.current()
	if snap == nil {
		return nil
	}
	if base == snap.base {
		return snap.widened
	}
	if d := snap.derived.Load(); d != nil && d.base == base {
		return d.pool
	}
	pool := widen(base, snap.extra)
	snap.derived.Store(&derivedPool{base: base, pool: pool})
	return pool
}

// resolveClusterCA returns the current cluster CA certificate, resolved
// fresh (never from the snapshot -- see ClusterCA). ctx bounds the
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
// Refuses the exemption outright when issuer is AFFIRMATIVELY CONFIRMED to
// be a registered Key B CA for some tenant (isRegisteredKeyBCA against a
// snapshot's Key-B-only pool): an operator (or a test harness) that
// accidentally adds a tenant's Key B CA to the static ClientCAs must not
// thereby grant that CA a blanket, any-tenant pass -- it stays scoped to
// its own tenant via the ordinary per-tenant check below. A Key B that no
// rebuild has read successfully (e.g. still sealed) is
// NOT treated as a match -- it proves nothing about issuer either way, and
// B3's actual requirement (go-qrdb#591's review, B3 remainder) is that a
// static anchor is honored regardless of Key B's own state; only an
// affirmatively confirmed Key B CA may fail closed.
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

// isRegisteredKeyBCA reports whether issuer AFFIRMATIVELY verifies against
// the Key-B-only pool of the latest snapshot (trustSnapshot.keyBOnly) --
// i.e. whether it is CONFIRMED to be some tenant's registered Key B CA,
// regardless of which. The snapshot's age does not matter here: an old
// confirmation can only make IsStaticallyTrusted stricter.
//
// No Key B provider installed at all (SetKeyBProvider never called), or a
// provider installed but no rebuild has read it yet (sealed, or Run not
// yet run), both answer false: an unread Key B is not evidence that
// issuer IS a Key B CA, only that this Pool cannot yet say either way,
// and B3's actual requirement (go-qrdb#591's review, B3 remainder) is that
// static, Key A and operator anchors are ALWAYS honored regardless of Key
// B's state -- only an issuer this Pool can AFFIRMATIVELY confirm is a
// registered Key B CA may cause IsStaticallyTrusted to refuse the
// exemption. Before this, an unbuilt cache answered true ("assume it's a
// Key B CA"), which refused the static exemption for every issuer on a
// self-issued cluster until Key B became readable for the first time --
// including the operator's own configured anchors, which have nothing to
// do with Key B.
func (p *Pool) isRegisteredKeyBCA(issuer *x509.Certificate) bool {
	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return false
	}
	snap := p.snapshot.Load()
	if snap == nil || snap.keyBOnly == nil {
		return false
	}
	_, err := issuer.Verify(x509.VerifyOptions{Roots: snap.keyBOnly, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

// VerifyIssuer reports whether a verified chain (leaf first) may
// authenticate as tenantID. Its DIRECT issuer, chain[1], may be the cluster CA
// (Key A, any tenant), a CA already in the listener's own static ClientCAs
// (see IsStaticallyTrusted), or tenantID's own registered Key B CA
// (CertSource.IsTenantIssuer, a load-only, byte-identical-DER check). Or the
// chain may contain one of tenantID's registered trust anchors at any depth
// above the leaf (trustanchor.ChainContainsAnchor), read fresh, so an anchor
// vouches for its own tenant only and a withdrawn one stops vouching at once.
// Returns *IssuerNotTrustedError when it is none of those, or when a check
// could not be completed.
func (p *Pool) VerifyIssuer(ctx context.Context, tenantID string, chain []*x509.Certificate) error {
	if len(chain) < 2 {
		return &IssuerNotTrustedError{TenantID: tenantID}
	}
	issuer := chain[1]
	if p.IsClusterCA(ctx, issuer) {
		return nil
	}
	if p.IsStaticallyTrusted(issuer) {
		return nil
	}
	keyBErr := p.verifyKeyBIssuer(ctx, tenantID, issuer)
	if keyBErr == nil {
		return nil
	}
	if ab := p.anchors.Load(); ab != nil {
		ok, err := trustanchor.ChainContainsAnchor(ctx, ab.src, tenantID, chain)
		if err != nil {
			return &IssuerNotTrustedError{TenantID: tenantID, Err: err}
		}
		if ok {
			return nil
		}
	}
	return keyBErr
}

// VerifyTenantIssuer reports whether issuer is tenantID's own registered
// Key B CA and nothing else: unlike VerifyIssuer it admits neither the
// cluster CA, nor a static ClientCAs entry, nor a trust anchor. It is the
// check a tenant-issued delegation is held to (pkg/auth/delegation): only
// the tenant's own CA speaks for the tenant, so a delegation the platform's
// CA, an operator's CA or an enterprise root signed is refused. Returns
// *IssuerNotTrustedError otherwise.
func (p *Pool) VerifyTenantIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error {
	if issuer == nil {
		return &IssuerNotTrustedError{TenantID: tenantID}
	}
	return p.verifyKeyBIssuer(ctx, tenantID, issuer)
}

// verifyKeyBIssuer is VerifyIssuer's Key B branch.
func (p *Pool) verifyKeyBIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error {
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
// (cfg.ClientCAs, read on every connection via the closure below -- the
// WIDENING decision uses the live field, so an operator rotating
// additional client CA files is picked up without re-wiring) plus every
// tenant's Key B CA certificate, from the snapshot Run last built -- see
// build for exactly how SetClusterCA's presence, absence, or failure each
// affect this. cfg.ClientCAs is ALSO captured once, right here, as a fixed
// snapshot for IsStaticallyTrusted's exemption specifically -- see its own
// doc comment for why that check needs a stable, Wire-time value rather
// than the live field the widening decision reads.
//
// Call once per *tls.Config, after cfg.ClientCAs already holds its Key-A
// baseline, and before any listener sharing it starts.
// A snapshot that grants no widening makes the installed hook return the
// base config unmodified -- cfg itself (equivalent to crypto/tls's own "no
// override" convention) when Wire composed with no prior hook, or whatever
// a prior composed hook itself returned otherwise -- the correct behavior
// before SetKeyBProvider has been called and a rebuild has run, on a
// reseal (CACertPEMs then fails: see docs/certificate-authority/
// architecture.md's "while sealed, Key B isn't available" rule), and for
// a CertProvider that does not implement CertSource.
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
		return p.configForClient(base), nil
	}
}

// configForClient builds the per-connection config GetConfigForClient
// returns, or base unchanged when no current snapshot grants widening. It
// never reads storage and never waits (go-qrdb#713): a store that has
// stalled -- no Raft leader, a reseal -- cannot hold a handshake past the
// listener's deadline, the handshake simply keeps the last snapshot built.
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
func (p *Pool) configForClient(base *tls.Config) *tls.Config {
	pool := p.widenedFor(base.ClientCAs)
	if pool == nil {
		// base, not nil: base is cfg itself when Wire's caller installed no
		// prior GetConfigForClient hook, so returning it is equivalent to
		// the original (nil, nil) "keep crypto/tls on the unmodified cfg"
		// behavior -- but when Wire composed with an existing hook, base is
		// THAT hook's own returned config, which must survive here
		// untouched rather than being silently discarded.
		return base
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
	return clone
}

// verifyPeerConnection is Wire's VerifyConnection hook: the manual
// verification configForClient's ClientAuth remap makes crypto/tls skip
// automatically. A connection with no presented certificate is left to
// whatever remapped ClientAuth mode decided (an optional mode lets it
// through; a required one never reaches here with an empty list at all,
// since crypto/tls refuses the handshake before VerifyConnection runs).
//
// ExtKeyUsageClientAuth, not Any (go-qrdb#591's review, blocker 3): this
// IS the EKU policy decision for a certificate presented AS a client
// identity, so a leaf this cluster or an operator issued for some OTHER
// purpose -- serverAuth on a peer's TLS listener, say -- must not also
// authenticate as a client merely because it chains to a CA this Pool
// trusts. IsStaticallyTrusted and isRegisteredKeyBCA stay on Any: they
// decide whether an ISSUER is trusted at all, not whether a presented
// LEAF's own EKU permits the role it is being used in.
func (p *Pool) verifyPeerConnection(cs tls.ConnectionState, pool *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return nil
	}
	intermediates := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	// A tenant-issued delegation carries a critical extension crypto/x509
	// does not handle; the authenticator enforces it (pkg/auth), so the
	// chain is verified as the handled copy.
	_, err := delegation.Verifiable(cs.PeerCertificates[0]).Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return &PeerCertificateNotTrustedError{Err: err}
	}
	return nil
}

// VerifyPeerChain verifies leaf (with optional intermediates) against
// this Pool's current widened trust pool -- the SAME verification
// verifyPeerConnection performs during the TLS handshake, including the
// same ExtKeyUsageClientAuth EKU policy (go-qrdb#591's review, blocker 3)
// -- for a caller that needs to re-derive the chain crypto/tls itself no
// longer builds: ClientAuth's remap in configForClient means
// tls.ConnectionState.VerifiedChains is never populated for a connection
// this Pool wired, so pkg/auth's authenticator (every request, not merely
// the handshake -- see its own doc comment on why) calls this instead
// whenever VerifiedChains comes back empty on a connection Wire touched,
// rather than treating an unverified PeerCertificates list as good enough.
// Returns the verified chain (leaf first) on success.
//
// Like the handshake, it reads the snapshot and never storage, so a
// stalled store cannot hold a request either; ctx is accepted for the
// auth layer's interface and unused.
//
// A snapshot that grants no widening (Key B unavailable -- sealed, or any
// other failed rebuild) does NOT refuse every leaf outright (go-qrdb#591's
// review, re-review blocker 2 regression). configForClient hits this exact
// state on a FRESH handshake and falls back to cfg unmodified, which
// leaves crypto/tls to verify that new connection against cfg's own
// static ClientCAs (this listener's baseline, already carrying Key A for
// every composer -- see Wire) with no Key B widening at all; this fallback
// reproduces that SAME trust decision for a long-lived connection
// re-verified here instead of at a fresh handshake, so a connection
// accepted while Key B was healthy keeps authenticating for a Key A or
// operator-trusted leaf across a reseal, exactly as a new connection made
// in that same sealed state would. Only a Key B-issued leaf -- never part
// of the static baseline -- is refused once Key B stops resolving.
func (p *Pool) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	if leaf == nil {
		return nil, &PeerCertificateNotTrustedError{}
	}
	pool := p.widenedFor(p.staticBase.Load())
	if pool == nil {
		pool = p.fallbackClientCAs()
	}
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	// The handled copy, as verifyPeerConnection verifies: the returned
	// chain's leaf is that copy, and pkg/auth reads the delegation from it.
	chains, verifyErr := delegation.Verifiable(leaf).Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		CurrentTime:   p.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if verifyErr != nil {
		return nil, &PeerCertificateNotTrustedError{Err: verifyErr}
	}
	return chains[0], nil
}

// fallbackClientCAs returns the pool a FRESH handshake would be verified
// against when no current snapshot grants widening: this listener's own
// static ClientCAs baseline (p.staticBase, captured once at Wire -- see
// IsStaticallyTrusted) plus the cluster CA, when the latest rebuild
// resolved one and is younger than snapshotMaxAge. It never includes
// Key B: that is exactly the trust the snapshot withholds, and the only
// thing this fallback must NOT grant. See VerifyPeerChain's doc comment
// for why this must match configForClient's own unmodified-cfg fallback
// rather than refusing every leaf.
func (p *Pool) fallbackClientCAs() *x509.CertPool {
	pool := x509.NewCertPool()
	if base := p.staticBase.Load(); base != nil {
		pool = base.Clone()
	}
	if snap := p.snapshot.Load(); snap != nil && snap.clusterCA != nil && time.Since(snap.built) < snapshotMaxAge {
		pool.AddCert(snap.clusterCA)
	}
	return pool
}

// anchorBundles returns every tenant's trust-anchor bundle for the widened
// pool. A failure to enumerate the tenants omits every anchor, and a failure
// to read one tenant's anchors omits that tenant's: an anchor the pool cannot
// read is not trusted, and Key B trust does not depend on it. Either omission
// lasts only until the next rebuild.
func (p *Pool) anchorBundles(ctx context.Context) [][]byte {
	ab := p.anchors.Load()
	if ab == nil || ab.tenants == nil {
		return nil
	}
	ids, err := ab.tenants.TenantIDs(ctx)
	if err != nil {
		return nil
	}
	out := make([][]byte, 0, len(ids))
	for _, id := range ids {
		bundle, err := ab.src.CABundle(ctx, id)
		if err != nil || len(bundle) == 0 {
			continue
		}
		out = append(out, bundle)
	}
	return out
}
