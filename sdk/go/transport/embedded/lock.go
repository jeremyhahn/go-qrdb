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

package embedded

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LockClient = (*Transport)(nil)

// WithLockService injects a LockServicer into the transport.
func WithLockService(svc service.LockServicer) Option {
	return func(t *Transport) {
		t.lockSvc = svc
	}
}

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled. Delegates to the injected LockServicer.
func (t *Transport) LockAcquire(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, t.missingService("LockServicer")
	}

	l, err := t.lockSvc.Lock(ctx, req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		return nil, err
	}

	return mapLockInfo(l), nil
}

// LockTryLock attempts to acquire the lock without blocking. Returns an
// error immediately when the lock is held by another owner.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, t.missingService("LockServicer")
	}

	l, err := t.lockSvc.TryLock(ctx, req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		return nil, err
	}

	return mapLockInfo(l), nil
}

// LockRelease releases the distributed lock identified by key and fencing token.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return t.missingService("LockServicer")
	}

	return t.lockSvc.Unlock(ctx, req.Key, req.FencingToken)
}

// LockExtend refreshes the TTL of an existing lock via lease keepalive.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, t.missingService("LockServicer")
	}

	l, err := t.lockSvc.Extend(ctx, req.Key, req.FencingToken)
	if err != nil {
		return nil, err
	}

	return mapLockInfo(l), nil
}

// LockStatus returns the current lock state for the given key, or nil
// when the key is not currently locked.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, t.missingService("LockServicer")
	}

	l, err := t.lockSvc.Status(ctx, key)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, nil
	}

	return mapLockInfo(l), nil
}

// LockList returns all active locks whose keys start with the given prefix.
// An empty prefix returns all active locks.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, t.missingService("LockServicer")
	}

	locks, err := t.lockSvc.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	infos := make([]*transport.LockInfo, 0, len(locks))
	for _, l := range locks {
		infos = append(infos, mapLockInfo(l))
	}

	return &transport.LockListResponse{Locks: infos}, nil
}

// mapLockInfo converts a lock.Lock to a transport.LockInfo.
func mapLockInfo(l *lock.Lock) *transport.LockInfo {
	return &transport.LockInfo{
		Key:          l.Key,
		FencingToken: l.FencingToken,
		LeaseID:      l.LeaseID,
		HolderID:     l.HolderID,
		Reentrant:    l.Reentrant,
		AcquireCount: l.AcquireCount,
		TTL:          l.TTL,
		AcquiredAt:   l.AcquiredAt,
	}
}
