// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// lapseClock is a clusterseal.Clock that moves only when the test advances
// it; its After never fires, so the monitor polls only when Run is called.
type lapseClock struct{ now atomic.Int64 }

func (c *lapseClock) Now() time.Time                       { return time.Unix(0, c.now.Load()) }
func (c *lapseClock) After(time.Duration) <-chan time.Time { return nil }
func (c *lapseClock) advance(d time.Duration)              { c.now.Add(int64(d)) }

const lapseLease = 3 * time.Second

// newLapsingMonitor builds a monitor over an unsealed node barrier whose
// lease is renewed once, by one poll, at the clock's start.
func newLapsingMonitor(t *testing.T) (*clusterseal.Monitor, *lapseClock) {
	t.Helper()
	clock := &lapseClock{}
	clock.now.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	m, err := clusterseal.NewMonitor(clusterseal.Config{
		KV: &reunsealKV{m: map[string][]byte{}}, Enforcer: &SealEnforcer{Barriers: newEnforcerRegistry(t)}, Self: "1",
		Members: func(context.Context) ([]string, error) { return []string{"1"}, nil }, Gate: reunsealGate{},
		Lease: lapseLease, PollInterval: time.Second, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Run(cancelledCtx())
	return m, clock
}

// TestClusterSeal_LapsedLeaseIsUnconfirmedNotSealed: a node whose lease on
// the seal records lapses -- the system shard leaderless longer than the
// lease, its barrier unsealed throughout -- refuses as
// SEAL_STATE_UNCONFIRMED on every surface, never as SEALED (DS-1689).
func TestClusterSeal_LapsedLeaseIsUnconfirmedNotSealed(t *testing.T) {
	m, clock := newLapsingMonitor(t)
	probe := NodeSealProbe(boolProbe(false), m)
	srv := &Server{opts: serverOptions{clusterSeal: m, sealedWindowProbe: probe}}
	if sealed, unconfirmed := sealedwindow.Sample(probe); sealed || unconfirmed {
		t.Fatalf("fresh lease: SealState = (%v, %v), want serving", sealed, unconfirmed)
	}
	if err := srv.nodeRefusal(); err != nil {
		t.Fatalf("fresh lease: nodeRefusal = %v, want nil", err)
	}

	clock.advance(lapseLease + time.Second)

	// The status API reports the state as it is: the record unsealed, this
	// node cut off from it.
	if r := clusterseal.ReportOf(m, clusterseal.NodeBarrier()); r.Sealed || r.Local != clusterseal.LocalCutOff {
		t.Fatalf("lapsed lease: report sealed=%v local=%q, want unsealed and %q", r.Sealed, r.Local, clusterseal.LocalCutOff)
	}
	if sealed, unconfirmed := sealedwindow.Sample(probe); !sealed || !unconfirmed {
		t.Fatalf("lapsed lease: SealState = (%v, %v), want closed and unconfirmed", sealed, unconfirmed)
	}
	err := srv.nodeRefusal()
	if _, ok := errors.AsType[*clusterseal.SealStateUnconfirmedError](err); !ok {
		t.Fatalf("lapsed lease: nodeRefusal = %v, want *clusterseal.SealStateUnconfirmedError", err)
	}
	if _, ok := errors.AsType[*clusterseal.SealedError](err); ok {
		t.Fatalf("lapsed lease: nodeRefusal = %v, reported as sealed", err)
	}
	if reason, _, ok := replicaforward.Refusal(err); !ok || reason != replicaforward.ReasonSealStateUnconfirmed {
		t.Fatalf("lapsed lease: wire reason %q (ok %v), want %s", reason, ok, replicaforward.ReasonSealStateUnconfirmed)
	}
	if d := sealedwindow.Decide(sealedwindow.Request{Sealed: true, Unconfirmed: true}); d != sealedwindow.RefuseUnconfirmed {
		t.Fatalf("lapsed lease: gate decision %v, want RefuseUnconfirmed", d)
	}

	// A fresh poll renews the lease and the node serves again.
	m.Run(cancelledCtx())
	if sealed, _ := sealedwindow.Sample(probe); sealed {
		t.Fatal("renewed lease: the node still refuses")
	}
}

// TestClusterSeal_LapsedLeaseOverSealedBarrierIsSealed: a sealed node
// barrier is SEALED whatever the lease says; the unconfirmed state never
// hides a seal the node can see.
func TestClusterSeal_LapsedLeaseOverSealedBarrierIsSealed(t *testing.T) {
	m, clock := newLapsingMonitor(t)
	clock.advance(lapseLease + time.Second)
	probe := NodeSealProbe(boolProbe(true), m)
	if sealed, unconfirmed := sealedwindow.Sample(probe); !sealed || unconfirmed {
		t.Fatalf("SealState = (%v, %v), want sealed and confirmed", sealed, unconfirmed)
	}
	srv := &Server{opts: serverOptions{clusterSeal: m, sealedWindowProbe: probe}}
	if _, ok := errors.AsType[*clusterseal.SealedError](srv.nodeRefusal()); !ok {
		t.Fatalf("nodeRefusal = %v, want *clusterseal.SealedError", srv.nodeRefusal())
	}
}
