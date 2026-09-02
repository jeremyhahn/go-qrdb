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

package lease

import (
	"sync"
	"testing"
	"time"
)

// TestLessor_KeepAliveVsExpirySweep_NoRace drives the exact goroutine pair
// that the sweeper creates: KeepAlive WRITES lease.expiry from the Raft apply
// goroutine while the expiry sweeper READS it through ExpiredLeaseIDs.
//
// This is a regression test for a real data race (go-qrdb#174), not a
// hypothetical one. lease.mu guarded keys and was taken by KeepAlive to write
// expiry -- but isExpired and remainingTTL read expiry with no lock at all.
// It went unobserved for as long as it did because the only composition that
// ran the sweeper (cmd/qrdbd) has no -race test driving a keep-alive
// concurrently with a sweep; the detector reported it the first time
// sdk/go/node started the sweeper under the embedded raft lease tests.
//
// The test asserts nothing about ordering, because there is nothing to assert:
// under -race its VERDICT IS THE DETECTOR'S. Without -race it still exercises
// both paths and is cheap, so it is not gated on the flag -- a test that only
// runs under one invocation is a test people forget they have.
func TestLessor_KeepAliveVsExpirySweep_NoRace(t *testing.T) {
	lr := NewLessor()
	t.Cleanup(lr.Close)

	// A 1s TTL keeps the lease oscillating across the expiry boundary for the
	// whole run, so the sweeper's read and KeepAlive's write genuinely
	// interleave rather than the reader always taking the same branch.
	l, err := lr.Grant(1)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	lr.AttachKey(l.ID, []byte("bound/key"))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: the Raft apply goroutine's role.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := lr.KeepAlive(l.ID); err != nil {
				return
			}
		}
	}()

	// Readers: the sweeper's ExpiredLeaseIDs, plus the two other unlocked
	// readers of the same field, so a fix that covers only the sweeper's path
	// still fails here.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = lr.ExpiredLeaseIDs()
				_ = lr.Leases()
				_ = lr.Lookup(l.ID)
			}
		}()
	}

	// Bounded by a timer rather than an iteration count: the window this is
	// hunting is scheduler-dependent, and an iteration count that is generous
	// on an idle box is a long test on a busy one.
	time.AfterFunc(250*time.Millisecond, func() { close(stop) })
	wg.Wait()
}
