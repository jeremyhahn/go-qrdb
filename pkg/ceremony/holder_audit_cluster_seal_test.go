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
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestHolderAudit_ClusterSealQuorumOneEmitterPerEvent pins that a seal
// quorum routed through the cluster seal (SubmitHolderShareWith, the path a
// share-holder seal quorum takes so the cluster record is written before any
// key is wiped) keeps the holder audit and the barrier's own records, once
// each: one holder-quorum-submit per authenticated holder from the holder
// authority, one quorum-complete from the barrier, no second copy of either,
// and the cluster seal run exactly once, in place of the barrier's own.
func TestHolderAudit_ClusterSealQuorumOneEmitterPerEvent(t *testing.T) {
	ctx := context.Background()
	ring := audit.NewRingBuffer(256)
	strategy, err := seal.NewShamirStrategy(2, 2)
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &seal.ShamirConfig{Threshold: 2, TotalShares: 2},
		AuditLogger: seal.NewComplianceAuditLogger(ring, tenantid.System, seal.SystemBarrierAuditName),
	}, strategy)
	require.NoError(t, err)
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	svc.SetCA(testCA(t))
	svc.SetTrustDomain(testTrustDomain)
	svc.SetAuditLogger(ring)
	holders := []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", "")}
	for _, h := range holders {
		res, err := svc.RegisterOfficer(ctx, seal.DomainUnseal, h.request("admin"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
	}
	_, err = svc.InitializeShamir(ctx, "")
	require.NoError(t, err)
	for _, h := range holders {
		ch, err := svc.BeginClaimShare(ctx, seal.DomainUnseal, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, ScopeSystem.ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, seal.DomainUnseal, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}
	before := len(ring.Snapshot())

	clusterSeals := 0
	clusterSeal := func() error { clusterSeals++; return barrier.Seal() }
	var out *seal.QuorumOutcome
	for _, h := range holders {
		ch, err := svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, h.username, "")
		require.NoError(t, err)
		out, err = barrier.SubmitHolderShareWith(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: h.username,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
			Share: h.openShare(t),
		}, clusterSeal)
		require.NoError(t, err)
	}
	require.True(t, out.Complete)
	require.True(t, barrier.IsSealed())
	assert.Equal(t, 1, clusterSeals, "the cluster seal ran once, in place of the barrier's own")

	counts := map[string]int{}
	for _, ev := range ring.Snapshot()[before:] {
		counts[ev.Action]++
		if ev.Action == ActionHolderQuorumSubmit {
			assert.Equal(t, audit.OutcomeSuccess, ev.Outcome)
			assert.Equal(t, string(seal.OpSeal), ev.Attributes["op"])
		}
	}
	assert.Equal(t, 2, counts[ActionHolderQuorumSubmit], "one holder record per authenticated submission")
	assert.Equal(t, 1, counts["quorum-complete"], "one barrier record for the round")
	assert.Zero(t, counts["quorum-share"], "the barrier records no second copy of a submission")
}
