package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// hookCatalog is a Catalog whose ListTenant runs afterList between reading
// the records and returning them, and whose Get fails with getErr when it is
// set. It makes the window between a list's snapshot and its reconciliation
// of the open map deterministic.
type hookCatalog struct {
	Catalog
	afterList func()
	getErr    error
	// afterGet runs once, after the next Get has read its record and before
	// the caller sees it: the window between a check and the act it guards.
	afterGet func()
}

var _ Catalog = (*hookCatalog)(nil)

func (c *hookCatalog) ListTenant(ctx context.Context, tenantID string) ([]DatabaseMeta, error) {
	metas, err := c.Catalog.ListTenant(ctx, tenantID)
	if c.afterList != nil {
		hook := c.afterList
		c.afterList = nil
		hook()
	}
	return metas, err
}

func (c *hookCatalog) Get(ctx context.Context, tenantID, name string) (DatabaseMeta, error) {
	if c.getErr != nil {
		return DatabaseMeta{}, c.getErr
	}
	meta, err := c.Catalog.Get(ctx, tenantID, name)
	if hook := c.afterGet; hook != nil {
		c.afterGet = nil
		hook()
	}
	return meta, err
}

// catalogGetFailedError is hookCatalog's read failure.
type catalogGetFailedError struct{}

func (catalogGetFailedError) Error() string { return "catalog get failed" }

func newHookCatalog(t *testing.T) *hookCatalog {
	t.Helper()
	inner, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	return &hookCatalog{Catalog: inner}
}

// A create that finishes after a list read the catalog is absent from the
// list's snapshot, but its record is live: the list must not release it.
func TestManager_ListForTenant_KeepsADatabaseCreatedAfterItsSnapshot(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	var created *Database
	cat.afterList = func() {
		db, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders"))
		if err != nil {
			t.Errorf("CreateForTenant inside the list window: %v", err)
			return
		}
		created = db
	}
	if _, err := mgr.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if created == nil {
		t.Fatal("the create inside the list window did not run")
	}

	if created.Status() != StatusOpen {
		t.Fatalf("created database status = %v after the list, want open", created.Status())
	}
	if _, err := os.Stat(mgr.dbDir("tenant-a", "orders")); err != nil {
		t.Fatalf("created database directory after the list: %v", err)
	}
	got, err := mgr.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if got != created {
		t.Fatal("GetForTenant returned a reopened database, want the one the create returned")
	}
}

// An entry whose record cannot be re-read is kept: the release is
// destructive, and only a record known to be gone or replaced justifies it.
func TestManager_ListForTenant_KeepsAnEntryWhoseRecordCannotBeRead(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	db, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	// The record goes behind the manager's back, and its re-read fails.
	if err := cat.Catalog.Delete(ctx, "tenant-a", "orders", db.Incarnation(), ""); err != nil {
		t.Fatalf("catalog Delete: %v", err)
	}
	cat.getErr = catalogGetFailedError{}
	if _, err := mgr.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if db.Status() != StatusOpen {
		t.Fatalf("status = %v with an unreadable record, want open", db.Status())
	}
	if _, ok := mgr.databases.Load(keyFor("tenant-a", "orders")); !ok {
		t.Fatal("entry released although its record could not be read")
	}

	// Once the record reads as gone, the next list releases it.
	cat.getErr = nil
	if _, err := mgr.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if _, ok := mgr.databases.Load(keyFor("tenant-a", "orders")); ok {
		t.Fatal("entry kept although its record is gone")
	}
	var nf *NotFoundError
	if _, err := mgr.GetForTenant(ctx, "orders"); !errors.As(err, &nf) {
		t.Fatalf("GetForTenant after the release = %v, want *NotFoundError", err)
	}
}

// A list on one node during another node's fenced drop fences its local
// entry; once that drop rolls its fence back, the next list clears it.
func TestManager_ListForTenant_ClearsAFenceRolledBackElsewhere(t *testing.T) {
	cat := newHookCatalog(t)
	a := NewManager(nil, t.TempDir())
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir())
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	dbA, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	dbB, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}

	// A's drop fences the record; B's list applies it.
	dbA.SetDropping(true)
	if err := a.claimDrop(ctx, dbA, "test-claim"); err != nil {
		t.Fatalf("fence on A: %v", err)
	}
	if _, err := b.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant on B: %v", err)
	}
	if !dbB.Dropping() {
		t.Fatal("B's entry is not fenced after A fenced the record")
	}

	// A's drop fails and rolls the fence back; B's next list clears it.
	dbA.SetDropping(false)
	if err := a.releaseDropClaim(ctx, dbA, "test-claim"); err != nil {
		t.Fatalf("rollback on A: %v", err)
	}
	if _, err := b.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant on B: %v", err)
	}
	if dbB.Dropping() {
		t.Fatal("B's entry is still fenced after A rolled the fence back")
	}
	if _, err := b.GetOwned("orders", "tenant-a"); err != nil {
		t.Fatalf("GetOwned on B after the rollback: %v", err)
	}
}

// A list never clears the fence of a drop in flight on its own node, nor one
// committed after its snapshot was taken.
func TestManager_ListForTenant_KeepsALocalDropsFence(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	db, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	key := keyFor("tenant-a", "orders")

	// A local drop in flight: claimed and fenced in memory, its durable
	// fence not yet written.
	mgr.dropping.Store(key, struct{}{})
	db.SetDropping(true)
	if _, err := mgr.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if !db.Dropping() {
		t.Fatal("a list cleared the fence of a drop in flight on this node")
	}
	mgr.dropping.Delete(key)

	// A fence committed after the list's snapshot: the snapshot says no
	// fence, the record now says fenced.
	cat.afterList = func() {
		if err := mgr.claimDrop(ctx, db, "test-claim"); err != nil {
			t.Errorf("fence inside the list window: %v", err)
		}
	}
	if _, err := mgr.ListForTenant(ctx); err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if !db.Dropping() {
		t.Fatal("a list cleared a fence committed after its snapshot")
	}
	var dropping *DroppingError
	if _, err := mgr.GetOwned("orders", "tenant-a"); !errors.As(err, &dropping) {
		t.Fatalf("GetOwned on a fenced database = %v, want *DroppingError", err)
	}
}

// markerFactory opens every backend through the embedded factory and leaves
// a marker file in the data directory it was given, so a test can tell
// whether the files of the database opened last are still on disk.
type markerFactory struct {
	BackendFactory
}

const openedMarker = "opened"

func (f markerFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dataDir, openedMarker), nil, 0o600); err != nil {
		return nil, err
	}
	return f.BackendFactory.Create(cfg, dataDir)
}

// A drop that passed its incarnation check and then paused while another
// node finished it and created the name again cannot remove what this node
// opens for the successor in the meantime: the local teardown holds the
// same claim a lookup's release and open take (go-qrdb#893).
func TestManager_Drop_PausedDropCannotTearDownItsSuccessor(t *testing.T) {
	cat := newHookCatalog(t)
	spy := &spyReclaimer{}
	mgr := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy),
		WithBackendFactory(markerFactory{NewEmbeddedBackendFactory()}))
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	stale, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	key := keyFor("tenant-a", "orders")
	marker := filepath.Join(mgr.dbDir("tenant-a", "orders"), dataSubDir, openedMarker)

	// The drop's next catalog read after its reclamation is its incarnation
	// check. Once that read has the drop's own record, another node takes
	// the drop over, finishes it and creates the name again, and a lookup
	// on this node reconciles against the successor's record.
	armed := false
	spy.before = func() {
		if armed {
			return
		}
		armed = true
		cat.afterGet = func() {
			meta, err := cat.Catalog.Get(ctx, "tenant-a", "orders")
			require.NoError(t, err)
			require.NoError(t, cat.Catalog.Delete(ctx, "tenant-a", "orders", meta.Incarnation, meta.DropClaim))
			require.NoError(t, cat.Catalog.Create(ctx, DatabaseMeta{TenantID: "tenant-a", Name: "orders", Backend: BackendMemory, Incarnation: "successor"}))
			_, err = mgr.ListForTenant(ctx)
			require.NoError(t, err)
		}
	}

	var superseded *DropSupersededError
	require.ErrorAs(t, mgr.DropForTenant(ctx, "orders"), &superseded)
	assert.True(t, armed, "the drop never reached its reclamation")
	assert.Nil(t, cat.afterGet, "the drop never read its record after the reclamation")
	assert.Equal(t, StatusClosed, stale.Status(), "the stale incarnation was left open")

	_, err = mgr.ListForTenant(ctx)
	require.NoError(t, err)
	val, ok := mgr.databases.Load(key)
	require.True(t, ok, "the successor did not open on this node")
	successor := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	assert.Equal(t, "successor", successor.Incarnation())
	assert.NotEqual(t, StatusClosed, successor.Status(), "the stale drop closed the successor")
	_, statErr := os.Stat(marker)
	assert.NoError(t, statErr, "the stale drop removed the successor's files")
	meta, err := cat.Get(ctx, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Equal(t, "successor", meta.Incarnation, "the stale drop removed the successor's record")
}

func TestManager_TryClaim_IsExclusiveUntilReleased(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	key := keyFor("tenant-a", "orders")

	unclaim, ok := mgr.tryClaim(key)
	require.True(t, ok, "the first claim of a free name was refused")
	_, ok = mgr.tryClaim(key)
	assert.False(t, ok, "a held claim was granted twice")
	_, ok = mgr.tryClaim(keyFor("tenant-b", "orders"))
	assert.True(t, ok, "a claim on one tenant's name blocked another tenant's")

	unclaim()
	again, ok := mgr.tryClaim(key)
	require.True(t, ok, "a released claim could not be taken again")
	again()
}

func TestManager_TryClaim_ReleaseWakesAWaiter(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	key := keyFor("tenant-a", "orders")

	unclaim, ok := mgr.tryClaim(key)
	require.True(t, ok)
	held, ok := mgr.inFlight.Load(key)
	require.True(t, ok, "a held claim is not recorded")
	unclaim()
	select {
	case <-held.(chan struct{}): //nolint:errcheck // type assertion on a value this container only ever holds
	default:
		t.Fatal("releasing a claim did not signal the waiters on it")
	}
	_, ok = mgr.inFlight.Load(key)
	assert.False(t, ok, "a released claim is still recorded")
}

func TestManager_AwaitClaim_WaitsForTheHolder(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	key := keyFor("tenant-a", "orders")

	unclaim, ok := mgr.tryClaim(key)
	require.True(t, ok)
	acquired := make(chan func(), 1)
	go func() {
		next, err := mgr.awaitClaim(context.Background(), key)
		if err != nil {
			t.Errorf("awaitClaim: %v", err)
			close(acquired)
			return
		}
		acquired <- next
	}()
	unclaim()
	next, ok := <-acquired
	require.True(t, ok, "the waiter never took the released claim")
	_, ok = mgr.tryClaim(key)
	assert.False(t, ok, "the waiter's claim is not held")
	next()
}

func TestManager_AwaitClaim_GivesUpWithTheContext(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	key := keyFor("tenant-a", "orders")

	unclaim, ok := mgr.tryClaim(key)
	require.True(t, ok)
	defer unclaim()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mgr.awaitClaim(ctx, key)
	assert.ErrorIs(t, err, context.Canceled)

	free, err := mgr.awaitClaim(ctx, keyFor("tenant-a", "invoices"))
	require.NoError(t, err, "a free claim was refused on a done context")
	free()
}

// newClaimDropManager returns a manager holding one database, "orders", of
// tenant-a, with a reclaimer whose reclamation runs before.
func newClaimDropManager(t *testing.T, before func(*Manager, *Database)) (*Manager, *Database, *spyReclaimer) {
	t.Helper()
	cat := newHookCatalog(t)
	spy := &spyReclaimer{}
	mgr := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.CreateForTenant(tenantContext("tenant-a"), validMemoryConfig("orders"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(mgr.dbDir("tenant-a", "orders"), 0o700))
	if before != nil {
		spy.before = func() { before(mgr, db) }
	}
	return mgr, db, spy
}

// A drop whose entry a release removed before the drop took the claim is
// superseded and leaves the name's directory alone.
func TestManager_Drop_EntryReleasedBeforeTheClaimIsSuperseded(t *testing.T) {
	mgr, db, _ := newClaimDropManager(t, func(mgr *Manager, db *Database) {
		mgr.databases.CompareAndDelete(keyFor("tenant-a", "orders"), db)
	})

	var superseded *DropSupersededError
	require.ErrorAs(t, mgr.DropForTenant(tenantContext("tenant-a"), "orders"), &superseded)
	_, statErr := os.Stat(mgr.dbDir("tenant-a", "orders"))
	assert.NoError(t, statErr, "a superseded drop removed the name's directory")
	assert.NotEqual(t, StatusClosed, db.Status(), "a superseded drop closed the database")
}

// A drop whose context ends while another holds the name's claim stops
// before the teardown, fenced, and a retry completes it.
func TestManager_Drop_GivesUpTheClaimWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(tenantContext("tenant-a"))
	var unclaim func()
	mgr, db, spy := newClaimDropManager(t, func(mgr *Manager, _ *Database) {
		var ok bool
		unclaim, ok = mgr.tryClaim(keyFor("tenant-a", "orders"))
		require.True(t, ok)
		cancel()
	})

	var dropErr *DropError
	require.ErrorAs(t, mgr.DropForTenant(ctx, "orders"), &dropErr)
	assert.ErrorIs(t, dropErr, context.Canceled)
	assert.True(t, db.Dropping(), "a drop that gave up the claim cleared its fence")
	_, statErr := os.Stat(mgr.dbDir("tenant-a", "orders"))
	assert.NoError(t, statErr, "a drop that never held the claim removed the directory")

	unclaim()
	spy.before = nil
	require.NoError(t, mgr.DropForTenant(tenantContext("tenant-a"), "orders"))
	_, ok := mgr.databases.Load(keyFor("tenant-a", "orders"))
	assert.False(t, ok, "the retried drop left the entry")
}

// A drop that finds the name claimed waits for the claim and then
// completes.
func TestManager_Drop_WaitsForAConcurrentClaim(t *testing.T) {
	claimed := make(chan func(), 1)
	mgr, _, _ := newClaimDropManager(t, func(mgr *Manager, _ *Database) {
		unclaim, ok := mgr.tryClaim(keyFor("tenant-a", "orders"))
		if !ok {
			t.Error("the name was already claimed")
			close(claimed)
			return
		}
		claimed <- unclaim
	})

	dropped := make(chan error, 1)
	go func() { dropped <- mgr.DropForTenant(tenantContext("tenant-a"), "orders") }()
	unclaim, ok := <-claimed
	require.True(t, ok)
	unclaim()
	require.NoError(t, <-dropped)
	_, statErr := os.Stat(mgr.dbDir("tenant-a", "orders"))
	assert.True(t, os.IsNotExist(statErr), "the drop did not remove the directory")
}
