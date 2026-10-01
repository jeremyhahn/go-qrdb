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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestQuorum_AnswerIsBoundToScopeDomainAndOp: a quorum challenge carries the
// purpose it was issued for -- scope, key domain, operation -- and the mtls
// answer signs it with the challenge ID. An answer signed over the bare
// nonce, or for another operation's purpose, does not verify, whoever's key
// made it; only the answer for exactly this challenge does.
func TestQuorum_AnswerIsBoundToScopeDomainAndOp(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	decoy, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", "")
	require.NoError(t, err)
	assert.Equal(t, "quorum:system:unseal:seal", decoy.MethodData[mtls.MethodDataPurpose], "a decoy carries the purpose too")

	for name, sign := range map[string]func(ch *QuorumChallenge) ([]byte, error){
		"the bare nonce": func(ch *QuorumChallenge) ([]byte, error) {
			return mtls.SignNonce(a.certKey, ch.Nonce)
		},
		"another operation's purpose": func(ch *QuorumChallenge) ([]byte, error) {
			return mtls.SignChallenge(a.certKey, ch.ChallengeID, "quorum:system:unseal:root-token", ch.Nonce)
		},
		"another scope's purpose": func(ch *QuorumChallenge) ([]byte, error) {
			return mtls.SignChallenge(a.certKey, ch.ChallengeID, "quorum:recovery:unseal:seal", ch.Nonce)
		},
	} {
		ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
		require.Equal(t, "quorum:system:unseal:seal", ch.MethodData[mtls.MethodDataPurpose])
		sig, err := sign(ch)
		require.NoError(t, err)
		_, err = e.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: "alice",
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig},
			Share: a.openShare(t),
		})
		var authErr *seal.HolderAuthenticationError
		require.ErrorAs(t, err, &authErr, name)
	}

	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	out, err := submitChallenge(t, e, ch, a)
	require.NoError(t, err)
	require.Equal(t, 1, out.Submitted)
}
