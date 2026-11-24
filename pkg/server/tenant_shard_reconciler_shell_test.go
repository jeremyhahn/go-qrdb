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

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// recordingShellRegistrar is a ShellRegistrar that records each tenant it is
// asked to register a shell for, and answers with err.
type recordingShellRegistrar struct {
	mu      sync.Mutex
	tenants []string
	err     error
}

// Compile-time interface check.
var _ ShellRegistrar = (*recordingShellRegistrar)(nil)

func (r *recordingShellRegistrar) RegisterTenantShell(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants = append(r.tenants, tenantID)
	return r.err
}

func (r *recordingShellRegistrar) registered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tenants...)
}

// TestTenantShardReconciler_RegistersShellForAPlacementWithoutABarrier
// verifies that a placement naming this node, for a tenant with no barrier
// here, registers the tenant's shell and still defers the shard start: the
// shell is sealed.
func TestTenantShardReconciler_RegistersShellForAPlacementWithoutABarrier(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("shells registered = %v, want [acme]", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times for a tenant whose barrier is only a shell, want 0", factory.attachCount())
	}
}

// homePlacement returns tenantID's home-group placement with one replica on node.
func homePlacement(tenantID, node string, shardID uint64) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		DatabaseID:      database.TenantHomeID,
		Database:        database.TenantHomeName,
		ShardID:         shardID,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: node, RaftAddr: node},
		},
	}
}

func TestTenantShardReconciler_NoShellOutsideTheServingSet(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers,
		placement1("acme", "orders", 1001),
		homePlacement("acme", "peer:2", 1002))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v on a node outside the serving set, want none", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}
}

func TestTenantShardReconciler_RegistersShellInsideTheServingSet(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers,
		placement1("acme", "orders", 1001),
		homePlacement("acme", "local:1", 1002))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	got := shells.registered()
	if len(got) == 0 {
		t.Fatal("no shell registered on a node inside the serving set")
	}
	for _, id := range got {
		if id != "acme" {
			t.Fatalf("shells registered = %v, want only acme", got)
		}
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}
}

// getFailingKV fails Get on a tenant home placement key while fail is set.
type getFailingKV struct {
	*memKV
	fail atomic.Bool
}

func (k *getFailingKV) Get(ctx context.Context, key string) ([]byte, error) {
	if k.fail.Load() && strings.HasSuffix(key, "/"+database.TenantHomeID) {
		return nil, errors.New("placement store unavailable")
	}
	return k.memKV.Get(ctx, key)
}

func TestTenantShardReconciler_NoShellWhenServingSetUnreadable(t *testing.T) {
	kv := &getFailingKV{memKV: newMemKV()}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), placement1("acme", "orders", 1001)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	factory := newFakeShardLifecycle()
	factory.shardIDOf = shardIDFromStore(store, "local:1")
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithBarrierReadiness(barriers))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	kv.fail.Store(true)
	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v with the serving set unreadable, want none", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}

	kv.fail.Store(false)
	if err := store.Put(context.Background(), homePlacement("acme", "local:1", 1002)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 2 || got[0] != "acme" || got[1] != "acme" {
		t.Fatalf("shells registered = %v, want acme for the home and orders placements", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_NoShellForAReadyBarrier verifies that a tenant
// whose barrier is registered and unsealed starts its shard and registers no
// shell.
func TestTenantShardReconciler_NoShellForAReadyBarrier(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markReady("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v for a ready barrier, want none", got)
	}
	if factory.attachCount() != 1 {
		t.Fatalf("Attach called %d times, want 1", factory.attachCount())
	}
}

// TestTenantShardReconciler_ShellFailureRetriesNextPass verifies that a shell
// registration that fails defers the shard and is retried on the next pass.
func TestTenantShardReconciler_ShellFailureRetriesNextPass(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{err: errors.New("volume assignment not replicated yet")}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())
	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 2 {
		t.Fatalf("shell registration attempts = %v, want one per pass", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times after failed shell registrations, want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_SetShellRegistrarNil verifies that clearing the
// registrar stops shell registration without disturbing the deferral.
func TestTenantShardReconciler_SetShellRegistrarNil(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)
	r.SetShellRegistrar(nil)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v after the registrar was cleared, want none", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}
}
