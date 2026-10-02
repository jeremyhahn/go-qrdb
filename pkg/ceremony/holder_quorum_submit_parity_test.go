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
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// beginThenBogusSubmit requests a quorum challenge for holder and answers it
// with a signature that does not verify, as a caller without the holder's
// key would.
func beginThenBogusSubmit(t *testing.T, e *holderEnv, holder string, share []byte) error {
	t.Helper()
	ctx := context.Background()
	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, holder, "")
	require.NoError(t, err)
	_, err = e.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: holder,
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: []byte("not a signature")},
		Share: share,
	})
	return err
}

// TestQuorum_BeginThenBogusSubmitIsIdenticalForHolderAndStranger: a caller who
// requests a challenge and answers it without the key learns nothing about
// whether the name is a holder. The decoy is stored like a real challenge, so
// the bogus answer runs the same signature verification and fails the same
// way, and the refusal that crosses the transport boundary is one fixed,
// opaque message of one class whoever it names.
func TestQuorum_BeginThenBogusSubmitIsIdenticalForHolderAndStranger(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	holderErr := beginThenBogusSubmit(t, e, "alice", a.openShare(t))
	strangerErr := beginThenBogusSubmit(t, e, "mallory", []byte("share"))
	require.Error(t, holderErr)
	require.Error(t, strangerErr)
	require.Equal(t, holderErr.Error(), strangerErr.Error(), "the refusal text tells a holder from a stranger")

	var holderAuth, strangerAuth *seal.HolderAuthenticationError
	require.ErrorAs(t, holderErr, &holderAuth)
	require.ErrorAs(t, strangerErr, &strangerAuth)
	require.Equal(t, holderAuth.Err.Error(), strangerAuth.Err.Error(),
		"a stored decoy fails the same verification a real challenge does")
}

// TestQuorum_NonMTLSDecoyHasTheFactorsShape: a stranger asked for with a
// non-mtls factor gets a decoy issued by that factor (it derives decoys,
// authfactors.DecoyEnroller), so the challenge has exactly the method data a
// real holder's challenge of that factor has, and the same public key on
// every request.
func TestQuorum_NonMTLSDecoyHasTheFactorsShape(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.initShamir(seal.FactorPolicyAnyEnrolled, newHolder(t, "alice", "").withAuthenticator(t), newHolder(t, "bob", ""))

	real, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", testFactorName)
	require.NoError(t, err)
	decoy, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", testFactorName)
	require.NoError(t, err)
	again, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", testFactorName)
	require.NoError(t, err)

	keys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, keys(real.MethodData), keys(decoy.MethodData), "the decoy's method data has another factor's shape")
	require.Equal(t, real.Factor, decoy.Factor)
	require.Equal(t, decoy.MethodData[factortest.ParamPublicKey], again.MethodData[factortest.ParamPublicKey])
	require.NotEqual(t, real.MethodData[factortest.ParamPublicKey], decoy.MethodData[factortest.ParamPublicKey])
}

// TestQuorum_DecoySurvivesRestart: a stranger's decoy shows the same public
// material after a restart, as a real holder's challenge does: the decoy seed
// is persisted with the roster, not drawn per process.
func TestQuorum_DecoySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.initShamir("", newHolder(t, "alice", ""), newHolder(t, "bob", ""))
	before, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", "")
	require.NoError(t, err)
	_, svc2 := e.restart(2, 2)
	after, err := svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "mallory", "")
	require.NoError(t, err)
	bk, bt := publicFace(before)
	ak, at := publicFace(after)
	require.Equal(t, bk, ak, "the decoy key changed across a restart")
	require.Equal(t, bt, at, "the decoy thumbprint changed across a restart")
}
