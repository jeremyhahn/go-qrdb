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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLeaderChecker is a minimal LeaderChecker test double.
type fakeLeaderChecker struct {
	isLeader   bool
	leaderAddr string
	err        error
}

func (f fakeLeaderChecker) IsLeader() (bool, string, error) {
	return f.isLeader, f.leaderAddr, f.err
}

// TestCeremonyServiceWithLeaderGate_RefusesOnAFollower covers the refusal:
// a follower's CeremonyInitialize is refused with
// *AdminCredentialNotLeaderError naming the current leader, and nothing
// reaches the wrapped servicer.
func TestCeremonyServiceWithLeaderGate_RefusesOnAFollower(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	gated := NewCeremonyServiceWithLeaderGate(inner, fakeLeaderChecker{isLeader: false, leaderAddr: "127.0.0.1:63002"})

	_, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
	})
	var notLeader *AdminCredentialNotLeaderError
	require.ErrorAs(t, err, &notLeader)
	assert.Equal(t, "127.0.0.1:63002", notLeader.LeaderAddress)
	assert.Contains(t, notLeader.Error(), "127.0.0.1:63002")

	status, serr := inner.CeremonyStatus(context.Background())
	require.NoError(t, serr)
	assert.Equal(t, "awaiting_init", status.State, "a refused follower call must not have initialized anything")
}

// TestCeremonyServiceWithLeaderGate_RefusesWithNoLeaderKnownYet covers the
// message when no leader has been elected yet: LeaderAddress is empty and
// the error says so rather than naming an empty address.
func TestCeremonyServiceWithLeaderGate_RefusesWithNoLeaderKnownYet(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	gated := NewCeremonyServiceWithLeaderGate(inner, fakeLeaderChecker{isLeader: false, leaderAddr: ""})

	_, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
	})
	var notLeader *AdminCredentialNotLeaderError
	require.ErrorAs(t, err, &notLeader)
	assert.Contains(t, notLeader.Error(), "no leader is known yet")
}

// TestCeremonyServiceWithLeaderGate_DelegatesOnTheLeader covers the success
// path: the leader's CeremonyInitialize reaches the wrapped servicer
// unchanged.
func TestCeremonyServiceWithLeaderGate_DelegatesOnTheLeader(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	gated := NewCeremonyServiceWithLeaderGate(inner, fakeLeaderChecker{isLeader: true})

	result, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.AdminCertPEM)
}

// TestCeremonyServiceWithLeaderGate_PropagatesALeadershipLookupError covers
// the fail-closed branch: an error determining leadership at all is
// propagated unchanged, never treated as "go ahead".
func TestCeremonyServiceWithLeaderGate_PropagatesALeadershipLookupError(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	cause := errors.New("shard not loaded")
	gated := NewCeremonyServiceWithLeaderGate(inner, fakeLeaderChecker{err: cause})

	_, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
	})
	require.ErrorIs(t, err, cause)
}

// TestCeremonyServiceWithLeaderGate_ComposesOverAdminRebind covers the
// production composition order: the leader gate refuses a retry attempt on
// a follower before CeremonyServiceWithAdminRebind's own reissue logic ever
// runs.
func TestCeremonyServiceWithLeaderGate_ComposesOverAdminRebind(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{err: errors.New("no binding")}
	rebound := NewCeremonyServiceWithAdminRebind(inner, core, unbound)
	gated := NewCeremonyServiceWithLeaderGate(rebound, fakeLeaderChecker{isLeader: true, leaderAddr: "127.0.0.1:63001"})

	// First call (leader, uninitialized): succeeds through both decorators.
	first, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.AdminCertPEM)

	// Second call, now on a follower: refused by the leader gate, never
	// reaching the admin-rebind retry logic underneath.
	gatedFollower := NewCeremonyServiceWithLeaderGate(rebound, fakeLeaderChecker{isLeader: false, leaderAddr: "127.0.0.1:63001"})
	_, err = gatedFollower.CeremonyInitialize(context.Background(), ca, nil)
	var notLeader *AdminCredentialNotLeaderError
	require.ErrorAs(t, err, &notLeader)
}
