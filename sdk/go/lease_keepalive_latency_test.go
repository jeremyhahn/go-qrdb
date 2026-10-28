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

package sdk_test

import (
	"context"
	"flag"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaseKeepAliveCadence is the pause between two renewals. The default keeps
// the run short; test/performance/lease-keepalive.md records the table at the
// 300ms and 1.5s cadences too, which are the ones a holder with a 2s TTL
// actually runs at: go test ./sdk/go -run LeaseKeepAliveLatencyTail
// -args -lease-keepalive-cadence=1500ms.
var leaseKeepAliveCadence = flag.Duration("lease-keepalive-cadence", 100*time.Millisecond,
	"pause between lease renewals in TestRaft_LeaseKeepAliveLatencyTail")

// leaseKeepAliveCycles is the sample size. The tail is a percentile, so it
// needs enough renewals to be one.
const leaseKeepAliveCycles = 64

// leaseKeepAliveTail is the latency distribution of a run of renewals.
type leaseKeepAliveTail struct {
	median, p90, max time.Duration
}

// newLeaseKeepAliveTail sorts took in place and reads its median, p90 and
// max.
func newLeaseKeepAliveTail(took []time.Duration) leaseKeepAliveTail {
	slices.Sort(took)
	return leaseKeepAliveTail{
		median: took[len(took)/2],
		p90:    took[len(took)*9/10],
		max:    took[len(took)-1],
	}
}

// ratio is the p90 over the median.
func (d leaseKeepAliveTail) ratio() float64 {
	return float64(d.p90) / float64(d.median)
}

// TestRaft_LeaseKeepAliveLatencyTail renews a lease against a real
// single-replica raft group on real disk and asserts no renewal went through
// the raft log (go-qrdb#275): a renewal is answered by the leader from
// memory, and the lease's TTL is under the checkpoint interval, so nothing
// on its path waits for a log commit and its fsync.
//
// The assertion is the code path, counted by service.LeaseService's
// LogRenewals, not the clock. The p90/median ratio this test used to assert
// failed on loaded CI runners with nothing on the renewal path waiting (main
// runs 68703 and 69207: 4.75x and 12.65x), and passed against the log path
// it was written for whenever no other process was syncing the disk
// (test/performance/lease-keepalive.md, 2.3-2.5x). The renewal latencies are
// still logged so a run under contention can be read back into that
// document, which is where the timing is a measurement rather than a gate.
func TestRaft_LeaseKeepAliveLatencyTail(t *testing.T) {
	n, _ := bootRaftNodeFull(t, raftNodeOpts{sealEnabled: true})
	coord, err := n.Client().Coordination()
	if err != nil {
		t.Fatalf("Coordination: %v", err)
	}
	// The embedded transport the coordinator renews through is handed this
	// node's lease service (node.go, embedded.WithLeaseService).
	leases, ok := n.Services().Lease.(*service.LeaseService)
	if !ok {
		t.Fatalf("node lease service is %T, want *service.LeaseService", n.Services().Lease)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	l, err := coord.AcquireLease(ctx, "latency/tail", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	before := leases.LogRenewals()
	cadence := time.NewTicker(*leaseKeepAliveCadence)
	defer cadence.Stop()
	took := make([]time.Duration, 0, leaseKeepAliveCycles)
	for range leaseKeepAliveCycles {
		<-cadence.C
		start := time.Now()
		if err := l.Renew(ctx); err != nil {
			t.Fatalf("Renew: %v", err)
		}
		took = append(took, time.Since(start))
	}
	logged := leases.LogRenewals() - before

	tail := newLeaseKeepAliveTail(took)
	t.Logf("lease keep-alive over %d renewals at %v cadence: median %v, p90 %v, max %v, p90/median %.2fx, %d through the raft log",
		len(took), *leaseKeepAliveCadence, tail.median, tail.p90, tail.max, tail.ratio(), logged)

	if logged != 0 {
		t.Fatalf("%d of %d renewals went through the raft log, want 0: a renewal the leader should answer from memory is waiting on a log commit",
			logged, len(took))
	}
}

// TestNewLeaseKeepAliveTail checks the median, p90 and max are read from the
// sorted run whatever order the renewals finished in.
func TestNewLeaseKeepAliveTail(t *testing.T) {
	took := make([]time.Duration, 10)
	for i := range took {
		took[i] = time.Duration(10-i) * time.Millisecond
	}
	got := newLeaseKeepAliveTail(took)
	want := leaseKeepAliveTail{median: 6 * time.Millisecond, p90: 10 * time.Millisecond, max: 10 * time.Millisecond}
	if got != want {
		t.Fatalf("newLeaseKeepAliveTail = %+v, want %+v", got, want)
	}
	if r := got.ratio(); r != 10.0/6.0 {
		t.Fatalf("ratio = %v, want %v", r, 10.0/6.0)
	}
}
