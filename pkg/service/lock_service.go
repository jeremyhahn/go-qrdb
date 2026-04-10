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

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// LockServicer defines the distributed lock management interface exposed
// to API handlers. It provides blocking and non-blocking lock acquisition
// with fencing tokens for safe distributed coordination.
type LockServicer interface {
	// Lock acquires a distributed lock, blocking until acquired or the
	// context is cancelled.
	Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)

	// TryLock attempts to acquire a lock immediately. Returns a
	// *lock.LockHeldError if the lock is held by another owner.
	TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)

	// Unlock releases a held lock. The fencing token must match the
	// current lock state to prevent stale unlocks.
	Unlock(ctx context.Context, key string, fencingToken int64) error

	// Extend refreshes the TTL on a held lock without releasing it.
	// The fencing token must match the current lock state.
	Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)

	// Status returns the current lock state for a key, or nil if the
	// key is not locked.
	Status(ctx context.Context, key string) (*lock.Lock, error)

	// List returns all active locks, optionally filtered by key prefix.
	List(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

// LockService wraps a lock.Locker to implement LockServicer for use
// by protocol API handlers.
type LockService struct {
	locker lock.Locker
}

// Compile-time interface check.
var _ LockServicer = (*LockService)(nil)

// NewLockService creates a new LockServicer backed by the given services.
// All four service parameters must be non-nil.
func NewLockService(
	txSvc TransactionServicer,
	leaseSvc LeaseServicer,
	watchSvc WatchServicer,
	kvSvc MVCCKVServicer,
	logger *slog.Logger,
) (LockServicer, error) {
	locker, err := lock.NewLocker(txSvc, leaseSvc, watchSvc, kvSvc, logger)
	if err != nil {
		return nil, err
	}
	return &LockService{locker: locker}, nil
}

// Lock acquires a distributed lock, blocking until acquired or cancelled.
func (s *LockService) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	return s.locker.Lock(ctx, lock.LockOptions{
		Key:       key,
		HolderID:  holderID,
		TTL:       ttl,
		Reentrant: reentrant,
	})
}

// TryLock attempts to acquire a lock immediately without blocking.
func (s *LockService) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	return s.locker.TryLock(ctx, lock.LockOptions{
		Key:       key,
		HolderID:  holderID,
		TTL:       ttl,
		Reentrant: reentrant,
	})
}

// Unlock releases a held lock validated by fencing token.
func (s *LockService) Unlock(ctx context.Context, key string, fencingToken int64) error {
	return s.locker.Unlock(ctx, key, fencingToken)
}

// Extend refreshes the TTL on a held lock.
func (s *LockService) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	return s.locker.Extend(ctx, key, fencingToken)
}

// Status returns the current lock state for a key.
func (s *LockService) Status(ctx context.Context, key string) (*lock.Lock, error) {
	return s.locker.Status(ctx, key)
}

// List returns all active locks, optionally filtered by prefix.
func (s *LockService) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	return s.locker.List(ctx, prefix)
}
