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
	"errors"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// LeaderTransferrer is the capability interface for handing a shard's
// leadership to a named replica. It sits beside AdminServicer rather than in
// it, as MemberPromoter does, so a test double or alternate AdminServicer
// that has not grown the method still satisfies AdminServicer; a transport
// binding type-asserts for it and answers *LeaderTransferUnsupportedError
// when it is absent. AdminService implements it unconditionally.
//
// admin:remove_member (pkg/permission, ScopeCross) gates it on every
// transport that wires this interface: moving leadership is the step
// RemoveMember already takes on its own when the replica it removes leads
// the shard, so it carries the same authorization.
type LeaderTransferrer interface {
	// TransferLeadership asks this node, which must lead shardID, to hand
	// leadership to targetReplicaID, a current voter of the shard, and waits
	// until that replica leads. See AdminService.TransferLeadership.
	TransferLeadership(ctx context.Context, shardID, targetReplicaID uint64) error
}

var _ LeaderTransferrer = (*AdminService)(nil)

// LeaderTransferUnsupportedError is returned by a transport binding when
// its configured AdminServicer does not implement LeaderTransferrer. The
// real AdminService always does; this is reached only by a test double or an
// alternate AdminServicer implementation that has not grown the method.
type LeaderTransferUnsupportedError struct{}

// Error returns a human-readable description.
func (e *LeaderTransferUnsupportedError) Error() string {
	return "service: this admin service does not support leadership transfer"
}

// TransferLeadership hands shardID's leadership from this node, its current
// leader, to targetReplicaID and returns once targetReplicaID is the leader
// this node observes. Raft only starts the transfer when asked (the target
// must catch up and win an election), so this bounds the wait with the same
// loop RemoveMember uses to move leadership off a replica it removes.
//
// It returns:
//   - *UnknownReplicaError when targetReplicaID is 0 or a replica the shard
//     has never had;
//   - *UnknownShardError when shardID is not loaded on this node;
//   - *NotLeaderError, naming the leader, when this node does not lead it;
//   - *LeaderTransferTargetError when the target is an observer, a witness,
//     or was removed;
//   - *LeaderTransferTimeoutError when the target has not taken over within
//     the bound, which is what a stopped or partitioned target produces;
//   - ctx.Err() when ctx ends first.
//
// Naming the current leader succeeds at once. A timeout changes nothing and
// may be retried; leadership may still move later if the target recovers.
func (s *AdminService) TransferLeadership(ctx context.Context, shardID, targetReplicaID uint64) error {
	if targetReplicaID == 0 {
		return &UnknownReplicaError{ShardID: shardID}
	}
	from, _, err := s.host.LeaderID(shardID)
	if err != nil {
		return translateLeaderTransferError(shardID, err)
	}
	if err := s.host.RequestLeaderTransfer(ctx, shardID, targetReplicaID); err != nil {
		return translateLeaderTransferError(shardID, err)
	}

	timeout := s.leaderTransferTimeout
	if timeout == 0 {
		timeout = defaultLeaderTransferWaitTimeout
	}
	return awaitLeaderTransfer(ctx,
		func() (uint64, bool, error) { return s.host.LeaderID(shardID) },
		func(id uint64) bool { return id == targetReplicaID },
		&LeaderTransferTimeoutError{ShardID: shardID, FromReplicaID: from, ToReplicaID: targetReplicaID, Timeout: timeout})
}

// translateLeaderTransferError maps the Raft host's refusals of a leader
// transfer onto the service's typed errors. Anything it does not recognise
// stays a *MembershipError, as RemoveMember's failures do.
func translateLeaderTransferError(shardID uint64, err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{ShardID: nle.ShardID, LeaderID: nle.LeaderID, LeaderAddress: nle.LeaderAddress}
	}
	var rnf *quicraft.ReplicaNotFoundError
	if errors.As(err, &rnf) {
		return &UnknownReplicaError{ShardID: rnf.ShardID, ReplicaID: rnf.ReplicaID}
	}
	var ime *quicraft.InvalidMemberError
	if errors.As(err, &ime) {
		return &LeaderTransferTargetError{ShardID: ime.ShardID, ReplicaID: ime.ReplicaID, Reason: ime.Reason}
	}
	if errors.Is(err, quicraft.ErrShardNotFound) {
		return &UnknownShardError{ShardID: shardID}
	}
	return &MembershipError{Op: "transfer-leadership", Err: err}
}
