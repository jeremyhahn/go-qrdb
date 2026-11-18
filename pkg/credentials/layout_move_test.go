package credentials

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BindStoreFrom moves the replicated layout of the earlier store into the
// new one, on a service whose node-local migration already ran; a move
// the source refuses at bind time finishes before the next operation, and
// a record the new store already holds is kept.
func TestBindStoreFrom_MovesTheEarlierStoreLayout(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
	policy := RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, svc.SetRotationPolicy(ctx, "db", policy))
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, second.Put(ctx, credentialPrefix+"kept", []byte("cluster")))
	require.NoError(t, first.Put(ctx, credentialPrefix+"kept", []byte("node")))

	from := &faultyStore{Store: first, op: "list", key: credentialPrefix}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from), "a refused move does not fail the bind")
	assert.True(t, from.fired)

	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	got, err := svc.RotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, policy, got)
	kept, err := second.Get(ctx, credentialPrefix+"kept")
	require.NoError(t, err)
	assert.Equal(t, "cluster", string(kept), "the new store's record is kept")
	for _, prefix := range layoutPrefixes {
		keys, err := first.List(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, keys, "moved records leave the earlier store")
	}
}

// A plain BindStore after BindStoreFrom drops the pending move: it was
// into the store the new binding replaces.
func TestBindStore_DropsAPendingMove(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	require.NoError(t, first.Put(ctx, credentialPrefix+"db", []byte("v")))
	svc := newUnboundService(t)
	from := &faultyStore{Store: first, op: "list", key: credentialPrefix}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from))
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.ensureLayoutMoved(ctx))
	value, err := first.Get(ctx, credentialPrefix+"db")
	require.NoError(t, err)
	assert.Equal(t, "v", string(value))
}

func TestBindStoreFrom_NilStoresRefused(t *testing.T) {
	svc := newUnboundService(t)
	var nilBarrier *seal.StorageBarrier
	assert.ErrorIs(t, svc.BindStoreFrom(context.Background(), seal.NewMemoryBackend(), nilBarrier), ErrNilBarrier)
	assert.ErrorIs(t, svc.BindStoreFrom(context.Background(), nil, seal.NewMemoryBackend()), ErrNilStore)
}
