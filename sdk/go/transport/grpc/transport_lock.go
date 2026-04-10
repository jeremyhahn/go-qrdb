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

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LockClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// LockClient implementation
// ---------------------------------------------------------------------------

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled, through the gRPC transport.
func (t *Transport) LockAcquire(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LockAcquire(ctx, &pb.LockAcquireRequest{
		Key:       req.Key,
		HolderId:  req.HolderID,
		Ttl:       req.TTL,
		Reentrant: req.Reentrant,
	})
	if err != nil {
		return nil, mapGRPCError("LockAcquire", err)
	}

	return protoToLockInfo(resp.GetLock()), nil
}

// LockTryLock attempts to acquire the lock without blocking through the
// gRPC transport. Returns an error if the lock is already held.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LockTryLock(ctx, &pb.LockTryLockRequest{
		Key:       req.Key,
		HolderId:  req.HolderID,
		Ttl:       req.TTL,
		Reentrant: req.Reentrant,
	})
	if err != nil {
		return nil, mapGRPCError("LockTryLock", err)
	}

	return protoToLockInfo(resp.GetLock()), nil
}

// LockRelease releases the distributed lock identified by key and fencing
// token through the gRPC transport.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.LockRelease(ctx, &pb.LockReleaseRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	})
	if err != nil {
		return mapGRPCError("LockRelease", err)
	}

	return nil
}

// LockExtend refreshes the TTL of an existing lock through the gRPC transport.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LockExtend(ctx, &pb.LockExtendRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	})
	if err != nil {
		return nil, mapGRPCError("LockExtend", err)
	}

	return protoToLockInfo(resp.GetLock()), nil
}

// LockStatus returns the current lock state for key through the gRPC transport.
// Returns nil if no lock is held for the key.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LockStatus(ctx, &pb.LockStatusRequest{
		Key: key,
	})
	if err != nil {
		return nil, mapGRPCError("LockStatus", err)
	}

	return protoToLockInfo(resp.GetLock()), nil
}

// LockList returns all active locks whose keys start with prefix through the
// gRPC transport.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LockList(ctx, &pb.LockListRequest{
		Prefix: prefix,
	})
	if err != nil {
		return nil, mapGRPCError("LockList", err)
	}

	locks := make([]*transport.LockInfo, len(resp.GetLocks()))
	for i, l := range resp.GetLocks() {
		locks[i] = protoToLockInfo(l)
	}

	return &transport.LockListResponse{Locks: locks}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// protoToLockInfo converts a proto LockInfo to a transport LockInfo.
func protoToLockInfo(l *pb.LockInfo) *transport.LockInfo {
	if l == nil {
		return nil
	}
	return &transport.LockInfo{
		Key:          l.GetKey(),
		FencingToken: l.GetFencingToken(),
		LeaseID:      l.GetLeaseId(),
		HolderID:     l.GetHolderId(),
		Reentrant:    l.GetReentrant(),
		AcquireCount: l.GetAcquireCount(),
		TTL:          l.GetTtl(),
		AcquiredAt:   l.GetAcquiredAt(),
	}
}
