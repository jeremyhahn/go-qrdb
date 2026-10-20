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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// undoneService is a barrier-backed service with the tenant-undo feature
// active, acknowledging as node.
func undoneService(t *testing.T, store seal.StorageBackend, node string) *TenantService {
	t.Helper()
	svc, _ := newTestServiceWithStore(t, store)
	WithTenantUndoGate(undoActive)(svc)
	WithNodeID(node)(svc)
	return svc
}

// undoneKVService is the no-seal service with the tenant-undo feature active,
// acknowledging as node against roster.
func undoneKVService(t *testing.T, store kvstore.KVStore, node string, roster UndoCleanupRoster) TenantServicer {
	t.Helper()
	svc, err := NewKVTenantService(store, testLogger())
	require.NoError(t, err)
	svc.(interface{ SetTenantUndoGate(func() error) }).SetTenantUndoGate(undoActive)
	svc.(interface {
		SetUndoCleanupRoster(string, UndoCleanupRoster)
	}).SetUndoCleanupRoster(node, roster)
	return svc
}

// mustReadAck returns node's acknowledgement of id's current undo.
func mustReadAck(t *testing.T, read tombstoneReader, id, node string) *undoneAck {
	t.Helper()
	ts := mustReadTombstone(t, read, id)
	data, err := read(context.Background(), undoneAckKey(id, ts.Undo, node))
	require.NoError(t, err)
	require.NotNil(t, data, "no acknowledgement by %q of %q", node, id)
	var ack undoneAck
	require.NoError(t, json.Unmarshal(data, &ack))
	return &ack
}

// An undo acknowledges this node's cleanup under its node identity, scoped to
// the undo's nonce.
func TestUndoCreateTenant_AcknowledgesThisNodesCleanup(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))

	ack := mustReadAck(t, store.Get, "acme", "node-a")
	assert.Equal(t, "node-a", ack.Node)
	assert.False(t, ack.AckedAt.IsZero())
}

// A reconcile pass writes back an acknowledgement that is missing, and only
// for an undone id: a retired one is never acknowledged.
func TestReconcileDeletedTenants_AcknowledgesUndoneIDsOnly(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	ctx := context.Background()

	for _, id := range []string{"acme", "gone"} {
		_, err := svc.CreateTenant(ctx, id, id, IsolationPrefix)
		require.NoError(t, err)
	}
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))
	require.NoError(t, svc.DeleteTenant(ctx, "gone"))
	nonce := mustReadTombstone(t, store.Get, "acme").Undo
	require.NoError(t, store.Delete(ctx, undoneAckKey("acme", nonce, "node-a")))

	_, err := svc.ReconcileDeletedTenants(ctx)
	require.NoError(t, err)

	mustReadAck(t, store.Get, "acme", "node-a")
	keys, err := store.List(ctx, undoneAckPrefix)
	require.NoError(t, err)
	assert.Equal(t, []string{undoneAckKey("acme", nonce, "node-a")}, keys)
}

// Without a node identity an acknowledgement could never be counted, so the
// reconcile reports it instead of writing one.
func TestReconcileDeletedTenants_UnnamedNodeReportsTheAck(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "")
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"), "the undo itself committed")

	report, err := svc.ReconcileDeletedTenants(ctx)
	var failures *ResidueReconcileFailuresError
	require.True(t, errors.As(err, &failures), "got %v", err)
	require.Len(t, report.Failed, 1)
	assert.Equal(t, "undo-ack", report.Failed[0].Op)
	assert.ErrorIs(t, failures.Errors[0].Err, ErrUndoNodeUnnamed)
	keys, err := store.List(ctx, undoneAckPrefix)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// The no-seal service acknowledges at the undo and on every reconcile pass.
func TestKVTenantService_AcknowledgesUndoneIDs(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", nil)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.(CreateUndoer).UndoCreateTenant(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-a")

	nonce := mustReadTombstone(t, store.Get, "acme").Undo
	require.NoError(t, store.Delete(ctx, undoneAckKey("acme", nonce, "node-a")))
	reconciler := svc.(interface {
		ReconcileDeletedTenants(context.Context) (*ReconcileReport, error)
	})
	report, err := reconciler.ReconcileDeletedTenants(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Inspected)
	mustReadAck(t, store.Get, "acme", "node-a")
}

// The no-seal service without a node identity reports the acknowledgement.
func TestKVTenantService_UnnamedNodeReportsTheAck(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "", nil)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.(CreateUndoer).UndoCreateTenant(ctx, "acme"))

	report, err := svc.(interface {
		ReconcileDeletedTenants(context.Context) (*ReconcileReport, error)
	}).ReconcileDeletedTenants(ctx)
	assert.ErrorIs(t, err, ErrUndoNodeUnnamed)
	require.Len(t, report.Failed, 1)
	assert.Equal(t, "acme", report.Failed[0].TenantID)
}

// ReconcileUndone acknowledges one undone id on demand, which is what another
// serving node runs when it learns of the undo.
func TestReconcileUndone_AcknowledgesTheUndoneID(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))
	nonce := mustReadTombstone(t, store.Get, "acme").Undo
	require.NoError(t, store.Delete(ctx, undoneAckKey("acme", nonce, "node-a")))

	require.NoError(t, svc.ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-a")
}

// A retired id, or one with no tombstone at all, is not acknowledged.
func TestReconcileUndone_LeavesRetiredAndUnknownIDsAlone(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc := undoneService(t, store, "node-a")
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "gone", "Gone", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteTenant(ctx, "gone"))

	require.NoError(t, svc.ReconcileUndone(ctx, "gone"))
	require.NoError(t, svc.ReconcileUndone(ctx, "never-existed"))
	keys, err := store.List(ctx, undoneAckPrefix)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestKVTenantService_ReconcileUndone(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := undoneKVService(t, store, "node-a", nil)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.(CreateUndoer).UndoCreateTenant(ctx, "acme"))
	nonce := mustReadTombstone(t, store.Get, "acme").Undo
	require.NoError(t, store.Delete(ctx, undoneAckKey("acme", nonce, "node-a")))

	reconciler, ok := svc.(UndoReconciler)
	require.True(t, ok, "%T does not reconcile one undone id", svc)
	require.NoError(t, reconciler.ReconcileUndone(ctx, "acme"))
	mustReadAck(t, store.Get, "acme", "node-a")
	require.NoError(t, reconciler.ReconcileUndone(ctx, "never-existed"))
}
