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
	"fmt"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// DefaultWriterLockTTL is the writer lock's lease when NewWriterLock is
// given none: the server refuses a lock of no lease.
const DefaultWriterLockTTL = 30 * time.Second

// NewWriterLock returns the database writer lock over a Go SDK client's lock
// service, held by holderID with a lease of ttl. A ttl of zero is
// DefaultWriterLockTTL; the wire carries whole seconds, so a ttl under a
// second is refused with sqlitevfs.ErrInvalidOption.
func NewWriterLock(l transport.LockClient, holderID string, ttl time.Duration) (sqlitevfs.WriterLock, error) {
	if l == nil {
		return nil, sqlitevfs.ErrNilBackend
	}
	if ttl == 0 {
		ttl = DefaultWriterLockTTL
	}
	if ttl < time.Second {
		return nil, fmt.Errorf("%w: writer lock ttl %s is under the wire's one second", sqlitevfs.ErrInvalidOption, ttl)
	}
	return sqlitevfs.NewLockServiceWriter(lockAdapter{c: l}, holderID, ttl)
}

// lockAdapter is a transport.LockClient as a sqlitevfs.LockServiceLocker.
// The lock outcomes already cross every wire typed, so they pass through.
type lockAdapter struct {
	c transport.LockClient
}

// TryLock acquires key for holderID without waiting.
func (a lockAdapter) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	info, err := a.c.LockTryLock(ctx, &transport.LockAcquireRequest{
		Key:       key,
		HolderID:  holderID,
		TTL:       int64(ttl / time.Second),
		Reentrant: reentrant,
	})
	if err != nil {
		return nil, wireError(err)
	}
	return toLock(info), nil
}

// Extend refreshes the lease of the lock held under fencingToken.
func (a lockAdapter) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	info, err := a.c.LockExtend(ctx, &transport.LockExtendRequest{Key: key, FencingToken: fencingToken})
	if err != nil {
		return nil, wireError(err)
	}
	return toLock(info), nil
}

// Unlock releases the lock held under fencingToken.
func (a lockAdapter) Unlock(ctx context.Context, key string, fencingToken int64) error {
	return wireError(a.c.LockRelease(ctx, &transport.LockReleaseRequest{Key: key, FencingToken: fencingToken}))
}

// toLock converts the wire's lock to the server's.
func toLock(info *transport.LockInfo) *lock.Lock {
	if info == nil {
		return nil
	}
	return &lock.Lock{
		Key:          info.Key,
		FencingToken: info.FencingToken,
		LeaseID:      info.LeaseID,
		HolderID:     info.HolderID,
		Reentrant:    info.Reentrant,
		AcquireCount: info.AcquireCount,
		TTL:          info.TTL,
		AcquiredAt:   info.AcquiredAt,
	}
}
