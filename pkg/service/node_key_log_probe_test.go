package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// listedDatabases is a TenantDatabaseLister over a fixed catalog.
type listedDatabases []database.DatabaseMeta

func (l listedDatabases) TenantRecords(context.Context, string) ([]database.DatabaseMeta, error) {
	return l, nil
}

// TestNodeKeyLogDatabases_JudgedByDataRoute: the probe lists every database
// whose data is served from a node-keyed log -- a shared-WAL raft group and
// a database served from the shared system shard -- and spares only a
// dedicated-WAL raft group and a database served from its own barrier store.
func TestNodeKeyLogDatabases_JudgedByDataRoute(t *testing.T) {
	on := &database.DatabaseBarrierConfig{Enabled: true}
	lister := listedDatabases{
		{TenantID: "bt", Name: "raft-shared", Backend: database.BackendRaft},
		{TenantID: "bt", Name: "raft-dedicated", Backend: database.BackendRaft, DedicatedWAL: true},
		{TenantID: "bt", Name: "pebble-shared", Backend: database.BackendPebble},
		{TenantID: "bt", Name: "memory-shared", Backend: database.BackendMemory},
		{TenantID: "bt", Name: "pebble-dir-no-barrier", Backend: database.BackendPebble, DataDir: "/d/1"},
		{TenantID: "bt", Name: "pebble-barrier-shared", Backend: database.BackendPebble, Barrier: on},
		{TenantID: "bt", Name: "pebble-barrier-own", Backend: database.BackendPebble, DataDir: "/d/2", Barrier: on},
		{TenantID: "other", Name: "foreign", Backend: database.BackendPebble},
	}
	got, err := NewNodeKeyLogProbe(lister).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	want := []string{"raft-shared", "pebble-shared", "memory-shared", "pebble-dir-no-barrier", "pebble-barrier-shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}
}

// unopenableCatalog mirrors database.Manager for a tenant whose shared-WAL
// database is recorded in the catalog but does not open on this node:
// ListForTenant logs the open failure and leaves the database out, while
// TenantRecords reports the catalog record as written.
type unopenableCatalog struct{ records []database.DatabaseMeta }

func (c unopenableCatalog) ListForTenant(context.Context) ([]database.DatabaseInfo, error) {
	return nil, nil
}

func (c unopenableCatalog) TenantRecords(_ context.Context, tenantID string) ([]database.DatabaseMeta, error) {
	var out []database.DatabaseMeta
	for _, r := range c.records {
		if r.TenantID == tenantID {
			out = append(out, r)
		}
	}
	return out, nil
}

// TestNodeKeyLogDatabases_CountsUnopenableRecords pins the strict-isolation
// probe to the catalog: a database that cannot open (a transient failure or
// an unreleased stale entry) still writes the node-keyed log once it opens
// again, so it must still refuse a tightening to strict.
func TestNodeKeyLogDatabases_CountsUnopenableRecords(t *testing.T) {
	c := unopenableCatalog{records: []database.DatabaseMeta{
		{TenantID: "bt", Name: "broken", Backend: database.BackendRaft},
		{TenantID: "bt", Name: "own-wal", Backend: database.BackendRaft, DedicatedWAL: true},
		{TenantID: "other", Name: "foreign", Backend: database.BackendRaft},
	}}
	got, err := NewNodeKeyLogProbe(c).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{"broken"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v: an unopenable shared-WAL database must still refuse strict", got, want)
	}
}

// sharedShardKeys is a system-shard KV double: ListAll returns the stored
// keys under prefix, or err.
type sharedShardKeys struct {
	keys []string
	err  error
}

func (s sharedShardKeys) ListAll(_ context.Context, prefix string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []string
	for _, k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// TestNodeKeyLogDatabases_ReportsImplicitDefaultNamespaceWithData: the
// implicit default database has no catalog record, but the tenant's keys in
// its shared namespace are on the system shard's node-keyed WAL, so a strict
// tightening must see it. Another tenant's keys there do not count.
func TestNodeKeyLogDatabases_ReportsImplicitDefaultNamespaceWithData(t *testing.T) {
	kv := sharedShardKeys{keys: []string{
		tenant.ComposeNamespace("bt", tenant.DefaultDatabase) + "k1",
		tenant.ComposeNamespace("other", "appdb") + "k2",
	}}
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase, "appdb"))
	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{tenant.DefaultDatabase}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}

	// A read error fails the probe: the tightening fails closed.
	failing := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(sharedShardKeys{err: errors.New("shard unavailable")}), tenant.DefaultDatabase))
	if _, err := failing.NodeKeyLogDatabases(context.Background(), "bt"); err == nil {
		t.Fatal("NodeKeyLogDatabases over a failing shard read = nil error, want the read error")
	}
}

// TestNodeKeyLogDatabases_ReadsCallTimeSharedNames proves the probe reads
// the names WithSharedNamespaceNames lists -- the SQL default database and
// the shared registry's databases -- when it runs, not when it is built,
// and dedupes them against the fixed names.
func TestNodeKeyLogDatabases_ReadsCallTimeSharedNames(t *testing.T) {
	kv := sharedShardKeys{keys: []string{
		tenant.ComposeNamespace("bt", "sqldefault") + "k1",
		tenant.ComposeNamespace("bt", "registered") + "k2",
		tenant.ComposeNamespace("other", "sqldefault") + "k3",
	}}
	var names []string
	probe := NewNodeKeyLogProbe(unopenableCatalog{},
		WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase),
		WithSharedNamespaceNames(func() []string { return names }))

	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases before any name is listed = (%v, %v), want none", got, err)
	}

	names = []string{"sqldefault", "registered", tenant.DefaultDatabase, "sqldefault", ""}
	got, err = probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{"sqldefault", "registered"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}

	// A tenant with no data under those names is not reported.
	got, err = probe.NodeKeyLogDatabases(context.Background(), "clean")
	if err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases(clean) = (%v, %v), want none", got, err)
	}

	// Without a shared reader the names are not read.
	bare := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaceNames(func() []string { return names }))
	if got, err := bare.NodeKeyLogDatabases(context.Background(), "bt"); err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases without a shared reader = (%v, %v), want none", got, err)
	}

	// A read error on a call-time name fails the probe closed.
	failing := NewNodeKeyLogProbe(unopenableCatalog{},
		WithSharedNamespaces(NewKVSharedNamespaceReader(sharedShardKeys{err: errors.New("shard unavailable")})),
		WithSharedNamespaceNames(func() []string { return []string{"sqldefault"} }))
	if _, err := failing.NodeKeyLogDatabases(context.Background(), "bt"); err == nil {
		t.Fatal("NodeKeyLogDatabases over a failing shard read = nil error, want the read error")
	}
}

// TestNodeKeyLogDatabases_IgnoresEmptyDefaultNamespace: a tenant that never
// wrote its implicit default database does not block a strict tightening.
func TestNodeKeyLogDatabases_IgnoresEmptyDefaultNamespace(t *testing.T) {
	kv := sharedShardKeys{keys: []string{tenant.ComposeNamespace("other", tenant.DefaultDatabase) + "k"}}
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase))
	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases = %v, want none", got)
	}
}

// TestCreateForTenant_BarrierPebbleBlocksTightening: a barrier tenant's
// pebble database is served from the shared system shard, so once created
// the probe a tightening consults lists it.
func TestCreateForTenant_BarrierPebbleBlocksTightening(t *testing.T) {
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "bt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendPebble
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	dbs, err := NewNodeKeyLogProbe(svc.(*DatabaseService).manager).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil || len(dbs) != 1 || dbs[0] != "app" {
		t.Fatalf("NodeKeyLogDatabases = (%v, %v), want [app]", dbs, err)
	}
}

// TestCreateForTenant_BarrierPebbleRolledBackUnderATighteningMarker: a
// barrier tenant's pebble create concurrent with a tightening to strict is
// refused and rolled back, as a shared-WAL raft create is.
func TestCreateForTenant_BarrierPebbleRolledBackUnderATighteningMarker(t *testing.T) {
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info, marker: true}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "bt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendPebble
	_, err := svc.CreateForTenant(ctx, cfg)
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "bt" {
		t.Fatalf("CreateForTenant(barrier, pebble) under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}
