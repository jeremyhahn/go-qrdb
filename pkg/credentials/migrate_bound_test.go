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
	// entered, when set, is signalled (without blocking) each time a
	// stalled Get starts waiting.
	entered chan struct{}
}

func (s *stalledStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.stalled.Load() {
		if s.entered != nil {
			select {
			case s.entered <- struct{}{}:
			default:
			}
		}
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

	// op runs under a caller context with no deadline. Should the bound
	// not hold, the watchdog cancels that context so the operation's
	// goroutine ends before the test fails.
	requireBounded := func(op func(context.Context) error) {
		t.Helper()
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- op(opCtx) }()
		select {
		case err := <-done:
			var opErr *OperationError
			require.ErrorAs(t, err, &opErr)
			assert.Equal(t, "migrate", opErr.Op)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Fatal("the operation is still waiting on a store that never answers")
		}
	}
	requireBounded(func(ctx context.Context) error {
		_, err := svc.RotateDue(ctx, time.Now())
		return err
	})
	requireBounded(func(ctx context.Context) error {
		_, err := svc.RetrieveCredentialBuffer(ctx, "db")
		return err
	})

	legacy.stalled.Store(false)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the retry finishes once the store answers")

	// A finished migration takes no timer: an expired bound cannot fail it.
	svc.rotationAttemptTimeout = time.Nanosecond
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}

// A migration holder stalled on a store that never answers, under a bound
// long enough not to end it, holds the migration lock. A caller queued behind
// it whose context is cancelled leaves the wait with the cancellation instead
// of waiting for the holder: a cancelled RotateDue cannot stall the scheduler.
func TestMigrateLock_CancelledWaiterReturnsWhileHolderStalls(t *testing.T) {
	ctx := context.Background()
	legacy := &stalledStore{Store: seal.NewMemoryBackend(), entered: make(chan struct{}, 1)}
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	shared := &leaderlessStore{Store: seal.NewMemoryBackend()}
	shared.down.Store(true)
	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared), "a migration that cannot finish does not fail the bind")
	shared.down.Store(false)
	svc.rotationAttemptTimeout = time.Hour
	legacy.stalled.Store(true)

	hctx, hcancel := context.WithCancel(ctx)
	defer hcancel()
	holderDone := make(chan error, 1)
	go func() {
		_, err := svc.ListMigrationConflicts(hctx)
		holderDone <- err
	}()
	select {
	case <-legacy.entered:
	case <-time.After(10 * time.Second):
		hcancel()
		<-holderDone
		t.Fatal("the holder never reached the stalled store")
	}

	requireCancelled := func(op func(context.Context) error) {
		t.Helper()
		opCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- op(opCtx) }()
		cancel()
		select {
		case err := <-done:
			var opErr *OperationError
			require.ErrorAs(t, err, &opErr)
			assert.Equal(t, "migrate", opErr.Op)
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(10 * time.Second):
			hcancel()
			<-holderDone
			<-done
			t.Fatal("a cancelled caller is still waiting on the migration lock")
		}
	}
	requireCancelled(func(ctx context.Context) error {
		_, err := svc.RotateDue(ctx, time.Now())
		return err
	})
	requireCancelled(func(ctx context.Context) error {
		_, err := svc.RetrieveCredentialBuffer(ctx, "db")
		return err
	})

	hcancel()
	assert.ErrorIs(t, <-holderDone, context.Canceled)
	legacy.stalled.Store(false)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the migration finishes once the store answers")
}

func TestCtxMutex(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		held    bool
		ctx     context.Context
		wantErr error
	}{
		{name: "free mutex locks", ctx: context.Background()},
		{name: "free mutex locks under an ended ctx", ctx: cancelled},
		{name: "held mutex with a cancelled ctx", held: true, ctx: cancelled, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m ctxMutex
			if tt.held {
				m.Lock()
			}
			err := m.LockContext(tt.ctx)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			// Exactly one lock is held either way: the earlier one or
			// LockContext's.
			m.Unlock()
			// Unlock freed it: it locks again at once.
			require.NoError(t, m.LockContext(context.Background()))
			m.Unlock()
			assert.Panics(t, m.Unlock, "unlock of an unlocked mutex")
		})
	}
}
