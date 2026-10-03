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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// These cover go-qrdb#547's reclaim routing: which node sweeps a pending
// job, and which store it is read from. Getting either wrong is silent --
// the job is looked for where it never was, or never looked for at all.

// stubReclaimRegistry answers resolve/resolveSharedStore with fixed results.
type stubReclaimRegistry struct {
	svcs       *DatabaseServices
	err        error
	sharedSvcs *DatabaseServices
	sharedErr  error
}

func (s *stubReclaimRegistry) resolve(string, string) (*DatabaseServices, error) {
	return s.svcs, s.err
}

func (s *stubReclaimRegistry) resolveSharedStore(string, string) (*DatabaseServices, error) {
	return s.sharedSvcs, s.sharedErr
}

// stubShardLeader leads exactly the databases it lists.
type stubShardLeader struct{ leads map[string]bool }

func (s *stubShardLeader) LeadsDatabase(tenantID, db string) bool { return s.leads[tenantID+"/"+db] }

func entry(placement service.StorePlacement, origin string) service.ReclaimIndexEntry {
	return service.ReclaimIndexEntry{JobID: "j", TenantID: "acme", Database: "shop", Placement: placement, Origin: origin}
}

// ---------------------------------------------------------------------------
// Ownership
// ---------------------------------------------------------------------------

func TestReclaimStoreResolver_Owns(t *testing.T) {
	leader := &stubShardLeader{leads: map[string]bool{"acme/shop": true}}
	r := newReclaimStoreResolver(&stubReclaimRegistry{}, leader, "2")
	noShards := newReclaimStoreResolver(&stubReclaimRegistry{}, (*RaftBackendFactory)(nil), "2")
	noOrigin := newReclaimStoreResolver(&stubReclaimRegistry{}, nil, "")

	cases := []struct {
		name         string
		resolver     *reclaimStoreResolver
		entry        service.ReclaimIndexEntry
		systemLeader bool
		want         bool
	}{
		{"shared on the system leader", r, entry(service.StorePlacementShared, "9"), true, true},
		{"shared on a follower", r, entry(service.StorePlacementShared, "2"), false, false},
		{"node-local on its origin, not the leader", r, entry(service.StorePlacementNode, "2"), false, true},
		{"node-local elsewhere, even on the leader", r, entry(service.StorePlacementNode, "9"), true, false},
		{"node-local with no origin configured", noOrigin, entry(service.StorePlacementNode, ""), true, false},
		{"dedicated group this node leads", r, service.ReclaimIndexEntry{TenantID: "acme", Database: "shop", Placement: service.StorePlacementShard}, false, true},
		{"dedicated group led elsewhere, even on the system leader", r, service.ReclaimIndexEntry{TenantID: "acme", Database: "other", Placement: service.StorePlacementShard}, true, false},
		{"dedicated group with no raft backend", noShards, entry(service.StorePlacementShard, ""), true, false},
		{"unknown placement has no owner", r, entry("future", "2"), true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resolver.Owns(tc.entry, tc.systemLeader); got != tc.want {
				t.Fatalf("Owns = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

func TestReclaimStoreResolver_SharedUsesTheSharedViewNotResolve(t *testing.T) {
	mvcc, txn := newMockMVCCForReclaim(), &markedTxn{}
	reg := &stubReclaimRegistry{
		err:        &rest.DatabaseNotFoundError{TenantID: "acme", Database: "shop"},
		sharedSvcs: &DatabaseServices{MVCC: mvcc, Transaction: txn, Placement: service.StorePlacementShared},
	}
	got, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementShared, "1"))
	if err != nil {
		t.Fatalf("ReclaimStore: %v", err)
	}
	requireResolvedTo(t, got, mvcc, txn)
}

// requireResolvedTo asserts store reads through mvcc and deletes through
// txn -- the database's own pair, the one its TableService commits through.
func requireResolvedTo(t *testing.T, store service.ReclaimStore, mvcc *markedMVCC, txn *markedTxn) {
	t.Helper()
	if _, err := store.GetRevision(context.Background(), "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if _, err := store.Execute(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !mvcc.read || !txn.executed {
		t.Fatalf("store must read through the database's MVCC view (%v) and delete through its transaction service (%v)", mvcc.read, txn.executed)
	}
}

func TestReclaimStoreResolver_SharedResolutionErrorIsTransient(t *testing.T) {
	reg := &stubReclaimRegistry{sharedErr: &rest.DatabaseNotFoundError{}}
	_, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementShared, "1"))
	var gone *service.ReclaimStoreGoneError
	if err == nil || errors.As(err, &gone) {
		t.Fatalf("err = %v: a shared store is never reported gone by resolution", err)
	}
}

func TestReclaimStoreResolver_NodeLocalRoutesThroughResolve(t *testing.T) {
	mvcc, txn := newMockMVCCForReclaim(), &markedTxn{}
	reg := &stubReclaimRegistry{svcs: &DatabaseServices{MVCC: mvcc, Transaction: txn, Placement: service.StorePlacementNode}}
	got, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1"))
	if err != nil {
		t.Fatalf("ReclaimStore: %v", err)
	}
	requireResolvedTo(t, got, mvcc, txn)
}

func TestReclaimStoreResolver_DroppedNodeLocalDatabaseIsGone(t *testing.T) {
	reg := &stubReclaimRegistry{err: &rest.DatabaseNotFoundError{TenantID: "acme", Database: "shop"}}
	_, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1"))
	var gone *service.ReclaimStoreGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("err = %v, want *service.ReclaimStoreGoneError", err)
	}
}

func TestReclaimStoreResolver_NotFoundForADedicatedGroupIsNotGone(t *testing.T) {
	reg := &stubReclaimRegistry{err: &rest.DatabaseNotFoundError{}}
	_, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementShard, ""))
	var gone *service.ReclaimStoreGoneError
	if err == nil || errors.As(err, &gone) {
		t.Fatalf("err = %v: only the publishing node's own catalog may declare a store gone", err)
	}
}

func TestReclaimStoreResolver_OtherErrorsAreTransient(t *testing.T) {
	boom := errors.New("barrier sealed")
	reg := &stubReclaimRegistry{err: boom}
	_, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolution error", err)
	}
}

// TestReclaimStoreResolver_PlacementMismatchIsRefused pins the fail-closed
// rule: when live routing no longer leads to the kind of store the job was
// published in, the sweeper must not go looking for it there.
func TestReclaimStoreResolver_PlacementMismatchIsRefused(t *testing.T) {
	reg := &stubReclaimRegistry{svcs: &DatabaseServices{MVCC: newMockMVCCForReclaim(), Placement: service.StorePlacementShared}}
	_, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1"))
	var mismatch *ReclaimPlacementMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *ReclaimPlacementMismatchError", err)
	}
	if !strings.Contains(mismatch.Error(), `"node"`) || !strings.Contains(mismatch.Error(), `"shared"`) {
		t.Errorf("error must name both placements: %v", mismatch)
	}
}

func TestReclaimStoreResolver_StoreWithoutMVCCIsAnError(t *testing.T) {
	reg := &stubReclaimRegistry{svcs: &DatabaseServices{Placement: service.StorePlacementNode}}
	if _, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1")); err == nil {
		t.Fatal("a store with no MVCC surface cannot be swept")
	}
}

func TestReclaimStoreResolver_StoreWithoutTransactionsIsAnError(t *testing.T) {
	reg := &stubReclaimRegistry{svcs: &DatabaseServices{MVCC: newMockMVCCForReclaim(), Placement: service.StorePlacementNode}}
	if _, err := newReclaimStoreResolver(reg, nil, "1").ReclaimStore(context.Background(), entry(service.StorePlacementNode, "1")); err == nil {
		t.Fatal("a store with no transaction surface cannot delete a page in one proposal")
	}
}

func TestRaftBackendFactory_LeadsDatabaseWithoutAStartedShardIsFalse(t *testing.T) {
	if (*RaftBackendFactory)(nil).LeadsDatabase("acme", "shop") {
		t.Error("a nil factory leads nothing")
	}
	f := &RaftBackendFactory{shards: map[raftShardKey]*raftShard{}}
	if f.LeadsDatabase("acme", "shop") {
		t.Error("a factory with no host leads nothing")
	}
}

// ---------------------------------------------------------------------------
// End to end over the real registry (the codex round-2 production path)
// ---------------------------------------------------------------------------

// TestReclaim_NamespacedTruncateIsDrainedThroughTheRegistry drives the path
// a prefix-isolated tenant's TRUNCATE actually takes: TableService resolved
// through the registry (so every key it writes is composite-namespaced
// "<tenant>/<db>/"), the index entry published un-namespaced in the system
// store, and the sweeper resolving the job back through the same registry.
// The fixture is not hand-written: an earlier revision's test wrote
// unscoped records and so could not see that scoped ones were never found.
func TestReclaim_NamespacedTruncateIsDrainedThroughTheRegistry(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, sys := newLockDataPlane(t, "system")
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", sys)
	reg := newRESTRegistry(dbReg, nil, tenantid.System,
		WithSharedSQLDefaultDatabase("shop"), withReclaimOrigin("1"))

	tables, err := reg.TableService("acme", "shop")
	if err != nil {
		t.Fatalf("TableService: %v", err)
	}
	def, err := tables.CreateTable(ctx, &service.CreateTableRequest{
		Database: "shop", Name: "t",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	kv, err := reg.KVService("acme", "shop")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	oldRows := service.TableDataPrefix("shop", def.TableID, 0)
	if err := kv.Put(ctx, oldRows+"row", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := tables.TruncateTable(ctx, "shop", "t", true); err != nil {
		t.Fatalf("TruncateTable: %v", err)
	}

	ns := tenant.ComposeNamespace("acme", "shop")
	// MVCC ListAll, not KV ListAll: the store machine's KV listing leaves
	// out root-level "__" keys, and the index entries are exactly that.
	rawKeys := func(prefix string) []string {
		keys, lerr := sys.MVCC.ListAll(context.Background(), prefix)
		if lerr != nil {
			t.Fatalf("ListAll(%q): %v", prefix, lerr)
		}
		return keys
	}
	if len(rawKeys(ns+oldRows)) != 1 || len(rawKeys(ns+"__reclaim:job/")) != 1 {
		t.Fatal("precondition: the row and the job are written namespaced")
	}
	if len(rawKeys("__reclaim:index/")) != 1 {
		t.Fatal("precondition: the index entry is written un-namespaced in the system store")
	}

	now := time.Unix(1_700_000_000, 0)
	sweeper := service.NewReclaimSweeper(sys.MVCC, service.WithReclaimClock(func() time.Time { return now }))
	sweeper.SetStoreResolver(newReclaimStoreResolver(reg, nil, "1"))
	sweeper.SweepOnce(context.Background(), nil, nil)
	now = now.Add(service.ReclaimQuiescence)
	sweeper.SweepOnce(context.Background(), nil, nil)

	for _, prefix := range []string{ns + oldRows, ns + "__reclaim:job/", "__reclaim:index/"} {
		if keys := rawKeys(prefix); len(keys) != 0 {
			t.Errorf("keys left under %q: %v", prefix, keys)
		}
	}
}

// TestRegistry_TableServicePublishesPlacementAndOrigin proves the registry
// hands TableService the placement resolve computed and this node's origin.
func TestRegistry_TableServicePublishesPlacementAndOrigin(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, sys := newLockDataPlane(t, "system")
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", sys)
	reg := newRESTRegistry(dbReg, nil, tenantid.System, WithSharedSQLDefaultDatabase("shop"), withReclaimOrigin("7"))

	tables, err := reg.TableService("acme", "shop")
	if err != nil {
		t.Fatalf("TableService: %v", err)
	}
	if _, err := tables.CreateTable(ctx, &service.CreateTableRequest{
		Database: "shop", Name: "t", Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := tables.TruncateTable(ctx, "shop", "t", true); err != nil {
		t.Fatalf("TruncateTable: %v", err)
	}
	keys, err := sys.MVCC.ListAll(context.Background(), "__reclaim:index/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("index entries = %v, %v", keys, err)
	}
	raw, err := sys.KV.Get(context.Background(), keys[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(raw), `"placement":"shared"`) || !strings.Contains(string(raw), `"origin":"7"`) {
		t.Errorf("entry = %s", raw)
	}
}

// TestRegistry_TableServiceWithoutASystemStoreRefusesToPublish proves the
// fail-closed composition rule reaches the registry.
func TestRegistry_TableServiceWithoutASystemStoreRefusesToPublish(t *testing.T) {
	_, sys := newLockDataPlane(t, "shop")
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("shop", sys)
	reg := newRESTRegistry(dbReg, nil, tenantid.System)
	tables, err := reg.TableService(tenantid.System, "shop")
	if err != nil {
		t.Fatalf("TableService: %v", err)
	}
	if _, err := tables.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "t", Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	_, err = tables.TruncateTable(context.Background(), "shop", "t", true)
	var unconfigured *service.ReclaimIndexUnconfiguredError
	if !errors.As(err, &unconfigured) {
		t.Fatalf("err = %v, want *service.ReclaimIndexUnconfiguredError", err)
	}
}

// ---------------------------------------------------------------------------
// Server wiring
// ---------------------------------------------------------------------------

func TestServer_ReclaimOriginIsTheReplicaID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Raft.ReplicaID = 42
	if got := New(cfg, nil, nil).reclaimOrigin(); got != "42" {
		t.Errorf("reclaimOrigin = %q, want %q", got, "42")
	}
	if got := (&Server{}).reclaimOrigin(); got != "" {
		t.Errorf("reclaimOrigin without a config = %q, want empty", got)
	}
}

// TestServer_WiresTheReclaimStoreResolver proves New installs a resolver on
// the sweeper it is given: a sweep that finds a shared job resolves it
// through the server's own registry.
func TestServer_WiresTheReclaimStoreResolver(t *testing.T) {
	_, sys := newLockDataPlane(t, "system")
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", sys)
	sweeper := service.NewReclaimSweeper(sys.MVCC)

	srv := New(&config.Config{}, nil, nil, WithReclaimSweeper(sweeper), WithDatabaseServiceRegistry(dbReg))
	if srv == nil {
		t.Fatal("New returned nil")
	}
	op, err := service.ReclaimIndexOp(service.ReclaimIndexEntry{
		JobID: "probe", TenantID: tenantid.System, Database: "system",
		Placement: service.StorePlacementShared,
		Fences:    []service.ReclaimFence{{Key: "never-written", ModRevision: 5}},
	})
	if err != nil {
		t.Fatalf("ReclaimIndexOp: %v", err)
	}
	if err := sys.KV.Put(context.Background(), string(op.Key), op.Value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sweeper.SweepOnce(context.Background(), nil, nil)
	if keys, _ := sys.MVCC.ListAll(context.Background(), "__reclaim:index/"); len(keys) != 0 {
		t.Fatal("with a resolver installed, a settled entry is resolved and removed")
	}
}

// TestServer_ReclaimResolverWiringIsANoOpWithoutASweeper covers the guard.
func TestServer_ReclaimResolverWiringIsANoOpWithoutASweeper(t *testing.T) {
	if New(&config.Config{}, nil, nil, WithTenantHostRegistry(NewTenantHostRegistry(nil, nil, nil))) == nil {
		t.Fatal("New returned nil")
	}
}

// markedMVCC is an MVCC service recording that it was read through.
type markedMVCC struct {
	service.MVCCKVServicer
	read bool
}

func (m *markedMVCC) GetRevision(context.Context, string) (*service.KeyValue, error) {
	m.read = true
	return &service.KeyValue{}, nil
}

// markedTxn is a transaction service recording that it executed.
type markedTxn struct {
	service.TransactionServicer
	executed bool
}

func (m *markedTxn) Execute(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
	m.executed = true
	return &service.TransactionResponse{Succeeded: true}, nil
}

// newMockMVCCForReclaim returns an MVCC service that records its reads.
func newMockMVCCForReclaim() *markedMVCC {
	return &markedMVCC{}
}
