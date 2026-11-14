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
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// This file covers an entry this node holds open after the catalog moved on
// to another incarnation, when release has to keep it: the obsolete entry is
// refused with StaleDatabaseError and never served, by GetOwned or by Open.

// laterGetFaultCatalog fails every Get after the first failAfter, once armed:
// the resolve's own read succeeds and release's read-back fails.
type laterGetFaultCatalog struct {
	Catalog
	failAfter int32
	gets      atomic.Int32
	err       error
}

func (c *laterGetFaultCatalog) Get(ctx context.Context, tenantID, name string) (DatabaseMeta, error) {
	if c.err != nil && c.gets.Add(1) > c.failAfter {
		return DatabaseMeta{}, c.err
	}
	return c.Catalog.Get(ctx, tenantID, name)
}

// newStaleEntry returns a manager holding "orders" open while the catalog
// records a successor incarnation created through another node.
func newStaleEntry(t *testing.T) (*Manager, *laterGetFaultCatalog, *Database) {
	t.Helper()
	inner, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	cat := &laterGetFaultCatalog{Catalog: inner, failAfter: 1}
	mgr := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })

	old, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, inner.Delete(ctx, "", "orders", old.Incarnation(), ""))
	require.NoError(t, inner.Create(ctx, DatabaseMeta{ID: "test-id", Name: "orders", Backend: BackendMemory, Incarnation: "successor"}))
	return mgr, cat, old
}

// assertRefused checks that both resolve paths refuse the obsolete entry.
func assertRefused(t *testing.T, mgr *Manager, wantErr error) {
	t.Helper()
	var stale *StaleDatabaseError

	db, err := mgr.GetOwned("orders", "")
	require.ErrorAs(t, err, &stale, "GetOwned refuses the entry it could not release")
	assert.Nil(t, db, "GetOwned never returns the stale entry")
	assert.Equal(t, "orders", stale.Name)
	if wantErr != nil {
		assert.ErrorIs(t, err, wantErr)
	}

	db, err = mgr.Open(context.Background(), "", "orders")
	require.ErrorAs(t, err, &stale, "Open's fast path refuses the obsolete entry")
	assert.Nil(t, db, "Open never returns the stale entry")
}

func TestManager_GetOwned_BusyReleaseRefusesStaleEntry(t *testing.T) {
	mgr, _, old := newStaleEntry(t)
	unclaim, claimed := mgr.tryClaim(keyFor("", "orders"))
	require.True(t, claimed)

	assertRefused(t, mgr, nil)
	local, ok := mgr.Local("orders", "")
	require.True(t, ok, "the busy release kept the entry")
	assert.Same(t, old, local)

	unclaim()
	db, err := mgr.GetOwned("orders", "")
	require.NoError(t, err, "a clean release re-opens the current incarnation")
	assert.Equal(t, "successor", db.Incarnation())
	assert.NotSame(t, old, db)
}

func TestManager_GetOwned_FailedReadBackRefusesStaleEntry(t *testing.T) {
	mgr, cat, old := newStaleEntry(t)
	readErr := errors.New("catalog unreachable")
	cat.err = readErr

	assertRefused(t, mgr, readErr)
	local, ok := mgr.Local("orders", "")
	require.True(t, ok, "the release that could not re-read kept the entry")
	assert.Same(t, old, local)

	cat.err = nil
	db, err := mgr.GetOwned("orders", "")
	require.NoError(t, err)
	assert.Equal(t, "successor", db.Incarnation())
}

func TestManager_ListForTenant_SkipsStaleEntryItCouldNotRelease(t *testing.T) {
	mgr, _, _ := newStaleEntry(t)
	unclaim, claimed := mgr.tryClaim(keyFor("", "orders"))
	require.True(t, claimed)
	defer unclaim()

	infos, err := mgr.ListForTenant(context.Background())
	require.NoError(t, err)
	for _, info := range infos {
		assert.NotEqual(t, "orders", info.Name, "the obsolete entry is not listed")
	}
}

func TestManager_Reconcile_CurrentRecordClearsObsoleteMark(t *testing.T) {
	mgr, cat, old := newStaleEntry(t)
	key := keyFor("", "orders")
	ctx := context.Background()
	meta, err := cat.Catalog.Get(ctx, "", "orders")
	require.NoError(t, err)

	old.obsolete.Store(true)
	meta.Incarnation = old.Incarnation()
	released, err := mgr.reconcile(ctx, cat, key, old, meta, true)
	require.NoError(t, err)
	assert.False(t, released)
	db, err := mgr.Open(ctx, "", "orders")
	require.NoError(t, err, "a record of the entry's incarnation makes it current again")
	assert.Same(t, old, db)
}

func TestStaleDatabaseError(t *testing.T) {
	busy := &StaleDatabaseError{Name: "orders"}
	assert.Contains(t, busy.Error(), `"orders"`)
	assert.NoError(t, busy.Unwrap())

	cause := errors.New("catalog unreachable")
	failed := &StaleDatabaseError{Name: "orders", Err: cause}
	assert.Contains(t, failed.Error(), cause.Error())
	assert.ErrorIs(t, failed, cause)
}
