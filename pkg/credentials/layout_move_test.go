package credentials

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
		if prefix == credentialPrefix {
			assert.Equal(t, []string{credentialPrefix + "kept"}, keys,
				"a conflicting name stays in the earlier store")
			continue
		}
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

// refusingStore reports every guarded batch as not applied, as a store does
// when a guarded key appeared between the check and the write.
type refusingStore struct {
	Store
}

func (r *refusingStore) PutAllIfAllUnchanged(context.Context, []kvstore.Guard, []kvstore.KV) (bool, error) {
	return false, nil
}

// putLayout writes one credential's full replicated-layout record set.
func putLayout(t *testing.T, st Store, name, value string) []string {
	t.Helper()
	keys := []string{
		credentialPrefix + name,
		MetaKeyPrefix + name,
		versionKey(name, 1),
		stagedKey(name, 2),
		MigrationClaimPrefix + name,
	}
	for _, key := range keys {
		require.NoError(t, st.Put(context.Background(), key, []byte(value+"@"+key)))
	}
	return keys
}

func requireValue(t *testing.T, st Store, key, want string) {
	t.Helper()
	got, err := st.Get(context.Background(), key)
	require.NoError(t, err, key)
	assert.Equal(t, want, string(got), key)
}

func requireAbsent(t *testing.T, st Store, key string) {
	t.Helper()
	keys, err := st.List(context.Background(), key)
	require.NoError(t, err)
	assert.NotContains(t, keys, key)
}

func TestMoveLayout_PartialDestinationConflictKeepsBothCopies(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	dbKeys := putLayout(t, from, "db", "old")
	otherKeys := putLayout(t, from, "other", "old")
	require.NoError(t, to.Put(ctx, MetaKeyPrefix+"db", []byte("new-meta")))

	err := moveLayout(ctx, from, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	assert.Contains(t, err.Error(), "db")
	assert.NotContains(t, err.Error(), "old@", "the message never carries values")

	for _, key := range dbKeys {
		requireValue(t, from, key, "old@"+key)
	}
	requireValue(t, to, MetaKeyPrefix+"db", "new-meta")
	for _, key := range dbKeys {
		if key != MetaKeyPrefix+"db" {
			requireAbsent(t, to, key)
		}
	}
	for _, key := range otherKeys {
		requireValue(t, to, key, "old@"+key)
		requireAbsent(t, from, key)
	}
}

func TestMoveLayout_VersionOnlyDestinationConflicts(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	dbKeys := putLayout(t, from, "db", "old")
	require.NoError(t, to.Put(ctx, versionKey("db", 7), []byte("new-v7")))

	err := moveLayout(ctx, from, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, key := range dbKeys {
		requireValue(t, from, key, "old@"+key)
	}
	requireAbsent(t, to, credentialPrefix+"db")
}

func TestMoveLayout_MovesWholeSets(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := append(putLayout(t, from, "db", "v"), putLayout(t, from, "db2", "w")...)
	require.NoError(t, to.Put(ctx, credentialPrefix+"unrelated", []byte("x")))

	require.NoError(t, moveLayout(ctx, from, to))

	for _, key := range keys {
		want := "v@" + key
		if strings.Contains(key, "db2") || strings.Contains(key, encodedName("db2")) {
			want = "w@" + key
		}
		requireValue(t, to, key, want)
	}
	for _, prefix := range layoutPrefixes {
		left, err := from.List(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "a moved set leaves the earlier store")
	}
	requireValue(t, to, credentialPrefix+"unrelated", "x")
}

func TestMoveLayout_RefusedGuardMovesNothing(t *testing.T) {
	ctx := context.Background()
	from := &captureStore{Store: seal.NewMemoryBackend()}
	to := seal.NewMemoryBackend()
	keys := putLayout(t, from, "db", "old")

	err := moveLayout(ctx, from, &refusingStore{Store: to})
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, key := range keys {
		requireAbsent(t, to, key)
	}
	for _, key := range keys {
		requireWiped(t, from.gets, key)
	}
	from.reset()
	for _, key := range keys {
		requireValue(t, from, key, "old@"+key)
	}
}

func TestBindStoreFrom_WipesTheMovedValues(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
	require.NoError(t, svc.BindStore(ctx, first))

	from := &captureStore{Store: first}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from))

	require.NotEmpty(t, from.gets, "the move read the earlier store")
	for key := range from.gets {
		requireWiped(t, from.gets, key)
	}
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the moved value is intact")
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
}
