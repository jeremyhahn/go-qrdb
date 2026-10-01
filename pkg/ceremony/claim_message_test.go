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
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestClaim_SignatureIsDomainSeparated: the claim proof is a signature by
// the holder's certificate key, and so is the mtls quorum factor's answer.
// Over the same raw bytes, a signature obtained for one could be presented
// for the other. The claim therefore signs a message of its own --
// "qrdb/share-claim/v1", the scope, the holder and the nonce -- and a
// signature over the bare nonce, or over another scope's or holder's claim
// message, is refused.
func TestClaim_SignatureIsDomainSeparated(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	label := ScopeSystem.ClaimLabel()
	for name, sign := range map[string]func(nonce []byte) ([]byte, error){
		"the bare nonce": func(nonce []byte) ([]byte, error) {
			return mtls.SignNonce(a.certKey, nonce)
		},
		"another scope's claim": func(nonce []byte) ([]byte, error) {
			return SignShareClaim(a.certKey, ScopeRecovery.ClaimLabel(), "alice", nonce)
		},
		"another holder's claim": func(nonce []byte) ([]byte, error) {
			return SignShareClaim(a.certKey, label, "bob", nonce)
		},
	} {
		ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
		require.NoError(t, err)
		sig, err := sign(ch.Nonce)
		require.NoError(t, err)
		_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), sig, a.thumb)
		assert.ErrorIs(t, err, ErrChallengeVerificationFailed, name)
	}

	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	sig, err := SignShareClaim(a.certKey, label, "alice", ch.Nonce)
	require.NoError(t, err)
	res, err := e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), sig, a.thumb)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Envelope)
}

// TestShareClaimMessage_Encoding pins the signed message: the context, the
// scope label, the holder and the nonce, NUL-separated, so no two distinct
// claims share a message.
func TestShareClaimMessage_Encoding(t *testing.T) {
	nonce := []byte{1, 2, 3}
	msg := ShareClaimMessage("database:orders", "alice", nonce)
	want := append([]byte("qrdb/share-claim/v1\x00database:orders\x00alice\x00"), nonce...)
	assert.Equal(t, want, msg)
	assert.False(t, bytes.Equal(ShareClaimMessage("system", "alice", nonce), ShareClaimMessage("recovery", "alice", nonce)))

	assert.Equal(t, "system", ScopeSystem.ClaimLabel())
	assert.Equal(t, "recovery", ScopeRecovery.ClaimLabel())
	assert.Equal(t, "tenant:acme", ScopeTenant("acme").ClaimLabel())
	assert.Equal(t, "database:orders", ScopeDatabase("acme", "orders").ClaimLabel())
	assert.Equal(t, ScopeDatabase("acme", "orders").ClaimLabel(), DatabaseClaimLabel("orders"))
}
