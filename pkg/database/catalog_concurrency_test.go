package database

import (
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// newTwoNodes returns two managers over one shared catalog, the way two
// nodes share the replicated one, each with the given options.
func newTwoNodes(t *testing.T, opts ...ManagerOption) (cat *KVCatalog, a, b *Manager) {
	t.Helper()
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	a = NewManager(nil, t.TempDir(), opts...)
	a.SetCatalog(cat)
	t.Cleanup(func() { _ = a.Close() })
	b = NewManager(nil, t.TempDir(), opts...)
	b.SetCatalog(cat)
	t.Cleanup(func() { _ = b.Close() })
	return cat, a, b
}

// TestManager_ConfigUpdateFromAnotherNodeKeepsTheDropFence checks a
// configuration update through a node that opened the database before
// another node fenced it for a drop cannot clear the durable fence: it is
// refused, and the record stays dropping while the keyspace is reclaimed.
func TestManager_ConfigUpdateFromAnotherNodeKeepsTheDropFence(t *testing.T) {
	spy := &spyReclaimer{}
	cat, a, b := newTwoNodes(t, WithKeyspaceReclaimer(spy))
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}

	// B's update runs while A reclaims: A's fence is durable by then.
	var updateErr error
	var fenced bool
	spy.before = func() {
		pess := &SQLConfig{Concurrency: ConcurrencyPessimistic}
		updateErr = b.UpdateSQLForTenant(ctx, "orders", pess)
		meta, err := cat.Get(ctx, "tenant-a", "orders")
		fenced = err == nil && meta.Dropping
	}
	if err := a.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on A: %v", err)
	}
	var dropping *DroppingError
	if !errors.As(updateErr, &dropping) {
		t.Fatalf("UpdateSQLForTenant on B mid-drop = %v, want *DroppingError", updateErr)
	}
	if !fenced {
		t.Fatal("the record lost its drop fence to B's update while A reclaimed")
	}
}

// TestManager_ConfigUpdatesFromTwoNodesKeepEachOthersFields checks each
// node's configuration update writes its own field into the record as the
// catalog holds it, not its whole stale copy over the other node's change.
func TestManager_ConfigUpdatesFromTwoNodesKeepEachOthersFields(t *testing.T) {
	cat, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}

	pess := &SQLConfig{Concurrency: ConcurrencyPessimistic}
	if err := a.UpdateSQLForTenant(ctx, "orders", pess); err != nil {
		t.Fatalf("UpdateSQLForTenant on A: %v", err)
	}
	if err := b.UpdateProtocolForTenant(ctx, "orders", ProtocolConfig{MySQL: true}); err != nil {
		t.Fatalf("UpdateProtocolForTenant on B: %v", err)
	}
	got, err := cat.Get(ctx, "tenant-a", "orders")
	if err != nil {
		t.Fatalf("catalog Get: %v", err)
	}
	if got.SQL == nil || got.SQL.Concurrency != ConcurrencyPessimistic {
		t.Fatalf("record SQL = %+v, want A's pessimistic setting kept", got.SQL)
	}
	if !got.Protocol.MySQL {
		t.Fatalf("record protocol = %+v, want B's MySQL setting", got.Protocol)
	}
}

// TestManager_ADropTakenOverCannotRemoveTheRecreatedDatabase checks a drop
// that another node's drop overtook -- and that finished, and the name was
// created again -- cannot remove the new database's record when it reaches
// its own delete: the delete names the incarnation it dropped.
func TestManager_ADropTakenOverCannotRemoveTheRecreatedDatabase(t *testing.T) {
	spy := &spyReclaimer{}
	cat, a, b := newTwoNodes(t, WithKeyspaceReclaimer(spy))
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}

	// While A reclaims, B drops the database and creates the name again.
	var successor *Database
	spy.before = func() {
		spy.before = nil
		if err := b.DropForTenant(ctx, "orders"); err != nil {
			t.Errorf("DropForTenant on B: %v", err)
			return
		}
		var err error
		if successor, err = b.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
			t.Errorf("CreateForTenant on B: %v", err)
		}
	}
	if err := a.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("A's overtaken drop reported success")
	}
	if successor == nil {
		t.FailNow()
	}
	got, err := cat.Get(ctx, "tenant-a", "orders")
	if err != nil {
		t.Fatalf("the re-created database's record is gone: %v", err)
	}
	if got.Incarnation != successor.Incarnation() || got.Dropping {
		t.Fatalf("record = %+v, want the successor's live record (incarnation %q)", got, successor.Incarnation())
	}
}

// TestManager_ADropTakenOverLeavesTheRecordToTheNewClaim checks the claim a
// drop writes with its fence is what its delete is conditioned on: once a
// retry on another node has taken the claim over, the first drop's delete
// is refused and the record stays, fenced, for the retry to finish.
func TestManager_ADropTakenOverLeavesTheRecordToTheNewClaim(t *testing.T) {
	spy := &spyReclaimer{}
	cat, a, b := newTwoNodes(t, WithKeyspaceReclaimer(spy))
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	dbB, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	spy.before = func() {
		if err := b.claimDrop(ctx, dbB, "retry-on-b"); err != nil {
			t.Errorf("claim on B: %v", err)
		}
	}
	var taken *DropInProgressError
	if err := a.DropForTenant(ctx, "orders"); !errors.As(err, &taken) {
		t.Fatalf("A's drop after B took the claim = %v, want *DropInProgressError", err)
	}
	got, err := cat.Get(ctx, "tenant-a", "orders")
	if err != nil {
		t.Fatalf("A's drop removed the record B's claim holds: %v", err)
	}
	if !got.Dropping || got.DropClaim != "retry-on-b" {
		t.Fatalf("record = %+v, want fenced under B's claim", got)
	}
}

// TestManager_ARolledBackDropLeavesATakenOverFenceInPlace checks a drop
// whose reclamation fails after another node's retry took its claim over
// does not roll that retry's fences back: neither the store fence nor the
// catalog record's.
func TestManager_ARolledBackDropLeavesATakenOverFenceInPlace(t *testing.T) {
	spy := &spyReclaimer{err: errors.New("reclaim failed")}
	cat, a, b := newTwoNodes(t, WithKeyspaceReclaimer(spy))
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	dbB, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	spy.before = func() {
		if err := b.claimDrop(ctx, dbB, "retry-on-b"); err != nil {
			t.Errorf("claim on B: %v", err)
		}
	}
	_, created, _ := spy.fenceCalls()
	var reclaimErr *KeyspaceReclaimError
	if err := a.DropForTenant(ctx, "orders"); !errors.As(err, &reclaimErr) {
		t.Fatalf("A's drop = %v, want *KeyspaceReclaimError", err)
	}
	if reclaimErr.RolledBack {
		t.Fatal("A reported its drop rolled back over B's claim")
	}
	if _, unfenced, _ := spy.fenceCalls(); len(unfenced) != len(created) {
		t.Fatalf("A cleared the store fence B's drop holds: %v", unfenced[len(created):])
	}
	got, err := cat.Get(ctx, "tenant-a", "orders")
	if err != nil {
		t.Fatalf("catalog Get: %v", err)
	}
	if !got.Dropping || got.DropClaim != "retry-on-b" {
		t.Fatalf("record = %+v, want fenced under B's claim", got)
	}
}

// TestManager_GetOwnedReleasesADatabaseDroppedThroughAnotherNode checks a
// node that held a database open answers *NotFoundError once another node
// has dropped it, rather than authorizing the caller against its cache.
func TestManager_GetOwnedReleasesADatabaseDroppedThroughAnotherNode(t *testing.T) {
	_, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if err := a.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on A: %v", err)
	}
	var nf *NotFoundError
	if db, err := b.GetOwned("orders", "tenant-a"); !errors.As(err, &nf) {
		t.Fatalf("GetOwned on B after A's drop = %v, %v; want *NotFoundError", db, err)
	}
	if _, ok := b.Local("orders", "tenant-a"); ok {
		t.Fatal("B still holds the dropped database open")
	}
}

// TestManager_GetOwnedResolvesTheSuccessorOfARecreatedDatabase checks a
// node holding a database that another node dropped and created again
// resolves the name to the new incarnation, not its cached predecessor.
func TestManager_GetOwnedResolvesTheSuccessorOfARecreatedDatabase(t *testing.T) {
	_, a, b := newTwoNodes(t, WithKeyspaceReclaimer(&spyReclaimer{}))
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	stale, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if err := a.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on A: %v", err)
	}
	successor, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant again on A: %v", err)
	}
	got, err := b.GetOwned("orders", "tenant-a")
	if err != nil {
		t.Fatalf("GetOwned on B: %v", err)
	}
	if got == stale || got.Incarnation() != successor.Incarnation() {
		t.Fatalf("GetOwned on B = incarnation %q, want the successor's %q", got.Incarnation(), successor.Incarnation())
	}
}

// TestManager_GetOwnedAppliesAFenceSetAndClearedThroughAnotherNode checks
// a drop fence written through another node refuses the caller on this one
// with *DroppingError, and its rollback admits the caller again, without a
// listing in between.
func TestManager_GetOwnedAppliesAFenceSetAndClearedThroughAnotherNode(t *testing.T) {
	_, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	dbA, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if err := a.claimDrop(ctx, dbA, "drop-on-a"); err != nil {
		t.Fatalf("fence on A: %v", err)
	}
	var dropping *DroppingError
	if _, err := b.GetOwned("orders", "tenant-a"); !errors.As(err, &dropping) {
		t.Fatalf("GetOwned on B after A's fence = %v, want *DroppingError", err)
	}
	if err := a.releaseDropClaim(ctx, dbA, "drop-on-a"); err != nil {
		t.Fatalf("rollback on A: %v", err)
	}
	if _, err := b.GetOwned("orders", "tenant-a"); err != nil {
		t.Fatalf("GetOwned on B after A's rollback: %v", err)
	}
}

// TestManager_GetOwnedSeesSettingsUpdatedThroughAnotherNode checks a node
// holding a database open takes the SQL and protocol settings an update
// through another node wrote to the catalog record, on its next lookup, and
// takes them again when they are cleared.
func TestManager_GetOwnedSeesSettingsUpdatedThroughAnotherNode(t *testing.T) {
	_, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}

	if err := a.UpdateSQLForTenant(ctx, "orders", &SQLConfig{Concurrency: ConcurrencyPessimistic}); err != nil {
		t.Fatalf("UpdateSQLForTenant on A: %v", err)
	}
	protocol := ProtocolConfig{MySQL: true, PostgreSQL: true}
	if err := a.UpdateProtocolForTenant(ctx, "orders", protocol); err != nil {
		t.Fatalf("UpdateProtocolForTenant on A: %v", err)
	}
	db, err := b.GetOwned("orders", "tenant-a")
	if err != nil {
		t.Fatalf("GetOwned on B: %v", err)
	}
	if got := db.SQLConfig().EffectiveConcurrency(); got != ConcurrencyPessimistic {
		t.Fatalf("B's concurrency after A's update = %q, want %q", got, ConcurrencyPessimistic)
	}
	if got := db.ProtocolConfig(); !sameProtocol(got, protocol) {
		t.Fatalf("B's protocols after A's update = %+v, want %+v", got, protocol)
	}
	if info := db.Info(); info.SQL.EffectiveConcurrency() != ConcurrencyPessimistic || !info.Protocol.PostgreSQL {
		t.Fatalf("B's info after A's update = %+v", info)
	}

	if err := a.UpdateSQLForTenant(ctx, "orders", nil); err != nil {
		t.Fatalf("clearing the SQL settings on A: %v", err)
	}
	db, err = b.GetOwned("orders", "tenant-a")
	if err != nil {
		t.Fatalf("GetOwned on B after the clear: %v", err)
	}
	if db.SQLConfig() != nil {
		t.Fatalf("B's SQL settings after A cleared them = %+v, want none", db.SQLConfig())
	}
}

// TestDatabase_AdoptSettingsKeepsAnUnchangedEntry checks a lookup that finds
// the record's settings already in place leaves the entry's configuration as
// it is, and one that finds them changed replaces exactly those settings.
func TestDatabase_AdoptSettingsKeepsAnUnchangedEntry(t *testing.T) {
	_, a, _ := newTwoNodes(t)
	cfg := validMemoryConfig("orders")
	cfg.Protocol = ProtocolConfig{MySQL: true, Etcd: &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379", AdvertiseClientURLs: []string{"https://db.example.com:2379"}}}
	cfg.SQL = &SQLConfig{Concurrency: ConcurrencyPessimistic}
	db, err := a.CreateForTenant(tenantContext("tenant-a"), cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	before := db.config.Load()

	same := *cfg.Protocol.Etcd
	same.AdvertiseClientURLs = slices.Clone(cfg.Protocol.Etcd.AdvertiseClientURLs)
	db.adoptSettings(ProtocolConfig{MySQL: true, Etcd: &same}, &SQLConfig{Concurrency: ConcurrencyPessimistic})
	if db.config.Load() != before {
		t.Fatal("equal settings replaced the entry's configuration")
	}

	moved := same
	moved.AdvertiseClientURLs = []string{"https://db2.example.com:2379"}
	db.adoptSettings(ProtocolConfig{MySQL: true, Etcd: &moved}, nil)
	if got := db.ProtocolConfig().Etcd.AdvertiseClientURLs; !slices.Equal(got, moved.AdvertiseClientURLs) {
		t.Fatalf("advertised URLs = %v, want %v", got, moved.AdvertiseClientURLs)
	}
	if db.SQLConfig() != nil {
		t.Fatalf("SQL settings = %+v, want none", db.SQLConfig())
	}
	if got := db.config.Load(); got.Name != before.Name || got.Backend != before.Backend {
		t.Fatalf("adopting settings changed more than the settings: %+v", got)
	}
}

// TestManager_GetOwnedFailsClosedWhenTheRecordCannotBeRead checks an open
// database is not authorized against while its catalog record cannot be
// read: the read error is returned and the entry is kept for the next call.
func TestManager_GetOwnedFailsClosedWhenTheRecordCannotBeRead(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	ctx := tenantContext("tenant-a")
	if _, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	boom := errors.New("catalog unavailable")
	cat.getErr = boom
	if db, err := mgr.GetOwned("orders", "tenant-a"); !errors.Is(err, boom) {
		t.Fatalf("GetOwned with an unreadable record = %v, %v; want the read error", db, err)
	}
	if _, ok := mgr.Local("orders", "tenant-a"); !ok {
		t.Fatal("an unreadable record released the open database")
	}
	cat.getErr = nil
	if _, err := mgr.GetOwned("orders", "tenant-a"); err != nil {
		t.Fatalf("GetOwned once the record reads again: %v", err)
	}
}
