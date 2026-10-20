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

// requireUndoPending asserts that err is *TenantUndoPendingError for id.
func requireUndoPending(t *testing.T, err error, id string) {
	t.Helper()
	var pending *TenantUndoPendingError
	require.True(t, errors.As(err, &pending), "got %v", err)
	assert.Equal(t, id, pending.TenantID)
}

// overlapOnce runs overlap inside the first reclamation only: the slower of
// two teardowns is still reclaiming while overlap runs.
func overlapOnce(overlap func()) *hookReclaimer {
	var ran bool
	r := &hookReclaimer{}
	r.during = func() {
		if ran {
			return
		}
		ran = true
		overlap()
	}
	return r
}

// An undo whose teardown is still reclaiming when a retried DeleteTenant
// completes the same teardown and marks the id torn down: this node does not
// acknowledge, and the id is not created again, until the slower teardown is
// over. Without the teardown's own hold the retry's TornDown let the
// acknowledgement through, and the reuse with it.
func TestUndoCreateTenant_OverlappingTeardownFencesTheAcknowledgement(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	WithUndoCleanupRoster(rosterOf("node-a"))(svc)
	ctx := context.Background()
	svc.SetKeyspaceReclaimer(overlapOnce(func() {
		require.NoError(t, svc.DeleteTenant(ctx, "acme"))
		require.True(t, mustReadTombstone(t, store.Get, "acme").TornDown)

		requireUndoPending(t, svc.ReconcileUndone(ctx, "acme"), "acme")
		assertNoAcks(t, store.List)
		_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		requireUndoPending(t, err, "acme")
	}))

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))

	mustReadAck(t, store.Get, "acme", "node-a")
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

// A retried teardown of an id this node already acknowledged voids that
// acknowledgement before it destroys anything: otherwise the last node to
// acknowledge releases the id while the retry is still reclaiming, and the
// next tenant's records go with it.
func TestDeleteTenant_RetryVoidsAnEarlierAcknowledgement(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	other := undoneService(t, store, "node-b")
	for _, s := range []*TenantService{svc, other} {
		WithUndoCleanupRoster(rosterOf("node-a", "node-b"))(s)
	}
	ctx := context.Background()
	createUndone(t, svc, "acme")
	mustReadAck(t, store.Get, "acme", "node-a")

	svc.SetKeyspaceReclaimer(overlapOnce(func() {
		require.NoError(t, other.ReconcileUndone(ctx, "acme"))
		mustReadAck(t, store.Get, "acme", "node-b")
		_, err := other.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		var pending *TenantUndoPendingError
		require.True(t, errors.As(err, &pending), "got %v", err)
		assert.Equal(t, []string{"node-a"}, pending.Pending)
	}))
	require.NoError(t, svc.DeleteTenant(ctx, "acme"))

	require.NoError(t, svc.ReconcileUndone(ctx, "acme"))
	_, err := other.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

// The no-seal service holds the same fence over its teardowns.
func TestKVTenantService_OverlappingTeardownFencesTheAcknowledgement(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", rosterOf("node-a"))
	ctx := context.Background()
	svc.(TeardownWiring).SetKeyspaceReclaimer(overlapOnce(func() {
		require.NoError(t, svc.DeleteTenant(ctx, "acme"))
		require.True(t, mustReadTombstone(t, store.Get, "acme").TornDown)

		requireUndoPending(t, svc.(UndoReconciler).ReconcileUndone(ctx, "acme"), "acme")
		assertNoAcks(t, store.List)
		_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		requireUndoPending(t, err, "acme")
	}))

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.(CreateUndoer).UndoCreateTenant(ctx, "acme"))

	mustReadAck(t, store.Get, "acme", "node-a")
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

// A KV-only retry voids an earlier acknowledgement as TenantService's does.
func TestKVTenantService_RetryVoidsAnEarlierAcknowledgement(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", rosterOf("node-a", "node-b"))
	other := undoneKVService(t, store, "node-b", rosterOf("node-a", "node-b"))
	ctx := context.Background()
	createUndone(t, svc, "acme")
	mustReadAck(t, store.Get, "acme", "node-a")

	svc.(TeardownWiring).SetKeyspaceReclaimer(overlapOnce(func() {
		require.NoError(t, other.(UndoReconciler).ReconcileUndone(ctx, "acme"))
		_, err := other.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		requireUndoPending(t, err, "acme")
	}))
	require.NoError(t, svc.DeleteTenant(ctx, "acme"))

	require.NoError(t, svc.(UndoReconciler).ReconcileUndone(ctx, "acme"))
	_, err := other.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
}

func TestGuardTeardown(t *testing.T) {
	ctx := context.Background()
	t.Run("a released id is not destroyed", func(t *testing.T) {
		destroy, err := guardTeardown(ctx, seal.NewMemoryBackend(), testLogger(), "acme")
		require.NoError(t, err)
		assert.False(t, destroy)
	})
	t.Run("a retired id is destroyed", func(t *testing.T) {
		store := seal.NewMemoryBackend()
		svc := undoneService(t, store, "node-a")
		_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
		require.NoError(t, err)
		require.NoError(t, svc.fence(ctx, "acme", newTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC())))
		destroy, err := guardTeardown(ctx, store, testLogger(), "acme")
		require.NoError(t, err)
		assert.True(t, destroy)
	})
	t.Run("an unreadable tombstone fails as a teardown", func(t *testing.T) {
		store := &failingGetStore{MemoryBackend: seal.NewMemoryBackend(), key: tombstoneKey("acme")}
		destroy, err := guardTeardown(ctx, store, testLogger(), "acme")
		assert.False(t, destroy)
		var te *TeardownError
		require.True(t, errors.As(err, &te), "got %v", err)
		assert.Equal(t, "acme", te.TenantID)
	})
}

// A teardown on a released id destroys nothing: what is keyed by the id may
// already be the next tenant's.
func TestFinishTeardown_ReleasedIDDestroysNothing(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	reclaim := &hookReclaimer{during: func() { t.Fatal("a released id was reclaimed") }}
	svc.SetKeyspaceReclaimer(reclaim)
	require.NoError(t, svc.finishTeardown(context.Background(), "acme"))

	kv := undoneKVService(t, kvstore.NewMemoryKVStore(), "node-a", nil).(*kvTenantService)
	kv.SetKeyspaceReclaimer(reclaim)
	require.NoError(t, kv.finishTeardown(context.Background(), "acme"))
}
