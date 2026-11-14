package credentials

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		_, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", h.Version)
		require.NoError(t, err, "version %d reaped", h.Version)
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
