package credentials

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// stalledStore is a store whose reads, while stalled is set, never answer:
// Get returns only when its context is done, with the context's error.
type stalledStore struct {
	Store
	stalled atomic.Bool
}

func (s *stalledStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.stalled.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Store.Get(ctx, key)
}

// The rotation scheduler calls RotateDue with a context that has no deadline.
// A migration retried under it is bounded, so a store that never answers does
// not hold the migration lock, and every credential operation behind it, for
// good.
func TestEnsureMigrated_RetryIsBoundedWithoutCallerDeadline(t *testing.T) {
	ctx := context.Background()
	legacy := &stalledStore{Store: seal.NewMemoryBackend()}
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	shared := &leaderlessStore{Store: seal.NewMemoryBackend()}
	shared.down.Store(true)
	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared), "a migration that cannot finish does not fail the bind")
	shared.down.Store(false)
	legacy.stalled.Store(true)
	svc.rotationAttemptTimeout = 20 * time.Millisecond

	requireBounded := func(op func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- op() }()
		select {
		case err := <-done:
			var opErr *OperationError
			require.ErrorAs(t, err, &opErr)
			assert.Equal(t, "migrate", opErr.Op)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(10 * time.Second):
			t.Fatal("the operation is still waiting on a store that never answers")
		}
	}
	requireBounded(func() error {
		_, err := svc.RotateDue(ctx, time.Now())
		return err
	})
	requireBounded(func() error {
		_, err := svc.RetrieveCredentialBuffer(ctx, "db")
		return err
	})

	legacy.stalled.Store(false)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the retry finishes once the store answers")

	// A finished migration takes no timer: an expired bound cannot fail it.
	svc.rotationAttemptTimeout = time.Nanosecond
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}
