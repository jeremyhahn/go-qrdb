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
	"errors"
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
	ctx, err := t.admit(ctx, "LockAcquire")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LockAcquire"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, &NilServiceError{Service: "LockServicer"}
	}

	l, err := t.lockSvc.Lock(ctx, req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		return nil, translateLockError(err)
	}

	return mapLockInfo(l), nil
}

// LockTryLock attempts to acquire the lock without blocking. Returns an
// error immediately when the lock is held by another owner.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	ctx, err := t.admit(ctx, "LockTryLock")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LockTryLock"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, &NilServiceError{Service: "LockServicer"}
	}

	l, err := t.lockSvc.TryLock(ctx, req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		return nil, translateLockError(err)
	}

	return mapLockInfo(l), nil
}

// LockRelease releases the distributed lock identified by key and fencing token.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	ctx, err := t.admit(ctx, "LockRelease")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "LockRelease"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return &NilServiceError{Service: "LockServicer"}
	}

	return translateLockError(t.lockSvc.Unlock(ctx, req.Key, req.FencingToken))
}

// LockExtend refreshes the TTL of an existing lock via lease keepalive.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	ctx, err := t.admit(ctx, "LockExtend")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LockExtend"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, &NilServiceError{Service: "LockServicer"}
	}

	l, err := t.lockSvc.Extend(ctx, req.Key, req.FencingToken)
	if err != nil {
		return nil, translateLockError(err)
	}

	return mapLockInfo(l), nil
}

// LockStatus returns the current lock state for the given key, or nil
// when the key is not currently locked.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	ctx, err := t.admit(ctx, "LockStatus")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LockStatus"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, &NilServiceError{Service: "LockServicer"}
	}

	l, err := t.lockSvc.Status(ctx, key)
	if err != nil {
		return nil, translateLockError(err)
	}
	if l == nil {
		return nil, nil
	}

	return mapLockInfo(l), nil
}

// LockList returns all active locks whose keys start with the given prefix.
// An empty prefix returns all active locks.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	ctx, err := t.admit(ctx, "LockList")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LockList"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.lockSvc == nil {
		return nil, &NilServiceError{Service: "LockServicer"}
	}

	locks, err := t.lockSvc.List(ctx, prefix)
	if err != nil {
		return nil, translateLockError(err)
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

// translateLockError keeps the lock package's own typed errors -- which the
// SDK re-exports as sdk.LockHeldError, sdk.NotLockHolderError and
// sdk.LockNotFoundError -- exactly as the lock service returned them, and
// translates everything else through translateError. A lock is composed from
// the lease, watch and MVCC engines, so a refusal from one of those (a
// backend that keeps no leases, say) must keep its identity across this seam
// as it does on every network transport, rather than arrive as a bare
// service error.
func translateLockError(err error) error {
	if err == nil {
		return nil
	}
	var held *lock.LockHeldError
	var notHolder *lock.NotLockHolderError
	var notFound *lock.LockNotFoundError
	if errors.As(err, &held) || errors.As(err, &notHolder) || errors.As(err, &notFound) {
		return err
	}
	return translateError(err)
}
