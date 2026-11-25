package credentials

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelledCtx is a context that is already done: a lock taken with it is
// taken only when it is free at once.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// awaitWriterQueued yields until a writer waits on m.
func awaitWriterQueued(m *ctxRWMutex) {
	for {
		m.mu.Lock()
		n := m.writersWaiting
		m.mu.Unlock()
		if n > 0 {
			return
		}
		runtime.Gosched()
	}
}

func TestCtxRWMutex(t *testing.T) {
	bg := context.Background()

	t.Run("SharedAndExclusive", func(t *testing.T) {
		var m ctxRWMutex
		require.NoError(t, m.RLockContext(bg))
		require.NoError(t, m.RLockContext(cancelledCtx()), "readers share the lock")
		require.ErrorIs(t, m.LockContext(cancelledCtx()), context.Canceled, "readers exclude a writer")
		m.RUnlock()
		require.ErrorIs(t, m.LockContext(cancelledCtx()), context.Canceled, "one reader still excludes a writer")
		m.RUnlock()
		require.NoError(t, m.LockContext(cancelledCtx()), "a free lock is taken whatever ctx")
		require.ErrorIs(t, m.RLockContext(cancelledCtx()), context.Canceled, "a writer excludes readers")
		require.ErrorIs(t, m.LockContext(cancelledCtx()), context.Canceled, "a writer excludes writers")
		m.Unlock()
		m.Lock()
		m.Unlock()
	})

	t.Run("WaitingWriterBlocksNewReaders", func(t *testing.T) {
		var m ctxRWMutex
		require.NoError(t, m.RLockContext(bg))
		locked := make(chan struct{})
		go func() {
			m.Lock()
			close(locked)
		}()
		awaitWriterQueued(&m)
		require.ErrorIs(t, m.RLockContext(cancelledCtx()), context.Canceled,
			"a new reader waits behind a waiting writer")
		select {
		case <-locked:
			t.Fatal("the writer took the lock while a reader held it")
		default:
		}
		m.RUnlock()
		<-locked
		m.Unlock()
		require.NoError(t, m.RLockContext(cancelledCtx()))
		m.RUnlock()
	})

	t.Run("CancelledWriterWaitReleasesReaders", func(t *testing.T) {
		var m ctxRWMutex
		require.NoError(t, m.RLockContext(bg))
		wctx, cancel := context.WithCancel(bg)
		werr := make(chan error, 1)
		go func() { werr <- m.LockContext(wctx) }()
		awaitWriterQueued(&m)
		rerr := make(chan error, 1)
		go func() { rerr <- m.RLockContext(bg) }()
		cancel()
		require.ErrorIs(t, <-werr, context.Canceled)
		require.NoError(t, <-rerr, "a reader queued behind the abandoned writer goes on")
		m.RUnlock()
		m.RUnlock()
		require.NoError(t, m.LockContext(cancelledCtx()), "the abandoned wait left no writer queued")
		m.Unlock()
	})

	t.Run("CancelledReaderWait", func(t *testing.T) {
		var m ctxRWMutex
		m.Lock()
		rctx, cancel := context.WithCancel(bg)
		rerr := make(chan error, 1)
		go func() { rerr <- m.RLockContext(rctx) }()
		cancel()
		require.ErrorIs(t, <-rerr, context.Canceled)
		m.Unlock()
		require.NoError(t, m.LockContext(cancelledCtx()), "the abandoned reader holds nothing")
		m.Unlock()
	})

	t.Run("UnlockOfUnlockedPanics", func(t *testing.T) {
		var m ctxRWMutex
		assert.Panics(t, m.RUnlock)
		assert.Panics(t, m.Unlock)
	})
}
