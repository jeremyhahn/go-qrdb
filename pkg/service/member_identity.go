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

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// MemberIdentityHost is what identity resolution needs from a host: its
// committed memberships and whether it has a transport. Host and
// *quicraft.Host both satisfy it.
type MemberIdentityHost interface {
	cluster.MembershipReader
	NodeIdentity() string
}

// ResolveMemberIdentity returns the member identity to commit when adding
// replicaID at raftAddr to shardID on host: override when given (refused if
// it contradicts an identity the cluster already committed), else the
// identity go-quicraft committed for the replica or its host. A node no
// committed membership names, with no override, is refused on a host with a
// transport. Every refusal is a *MemberIdentityError. See
// cluster.IdentityResolver.
func ResolveMemberIdentity(ctx context.Context, host MemberIdentityHost, shardID, replicaID uint64, raftAddr, override string) (string, error) {
	r := cluster.NewIdentityResolver(host, shard.ShardKV, host.NodeIdentity() != "")
	identity, err := r.Resolve(ctx, shardID, replicaID, raftAddr, override)
	if err != nil {
		return "", &MemberIdentityError{ShardID: shardID, ReplicaID: replicaID, Err: err}
	}
	return identity, nil
}

// MemberIdentityError is returned when the identity a membership add needs
// cannot be settled: the operator-supplied identity is malformed
// (*cluster.MemberIdentityFormatError) or contradicts a committed one
// (*cluster.MemberIdentityConflictError), the address it names is committed
// with more than one identity (*cluster.MemberIdentityAmbiguousError), the
// replica ID is committed at another address
// (*cluster.MemberAddressMismatchError), the node is in no committed
// membership and none was supplied
// (*cluster.MemberIdentityUnknownError), or the committed membership could not
// be read (*cluster.MembershipReadError).
type MemberIdentityError struct {
	ShardID   uint64
	ReplicaID uint64
	Err       error
}

// Error returns a human-readable description.
func (e *MemberIdentityError) Error() string {
	return "service: member identity: " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *MemberIdentityError) Unwrap() error { return e.Err }

// MemberIdentityRefusal is the transport-neutral kind of a
// *MemberIdentityError, which every transport maps to its own status code.
type MemberIdentityRefusal uint8

const (
	// IdentityInvalid: the supplied identity is not spki:<64 hex digits>.
	IdentityInvalid MemberIdentityRefusal = iota
	// IdentityConflict: the supplied identity contradicts a committed one,
	// the committed membership binds the address to more than one identity,
	// or it already places the replica ID at another address.
	IdentityConflict
	// IdentityUnknown: the node is in no committed membership and no
	// identity was supplied.
	IdentityUnknown
	// IdentityMembershipUnavailable: the committed membership could not be
	// read (no leader, shutting down); retrying may succeed.
	IdentityMembershipUnavailable
)

// Refusal classifies the error.
func (e *MemberIdentityError) Refusal() MemberIdentityRefusal {
	var format *cluster.MemberIdentityFormatError
	var conflict *cluster.MemberIdentityConflictError
	var ambiguous *cluster.MemberIdentityAmbiguousError
	var mismatch *cluster.MemberAddressMismatchError
	var read *cluster.MembershipReadError
	switch {
	case errors.As(e.Err, &format):
		return IdentityInvalid
	case errors.As(e.Err, &conflict), errors.As(e.Err, &ambiguous), errors.As(e.Err, &mismatch):
		return IdentityConflict
	case errors.As(e.Err, &read):
		return IdentityMembershipUnavailable
	default:
		return IdentityUnknown
	}
}

// MemberIdentities is the Config.MemberIdentities for starting host's
// replicaID of shardID with the given members (replica ID to Raft address). A
// host with a transport admits a member by its node identity alone
// (go-qrdb#556): the local replica's is the identity host presents, and every
// other member's is resolved as ResolveMemberIdentity resolves it, from the
// committed memberships.
//
// A founder (join false) needs every member's identity and fails with the
// *MemberIdentityError naming the first it cannot resolve. A joiner needs only
// anchors — replicas it accepts its bootstrap from — so a member that cannot
// be resolved is left out rather than failing the join; go-quicraft refuses a
// join left with no anchor at all. A transport-less host needs none, and the
// result is nil.
func MemberIdentities(ctx context.Context, host MemberIdentityHost, shardID, replicaID uint64, join bool, members map[uint64]string) (map[uint64]string, error) {
	self := host.NodeIdentity()
	if self == "" {
		return nil, nil
	}
	out := make(map[uint64]string, len(members))
	for id, addr := range members {
		if id == replicaID {
			out[id] = self
			continue
		}
		identity, err := ResolveMemberIdentity(ctx, host, shardID, id, addr, "")
		if err != nil {
			if join {
				continue
			}
			return nil, err
		}
		out[id] = identity
	}
	return out, nil
}
