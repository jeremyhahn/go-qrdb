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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// LockBinding exposes distributed lock management with fencing tokens to
// the Wails frontend.
type LockBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewLockBinding creates a new LockBinding backed by the given connection.
func NewLockBinding(log *logger.Logger, conn *ConnectionBinding) *LockBinding {
	return &LockBinding{logger: log, conn: conn}
}

// Acquire acquires a distributed lock, blocking until it becomes available
// or the request is cancelled.
func (b *LockBinding) Acquire(req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	b.logger.Debug("Acquire called", "key", req.Key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Acquire failed", "error", err)
		return nil, err
	}
	info, err := cl.Lock().LockAcquire(context.Background(), req)
	if err != nil {
		b.logger.Error("Acquire failed", "key", req.Key, "error", err)
		return nil, err
	}
	b.logger.Debug("Acquire completed", "key", req.Key, "fencingToken", info.FencingToken)
	return info, nil
}

// TryLock attempts to acquire a lock without blocking, returning an error
// immediately when the lock is held by another owner.
func (b *LockBinding) TryLock(req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	b.logger.Debug("TryLock called", "key", req.Key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("TryLock failed", "error", err)
		return nil, err
	}
	info, err := cl.Lock().LockTryLock(context.Background(), req)
	if err != nil {
		b.logger.Error("TryLock failed", "key", req.Key, "error", err)
		return nil, err
	}
	b.logger.Debug("TryLock completed", "key", req.Key, "fencingToken", info.FencingToken)
	return info, nil
}

// Release releases a held lock identified by key and fencing token.
func (b *LockBinding) Release(req *transport.LockReleaseRequest) error {
	b.logger.Debug("Release called", "key", req.Key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Release failed", "error", err)
		return err
	}
	if err := cl.Lock().LockRelease(context.Background(), req); err != nil {
		b.logger.Error("Release failed", "key", req.Key, "error", err)
		return err
	}
	b.logger.Debug("Release completed", "key", req.Key)
	return nil
}

// Extend refreshes the TTL of an existing lock via lease keepalive.
func (b *LockBinding) Extend(req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	b.logger.Debug("Extend called", "key", req.Key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Extend failed", "error", err)
		return nil, err
	}
	info, err := cl.Lock().LockExtend(context.Background(), req)
	if err != nil {
		b.logger.Error("Extend failed", "key", req.Key, "error", err)
		return nil, err
	}
	b.logger.Debug("Extend completed", "key", req.Key, "fencingToken", info.FencingToken)
	return info, nil
}

// Status returns the current lock state for key, or nil when not locked.
func (b *LockBinding) Status(key string) (*transport.LockInfo, error) {
	b.logger.Debug("Status called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	info, err := cl.Lock().LockStatus(context.Background(), key)
	if err != nil {
		b.logger.Error("Status failed", "key", key, "error", err)
		return nil, err
	}
	b.logger.Debug("Status completed", "key", key)
	return info, nil
}

// List returns all active locks whose keys start with the given prefix.
func (b *LockBinding) List(prefix string) (*transport.LockListResponse, error) {
	b.logger.Debug("List called", "prefix", prefix)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Lock().LockList(context.Background(), prefix)
	if err != nil {
		b.logger.Error("List failed", "prefix", prefix, "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(resp.Locks))
	return resp, nil
}
