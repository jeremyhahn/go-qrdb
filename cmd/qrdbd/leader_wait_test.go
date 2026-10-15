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

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// replacedLeaderWait is the wall time awaitLeader used to give up after: 30
// polls with a backoff doubling from 50 ms to a 500 ms cap. It is here only so
// the budget tests can say that no configuration waits less than it did.
const replacedLeaderWait = 13250 * time.Millisecond

// TestAwaitLeader_LeaderPastThirtyPollsIsFound is the defect itself: a leader
// that is elected after the thirtieth poll. The old wait counted polls and
// gave up on the thirtieth however much of its time budget remained.
func TestAwaitLeader_LeaderPastThirtyPollsIsFound(t *testing.T) {
	var polls atomic.Int64
	node := &mockRaftNode{
		getLeaderFn: func(uint64) (uint64, bool, error) {
			if polls.Add(1) < 40 {
				return 0, false, nil
			}
			return 1, true, nil
		},
	}

	require.NoError(t, awaitLeader(context.Background(), node, testLeaderWait, 1))
	assert.Equal(t, int64(40), polls.Load())
}

// TestAwaitLeader_CancelStopsTheWaitAtOnce cancels mid-wait under a budget of
// an hour: the wait must end on the cancel, not on the budget.
func TestAwaitLeader_CancelStopsTheWaitAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int64
	node := &mockRaftNode{
		getLeaderFn: func(uint64) (uint64, bool, error) {
			if polls.Add(1) == 3 {
				cancel()
			}
			return 0, false, nil
		},
	}

	start := time.Now()
	err := awaitLeader(ctx, node, time.Hour, 1)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 10*time.Second, "the wait outlived its context")
	assert.Equal(t, int64(3), polls.Load(), "the wait polled again after its context ended")
}

// TestAwaitLeader_BudgetExhaustedNamesShardAndBudget asserts the typed error a
// wait that runs out of budget returns, and that its message carries the
// shard and how long it waited.
func TestAwaitLeader_ExhaustedRoundsDoNotEndTheWait(t *testing.T) {
	// The leader becomes known only after several rounds of the budget have
	// run out -- a quorum unsealed long after this node. Each exhausted round
	// is logged and the wait goes on, so the leader is still found.
	const budget = 50 * time.Millisecond
	const leaderAfter = 4 * budget
	start := time.Now()
	node := &mockRaftNode{
		getLeaderFn: func(uint64) (uint64, bool, error) {
			if time.Since(start) < leaderAfter {
				return 0, false, nil
			}
			return 1, true, nil
		},
	}

	require.NoError(t, awaitLeader(context.Background(), node, budget, 7))
	assert.GreaterOrEqual(t, time.Since(start), leaderAfter)
}

// TestWaitForLeaderElection_ReportsTheLeaderWaitAsAnAuthBootstrapStep asserts
// that the bootstrap wrapper keeps the typed cause recoverable.
func TestWaitForLeaderElection_ReportsTheLeaderWaitAsAnAuthBootstrapStep(t *testing.T) {
	node := &mockRaftNode{
		getLeaderFn: func(uint64) (uint64, bool, error) { return 0, false, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := waitForLeaderElection(ctx, node, 50*time.Millisecond, 3)
	var abe *AuthBootstrapError
	require.ErrorAs(t, err, &abe)
	assert.Equal(t, "wait for leader", abe.Step)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	var we *server.ShardLeaderWaitError
	assert.False(t, errors.As(err, &we), "an exhausted round of the budget must not end the wait: %v", err)
}

// TestLeaderWaitBudget_MultiNodeIsElectionsAtQuicraftDefaults: a clustered
// daemon leaves the election timeout to go-quicraft, so the budget follows
// go-quicraft's defaults.
func TestLeaderWaitBudget_MultiNodeIsElectionsAtQuicraftDefaults(t *testing.T) {
	f := multiNodeFlags()
	require.False(t, isSingleNode(f), "precondition: multi-node flags")

	electionTimeout := time.Duration(quicraftconfig.DefaultElectionRTT*quicraftconfig.DefaultRTTMillisecond) * time.Millisecond
	assert.Equal(t, leaderWaitElections*electionTimeout, leaderWaitBudget(f))
	assert.Greater(t, leaderWaitBudget(f), replacedLeaderWait)
}

// TestLeaderWaitBudget_SingleNodeIsElectionsAtTransportlessTuning: a
// single-node daemon elects on the transport-less tuning, so its budget is
// that much shorter -- and still no shorter than the wait it replaces.
func TestLeaderWaitBudget_SingleNodeIsElectionsAtTransportlessTuning(t *testing.T) {
	f := singleNodeFlags()
	require.True(t, isSingleNode(f), "precondition: single-node flags")

	electionTimeout := time.Duration(cluster.SingleNodeElectionRTT*cluster.TransportlessRTTMillisecond) * time.Millisecond
	assert.Equal(t, leaderWaitElections*electionTimeout, leaderWaitBudget(f))
	assert.GreaterOrEqual(t, leaderWaitBudget(f), replacedLeaderWait)
}

// TestLeaderWaitBudget_SingleNodeJoinKeepsTheDefaultElectionRTT: --join keeps
// go-quicraft's ElectionRTT on the transport-less RTT (buildShardConfig), and
// the budget follows the configuration actually built.
func TestLeaderWaitBudget_SingleNodeJoinKeepsTheDefaultElectionRTT(t *testing.T) {
	f := singleNodeFlags()
	f.join = true

	electionTimeout := time.Duration(quicraftconfig.DefaultElectionRTT*cluster.TransportlessRTTMillisecond) * time.Millisecond
	assert.Equal(t, leaderWaitElections*electionTimeout, leaderWaitBudget(f))
}
