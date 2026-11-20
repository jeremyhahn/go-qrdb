package database

import (
	"context"
	"errors"
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
	creator.SetCatalog(&settleFaultCatalog{Catalog: cat, deleteErr: deleteErr})
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
}
