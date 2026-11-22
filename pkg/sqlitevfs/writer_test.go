// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlitevfs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// memLocker is an in-memory lock.Locker over a mutex-guarded map. It keeps
// the contract the page store relies on: TryLock refuses a key another
// holder has with *lock.LockHeldError and hands out strictly increasing
// fencing tokens; Extend and Unlock return *lock.LockNotFoundError for an
// absent key and *lock.NotLockHolderError for a wrong token. Lock is TryLock:
// blocking acquisition is not exercised by these tests. expire deletes an
// entry to model lease expiry.
type memLocker struct {
	mu    sync.Mutex
	locks map[string]lock.Lock
	next  int64
}

var _ lock.Locker = (*memLocker)(nil)

func newMemLocker() *memLocker {
	return &memLocker{locks: make(map[string]lock.Lock)}
}

func (m *memLocker) Lock(ctx context.Context, opts lock.LockOptions) (*lock.Lock, error) {
	return m.TryLock(ctx, opts)
}

func (m *memLocker) TryLock(_ context.Context, opts lock.LockOptions) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.locks[opts.Key]; ok {
		if !opts.Reentrant || held.HolderID != opts.HolderID {
			return nil, &lock.LockHeldError{Key: opts.Key, HolderID: held.HolderID}
		}
		held.AcquireCount++
		m.locks[opts.Key] = held
		out := held
		return &out, nil
	}
	m.next++
	l := lock.Lock{
		Key:          opts.Key,
		FencingToken: m.next,
		HolderID:     opts.HolderID,
		Reentrant:    opts.Reentrant,
		AcquireCount: 1,
		TTL:          int64(opts.TTL / time.Second),
	}
	m.locks[opts.Key] = l
	out := l
	return &out, nil
}

// holding returns the entry of key held under token, or the error the lock
// service returns.
func (m *memLocker) holding(key string, token int64) (lock.Lock, error) {
	l, ok := m.locks[key]
	if !ok {
		return lock.Lock{}, &lock.LockNotFoundError{Key: key}
	}
	if l.FencingToken != token {
		return lock.Lock{}, &lock.NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: l.FencingToken}
	}
	return l, nil
}

func (m *memLocker) Unlock(_ context.Context, key string, token int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.holding(key, token); err != nil {
		return err
	}
	delete(m.locks, key)
	return nil
}

func (m *memLocker) Extend(_ context.Context, key string, token int64) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.holding(key, token)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (m *memLocker) Status(_ context.Context, key string) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[key]
	if !ok {
		return nil, nil
	}
	return &l, nil
}

func (m *memLocker) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*lock.Lock
	for key, l := range m.locks {
		if strings.HasPrefix(key, prefix) {
			l := l
			out = append(out, &l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// expire drops key's lock, as the lock service does when its lease runs out.
func (m *memLocker) expire(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, key)
}

func newWriter(t *testing.T, l lock.Locker, holder string) WriterLock {
	t.Helper()
	w, err := NewLockerWriter(l, holder, time.Minute)
	if err != nil {
		t.Fatalf("NewLockerWriter: %v", err)
	}
	return w
}

func TestNewLockerWriterRefusals(t *testing.T) {
	for name, build := range map[string]func() (WriterLock, error){
		"nil locker":   func() (WriterLock, error) { return NewLockerWriter(nil, "h", time.Minute) },
		"empty holder": func() (WriterLock, error) { return NewLockerWriter(newMemLocker(), "", time.Minute) },
		"zero ttl":     func() (WriterLock, error) { return NewLockerWriter(newMemLocker(), "h", 0) },
		"negative ttl": func() (WriterLock, error) { return NewLockerWriter(newMemLocker(), "h", -time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			if w, err := build(); !errors.Is(err, ErrInvalidOption) || w != nil {
				t.Fatalf("NewLockerWriter = %v, %v, want ErrInvalidOption", w, err)
			}
		})
	}
	if _, err := NewStore(newTestBackend(t), "app.db", WithWriterLock(nil)); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("WithWriterLock(nil) err = %v, want ErrInvalidOption", err)
	}
}

func TestLockerWriterMapping(t *testing.T) {
	ctx := t.Context()
	locker := newMemLocker()
	a := newWriter(t, locker, "a")
	b := newWriter(t, locker, "b")
	const key = "sqlite/app.db/writer"

	token, err := a.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := b.Acquire(ctx, key); !errors.Is(err, ErrBusy) {
		t.Fatalf("held Acquire err = %v, want ErrBusy", err)
	}
	if _, err := a.Acquire(ctx, key); !errors.Is(err, ErrBusy) {
		t.Fatalf("re-Acquire by the holder err = %v, want ErrBusy (not reentrant)", err)
	}
	if err := a.Check(ctx, key, token); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := a.Check(ctx, key, token+100); !errors.Is(err, ErrBusy) {
		t.Fatalf("Check with a wrong token err = %v, want ErrBusy", err)
	}
	if err := a.Release(ctx, key, token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := b.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if again <= token {
		t.Fatalf("fencing token %d after %d, want it to rise", again, token)
	}
	locker.expire(key)
	if err := b.Check(ctx, key, again); !errors.Is(err, ErrBusy) {
		t.Fatalf("Check after expiry err = %v, want ErrBusy", err)
	}
	if err := b.Release(ctx, key, again); err != nil {
		t.Fatalf("Release after expiry = %v, want nil", err)
	}
}
