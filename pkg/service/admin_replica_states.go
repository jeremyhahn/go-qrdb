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
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// ReplicaStateReader reports, for every replica the answering node holds,
// its applied index and state-machine hash. It is a distinct interface from
// AdminServicer for the reason MemberPromoter is: every transport binding
// type-asserts for it (adminSvc.(ReplicaStateReader)) rather than every
// AdminServicer implementation -- including every test double -- growing a
// method most of them have no host to serve. AdminService implements it.
//
// The read answers for this node only and is never forwarded: comparing
// replicas means asking every node. admin:replica_state (pkg/permission,
// ScopeCross) gates it on every transport.
type ReplicaStateReader interface {
	// ReplicaStates reports the replicas of shardID on this node, or of
	// every shard when shardID is 0. Witnesses hold no state machine and
	// are never listed. Replicas are ordered by shard ID.
	ReplicaStates(ctx context.Context, shardID uint64) (*ReplicaStatesResponse, error)
}

// StateMachineHasher is the host capability ReplicaStates reads through. A
// Raft host satisfies it (go-quicraft's Host.StateMachineHash, promoted by
// raft.HostAdapter); the standalone backend does not, and AdminService
// refuses the read there with *ReplicaStateUnsupportedError.
type StateMachineHasher interface {
	// StateMachineHash returns the state-machine hash of this host's
	// replica of shardID and the applied index it was taken at. ok is false
	// when the state machine cannot hash itself.
	StateMachineHash(shardID uint64) (hash, appliedIndex uint64, ok bool, err error)
}

var _ ReplicaStateReader = (*AdminService)(nil)

// ReplicaStates implements ReplicaStateReader.
func (s *AdminService) ReplicaStates(ctx context.Context, shardID uint64) (*ReplicaStatesResponse, error) {
	hasher, ok := s.host.(StateMachineHasher)
	if !ok {
		return nil, &ReplicaStateUnsupportedError{}
	}
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	replicas := make([]ReplicaState, 0, len(info.ShardInfoList))
	for _, si := range info.ShardInfoList {
		if si.IsWitness || (shardID != 0 && si.ShardID != shardID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash, applied, supported, err := hasher.StateMachineHash(si.ShardID)
		switch {
		case errors.Is(err, quicraft.ErrShardNotFound) && shardID == 0:
			// Stopped between the listing and the read: it is no longer
			// this node's to report.
			continue
		case errors.Is(err, quicraft.ErrShardNotFound):
			return nil, &ReplicaNotHostedError{ShardID: shardID}
		case err != nil:
			return nil, &ReplicaStateError{ShardID: si.ShardID, Err: err}
		case !supported:
			return nil, &StateMachineHashUnsupportedError{ShardID: si.ShardID}
		}
		replicas = append(replicas, ReplicaState{
			ShardID:      si.ShardID,
			ReplicaID:    si.ReplicaID,
			AppliedIndex: applied,
			Hash:         hash,
		})
	}
	if shardID != 0 && len(replicas) == 0 {
		return nil, &ReplicaNotHostedError{ShardID: shardID}
	}
	slices.SortFunc(replicas, func(a, b ReplicaState) int { return cmp.Compare(a.ShardID, b.ShardID) })
	return &ReplicaStatesResponse{NodeID: s.host.ID(), Replicas: replicas}, nil
}

// ReplicaStateUnsupportedError is returned when the node's host cannot hash
// its state machines: a standalone node, which runs no Raft replicas.
type ReplicaStateUnsupportedError struct{}

// Error returns a human-readable description.
func (e *ReplicaStateUnsupportedError) Error() string {
	return "service: replica states requires mode: raft"
}

// ReplicaStateReadUnsupportedError is returned by a transport binding when
// its configured AdminServicer does not implement ReplicaStateReader. The
// real AdminService always does; this is reached only by a test double or an
// alternate AdminServicer implementation that has not grown the method.
type ReplicaStateReadUnsupportedError struct{}

// Error returns a human-readable description.
func (e *ReplicaStateReadUnsupportedError) Error() string {
	return "service: this admin service does not support reading replica states"
}

// StateMachineHashUnsupportedError is returned when a shard's state machine
// cannot hash itself. The read refuses rather than reporting a zero hash
// another replica could agree with.
type StateMachineHashUnsupportedError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *StateMachineHashUnsupportedError) Error() string {
	return "service: the state machine of shard " + strconv.FormatUint(e.ShardID, 10) + " does not support hashing"
}

// ReplicaNotHostedError is returned when a replica-state read names a shard
// this node holds no replica of, or holds only as a witness.
type ReplicaNotHostedError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *ReplicaNotHostedError) Error() string {
	return "service: shard " + strconv.FormatUint(e.ShardID, 10) + " is not hosted on this node"
}

// ReplicaStateError is returned when the host refuses a replica's state for
// any reason other than the shard not being hosted.
type ReplicaStateError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ReplicaStateError) Error() string {
	return "service: cannot read the state of shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the host's error.
func (e *ReplicaStateError) Unwrap() error {
	return e.Err
}
