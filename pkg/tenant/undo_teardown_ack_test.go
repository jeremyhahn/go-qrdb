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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// hookReclaimer is a KeyspaceReclaimer that runs during while the keyspace
// reclamation is in progress, then fails with err.
type hookReclaimer struct {
	during func()
	err    error
}

func (r *hookReclaimer) ReclaimTenantKeyspace(context.Context, string) error {
	if r.during != nil {
		r.during()
	}
	return r.err
}

// tombstoneEventStore delivers every tombstone write to onTombstone the
// moment it commits, before the writer's next step: the order a watching
// node sees it in.
type tombstoneEventStore struct {
	*seal.MemoryBackend
	onTombstone func(id string)
}

func (s *tombstoneEventStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.MemoryBackend.Put(ctx, key, data); err != nil {
		return err
	}
	s.deliver(key)
	return nil
}

func (s *tombstoneEventStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	swapped, err := s.MemoryBackend.CompareAndSwap(ctx, key, expected, next)
	if swapped {
		s.deliver(key)
	}
	return swapped, err
}

func (s *tombstoneEventStore) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	written, err := s.MemoryBackend.PutIfAbsent(ctx, key, data)
	if written {
		s.deliver(key)
	}
	return written, err
}

func (s *tombstoneEventStore) deliver(key string) {
	if id, ok := strings.CutPrefix(key, TombstonePrefix); ok && s.onTombstone != nil {
		s.onTombstone(id)
	}
}

// assertNoAcks fails when any node has acknowledged any undone id.
func assertNoAcks(t *testing.T, list func(context.Context, string) ([]string, error)) {
	t.Helper()
	keys, err := list(context.Background(), undoneAckPrefix)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// A failed keyspace reclamation leaves the shared keyspace holding the undone
// tenant's records, so neither the undoing node nor any other acknowledges
// until a re-issued teardown completes it.
func TestUndoCreateTenant_FailedReclaimIsNeverAcknowledged(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	other := undoneService(t, store, "node-b")
	reclaim := &hookReclaimer{err: errStoreFault}
	svc.SetKeyspaceReclaimer(reclaim)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	var ke *KeyspaceReclaimError
	require.True(t, errors.As(svc.UndoCreateTenant(ctx, "acme"), &ke))
	assert.False(t, mustReadTombstone(t, store.Get, "acme").TornDown)

	require.NoError(t, other.ReconcileUndone(ctx, "acme"))
	require.NoError(t, svc.ReconcileUndone(ctx, "acme"))
	_, err = svc.ReconcileDeletedTenants(ctx)
	require.NoError(t, err)
	assertNoAcks(t, store.List)

	reclaim.err = nil
	require.NoError(t, svc.DeleteTenant(ctx, "acme"), "a re-issued teardown resumes the reclamation")
	assert.True(t, mustReadTombstone(t, store.Get, "acme").TornDown)
	require.NoError(t, other.ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-b")
}

// A reconcile that runs while the teardown is still reclaiming the keyspace
// does not acknowledge.
func TestUndoCreateTenant_NoAckWhileTeardownRuns(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	other := undoneService(t, store, "node-b")
	ctx := context.Background()
	reclaim := &hookReclaimer{during: func() {
		require.NoError(t, other.ReconcileUndone(ctx, "acme"))
		assertNoAcks(t, store.List)
	}}
	svc.SetKeyspaceReclaimer(reclaim)

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-a")
	require.NoError(t, other.ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-b")
}

// The no-seal service holds the ack back the same way.
func TestKVTenantService_FailedReclaimIsNeverAcknowledged(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", nil)
	other := undoneKVService(t, store, "node-b", nil)
	reclaim := &hookReclaimer{err: errStoreFault}
	svc.(TeardownWiring).SetKeyspaceReclaimer(reclaim)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	var ke *KeyspaceReclaimError
	require.True(t, errors.As(svc.(CreateUndoer).UndoCreateTenant(ctx, "acme"), &ke))
	require.NoError(t, other.(UndoReconciler).ReconcileUndone(ctx, "acme"))
	assertNoAcks(t, store.List)

	reclaim.err = nil
	require.NoError(t, svc.DeleteTenant(ctx, "acme"))
	require.NoError(t, other.(UndoReconciler).ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-b")
}

// A watching node that sees the undone tombstone before the tenant's metadata
// delete commits skips the id; the teardown's completion is a second
// tombstone write, after that delete, and the node acknowledges on it.
func TestUndoCreateTenant_TombstoneBeforeMetadataDeleteEndsAcked(t *testing.T) {
	store := &tombstoneEventStore{MemoryBackend: seal.NewMemoryBackend()}
	svc := undoneService(t, store, "node-a")
	watcher := undoneService(t, store.MemoryBackend, "node-b")
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)

	var sawLiveRecord []bool
	store.onTombstone = func(id string) {
		_, getErr := store.MemoryBackend.Get(ctx, metadataPrefix+id)
		sawLiveRecord = append(sawLiveRecord, getErr == nil)
		require.NoError(t, watcher.ReconcileUndone(ctx, id))
	}
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))

	require.Equal(t, []bool{true, false}, sawLiveRecord,
		"the first tombstone event precedes the metadata delete; the completion follows it")
	mustReadAck(t, store.Get, "acme", "node-b")
}

// plainUndoStore has no conditional rewrite.
type plainUndoStore struct{ m *seal.MemoryBackend }

func (s plainUndoStore) Get(ctx context.Context, k string) ([]byte, error) { return s.m.Get(ctx, k) }
func (s plainUndoStore) Put(ctx context.Context, k string, v []byte) error { return s.m.Put(ctx, k, v) }
func (s plainUndoStore) Delete(ctx context.Context, k string) error        { return s.m.Delete(ctx, k) }
func (s plainUndoStore) List(ctx context.Context, p string) ([]string, error) {
	return s.m.List(ctx, p)
}

// faultyUndoStore fails its reads or its swaps.
type faultyUndoStore struct {
	*seal.MemoryBackend
	getErr, casErr error
}

func (s *faultyUndoStore) Get(ctx context.Context, k string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.MemoryBackend.Get(ctx, k)
}

func (s *faultyUndoStore) CompareAndSwap(ctx context.Context, k string, old, next []byte) (bool, error) {
	if s.casErr != nil {
		return false, s.casErr
	}
	return s.MemoryBackend.CompareAndSwap(ctx, k, old, next)
}

func TestMarkTornDown(t *testing.T) {
	ctx := context.Background()
	undone := []byte(`{"id":"acme","state":"undone","undo":"n1"}`)
	put := func(t *testing.T, m *seal.MemoryBackend, data []byte) {
		t.Helper()
		require.NoError(t, m.Put(ctx, tombstoneKey("acme"), data))
	}

	t.Run("marks an undone tombstone once", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone)
		require.NoError(t, markTornDown(ctx, m, "acme"))
		ts := mustReadTombstone(t, m.Get, "acme")
		assert.True(t, ts.TornDown)
		assert.Equal(t, "n1", ts.Undo, "the undo nonce the acknowledgements are scoped to is kept")
		require.NoError(t, markTornDown(ctx, m, "acme"))
	})
	t.Run("leaves absent, retired and unreadable tombstones alone", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"retired":    []byte(`{"id":"acme"}`),
			"unreadable": []byte(`{`),
		} {
			m := seal.NewMemoryBackend()
			put(t, m, data)
			require.NoError(t, markTornDown(ctx, plainUndoStore{m}, "acme"), name)
			got, err := m.Get(ctx, tombstoneKey("acme"))
			require.NoError(t, err)
			assert.Equal(t, data, got, name)
		}
		require.NoError(t, markTornDown(ctx, seal.NewMemoryBackend(), "acme"))
	})
	t.Run("a store without a conditional rewrite is refused", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone)
		assert.ErrorIs(t, markTornDown(ctx, plainUndoStore{m}, "acme"), ErrTenantStoreNotAtomic)
	})
	t.Run("store faults are typed", func(t *testing.T) {
		for name, s := range map[string]*faultyUndoStore{
			"get":  {getErr: errStoreFault},
			"swap": {casErr: errStoreFault},
		} {
			s.MemoryBackend = seal.NewMemoryBackend()
			put(t, s.MemoryBackend, undone)
			var se *StoreError
			err := markTornDown(ctx, s, "acme")
			require.True(t, errors.As(err, &se), "%s: got %v", name, err)
			assert.ErrorIs(t, err, errStoreFault, name)
		}
	})
	t.Run("a tombstone rewritten since it was read is left to its writer", func(t *testing.T) {
		m := seal.NewMemoryBackend()
		put(t, m, undone)
		race := &tombstoneEventStore{MemoryBackend: m}
		rewritten := []byte(`{"id":"acme","state":"undone","undo":"n2"}`)
		// The swap compares against what was read, which a later undo has
		// already replaced by the time it lands.
		s := &rewriteBeforeSwap{tombstoneEventStore: race, next: rewritten}
		require.NoError(t, markTornDown(ctx, s, "acme"))
		got, err := m.Get(ctx, tombstoneKey("acme"))
		require.NoError(t, err)
		assert.Equal(t, rewritten, got)
	})
}

// rewriteBeforeSwap replaces the tombstone just before the swap it forwards.
type rewriteBeforeSwap struct {
	*tombstoneEventStore
	next []byte
}

func (s *rewriteBeforeSwap) CompareAndSwap(ctx context.Context, k string, old, next []byte) (bool, error) {
	if err := s.MemoryBackend.Put(ctx, k, s.next); err != nil {
		return false, err
	}
	return s.tombstoneEventStore.CompareAndSwap(ctx, k, old, next)
}
