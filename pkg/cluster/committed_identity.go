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

package cluster

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// MembershipReader reads a shard's COMMITTED membership, linearizably. A
// *quicraft.Host satisfies it.
type MembershipReader interface {
	SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error)
}

// IdentityResolver supplies the member identity every add path hands to
// go-quicraft's *WithIdentity membership calls (go-qrdb#556).
//
// It keeps no store of its own. go-quicraft commits every member's identity
// in the shard's membership (quicraft.Membership.Identities) — seeded at
// bootstrap from Config.MemberIdentities and changed only by committed config
// changes — so a host that is already a member of any shard this node can
// read has an identity the cluster has already agreed on. The resolver reads
// it from there:
//
//   - a replica the target shard already names (a promotion, a re-add) has
//     its identity in that shard's own membership;
//   - any other replica is placed on a host, and a host that is a member of
//     the system shard has its identity in the system shard's membership,
//     found by the host's Raft address.
//
// A host in no committed membership — a brand-new node — has no identity the
// cluster can vouch for. It must come from outside the cluster, from the
// operator (the override), because learning it from the new node's own
// connection would let any holder of a cluster certificate claim the replica;
// see quicraft.Host.RequestAddNodeWithIdentity.
type IdentityResolver struct {
	members     MembershipReader
	systemShard uint64
	required    bool
}

// NewIdentityResolver returns a resolver over members. systemShardID is the
// shard every host is a member of. required reports whether the host has a
// Raft transport: a transport-less host admits members without identities,
// so there no membership is read and the result is the override, if any.
func NewIdentityResolver(members MembershipReader, systemShardID uint64, required bool) *IdentityResolver {
	return &IdentityResolver{members: members, systemShard: systemShardID, required: required}
}

// Resolve returns the identity to commit for replicaID at addr in shardID.
//
// override is the operator-supplied identity, or "". When the cluster has
// already committed an identity for the replica (or its host), an override
// must equal it (*MemberIdentityConflictError); otherwise the override is
// used. Without an override the committed identity is used, and a replica
// with none is refused with *MemberIdentityUnknownError on a host that
// requires identities. An address the committed membership binds to more than
// one identity is refused with *MemberIdentityAmbiguousError, a replica ID
// committed at a different address with *MemberAddressMismatchError, and a
// membership that cannot be read with *MembershipReadError.
func (r *IdentityResolver) Resolve(ctx context.Context, shardID, replicaID uint64, addr, override string) (string, error) {
	if override != "" {
		if err := ValidateMemberIdentity(override); err != nil {
			return "", err
		}
	}
	if !r.required {
		// A transport-less host admits members without identities and has
		// no wire boundary for one to protect: nothing to look up.
		return override, nil
	}
	committed, err := r.committed(ctx, shardID, replicaID, addr)
	if err != nil {
		return "", err
	}
	switch {
	case override != "" && committed != "" && override != committed:
		return "", &MemberIdentityConflictError{ShardID: shardID, ReplicaID: replicaID, Committed: committed, Override: override}
	case override != "":
		return override, nil
	case committed != "":
		return committed, nil
	default:
		return "", &MemberIdentityUnknownError{ShardID: shardID, ReplicaID: replicaID, Address: addr}
	}
}

// committed looks the replica up in the target shard's membership, then its
// host in the system shard's, by address. A shard this host has not loaded
// (quicraft.ErrShardNotFound — a dedicated shard being founded) contributes
// nothing; any other failure to read it is a *MembershipReadError, because
// falling through would resolve the replica from its host alone and could
// miss an identity the target shard has already committed for it.
//
// The address lookup runs for the system shard too: a new replica ID at the
// address of a host the system shard already names is that host, so an
// override is checked against the host's committed identity.
func (r *IdentityResolver) committed(ctx context.Context, shardID, replicaID uint64, addr string) (string, error) {
	if shardID != r.systemShard {
		m, err := r.members.SyncGetShardMembership(ctx, shardID)
		switch {
		case errors.Is(err, quicraft.ErrShardNotFound):
		case err != nil:
			return "", &MembershipReadError{ShardID: shardID, Err: err}
		case m != nil:
			id, err := replicaIdentity(shardID, m, replicaID, addr)
			if err != nil || id != "" {
				return id, err
			}
		}
	}
	sys, err := r.members.SyncGetShardMembership(ctx, r.systemShard)
	if err != nil {
		return "", &MembershipReadError{ShardID: r.systemShard, Err: err}
	}
	if sys == nil {
		return "", nil
	}
	if shardID == r.systemShard {
		id, err := replicaIdentity(shardID, sys, replicaID, addr)
		if err != nil || id != "" {
			return id, err
		}
	}
	return r.hostIdentity(sys, addr)
}

// replicaIdentity returns the identity m committed for replicaID. A replica
// ID the membership already places at a different address than addr is
// refused with *MemberAddressMismatchError: the add names one host, the
// committed identity belongs to another, and committing it would bind the
// replica to a host it is not on.
func replicaIdentity(shardID uint64, m *quicraft.Membership, replicaID uint64, addr string) (string, error) {
	for _, roster := range []map[uint64]string{m.Nodes, m.Observers, m.Witnesses} {
		if committed, ok := roster[replicaID]; ok && addr != "" && committed != addr {
			return "", &MemberAddressMismatchError{ShardID: shardID, ReplicaID: replicaID, Committed: committed, Address: addr}
		}
	}
	return m.Identities[replicaID], nil
}

// MemberAddressMismatchError is returned when an add names a replica ID the
// shard's committed membership already places at a different address.
type MemberAddressMismatchError struct {
	ShardID   uint64
	ReplicaID uint64
	Committed string
	Address   string
}

// Error returns a human-readable description.
func (e *MemberAddressMismatchError) Error() string {
	return "cluster: replica " + strconv.FormatUint(e.ReplicaID, 10) + " of shard " + strconv.FormatUint(e.ShardID, 10) +
		" is committed at " + e.Committed + ", not " + e.Address
}

// hostIdentity returns the identity the system shard committed for the host
// at addr. Every replica the membership places at addr is consulted: if they
// carry different identities the address does not name one host the cluster
// agrees on, and the add is refused rather than bound to whichever was seen
// first.
func (r *IdentityResolver) hostIdentity(sys *quicraft.Membership, addr string) (string, error) {
	if addr == "" {
		return "", nil
	}
	var found string
	var seen []string
	for _, roster := range []map[uint64]string{sys.Nodes, sys.Observers, sys.Witnesses} {
		for id, a := range roster {
			if a != addr {
				continue
			}
			identity := sys.Identities[id]
			if identity == "" || slices.Contains(seen, identity) {
				continue
			}
			seen = append(seen, identity)
			found = identity
		}
	}
	if len(seen) > 1 {
		slices.Sort(seen)
		return "", &MemberIdentityAmbiguousError{ShardID: r.systemShard, Address: addr, Identities: seen}
	}
	return found, nil
}

// MemberIdentityAmbiguousError is returned when the committed membership
// places replicas with different identities at one address, so the address
// does not name a single host whose identity the cluster agrees on.
type MemberIdentityAmbiguousError struct {
	ShardID    uint64
	Address    string
	Identities []string
}

// Error returns a human-readable description.
func (e *MemberIdentityAmbiguousError) Error() string {
	return "cluster: address " + e.Address + " is committed in shard " + strconv.FormatUint(e.ShardID, 10) +
		" with different identities (" + strings.Join(e.Identities, ", ") + ")"
}

// MemberIdentityConflictError is returned when an operator-supplied identity
// contradicts the identity the cluster has already committed for the replica
// (or its host). Committing it would bind a second keypair to one member.
type MemberIdentityConflictError struct {
	ShardID   uint64
	ReplicaID uint64
	Committed string
	Override  string
}

// Error returns a human-readable description.
func (e *MemberIdentityConflictError) Error() string {
	return "cluster: identity " + e.Override + " for replica " + strconv.FormatUint(e.ReplicaID, 10) +
		" of shard " + strconv.FormatUint(e.ShardID, 10) +
		" contradicts the committed identity " + e.Committed
}

// MemberIdentityUnknownError is returned when a replica is being added to a
// shard on a host that requires identities, no committed membership names it
// or its host, and no identity was supplied.
type MemberIdentityUnknownError struct {
	ShardID   uint64
	ReplicaID uint64
	Address   string
}

// Error returns a human-readable description, including how to get the
// identity.
func (e *MemberIdentityUnknownError) Error() string {
	return "cluster: no committed member identity for replica " + strconv.FormatUint(e.ReplicaID, 10) +
		" (" + e.Address + ") of shard " + strconv.FormatUint(e.ShardID, 10) +
		": it is not a member of any committed membership; print its identity with " +
		"`qrdbd node-identity --cert <its node certificate>` and pass it as the member's identity"
}

// MembershipReadError is returned when the committed membership an identity
// lookup needs cannot be read.
type MembershipReadError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *MembershipReadError) Error() string {
	return "cluster: read committed membership of shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MembershipReadError) Unwrap() error { return e.Err }
