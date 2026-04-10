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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LeaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL in seconds
// through the gRPC transport.
func (t *Transport) LeaseGrant(ctx context.Context, ttl int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseGrant(ctx, &pb.LeaseGrantRequest{
		Ttl: ttl,
	})
	if err != nil {
		return nil, mapGRPCError("LeaseGrant", err)
	}

	return protoToLeaseInfo(resp.GetLease()), nil
}

// LeaseRevoke immediately expires a lease and deletes attached keys
// through the gRPC transport.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return mapGRPCError("LeaseRevoke", err)
	}
	return nil
}

// LeaseKeepAlive resets the TTL on an existing lease through the
// gRPC transport.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseKeepAlive(ctx, &pb.LeaseKeepAliveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, mapGRPCError("LeaseKeepAlive", err)
	}

	return protoToLeaseInfo(resp.GetLease()), nil
}

// LeaseTimeToLive returns the current state of a lease through the
// gRPC transport.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseTimeToLive(ctx, &pb.LeaseTimeToLiveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, mapGRPCError("LeaseTimeToLive", err)
	}

	return protoToLeaseInfo(resp.GetLease()), nil
}

// LeaseList returns all active leases through the gRPC transport.
func (t *Transport) LeaseList(ctx context.Context) ([]*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("LeaseList", err)
	}

	leases := make([]*transport.LeaseInfo, len(resp.GetLeases()))
	for i, l := range resp.GetLeases() {
		leases[i] = protoToLeaseInfo(l)
	}

	return leases, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// protoToLeaseInfo converts a proto Lease to a transport LeaseInfo.
func protoToLeaseInfo(l *pb.Lease) *transport.LeaseInfo {
	if l == nil {
		return nil
	}
	return &transport.LeaseInfo{
		ID:           l.GetId(),
		TTL:          l.GetTtl(),
		GrantedTTL:   l.GetGrantedTtl(),
		RemainingTTL: l.GetRemainingTtl(),
		Keys:         l.GetKeys(),
	}
}
