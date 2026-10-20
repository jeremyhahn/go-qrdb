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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// missedUndoTeardown puts reg's node in the state a node that was offline
// for info's undo restarts in: the undone tombstone torn down, the metadata
// gone, the catalog Assignment released by the undoing node's teardown, and
// no barrier in memory -- only this node's own directory is left.
func missedUndoTeardown(t *testing.T, store *seal.MemoryBackend, reg *seal.BarrierRegistry, info *TenantInfo) {
	t.Helper()
	ctx := context.Background()
	ts := newUndoneTombstone(info, time.Now().UTC())
	ts.TornDown = true
	data, err := json.Marshal(ts)
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, tombstoneKey(info.ID), data))
	require.NoError(t, store.Delete(ctx, metadataPrefix+info.ID))
	require.NoError(t, volumeAllocatorFor(t, reg).Release(ctx, info.ID))
	require.NoError(t, reg.ReleaseTenant(ctx, info.ID))
}

// TestReconcileUndone_ReclaimsADirectoryWhoseAssignmentWasReleased proves a
// node that missed an undo's teardown removes its own directory BEFORE it
// acknowledges, although the teardown elsewhere already released the catalog
// Assignment that named it: the tombstone's pool still does.
func TestReconcileUndone_ReclaimsADirectoryWhoseAssignmentWasReleased(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	WithTenantUndoGate(undoActive)(svc)
	WithNodeID("node-b")(svc)

	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vund")
	require.NoError(t, err)
	require.True(t, dirExists(t, dataRoot), "precondition: DataRoot not created")
	missedUndoTeardown(t, store, reg, info)

	require.NoError(t, svc.ReconcileUndone(ctx, "vund"))
	require.False(t, dirExists(t, dataRoot), "acknowledged the undo with this node's directory still present")
	mustReadAck(t, store.Get, "vund", "node-b")
}

// TestReconcileUndone_ReclaimsABarrierTenantsReleasedDirectory proves the
// same for a barrier-isolated tenant, whose pooled Pebble the registry placed
// without a caller-named pool: the tenant record, and so the tombstone, carry
// the pool it chose, and a node that restarts after the catalog release
// removes its own directory before it acknowledges.
func TestReconcileUndone_ReclaimsABarrierTenantsReleasedDirectory(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	WithTenantUndoGate(undoActive)(svc)
	WithNodeID("node-b")(svc)

	info, err := svc.CreateTenant(ctx, "bund", "Undone", IsolationBarrier)
	require.NoError(t, err)
	assignment, err := volumeAllocatorFor(t, reg).Resolve(ctx, "bund")
	require.NoError(t, err)
	require.Equal(t, assignment.Pool, info.VolumePool, "the tenant record does not name the pool its barrier was placed on")
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "bund")
	require.NoError(t, err)
	require.True(t, dirExists(t, dataRoot), "precondition: barrier directory not created")
	missedUndoTeardown(t, store, reg, info)
	require.Equal(t, info.VolumePool, mustReadTombstone(t, store.Get, "bund").VolumePool)

	require.NoError(t, svc.ReconcileUndone(ctx, "bund"))
	require.False(t, dirExists(t, dataRoot), "acknowledged the undo with this node's barrier directory still present")
	mustReadAck(t, store.Get, "bund", "node-b")
}

// TestRegisterDedicatedBarrier_PlacementUnresolvable proves a barrier whose
// placement cannot be read back is unregistered and refused: a tenant record
// without its pool would leave its directory unfindable after an undo.
func TestRegisterDedicatedBarrier_PlacementUnresolvable(t *testing.T) {
	ctx := context.Background()
	svc, reg, _ := newReconcileService(t)
	_, err := svc.registerDedicatedBarrier(ctx, "bund", false)
	require.NoError(t, err)
	require.NoError(t, volumeAllocatorFor(t, reg).Release(ctx, "bund"))
	require.NoError(t, reg.UnregisterTenant("bund"))

	pool, err := svc.barrierPlacementPool(ctx, "bund")
	require.Error(t, err)
	require.Empty(t, pool)

	unpooled, err := seal.NewBarrierRegistry(testLogger(), sealtest.UnsealedBarrier(t))
	require.NoError(t, err)
	bare, err := NewTenantService(seal.NewMemoryBackend(), unpooled, testLogger())
	require.NoError(t, err)
	_, err = bare.barrierPlacementPool(ctx, "bund")
	require.ErrorIs(t, err, seal.ErrPoolNotConfigured)
}

// TestReconcileDeletedTenants_ReclaimsByTheTombstonesPool proves the periodic
// pass reclaims the same directory, and that a second pass finds nothing.
func TestReconcileDeletedTenants_ReclaimsByTheTombstonesPool(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	WithTenantUndoGate(undoActive)(svc)
	WithNodeID("node-b")(svc)

	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vund")
	require.NoError(t, err)
	missedUndoTeardown(t, store, reg, info)

	report, err := svc.ReconcileDeletedTenants(ctx)
	require.NoError(t, err)
	require.Len(t, report.Reclaimed, 1)
	require.False(t, dirExists(t, dataRoot))
	mustReadAck(t, store.Get, "vund", "node-b")

	again, err := svc.ReconcileDeletedTenants(ctx)
	require.NoError(t, err)
	require.Empty(t, again.Reclaimed)
}

// TestDataRootCandidates_UnknownPoolFailsClosed proves a tombstone naming a
// pool this node cannot resolve is a failure, never an acknowledgement.
func TestDataRootCandidates_UnknownPoolFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	WithTenantUndoGate(undoActive)(svc)
	WithNodeID("node-b")(svc)

	info, err := svc.CreateTenant(ctx, "vund", "Undone", IsolationVolume)
	require.NoError(t, err)
	missedUndoTeardown(t, store, reg, info)
	info.VolumePool = "no-such-pool"
	ts := newUndoneTombstone(info, time.Now().UTC())
	ts.TornDown = true
	data, err := json.Marshal(ts)
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, tombstoneKey("vund"), data))

	require.Error(t, svc.ReconcileUndone(ctx, "vund"))
	_, err = store.Get(ctx, undoneAckKey("vund", ts.Undo, "node-b"))
	require.True(t, isAbsent(err), "acknowledged an undo whose directory it could not locate: %v", err)
}
