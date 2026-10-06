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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// PendingJoinRequest is the operator-facing view of a queued, tokenless
// member-side CSR join request. See clusterenroll.PendingJoinRequest.
type PendingJoinRequest = clusterenroll.PendingJoinRequest

// JoinRequestReviewer lists, approves and rejects CSRs a joiner queued
// through the member-side CSR approval path (req.Token == ""; see
// pkg/clusterenroll's pending_join.go). It is a distinct interface from
// AdminServicer, not methods added to it, for the identical reason
// ClusterJoinTokenMinter and MemberPromoter are distinct interfaces (see
// admin_join_token.go): every transport binding that wants to expose it
// type-asserts for this interface (adminSvc.(JoinRequestReviewer)) rather
// than every AdminServicer implementation -- including every test double
// across this repo -- needing to grow three methods most of them have no
// store to serve. AdminService implements it once member-side CSR join is
// configured (WithClusterJoinTokenStore / SetClusterJoinTokenStore) --
// Approve and Reject need no cakey.Provider of their own: deciding who may
// be admitted is a store-only operation, and the actual signing happens the
// next time the joiner's own retry reaches HandleJoinRequest on the join
// listener, exactly as it does for a minted token.
//
// cluster:join_token (ScopeCross, platform-only) gates every method here on
// every transport that wires this interface: reviewing a tokenless CSR is
// the same whole-cluster admission decision minting a token already is, not
// a narrower one.
type JoinRequestReviewer interface {
	// ListPendingJoinRequests returns every request awaiting a decision.
	ListPendingJoinRequests(ctx context.Context) ([]*PendingJoinRequest, error)

	// ApproveJoinRequest binds id's queued CSR to
	// spiffe://<trust domain>/node/<nodeID> -- the identity the eventual
	// leaf carries, an operator choice, never the joiner's self-reported
	// one. See clusterenroll.ApproveJoinRequest.
	ApproveJoinRequest(ctx context.Context, id, nodeID string) error

	// RejectJoinRequest records a terminal refusal of id's queued CSR.
	RejectJoinRequest(ctx context.Context, id, reason string) error
}

// ClusterJoinRequestReviewUnsupportedError is returned by a transport
// binding when its configured AdminServicer does not implement
// JoinRequestReviewer. The real AdminService always does; this is reached
// only by a test double or an alternate AdminServicer implementation that
// has not grown the methods.
type ClusterJoinRequestReviewUnsupportedError struct{}

// Error returns a human-readable description.
func (e *ClusterJoinRequestReviewUnsupportedError) Error() string {
	return "service: this admin service does not support reviewing join requests"
}

// approverIdentity reports the deciding operator's identity for the audit
// trail ApproveJoinRequest/RejectJoinRequest records, or "" when ctx carries
// none (e.g. the embedded transport, which authenticates no caller of its
// own -- see docs/embedding.md).
func approverIdentity(ctx context.Context) string {
	if id := auth.IdentityFromContext(ctx); id != nil {
		return id.Subject
	}
	return ""
}

// ListPendingJoinRequests implements JoinRequestReviewer.
func (s *AdminService) ListPendingJoinRequests(ctx context.Context) ([]*PendingJoinRequest, error) {
	store, _ := s.clusterJoinStoreAndDomain()
	if store == nil {
		return nil, &ClusterJoinTokenNotConfiguredError{}
	}
	return clusterenroll.ListPendingJoinRequests(ctx, store)
}

// ApproveJoinRequest implements JoinRequestReviewer.
func (s *AdminService) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	store, trustDomain := s.clusterJoinStoreAndDomain()
	if store == nil || trustDomain == "" {
		return &ClusterJoinTokenNotConfiguredError{}
	}
	_, err := clusterenroll.ApproveJoinRequest(ctx, store, trustDomain, id, nodeID, approverIdentity(ctx))
	return err
}

// RejectJoinRequest implements JoinRequestReviewer.
func (s *AdminService) RejectJoinRequest(ctx context.Context, id, reason string) error {
	store, _ := s.clusterJoinStoreAndDomain()
	if store == nil {
		return &ClusterJoinTokenNotConfiguredError{}
	}
	_, err := clusterenroll.RejectJoinRequest(ctx, store, id, reason, approverIdentity(ctx))
	return err
}

var _ JoinRequestReviewer = (*AdminService)(nil)

// ObserverMemberAdder adds a replica to a shard as a non-voting observer
// rather than a voter. It is a distinct interface from AdminServicer for
// the identical reason JoinRequestReviewer is -- see that type's own doc
// comment. AdminService implements it unconditionally (see
// AdminService.AddObserverMember in service.go); admin:add_member
// (ScopeCross) gates it on every transport that wires this interface, the
// same reconfiguration permission AddMember, RemoveMember and PromoteMember
// already require.
type ObserverMemberAdder interface {
	// AddObserverMember adds replicaID to shardID as an observer. See
	// MemberPromoter.PromoteMember for turning it into a voter later.
	AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error
}

var _ ObserverMemberAdder = (*AdminService)(nil)
