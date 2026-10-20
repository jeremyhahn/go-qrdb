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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// rosterOf is a fixed serving-node roster.
func rosterOf(nodes ...string) UndoCleanupRoster {
	return func(context.Context) ([]string, error) { return nodes, nil }
}

// createUndone creates id on svc and undoes it.
func createUndone(t *testing.T, svc TenantServicer, id string) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.CreateTenant(ctx, id, id, IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.(CreateUndoer).UndoCreateTenant(ctx, id))
}

// An undone id every serving node has acknowledged is reused, and the release
// takes the tombstone and that undo's acknowledgements with it.
func TestCreateTenant_ReusesAnUndoneIDOnceEveryNodeAcked(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	WithUndoCleanupRoster(rosterOf("node-a"))(svc)
	ctx := context.Background()
	createUndone(t, svc, "acme")

	info, err := svc.CreateTenant(ctx, "acme", "Acme Again", IsolationPrefix)
	require.NoError(t, err)
	assert.Equal(t, "Acme Again", info.Name)

	_, tombstoned, err := svc.deletedAt(ctx, "acme")
	require.NoError(t, err)
	assert.False(t, tombstoned)
	keys, err := store.List(ctx, undoneAckPrefix)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// Until the last serving node acknowledges, the create answers the retryable
// refusal naming the nodes it waits on.
func TestCreateTenant_UndoneIDWaitsOnEveryNode(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	WithUndoCleanupRoster(rosterOf("node-a", "node-b", "node-c"))(svc)
	ctx := context.Background()
	createUndone(t, svc, "acme")

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	assert.Equal(t, "acme", pending.TenantID)
	assert.Equal(t, []string{"node-b", "node-c"}, pending.Pending)
	assert.Contains(t, pending.Error(), "node-b, node-c")
	assert.Equal(t, UndoPendingRetryAfter, pending.RetryAfter)

	require.NoError(t, ackUndone(ctx, store, "acme", "node-b"))
	require.NoError(t, ackUndone(ctx, store, "acme", "node-c"))
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

// An acknowledgement of an earlier undo of the same id does not count toward
// a later one.
func TestCreateTenant_AckOfAnotherUndoDoesNotCount(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	WithUndoCleanupRoster(rosterOf("node-a", "node-b"))(svc)
	ctx := context.Background()
	createUndone(t, svc, "acme")
	require.NoError(t, store.Put(ctx, undoneAckKey("acme", "earlier-undo", "node-b"), []byte("{}")))

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	assert.Equal(t, []string{"node-b"}, pending.Pending)
}

// A retired id stays retired with the feature active, and an undone id with
// no roster to count against is never released.
func TestCreateTenant_UndoReleaseRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("retired", func(t *testing.T) {
		svc := undoneService(t, seal.NewMemoryBackend(), "node-a")
		WithUndoCleanupRoster(rosterOf("node-a"))(svc)
		_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		require.NoError(t, err)
		require.NoError(t, svc.DeleteTenant(ctx, "acme"))
		_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		var deleted *TenantDeletedError
		assert.True(t, errors.As(err, &deleted), "got %v", err)
	})
	for name, roster := range map[string]UndoCleanupRoster{"no roster": nil, "empty roster": rosterOf()} {
		t.Run(name, func(t *testing.T) {
			svc := undoneService(t, seal.NewMemoryBackend(), "node-a")
			WithUndoCleanupRoster(roster)(svc)
			createUndone(t, svc, "acme")
			_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
			var pending *TenantUndoPendingError
			require.True(t, errors.As(err, &pending), "got %v", err)
			assert.Empty(t, pending.Pending)
		})
	}
}

// A roster that cannot be read fails the create; it never releases the id.
func TestCreateTenant_UndoRosterErrorFails(t *testing.T) {
	svc := undoneService(t, seal.NewMemoryBackend(), "node-a")
	boom := errors.New("membership unavailable")
	WithUndoCleanupRoster(func(context.Context) ([]string, error) { return nil, boom })(svc)
	createUndone(t, svc, "acme")
	_, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix)
	var se *StoreError
	require.True(t, errors.As(err, &se), "got %v", err)
	assert.Equal(t, "undo-roster", se.Op)
	assert.ErrorIs(t, err, boom)
}

// The no-seal service releases an undone id the same way.
func TestKVTenantService_ReusesAnUndoneID(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", rosterOf("node-a"))
	createUndone(t, svc, "acme")
	_, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

// deleteFirstBackend runs a concurrent deletion inside the undo's read of the
// tenant record: the deletion's whole fence lands after the undo has read the
// record and before the undo writes its own tombstone.
type deleteFirstBackend struct {
	*seal.MemoryBackend
	key    string
	before func()
}

func (b *deleteFirstBackend) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := b.MemoryBackend.Get(ctx, key)
	if key == b.key && b.before != nil {
		run := b.before
		b.before = nil
		run()
	}
	return data, err
}

// deleteFirstKVStore is deleteFirstBackend for the no-seal service's store.
type deleteFirstKVStore struct {
	*kvstore.MemoryKVStore
	key    string
	before func()
}

func (s *deleteFirstKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.MemoryKVStore.Get(ctx, key)
	if key == s.key && s.before != nil {
		run := s.before
		s.before = nil
		run()
	}
	return data, err
}

// requireRetirementHeld asserts the undo lost to the deletion and left its
// permanent tombstone in place.
func requireRetirementHeld(t *testing.T, err error, read tombstoneReader, id string) {
	t.Helper()
	var deleted *TenantDeletedError
	require.ErrorAs(t, err, &deleted)
	assert.Equal(t, id, deleted.TenantID)
	ts := mustReadTombstone(t, read, id)
	assert.False(t, ts.Undone(), "the undo downgraded a permanent retirement")
	assert.False(t, ts.DeletedAt.IsZero())
}

func TestTenantService_UndoRacingADeletionKeepsTheRetirement(t *testing.T) {
	ctx := context.Background()
	store := &deleteFirstBackend{MemoryBackend: seal.NewMemoryBackend(), key: metadataPrefix + "acme"}
	svc := undoneService(t, store, "node-a")
	WithUndoCleanupRoster(rosterOf("node-a"))(svc)
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)

	store.before = func() { require.NoError(t, svc.DeleteTenant(ctx, "acme")) }
	err = svc.UndoCreateTenant(ctx, "acme")

	requireRetirementHeld(t, err, store.Get, "acme")
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	assert.ErrorAs(t, err, new(*TenantDeletedError), "a retired id was reused")
}

func TestKVTenantService_UndoRacingADeletionKeepsTheRetirement(t *testing.T) {
	ctx := context.Background()
	store := &deleteFirstKVStore{MemoryKVStore: kvstore.NewMemoryKVStore(), key: metadataPrefix + "acme"}
	svc := undoneKVService(t, store, "node-a", rosterOf("node-a"))
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)

	store.before = func() { require.NoError(t, svc.DeleteTenant(ctx, "acme")) }
	err = svc.(CreateUndoer).UndoCreateTenant(ctx, "acme")

	requireRetirementHeld(t, err, store.Get, "acme")
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	assert.ErrorAs(t, err, new(*TenantDeletedError), "a retired id was reused")
}

func TestPutTombstone_AnUndoReplacesAnEarlierUndo(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	first := newUndoneTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC())
	require.NoError(t, putTombstone(ctx, store, "acme", first))
	second := newUndoneTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC())
	require.NoError(t, putTombstone(ctx, store, "acme", second))

	held := mustReadTombstone(t, store.Get, "acme")
	assert.True(t, held.Undone())
	assert.Equal(t, second.Undo, held.Undo)
}

func TestPutTombstone_AnUndoOnAStoreWithoutConditionalWritesIsRefused(t *testing.T) {
	ctx := context.Background()
	undone := newUndoneTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC())
	err := putTombstone(ctx, plainUndoStore{seal.NewMemoryBackend()}, "acme", undone)
	assert.ErrorIs(t, err, ErrTenantStoreNotAtomic)
}

// lostReleaseStore is a store whose conditional delete always loses, as when
// the tombstone was rewritten after it was read.
type lostReleaseStore struct{ *kvstore.MemoryKVStore }

func (lostReleaseStore) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, nil
}

// releaseUndone releases only the tombstone it read: a lost conditional
// delete is a retryable refusal, and leaves the acknowledgements in place.
func TestReleaseUndone_LostCompareAndDeleteIsPending(t *testing.T) {
	inner := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, inner, "node-a", rosterOf("node-a"))
	createUndone(t, svc, "acme")

	released, err := releaseUndone(context.Background(), lostReleaseStore{inner}, rosterOf("node-a"), "acme")
	assert.False(t, released)
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	mustReadAck(t, inner.Get, "acme", "node-a")
}

// With the feature off commitCreate never releases: an undone tombstone left
// by an earlier activation still refuses as retired.
func TestCommitCreate_NoReleaseRefusesAsRetired(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	createUndone(t, svc, "acme")

	err := commitCreate(context.Background(), store, store.Get, "acme", []byte("{}"), nil)
	var deleted *TenantDeletedError
	assert.True(t, errors.As(err, &deleted), "got %v", err)
}
