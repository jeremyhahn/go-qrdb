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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// claimOnClock begins a claim for h, advances clk by wait, and completes it.
func claimOnClock(t *testing.T, e *holderEnv, clk *clocktest.Fake, h *testHolder, wait time.Duration) error {
	t.Helper()
	ctx := context.Background()
	issued := clk.Now()
	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, h.username, h.thumb)
	require.NoError(t, err)
	require.True(t, ch.ExpiresAt.Equal(issued.Add(challengeTTL)), "the challenge expires challengeTTL after the injected now")
	clk.Advance(wait)
	sig, err := SignShareClaim(h.certKey, ScopeSystem.ClaimLabel(), h.username, ch.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
	return err
}

// TestSetClock_ClaimChallengeExpiresOnTheInjectedClock pins the claim
// challenge to the composer's clock: answered at its ExpiresAt it is
// accepted, one tick after it is refused, whatever the wall clock says.
func TestSetClock_ClaimChallengeExpiresOnTheInjectedClock(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(context.Background(), "")
	require.NoError(t, err)
	clk := clocktest.NewAt(time.Now().Add(-24 * time.Hour))
	e.svc.SetClock(clk)

	require.NoError(t, claimOnClock(t, e, clk, a, challengeTTL), "a challenge answered at its ExpiresAt is accepted")
	require.ErrorIs(t, claimOnClock(t, e, clk, b, challengeTTL+time.Nanosecond), ErrChallengeExpired,
		"a challenge answered one tick after its ExpiresAt is refused")
}

// TestSetClock_ReachesHolderDeps pins that the installed clock is carried to
// every holder authority built from HolderDeps, and that nil keeps the real
// one.
func TestSetClock_ReachesHolderDeps(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	clk := clocktest.New()
	e.svc.SetClock(clk)
	deps := e.svc.HolderDeps()
	require.Same(t, clk, deps.Clock)
	require.True(t, deps.now().Equal(clk.Now()))

	e.svc.SetClock(nil)
	require.Nil(t, e.svc.HolderDeps().Clock)
	var none *HolderDeps
	for _, d := range []*HolderDeps{none, {}} {
		require.WithinDuration(t, time.Now(), d.now(), time.Minute, "nil keeps the real clock")
	}
}
