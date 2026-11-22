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
	"fmt"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// WriterLock serializes the writers of a database (Q3 A): the transaction
// that holds it between Txn.Reserve and Txn.Commit is the only page store
// writer of the database.
type WriterLock interface {
	// Acquire takes the lock named key and returns its fencing token. It
	// never blocks: a lock held by another writer is ErrBusy at once, and
	// SQLite's busy handler decides whether to retry.
	Acquire(ctx context.Context, key string) (token int64, err error)
	// Check confirms that the holder of token still owns the lock and
	// keeps its lease alive. A lost lease is ErrBusy.
	Check(ctx context.Context, key string, token int64) error
	// Release gives the lock up. Releasing a lock whose lease already
	// expired is not an error: nothing is held any more, and a commit is
	// never failed over it.
	Release(ctx context.Context, key string, token int64) error
}

// writerLocker is the part of a distributed lock the writer lock uses.
// lock.Locker satisfies it, and lockServiceAdapter puts a
// LockServiceLocker behind it.
type writerLocker interface {
	TryLock(ctx context.Context, opts lock.LockOptions) (*lock.Lock, error)
	Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	Unlock(ctx context.Context, key string, fencingToken int64) error
}

// LockServiceLocker is the part of the server's lock service the writer
// lock uses. *service.LockService, the lock service the server resolves for
// a (tenant, database) scope, satisfies it.
type LockServiceLocker interface {
	TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	Unlock(ctx context.Context, key string, fencingToken int64) error
}

// lockServiceAdapter is a writerLocker over a LockServiceLocker.
type lockServiceAdapter struct {
	svc LockServiceLocker
}

func (a lockServiceAdapter) TryLock(ctx context.Context, opts lock.LockOptions) (*lock.Lock, error) {
	return a.svc.TryLock(ctx, opts.Key, opts.HolderID, opts.TTL, opts.Reentrant)
}

func (a lockServiceAdapter) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	return a.svc.Extend(ctx, key, fencingToken)
}

func (a lockServiceAdapter) Unlock(ctx context.Context, key string, fencingToken int64) error {
	return a.svc.Unlock(ctx, key, fencingToken)
}

// lockerWriter is the WriterLock over a writerLocker.
type lockerWriter struct {
	locker   writerLocker
	holderID string
	ttl      time.Duration
}

// NewLockerWriter returns the Q3 A serialized-writer default: a WriterLock
// over the distributed lock l, taken as holderID with a lease of ttl. The
// lock is not reentrant. A nil locker, an empty holderID or a non-positive
// ttl is refused with ErrInvalidOption. NewLockServiceWriter builds the same
// writer lock over the server's lock service.
func NewLockerWriter(l lock.Locker, holderID string, ttl time.Duration) (WriterLock, error) {
	if l == nil {
		return nil, fmt.Errorf("%w: nil locker", ErrInvalidOption)
	}
	if holderID == "" {
		return nil, fmt.Errorf("%w: empty holder id", ErrInvalidOption)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: lease TTL %s", ErrInvalidOption, ttl)
	}
	return &lockerWriter{locker: l, holderID: holderID, ttl: ttl}, nil
}

// NewLockServiceWriter returns the writer lock of NewLockerWriter over svc,
// the lock service the server resolves for a (tenant, database) scope, taken
// as holderID with a lease of ttl. The lock is not reentrant, and its errors
// map as NewLockerWriter's do. A nil service, an empty holderID or a
// non-positive ttl is refused with ErrInvalidOption.
func NewLockServiceWriter(svc LockServiceLocker, holderID string, ttl time.Duration) (WriterLock, error) {
	if svc == nil {
		return nil, fmt.Errorf("%w: nil lock service", ErrInvalidOption)
	}
	if holderID == "" {
		return nil, fmt.Errorf("%w: empty holder id", ErrInvalidOption)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: lease TTL %s", ErrInvalidOption, ttl)
	}
	return &lockerWriter{locker: lockServiceAdapter{svc: svc}, holderID: holderID, ttl: ttl}, nil
}

// Acquire calls TryLock once; a held lock is ErrBusy naming the key and its
// holder.
func (w *lockerWriter) Acquire(ctx context.Context, key string) (int64, error) {
	l, err := w.locker.TryLock(ctx, lock.LockOptions{Key: key, HolderID: w.holderID, TTL: w.ttl})
	if err != nil {
		var held *lock.LockHeldError
		if errors.As(err, &held) {
			return 0, fmt.Errorf("%w: %q is held by %q", ErrBusy, key, held.HolderID)
		}
		return 0, fmt.Errorf("sqlitevfs: acquire writer lock %q: %w", key, err)
	}
	return l.FencingToken, nil
}

// Check extends the lease (a keepalive, so a long transaction that checks
// keeps it); a lock that is gone or held under another token is ErrBusy,
// wrapping the lock service's error so errors.As still finds it.
func (w *lockerWriter) Check(ctx context.Context, key string, token int64) error {
	if _, err := w.locker.Extend(ctx, key, token); err != nil {
		if leaseLost(err) {
			return fmt.Errorf("%w: lease on %q lost: %w", ErrBusy, key, err)
		}
		return fmt.Errorf("sqlitevfs: check writer lock %q: %w", key, err)
	}
	return nil
}

// Release unlocks; a lock that already expired or passed to another holder
// returns nil.
func (w *lockerWriter) Release(ctx context.Context, key string, token int64) error {
	if err := w.locker.Unlock(ctx, key, token); err != nil {
		if leaseLost(err) {
			return nil
		}
		return fmt.Errorf("sqlitevfs: release writer lock %q: %w", key, err)
	}
	return nil
}

// leaseLost reports whether err says the caller no longer holds the lock.
func leaseLost(err error) bool {
	var notHolder *lock.NotLockHolderError
	var notFound *lock.LockNotFoundError
	return errors.As(err, &notHolder) || errors.As(err, &notFound)
}
