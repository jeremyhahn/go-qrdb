package credentials

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// rotateWithHistory rotates name n times, each keeping the previous value
// as a history version inside its grace period.
func rotateWithHistory(t *testing.T, svc *CredentialService, name string, n int) {
	t.Helper()
	for i := range n {
		_, err := svc.RotateCredential(t.Context(), name, RotateOptions{
			NewValue:    []byte{byte('a' + i), 'v'},
			GracePeriod: 24 * time.Hour,
		})
		require.NoError(t, err)
	}
}

// TestCleanupRotation_SkipsARecreatedCredential: a retention cleanup
// scheduled for a rotation of a credential that was deleted and recreated
// since leaves the new credential's history alone.
func TestCleanupRotation_SkipsARecreatedCredential(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)

	// The first generation: rotated, its scheduled cleanup still pending.
	setFaultPolicy(t, svc, "k", 2)
	rotateWithHistory(t, svc, "k", 2)
	require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark", func(m *credentialMeta) bool {
		m.PendingCleanup = "c1"
		return true
	}))

	// Deleted and recreated, with history beyond its retention.
	require.NoError(t, svc.DeleteCredential(ctx, "k"))
	setFaultPolicy(t, svc, "k", 2)
	rotateWithHistory(t, svc, "k", 2)
	before, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	require.Len(t, before.History, 2)

	// The first generation's cleanup runs late.
	require.NoError(t, svc.cleanupRotation(ctx, "k", "c1"))

	after, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, before.History, after.History, "a recreated credential's history was trimmed")
	for _, h := range after.History {
		buf, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", h.Version)
		require.NoError(t, err, "version %d reaped", h.Version)
		buf.Destroy()
	}
}

// TestPruneBarrierHistory_BoundToItsCleanup: the trim runs only on the
// record that still names the cleanup's rotation.
func TestPruneBarrierHistory_BoundToItsCleanup(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	rotateWithHistory(t, svc, "k", 3)
	require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark", func(m *credentialMeta) bool {
		m.PendingCleanup = "c1"
		return true
	}))

	require.NoError(t, svc.pruneBarrierHistory(ctx, "k", "other"))
	meta, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	require.Len(t, meta.History, 3)

	require.NoError(t, svc.pruneBarrierHistory(ctx, "k", "c1"))
	meta, err = svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	assert.Len(t, meta.History, 1)
}

// writeHookStore runs before, once, ahead of the first delete or batch
// write that reaches the store: the point between a cleanup's read of the
// metadata record and its first change.
type writeHookStore struct {
	Store
	before func()
}

func (h *writeHookStore) fire() {
	if f := h.before; f != nil {
		h.before = nil
		f()
	}
}

func (h *writeHookStore) Delete(ctx context.Context, key string) error {
	h.fire()
	return h.Store.Delete(ctx, key)
}

func (h *writeHookStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	h.fire()
	return h.Store.PutAllIfAllUnchanged(ctx, guards, writes)
}

// TestPruneBarrierHistory_RecreatedBetweenReadAndDelete: a credential
// deleted and recreated after the trim read the record that selected its
// versions, and before it deleted them, keeps every retained version of the
// new credential: the trim's batch is guarded on the record it read, so it
// deletes nothing.
func TestPruneBarrierHistory_RecreatedBetweenReadAndDelete(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	rotateWithHistory(t, svc, "k", 3)
	require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark", func(m *credentialMeta) bool {
		m.PendingCleanup = "c1"
		return true
	}))

	var recreated *credentialMeta
	inner, err := svc.store.current()
	require.NoError(t, err)
	hook := &writeHookStore{Store: inner}
	hook.before = func() {
		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		setFaultPolicy(t, svc, "k", 2)
		rotateWithHistory(t, svc, "k", 2)
		var err error
		recreated, err = svc.loadBarrierMeta(ctx, "k")
		require.NoError(t, err)
		require.Empty(t, recreated.PendingCleanup)
	}
	require.NoError(t, svc.store.bind(hook))

	require.NoError(t, svc.pruneBarrierHistory(ctx, "k", "c1"))
	require.NotNil(t, recreated, "the hook never ran")
	require.Len(t, recreated.History, 2)

	after, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, recreated.History, after.History, "the recreated credential's history was trimmed")
	for _, h := range after.History {
		buf, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", h.Version)
		require.NoError(t, err, "version %d reaped", h.Version)
		buf.Destroy()
	}
}

// TestCleanupRotation_RecreatedKeepsItsStagedValue: a credential deleted
// and recreated after the cleanup listed the spent staged values, and before
// it deleted them, keeps the value its own rotation staged under the same
// key: the deletes are one batch guarded on the record that named the
// cleanup's rotation, so they delete nothing.
func TestCleanupRotation_RecreatedKeepsItsStagedValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	rotateWithHistory(t, svc, "k", 3)
	first, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	staged := first.CurrentVersion
	// The first generation left a spent staged value for its current
	// version; its cleanup is pending.
	require.NoError(t, svc.store.Put(ctx, stagedKey("k", staged), []byte("spent")))
	require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark", func(m *credentialMeta) bool {
		m.PendingCleanup = "c1"
		return true
	}))

	var recreated *credentialMeta
	inner, err := svc.store.current()
	require.NoError(t, err)
	hook := &writeHookStore{Store: inner}
	hook.before = func() {
		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		setFaultPolicy(t, svc, "k", 2)
		// The new generation's rotation stages its value under the key
		// the first generation's cleanup listed.
		require.NoError(t, svc.store.Put(ctx, stagedKey("k", staged), []byte("in-flight")))
		require.NoError(t, svc.updateBarrierMeta(ctx, "k", "stage", func(m *credentialMeta) bool {
			m.Staged = staged
			return true
		}))
		var err error
		recreated, err = svc.loadBarrierMeta(ctx, "k")
		require.NoError(t, err)
		require.Less(t, recreated.CurrentVersion, staged)
	}
	require.NoError(t, svc.store.bind(hook))

	require.NoError(t, svc.cleanupRotation(ctx, "k", "c1"))
	require.NotNil(t, recreated, "the hook never ran")

	value, err := svc.store.Get(ctx, stagedKey("k", staged))
	require.NoError(t, err, "the recreated credential's staged value was deleted")
	assert.Equal(t, "in-flight", string(value))
	after, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, staged, after.Staged)
}
