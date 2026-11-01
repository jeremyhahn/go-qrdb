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
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// replacedLeaderWait is the wall time the leader wait used to give up after: 30
// polls with a backoff doubling from 50 ms to a 500 ms cap. It is here only so
// the budget tests can say that no configuration waits less than it did.
const replacedLeaderWait = 13250 * time.Millisecond

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
