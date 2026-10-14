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

//go:build integration

package integration_test

import (
	"testing"
	"time"
)

// TestClusterFault_SIGKILLUnderLoad kills the leader and then a follower of a
// 3-node cluster with SIGKILL while concurrent writers load it, restarts
// each, and proves that the cluster lost nothing it acknowledged: every
// acknowledged write is on every replica, the replicas hold identical data,
// no shard halted, and the surviving majority elected a new leader within
// faultElectionBound. Writes whose outcome the client never learned may land
// or not; the only value any replica may hold for a key is the one written.
//
// It runs once per seal type in faultSeals, each subtest on its own
// cluster whose nodes hold their own credentials. A restarted node comes back
// sealed under every keyed seal type, must refuse data until it is unsealed
// with its own credential, and is unsealed that way before it rejoins.
func TestClusterFault_SIGKILLUnderLoad(t *testing.T) {
	for _, seal := range faultSeals {
		t.Run(string(seal), func(t *testing.T) {
			start := time.Now()
			sigkillUnderLoad(t, seal)
			t.Logf("seal %s: %v", seal, time.Since(start).Round(time.Second))
		})
	}
}

// sigkillUnderLoad is TestClusterFault_SIGKILLUnderLoad on one cluster
// running under seal.
func sigkillUnderLoad(t *testing.T, seal faultSeal) {
	c := startFaultCluster(t, false, seal)
	ledger := newWriteLedger("crash")
	load := c.startFaultLoad(t, ledger.prefix, 4)

	// stepAcked is how much acknowledged progress each phase waits for: the
	// writers are mid-load whenever a node dies, and the cluster is shown to
	// serve again before the next fault.
	const stepAcked = 50
	load.waitAcked(t, stepAcked, faultRecoveryTimeout)

	leader, term := c.leader(t, []int{0, 1, 2}, 0, faultRecoveryTimeout)
	c.kill(t, leader)
	killed := time.Now()
	next, nextTerm := c.leader(t, c.others(leader), term, faultElectionBound)
	t.Logf("SIGKILL of leader node %d (term %d): node %d elected in term %d after %v (bound %v)",
		c.nodes[leader].replicaID, term, c.nodes[next].replicaID, nextTerm, time.Since(killed), faultElectionBound)
	load.waitAcked(t, load.acked.Load()+stepAcked, faultRecoveryTimeout)

	c.restart(t, leader)
	current := c.waitSettled(t, faultRecoveryTimeout)
	load.waitAcked(t, load.acked.Load()+stepAcked, faultRecoveryTimeout)

	follower := c.others(current)[0]
	c.kill(t, follower)
	t.Logf("SIGKILL of follower node %d", c.nodes[follower].replicaID)
	load.waitAcked(t, load.acked.Load()+stepAcked, faultRecoveryTimeout)

	c.restart(t, follower)
	c.waitSettled(t, faultRecoveryTimeout)
	load.waitAcked(t, load.acked.Load()+stepAcked, faultRecoveryTimeout)

	load.finish(t, ledger)
	t.Logf("load: %d writes issued, %d acknowledged", len(ledger.attempted), len(ledger.acked))
	c.waitConverged(t, ledger, faultRecoveryTimeout)
	c.waitSettled(t, faultRecoveryTimeout)
}
