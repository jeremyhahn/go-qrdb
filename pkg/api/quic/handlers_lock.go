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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// lockToProto converts a lock.Lock to its protobuf representation.
func lockToProto(l *lock.Lock) *pb.LockInfo {
	if l == nil {
		return nil
	}
	return &pb.LockInfo{
		Key:          l.Key,
		FencingToken: l.FencingToken,
		LeaseId:      l.LeaseID,
		HolderId:     l.HolderID,
		Reentrant:    l.Reentrant,
		AcquireCount: l.AcquireCount,
		Ttl:          l.TTL,
		AcquiredAt:   l.AcquiredAt,
	}
}

// handleLockAcquire unmarshals a LockAcquireRequest, acquires the
// distributed lock via the lock service (blocking), and returns a
// serialized LockAcquireResponse.
func (s *Server) handleLockAcquire(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockAcquireRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := s.lockSvc.Lock(ctx, req.GetKey(), req.GetHolderId(), time.Duration(req.GetTtl())*time.Second, req.GetReentrant())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LockAcquireResponse{Lock: lockToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLockTryLock unmarshals a LockTryLockRequest, attempts to acquire
// the distributed lock immediately without blocking, and returns a
// serialized LockTryLockResponse.
func (s *Server) handleLockTryLock(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockTryLockRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := s.lockSvc.TryLock(ctx, req.GetKey(), req.GetHolderId(), time.Duration(req.GetTtl())*time.Second, req.GetReentrant())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LockTryLockResponse{Lock: lockToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLockRelease unmarshals a LockReleaseRequest, releases the held
// lock validated by fencing token, and returns an empty response payload.
func (s *Server) handleLockRelease(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockReleaseRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.lockSvc.Unlock(ctx, req.GetKey(), req.GetFencingToken()); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleLockExtend unmarshals a LockExtendRequest, refreshes the TTL
// on the held lock, and returns a serialized LockExtendResponse.
func (s *Server) handleLockExtend(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockExtendRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := s.lockSvc.Extend(ctx, req.GetKey(), req.GetFencingToken())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LockExtendResponse{Lock: lockToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLockStatus unmarshals a LockStatusRequest, retrieves the current
// state of the specified lock, and returns a serialized LockStatusResponse.
func (s *Server) handleLockStatus(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockStatusRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := s.lockSvc.Status(ctx, req.GetKey())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LockStatusResponse{Lock: lockToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLockList unmarshals a LockListRequest, retrieves all active locks
// optionally filtered by prefix, and returns a serialized LockListResponse.
func (s *Server) handleLockList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.lockSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "lock"}
	}

	var req pb.LockListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	locks, err := s.lockSvc.List(ctx, req.GetPrefix())
	if err != nil {
		return nil, err
	}

	pbLocks := make([]*pb.LockInfo, len(locks))
	for i, l := range locks {
		pbLocks[i] = lockToProto(l)
	}

	resp, err := proto.Marshal(&pb.LockListResponse{Locks: pbLocks})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
