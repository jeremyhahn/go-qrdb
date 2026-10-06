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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	quicraftkv "github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// The drop fence is atomic with every write.
//
// These drive the real apply path: namespaced views over pkg/service
// servicers over a LocalBackend over a real MVCCEngine, and a real
// database.Manager whose Drop fences the store through a real
// service.KeyspaceReclaimService. The one thing a test controls is WHEN a
// guarded proposal is applied, which is what makes the race deterministic.
// ---------------------------------------------------------------------------

// proposalGate holds one guarded proposal between being proposed and being
// applied.
type proposalGate struct {
	proposed chan []byte
	release  chan struct{}
}

// gatedHost is a LocalBackend that can hold the next guarded proposal
// before it reaches the state machine -- a write that has been resolved,
// passed every check its caller makes, and been proposed, but whose log
// entry has not been applied yet. It records every proposal it applies.
type gatedHost struct {
	*backend.LocalBackend

	mu        sync.Mutex
	gate      *proposalGate
	proposals [][]byte
}

// holdNextGuardedProposal arms the gate for the next guarded proposal.
func (h *gatedHost) holdNextGuardedProposal() *proposalGate {
	g := &proposalGate{proposed: make(chan []byte, 1), release: make(chan struct{})}
	h.mu.Lock()
	h.gate = g
	h.mu.Unlock()
	return g
}

// takeGate disarms and returns the gate if cmd is a guarded proposal.
func (h *gatedHost) takeGate(cmd []byte) *proposalGate {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.proposals = append(h.proposals, append([]byte(nil), cmd...))
	if len(cmd) == 0 || cmd[0] != protocol.OpGuarded || h.gate == nil {
		return nil
	}
	g := h.gate
	h.gate = nil
	return g
}

// recorded returns the proposals made since from.
func (h *gatedHost) recorded(from int) [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.proposals[from:]...)
}

// mark returns the current proposal count.
func (h *gatedHost) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.proposals)
}

// SyncPropose applies cmd, first holding it at the gate when armed.
func (h *gatedHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	if g := h.takeGate(cmd); g != nil {
		g.proposed <- cmd
		<-g.release
	}
	return h.LocalBackend.SyncPropose(ctx, shardID, cmd)
}

// ProposeWithMode applies cmd through SyncPropose.
func (h *gatedHost) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
	return h.SyncPropose(ctx, shardID, cmd)
}

// newGatedHost returns a gatedHost over a fresh in-memory MVCCEngine.
func newGatedHost(t *testing.T) *gatedHost {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	lb := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = lb.Close() })
	return &gatedHost{LocalBackend: lb}
}

// rawServices returns the un-namespaced data-plane servicers over host.
func rawServices(host service.Host) *DatabaseServices {
	return &DatabaseServices{
		Name:        tenant.DefaultDatabase,
		KV:          service.NewKVService(host, shard.ShardKV),
		MVCC:        service.NewMVCCKVService(host, shard.ShardKV),
		Batch:       service.NewBatchService(host, shard.ShardKV),
		Transaction: service.NewTransactionService(host, shard.ShardKV),
		TimeSeries:  service.NewTimeSeriesService(host, shard.ShardKV),
		Index:       service.NewIndexService(host, shard.ShardKV),
	}
}

// sharedStoreFixture is a shared-store database "orders" of the system
// tenant, resolvable through a real registry and droppable through a real
// Manager whose reclaimer fences the same store.
type sharedStoreFixture struct {
	host *gatedHost
	raw  *DatabaseServices
	mgr  *database.Manager
	reg  *restServiceRegistry
}

func newSharedStoreFixture(t *testing.T) *sharedStoreFixture {
	t.Helper()
	host := newGatedHost(t)
	raw := rawServices(host)

	mgr := database.NewManager(nil, t.TempDir())
	mgr.SetKeyspaceReclaimer(service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: raw.KV, Index: raw.Index}))
	if _, err := mgr.Create(database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dbReg := NewDatabaseServiceRegistry(tenant.DefaultDatabase)
	dbReg.Register(tenant.DefaultDatabase, raw)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		withDatabaseManager(mgr)).(*restServiceRegistry)
	return &sharedStoreFixture{host: host, raw: raw, mgr: mgr, reg: reg}
}

// assertDroppingError fails unless err is the typed drop refusal for db.
func assertDroppingError(t *testing.T, err error, db string) {
	t.Helper()
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("error = %v (%T), want *database.DroppingError", err, err)
	}
	if dropping.Name != db {
		t.Fatalf("DroppingError.Name = %q, want %q", dropping.Name, db)
	}
	if !errors.Is(err, service.ErrNotStored) {
		t.Fatal("a write refused by the fence stored nothing, and must say so (service.ErrNotStored)")
	}
}

// TestDropFence_WriteResolvedBeforeDropIsRejectedAtApply is the race the
// fence exists for, forced into its worst order: the write resolves the
// database, passes every check, and is PROPOSED; only then does DROP
// DATABASE run to completion -- fence, reclamation, catalog removal -- and
// only after that is the write's entry applied. It must be refused by the
// state machine, and nothing may be left behind under the dropped
// database's namespace.
func TestDropFence_WriteResolvedBeforeDropIsRejectedAtApply(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()

	kv, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}

	gate := f.host.holdNextGuardedProposal()
	errc := make(chan error, 1)
	go func() { errc <- kv.Put(ctx, "k", []byte("late")) }()

	select {
	case <-gate.proposed:
	case <-time.After(10 * time.Second):
		t.Fatal("the write was never proposed")
	}

	if err := f.mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	close(gate.release)

	assertDroppingError(t, <-errc, "orders")

	keys, err := f.raw.KV.ListAll(ctx, tenant.ComposeNamespace(tenantid.System, "orders"))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("the refused write left %v in the dropped database's keyspace", keys)
	}
}

// TestDropFence_WriteAppliedBeforeTheFenceIsReclaimed is the other order: a
// write whose entry applies BEFORE the fence landed and is removed by the
// reclamation that follows it, because the reclamation's scan runs after
// the fence has applied.
func TestDropFence_WriteAppliedBeforeTheFenceIsReclaimed(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()

	kv, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if err := kv.Put(ctx, "k", []byte("early")); err != nil {
		t.Fatalf("Put before the drop: %v", err)
	}
	if err := f.mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	keys, err := f.raw.KV.ListAll(ctx, tenant.ComposeNamespace(tenantid.System, "orders"))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("a write applied before the fence survived the drop: %v", keys)
	}
}

// TestDropFence_HandlesAreIncarnationScoped pins what a stale handle can
// reach: a handle resolved before a drop stays refused after the drop, and
// STILL refused once the name is created again -- the new database is a
// new incarnation, and the old handle's guard names the old one -- while a
// handle resolved against the new database writes normally.
func TestDropFence_HandlesAreIncarnationScoped(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()

	stale, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if err := f.mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	assertDroppingError(t, stale.Put(ctx, "k", []byte("v")), "orders")

	if _, err := f.mgr.Create(database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	assertDroppingError(t, stale.Put(ctx, "k", []byte("into the new database")), "orders")

	fresh, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService after re-create: %v", err)
	}
	if err := fresh.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put into the re-created database: %v", err)
	}
	got, err := fresh.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("re-created database holds (%q, %v), want only its own write", got, err)
	}
}

// TestDropFence_FailedReclamationRollsTheFenceBack proves a drop that fails
// after fencing leaves the database writable again: the store marker is
// restored to the database's own incarnation -- not merely removed -- and
// the local fence is cleared, and a handle resolved before the failed drop
// writes as well as a fresh one.
func TestDropFence_FailedReclamationRollsTheFenceBack(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()
	db, err := f.mgr.Get("orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	before, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	f.mgr.SetKeyspaceReclaimer(&failingReclaimAfterFence{KeyspaceReclaimService: service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: f.raw.KV})})

	var reclaimErr *database.KeyspaceReclaimError
	if err := f.mgr.Drop(ctx, "orders"); !errors.As(err, &reclaimErr) {
		t.Fatalf("Drop error = %v, want *database.KeyspaceReclaimError", err)
	}

	marker, err := f.raw.KV.Get(ctx, service.DropFenceKey(tenantid.System, "orders"))
	if err != nil || string(marker) != db.Incarnation() {
		t.Fatalf("marker after a failed drop = (%q, %v), want the database's incarnation %q", marker, err, db.Incarnation())
	}
	if db.Dropping() {
		t.Fatal("the local fence survived a rollback whose store fence was cleared")
	}
	if err := before.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put on a handle from before the failed drop = %v, want success", err)
	}
	kv, err := f.reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService after a failed drop: %v", err)
	}
	if err := kv.Put(ctx, "k2", []byte("v")); err != nil {
		t.Fatalf("Put after a failed drop = %v, want success (the fence must be rolled back)", err)
	}
}

// failingReclaimAfterFence fences and unfences for real and fails the
// reclamation.
type failingReclaimAfterFence struct {
	*service.KeyspaceReclaimService
}

func (f *failingReclaimAfterFence) ReclaimDatabaseKeyspace(context.Context, string, string, string) error {
	return errors.New("simulated reclamation failure")
}

// stubRaftFence is a raftFenceSource answering with one dedicated shard's
// services.
type stubRaftFence struct {
	svcs *DatabaseServices
	err  error
}

func (s *stubRaftFence) FenceServices(context.Context, string, string) (*DatabaseServices, error) {
	return s.svcs, s.err
}

// TestDropFence_DedicatedRaftWriteResolvedBeforeDropIsRejectedAtApply is the
// same race for a dedicated-raft database: its writes go to its OWN shard,
// so the fence has to be written there -- not in the system store -- for
// the shard's state machine to refuse the late write.
func TestDropFence_DedicatedRaftWriteResolvedBeforeDropIsRejectedAtApply(t *testing.T) {
	ctx := context.Background()
	sysHost := newGatedHost(t)
	sys := rawServices(sysHost)
	dedHost := newGatedHost(t)
	ded := rawServices(dedHost)

	reclaim := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: sys.KV, Index: sys.Index})
	reclaim.SetStoreResolver(&storeResolver{
		raft:         &stubRaftFence{svcs: ded},
		tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationRaft},
		systemTenant: tenantid.System,
	})
	mgr := database.NewManager(nil, t.TempDir())
	mgr.SetKeyspaceReclaimer(reclaim)
	tctx := tenant.WithTenantID(ctx, "rt")
	if _, err := mgr.CreateForTenant(tctx, database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	// Exactly the views resolve's IsolationRaft branch builds.
	fence, err := newDatabaseFence("rt", "orders", "", nil)
	if err != nil {
		t.Fatalf("newDatabaseFence: %v", err)
	}
	views := fence.apply(ded, "", false)

	gate := dedHost.holdNextGuardedProposal()
	errc := make(chan error, 1)
	go func() { errc <- views.KV.Put(ctx, "k", []byte("late")) }()
	select {
	case <-gate.proposed:
	case <-time.After(10 * time.Second):
		t.Fatal("the write was never proposed")
	}

	if err := mgr.DropForTenant(tctx, "orders"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	close(gate.release)

	assertDroppingError(t, <-errc, "orders")

	fenceKey := service.DropFenceKey("rt", "orders")
	if _, err := ded.KV.Get(ctx, fenceKey); err != nil {
		t.Fatalf("the fence is not in the database's own shard: %v", err)
	}
	if _, err := sys.KV.Get(ctx, fenceKey); err == nil {
		t.Fatal("the fence was written to the system store, where the database's writes are not guarded")
	}
	if _, err := ded.KV.Get(ctx, "k"); err == nil {
		t.Fatal("the refused write landed in the dedicated shard")
	}
}

// servicerVerbs classifies every method of the servicer interfaces a
// database's views implement. writes are the verbs that propose; reads are
// the verbs that do not. TestDropFence_EveryWriteVerbCarriesTheGuard
// enumerates the interfaces by reflection and fails on any method in
// neither, so a verb added to an interface is classified -- and, if it
// writes, exercised -- before it can ship unguarded.
var servicerReadVerbs = map[string]bool{
	"Get": true, "GetStale": true, "GetWithConsistency": true, "GetEntity": true,
	"List": true, "ListAll": true, "ListStale": true, "ListWithConsistency": true, "ListEntities": true,
	"Scan": true, "ScanStale": true, "ScanWithConsistency": true, "ExistsWithConsistency": true,
	"GetRevision": true, "GetRevisionWithConsistency": true, "GetAtRevision": true, "GetAtRevisionWithConsistency": true,
	"Range": true, "RangeWithConsistency": true, "CurrentRevision": true,
	"Query": true, "QueryWithConsistency": true, "QueryRich": true, "QueryRichWithConsistency": true,
	"Aggregate": true, "AggregateWithConsistency": true, "GetRetentionPolicy": true,
	"ListMetrics": true, "ListMetricsWithConsistency": true, "ListIndexes": true,
	"GetCollectionInfo": true, "ListCollections": true, "GetDocument": true, "ListDocuments": true,
}

// kvWriteVerbs returns the write verbs of a KVServicer view.
func kvWriteVerbs(ctx context.Context, kv service.KVServicer, suffix string) map[string]func() error {
	return map[string]func() error{
		"Put":              func() error { return kv.Put(ctx, "a"+suffix, []byte("v")) },
		"PutIfAbsent":      func() error { _, err := kv.PutIfAbsent(ctx, "pia"+suffix, []byte("v")); return err },
		"CompareAndSwap":   func() error { _, err := kv.CompareAndSwap(ctx, "a"+suffix, []byte("v"), []byte("w")); return err },
		"CompareAndDelete": func() error { _, err := kv.CompareAndDelete(ctx, "a"+suffix, []byte("w")); return err },
		"Delete":           func() error { return kv.Delete(ctx, "pia"+suffix) },
		"PutWithMode":      func() error { return kv.PutWithMode(ctx, "m"+suffix, []byte("v"), writemode.Sync) },
		"DeleteWithMode":   func() error { return kv.DeleteWithMode(ctx, "m"+suffix, writemode.Sync) },
		"PutEntity": func() error {
			_, err := kv.PutEntity(ctx, &service.PutEntityRequest{EntityType: "widget", ID: "1" + suffix, Data: []byte("v")})
			return err
		},
		"DeleteEntity": func() error {
			_, err := kv.DeleteEntity(ctx, &service.DeleteEntityRequest{EntityType: "widget", ID: "1" + suffix})
			return err
		},
	}
}

// TestDropFence_EveryWriteVerbCarriesTheGuard drives every write verb of
// every guarded view -- including the collection service built over them --
// and asserts that each proposal it makes is an OpGuarded command carrying
// the database's fence at its incarnation. It is the conformance check that
// replaces "remember to re-check the fence in every method": the verb set
// comes from the interfaces themselves, so a verb that proposed without the
// guard, or a new verb nobody classified, fails here.
func TestDropFence_EveryWriteVerbCarriesTheGuard(t *testing.T) {
	host := newGatedHost(t)
	raw := rawServices(host)
	fence, err := newDatabaseFence("acme", "orders", "inc-1", nil)
	if err != nil {
		t.Fatalf("newDatabaseFence: %v", err)
	}
	reg := NewRESTServiceRegistry(NewDatabaseServiceRegistry(tenant.DefaultDatabase), nil, tenantid.System).(*restServiceRegistry)
	views := reg.applyComposite(raw, "acme", "orders", fence)
	ctx := context.Background()
	now := time.Now()
	// The live incarnation, as the database's create marks it: without it
	// every guarded write is refused, and a verb that reads back what an
	// earlier one wrote (a collection's documents) never gets to propose.
	if err := raw.KV.Put(ctx, service.DropFenceKey("acme", "orders"), []byte("inc-1")); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	putOp := []service.Op{{Type: service.OpPut, Key: []byte("b"), Value: []byte("v")}}
	var docID uint64

	mvccWrites := kvWriteVerbs(ctx, views.MVCC, "-mvcc")
	mvccWrites["PutWithLease"] = func() error { _, err := views.MVCC.PutWithLease(ctx, "l", []byte("v"), 0); return err }
	mvccWrites["PutWithLeaseAndMode"] = func() error {
		_, err := views.MVCC.PutWithLeaseAndMode(ctx, "lm", []byte("v"), 0, writemode.Sync)
		return err
	}
	mvccWrites["Compact"] = func() error { return views.MVCC.Compact(ctx, 1) }
	mvccWrites["RestoreToRevision"] = func() error {
		// Change a key after a known revision, so restoring to that
		// revision has something to write back.
		first, err := views.MVCC.PutWithLease(ctx, "restore/k", []byte("before"), 0)
		if err != nil {
			return err
		}
		if err := views.MVCC.Put(ctx, "restore/k", []byte("after")); err != nil {
			return err
		}
		_, err = views.MVCC.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: first.ModRevision, Prefix: "restore/"})
		return err
	}

	servicers := []struct {
		name   string
		iface  reflect.Type
		writes map[string]func() error
	}{
		{"KV", reflect.TypeOf((*service.KVServicer)(nil)).Elem(), kvWriteVerbs(ctx, views.KV, "")},
		{"MVCC", reflect.TypeOf((*service.MVCCKVServicer)(nil)).Elem(), mvccWrites},
		{"Batch", reflect.TypeOf((*service.BatchServicer)(nil)).Elem(), map[string]func() error{
			"Execute":         func() error { _, err := views.Batch.Execute(ctx, putOp); return err },
			"ExecuteWithMode": func() error { _, err := views.Batch.ExecuteWithMode(ctx, putOp, writemode.Sync); return err },
			"ExecuteEntity": func() error {
				_, err := views.Batch.ExecuteEntity(ctx, &service.BatchEntityRequest{Ops: []service.BatchEntityOp{{Type: "put", EntityType: "widget", ID: "2", Data: []byte("v")}}})
				return err
			},
			"ImportBegin": func() error {
				_, err := views.Batch.ImportBegin(ctx, service.ImportOptions{Format: "json", KeyField: "id", ValueMode: "row"})
				return err
			},
			"ImportAppend": func() error {
				sid, err := views.Batch.ImportBegin(ctx, service.ImportOptions{Format: "json", KeyField: "id", ValueMode: "row"})
				if err != nil {
					return err
				}
				return views.Batch.ImportAppend(ctx, sid, 0, []byte(`[{"id":"i2","v":"x"}]`))
			},
			"ImportCommit": func() error {
				sid, err := views.Batch.ImportBegin(ctx, service.ImportOptions{Format: "json", KeyField: "id", ValueMode: "row"})
				if err != nil {
					return err
				}
				if err := views.Batch.ImportAppend(ctx, sid, 0, []byte(`[{"id":"i3","v":"x"}]`)); err != nil {
					return err
				}
				_, err = views.Batch.ImportCommit(ctx, sid)
				return err
			},
			"ImportAbort": func() error {
				sid, err := views.Batch.ImportBegin(ctx, service.ImportOptions{Format: "json", KeyField: "id", ValueMode: "row"})
				if err != nil {
					return err
				}
				return views.Batch.ImportAbort(ctx, sid)
			},
		}},
		{"Transaction", reflect.TypeOf((*service.TransactionServicer)(nil)).Elem(), map[string]func() error{
			"Execute": func() error { _, err := views.Transaction.Execute(ctx, nil, putOp, nil); return err },
			"ExecuteWithMode": func() error {
				_, err := views.Transaction.ExecuteWithMode(ctx, nil, putOp, nil, writemode.Sync)
				return err
			},
			"ExecuteEntity": func() error {
				_, err := views.Transaction.ExecuteEntity(ctx, &service.TransactionEntityRequest{Success: []service.TransactionEntityOp{{Type: "put", EntityType: "widget", ID: "3", Data: []byte("v")}}})
				return err
			},
		}},
		{"TimeSeries", reflect.TypeOf((*service.TimeSeriesServicer)(nil)).Elem(), map[string]func() error{
			"Write": func() error { return views.TimeSeries.Write(ctx, "cpu", 1, now, nil) },
			"WriteBatch": func() error {
				return views.TimeSeries.WriteBatch(ctx, []timeseries.DataPoint{{Metric: "cpu", Value: 1, Timestamp: now}})
			},
			"WritePoint": func() error {
				return views.TimeSeries.WritePoint(ctx, &timeseries.DataPoint{Metric: "cpu", Value: 1, Timestamp: now})
			},
			"DeleteSeries": func() error {
				return views.TimeSeries.DeleteSeries(ctx, &timeseries.DeleteRequest{Metric: "cpu", StartTime: now.Add(-time.Hour), EndTime: now.Add(time.Hour)})
			},
			"SetRetentionPolicy": func() error {
				return views.TimeSeries.SetRetentionPolicy(ctx, "cpu", &timeseries.RetentionPolicy{Duration: time.Hour})
			},
		}},
		{"Index", reflect.TypeOf((*service.IndexServicer)(nil)).Elem(), map[string]func() error{
			"CreateIndex": func() error { return views.Index.CreateIndex(ctx, "by_name", "name", index.IndexConfig{}) },
			"DropIndex":   func() error { return views.Index.DropIndex(ctx, "by_name") },
			// The decision is taken on the apply path, not here, so a
			// mismatched owner still proposes -- the verb's own outcome is
			// not under test (see the comment above this loop).
			"DropOwnedIndex": func() error { return views.Index.DropOwnedIndex(ctx, "by_name", "someone-else") },
		}},
		{"Collection", reflect.TypeOf((*service.CollectionServicer)(nil)).Elem(), map[string]func() error{
			"CreateCollection": func() error {
				_, err := views.Collection.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "orders", Name: "users"})
				return err
			},
			"InsertDocument": func() error {
				resp, err := views.Collection.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "orders", Collection: "users", Document: map[string]any{"name": "a"}})
				if err == nil {
					docID = resp.ID
				}
				return err
			},
			"UpsertDocument": func() error {
				_, err := views.Collection.UpsertDocument(ctx, &service.UpsertDocumentRequest{Database: "orders", Collection: "users", ID: 1, Document: map[string]any{"name": "a-upsert"}})
				return err
			},
			"UpdateDocument": func() error {
				_, err := views.Collection.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "orders", Collection: "users", ID: docID, Document: map[string]any{"name": "b"}})
				return err
			},
			"DeleteDocument": func() error {
				_, err := views.Collection.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "orders", Collection: "users", ID: docID})
				return err
			},
			"DropCollection": func() error {
				return views.Collection.DropCollection(ctx, &service.DropCollectionRequest{Database: "orders", Name: "users"})
			},
		}},
	}

	fenceKey := []byte(service.DropFenceKey("acme", "orders"))
	for _, sv := range servicers {
		// Deterministic order, so a verb whose setup another relies on
		// (CreateCollection before InsertDocument) runs first.
		names := make([]string, 0, sv.iface.NumMethod())
		for i := 0; i < sv.iface.NumMethod(); i++ {
			names = append(names, sv.iface.Method(i).Name)
		}
		sort.SliceStable(names, func(i, j int) bool { return verbOrder(names[i]) < verbOrder(names[j]) })
		for _, name := range names {
			call, isWrite := sv.writes[name]
			if !isWrite {
				if !servicerReadVerbs[name] {
					t.Errorf("%s.%s is not classified: add it to its write verbs (and exercise it) or to servicerReadVerbs", sv.name, name)
				}
				continue
			}
			t.Run(sv.name+"."+name, func(t *testing.T) {
				from := host.mark()
				// The verb's own outcome is not under test -- a time-series
				// write refused by an engine built without time-series still
				// proposed.
				callErr := call()
				proposals := host.recorded(from)
				if len(proposals) == 0 {
					t.Fatalf("the verb made no proposal (it returned %v), so this test cannot see whether it is guarded", callErr)
				}
				for i, cmd := range proposals {
					if isCollectionWorkJournalWrite(cmd) {
						// CreateCollection and DropCollection record the
						// transition in the collection work journal before
						// their guarded metadata write (CollectionService.
						// recordWork): a global, cross-database index the
						// sweeper scans, deliberately kept OUTSIDE every
						// database's keyspace (go-qrdb#547) and therefore
						// outside this fence too. An entry outliving a
						// transition the fence refused is harmless -- the
						// sweeper reclaims it once the collection it names
						// never becomes active (CollectionWorkJournal).
						continue
					}
					compares, _, err := protocol.DecodeGuarded(cmd[1:])
					if cmd[0] != protocol.OpGuarded || err != nil {
						t.Fatalf("proposal %d (opcode %d) is not guarded: the fence cannot refuse it", i, cmd[0])
					}
					if len(compares) != 1 || string(compares[0].Key) != string(fenceKey) || string(compares[0].Value) != "inc-1" {
						t.Fatalf("proposal %d is guarded by %+v, want exactly the fence of acme/orders at incarnation inc-1", i, compares)
					}
				}
			})
		}
	}
}

// isCollectionWorkJournalWrite reports whether cmd is a plain (unguarded) KV
// put to the collection work journal's key space (see collectionWorkJournal),
// which lives outside every database's keyspace by design and therefore
// outside this test's per-database fence check.
func isCollectionWorkJournalWrite(cmd []byte) bool {
	c, err := quicraftkv.DecodeCommand(cmd)
	if err != nil || c.Op != quicraftkv.OpPut {
		return false
	}
	return strings.HasPrefix(string(c.Key), collectionWorkPrefix)
}

// verbOrder sorts setup verbs ahead of the verbs that depend on them.
func verbOrder(name string) int {
	switch name {
	case "CreateCollection", "Put", "CreateIndex":
		return 0
	case "InsertDocument":
		return 1
	case "UpdateDocument":
		return 2
	case "DeleteDocument", "DropIndex":
		return 3
	case "DropCollection":
		return 4
	default:
		return 1
	}
}

// TestDropFence_DefaultDatabaseViewsCarryNoGuard pins the other half of
// newDatabaseFence: the default database cannot be dropped, so its writes
// pay for no guard at all.
func TestDropFence_DefaultDatabaseViewsCarryNoGuard(t *testing.T) {
	for _, db := range []string{"", tenant.DefaultDatabase} {
		fence, err := newDatabaseFence("acme", db, "inc-1", nil)
		if err != nil {
			t.Fatalf("newDatabaseFence(%q): %v", db, err)
		}
		if !fence.guard.IsZero() {
			t.Fatalf("newDatabaseFence(%q) carries a guard; the default database cannot be dropped", db)
		}
	}
}

// TestGuardedCollectionService_ScopesOnlyWrites proves the collection
// wrapper a dedicated-raft database's views use attaches the guard to the
// context of its writes and leaves reads alone.
func TestGuardedCollectionService_ScopesOnlyWrites(t *testing.T) {
	guard, err := service.DropFenceGuard("rt", "orders", "")
	if err != nil {
		t.Fatalf("DropFenceGuard: %v", err)
	}
	inner := &ctxRecordingCollection{}
	svc := &guardedCollectionService{dbGuard: dbGuard{guard: guard}, inner: inner}
	ctx := context.Background()

	writes := map[string]func(){
		"CreateCollection": func() { _, _ = svc.CreateCollection(ctx, &service.CreateCollectionRequest{}) },
		"DropCollection":   func() { _ = svc.DropCollection(ctx, &service.DropCollectionRequest{}) },
		"InsertDocument":   func() { _, _ = svc.InsertDocument(ctx, &service.InsertDocumentRequest{}) },
		"UpdateDocument":   func() { _, _ = svc.UpdateDocument(ctx, &service.UpdateDocumentRequest{}) },
		"DeleteDocument":   func() { _, _ = svc.DeleteDocument(ctx, &service.DeleteDocumentRequest{}) },
	}
	for name, call := range writes {
		call()
		if service.WriteGuardFrom(inner.last).IsZero() {
			t.Errorf("%s reached the collection service without the write guard", name)
		}
	}
	reads := map[string]func(){
		"ListCollections":   func() { _, _ = svc.ListCollections(ctx, &service.ListCollectionsRequest{}) },
		"GetCollectionInfo": func() { _, _ = svc.GetCollectionInfo(ctx, "orders", "c") },
		"GetDocument":       func() { _, _ = svc.GetDocument(ctx, &service.GetDocumentRequest{}) },
		"ListDocuments":     func() { _, _ = svc.ListDocuments(ctx, &service.ListDocumentsRequest{}) },
	}
	for name, call := range reads {
		call()
		if !service.WriteGuardFrom(inner.last).IsZero() {
			t.Errorf("%s is a read and must not be scoped", name)
		}
	}
}

// ctxRecordingCollection records the context of the last call.
type ctxRecordingCollection struct {
	last context.Context
}

var _ service.CollectionServicer = (*ctxRecordingCollection)(nil)

func (c *ctxRecordingCollection) CreateCollection(ctx context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	c.last = ctx
	return &service.CollectionInfo{}, nil
}

func (c *ctxRecordingCollection) DropCollection(ctx context.Context, _ *service.DropCollectionRequest) error {
	c.last = ctx
	return nil
}

func (c *ctxRecordingCollection) ListCollections(ctx context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	c.last = ctx
	return &service.ListCollectionsResponse{}, nil
}

func (c *ctxRecordingCollection) GetCollectionInfo(ctx context.Context, _, _ string) (*service.CollectionInfo, error) {
	c.last = ctx
	return &service.CollectionInfo{}, nil
}

func (c *ctxRecordingCollection) InsertDocument(ctx context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	c.last = ctx
	return &service.InsertDocumentResponse{}, nil
}

func (c *ctxRecordingCollection) GetDocument(ctx context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	c.last = ctx
	return &service.GetDocumentResponse{}, nil
}

func (c *ctxRecordingCollection) UpdateDocument(ctx context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	c.last = ctx
	return &service.UpdateDocumentResponse{}, nil
}

func (c *ctxRecordingCollection) DeleteDocument(ctx context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	c.last = ctx
	return &service.DeleteDocumentResponse{}, nil
}

func (c *ctxRecordingCollection) UpsertDocument(ctx context.Context, _ *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	c.last = ctx
	return &service.UpsertDocumentResponse{}, nil
}

func (c *ctxRecordingCollection) ListDocuments(ctx context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	c.last = ctx
	return &service.ListDocumentsResponse{}, nil
}
