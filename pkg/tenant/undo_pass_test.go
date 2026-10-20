package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// undoneVolumeResidue creates a volume tenant on svc and leaves it as an
// undo another node ran would: the metadata gone, an undone torn-down
// tombstone acknowledged by acked, and this node's registration and
// placement still in place.
func undoneVolumeResidue(t *testing.T, svc *TenantService, store *seal.MemoryBackend, id string, acked ...string) *Tombstone {
	t.Helper()
	ctx := context.Background()
	info, err := svc.CreateTenant(ctx, id, "Undone", IsolationVolume)
	require.NoError(t, err)
	require.NoError(t, store.Delete(ctx, metadataPrefix+id))
	return putUndone(t, store, info, acked...)
}

// Of two overlapping cleanup passes, the one that finishes first does not
// acknowledge while the other may still destroy state under the id: the undo
// stays unreleased until the slower pass is over, so no later create's
// barrier or placement can be destroyed by it.
func TestReconcileUndone_OverlappingPassesAcknowledgeOnlyOnceBothEnd(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)
	ts := undoneVolumeResidue(t, svc, store, "vund", "node-a")

	entered, proceed := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	svc.SetTenantRuntimeCloser(closerFunc(func(context.Context, string) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return nil
	}))
	slow := make(chan error, 1)
	go func() { slow <- svc.ReconcileUndone(ctx, "vund") }()
	<-entered

	var pending *TenantUndoPendingError
	err := svc.ReconcileUndone(ctx, "vund")
	require.True(t, errors.As(err, &pending), "the overlapping pass acknowledged while the other was in flight: %v", err)
	_, err = store.Get(ctx, undoneAckKey("vund", ts.Undo, "node-b"))
	assert.True(t, isAbsent(err), "acknowledged with a pass in flight: %v", err)
	released, err := releaseUndone(ctx, store, rosterOf("node-a", "node-b"), "vund")
	assert.False(t, released, "the undo was released with a pass in flight")
	require.True(t, errors.As(err, &pending), "got %v", err)

	close(proceed)
	require.NoError(t, <-slow)
	mustReadAck(t, store.Get, "vund", "node-b")

	// Released only now, the id's next tenant keeps its barrier.
	_, err = svc.CreateTenant(ctx, "vund", "Next", IsolationVolume)
	require.NoError(t, err)
	_, err = reg.GetTenant("vund")
	require.NoError(t, err, "the next tenant's barrier was destroyed")
}

// A pass that begins once this node acknowledged the undo does nothing: a
// later create may already be provisioning under the id, and what the pass
// would destroy is that create's.
func TestReconcileUndone_APassAfterTheAcknowledgementDoesNotDestroy(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)
	undoneVolumeResidue(t, svc, store, "vund", "node-a", "node-b")
	var calls atomic.Int32
	svc.SetTenantRuntimeCloser(closerFunc(func(context.Context, string) error {
		calls.Add(1)
		return nil
	}))

	require.NoError(t, svc.ReconcileUndone(ctx, "vund"))
	assert.Zero(t, calls.Load(), "the pass closed the id's runtime")
	_, err := reg.GetTenant("vund")
	require.NoError(t, err, "the pass destroyed a barrier registered after the acknowledgement")
	_, err = volumeAllocatorFor(t, reg).Resolve(ctx, "vund")
	require.NoError(t, err, "the pass dropped a placement made after the acknowledgement")
}

// TestReconcileUndone_ASkippedDirectoryWithholdsTheAcknowledgement pins that
// a directory a live tenant claims while the pass runs is neither removed nor
// forgotten: the id's records stay and this node does not acknowledge, so the
// id cannot be reused over the directory. Once the claim is gone, the next
// pass removes the directory and acknowledges.
func TestReconcileUndone_ASkippedDirectoryWithholdsTheAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, reg := newRollbackService(t, store)
	ts := undoneVolumeResidue(t, svc, store, "vund", "node-a")
	contested, err := reg.LocalTenantDataRoot(ctx, "vund")
	require.NoError(t, err)

	allocator := volumeAllocatorFor(t, reg)
	var calls atomic.Int32
	svc.SetTenantRuntimeCloser(closerFunc(func(context.Context, string) error {
		if calls.Add(1) > 1 {
			return nil
		}
		// A peer's tenant claims the same directory mid-pass.
		if _, err := allocator.Reserve(ctx, "vpeer", "default", "vund", 0); err != nil {
			return err
		}
		rec, _ := json.Marshal(&TenantInfo{ID: "vpeer", Name: "Peer", IsolationType: IsolationVolume, VolumePool: "default"})
		return store.Put(ctx, metadataPrefix+"vpeer", rec)
	}))

	var pending *TenantUndoPendingError
	err = svc.ReconcileUndone(ctx, "vund")
	require.True(t, errors.As(err, &pending), "got %v; want the skip reported as pending", err)
	assert.Equal(t, []string{"node-b"}, pending.Pending)
	assert.True(t, dirExists(t, contested), "a directory a live tenant claims was removed")
	_, err = store.Get(ctx, undoneAckKey("vund", ts.Undo, "node-b"))
	assert.True(t, isAbsent(err), "acknowledged with the directory still present: %v", err)
	_, err = allocator.Resolve(ctx, "vund")
	require.NoError(t, err, "the records naming a skipped directory were dropped")

	// The claim goes away; the next pass finishes the id.
	require.NoError(t, store.Delete(ctx, metadataPrefix+"vpeer"))
	require.NoError(t, svc.ReconcileUndone(ctx, "vund"))
	assert.False(t, dirExists(t, contested), "the released directory was left behind")
	mustReadAck(t, store.Get, "vund", "node-b")
}
