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

package ceremony

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestClaim_InProcessBindsToTheEnrolledCertificate pins the in-process
// claim: with no TLS connection to carry a certificate, the claim is bound to
// the holder's enrolled certificate and released only for a signature over
// the fresh claim nonce made with that certificate's key -- the proof the
// mtls quorum factor verifies.
func TestClaim_InProcessBindsToTheEnrolledCertificate(t *testing.T) {
	ctx := WithInProcessClaim(context.Background())
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	// Another holder's key does not release alice's envelope.
	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", "")
	require.NoError(t, err, "an in-process claim needs no TLS certificate")
	wrongSig, err := SignShareClaim(b.certKey, ScopeSystem.ClaimLabel(), "alice", ch.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), wrongSig, "")
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)

	// The holder's own key does, once.
	ch, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", "")
	require.NoError(t, err)
	sig, err := SignShareClaim(a.certKey, ScopeSystem.ClaimLabel(), "alice", ch.Nonce)
	require.NoError(t, err)
	res, err := e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), sig, "")
	require.NoError(t, err)
	a.envelope = res.Envelope
	assert.NotEmpty(t, a.openShare(t))

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", "")
	assert.ErrorIs(t, err, ErrNoPendingShare, "an envelope is delivered once")
}

// TestClaim_InProcessStillChecksAPresentedCertificate pins that the
// in-process binding applies only when no certificate is presented: one that
// is presented must still be the holder's own.
func TestClaim_InProcessStillChecksAPresentedCertificate(t *testing.T) {
	ctx := WithInProcessClaim(context.Background())
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", b.thumb)
	assert.ErrorIs(t, err, ErrCertThumbprintMismatch)
}

// TestClaim_NetworkCallerWithoutCertificateIsRefused pins that a caller not
// marked in-process gets no claim without its certificate: the network
// transports keep their TLS check.
func TestClaim_NetworkCallerWithoutCertificateIsRefused(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", "")
	assert.ErrorIs(t, err, ErrCertThumbprintMismatch)

	// A nonce issued over a certificate cannot be completed in-process
	// without one: the marker binds, it does not bypass.
	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	sig, err := SignShareClaim(b.certKey, ScopeSystem.ClaimLabel(), "alice", ch.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(WithInProcessClaim(ctx), seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), sig, "")
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// TestInProcessClaim_Marker pins the marker's own semantics.
func TestInProcessClaim_Marker(t *testing.T) {
	assert.False(t, InProcessClaim(context.Background()))
	assert.True(t, InProcessClaim(WithInProcessClaim(context.Background())))
}
