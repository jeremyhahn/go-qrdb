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
)

// handleLeaseGrant unmarshals a LeaseGrantRequest, creates a new lease
// with the specified TTL via the lease service, and returns a serialized
// LeaseGrantResponse.
func (s *Server) handleLeaseGrant(ctx context.Context, payload []byte) ([]byte, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.LeaseGrantRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := leaseSvc.Grant(ctx, req.GetTtl())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LeaseGrantResponse{Lease: leaseToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLeaseRevoke unmarshals a LeaseRevokeRequest, immediately expires
// the specified lease and deletes attached keys, and returns an empty
// response payload.
func (s *Server) handleLeaseRevoke(ctx context.Context, payload []byte) ([]byte, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.LeaseRevokeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := leaseSvc.Revoke(ctx, req.GetLeaseId()); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleLeaseKeepAlive unmarshals a LeaseKeepAliveRequest, resets the
// TTL on the specified lease, and returns a serialized
// LeaseKeepAliveResponse with the refreshed lease state.
func (s *Server) handleLeaseKeepAlive(ctx context.Context, payload []byte) ([]byte, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.LeaseKeepAliveRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := leaseSvc.KeepAlive(ctx, req.GetLeaseId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LeaseKeepAliveResponse{Lease: leaseToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLeaseTimeToLive unmarshals a LeaseTimeToLiveRequest, retrieves
// the current state of the specified lease including its remaining TTL,
// and returns a serialized LeaseTimeToLiveResponse.
func (s *Server) handleLeaseTimeToLive(ctx context.Context, payload []byte) ([]byte, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.LeaseTimeToLiveRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	l, err := leaseSvc.TimeToLive(ctx, req.GetLeaseId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.LeaseTimeToLiveResponse{Lease: leaseToProto(l)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleLeaseList retrieves all active leases from the lease service and
// returns a serialized LeaseListResponse. The payload is ignored since
// no request parameters are required.
func (s *Server) handleLeaseList(ctx context.Context, _ []byte) ([]byte, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	leases, err := leaseSvc.Leases(ctx)
	if err != nil {
		return nil, err
	}

	pbLeases := make([]*pb.Lease, len(leases))
	for i, l := range leases {
		pbLeases[i] = leaseToProto(l)
	}

	resp, err := proto.Marshal(&pb.LeaseListResponse{Leases: pbLeases})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
