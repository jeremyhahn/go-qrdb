// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// rollingOn reports the rolling-migration feature activated.
func rollingOn() bool { return true }

// failingKV is a memKV whose Put and Get fail on demand.
type failingKV struct {
	*memKV
	putErr, getErr error
}

func (k *failingKV) Put(ctx context.Context, key string, v []byte) error {
	if k.putErr != nil {
		return k.putErr
	}
	return k.memKV.Put(ctx, key, v)
}

func (k *failingKV) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if k.putErr != nil {
		return false, k.putErr
	}
	return k.memKV.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
}

func (k *failingKV) Get(ctx context.Context, key string) ([]byte, error) {
	if k.getErr != nil {
		return nil, k.getErr
	}
	return k.memKV.Get(ctx, key)
}

// rollingCoordinator is a leader Coordinator for owners n1..n3 on whose node,
// n1, the request is made.
func rollingCoordinator(s *Store, gate func() bool) *Coordinator {
	return NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, nil, WithRollingSteps(gate, "n1"))
}

func requireSteps(t *testing.T, s *Store, want ...NodeStep) {
	t.Helper()
	m, err := s.GetMigration(context.Background(), tid, db)
	if err != nil || m == nil {
		t.Fatalf("GetMigration = %v, %v", m, err)
	}
	if !slices.Equal(m.Steps, want) {
		t.Fatalf("Steps = %+v, want %+v", m.Steps, want)
	}
}

func step(node string, state StepState) NodeStep { return NodeStep{NodeID: node, State: state} }

func TestRollingSteps_CoordinatorLast(t *testing.T) {
	got := rollingSteps([]string{"n1", "n2", "n3"}, "n2")
	want := []NodeStep{step("n1", StepRecycling), step("n3", StepPending), step("n2", StepPending)}
	if !slices.Equal(got, want) {
		t.Fatalf("rollingSteps = %+v, want %+v", got, want)
	}
}

func TestRollingSteps_CoordinatorNotAnOwner(t *testing.T) {
	got := rollingSteps([]string{"n1", "n2"}, "n9")
	want := []NodeStep{step("n1", StepRecycling), step("n2", StepPending)}
	if !slices.Equal(got, want) {
		t.Fatalf("rollingSteps = %+v, want %+v", got, want)
	}
	if got := rollingSteps(nil, "n9"); len(got) != 0 {
		t.Fatalf("rollingSteps(no owners) = %+v, want none", got)
	}
}

func TestCoordinator_Request_WritesRollingStepsOnceActivated(t *testing.T) {
	s := NewStore(newMemKV())
	if _, err := rollingCoordinator(s, rollingOn).Request(context.Background(), tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	requireSteps(t, s, step("n2", StepRecycling), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Request_NoStepsBeforeActivation(t *testing.T) {
	for name, gate := range map[string]func() bool{"not activated": func() bool { return false }, "no gate": nil} {
		t.Run(name, func(t *testing.T) {
			s := NewStore(newMemKV())
			if _, err := rollingCoordinator(s, gate).Request(context.Background(), tid, db, validOpts()); err != nil {
				t.Fatalf("Request: %v", err)
			}
			requireSteps(t, s)
		})
	}
}

func TestCoordinator_Advance_WaitsForTheRecyclingOwnersAck(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := rollingCoordinator(s, rollingOn)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	// No ack, an ack still copying, and an ack for another generation all
	// leave n2 recycling and n3 pending.
	for _, phase := range []seal.MigrationPhase{"", seal.PhaseCopying} {
		if phase != "" {
			ack(t, s, "n2", 1, phase)
		}
		if wrote, err := c.Advance(ctx, tid, db); wrote || err != nil {
			t.Fatalf("Advance(n2 %q) = %v, %v; want false, nil", phase, wrote, err)
		}
	}
	ack(t, s, "n2", 0, seal.PhaseComplete)
	if wrote, err := c.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance(stale ack) = %v, %v; want false, nil", wrote, err)
	}
	requireSteps(t, s, step("n2", StepRecycling), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Advance_HandsTheTurnOnOneOwnerAtATime(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := rollingCoordinator(s, rollingOn)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n2", 1, seal.PhaseComplete)
	if wrote, err := c.Advance(ctx, tid, db); !wrote || err != nil {
		t.Fatalf("Advance = %v, %v; want true, nil", wrote, err)
	}
	requireSteps(t, s, step("n2", StepServing), step("n3", StepRecycling), step("n1", StepPending))
	requireComplete(t, ctx, s, false, "one of three owners serving")

	ack(t, s, "n3", 1, seal.PhaseComplete)
	ack(t, s, "n1", 1, seal.PhaseComplete) // early: n1's turn has not come
	if _, err := c.Advance(ctx, tid, db); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	requireSteps(t, s, step("n2", StepServing), step("n3", StepServing), step("n1", StepRecycling))
	requireComplete(t, ctx, s, false, "every owner acked but n1 not yet recorded serving")

	if _, err := c.Advance(ctx, tid, db); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	requireSteps(t, s, step("n2", StepServing), step("n3", StepServing), step("n1", StepServing))
	requireComplete(t, ctx, s, true, "every step serving")
	if wrote, err := c.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance(rolled out) = %v, %v; want false, nil", wrote, err)
	}
}

func TestCoordinator_Advance_ANewCoordinatorResumesFromTheRecord(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if _, err := rollingCoordinator(s, rollingOn).Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n2", 1, seal.PhaseComplete)
	// The requesting leader is gone; another node's coordinator, which never
	// saw the request, carries the rollout on from the replicated record.
	next := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if wrote, err := next.Advance(ctx, tid, db); !wrote || err != nil {
		t.Fatalf("Advance = %v, %v; want true, nil", wrote, err)
	}
	requireSteps(t, s, step("n2", StepServing), step("n3", StepRecycling), step("n1", StepPending))
}

func TestCoordinator_Advance_LeavesOtherRecordsAlone(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	follower := NewCoordinator(s, fixedOwners{}, func() bool { return false }, nil)
	leader := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if wrote, err := leader.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance(no record) = %v, %v; want false, nil", wrote, err)
	}
	seedMigration(t, s, 1, []string{"n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	if wrote, err := leader.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance(no steps) = %v, %v; want false, nil", wrote, err)
	}
	m, raw, _ := s.readMigration(ctx, tid, db)
	m.Steps = []NodeStep{step("n1", StepRecycling)}
	if err := s.swapMigration(ctx, raw, m); err != nil {
		t.Fatalf("swapMigration: %v", err)
	}
	if wrote, err := follower.Advance(ctx, tid, db); wrote || err != nil {
		t.Fatalf("Advance(follower) = %v, %v; want false, nil", wrote, err)
	}
	requireSteps(t, s, step("n1", StepRecycling))
}

func TestCoordinator_Advance_StartsAPendingFirstStep(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	m, raw, _ := s.readMigration(ctx, tid, db)
	m.Steps = []NodeStep{step("n1", StepPending), step("n2", StepPending)}
	if err := s.swapMigration(ctx, raw, m); err != nil {
		t.Fatalf("swapMigration: %v", err)
	}
	c := NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil)
	if wrote, err := c.Advance(ctx, tid, db); !wrote || err != nil {
		t.Fatalf("Advance = %v, %v; want true, nil", wrote, err)
	}
	requireSteps(t, s, step("n1", StepRecycling), step("n2", StepPending))
}

func TestCoordinator_Advance_StoreErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	kv := &failingKV{memKV: newMemKV()}
	s := NewStore(kv)
	c := rollingCoordinator(s, rollingOn)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n2", 1, seal.PhaseComplete)
	kv.putErr = boom
	if wrote, err := c.Advance(ctx, tid, db); wrote || !errors.Is(err, boom) {
		t.Fatalf("Advance(put fails) = %v, %v; want false, boom", wrote, err)
	}
	kv.putErr, kv.getErr = nil, boom
	if wrote, err := c.Advance(ctx, tid, db); wrote || !errors.Is(err, boom) {
		t.Fatalf("Advance(get fails) = %v, %v; want false, boom", wrote, err)
	}
}

func TestReconciler_RollingRecyclesOnlyOnItsTurn(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if _, err := rollingCoordinator(s, rollingOn).Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	for _, node := range []string{"n1", "n3"} {
		rec := &recordingRecycler{}
		if n, err := NewReconciler(s, node, rec, nil, nil, nil).ReconcileOnce(ctx); n != 0 || err != nil || rec.count() != 0 {
			t.Fatalf("%s (pending) recycled %d times, ReconcileOnce = %d, %v; want none", node, rec.count(), n, err)
		}
	}
	rec := &recordingRecycler{}
	if n, err := NewReconciler(s, "n2", rec, nil, nil, nil).ReconcileOnce(ctx); n != 1 || err != nil {
		t.Fatalf("n2 (recycling) ReconcileOnce = %d, %v; want 1, nil", n, err)
	}
}

func TestReconciler_RollingOwnerMissingFromStepsWaits(t *testing.T) {
	m := &ClusterMigration{Owners: []string{"n1", "n2"}, Steps: []NodeStep{step("n1", StepRecycling)}}
	if m.turnOf("n2") {
		t.Fatal("an owner with no step must not recycle in a rolling migration")
	}
	if !(&ClusterMigration{Owners: []string{"n2"}}).turnOf("n2") {
		t.Fatal("every owner of a migration without steps recycles at once")
	}
}

// inFlightRecycler counts the owners recycling at the same time across a
// cluster of reconcilers sharing one store, as the Raft log shares it.
type inFlightRecycler struct {
	mu       *sync.Mutex
	inFlight *int
	peak     *int
	order    *[]string
	node     string
}

func (inFlightRecycler) AtTarget(*ClusterMigration) bool { return false }

func (r inFlightRecycler) Recycle(context.Context, *ClusterMigration, []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.order = append(*r.order, r.node)
	*r.inFlight++
	*r.peak = max(*r.peak, *r.inFlight)
	return nil
}

// down ends every recycle of a tick: the node is back, and its ack written.
func (r inFlightRecycler) down() { *r.inFlight = 0 }

func TestRolling_ThreeOwnersNeverTwoAwayAtOnce(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := rollingCoordinator(s, rollingOn)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	var (
		mu              sync.Mutex
		inFlight, peak  int
		order           []string
		reconcilers     []*Reconciler
		sharedRecyclers []inFlightRecycler
	)
	for _, node := range []string{"n1", "n2", "n3"} {
		rec := inFlightRecycler{mu: &mu, inFlight: &inFlight, peak: &peak, order: &order, node: node}
		sharedRecyclers = append(sharedRecyclers, rec)
		reconcilers = append(reconcilers, NewReconciler(s, node, rec, nil, nil, nil))
	}
	for tick := 0; tick < 6; tick++ {
		for _, r := range reconcilers {
			if _, err := r.ReconcileOnce(ctx); err != nil {
				t.Fatalf("tick %d: ReconcileOnce: %v", tick, err)
			}
		}
		if peak > 1 {
			t.Fatalf("tick %d: %d owners recycling at once, order %v", tick, peak, order)
		}
		sharedRecyclers[0].down()
		if _, err := c.Advance(ctx, tid, db); err != nil {
			t.Fatalf("tick %d: Advance: %v", tick, err)
		}
	}
	if want := []string{"n2", "n3", "n1"}; !slices.Equal(order, want) {
		t.Fatalf("recycle order = %v, want %v", order, want)
	}
	if reaped, err := c.Reap(ctx, tid, db); !reaped || err != nil {
		t.Fatalf("Reap = %v, %v; want true, nil", reaped, err)
	}
}
