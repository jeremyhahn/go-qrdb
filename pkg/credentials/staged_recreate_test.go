package credentials

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// commitHookStore runs after following each guarded batch that applies,
// until after reports it is done.
type commitHookStore struct {
	Store
	after func() bool
}

func (h *commitHookStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	applied, err := h.Store.PutAllIfAllUnchanged(ctx, guards, writes)
	if f := h.after; f != nil && applied && err == nil {
		h.after = nil
		if !f() {
			h.after = f
		}
	}
	return applied, err
}

// leaveSpentStaged arranges for a staged value under version, which the
// next rotation of "k" commits, to be left behind once that commit has
// applied (as one an abandoned stage failed to delete is), with its
// delete armed to fail.
func leaveSpentStaged(t *testing.T, svc *CredentialService, backend *faultBackend, version uint64) {
	t.Helper()
	inner, err := svc.store.current()
	require.NoError(t, err)
	hook := &commitHookStore{Store: inner}
	hook.after = func() bool {
		meta, err := svc.loadBarrierMeta(context.Background(), "k")
		require.NoError(t, err)
		if meta.CurrentVersion != version {
			return false
		}
		require.NoError(t, svc.store.Put(context.Background(), stagedKey("k", version), []byte("spent")))
		arm(&backend.failDelete, stagedKey("k", version))
		return true
	}
	require.NoError(t, svc.store.bind(hook))
}

// recreateAndStage deletes "k", recreates it and stages version for the
// new credential, as a rotation of the recreated credential does.
func recreateAndStage(t *testing.T, svc *CredentialService, version uint64) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.DeleteCredential(ctx, "k"))
	setFaultPolicy(t, svc, "k", 2)
	require.NoError(t, svc.store.Put(ctx, stagedKey("k", version), []byte("in-flight")))
	require.NoError(t, svc.updateBarrierMeta(ctx, "k", "stage", func(m *credentialMeta) bool {
		m.Staged = version
		return true
	}))
}

func requireStagedKept(t *testing.T, svc *CredentialService, version uint64) {
	t.Helper()
	value, err := svc.store.Get(context.Background(), stagedKey("k", version))
	require.NoError(t, err, "the recreated credential's staged value was deleted")
	assert.Equal(t, "in-flight", string(value))
	meta, err := svc.loadBarrierMeta(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, version, meta.Staged)
}

func TestRotateBarrier_RecreatedAfterCommitKeepsItsStagedValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	inner, err := svc.store.current()
	require.NoError(t, err)
	var committed uint64
	hook := &commitHookStore{Store: inner}
	hook.after = func() bool {
		meta, err := svc.loadBarrierMeta(ctx, "k")
		require.NoError(t, err)
		committed = meta.CurrentVersion
		recreateAndStage(t, svc, committed)
		return true
	}
	require.NoError(t, svc.store.bind(hook))

	result, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("rotated")})
	require.NoError(t, err)
	require.NotZero(t, committed, "the hook never ran")
	require.Equal(t, result.NewVersion, committed)
	requireStagedKept(t, svc, committed)
}

func TestRotateBarrier_SpentStagedValueDeletedWithTheCommit(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	meta, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	_, exists, err := svc.loadBarrierCurrent(ctx, "k")
	require.NoError(t, err)
	_, next := rotationVersions(meta.CurrentVersion, exists)
	require.NoError(t, svc.store.Put(ctx, stagedKey("k", next), []byte("spent")))

	result, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("rotated")})
	require.NoError(t, err)
	require.Equal(t, next, result.NewVersion)
	_, err = svc.store.Get(ctx, stagedKey("k", next))
	require.Error(t, err, "the spent staged value survived the commit")
}

func TestRotateBarrier_AbandonKeepsARecreatedCredentialsStagedValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	opts := scheduledOpts(t, svc, "scheduled")
	const staged = uint64(2)
	opts.schedule.resumed = true
	opts.schedule.staged = staged
	// The credential is deleted after the claim: the commit finds no
	// claim and abandons its staged version, which the metadata it read
	// does not name.
	require.NoError(t, svc.DeleteCredential(ctx, "k"))
	inner, err := svc.store.current()
	require.NoError(t, err)
	hook := &writeHookStore{Store: inner}
	ran := false
	hook.before = func() {
		ran = true
		recreateAndStage(t, svc, staged)
	}
	require.NoError(t, svc.store.bind(hook))

	_, err = svc.RotateCredential(ctx, "k", opts)
	require.ErrorIs(t, err, ErrRotationClaimLost)
	require.True(t, ran, "the hook never ran")
	requireStagedKept(t, svc, staged)
}
