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

package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// This file covers a catalog create whose outcome is indeterminate: the
// record committed (or another node's did) and the create was told it
// failed. The rollback must not leave a record with no database behind it,
// and must never remove a record that is not its own.

// indeterminateStore is the catalog's system store. Its next PutIfAbsent
// commits -- the value it was asked to write, or, with other set, another
// node's record of the same key -- and then reports what a Raft proposal
// reports when leadership moved or its wait ended after the entry was
// handed to Raft.
type indeterminateStore struct {
	*kvstore.MemoryKVStore
	armed bool
	other *DatabaseMeta
}

func (s *indeterminateStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if !s.armed {
		return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
	}
	s.armed = false
	if s.other != nil {
		data, err := json.Marshal(s.other)
		if err != nil {
			return false, err
		}
		value = data
	}
	if _, err := s.MemoryKVStore.PutIfAbsent(ctx, key, value); err != nil {
		return false, err
	}
	return false, &quicraft.MaybeAppliedError{Err: context.DeadlineExceeded}
}

// settleFaultCatalog fails the catalog's Get or Delete, after the write has
// already been settled into the store underneath it.
type settleFaultCatalog struct {
	Catalog
	getErr    error
	deleteErr error
}

func (c *settleFaultCatalog) Get(ctx context.Context, tenantID, name string) (DatabaseMeta, error) {
	if c.getErr != nil {
		return DatabaseMeta{}, c.getErr
	}
	return c.Catalog.Get(ctx, tenantID, name)
}

func (c *settleFaultCatalog) Delete(ctx context.Context, tenantID, name, incarnation, dropClaim string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	return c.Catalog.Delete(ctx, tenantID, name, incarnation, dropClaim)
}

// newSettleManager returns a manager with a reclaimer -- so each create
// writes its own incarnation -- over a catalog whose store is returned.
func newSettleManager(t *testing.T) (*Manager, *indeterminateStore, *KVCatalog) {
	t.Helper()
	store := &indeterminateStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	cat, err := NewKVCatalog(store, nil)
	require.NoError(t, err)
	mgr := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, store, cat
}

func TestManager_Create_IndeterminateWriteRemovesItsOwnRecord(t *testing.T) {
	mgr, store, cat := newSettleManager(t)
	store.armed = true

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.Error(t, err)
	assert.True(t, quicraft.MaybeApplied(err), "the write's own error is returned: %v", err)
	var orphan *CatalogOrphanError
	assert.False(t, errors.As(err, &orphan), "the record was settled: %v", err)

	_, err = cat.Get(context.Background(), "", "orders")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound, "the committed record was removed")
	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.True(t, os.IsNotExist(err), "the create's directory was removed: %v", err)

	db, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.NoError(t, err, "the name is free for a retry")
	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err)
	assert.Equal(t, db.Incarnation(), meta.Incarnation)
}

func TestManager_Create_IndeterminateWriteLeavesAnotherIncarnation(t *testing.T) {
	mgr, store, cat := newSettleManager(t)
	theirs := DatabaseMeta{ID: "test-id", Name: "orders", Backend: BackendMemory, Incarnation: "their-incarnation"}
	store.armed, store.other = true, &theirs

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.Error(t, err)
	var orphan *CatalogOrphanError
	assert.False(t, errors.As(err, &orphan), "another node's record is no orphan: %v", err)

	meta, err := cat.Get(context.Background(), "", "orders")
	require.NoError(t, err, "another node's record is never deleted")
	assert.Equal(t, "their-incarnation", meta.Incarnation)
	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.True(t, os.IsNotExist(err), "this create's directory was removed: %v", err)
}

func TestManager_Create_IndeterminateWriteDeleteFailsKeepsDirectories(t *testing.T) {
	mgr, store, cat := newSettleManager(t)
	deleteErr := errors.New("catalog delete timed out")
	mgr.SetCatalog(&settleFaultCatalog{Catalog: cat, deleteErr: deleteErr})
	store.armed = true

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan)
	assert.Equal(t, "orders", orphan.Name)
	assert.ErrorIs(t, orphan.Cleanup, deleteErr)
	assert.True(t, quicraft.MaybeApplied(err), "the write error stays in the chain")

	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.NoError(t, err, "the directories stay for the drop that removes the record")
	_, err = cat.Get(context.Background(), "", "orders")
	assert.NoError(t, err, "the record the delete could not remove is still there")
}

// A create whose record was written, and which failed at a later step, is
// an orphan when its rollback cannot remove that record: the record names a
// database whose storage and barrier metadata are under its directories, so
// those stay for the drop that removes the record.
func TestManager_Create_LaterStepFailsDeleteFailsKeepsDirectories(t *testing.T) {
	mgr, _, cat := newSettleManager(t)
	unfenceErr := errors.New("unfence refused")
	mgr.reclaimer = &spyReclaimer{unfenceErr: unfenceErr}
	deleteErr := errors.New("catalog delete timed out")
	mgr.SetCatalog(&settleFaultCatalog{Catalog: cat, deleteErr: deleteErr})

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan)
	assert.Equal(t, "orders", orphan.Name)
	assert.ErrorIs(t, orphan.Cleanup, deleteErr)
	assert.ErrorIs(t, err, unfenceErr, "the step that failed the create stays in the chain")

	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.NoError(t, err, "the directories stay for the drop that removes the record")
	_, err = cat.Get(context.Background(), "", "orders")
	assert.NoError(t, err, "the record the delete could not remove is still there")
}

// When the rollback does remove the record, the failed create leaves
// nothing: no record and no directories, and the step's own error.
func TestManager_Create_LaterStepFailsRollsBackRecordAndDirectories(t *testing.T) {
	mgr, _, cat := newSettleManager(t)
	unfenceErr := errors.New("unfence refused")
	mgr.reclaimer = &spyReclaimer{unfenceErr: unfenceErr}

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.ErrorIs(t, err, unfenceErr)
	var orphan *CatalogOrphanError
	assert.False(t, errors.As(err, &orphan), "a confirmed rollback is not an orphan")

	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.True(t, os.IsNotExist(err), "the rollback left the directories")
	_, err = cat.Get(context.Background(), "", "orders")
	var notFound *NotFoundError
	assert.ErrorAs(t, err, &notFound, "the rollback left the record")
}

func TestManager_Create_IndeterminateWriteReadBackFailsKeepsDirectories(t *testing.T) {
	mgr, store, cat := newSettleManager(t)
	getErr := errors.New("catalog unreachable")
	mgr.SetCatalog(&settleFaultCatalog{Catalog: cat, getErr: getErr})
	store.armed = true

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan)
	assert.ErrorIs(t, orphan.Cleanup, getErr)
	assert.Contains(t, orphan.Error(), "orphan")
	_, err = os.Stat(mgr.dbDir("", "orders"))
	assert.NoError(t, err)
}

func TestManager_Create_IndeterminateWriteWithoutIncarnationIsAnOrphan(t *testing.T) {
	store := &indeterminateStore{MemoryKVStore: kvstore.NewMemoryKVStore(), armed: true}
	cat, err := NewKVCatalog(store, nil)
	require.NoError(t, err)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })

	_, err = mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	var orphan *CatalogOrphanError
	require.ErrorAs(t, err, &orphan, "an empty incarnation cannot be attributed to this create")
	assert.NoError(t, orphan.Cleanup)
	_, err = cat.Get(context.Background(), "", "orders")
	assert.NoError(t, err, "a record that cannot be attributed is not deleted")
}

func TestCreateOutcomeUnknown(t *testing.T) {
	write := func(err error) error { return &CatalogWriteError{Key: "k", Err: err} }
	cases := map[string]struct {
		err  error
		want bool
	}{
		"maybe applied":      {write(&quicraft.MaybeAppliedError{Err: errors.New("leader changed")}), true},
		"deadline":           {write(context.DeadlineExceeded), true},
		"canceled":           {write(context.Canceled), true},
		"refused write":      {write(errors.New("disk full")), false},
		"already exists":     {&AlreadyExistsError{Name: "x"}, false},
		"deadline, no write": {context.DeadlineExceeded, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, createOutcomeUnknown(tc.err))
		})
	}
}
