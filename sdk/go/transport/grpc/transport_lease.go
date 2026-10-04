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

// mapLeaseAbsent is mapGRPCError for the lease-by-ID endpoints: it turns the
// server's codes.NotFound into the typed lease error every other transport
// carries for this condition (go-qrdb#169, completing #141 for the lease
// family), using the caller's own leaseID rather than parsing it back out of
// the status message. Everything else falls through to mapGRPCError
// unchanged.
//
// It is a SEPARATE function rather than a new case in mapGRPCError for the
// same reason mapUserNotFound is (see transport_auth.go): mapGRPCError sees
// only a code and a message and cannot know which lease call this is, so a
// NotFound case there could only guess -- and it already guesses, mapping
// every NotFound to KeyNotFoundError. The call site knows, because it knows
// which lease it asked about.
//
// pkg/api/grpc/errors.go maps both service.LeaseNotFoundError and
// service.LeaseExpiredError to codes.NotFound -- gRPC has no expired-specific
// sibling of NotFound, the same way REST's http.StatusNotFound doesn't --
// so the message text is the only signal left to tell the two apart. That is
// NOT the substring classification go-qrdb#289 deleted: #289 guessed WHICH
// SUBJECT was missing from a code shared across subjects (user, key, ...);
// here the call site already knows the subject is a lease, and only the two
// lease Error() strings the service layer defines ("lease not found" /
// "lease expired") are being told apart.
//
// NOT ESTABLISHED: whether any production state machine actually constructs
// service.LeaseExpiredError today -- pkg/lease's Lessor removes an expired
// lease from its table rather than reporting it distinct from not-found, so
// this branch is currently unreached in practice. The mapping exists so a
// caller's errors.As matches the day the engine does report it, the same as
// the LeaseExpiredError case sdk/go/transport/embedded/errors.go already
// carries for the identical, currently-unreached reason.
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

// mapOpsNotFoundError is mapLeaseAbsent for ExecuteTransaction and
// ExecuteBatch: checkResultError's only NotFound-classed outcome for a
// transaction or batch command is a write naming a lease the state machine
// does not hold (pkg/service/errors.go, pkg/statemachine/native_txn.go's
// batchMutations), so a NotFound status is unambiguous here too, and the
// offending lease ID is read back out of the SAME ops the caller sent,
// in the order the state machine validates them -- the first op naming a
// lease, success ops before failure ops -- rather than guessed from the
// server's message text (the substring classification go-qrdb#289 deleted).
//
// A request whose success AND failure branches each name a DIFFERENT lease
// cannot be disambiguated this way, since the caller does not learn which
// branch ran from a failed proposal; it is not a case this suite or any
// production caller constructs today, and scanning success ops first keeps
// the common one-lease-in-the-request shape exact.
func mapOpsNotFoundError(method string, ops []transport.Op, err error) error {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		return mapGRPCError(method, err)
	}
	for _, op := range ops {
		if op.LeaseID != 0 {
			return &transport.LeaseNotFoundError{LeaseID: op.LeaseID}
		}
	}
	return mapGRPCError(method, err)
}

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
		return mapLeaseAbsent("LeaseRevoke", leaseID, err)
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
		return nil, mapLeaseAbsent("LeaseKeepAlive", leaseID, err)
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
		return nil, mapLeaseAbsent("LeaseTimeToLive", leaseID, err)
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
