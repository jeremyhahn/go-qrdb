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

package ceremony

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// withVerifiedPeerCert is ctx carrying a TLS connection whose certificate,
// h's, the TLS stack verified against the node's client CA.
func withVerifiedPeerCert(t *testing.T, ctx context.Context, h *testHolder) context.Context {
	t.Helper()
	cert, err := mtls.ParseCertificatePEM(h.certPEM)
	require.NoError(t, err)
	return auth.WithTLSState(ctx, &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	})
}

// TestQuorum_UnverifiedCertificateIsAnonymous: a certificate the TLS stack
// did not verify is anyone's to mint, so it must not buy a throttle bucket of
// its own; a caller minting a fresh one per request would otherwise never be
// throttled. Its requests draw on the scope's anonymous bucket; a verified
// certificate keys a bucket of its own.
func TestQuorum_UnverifiedCertificateIsAnonymous(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	lim := newRecordingLimiter(100)
	e.svc.SetQuorumLimiter(lim.allow)

	for _, h := range []*testHolder{a, b} {
		_, err := e.svc.BeginQuorum(withPeerCert(t, ctx, h), seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
	}
	_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	require.Len(t, lim.seen, 1, "unverified certificates and no certificate share the anonymous bucket")
	for key, anon := range lim.anon {
		assert.True(t, anon, key)
		assert.Equal(t, 3, lim.seen[key])
	}

	_, err = e.svc.BeginQuorum(withVerifiedPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	assert.Len(t, lim.seen, 2, "a verified certificate keys a bucket of its own")
}

// TestQuorum_UnverifiedCertificateStillBinds: binding a challenge to the
// holder needs only the certificate the connection proved possession of, not
// a verified chain, so an unverified holder certificate still protects the
// holder's challenge from anonymous eviction.
func TestQuorum_UnverifiedCertificateStillBinds(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	bound, err := e.svc.BeginQuorum(withPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	for i := 0; i < 2*maxPendingPerHolderOp; i++ {
		_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
	}
	_, err = submitChallenge(t, e, bound, a)
	require.NoError(t, err)
}
