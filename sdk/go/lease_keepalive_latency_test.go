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

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
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

// leaseKeepAliveTick is the raft tick of the node the test boots. A
// single-member node has no transport and takes
// cluster.TransportlessRTTMillisecond, and every periodic step a renewal could
// wait on is paced by it: a heartbeat is one tick, the election timeout
// cluster.SingleNodeElectionRTT ticks.
var leaseKeepAliveTick = time.Duration(cluster.TransportlessRTTMillisecond) * time.Millisecond

// leaseKeepAliveTailFloor is the p90 a tail must exceed, as well as the ratio,
// to fail: a tenth of a raft tick, 2.5 ms. A renewal answered from the
// leader's memory takes tens of microseconds, so a p90 under the floor is
// scheduler jitter on a loaded host: main run 68703 measured 4.75x on a
// 1.3 ms p90 with nothing on the renewal path waiting. A renewal that waits
// on a tick-paced step pays up to a whole tick, ten times the floor, and the
// raft-log fsync the bound was written for put the p90 at 3.2-3.4 ms under a
// competing disk writer (test/performance/lease-keepalive.md), above it.
var leaseKeepAliveTailFloor = leaseKeepAliveTick / 10

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

// waits reports whether the tail shows a renewal waiting on something the
// leader should not wait for: a p90 at least leaseKeepAliveTailBound times the
// median, and above leaseKeepAliveTailFloor.
func (d leaseKeepAliveTail) waits() bool {
	return d.ratio() >= leaseKeepAliveTailBound && d.p90 > leaseKeepAliveTailFloor
}

// TestRaft_LeaseKeepAliveLatencyTail measures the round trip of a lease
// renewal against a real single-replica raft group on real disk, and asserts
// its tail stays inside leaseKeepAliveTailBound times its median or under
// leaseKeepAliveTailFloor.
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

	tail := newLeaseKeepAliveTail(took)
	t.Logf("lease keep-alive over %d renewals at %v cadence: median %v, p90 %v, max %v, p90/median %.2fx",
		len(took), *leaseKeepAliveCadence, tail.median, tail.p90, tail.max, tail.ratio())

	if tail.waits() {
		t.Fatalf("p90/median = %.2fx (median %v, p90 %v), want under %.0fx or a p90 under %v: a renewal is waiting on something the leader should not wait for",
			tail.ratio(), tail.median, tail.p90, leaseKeepAliveTailBound, leaseKeepAliveTailFloor)
	}
}

// leaseKeepAliveSample is a run of leaseKeepAliveCycles renewals of which
// slowCount took slow and the rest fast: the distribution a renewal double
// that adds a wait to slowCount renewals produces.
func leaseKeepAliveSample(fast, slow time.Duration, slowCount int) []time.Duration {
	took := make([]time.Duration, leaseKeepAliveCycles)
	for i := range took {
		took[i] = fast
		if i < slowCount {
			took[i] = slow
		}
	}
	return took
}

// TestLeaseKeepAliveTail_Waits checks the bound on distributions with and
// without a guarded wait. More than a tenth of the renewals waiting puts the
// wait at the p90; 7 of 64 does.
func TestLeaseKeepAliveTail_Waits(t *testing.T) {
	cases := []struct {
		name string
		took []time.Duration
		want bool
	}{
		// Each renewal answered from memory, at the after-figures of
		// test/performance/lease-keepalive.md.
		{"memory", leaseKeepAliveSample(69*time.Microsecond, 87*time.Microsecond, 7), false},
		// Main run 68703: a loaded runner under FIPS 140-3 mode, nothing waiting.
		{"scheduler jitter", leaseKeepAliveSample(273200*time.Nanosecond, 1296845*time.Nanosecond, 7), false},
		// A uniformly slow host: every renewal slow alike is not a tail.
		{"uniformly slow", leaseKeepAliveSample(5*time.Millisecond, 6*time.Millisecond, 7), false},
		// Every renewal through the raft log, under a competing disk-sync
		// writer: the before-figures of test/performance/lease-keepalive.md.
		{"log fsync, writer run 1", leaseKeepAliveSample(884*time.Microsecond, 3430*time.Microsecond, 7), true},
		{"log fsync, writer run 3", leaseKeepAliveSample(880*time.Microsecond, 3180*time.Microsecond, 7), true},
		// A renewal held for one raft tick, as one waiting on a heartbeat is.
		{"tick wait", leaseKeepAliveSample(69*time.Microsecond, leaseKeepAliveTick, 7), true},
		// The same wait on too few renewals to reach the p90.
		{"tick wait under p90", leaseKeepAliveSample(69*time.Microsecond, leaseKeepAliveTick, 6), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tail := newLeaseKeepAliveTail(tc.took)
			if got := tail.waits(); got != tc.want {
				t.Fatalf("waits() = %v, want %v (median %v, p90 %v, p90/median %.2fx, floor %v)",
					got, tc.want, tail.median, tail.p90, tail.ratio(), leaseKeepAliveTailFloor)
			}
		})
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
