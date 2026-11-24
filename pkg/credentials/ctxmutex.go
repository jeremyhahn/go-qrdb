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

// ctxRWMutex is a reader/writer lock whose waits a context can end. The
// zero value is an unlocked mutex. It admits the service's credential
// operations (shared) against a store (re)binding (exclusive): a writer
// that is waiting blocks new readers, so a stream of operations cannot
// starve a binding. As with ctxMutex, a lock that is free is taken
// whatever the state of ctx; only the waits are cancellable.
type ctxRWMutex struct {
	mu             sync.Mutex
	readers        int
	writer         bool
	writersWaiting int
	// changed is closed, and dropped, on every release or abandoned
	// writer wait; a blocked caller waits on it and then re-checks.
	changed chan struct{}
}

// waitChan returns the channel the next state change closes. m.mu is held.
func (m *ctxRWMutex) waitChan() chan struct{} {
	if m.changed == nil {
		m.changed = make(chan struct{})
	}
	return m.changed
}

// broadcast wakes every blocked caller. m.mu is held.
func (m *ctxRWMutex) broadcast() {
	if m.changed != nil {
		close(m.changed)
		m.changed = nil
	}
}

// RLockContext locks m for reading, or returns ctx.Err() once ctx is done
// while a writer holds m or waits for it.
func (m *ctxRWMutex) RLockContext(ctx context.Context) error {
	for {
		m.mu.Lock()
		if !m.writer && m.writersWaiting == 0 {
			m.readers++
			m.mu.Unlock()
			return nil
		}
		ch := m.waitChan()
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// RUnlock undoes one RLockContext. Unlocking a mutex not read-locked
// panics, as sync.RWMutex does.
func (m *ctxRWMutex) RUnlock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readers == 0 {
		panic("credentials: RUnlock of unlocked ctxRWMutex")
	}
	m.readers--
	if m.readers == 0 {
		m.broadcast()
	}
}

// Lock locks m for writing, waiting as long as it takes.
func (m *ctxRWMutex) Lock() {
	_ = m.LockContext(context.Background())
}

// LockContext locks m for writing, or returns ctx.Err() once ctx is done
// while readers or another writer hold m. While it waits, new readers
// wait behind it.
func (m *ctxRWMutex) LockContext(ctx context.Context) error {
	waiting := false
	for {
		m.mu.Lock()
		if !m.writer && m.readers == 0 {
			m.writer = true
			if waiting {
				m.writersWaiting--
			}
			m.mu.Unlock()
			return nil
		}
		if !waiting {
			waiting = true
			m.writersWaiting++
		}
		ch := m.waitChan()
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			m.mu.Lock()
			m.writersWaiting--
			// Readers queued behind this writer may go on now.
			m.broadcast()
			m.mu.Unlock()
			return ctx.Err()
		}
	}
}

// Unlock unlocks m for writing. Unlocking a mutex not write-locked panics,
// as sync.RWMutex does.
func (m *ctxRWMutex) Unlock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.writer {
		panic("credentials: Unlock of unlocked ctxRWMutex")
	}
	m.writer = false
	m.broadcast()
}
