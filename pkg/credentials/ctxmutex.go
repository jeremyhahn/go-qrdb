package credentials

import (
	"context"
	"sync"
)

// ctxMutex is a mutual-exclusion lock whose wait a context can end. The
// zero value is an unlocked mutex. It guards the migration and layout-move
// state, whose holders run storage I/O: a caller whose context is cancelled
// or past its deadline leaves the queue instead of waiting on a stalled
// holder.
type ctxMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *ctxMutex) init() {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
}

// Lock locks m, waiting as long as it takes.
func (m *ctxMutex) Lock() {
	m.init()
	m.ch <- struct{}{}
}

// LockContext locks m, or returns ctx.Err() once ctx is done while m is
// held by another. A free mutex is taken whatever the state of ctx, as
// sync.Mutex would: only the wait is cancellable.
func (m *ctxMutex) LockContext(ctx context.Context) error {
	m.init()
	select {
	case m.ch <- struct{}{}:
		return nil
	default:
	}
	select {
	case m.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock unlocks m. Unlocking an unlocked ctxMutex panics, as
// sync.Mutex does.
func (m *ctxMutex) Unlock() {
	m.init()
	select {
	case <-m.ch:
	default:
		panic("credentials: unlock of unlocked ctxMutex")
	}
}
