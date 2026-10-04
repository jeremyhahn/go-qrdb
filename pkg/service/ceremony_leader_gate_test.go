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
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
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
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
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
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
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
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
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
	rebound := NewCeremonyServiceWithAdminRebind(inner, core, unbound, nil, nil)
	gated := NewCeremonyServiceWithLeaderGate(rebound, fakeLeaderChecker{isLeader: true, leaderAddr: "127.0.0.1:63001"})

	// First call (leader, uninitialized): succeeds through both decorators.
	first, err := gated.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
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

// TestHostLeaderChecker_ReportsLeadership covers the three outcomes
// HostLeaderChecker.IsLeader derives from Host.GetLeaderID (go-qrdb#548
// review item 6): this replica is the leader, another replica is and its
// address is known from Members, and no leader is known yet.
func TestHostLeaderChecker_ReportsLeadership(t *testing.T) {
	cases := map[string]struct {
		getLeaderIDFn func(uint64) (uint64, bool, error)
		replicaID     uint64
		members       map[uint64]string
		wantLeader    bool
		wantAddr      string
		wantErr       error
	}{
		"this replica is the leader": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 1, true, nil },
			replicaID:     1,
			wantLeader:    true,
		},
		"another replica is the leader, address known": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 2, true, nil },
			replicaID:     1,
			members:       map[uint64]string{2: "127.0.0.1:63002"},
			wantLeader:    false,
			wantAddr:      "127.0.0.1:63002",
		},
		"another replica is the leader, address unknown": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 2, true, nil },
			replicaID:     1,
			wantLeader:    false,
			wantAddr:      "",
		},
		"no leader known yet": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 0, false, nil },
			replicaID:     1,
			wantLeader:    false,
			wantAddr:      "",
		},
		"the host could not answer": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 0, false, errors.New("shard not loaded") },
			replicaID:     1,
			wantLeader:    false,
			wantErr:       errors.New("shard not loaded"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			checker := HostLeaderChecker{
				Host:      &stubLeaderHost{getLeaderIDFn: tc.getLeaderIDFn},
				ShardID:   1,
				ReplicaID: tc.replicaID,
				Members:   tc.members,
			}
			isLeader, addr, err := checker.IsLeader()
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.Equal(t, tc.wantErr.Error(), err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantLeader, isLeader)
			assert.Equal(t, tc.wantAddr, addr)
		})
	}
}

// stubLeaderHost satisfies only the slice of Host HostLeaderChecker calls,
// embedding the full interface so the type still compiles against it --
// every unimplemented method panics if ever called, which would fail the
// test loudly rather than silently returning a zero value.
type stubLeaderHost struct {
	Host
	getLeaderIDFn func(shardID uint64) (uint64, bool, error)
}

func (s *stubLeaderHost) GetLeaderID(shardID uint64) (uint64, bool, error) {
	return s.getLeaderIDFn(shardID)
}

// TestComposeCeremonyServiceWithAdminCredentialWiring_NilPassthrough covers
// the two shapes the composer must leave UNCHANGED: a nil ceremonySvc, and
// one whose core cannot be resolved via CeremonyServiceCore.
func TestComposeCeremonyServiceWithAdminCredentialWiring_NilPassthrough(t *testing.T) {
	assert.Nil(t, ComposeCeremonyServiceWithAdminCredentialWiring(nil, nil, nil, nil, nil))

	unresolvable := unresolvableCeremonyServicer{}
	got := ComposeCeremonyServiceWithAdminCredentialWiring(unresolvable, nil, nil, nil, nil)
	assert.Equal(t, unresolvable, got, "a CeremonyServicer whose core cannot be resolved must pass through unchanged")
}

// unresolvableCeremonyServicer is a CeremonyServicer CeremonyServiceCore
// cannot resolve: it is not *CeremonyService, by construction.
type unresolvableCeremonyServicer struct{ CeremonyServicer }

// TestComposeCeremonyServiceWithAdminCredentialWiring_SameOrderAsManual
// proves the shared composer wires the identical two decorators, in the
// identical order, as TestCeremonyServiceWithLeaderGate_ComposesOverAdminRebind
// builds by hand: a follower is refused by the leader gate before the
// admin-rebind retry logic underneath ever runs, and the leader succeeds
// through both.
func TestComposeCeremonyServiceWithAdminCredentialWiring_SameOrderAsManual(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	unbound := &fakeAdminLookup{err: errors.New("no binding")}

	composed := ComposeCeremonyServiceWithAdminCredentialWiring(
		inner, unbound, nil, fakeLeaderChecker{isLeader: true, leaderAddr: "127.0.0.1:63001"}, nil)
	first, err := composed.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.AdminCertPEM)

	composedFollower := ComposeCeremonyServiceWithAdminCredentialWiring(
		inner, unbound, nil, fakeLeaderChecker{isLeader: false, leaderAddr: "127.0.0.1:63001"}, nil)
	_, err = composedFollower.CeremonyInitialize(context.Background(), ca, nil)
	var notLeader *AdminCredentialNotLeaderError
	require.ErrorAs(t, err, &notLeader)
}
