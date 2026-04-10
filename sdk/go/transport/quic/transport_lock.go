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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LockClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// LockClient implementation
// ---------------------------------------------------------------------------

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled, through the QUIC transport.
func (t *Transport) LockAcquire(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockAcquireRequest{
		Key:       req.Key,
		HolderId:  req.HolderID,
		Ttl:       req.TTL,
		Reentrant: req.Reentrant,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lock.Acquire", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LockAcquireResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLockInfo(pbResp.GetLock()), nil
}

// LockTryLock attempts to acquire the lock without blocking through the
// QUIC transport. Returns an error if the lock is already held.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockTryLockRequest{
		Key:       req.Key,
		HolderId:  req.HolderID,
		Ttl:       req.TTL,
		Reentrant: req.Reentrant,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lock.TryLock", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LockTryLockResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLockInfo(pbResp.GetLock()), nil
}

// LockRelease releases the distributed lock identified by key and fencing
// token through the QUIC transport.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockReleaseRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "lock.Release", payload)
	return err
}

// LockExtend refreshes the TTL of an existing lock through the QUIC transport.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockExtendRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lock.Extend", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LockExtendResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLockInfo(pbResp.GetLock()), nil
}

// LockStatus returns the current lock state for key through the QUIC
// transport. Returns nil if no lock is held for the key.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockStatusRequest{
		Key: key,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lock.Status", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LockStatusResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLockInfo(pbResp.GetLock()), nil
}

// LockList returns all active locks whose keys start with prefix through
// the QUIC transport.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LockListRequest{
		Prefix: prefix,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lock.List", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LockListResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	locks := make([]*transport.LockInfo, len(pbResp.GetLocks()))
	for i, l := range pbResp.GetLocks() {
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
