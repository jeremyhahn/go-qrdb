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
	"sort"
	"testing"
	"time"
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

// leaseKeepAliveTailBound is the p90/median ratio a renewal must stay under
// (go-qrdb#275). A renewal that waits on a raft-log fsync fails it on a busy
// host; a renewal answered from the leader's memory does not.
const leaseKeepAliveTailBound = 3.0

// TestRaft_LeaseKeepAliveLatencyTail measures the round trip of a lease
// renewal against a real single-replica raft group on real disk, and asserts
// its tail stays inside leaseKeepAliveTailBound times its median.
//
// The renewal is answered by the leader from memory and replicated by the
// periodic checkpoint, so nothing on its path waits for a log fsync. The
// measured p50/p90/max are logged so a run under CPU contention (pin the test
// with taskset and add busy loops) can be read back into the performance
// notes.
func TestRaft_LeaseKeepAliveLatencyTail(t *testing.T) {
	coord, _ := bootRaftNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	l, err := coord.AcquireLease(ctx, "latency/tail", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

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

	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	median := took[len(took)/2]
	p90 := took[len(took)*9/10]
	max := took[len(took)-1]
	ratio := float64(p90) / float64(median)
	t.Logf("lease keep-alive over %d renewals at %v cadence: median %v, p90 %v, max %v, p90/median %.2fx",
		len(took), *leaseKeepAliveCadence, median, p90, max, ratio)

	if ratio >= leaseKeepAliveTailBound {
		t.Fatalf("p90/median = %.2fx (median %v, p90 %v), want under %.0fx: a renewal is waiting on something the leader should not wait for",
			ratio, median, p90, leaseKeepAliveTailBound)
	}
}
