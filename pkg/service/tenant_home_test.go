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

package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newHomeDatabaseService returns a placement-wired DatabaseService for the
// raft tenant "rt" over a cluster of nodes.
func newHomeDatabaseService(t *testing.T, nodes []placement.NodeLoad) (*DatabaseService, *placement.Store) {
	t.Helper()
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, nodes)
	return svc.(*DatabaseService), store
}

func TestProvisionTenantHome_PlacesOnEveryNode(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()

	plan, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	if plan.Database != database.TenantHomeName || plan.TenantID != "rt" {
		t.Fatalf("placement = %s/%s, want rt/%s", plan.TenantID, plan.Database, database.TenantHomeName)
	}
	if plan.RF != 3 || len(plan.Replicas) != 3 {
		t.Fatalf("RF = %d with %d replicas, want 3 (every node)", plan.RF, len(plan.Replicas))
	}
	if plan.ShardID == 0 {
		t.Fatal("the home group must carry a ledger-issued shard ID")
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if stored.ShardID != plan.ShardID {
		t.Fatalf("stored shard %d, returned %d", stored.ShardID, plan.ShardID)
	}
	if !plan.DedicatedWAL || !stored.DedicatedWAL {
		t.Fatalf("the home group must keep its Raft log in a WAL under the tenant's key: returned %t, stored %t", plan.DedicatedWAL, stored.DedicatedWAL)
	}
	// The home group is a placement only: no catalog database exists for it.
	if _, err := svc.GetForTenant(tenant.WithTenantID(ctx, "rt"), database.TenantHomeName); err == nil {
		t.Fatal("the home group must not be a catalog database")
	}
}

func TestProvisionTenantHome_Idempotent(t *testing.T) {
	svc, _ := newHomeDatabaseService(t, healthyNodes("n1", "n2"))
	ctx := context.Background()

	first, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("first ProvisionTenantHome: %v", err)
	}
	second, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("second ProvisionTenantHome: %v", err)
	}
	if second.ShardID != first.ShardID {
		t.Fatalf("re-provisioning claimed a new shard %d, want the existing %d", second.ShardID, first.ShardID)
	}
}

// rendezvousPlacementKV holds the first parties reads of key, each made,
// until all of them have arrived: every one of that many concurrent creates
// reads the entry as absent before any of them writes it.
type rendezvousPlacementKV struct {
	*memPlacementKV
	key string

	mu      sync.Mutex
	parties int
	arrived chan struct{}
}

func newRendezvousPlacementKV(key string, parties int) *rendezvousPlacementKV {
	return &rendezvousPlacementKV{memPlacementKV: newMemPlacementKV(), key: key, parties: parties, arrived: make(chan struct{})}
}

func (r *rendezvousPlacementKV) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.memPlacementKV.Get(ctx, key)
	if key != r.key {
		return v, err
	}
	r.mu.Lock()
	held := r.parties > 0
	if held {
		r.parties--
		if r.parties == 0 {
			close(r.arrived)
		}
	}
	r.mu.Unlock()
	if held {
		select {
		case <-r.arrived:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return v, err
}

// TestProvisionTenantHome_ConcurrentCallsSettleOnOnePlacement races two
// provisions of one tenant's home group (bootstrap.ProvisionTenant is not
// exclusive), each reading the placement as absent before either writes:
// every caller must get the one placement the table keeps.
func TestProvisionTenantHome_ConcurrentCallsSettleOnOnePlacement(t *testing.T) {
	const callers = 2
	kv := newRendezvousPlacementKV(placement.PlacementPrefix+"rt/"+database.TenantHomeName, callers)
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svcI, store := newPlacementDatabaseServiceOn(t, raftTenant(t), f, healthyNodes("n1", "n2", "n3"), kv)
	svc := svcI.(*DatabaseService)
	ctx := context.Background()

	plans := make([]*placement.Placement, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { plans[i], errs[i] = svc.ProvisionTenantHome(ctx, "rt") })
	}
	wg.Wait()

	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: ProvisionTenantHome: %v", i, errs[i])
		}
		if plans[i].ShardID != stored.ShardID {
			t.Fatalf("caller %d got home shard %d, the table keeps %d: a concurrent provision overwrote the placement", i, plans[i].ShardID, stored.ShardID)
		}
	}
}

// TestProvisionTenantHome_RefusesATableWithoutAtomicCreate proves a placement
// table that cannot create an entry only if it is absent places nothing,
// rather than falling back to a write that could overwrite another caller's.
func TestProvisionTenantHome_RefusesATableWithoutAtomicCreate(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svcI, store := newPlacementDatabaseServiceOn(t, raftTenant(t), f, healthyNodes("n1"), putOnlyPlacementKV{newMemPlacementKV()})
	ctx := context.Background()

	_, err := svcI.(*DatabaseService).ProvisionTenantHome(ctx, "rt")
	var unsupported *placement.AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *placement.AtomicWriteUnsupportedError", err)
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound", err)
	}
}

// putOnlyPlacementKV is a placement.KV with none of the guarded-write
// capabilities.
type putOnlyPlacementKV struct{ m *memPlacementKV }

func (p putOnlyPlacementKV) Put(ctx context.Context, key string, value []byte) error {
	return p.m.Put(ctx, key, value)
}

func (p putOnlyPlacementKV) Get(ctx context.Context, key string) ([]byte, error) {
	return p.m.Get(ctx, key)
}

func (p putOnlyPlacementKV) Delete(ctx context.Context, key string) error {
	return p.m.Delete(ctx, key)
}

func (p putOnlyPlacementKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return p.m.Scan(ctx, prefix, fn)
}

func TestProvisionTenantHome_UnplaceableWithoutPlacement(t *testing.T) {
	svc := &DatabaseService{}
	_, err := svc.ProvisionTenantHome(context.Background(), "rt")
	var want *TenantHomeUnplaceableError
	if !errors.As(err, &want) || want.TenantID != "rt" {
		t.Fatalf("err = %v, want *TenantHomeUnplaceableError for rt", err)
	}
	if want.Error() != "service: tenant rt home group cannot be placed: placement is not configured on this node" {
		t.Fatalf("Error() = %q", want.Error())
	}
}

func TestProvisionTenantHome_UnknownTenantPlacesNothing(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1"))
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "ghost"); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("err = %v, want ErrTenantNotFound", err)
	}
	if _, err := store.Get(ctx, "ghost", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound (nothing may be placed for an unknown tenant)", err)
	}
}

func TestProvisionTenantHome_NoHealthyNodesFails(t *testing.T) {
	svc, store := newHomeDatabaseService(t, nil)
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err == nil {
		t.Fatal("ProvisionTenantHome with no nodes succeeded, want a placement error")
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound", err)
	}
}

func TestRetireTenantHome_DeletesThePlacement(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2"))
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	if err := svc.RetireTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("RetireTenantHome: %v", err)
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound after retiring the home", err)
	}
	// A tenant with no home group is the success case, so a retry converges.
	if err := svc.RetireTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("second RetireTenantHome: %v", err)
	}
}

func TestRetireTenantHome_NoPlacementTableIsNoOp(t *testing.T) {
	svc := &DatabaseService{}
	if err := svc.RetireTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("RetireTenantHome without a placement table = %v, want nil", err)
	}
	var _ tenant.HomeRetirer = svc
}

// The home group's placement is not a database a tenant can scale: the
// request is refused as for a name with no dedicated-raft placement, and the
// group keeps the replication factor it was placed with.
func TestScaleForTenant_RefusesTenantHome(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()
	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, "rt")
	for _, name := range []string{database.TenantHomeName, "__HOME", "does-not-exist"} {
		_, err := svc.ScaleForTenant(tctx, name, 1)
		var nd *NotDedicatedRaftDatabaseError
		if !errors.As(err, &nd) || nd.Database != name {
			t.Fatalf("ScaleForTenant(%q) = %v (%T), want *NotDedicatedRaftDatabaseError", name, err, err)
		}
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if stored.RF != 3 {
		t.Fatalf("home group RF = %d after a refused scale, want 3", stored.RF)
	}
}

// Dropping the home group fails exactly as dropping a database that does not
// exist, and leaves its placement in place: only RetireTenantHome removes it.
func TestDropForTenant_RefusesTenantHome(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()
	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, "rt")
	unknown := svc.DropForTenant(tctx, "does-not-exist")
	if unknown == nil {
		t.Fatal("dropping an unknown database must fail")
	}
	for _, name := range []string{database.TenantHomeName, "__HOME"} {
		err := svc.DropForTenant(tctx, name)
		if err == nil || reflect.TypeOf(err) != reflect.TypeOf(unknown) {
			t.Fatalf("DropForTenant(%q) = %v (%T), want the unknown-database error %T", name, err, err, unknown)
		}
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("the home group's placement must survive a refused drop: %v", err)
	}
	if stored.Dropping {
		t.Fatal("a refused drop must not fence the home group")
	}
}
