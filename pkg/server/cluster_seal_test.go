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
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// testClusterSeal is a ClusterSeal over a faithful Sealer double, with a
// settable Refusing and a real stream registry.
type testClusterSeal struct {
	*clustersealtest.Sealer
	mu       sync.Mutex
	refusing bool
	streams  *clusterseal.Streams
}

var _ ClusterSeal = (*testClusterSeal)(nil)

func newTestClusterSeal() *testClusterSeal {
	return &testClusterSeal{Sealer: clustersealtest.New(true), streams: clusterseal.NewStreams()}
}

func (c *testClusterSeal) Refusing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refusing
}

func (c *testClusterSeal) setRefusing(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusing = v
}

func (c *testClusterSeal) Streams() *clusterseal.Streams { return c.streams }

type boolProbe bool

func (p boolProbe) IsSealed() bool { return bool(p) }

func TestNodeSealProbe(t *testing.T) {
	cs := newTestClusterSeal()
	for _, tc := range []struct {
		barrierSealed, refusing, want bool
	}{
		{false, false, false},
		{true, false, true},
		{false, true, true},
		{true, true, true},
	} {
		cs.setRefusing(tc.refusing)
		if got := (nodeSealProbe{barrier: boolProbe(tc.barrierSealed), seal: cs}).IsSealed(); got != tc.want {
			t.Errorf("barrier sealed=%v refusing=%v: IsSealed = %v, want %v", tc.barrierSealed, tc.refusing, got, tc.want)
		}
	}
}

func TestNodeSealProbe_SealState(t *testing.T) {
	for _, tc := range []struct {
		name                string
		barrierSealed       bool
		refusing            bool
		local               clusterseal.Local
		sealed, unconfirmed bool
	}{
		{name: "serving", local: clusterseal.LocalServing},
		{name: "barrier sealed", barrierSealed: true, sealed: true},
		{name: "barrier sealed and cut off", barrierSealed: true, refusing: true, local: clusterseal.LocalCutOff, sealed: true},
		{name: "held", refusing: true, local: clusterseal.LocalHeld, sealed: true},
		{name: "node record sealed", refusing: true, local: clusterseal.LocalSealed, sealed: true},
		{name: "cut off", refusing: true, local: clusterseal.LocalCutOff, sealed: true, unconfirmed: true},
		{name: "unconfirmed", refusing: true, local: clusterseal.LocalUnconfirmed, sealed: true, unconfirmed: true},
		{name: "not yet loaded", refusing: true, sealed: true, unconfirmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestClusterSeal()
			cs.setRefusing(tc.refusing)
			cs.SetLocal(tc.local)
			sealed, unconfirmed := (nodeSealProbe{barrier: boolProbe(tc.barrierSealed), seal: cs}).SealState()
			if sealed != tc.sealed || unconfirmed != tc.unconfirmed {
				t.Fatalf("SealState = (%v, %v), want (%v, %v)", sealed, unconfirmed, tc.sealed, tc.unconfirmed)
			}
		})
	}
}

// TestNodeRefusal answers why the node refuses, with the typed cause each
// listener without sealed-window middleware of its own returns: held or a
// sealed node barrier is the node barrier's SealedError; cut off or
// unconfirmed is SealStateUnconfirmedError; a serving node, or one with no
// probe and no cluster seal, refuses nothing.
func TestNodeRefusal(t *testing.T) {
	var sealed *clusterseal.SealedError
	var unconfirmed *clusterseal.SealStateUnconfirmedError

	cs := newTestClusterSeal()
	held := &Server{opts: serverOptions{clusterSeal: cs, sealedWindowProbe: boolProbe(false)}}
	if err := held.nodeRefusal(); err != nil {
		t.Fatalf("serving node: %v", err)
	}
	cs.setRefusing(true)
	cs.SetLocal(clusterseal.LocalHeld)
	if err := held.nodeRefusal(); !errors.As(err, &sealed) || sealed.Barrier != clusterseal.NodeBarrier() {
		t.Fatalf("held: %v, want the node barrier's SealedError", err)
	}
	cs.SetLocal(clusterseal.LocalSealed)
	if err := held.nodeRefusal(); !errors.As(err, &sealed) {
		t.Fatalf("node record sealed: %v, want SealedError", err)
	}
	cs.SetLocal(clusterseal.LocalCutOff)
	if err := held.nodeRefusal(); !errors.As(err, &unconfirmed) {
		t.Fatalf("cut off: %v, want SealStateUnconfirmedError", err)
	}

	// The server installs the composed probe, which reports a cut-off node as
	// closed; nodeRefusal must still name the state it cannot confirm.
	wrapped := &Server{opts: serverOptions{clusterSeal: cs, sealedWindowProbe: NodeSealProbe(boolProbe(false), cs)}}
	if err := wrapped.nodeRefusal(); !errors.As(err, &unconfirmed) {
		t.Fatalf("composed probe, cut off: %v, want SealStateUnconfirmedError", err)
	}
	cs.SetLocal(clusterseal.LocalHeld)
	if err := wrapped.nodeRefusal(); !errors.As(err, &sealed) {
		t.Fatalf("composed probe, held: %v, want SealedError", err)
	}

	barrierSealed := &Server{opts: serverOptions{sealedWindowProbe: boolProbe(true)}}
	if err := barrierSealed.nodeRefusal(); !errors.As(err, &sealed) {
		t.Fatalf("sealed node barrier: %v, want SealedError", err)
	}
	if err := (&Server{}).nodeRefusal(); err != nil {
		t.Fatalf("no probe, no cluster seal: %v", err)
	}
}

// TestApplyClusterSeal: the regular listener's probe is wrapped; the boot-time
// sealed-window listener's, which runs before the host, is not.
func TestApplyClusterSeal(t *testing.T) {
	cs := newTestClusterSeal()
	regular := serverOptions{sealedWindowProbe: boolProbe(false), clusterSeal: cs}
	regular.applyClusterSeal()
	if _, ok := regular.sealedWindowProbe.(nodeSealProbe); !ok {
		t.Fatalf("regular listener probe = %T, want nodeSealProbe", regular.sealedWindowProbe)
	}
	listener := serverOptions{sealedWindowProbe: boolProbe(true), sealedWindowListener: true, clusterSeal: cs}
	listener.applyClusterSeal()
	if _, ok := listener.sealedWindowProbe.(nodeSealProbe); ok {
		t.Fatal("the boot-time listener's probe was wrapped")
	}
	none := serverOptions{clusterSeal: cs}
	none.applyClusterSeal()
	if none.sealedWindowProbe != nil {
		t.Fatal("a probe was invented where none was configured")
	}
}

// TestClusterSealKV: a missing key is reported as not found rather than an
// error, and the record scan is not filtered by the internal-key guard --
// mockKVService.Scan applies the real FilterInternalMap.
func TestClusterSealKV(t *testing.T) {
	ctx := context.Background()
	kv := NewClusterSealKV(newMockKVService())
	if v, found, err := kv.Get(ctx, clusterseal.RecordsPrefix+"node"); err != nil || found || v != nil {
		t.Fatalf("Get missing = %q, %v, %v", v, found, err)
	}
	if ok, err := kv.PutIfAbsent(ctx, clusterseal.RecordsPrefix+"node", []byte("a")); err != nil || !ok {
		t.Fatalf("PutIfAbsent = %v, %v", ok, err)
	}
	if ok, err := kv.CompareAndSwap(ctx, clusterseal.RecordsPrefix+"node", []byte("a"), []byte("b")); err != nil || !ok {
		t.Fatalf("CompareAndSwap = %v, %v", ok, err)
	}
	if err := kv.Put(ctx, clusterseal.AcksPrefix+"n1", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if v, found, err := kv.Get(ctx, clusterseal.RecordsPrefix+"node"); err != nil || !found || string(v) != "b" {
		t.Fatalf("Get = %q, %v, %v", v, found, err)
	}
	got, err := kv.Scan(ctx, clusterseal.RecordsPrefix)
	if err != nil || string(got[clusterseal.RecordsPrefix+"node"]) != "b" {
		t.Fatalf("Scan = %v, %v: the internal-key guard filtered the seal records", got, err)
	}
	failing := NewClusterSealKV(&errKV{err: errors.New("no quorum")})
	if _, _, err := failing.Get(ctx, "k"); err == nil {
		t.Fatal("a read failure was reported as not found")
	}
}

// errKV fails every Get with err.
type errKV struct {
	service.KVServicer
	err error
}

func (e *errKV) Get(context.Context, string) ([]byte, error) { return nil, e.err }

// fakeShards records which dedicated shards the enforcer closed.
type fakeShards struct {
	started []StartedShard
	closed  []StartedShard
	err     error
}

func (f *fakeShards) ListStarted() []StartedShard { return f.started }

func (f *fakeShards) Close(tenantID, db string) error {
	f.closed = append(f.closed, StartedShard{TenantID: tenantID, Database: db})
	return f.err
}

// fakeHosts records which volume hosts the enforcer closed.
type fakeHosts struct {
	closed []string
	err    error
}

func (f *fakeHosts) Close(tenantID string) error {
	f.closed = append(f.closed, tenantID)
	return f.err
}

// fakeDatabases records which databases the enforcer sealed.
type fakeDatabases struct {
	sealed []string
	err    error
	marked int
}

var _ databaseSealer = (*fakeDatabases)(nil)

// SealDatabaseLocal records tenantID/name, the database the record names.
func (f *fakeDatabases) SealDatabaseLocal(tenantID, name string) error {
	f.sealed = append(f.sealed, tenantID+"/"+name)
	return f.err
}

// SealBarrierDatabases counts the node seal's database half.
func (f *fakeDatabases) SealBarrierDatabases() error {
	f.marked++
	return nil
}

// newEnforcerRegistry returns a registry whose node barrier is initialized and
// unsealed and which holds an unsealed tenant barrier "acme".
func newEnforcerRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("enforcer-pass"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sys, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "sys/root"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	reg, err := seal.NewBarrierRegistry(logger, sys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.RegisterTenantDefault("acme"); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(context.Background(), "acme", seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func TestSealEnforcer_SealTenantStopsItsShardsThenWipesItsKey(t *testing.T) {
	reg := newEnforcerRegistry(t)
	shards := &fakeShards{started: []StartedShard{
		{TenantID: "acme", Database: "orders"}, {TenantID: "globex", Database: "orders"}, {TenantID: "acme", Database: "billing"},
	}}
	hosts := &fakeHosts{}
	e := &SealEnforcer{Barriers: reg, Shards: shards, Hosts: hosts}
	if !e.TenantUnsealed("acme") {
		t.Fatal("fixture tenant not unsealed")
	}
	if err := e.SealTenant(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	want := []StartedShard{{TenantID: "acme", Database: "orders"}, {TenantID: "acme", Database: "billing"}}
	if !slices.Equal(shards.closed, want) {
		t.Fatalf("closed shards = %v, want %v", shards.closed, want)
	}
	if !slices.Equal(hosts.closed, []string{"acme"}) {
		t.Fatalf("closed hosts = %v", hosts.closed)
	}
	if e.TenantUnsealed("acme") {
		t.Fatal("tenant key still held")
	}
	if !e.NodeUnsealed() {
		t.Fatal("a tenant seal sealed the node")
	}
	// A tenant this node does not hold is sealed already.
	if err := e.SealTenant(context.Background(), "nowhere"); err != nil {
		t.Fatalf("SealTenant(unknown) = %v", err)
	}
}

func TestSealEnforcer_SealTenantReportsEveryFailureAndStillWipes(t *testing.T) {
	reg := newEnforcerRegistry(t)
	shardErr, hostErr := errors.New("shard close"), errors.New("host close")
	e := &SealEnforcer{Barriers: reg,
		Shards: &fakeShards{started: []StartedShard{{TenantID: "acme", Database: "orders"}}, err: shardErr},
		Hosts:  &fakeHosts{err: hostErr}}
	err := e.SealTenant(context.Background(), "acme")
	if !errors.Is(err, shardErr) || !errors.Is(err, hostErr) {
		t.Fatalf("err = %v, want both failures", err)
	}
	if e.TenantUnsealed("acme") {
		t.Fatal("the key survived a failed shard close")
	}
}

func TestSealEnforcer_SealNodeWipesEveryKey(t *testing.T) {
	reg := newEnforcerRegistry(t)
	e := &SealEnforcer{Barriers: reg}
	if err := e.SealNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.NodeUnsealed() || e.TenantUnsealed("acme") {
		t.Fatal("a key survived the node seal")
	}
}

func TestSealEnforcer_SealDatabase(t *testing.T) {
	reg := newEnforcerRegistry(t)
	if err := (&SealEnforcer{Barriers: reg}).SealDatabase(context.Background(), "system", "secure"); err != nil {
		t.Fatalf("no database service: %v", err)
	}
	dbs := &fakeDatabases{}
	e := &SealEnforcer{Barriers: reg, Databases: dbs}
	if err := e.SealDatabase(context.Background(), "system", "secure"); err != nil || !slices.Equal(dbs.sealed, []string{"system/secure"}) {
		t.Fatalf("SealDatabase = %v, sealed %v", err, dbs.sealed)
	}
	for _, absent := range []error{&database.NotFoundError{Name: "secure"}, &database.BarrierSealError{Name: "secure", Err: seal.ErrTenantNotFound}} {
		dbs.err = absent
		if err := e.SealDatabase(context.Background(), "system", "secure"); err != nil {
			t.Fatalf("a database this node does not hold: %v", err)
		}
	}
	boom := errors.New("seal failed")
	dbs.err = boom
	if err := e.SealDatabase(context.Background(), "system", "secure"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if e.DatabaseUnsealed("system", "nodb") {
		t.Fatal("an absent database reported unsealed")
	}
	if !e.DatabaseUnsealed("system", "acme") {
		t.Fatal("DatabaseUnsealed did not read the registry entry")
	}
}

func TestSealEnforcer_NodeUnsealedWithoutSystemBarrier(t *testing.T) {
	reg := newEnforcerRegistry(t)
	sys := reg.SystemBarrier()
	reg.SetSystemBarrier(nil)
	unsealed := (&SealEnforcer{Barriers: reg}).NodeUnsealed()
	reg.SetSystemBarrier(sys)
	if unsealed {
		t.Fatal("a node with no system barrier reported unsealed")
	}
}

// TestSealedWatchService_SealEndsTheWatch: a watch on a tenant's database ends
// with the seal's refusal when the tenant is sealed, and its subscription is
// forgotten either way.
func TestSealedWatchService_SealEndsTheWatch(t *testing.T) {
	streams := clusterseal.NewStreams()
	inner := &queuedWatchService{}
	svc := newSealedWatchService(inner, streams, "acme", "orders", nil)
	w, err := svc.Watch(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if streams.Len() != 1 {
		t.Fatalf("subscriptions = %d", streams.Len())
	}
	cause := &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("acme")}
	streams.EndTenant("acme", cause)
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Fatal("an event after the seal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end on the seal")
	}
	if !errors.Is(w.Err(), cause) {
		t.Fatalf("Err = %v, want the seal", w.Err())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if streams.Len() != 0 {
		t.Fatalf("subscriptions = %d after the seal", streams.Len())
	}
}

func TestSealedWatchService_CloseForgetsTheSubscription(t *testing.T) {
	streams := clusterseal.NewStreams()
	inner := &queuedWatchService{}
	svc := newSealedWatchService(inner, streams, "", "system", nil)
	w, err := svc.WatchPrefix(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if streams.Len() != 0 {
		t.Fatalf("subscriptions = %d after Close", streams.Len())
	}
	// The system tenant is canonicalized, so a node seal or a seal of the
	// system tenant's database reaches its watches.
	w2, _ := svc.Watch(context.Background(), "k")
	streams.EndDatabase("system", "system", &clusterseal.SealedError{Barrier: clusterseal.DatabaseBarrier("system", "system")})
	if _, ok := <-w2.Events(); ok {
		t.Fatal("watch survived")
	}
}

func TestSealedWatchService_InnerFailureOpensNothing(t *testing.T) {
	streams := clusterseal.NewStreams()
	boom := errors.New("open failed")
	svc := newSealedWatchService(&queuedWatchService{err: boom}, streams, "acme", "orders", nil)
	if _, err := svc.Watch(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("Watch err = %v", err)
	}
	if _, err := svc.WatchPrefix(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("WatchPrefix err = %v", err)
	}
	if _, err := svc.WatchEntity(context.Background(), &service.WatchEntityRequest{}); !errors.Is(err, boom) {
		t.Fatalf("WatchEntity err = %v", err)
	}
	if _, err := svc.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{}); !errors.Is(err, boom) {
		t.Fatalf("WatchEntityType err = %v", err)
	}
	if streams.Len() != 0 {
		t.Fatalf("subscriptions = %d after failed opens", streams.Len())
	}
}

func TestSealedWatchService_EntityWatchesEnd(t *testing.T) {
	for name, open := range map[string]func(*sealedWatchService) (<-chan service.WatchEntityEvent, error){
		"entity": func(s *sealedWatchService) (<-chan service.WatchEntityEvent, error) {
			return s.WatchEntity(context.Background(), &service.WatchEntityRequest{})
		},
		"entity type": func(s *sealedWatchService) (<-chan service.WatchEntityEvent, error) {
			return s.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			streams := clusterseal.NewStreams()
			inner := &queuedWatchService{}
			ch, err := open(newSealedWatchService(inner, streams, "acme", "orders", nil))
			if err != nil {
				t.Fatal(err)
			}
			streams.EndAll(&clusterseal.SealStateUnconfirmedError{})
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatal("an event after the cut-off")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the entity watch did not end")
			}
			if inner.entityCtx.Err() == nil {
				t.Fatal("the inner entity watch was not cancelled")
			}
			if streams.Len() != 0 {
				t.Fatalf("subscriptions = %d", streams.Len())
			}
		})
	}
}

// TestRegistryRefusesSealedRecords: a tenant or database whose cluster seal
// record is sealed is refused at resolution even while this node still holds
// its key -- the gate closes before the enforcement wipes it, and stays closed
// on a node whose clear has not been written.
func TestRegistryRefusesSealedRecords(t *testing.T) {
	reg := newEnforcerRegistry(t)
	cs := newTestClusterSeal()
	r := &restServiceRegistry{barrierReg: reg, systemTenant: "system", clusterSeal: cs}
	if err := r.checkTenantBarrier("acme"); err != nil {
		t.Fatalf("unsealed tenant refused: %v", err)
	}
	cs.Put(clusterseal.Record{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 1, Sealed: true})
	var sealed *rest.BarrierSealedError
	if err := r.checkTenantBarrier("acme"); !errors.As(err, &sealed) {
		t.Fatalf("err = %v, want *rest.BarrierSealedError", err)
	}

	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.Create(database.DatabaseConfig{Name: "app", Backend: database.BackendMemory}); err != nil {
		t.Fatal(err)
	}
	r.dbManager = mgr
	if err := r.refuseSealedDatabase("system", "app"); err != nil {
		t.Fatalf("unsealed database refused: %v", err)
	}
	cs.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("system", "app"), Epoch: 1, Sealed: true})
	if err := r.refuseSealedDatabase("", "app"); !errors.As(err, &sealed) {
		t.Fatalf("err = %v, want *rest.BarrierSealedError for the system tenant's database", err)
	}
}

func TestClusterSealStreams(t *testing.T) {
	if (&Server{}).clusterSealStreams() != nil {
		t.Fatal("streams without cluster sealing")
	}
	cs := newTestClusterSeal()
	s := &Server{opts: serverOptions{clusterSeal: cs}}
	if s.clusterSealStreams() != cs.streams {
		t.Fatal("wrong stream registry")
	}
}

// membershipReader reports a fixed system-shard membership, or err.
type membershipReader struct {
	ms  *quicraft.Membership
	err error
}

func (r membershipReader) GetShardMembership(uint64) (*quicraft.Membership, error) {
	return r.ms, r.err
}

// TestSystemShardMembers lists every system-shard member by replica ID --
// voters, observers and witnesses all run a node that must acknowledge a
// node-barrier seal -- sorted, and reports a membership that cannot be read.
func TestSystemShardMembers(t *testing.T) {
	members := SystemShardMembers(membershipReader{ms: &quicraft.Membership{
		Nodes:     map[uint64]string{3: "c", 1: "a"},
		Observers: map[uint64]string{7: "g"},
		Witnesses: map[uint64]string{12: "w"},
	}}, 1)
	got, err := members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1", "12", "3", "7"}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	boom := errors.New("no membership")
	if _, err := SystemShardMembers(membershipReader{err: boom}, 1)(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// TestNewClusterSealMonitor builds a monitor from a composer's parts: it
// identifies the node by replica ID and names it by raft address, leaves
// absent shard factories out rather than wrapping typed nils, and is never
// active without a feature gate.
func TestNewClusterSealMonitor(t *testing.T) {
	reg := newEnforcerRegistry(t)
	streams := clusterseal.NewStreams()
	m, err := NewClusterSealMonitor(ClusterSealParts{
		KV:          newMockKVService(),
		Barriers:    reg,
		Membership:  membershipReader{ms: &quicraft.Membership{Nodes: map[uint64]string{2: "192.0.2.2:5000"}}},
		SystemShard: 1,
		ReplicaID:   2,
		RaftAddress: "192.0.2.2:5000",
		Timing:      config.ClusterSealTiming{Lease: 15 * time.Second, PollInterval: time.Second, AckTimeout: 5 * time.Second},
		Streams:     streams,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() {
		t.Fatal("a monitor with no feature gate is active")
	}
	if m.Streams() != streams {
		t.Fatal("the monitor did not adopt the shared stream registry")
	}
	if _, err := NewClusterSealMonitor(ClusterSealParts{}); err == nil {
		t.Fatal("a monitor with no parts was built")
	}
}

// TestSealEnforcer_SealNodeMarksBarrierDatabasesSealed: a cluster node seal
// marks the node's barrier-enabled databases sealed, as a node-local seal
// does.
func TestSealEnforcer_SealNodeMarksBarrierDatabasesSealed(t *testing.T) {
	dbs := &fakeDatabases{}
	if err := (&SealEnforcer{Barriers: newEnforcerRegistry(t), Databases: dbs}).SealNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dbs.marked != 1 {
		t.Fatalf("databases marked %d times, want 1", dbs.marked)
	}
}
