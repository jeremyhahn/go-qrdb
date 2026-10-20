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

package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// postureKey is the system-barrier key of id's barrier posture record.
func postureKey(id string) string { return "__registry:tenant_posture/" + id }

// newRollbackService is newReconcileService over store, with the tenant-undo
// feature active, acknowledging as node-b against node-a and node-b.
func newRollbackService(t *testing.T, store seal.StorageBackend) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	require.NoError(t, reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}))
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithVolumeAllocator(volumeAllocatorFor(t, reg)),
		WithTenantUndoGate(undoActive), WithNodeID("node-b"), WithUndoCleanupRoster(rosterOf("node-a", "node-b")))
	require.NoError(t, err)
	return svc, reg
}

// putUndone writes info's undone tombstone, torn down, with every named
// node's acknowledgement of it, and returns it.
func putUndone(t *testing.T, store *seal.MemoryBackend, info *TenantInfo, acked ...string) *Tombstone {
	t.Helper()
	ctx := context.Background()
	ts := newUndoneTombstone(info, time.Now().UTC())
	ts.TornDown = true
	data, err := json.Marshal(ts)
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, tombstoneKey(info.ID), data))
	for _, node := range acked {
		require.NoError(t, store.Put(ctx, undoneAckKey(info.ID, ts.Undo, node), []byte(`{}`)))
	}
	return ts
}

// A create refused by an undo whose last acknowledgement then landed, and
// whose id another create then released and committed, leaves that create's
// catalog Assignment, posture record and directory alone.
func TestRollbackRegistration_LeavesALaterCreatesRecords(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)

	_, err := svc.CreateTenant(ctx, "vund", "Later", IsolationVolume)
	require.NoError(t, err)
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vund")
	require.NoError(t, err)

	svc.rollbackRegistration(ctx, "vund", IsolationVolume, &TenantUndoPendingError{TenantID: "vund"})

	_, err = volumeAllocatorFor(t, reg).Resolve(ctx, "vund")
	require.NoError(t, err, "the rollback released a later create's Assignment")
	_, err = reg.SystemBarrier().Get(ctx, postureKey("vund"))
	require.NoError(t, err, "the rollback dropped a later create's posture record")
	require.True(t, dirExists(t, dataRoot), "the rollback removed a later create's directory")
}

// A create refused by an undo every node already acknowledged voids those
// acknowledgements before it destroys what it provisioned, so the id cannot
// be released under it; it then acknowledges the new nonce itself.
func TestRollbackRegistration_VoidsTheUndosAcknowledgementsBeforeDestroying(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)

	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vund")
	require.NoError(t, err)
	require.NoError(t, store.Delete(ctx, metadataPrefix+"vund"))
	voided := putUndone(t, store, info, "node-a", "node-b")

	svc.rollbackRegistration(ctx, "vund", IsolationVolume, &TenantUndoPendingError{TenantID: "vund"})

	ts := mustReadTombstone(t, store.Get, "vund")
	require.True(t, ts.Undone() && ts.TornDown, "the fence changed kind: %+v", ts)
	require.NotEqual(t, voided.Undo, ts.Undo, "the acknowledgements the rollback raced were not voided")
	left, err := store.List(ctx, undoneAckScope("vund", voided.Undo))
	require.NoError(t, err)
	assert.Empty(t, left, "acknowledgements of the voided nonce were left behind")
	mustReadAck(t, store.Get, "vund", "node-b")
	assert.False(t, dirExists(t, dataRoot))
	_, err = volumeAllocatorFor(t, reg).Resolve(ctx, "vund")
	assert.Error(t, err, "the refused create's Assignment survived its rollback")

	released, err := releaseUndone(ctx, store, rosterOf("node-a", "node-b"), "vund")
	assert.False(t, released)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	assert.Equal(t, []string{"node-a"}, pending.Pending)
}

// A create that provisioned before an undo of its id was written, and found
// that undo already acknowledged everywhere at its commit, is refused rather
// than released onto records the undo's teardown may have destroyed.
func TestCreateTenant_RefusesAnUndoWrittenWhileItProvisioned(t *testing.T) {
	ctx := context.Background()
	store := &undoBeforeCommit{MemoryBackend: seal.NewMemoryBackend()}
	svc, _ := newRollbackService(t, store)
	store.undo = func() {
		putUndone(t, store.MemoryBackend, &TenantInfo{ID: "vund", IsolationType: IsolationVolume}, "node-a", "node-b")
	}

	_, err := svc.CreateTenant(ctx, "vund", "Late", IsolationVolume)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	_, err = store.Get(ctx, metadataPrefix+"vund")
	assert.True(t, isAbsent(err), "committed over an undo written while it provisioned: %v", err)
	assert.True(t, mustReadTombstone(t, store.Get, "vund").Undone())
}

// undoBeforeCommit writes an undo of the id being created just before the
// create's commit, once.
type undoBeforeCommit struct {
	*seal.MemoryBackend
	undo func()
}

func (s *undoBeforeCommit) PutIfAllAbsent(ctx context.Context, key string, data []byte, fences ...string) (bool, error) {
	if s.undo != nil {
		s.undo()
		s.undo = nil
	}
	return s.MemoryBackend.PutIfAllAbsent(ctx, key, data, fences...)
}

// A create of an id an undo still fences is refused before it provisions
// anything, and changes nothing the undo's release waits on.
func TestCreateTenant_RefusesAPendingUndoBeforeProvisioning(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)
	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "vund"))
	before, err := store.Get(ctx, tombstoneKey("vund"))
	require.NoError(t, err)

	_, err = svc.CreateTenant(ctx, "vund", "Again", IsolationVolume)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	after, err := store.Get(ctx, tombstoneKey("vund"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused create rewrote the undo it waits on")
	_, err = volumeAllocatorFor(t, reg).Resolve(ctx, info.ID)
	assert.Error(t, err, "a refused create provisioned an Assignment")
}

// neverSwaps loses every conditional rewrite.
type neverSwaps struct{ *seal.MemoryBackend }

func (neverSwaps) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

// releaseBeforeSwap releases the tombstone just before the rewrite lands.
type releaseBeforeSwap struct{ *seal.MemoryBackend }

func (s releaseBeforeSwap) CompareAndSwap(ctx context.Context, k string, old, next []byte) (bool, error) {
	if err := s.MemoryBackend.Delete(ctx, k); err != nil {
		return false, err
	}
	return s.MemoryBackend.CompareAndSwap(ctx, k, old, next)
}

func TestFenceRollback(t *testing.T) {
	ctx := context.Background()
	undone := func(tornDown bool, nonce string) []byte {
		data, err := json.Marshal(&Tombstone{ID: "acme", State: TombstoneUndone, Undo: nonce, TornDown: tornDown})
		require.NoError(t, err)
		return data
	}
	put := func(t *testing.T, m *seal.MemoryBackend, data []byte) {
		t.Helper()
		require.NoError(t, m.Put(ctx, tombstoneKey("acme"), data))
	}

	t.Run("a fence that never releases is kept as it is", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"retired":          []byte(`{"id":"acme"}`),
			"unreadable":       []byte(`{`),
			"undone, not torn": undone(false, "n1"),
			"undone, no nonce": undone(true, ""),
		} {
			m := seal.NewMemoryBackend()
			put(t, m, data)
			fenced, err := fenceTeardown(ctx, plainUndoStore{m}, "acme")
			require.NoError(t, err, name)
			assert.True(t, fenced, name)
			got, err := m.Get(ctx, tombstoneKey("acme"))
			require.NoError(t, err)
			assert.Equal(t, data, got, name)
		}
	})
	t.Run("a released id is not fenced", func(t *testing.T) {
		fenced, err := fenceTeardown(ctx, seal.NewMemoryBackend(), "acme")
		require.NoError(t, err)
		assert.False(t, fenced)
	})
	t.Run("a release that lands first wins", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone(true, "n1"))
		fenced, err := fenceTeardown(ctx, releaseBeforeSwap{m}, "acme")
		require.NoError(t, err)
		assert.False(t, fenced)
	})
	t.Run("a torn-down undo is voided under a fresh nonce", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone(true, "n1"))
		require.NoError(t, m.Put(ctx, undoneAckKey("acme", "n1", "node-a"), []byte(`{}`)))
		fenced, err := fenceTeardown(ctx, m, "acme")
		require.NoError(t, err)
		assert.True(t, fenced)
		ts := mustReadTombstone(t, m.Get, "acme")
		assert.True(t, ts.Undone() && ts.TornDown)
		assert.NotEqual(t, "n1", ts.Undo)
		_, err = m.Get(ctx, undoneAckKey("acme", "n1", "node-a"))
		assert.True(t, isAbsent(err), "voided acknowledgement left: %v", err)
	})
	t.Run("a tombstone rewritten on every attempt is pending", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone(true, "n1"))
		fenced, err := fenceTeardown(ctx, neverSwaps{m}, "acme")
		assert.False(t, fenced)
		var pending *TenantUndoPendingError
		assert.True(t, errors.As(err, &pending), "got %v", err)
	})
	t.Run("a store without a conditional rewrite is refused", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone(true, "n1"))
		fenced, err := fenceTeardown(ctx, plainUndoStore{m}, "acme")
		assert.False(t, fenced)
		assert.ErrorIs(t, err, ErrTenantStoreNotAtomic)
	})
	t.Run("store faults are typed", func(t *testing.T) {
		for name, s := range map[string]*faultyUndoStore{
			"get":  {getErr: errStoreFault},
			"swap": {casErr: errStoreFault},
		} {
			s.MemoryBackend = seal.NewMemoryBackend()
			put(t, s.MemoryBackend, undone(true, "n1"))
			fenced, err := fenceTeardown(ctx, s, "acme")
			assert.False(t, fenced, name)
			var se *StoreError
			require.True(t, errors.As(err, &se), "%s: got %v", name, err)
			assert.ErrorIs(t, err, errStoreFault, name)
		}
	})
}

// A rollback that cannot fence the id only forgets its registration.
func TestRollbackRegistration_UnfencedKeepsTheRecords(t *testing.T) {
	ctx := context.Background()
	m := seal.NewMemoryBackend()
	store := &faultyUndoStore{MemoryBackend: m}
	svc, reg := newRollbackService(t, store)
	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	require.NoError(t, m.Delete(ctx, metadataPrefix+"vund"))
	putUndone(t, m, info)
	store.casErr = errStoreFault

	svc.rollbackRegistration(ctx, "vund", IsolationVolume, &TenantUndoPendingError{TenantID: "vund"})

	_, err = volumeAllocatorFor(t, reg).Resolve(ctx, "vund")
	assert.NoError(t, err, "destroyed records under an id it could not fence")
	assert.False(t, svc.undoAckHolds.held("vund"), "the hold outlived the rollback")
}

// A held id is not acknowledged; once released, it is.
func TestReconcileUndone_WithholdsAHeldAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-b")
	createUndone(t, svc, "acme")
	ts := mustReadTombstone(t, store.Get, "acme")
	require.NoError(t, store.Delete(ctx, undoneAckKey("acme", ts.Undo, "node-b")))

	release := svc.undoAckHolds.hold("acme")
	var pending *TenantUndoPendingError
	require.True(t, errors.As(svc.ReconcileUndone(ctx, "acme"), &pending))
	_, err := store.Get(ctx, undoneAckKey("acme", ts.Undo, "node-b"))
	assert.True(t, isAbsent(err), "acknowledged a held id: %v", err)

	release()
	require.NoError(t, svc.ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-b")
}

func TestUndoAckHolds(t *testing.T) {
	t.Run("holds nest", func(t *testing.T) {
		var h undoAckHolds
		assert.False(t, h.held("acme"))
		first, second := h.hold("acme"), h.hold("acme")
		first()
		assert.True(t, h.held("acme"))
		assert.False(t, h.held("other"))
		second()
		assert.False(t, h.held("acme"))
		_, ok := h.m.Load("acme")
		assert.False(t, ok, "a released id's entry was left in the map")
	})
	t.Run("an entry that is not a count is no hold", func(t *testing.T) {
		var h undoAckHolds
		h.m.Store("acme", "not a count")
		assert.False(t, h.held("acme"))
		release := h.hold("acme")
		assert.True(t, h.held("acme"))
		release()
		assert.False(t, h.held("acme"))
	})
	t.Run("concurrent holds and releases settle", func(t *testing.T) {
		var h undoAckHolds
		var wg sync.WaitGroup
		for range 64 {
			wg.Go(func() {
				for range 100 {
					release := h.hold("acme")
					if !h.held("acme") {
						t.Error("an id was not held while a hold on it was")
					}
					release()
				}
			})
		}
		wg.Wait()
		assert.False(t, h.held("acme"))
	})
}
