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
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// journalStore returns a store holding a collection work journal entry for
// each (tenant, database) given, recorded at the given time, plus the
// DatabaseServices bundle over it.
func journalStore(t *testing.T, at time.Time, work ...[2]string) *DatabaseServices {
	t.Helper()
	kv := newMockKVService()
	for _, w := range work {
		j := &collectionWorkJournal{kv: kv, tenant: w[0], now: func() time.Time { return at }}
		if err := j.Record(context.Background(), w[1], "c"); err != nil {
			t.Fatal(err)
		}
	}
	return &DatabaseServices{KV: kv}
}

// TestServer_CollectionSweepTargets_OnlyWhatIsServedHere pins that the
// sweeper never becomes the reason a node starts a tenant's backend: a raft
// tenant's databases are swept only where their shard is already started --
// and led from this node -- and a volume tenant's only once its Host is
// already built.
//
// Mutation-checked: without the filter the unstarted raft database and the
// unbuilt volume tenant appear as targets, and resolving them would start
// them.
func TestServer_CollectionSweepTargets_OnlyWhatIsServedHere(t *testing.T) {
	tenants := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			{ID: tenant.SystemTenantID},
			{ID: "raft-t", IsolationType: tenant.IsolationRaft},
			{ID: "vol-up", IsolationType: tenant.IsolationVolume},
			{ID: "vol-down", IsolationType: tenant.IsolationVolume},
		}, nil
	}}
	hosts := NewTenantHostRegistry(&seal.BarrierRegistry{}, staticDataRoot{}, volTestLogger())
	hosts.hosts["vol-up"] = &tenantHost{tenantID: "vol-up", services: journalStore(t, time.Now(), [2]string{"vol-up", "system"})}
	// The shared store's journal: the system tenant's work, and a stale entry
	// of a tenant that no longer exists, which is removed rather than swept.
	shared := journalStore(t, time.Now().Add(-time.Hour), [2]string{tenant.SystemTenantID, "system"}, [2]string{"gone-t", "db"})
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", shared)
	raft := &RaftBackendFactory{
		shards: map[raftShardKey]*raftShard{
			{tenantID: "raft-t", database: "started"}:  {shardID: 7, replicaID: 1},
			{tenantID: "raft-t", database: "follower"}: {shardID: 8, replicaID: 1},
		},
		leaderOf: func(shardID uint64) (uint64, bool, error) {
			if shardID == 7 {
				return 1, true, nil
			}
			return 2, true, nil // another node's replica leads shard 8
		},
	}

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(dbReg),
		WithTenantService(tenants), WithTenantHostRegistry(hosts), WithRaftBackendFactory(raft))

	targets, err := srv.collectionSweepTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(targets))
	for i, tgt := range targets {
		got[i] = tgt.Tenant + "/" + tgt.Database
	}
	sort.Strings(got)
	want := []string{"raft-t/started", tenant.SystemTenantID + "/system", "vol-up/system"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
	if _, ok := shared.KV.(*mockKVService).store[collectionWorkKey("gone-t", "db", "c")]; ok {
		t.Fatal("a stale entry of a tenant that no longer exists is removed")
	}
	if (*TenantHostRegistry)(nil).Started("x") {
		t.Fatal("a nil registry has started nothing")
	}
}

// slowSweeper stands in for a database whose teardown runs until its context
// is cancelled.
type slowSweeper struct {
	service.CollectionServicer
	calls []string
}

var _ service.CollectionSweeper = (*slowSweeper)(nil)

func (s *slowSweeper) SweepStuckCollections(ctx context.Context, database string, _ time.Duration) (service.CollectionSweepResult, error) {
	s.calls = append(s.calls, database)
	if database == "slow" {
		<-ctx.Done()
		return service.CollectionSweepResult{}, ctx.Err()
	}
	return service.CollectionSweepResult{Swept: 1}, nil
}

// slowRegistry resolves every database to one slowSweeper.
type slowRegistry struct {
	service.Registry
	svc *slowSweeper
}

func (r *slowRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return r.svc, nil
}

// TestCollectionSweeper_ASlowDatabaseDoesNotBlockTheOthers pins the bound on
// per-database work: a teardown that runs long is cut off at the target
// timeout and resumes on the next pass, and the databases after it are swept
// in the same pass.
func TestCollectionSweeper_ASlowDatabaseDoesNotBlockTheOthers(t *testing.T) {
	svc := &slowSweeper{}
	sweeper := &collectionSweeper{
		registry:      &slowRegistry{svc: svc},
		targetTimeout: 10 * time.Millisecond,
		logger:        slog.Default(),
		targets: func(context.Context) ([]collectionSweepTarget, error) {
			return []collectionSweepTarget{
				{Tenant: tenant.SystemTenantID, Database: "slow"},
				{Tenant: tenant.SystemTenantID, Database: "after"},
			}, nil
		},
	}
	if got := sweeper.sweepOnce(context.Background()); got != 1 {
		t.Fatalf("swept %d, want 1 from the database after the slow one", got)
	}
	if len(svc.calls) != 2 || svc.calls[1] != "after" {
		t.Fatalf("calls = %v", svc.calls)
	}
}
