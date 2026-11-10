package credentials

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newHomeStore returns a Store over a fresh in-memory atomic KV, the shape of
// a tenant's home store.
func newHomeStore(t *testing.T) Store {
	t.Helper()
	home, err := NewKVStore(kvstoretest.NewGuardedMemoryStore())
	require.NoError(t, err)
	return home
}

// newLegacyBarrier returns a tenant barrier closed at cleanup.
func newLegacyBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	barrier := newTestBarrier(t)
	t.Cleanup(func() { _ = barrier.Close() })
	return barrier
}

func TestForTenant_SharesAuditLogger(t *testing.T) {
	parent, al := newAuditManualService(t)

	derived, err := parent.ForTenant(context.Background(), newHomeStore(t), newLegacyBarrier(t))
	require.NoError(t, err)
	require.NotSame(t, parent, derived)
	assert.Equal(t, StrategyBarrier, derived.Strategy())

	require.NoError(t, derived.StoreCredential(auditCtx(), "tenant-key", []byte("v")))

	ev, ok := al.findByAction("store")
	require.True(t, ok, "expected the derived store's audit event on the parent's logger")
	assert.Equal(t, "credential:tenant-key", ev.Resource)
}

func TestForTenant_IsolatedFromParent(t *testing.T) {
	parent := newBarrierService(t)

	derived, err := parent.ForTenant(context.Background(), newHomeStore(t), newLegacyBarrier(t))
	require.NoError(t, err)
	ctx := auditCtx()
	require.NoError(t, derived.StoreCredential(ctx, "tenant-only", []byte("secret")))

	buf, err := derived.RetrieveCredentialBuffer(ctx, "tenant-only")
	require.NoError(t, err)
	equal, err := buf.EqualBytes([]byte("secret"))
	require.NoError(t, err)
	assert.True(t, equal)
	buf.Destroy()

	_, err = parent.RetrieveCredentialBuffer(ctx, "tenant-only")
	assert.ErrorIs(t, err, ErrCredentialNotFound,
		"the parent store must not see the derived store's credential")
}

// TestForTenant_HomeStoreSharedAcrossNodes is the cross-node case: two nodes'
// services over one replicated home store, each with its own node-local
// barrier, read the same policy and value.
func TestForTenant_HomeStoreSharedAcrossNodes(t *testing.T) {
	home := newHomeStore(t)
	ctx := auditCtx()

	nodeA, err := newBarrierService(t).ForTenant(context.Background(), home, newLegacyBarrier(t))
	require.NoError(t, err)
	nodeB, err := newBarrierService(t).ForTenant(context.Background(), home, newLegacyBarrier(t))
	require.NoError(t, err)

	require.NoError(t, nodeA.StoreCredential(ctx, "shared", []byte("v1")))
	policy := RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, nodeA.SetRotationPolicy(ctx, "shared", policy))

	got, err := nodeB.RotationPolicy(ctx, "shared")
	require.NoError(t, err)
	assert.Equal(t, policy, got, "a policy set on node A must be the record node B reads")

	buf, err := nodeB.RetrieveCredentialBuffer(ctx, "shared")
	require.NoError(t, err)
	equal, err := buf.EqualBytes([]byte("v1"))
	require.NoError(t, err)
	assert.True(t, equal)
	buf.Destroy()
}

func TestForTenant_NilHome(t *testing.T) {
	parent := newBarrierService(t)
	derived, err := parent.ForTenant(context.Background(), nil, newLegacyBarrier(t))
	assert.ErrorIs(t, err, ErrNilStore)
	assert.Nil(t, derived)
}

func TestForTenant_NilBarrier(t *testing.T) {
	parent := newBarrierService(t)
	derived, err := parent.ForTenant(context.Background(), newHomeStore(t), nil)
	assert.ErrorIs(t, err, ErrNilBarrier)
	assert.Nil(t, derived)
}

// plainKV serves no conditional writes.
type plainKV struct{ kvstore.KVStore }

func TestNewKVStore_RefusesNilAndNonAtomic(t *testing.T) {
	_, err := NewKVStore(nil)
	assert.ErrorIs(t, err, ErrNilStore)

	_, err = NewKVStore(plainKV{kvstore.NewMemoryKVStore()})
	assert.ErrorIs(t, err, ErrStoreNotAtomic)

	// Conditional writes alone are not enough: a rotation commits its
	// records in one guarded batch.
	_, err = NewKVStore(kvstore.NewMemoryKVStore())
	assert.ErrorIs(t, err, ErrStoreNotAtomic)
}

func TestNewKVStore_MissingKeyIsSecretNotFound(t *testing.T) {
	kv := kvstoretest.NewGuardedMemoryStore()
	store, err := NewKVStore(kv)
	require.NoError(t, err)
	ctx := context.Background()

	_, err = store.Get(ctx, "absent")
	assert.ErrorIs(t, err, seal.ErrSecretNotFound)

	require.NoError(t, kv.Put(ctx, "present", []byte("v")))
	got, err := store.Get(ctx, "present")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}
