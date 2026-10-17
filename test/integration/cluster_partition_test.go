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
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestClusterFault_PartitionIsolatesLeader cuts the leader of a 3-node
// cluster off from both followers at the UDP relays in front of every Raft
// listener, then heals the cut. While isolated, the old leader acknowledges
// no write and stops claiming leadership, and the majority elects a leader
// and serves. After the heal, the old leader follows the new one, serves
// again, and holds exactly what the majority holds: every acknowledged
// write, and none of the writes only it saw.
//
// No node restarts, so the seal type does not reach the fault: it runs once,
// under passphrase, the keyed strategy a production node is initialized with.
func TestClusterFault_PartitionIsolatesLeader(t *testing.T) {
	c := startFaultCluster(t, true, faultSealPassphrase)
	ledger := newWriteLedger("partition")
	client := mtlsHTTPClient(t, c.ca, faultWriteTimeout)

	// Every node reached every peer through the relays; were any path
	// direct, cutting the relays would partition nothing.
	for _, node := range c.nodes {
		if node.relay.Forwarded() == 0 {
			t.Fatalf("relay in front of node %d carried no traffic", node.replicaID)
		}
	}

	write := func(i int, key string) {
		t.Helper()
		c.putAcked(t, client, i, key, faultValue(key), faultRecoveryTimeout)
		ledger.attempted[key] = faultValue(key)
		ledger.acked[key] = struct{}{}
	}
	for n := range 9 {
		write(n%len(c.nodes), fmt.Sprintf("%s-before-%02d", ledger.prefix, n))
	}

	old, term := c.leader(t, []int{0, 1, 2}, 0, faultRecoveryTimeout)
	majority := c.others(old)
	c.isolate(t, old)
	cut := time.Now()

	// Writes through the isolated leader, issued while it still believes it
	// leads. It can append them to its own log but never commit them.
	refused := c.minorityWrites(client, old, ledger.prefix+"-isolated-a", 5)

	next, nextTerm := c.leader(t, majority, term, faultElectionBound)
	t.Logf("isolated leader node %d (term %d): node %d elected in term %d after %v (bound %v)",
		c.nodes[old].replicaID, term, c.nodes[next].replicaID, nextTerm, time.Since(cut), faultElectionBound)
	for n := range 9 {
		write(majority[n%len(majority)], fmt.Sprintf("%s-majority-%02d", ledger.prefix, n))
	}

	// CheckQuorum: a leader that hears from no quorum for an election
	// timeout steps down on its own side of the cut.
	c.waitNotLeader(t, old, faultElectionBound)
	refusedLater := c.minorityWrites(client, old, ledger.prefix+"-isolated-b", 5)

	for _, outcomes := range []<-chan faultMinorityWrite{refused, refusedLater} {
		for w := range outcomes {
			if w.acked {
				t.Errorf("isolated node %d acknowledged %s", c.nodes[old].replicaID, w.key)
			}
			ledger.lost[w.key] = struct{}{}
		}
	}
	if c.nodes[old].relay.Dropped() == 0 {
		t.Fatalf("relay in front of isolated node %d dropped nothing", c.nodes[old].replicaID)
	}

	c.heal()
	leader := c.waitSettled(t, faultRecoveryTimeout)
	if leader == old {
		t.Fatalf("isolated node %d leads again after the heal; its log lacks the majority's writes", c.nodes[old].replicaID)
	}
	for n := range 9 {
		write(old, fmt.Sprintf("%s-after-%02d", ledger.prefix, n))
	}
	c.waitConverged(t, ledger, faultRecoveryTimeout)
}

// faultMinorityWrite is the outcome of one write through an isolated node.
type faultMinorityWrite struct {
	key   string
	acked bool
}

// minorityWrites issues n concurrent writes through node i and returns a
// channel that yields each outcome and closes once all have returned.
func (c *faultCluster) minorityWrites(client *http.Client, i int, prefix string, n int) <-chan faultMinorityWrite {
	out := make(chan faultMinorityWrite, n)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() {
			key := fmt.Sprintf("%s-%02d", prefix, k)
			out <- faultMinorityWrite{key: key, acked: c.put(client, i, key, faultValue(key))}
		})
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// waitNotLeader waits until node i stops reporting itself leader of the data
// shard.
func (c *faultCluster) waitNotLeader(t *testing.T, i int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s, err := c.dataShard(i)
		if err == nil && !s.IsLeader {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %d still leads %v after it was cut off (last: %+v, %v)", c.nodes[i].replicaID, timeout, s, err)
		}
		time.Sleep(faultPollInterval)
	}
}
