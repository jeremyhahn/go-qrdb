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

package unix

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LeaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// mapLeaseAbsent is mapGRPCError for the lease-by-ID endpoints. It mirrors
// sdk/go/transport/grpc's function of the same name: the unix transport is
// gRPC over a domain socket talking to the identical pkg/api/grpc server, so
// it receives the identical codes.NotFound for both service.LeaseNotFoundError
// and service.LeaseExpiredError and needs the identical message-text split to
// tell them apart (go-qrdb#169, completing #141 for the lease family). See
// the grpc package's mapLeaseAbsent for the full rationale, including why this
// is not the substring classification go-qrdb#289 deleted and what is not
// established about service.LeaseExpiredError's production reachability.
func mapLeaseAbsent(method string, leaseID int64, err error) error {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		return mapGRPCError(method, err)
	}
	if strings.Contains(st.Message(), "lease expired") {
		return &transport.LeaseExpiredError{LeaseID: leaseID}
	}
	return &transport.LeaseNotFoundError{LeaseID: leaseID}
}

// LeaseGrant creates a new lease with the specified TTL in seconds.
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

// LeaseRevoke immediately expires a lease and deletes attached keys.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return mapLeaseAbsent("LeaseRevoke", leaseID, err)
	}
	return nil
}

// LeaseKeepAlive resets the TTL on an existing lease.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseKeepAlive(ctx, &pb.LeaseKeepAliveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, mapLeaseAbsent("LeaseKeepAlive", leaseID, err)
	}

	return protoToLeaseInfo(resp.GetLease()), nil
}

// LeaseTimeToLive returns the current state of a lease.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.LeaseTimeToLive(ctx, &pb.LeaseTimeToLiveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, mapLeaseAbsent("LeaseTimeToLive", leaseID, err)
	}

	return protoToLeaseInfo(resp.GetLease()), nil
}

// LeaseList returns all active leases.
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
// Proto conversion helpers
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
