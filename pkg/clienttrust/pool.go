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
// daemon, the SDK node builders, and the qrdb_embedded Wails composer)
// wires onto its own *tls.Config, rather than each keeping its own copy of
// this logic.
//
// See docs/certificate-authority/architecture.md, "Key B trust on the
// client-facing listeners": a presented client certificate must verify
// against Key A (the cluster CA, trusted statically) plus every tenant's
// Key B CA certificate (the login CA, read fresh on every connection so a
// CA created -- or rotated to an HSM-backed one -- after the listener
// started is picked up without a restart).
package clienttrust

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// CertSource is the capability DefaultCertProvider.CACertPEMs provides:
// every tenant's Key B CA certificate (or, with an injected cakey.Provider,
// the single one that replaces them). It is declared here, rather than
// added to cert.CertProvider, because that interface is what an embedder's
// own CertProvider replacement (server.WithCertProvider) must satisfy, and
// multi-tenant enumeration is not part of its contract -- a type assertion
// against this shape is how Pool asks for the capability without widening
// the interface every CertProvider implementation must carry.
type CertSource interface {
	CACertPEMs(ctx context.Context) ([][]byte, error)
}

// Pool holds the dynamic Key B trust state behind a tls.Config.
// GetConfigForClient hook installed by Wire. The zero value is not usable;
// construct with New.
type Pool struct {
	provider atomic.Pointer[providerBox]
}

// providerBox boxes a cert.CertProvider so it can be held in an
// atomic.Pointer, which needs a concrete pointee -- an interface value is
// not one.
type providerBox struct {
	prov cert.CertProvider
}

// New returns a Pool with no Key B provider installed yet: until
// SetKeyBProvider is called, every connection sees the listener's static
// ClientCAs pool unchanged (Key A only), which is the correct pre-unseal
// state.
func New() *Pool {
	return &Pool{}
}

// SetKeyBProvider installs the CertProvider the pool reads Key B's CA
// certificates from. Call it once, as soon as the CertProvider exists --
// which is after lifecycle.BuildServices runs, meaning after the barrier is
// first unsealed. Safe to call concurrently with a live GetConfigForClient
// hook: the next connection sees the newly installed provider. A nil prov
// (or one that does not implement CertSource) reverts the pool to Key A
// only.
func (p *Pool) SetKeyBProvider(prov cert.CertProvider) {
	p.provider.Store(&providerBox{prov: prov})
}

// Wire installs cfg.GetConfigForClient, so that every client-facing
// listener using cfg verifies a presented client certificate against Key A
// (cfg.ClientCAs, read fresh on every connection -- NOT captured here --
// since the caller may still be finishing TLS setup, including the
// ClientCAs assignment itself, when Wire runs) plus every tenant's Key B CA
// certificate, read from whatever provider SetKeyBProvider most recently
// installed.
//
// Call once per *tls.Config, before any listener sharing it starts.
// Returning (nil, nil) from the installed hook keeps crypto/tls on the
// original, unmodified cfg -- the correct behavior before the first
// SetKeyBProvider call, on a CACertPEMs failure (the ordinary outcome while
// sealed in Raft mode, where the replicated store cannot be opened at all),
// and for a CertProvider that does not implement CertSource.
//
// The peer/Raft/enrollment transport must never call Wire on its own TLS
// config: it is meant to trust the cluster CA (Key A) only, never Key B, so
// its trust pool is built independently, directly from the cluster CA
// certificate.
func (p *Pool) Wire(cfg *tls.Config) {
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return p.configForClient(cfg)
	}
}

// configForClient builds the per-connection config GetConfigForClient
// returns, or (nil, nil) to keep base unchanged.
func (p *Pool) configForClient(base *tls.Config) (*tls.Config, error) {
	box := p.provider.Load()
	if box == nil || box.prov == nil {
		return nil, nil
	}
	src, ok := box.prov.(CertSource)
	if !ok {
		return nil, nil
	}
	pems, err := src.CACertPEMs(context.Background())
	if err != nil || len(pems) == 0 {
		// Sealed (the ordinary cause in Raft mode, where the replicated
		// store cannot be opened while sealed), or no tenant has
		// established Key B yet: fall back to the static config, not an
		// error, so a Key A holder keeps authenticating through a reseal.
		return nil, nil
	}

	pool := x509.NewCertPool()
	if base.ClientCAs != nil {
		pool = base.ClientCAs.Clone()
	}
	for _, p := range pems {
		pool.AppendCertsFromPEM(p)
	}
	clone := base.Clone()
	clone.ClientCAs = pool
	// crypto/tls invokes GetConfigForClient at most once per handshake, so
	// this is not re-entered for the connection being set up; nil it anyway
	// so the clone is never mistaken for a second, live hook.
	clone.GetConfigForClient = nil
	return clone, nil
}
