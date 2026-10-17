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
	"io"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"
)

// leaseReaders is how many clients read through the old leader at once in a
// lease probe. Several make it likely that one read reaches the old leader
// on the first tick after it resumes, which is the read a lease measured in
// ticks rather than elapsed time would wrongly serve.
const leaseReaders = 8

// TestClusterFault_LeaseReadAfterLeaderChange proves that a leader's read
// lease never serves a value a successor has overwritten. lease_read is on,
// as it is by default, so a leader answers a linearizable read from its lease
// without a heartbeat round. In each case the leader of a 3-node cluster loses
// its followers, the majority elects a successor and overwrites a key, and
// clients reading that key through the old leader must never see the old
// value once the overwrite has been acknowledged:
//
//   - isolated: the leader is cut off at the relays and keeps running, so its
//     lease ages out while it still believes it leads;
//   - paused and isolated: the leader is stopped with SIGSTOP, cut off and
//     resumed after the overwrite, so its first tick finds acknowledgements
//     taken before the pause and the reads queued during it;
//   - paused: as above, but resumed with its links restored, so it hears the
//     new term as soon as it sends.
//
// No node restarts (a paused process keeps its keys), so the seal type does
// not reach the fault: it runs once, under passphrase, as the partition test
// does.
func TestClusterFault_LeaseReadAfterLeaderChange(t *testing.T) {
	c := startFaultCluster(t, true, faultSealPassphrase)
	writer := mtlsHTTPClient(t, c.ca, faultWriteTimeout)

	for _, tc := range []struct {
		name            string
		pause, isolated bool
	}{
		{name: "isolated", isolated: true},
		{name: "paused and isolated", pause: true, isolated: true},
		{name: "paused", pause: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := c.waitSettled(t, faultRecoveryTimeout)
			_, term := c.leader(t, []int{old}, 0, faultRecoveryTimeout)
			key := fmt.Sprintf("lease-%d", term)
			before, after := faultValue(key+"-before"), faultValue(key+"-after")
			c.putAcked(t, writer, old, key, before, faultRecoveryTimeout)
			// A linearizable read through the leader both proves the value
			// is there and leaves the lease freshly held.
			if got, err := c.readLinearizable(writer, old, key); err != nil || got != before {
				t.Fatalf("read through leader node %d = %q, %v; want %q", c.nodes[old].replicaID, got, err, before)
			}

			var probe *leaseProbe
			if !tc.pause {
				probe = c.startLeaseProbe(t, old, key)
			}
			if tc.pause {
				c.signal(t, old, syscall.SIGSTOP)
			}
			if tc.isolated {
				c.isolate(t, old)
			}
			cut := time.Now()

			next, nextTerm := c.leader(t, c.others(old), term, faultElectionBound)
			c.putAcked(t, writer, next, key, after, faultRecoveryTimeout)
			overwritten := time.Now()
			t.Logf("leader node %d cut off in term %d; node %d elected in term %d and overwrote %s %v later",
				c.nodes[old].replicaID, term, c.nodes[next].replicaID, nextTerm, key, overwritten.Sub(cut))

			if tc.pause {
				// The readers dial while the old leader is stopped, so their
				// requests are waiting for it the moment it resumes.
				probe = c.startLeaseProbe(t, old, key)
				time.Sleep(faultPollInterval)
				c.signal(t, old, syscall.SIGCONT)
			}
			// Long enough for the old leader to step down on its own
			// (CheckQuorum) or on hearing the new term.
			time.Sleep(2 * faultMaxElectionTimeout)
			reads := probe.stop()

			c.heal()
			var served, stale int
			values := make(map[string]int)
			// lastBefore is how long after the cut the old value was last
			// served: the lease outliving the cut, which it may, by at most
			// the lease duration.
			var lastBefore time.Duration
			var firstRefusal error
			for _, r := range reads {
				if r.err != nil {
					if firstRefusal == nil && r.call.After(cut) {
						firstRefusal = r.err
						t.Logf("first refusal, called %v after the cut: %v", r.call.Sub(cut), r.err)
					}
					continue
				}
				served++
				values[r.value]++
				if r.value == before {
					lastBefore = max(lastBefore, r.call.Sub(cut))
				}
				if r.value == before && r.call.After(overwritten) {
					stale++
					t.Errorf("STALE READ: node %d served %q for %s, called %v after %q was acknowledged",
						c.nodes[old].replicaID, r.value, key, r.call.Sub(overwritten), after)
				}
			}
			t.Logf("%d reads through node %d: %d served (%d old, %d new; old value last called %v after the cut), %d refused or timed out, %d stale",
				len(reads), c.nodes[old].replicaID, served, values[before], values[after], lastBefore, len(reads)-served, stale)
			if len(reads) == 0 {
				t.Fatalf("no read reached node %d", c.nodes[old].replicaID)
			}

			c.waitSettled(t, faultRecoveryTimeout)
			deadline := time.Now().Add(faultRecoveryTimeout)
			for {
				got, err := c.readLinearizable(writer, old, key)
				if err == nil && got == after {
					break
				}
				if err == nil && got != after {
					t.Fatalf("node %d serves %q for %s after the heal; want %q", c.nodes[old].replicaID, got, key, after)
				}
				if time.Now().After(deadline) {
					t.Fatalf("node %d did not serve %s within %v of the heal: %v", c.nodes[old].replicaID, key, faultRecoveryTimeout, err)
				}
				time.Sleep(faultPollInterval)
			}
		})
	}
}

// signal sends sig to node i's daemon.
func (c *faultCluster) signal(t *testing.T, i int, sig syscall.Signal) {
	t.Helper()
	if err := c.nodes[i].cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v to node %d: %v", sig, c.nodes[i].replicaID, err)
	}
	if sig == syscall.SIGSTOP {
		// A test that fails while the node is stopped must not leave it
		// stopped for the other cleanups to wait on.
		t.Cleanup(func() { _ = c.nodes[i].cmd.Process.Signal(syscall.SIGCONT) })
	}
}

// readLinearizable reads key through node i at the linearizable level, the
// level a lease read serves.
func (c *faultCluster) readLinearizable(client *http.Client, i int, key string) (string, error) {
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/%s?consistency=linearizable", c.nodes[i].adminPort, key))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &faultStatusError{Port: c.nodes[i].adminPort, Code: resp.StatusCode, Body: string(body)}
	}
	return string(body), nil
}

// leaseRead is one read a leaseProbe issued: when it was called and what it
// returned.
type leaseRead struct {
	call  time.Time
	value string
	err   error
}

// leaseProbe is a set of clients reading one key through one node over and
// over until stopped.
type leaseProbe struct {
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
	mu    sync.Mutex
	reads []leaseRead
}

// startLeaseProbe starts leaseReaders clients, each on its own connection,
// reading key through node i. The probe is stopped when t ends, so a test
// that fails before stopping it does not leave its clients reading.
func (c *faultCluster) startLeaseProbe(t *testing.T, i int, key string) *leaseProbe {
	t.Helper()
	p := &leaseProbe{done: make(chan struct{})}
	for range leaseReaders {
		client := mtlsHTTPClient(t, c.ca, faultWriteTimeout)
		p.wg.Go(func() {
			for {
				select {
				case <-p.done:
					return
				default:
				}
				r := leaseRead{call: time.Now()}
				r.value, r.err = c.readLinearizable(client, i, key)
				p.mu.Lock()
				p.reads = append(p.reads, r)
				p.mu.Unlock()
				if r.err != nil {
					time.Sleep(faultPollInterval)
				}
			}
		})
	}
	// Registered after the clients, so it runs before their own cleanups.
	t.Cleanup(func() { p.stop() })
	return p
}

// stop stops the probe's clients, waits for their last reads and returns
// every read. It may be called more than once.
func (p *leaseProbe) stop() []leaseRead {
	p.once.Do(func() { close(p.done) })
	p.wg.Wait()
	return p.reads
}
