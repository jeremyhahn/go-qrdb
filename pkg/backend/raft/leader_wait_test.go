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

//go:build !integration

package raft

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

const leaderWaitShard uint64 = 7

// noLeaderRefusal is go-quicraft's refusal on a node that knows no leader.
func noLeaderRefusal() error {
	return &quicraft.NotLeaderError{ShardID: leaderWaitShard}
}

// leaderElsewhereRefusal is go-quicraft's refusal naming a known leader.
func leaderElsewhereRefusal() error {
	return &quicraft.NotLeaderError{ShardID: leaderWaitShard, LeaderID: 2, LeaderAddress: "127.0.0.1:5002"}
}

// electionResolver is a fake leader report: no leader for the first
// unknownPolls polls of GetLeaderID, then replica 2.
type electionResolver struct {
	unknownPolls int64
	polls        atomic.Int64
}

func (r *electionResolver) GetLeaderID(uint64) (uint64, bool, error) {
	if r.polls.Add(1) <= r.unknownPolls {
		return 0, false, nil
	}
	return 2, true, nil
}

// refuseFirst returns an op that answers refusal for its first n calls and
// then ok, counting every call in calls.
func refuseFirst[T any](n int64, calls *atomic.Int64, refusal func() error, ok T) func() (T, error) {
	return func() (T, error) {
		var zero T
		if calls.Add(1) <= n {
			return zero, refusal()
		}
		return ok, nil
	}
}

// electionAdapter is an adapter whose inner host knows no leader for
// unknownPolls polls and refuses the first refusals requests as leaderless.
func electionAdapter(unknownPolls, refusals int64, calls *atomic.Int64) (*HostAdapter, *electionResolver) {
	r := &electionResolver{unknownPolls: unknownPolls}
	mock := &mockQuicraftHost{
		getLeaderIDFn: r.GetLeaderID,
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			return refuseFirst(refusals, calls, noLeaderRefusal, sm.Result{Data: cmd})()
		},
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, noLeaderRefusal, q)()
		},
		followerReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, noLeaderRefusal, q)()
		},
		syncGetShardMembershipFn: func(context.Context, uint64) (*quicraft.Membership, error) {
			return refuseFirst(refusals, calls, noLeaderRefusal, &quicraft.Membership{ConfigChangeID: 7})()
		},
	}
	return newTestAdapter(mock), r
}

func TestHostAdapter_SyncPropose_HoldsThroughLeaderlessMoment(t *testing.T) {
	var calls atomic.Int64
	a, r := electionAdapter(3, 1, &calls)

	res, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	require.NoError(t, err)
	assert.Equal(t, []byte("cmd"), res.Data)
	assert.Equal(t, int64(2), calls.Load(), "proposed once refused, once after the leader was known")
	assert.Equal(t, int64(4), r.polls.Load(), "re-proposed only once a leader was reported")
}

func TestHostAdapter_ProposeWithMode_HoldsThroughLeaderlessMoment(t *testing.T) {
	for _, mode := range []writemode.Mode{writemode.Sync, writemode.WALDurable, writemode.Batched} {
		var calls atomic.Int64
		a, _ := electionAdapter(2, 1, &calls)

		res, err := a.ProposeWithMode(context.Background(), leaderWaitShard, []byte("cmd"), mode)
		require.NoError(t, err, "mode %v", mode)
		assert.Equal(t, []byte("cmd"), res.Data)
		assert.Equal(t, int64(2), calls.Load())
	}
}

func TestHostAdapter_SyncRead_HoldsThroughLeaderlessMoment(t *testing.T) {
	var calls atomic.Int64
	a, _ := electionAdapter(2, 1, &calls)

	got, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err)
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_FollowerRead_HoldsThroughLeaderlessMoment(t *testing.T) {
	var calls atomic.Int64
	a, _ := electionAdapter(2, 1, &calls)

	got, err := a.FollowerRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err)
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(2), calls.Load())
}

// The committed membership is read with a ReadIndex, so a membership change
// that has just removed the leader refuses it until the shard elects another.
func TestHostAdapter_SyncGetShardMembership_HoldsThroughLeaderlessMoment(t *testing.T) {
	var calls atomic.Int64
	a, _ := electionAdapter(2, 1, &calls)

	m, err := a.SyncGetShardMembership(context.Background(), leaderWaitShard)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, uint64(7), m.ConfigChangeID)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_ReadWithConsistency_HoldsThroughLeaderlessMoment(t *testing.T) {
	levels := []consistency.ReadOptions{
		{Consistency: consistency.Linearizable},
		{Consistency: consistency.LeaseRead},
		{Consistency: consistency.FollowerRead},
		{},
	}
	for _, opts := range levels {
		var calls atomic.Int64
		a, _ := electionAdapter(1, 1, &calls)

		got, err := a.ReadWithConsistency(context.Background(), leaderWaitShard, "q", opts)
		require.NoError(t, err, "consistency %v", opts.Consistency)
		assert.Equal(t, "q", got)
		assert.Equal(t, int64(2), calls.Load())
	}
}

func TestHostAdapter_SyncPropose_LeaderWaitIsBounded(t *testing.T) {
	var calls atomic.Int64
	a, _ := electionAdapter(1<<40, 1<<40, &calls)
	a.leaderWait = 60 * time.Millisecond

	start := time.Now()
	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	elapsed := time.Since(start)

	var lwe *LeaderWaitError
	require.ErrorAs(t, err, &lwe)
	assert.Equal(t, leaderWaitShard, lwe.ShardID)
	assert.GreaterOrEqual(t, lwe.Waited, a.leaderWait)
	var nle *quicraft.NotLeaderError
	require.ErrorAs(t, err, &nle, "the not-leader refusal stays classifiable")
	assert.Zero(t, nle.LeaderID)
	assert.GreaterOrEqual(t, elapsed, a.leaderWait)
	assert.Less(t, elapsed, 5*time.Second)
	assert.Equal(t, int64(1), calls.Load(), "never re-proposed while no leader was reported")
}

func TestHostAdapter_SyncRead_LeaderWaitStopsAtContextDeadline(t *testing.T) {
	var calls atomic.Int64
	a, _ := electionAdapter(1<<40, 1<<40, &calls)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.SyncRead(ctx, leaderWaitShard, "q")

	var lwe *LeaderWaitError
	require.ErrorAs(t, err, &lwe)
	assert.Less(t, time.Since(start), 5*time.Second, "the caller's deadline bounds the wait, not the 10s default")
}

func TestHostAdapter_SyncPropose_LeaderElsewhereIsNotHeld(t *testing.T) {
	var calls atomic.Int64
	r := &electionResolver{}
	a := newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: r.GetLeaderID,
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			calls.Add(1)
			return sm.Result{}, leaderElsewhereRefusal()
		},
	})

	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	var nle *quicraft.NotLeaderError
	require.ErrorAs(t, err, &nle)
	assert.Equal(t, uint64(2), nle.LeaderID)
	var lwe *LeaderWaitError
	assert.False(t, errors.As(err, &lwe), "a refusal naming a leader is the caller's to forward")
	assert.Equal(t, int64(1), calls.Load())
	assert.Zero(t, r.polls.Load())
}

func TestHostAdapter_SyncRead_OtherErrorIsNotHeld(t *testing.T) {
	var calls atomic.Int64
	a := newTestAdapter(&mockQuicraftHost{
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			calls.Add(1)
			return nil, quicraft.ErrShardNotFound
		},
	})

	_, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	require.ErrorIs(t, err, quicraft.ErrShardNotFound)
	assert.Equal(t, int64(1), calls.Load())
}

func TestHostAdapter_SyncGetShardMembership_OtherErrorIsNotHeld(t *testing.T) {
	var calls atomic.Int64
	a := newTestAdapter(&mockQuicraftHost{
		syncGetShardMembershipFn: func(context.Context, uint64) (*quicraft.Membership, error) {
			calls.Add(1)
			return nil, quicraft.ErrShardNotFound
		},
	})

	_, err := a.SyncGetShardMembership(context.Background(), leaderWaitShard)
	require.ErrorIs(t, err, quicraft.ErrShardNotFound)
	assert.Equal(t, int64(1), calls.Load())
}

// strandedReadRefusal is go-quicraft's answer to a read a leadership change
// stranded (failLeaderChangeRequests): the new leader is named, its address
// is not.
func strandedReadRefusal() error {
	return &quicraft.NotLeaderError{ShardID: leaderWaitShard, LeaderID: 2}
}

// strandedReadAdapter is an adapter whose reads are stranded by a leadership
// change refusals times and then served, with replica 2 known as leader.
func strandedReadAdapter(refusals int64, calls *atomic.Int64) (*HostAdapter, *electionResolver) {
	r := &electionResolver{}
	return newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: r.GetLeaderID,
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, strandedReadRefusal, q)()
		},
		followerReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, strandedReadRefusal, q)()
		},
	}), r
}

func TestHostAdapter_SyncRead_ReissuedWhenALeaderChangeStrandsIt(t *testing.T) {
	var calls atomic.Int64
	a, _ := strandedReadAdapter(1, &calls)

	got, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err, "a read the leader change stranded is reissued, not handed to a caller with nowhere to forward it")
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_FollowerRead_ReissuedWhenALeaderChangeStrandsIt(t *testing.T) {
	var calls atomic.Int64
	a, _ := strandedReadAdapter(1, &calls)

	got, err := a.FollowerRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err)
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_SyncRead_StrandedReissueIsBounded(t *testing.T) {
	var calls atomic.Int64
	a, _ := strandedReadAdapter(1<<40, &calls)
	a.leaderWait = 60 * time.Millisecond

	start := time.Now()
	_, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	elapsed := time.Since(start)

	var lwe *LeaderWaitError
	require.ErrorAs(t, err, &lwe)
	var nle *quicraft.NotLeaderError
	require.ErrorAs(t, err, &nle, "the not-leader refusal stays classifiable")
	assert.Equal(t, uint64(2), nle.LeaderID)
	assert.GreaterOrEqual(t, elapsed, a.leaderWait)
	assert.Less(t, elapsed, 5*time.Second)
	assert.Greater(t, calls.Load(), int64(1))
}

func TestHostAdapter_SyncPropose_StrandedProposalIsNotReissued(t *testing.T) {
	var calls atomic.Int64
	r := &electionResolver{}
	a := newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: r.GetLeaderID,
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			calls.Add(1)
			return sm.Result{}, strandedReadRefusal()
		},
	})

	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	var nle *quicraft.NotLeaderError
	require.ErrorAs(t, err, &nle)
	assert.Equal(t, uint64(2), nle.LeaderID)
	assert.Equal(t, int64(1), calls.Load(), "a stranded proposal may still commit, so it is never proposed again")
}

func TestHostAdapter_SyncPropose_RefusedAgainAfterLeaderKnown(t *testing.T) {
	// The leader report and go-quicraft's proposal path can disagree for a
	// moment; every re-proposal still waits a poll interval first.
	var calls atomic.Int64
	a, _ := electionAdapter(0, 3, &calls)

	start := time.Now()
	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	require.NoError(t, err)
	assert.Equal(t, int64(4), calls.Load())
	assert.GreaterOrEqual(t, time.Since(start), leaderPollBaseDelay*(1+2+4))
}

func TestHostAdapter_SyncPropose_LeaderLookupErrorIsLeftToOp(t *testing.T) {
	var calls atomic.Int64
	a := newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: func(uint64) (uint64, bool, error) { return 0, false, quicraft.ErrShardNotFound },
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			if calls.Add(1) == 1 {
				return sm.Result{}, noLeaderRefusal()
			}
			return sm.Result{}, quicraft.ErrShardNotFound
		},
	})

	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	require.ErrorIs(t, err, quicraft.ErrShardNotFound)
	assert.Equal(t, int64(2), calls.Load())
}

func TestLeaderUnknown(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"other error", quicraft.ErrShardNotFound, false},
		{"no leader known", noLeaderRefusal(), true},
		{"leader elsewhere", leaderElsewhereRefusal(), false},
		{"leader ID only", &quicraft.NotLeaderError{LeaderID: 3}, false},
		{"leader address only", &quicraft.NotLeaderError{LeaderAddress: "127.0.0.1:5003"}, false},
		{"wrapped", &LeaderWaitError{Err: noLeaderRefusal()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, leaderUnknown(tt.err))
		})
	}
}

func TestLeaderWaitError(t *testing.T) {
	refusal := noLeaderRefusal()
	err := &LeaderWaitError{ShardID: leaderWaitShard, Waited: 2 * time.Second, Err: refusal}

	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "raft: shard 7: no leader known after waiting 2s: "), msg)
	assert.True(t, strings.HasSuffix(msg, refusal.Error()), msg)
	assert.Same(t, refusal, errors.Unwrap(err))
	assert.ErrorIs(t, err, refusal)
}

func TestLeaderWaitError_LeaderNamed(t *testing.T) {
	refusal := strandedReadRefusal()
	err := &LeaderWaitError{ShardID: leaderWaitShard, Waited: 2 * time.Second, Err: refusal}

	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "raft: shard 7: still refused as not leader after waiting 2s: "), msg)
	assert.True(t, strings.HasSuffix(msg, refusal.Error()), msg)
	assert.Same(t, refusal, errors.Unwrap(err))
}

// notReadyAdapter is an adapter whose shard has a known leader that has not
// yet committed an entry in its term: its first `refusals` reads, of every
// kind, answer quicraft.ErrReadIndexNotReady, as go-quicraft does between a
// leader's election and the commit of its no-op.
func notReadyAdapter(refusals int64, calls *atomic.Int64) *HostAdapter {
	r := &electionResolver{}
	notReady := func() error { return quicraft.ErrReadIndexNotReady }
	return newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: r.GetLeaderID,
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, notReady, q)()
		},
		followerReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			return refuseFirst[interface{}](refusals, calls, notReady, q)()
		},
		syncGetShardMembershipFn: func(context.Context, uint64) (*quicraft.Membership, error) {
			return refuseFirst(refusals, calls, notReady, &quicraft.Membership{ConfigChangeID: 7})()
		},
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return refuseFirst(refusals, calls, notReady, sm.Result{Value: 1})()
		},
	})
}

func TestHostAdapter_SyncRead_WaitsForNewLeadersFirstCommit(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(2, &calls)

	got, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err, "a read a new leader cannot serve yet waits for its first commit, not the client")
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(3), calls.Load())
}

func TestHostAdapter_FollowerRead_WaitsForNewLeadersFirstCommit(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(1, &calls)

	got, err := a.FollowerRead(context.Background(), leaderWaitShard, "q")
	require.NoError(t, err)
	assert.Equal(t, "q", got)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_SyncGetShardMembership_WaitsForNewLeadersFirstCommit(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(1, &calls)

	m, err := a.SyncGetShardMembership(context.Background(), leaderWaitShard)
	require.NoError(t, err, "the committed-membership read an admin write makes waits out the election")
	require.NotNil(t, m)
	assert.Equal(t, uint64(7), m.ConfigChangeID)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_SyncGetShardMembership_ReissuedWhenALeaderChangeStrandsIt(t *testing.T) {
	var calls atomic.Int64
	a := newTestAdapter(&mockQuicraftHost{
		getLeaderIDFn: (&electionResolver{}).GetLeaderID,
		syncGetShardMembershipFn: func(context.Context, uint64) (*quicraft.Membership, error) {
			return refuseFirst(1, &calls, strandedReadRefusal, &quicraft.Membership{ConfigChangeID: 7})()
		},
	})

	m, err := a.SyncGetShardMembership(context.Background(), leaderWaitShard)
	require.NoError(t, err, "a membership read is a read: one the leader change stranded is reissued")
	require.NotNil(t, m)
	assert.Equal(t, int64(2), calls.Load())
}

func TestHostAdapter_SyncRead_FirstCommitWaitIsBounded(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(1<<40, &calls)
	a.leaderWait = 60 * time.Millisecond

	start := time.Now()
	_, err := a.SyncRead(context.Background(), leaderWaitShard, "q")
	elapsed := time.Since(start)

	var lwe *LeaderWaitError
	require.ErrorAs(t, err, &lwe)
	require.ErrorIs(t, err, quicraft.ErrReadIndexNotReady, "the not-ready refusal stays classifiable")
	assert.GreaterOrEqual(t, elapsed, a.leaderWait)
	assert.Less(t, elapsed, 5*time.Second)
	assert.Greater(t, calls.Load(), int64(1))
}

func TestHostAdapter_SyncGetShardMembership_FirstCommitWaitStopsAtContextDeadline(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(1<<40, &calls)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.SyncGetShardMembership(ctx, leaderWaitShard)

	var lwe *LeaderWaitError
	require.ErrorAs(t, err, &lwe)
	require.ErrorIs(t, err, quicraft.ErrReadIndexNotReady)
	assert.Less(t, time.Since(start), 5*time.Second, "the caller's deadline bounds the wait, not the 10s default")
}

func TestHostAdapter_SyncPropose_NotReadyIsNotHeld(t *testing.T) {
	var calls atomic.Int64
	a := notReadyAdapter(1<<40, &calls)

	_, err := a.SyncPropose(context.Background(), leaderWaitShard, []byte("cmd"))
	require.ErrorIs(t, err, quicraft.ErrReadIndexNotReady)
	assert.Equal(t, int64(1), calls.Load(), "only reads wait for the first commit; a proposal is never reissued on it")
}

func TestLeaderWaitError_FirstCommitPending(t *testing.T) {
	err := &LeaderWaitError{ShardID: leaderWaitShard, Waited: 2 * time.Second, Err: quicraft.ErrReadIndexNotReady}

	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "raft: shard 7: leader had not committed in its term after waiting 2s: "), msg)
	assert.True(t, strings.HasSuffix(msg, quicraft.ErrReadIndexNotReady.Error()), msg)
	assert.ErrorIs(t, err, quicraft.ErrReadIndexNotReady)
}
