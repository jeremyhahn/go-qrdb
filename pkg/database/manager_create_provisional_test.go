package database

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newProvisionalPeers returns two managers, as two nodes, sharing one
// catalog: what one creates, the other reads through the catalog alone.
func newProvisionalPeers(t *testing.T) (creator, peer *Manager, cat *KVCatalog) {
	t.Helper()
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	creator = NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	creator.SetCatalog(cat)
	peer = NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	peer.SetCatalog(cat)
	t.Cleanup(func() {
		_ = creator.Close()
		_ = peer.Close()
	})
	return creator, peer, cat
}

// assertNotServing checks that peer, holding nothing open, neither opens
// nor resolves the record the catalog holds for name.
func assertNotServing(t *testing.T, peer *Manager, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := peer.Open(ctx, "", name)
	var notFound *NotFoundError
	assert.ErrorAs(t, err, &notFound, "a peer must not open a record the create guard has not accepted")
	_, err = peer.GetOwned(name, "")
	assert.Error(t, err, "GetOwned must not open it either")
	_, _, ok, err := peer.RecordedIdentity(ctx, "", name)
	assert.NoError(t, err)
	assert.False(t, ok, "the raft resolver must not resolve it")
	require.NoError(t, peer.OpenAll(ctx))
	_, held := peer.Local(name, "")
	assert.False(t, held, "OpenAll must not open it")
}

func TestManager_Create_RecordNonServingUntilGuardAccepts(t *testing.T) {
	creator, peer, cat := newProvisionalPeers(t)
	guardRan := false
	ctx := WithCreateGuard(context.Background(), func(gctx context.Context) error {
		guardRan = true
		meta, err := cat.Get(gctx, "", "orders")
		require.NoError(t, err, "the guard runs with the record in the catalog")
		assert.True(t, meta.Provisional, "the record is provisional while the guard runs")
		assertNotServing(t, peer, "orders")
		return nil
	})

	_, err := creator.CreateWithCredentials(ctx, DatabaseConfig{Name: "orders", Backend: BackendMemory}, seal.Credentials{})
	require.NoError(t, err)
	require.True(t, guardRan)

	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err)
	assert.False(t, meta.Provisional, "an accepted create promotes its record")
	_, err = peer.Open(context.Background(), "", "orders")
	assert.NoError(t, err, "a promoted record opens on a peer")
}

func TestManager_Create_GuardRefusalWithFailedCleanupStaysNonServing(t *testing.T) {
	creator, peer, cat := newProvisionalPeers(t)
	deleteErr := errors.New("catalog delete timed out")
	fault := &settleFaultCatalog{Catalog: cat, deleteErr: deleteErr}
	creator.SetCatalog(fault)
	refused := errors.New("tenant tightened to strict")
	ctx := WithCreateGuard(context.Background(), func(context.Context) error { return refused })

	_, err := creator.CreateWithCredentials(ctx, DatabaseConfig{Name: "orders", Backend: BackendMemory}, seal.Credentials{})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan)
	assert.ErrorIs(t, err, refused)

	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err, "the record the cleanup could not remove is kept")
	assert.True(t, meta.Provisional, "a refused record is never promoted")
	assertNotServing(t, peer, "orders")

	// The store recovers: a drop of the name reclaims the stranded record,
	// and the name can be created again.
	fault.deleteErr = nil
	require.NoError(t, creator.DropForTenant(context.Background(), "orders"))
	_, err = cat.Get(context.Background(), "", "orders")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound, "the reclaimed record is gone")

	_, err = creator.CreateWithCredentials(context.Background(), DatabaseConfig{Name: "orders", Backend: BackendMemory}, seal.Credentials{})
	require.NoError(t, err, "a re-create of a reclaimed name succeeds")
	meta, err = cat.Get(context.Background(), "", "orders")
	require.NoError(t, err)
	assert.False(t, meta.Provisional, "the re-create is promoted")
	_, held := creator.Local("orders", "")
	assert.True(t, held, "the creator holds the re-created database open")
	_, err = peer.Open(context.Background(), "", "orders")
	require.NoError(t, err, "the peer opens the re-created database")
}

// strandedProvisional leaves a provisional record of name behind: a create
// the guard refuses, whose cleanup cannot remove the record. It returns the
// record and clears the fault.
func strandedProvisional(t *testing.T, creator *Manager, cat *KVCatalog, name string) DatabaseMeta {
	t.Helper()
	fault := &settleFaultCatalog{Catalog: cat, deleteErr: errors.New("catalog delete timed out")}
	creator.SetCatalog(fault)
	ctx := WithCreateGuard(context.Background(), func(context.Context) error { return errors.New("refused") })
	_, err := creator.CreateWithCredentials(ctx, DatabaseConfig{Name: name, Backend: BackendMemory}, seal.Credentials{})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan)
	creator.SetCatalog(cat)
	meta, err := cat.Get(context.Background(), "", name)
	require.NoError(t, err)
	require.True(t, meta.Provisional)
	return meta
}

func TestManager_DropIncarnationForTenant_ReclaimsProvisional_WrongIDSuperseded(t *testing.T) {
	creator, peer, cat := newProvisionalPeers(t)
	meta := strandedProvisional(t, creator, cat, "orders")
	ctx := context.Background()

	err := creator.DropIncarnationForTenant(ctx, "orders", "another-id", meta.Incarnation)
	var superseded *DropSupersededError
	require.ErrorAs(t, err, &superseded, "a wrong id is refused")
	err = creator.DropIncarnationForTenant(ctx, "orders", meta.ID, "another-incarnation")
	require.ErrorAs(t, err, &superseded, "a wrong incarnation is refused")
	kept, err := cat.Get(ctx, "", "orders")
	require.NoError(t, err, "a refused reclaim keeps the record")
	assert.Equal(t, meta.Incarnation, kept.Incarnation)
	assert.Empty(t, kept.DropClaim, "a refused reclaim takes no claim")

	require.NoError(t, creator.DropIncarnationForTenant(ctx, "orders", meta.ID, meta.Incarnation))
	_, err = cat.Get(ctx, "", "orders")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound, "the right id and incarnation reclaim the record")
	assertNotServing(t, peer, "orders")

	err = creator.DropForTenant(ctx, "orders")
	require.ErrorAs(t, err, &notFound, "a drop with nothing to reclaim stays NotFound")
}

// reclaimRaceCatalog claims a provisional record for a reclaim (sets its
// DropClaim) at the first Update -- after the create's catalog write and
// before its promotion.
type reclaimRaceCatalog struct {
	Catalog
	claimed bool
}

func (c *reclaimRaceCatalog) Update(ctx context.Context, tenantID, name, incarnation string, mutate func(*DatabaseMeta) error) error {
	if !c.claimed {
		if err := c.Catalog.Update(ctx, tenantID, name, incarnation, func(meta *DatabaseMeta) error {
			if meta.Provisional {
				meta.DropClaim = "reclaimer"
			}
			return nil
		}); err != nil {
			return err
		}
		c.claimed = true
	}
	return c.Catalog.Update(ctx, tenantID, name, incarnation, mutate)
}

func TestManager_Create_PromotionRefusedAfterReclaimClaim(t *testing.T) {
	creator, peer, cat := newProvisionalPeers(t)
	creator.SetCatalog(&reclaimRaceCatalog{Catalog: cat})
	ctx := WithCreateGuard(context.Background(), func(context.Context) error { return nil })

	_, err := creator.CreateWithCredentials(ctx, DatabaseConfig{Name: "orders", Backend: BackendMemory}, seal.Credentials{})
	var inProgress *DropInProgressError
	require.ErrorAs(t, err, &inProgress, "a claimed record is never promoted")
	var orphan *CatalogOrphanError
	assert.False(t, errors.As(err, &orphan), "the claimed record is the reclaimer's, not an orphan")
	_, held := creator.Local("orders", "")
	assert.False(t, held, "the creator never opens the claimed record")

	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err, "the record is left to the reclaimer")
	assert.True(t, meta.Provisional)
	assertNotServing(t, peer, "orders")

	// A reclaim that stopped before its delete is taken over by the next.
	creator.SetCatalog(cat)
	require.NoError(t, creator.DropForTenant(context.Background(), "orders"))
	_, err = cat.Get(context.Background(), "", "orders")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// reclaimSuccessorCatalog runs succeed before the first promotion Update:
// a drop reclaims the provisional record and a successor re-creates the
// name, so the stale create's promotion finds its incarnation gone.
type reclaimSuccessorCatalog struct {
	Catalog
	succeed func(ctx context.Context, tenantID, name, incarnation string)
	ran     bool
}

func (c *reclaimSuccessorCatalog) Update(ctx context.Context, tenantID, name, incarnation string, mutate func(*DatabaseMeta) error) error {
	if !c.ran {
		c.ran = true
		c.succeed(ctx, tenantID, name, incarnation)
	}
	return c.Catalog.Update(ctx, tenantID, name, incarnation, mutate)
}

// listDirs returns every directory under root.
func listDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	}))
	return dirs
}

// TestManager_Create_ReclaimedCreateKeepsSuccessorDirs: a create whose
// provisional record a drop reclaimed, and whose name a successor then
// re-created in the same directories, fails without removing them.
func TestManager_Create_ReclaimedCreateKeepsSuccessorDirs(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	base := t.TempDir()
	creator := NewManager(nil, base, WithKeyspaceReclaimer(&spyReclaimer{}))
	successor := NewManager(nil, base, WithKeyspaceReclaimer(&spyReclaimer{}))
	successor.SetCatalog(cat)
	t.Cleanup(func() {
		_ = creator.Close()
		_ = successor.Close()
	})
	ctx := WithCreateGuard(context.Background(), func(context.Context) error { return nil })
	var successorDirs []string
	var successorIncarnation string
	creator.SetCatalog(&reclaimSuccessorCatalog{Catalog: cat, succeed: func(_ context.Context, tenantID, name, incarnation string) {
		require.NoError(t, cat.Delete(context.Background(), tenantID, name, incarnation, ""), "the reclaim")
		_, err := successor.CreateWithCredentials(ctx, DatabaseConfig{Name: name, Backend: BackendMemory}, seal.Credentials{})
		require.NoError(t, err, "the successor re-creates the name")
		meta, err := cat.Get(context.Background(), tenantID, name)
		require.NoError(t, err)
		successorIncarnation = meta.Incarnation
		successorDirs = listDirs(t, base)
	}})

	_, err = creator.CreateWithCredentials(ctx, DatabaseConfig{Name: "orders", Backend: BackendMemory}, seal.Credentials{})
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound, "the stale create is never promoted")
	var orphan *CatalogOrphanError
	assert.False(t, errors.As(err, &orphan), "the successor's record is not the stale create's orphan")
	_, held := creator.Local("orders", "")
	assert.False(t, held)

	require.NotEmpty(t, successorDirs)
	for _, dir := range successorDirs {
		_, statErr := os.Stat(dir)
		assert.NoError(t, statErr, "the successor's directory %s survives", dir)
	}
	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err)
	assert.Equal(t, successorIncarnation, meta.Incarnation, "the successor's record survives")
	_, held = successor.Local("orders", "")
	assert.True(t, held, "the successor still serves the name")
}

// A provisioned database is created on every node at boot under the one
// shared incarnation, with no API guard to accept it: its record serves at
// once. A peer provisioning the same name while the first node is still
// between writing the record and finishing its create finds the database
// rather than being refused with AlreadyExistsError (a restarted node of a
// rolling restart exited at boot this way).
func TestManager_Provision_PeerFindsRecordMidCreate(t *testing.T) {
	creator, peer, cat := newProvisionalPeers(t)
	cfg := DatabaseConfig{Name: "system", Backend: BackendMemory}
	guardRan := false
	ctx := WithCreateGuard(context.Background(), func(gctx context.Context) error {
		guardRan = true
		meta, err := cat.Get(gctx, "", "system")
		require.NoError(t, err, "the record is in the catalog")
		assert.False(t, meta.Provisional, "a provisioned record is never provisional")
		db, err := peer.Provision(gctx, cfg)
		require.NoError(t, err, "a peer provisioning the name mid-create finds it")
		assert.NotNil(t, db)
		return nil
	})

	_, err := creator.Provision(ctx, cfg)
	require.NoError(t, err)
	require.True(t, guardRan)
}

// A node restarting on the catalog its earlier run provisioned into opens
// the recorded database again rather than failing with AlreadyExistsError.
func TestManager_Provision_RestartFindsRecord(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	cfg := DatabaseConfig{Name: "system", Backend: BackendMemory}

	first := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	first.SetCatalog(cat)
	_, err = first.Provision(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	restarted := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	restarted.SetCatalog(cat)
	t.Cleanup(func() { _ = restarted.Close() })
	db, err := restarted.Provision(context.Background(), cfg)
	require.NoError(t, err, "a restarted node finds its provisioned database")
	assert.NotNil(t, db)
	meta, err := cat.Get(context.Background(), "", "system")
	require.NoError(t, err)
	assert.False(t, meta.Provisional)
}
