package credentials

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// leaderlessStore is a store whose shard, while down is set, has no leader:
// every operation fails with the error the replicated backend passes up from
// the shard, and nothing is read or written. It counts its List calls.
type leaderlessStore struct {
	Store
	down  atomic.Bool
	lists atomic.Int32
}

func (l *leaderlessStore) err() error {
	if l.down.Load() {
		return quicraft.ErrShardNotReady
	}
	return nil
}

func (l *leaderlessStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := l.err(); err != nil {
		return nil, err
	}
	return l.Store.Get(ctx, key)
}

func (l *leaderlessStore) Put(ctx context.Context, key string, value []byte) error {
	if err := l.err(); err != nil {
		return err
	}
	return l.Store.Put(ctx, key, value)
}

func (l *leaderlessStore) Delete(ctx context.Context, key string) error {
	if err := l.err(); err != nil {
		return err
	}
	return l.Store.Delete(ctx, key)
}

func (l *leaderlessStore) List(ctx context.Context, prefix string) ([]string, error) {
	l.lists.Add(1)
	if err := l.err(); err != nil {
		return nil, err
	}
	return l.Store.List(ctx, prefix)
}

func (l *leaderlessStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := l.err(); err != nil {
		return false, err
	}
	return l.Store.PutIfAbsent(ctx, key, value)
}

func (l *leaderlessStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := l.err(); err != nil {
		return false, err
	}
	return l.Store.CompareAndSwap(ctx, key, expected, next)
}

func (l *leaderlessStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if err := l.err(); err != nil {
		return false, err
	}
	return l.Store.PutAllIfAllUnchanged(ctx, guards, writes)
}

// A node binds its credential store while the system shard may have no
// leader yet. The migration that fails then runs again before the next
// ordinary operation, which serves nothing until the node's own records have
// reached the replicated store.
func TestBindStore_FailedMigrationRetriedBeforeOrdinaryAccess(t *testing.T) {
	ctx := context.Background()
	legacy := &leaderlessStore{Store: seal.NewMemoryBackend()}
	policy := &RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	writeLegacy(t, legacy, "db", "v2", "v1", policy)
	shared := &leaderlessStore{Store: seal.NewMemoryBackend()}
	shared.down.Store(true)
	var calls atomic.Int32
	svc := newMigratingService(t, legacy, WithRotationValueSource(countingSource(&calls)))

	require.NoError(t, svc.BindStore(ctx, shared), "a migration that cannot finish does not fail the bind")

	requireMigrateError := func(err error) {
		t.Helper()
		var opErr *OperationError
		require.ErrorAs(t, err, &opErr)
		assert.Equal(t, "migrate", opErr.Op)
		assert.ErrorIs(t, err, quicraft.ErrShardNotReady)
	}
	buf, err := svc.RetrieveCredentialBuffer(ctx, "db")
	requireMigrateError(err)
	assert.Nil(t, buf)
	n, err := svc.RotateDue(ctx, time.Now().Add(100*365*24*time.Hour))
	requireMigrateError(err)
	assert.Zero(t, n)
	assert.Zero(t, calls.Load(), "no value is requested for a store the node's records have not reached")

	shared.down.Store(false)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the first retrieve finishes the migration")
	value, err := shared.Store.Get(ctx, credentialPrefix+"db")
	require.NoError(t, err)
	assert.Equal(t, "v2", string(value), "the record is in the replicated store")
	assert.Empty(t, legacyKeys(t, legacy))

	lists := legacy.lists.Load() + shared.lists.Load()
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, lists, legacy.lists.Load()+shared.lists.Load(), "a finished migration costs a retrieve no store read")
}
