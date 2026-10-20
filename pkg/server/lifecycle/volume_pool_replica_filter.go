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

package lifecycle

import (
	"context"
	"errors"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// PoolNotHealthyOnNodeError refuses a replica placement because the
// candidate node's latest health report does not show the tenant's
// volume pool in a ready state there -- security review item 5: a raft
// tenant's shard must never land on a node that cannot actually open its
// pool, even though the SHARD is placed by raftAddr, not by pool. Never
// carries a filesystem path (only the pool name, tenant id and node id);
// a node report's own Detail, which might, is deliberately not read here.
type PoolNotHealthyOnNodeError struct {
	TenantID string
	Pool     string
	NodeID   string
	// State is the node's reported state for this pool ("locked",
	// "unavailable", "unmapped", "no_report"), or empty when the node
	// has no report for this pool at all.
	State string
}

// Error implements the error interface.
func (e *PoolNotHealthyOnNodeError) Error() string {
	msg := "lifecycle: tenant " + e.TenantID + "'s pool " + e.Pool + " is not ready on node " + e.NodeID
	if e.State != "" {
		msg += " (" + e.State + ")"
	}
	return msg
}

// UnknownRaftAddrError refuses a replica placement because the
// candidate's raftAddr does not resolve to a known NodeID -- the
// volume-pool-health filter cannot verify a node it cannot name, so it
// fails closed rather than treating an unresolvable address as healthy.
type UnknownRaftAddrError struct {
	RaftAddr string
}

// Error implements the error interface.
func (e *UnknownRaftAddrError) Error() string {
	return "lifecycle: raft address " + e.RaftAddr + " does not resolve to a known node id"
}

// newVolumePoolHealthFilter builds the placement.ReplicaFilter security
// review item 5 asks for: a tenant's replica is refused on a node whose
// latest health report does not show the tenant's volume pool StateReady
// there. Composed via placement.WithReplicaFilter alongside the feature-
// gate filter (ANDed, not replacing it).
//
// tenantID is resolved against volumeSvc's underlying allocator (the SAME
// catalog BarrierRegistry.VolumeAllocator() already attaches): a tenant
// with no Assignment at all (*volume.AssignmentNotFoundError) -- a
// prefix/barrier-isolation tenant, or the system tenant's own shard --
// means this filter does not apply, and the candidate is admitted
// without further checks; this is deliberately NOT the same thing as
// "healthy," it is "not this filter's concern."
//
// raftAddrToNodeID is a point-in-time snapshot (see Infra.RaftAddrToNodeID's
// own doc comment for why it cannot be a live lookup at BuildServices
// construction time); members, read at filter time, covers the nodes it
// cannot -- see resolveNodeID. A raftAddr neither resolves is refused with
// *UnknownRaftAddrError: this filter fails closed on an address it
// cannot verify rather than silently admitting it.
func newVolumePoolHealthFilter(allocator volume.Allocator, volumeSvc volume.Service, raftAddrToNodeID map[string]string, members systemMembership) placement.ReplicaFilter {
	return func(tenantID string, _ uint64, raftAddr string) error {
		ctx := context.Background()
		assignment, err := allocator.Resolve(ctx, tenantID)
		if err != nil {
			var notFound *volume.AssignmentNotFoundError
			if errors.As(err, &notFound) {
				return nil
			}
			return err
		}

		nodeID, err := resolveNodeID(ctx, raftAddr, raftAddrToNodeID, members)
		if err != nil {
			return err
		}

		detail, err := volumeSvc.GetPool(ctx, assignment.Pool)
		if err != nil {
			return err
		}
		for _, row := range detail.NodeReports {
			if row.NodeID != nodeID {
				continue
			}
			if row.State == volume.NodeStateReady {
				return nil
			}
			return &PoolNotHealthyOnNodeError{TenantID: tenantID, Pool: assignment.Pool, NodeID: nodeID, State: row.State}
		}
		return &PoolNotHealthyOnNodeError{TenantID: tenantID, Pool: assignment.Pool, NodeID: nodeID}
	}
}

// systemMembership reads the system shard's committed membership: the live
// source of every node's replica ID, the decimal form of which is the NodeID
// a volume pool's per-node health report is keyed by.
type systemMembership func(ctx context.Context) (*quicraft.Membership, error)

// resolveNodeID translates a placement candidate's raftAddr into the NodeID
// its volume-pool health report is keyed by. The composer's boot-time
// snapshot answers first; on a miss the system shard's live membership does,
// because a node admitted after this one booted (`admin member add`) is in
// the membership and never in the snapshot. An address in neither is refused
// with *UnknownRaftAddrError, and a membership read that fails is returned
// as is: both fail closed. Witnesses are not consulted, since a witness
// hosts no data and never reports a pool.
func resolveNodeID(ctx context.Context, raftAddr string, snapshot map[string]string, members systemMembership) (string, error) {
	if nodeID, ok := snapshot[raftAddr]; ok {
		return nodeID, nil
	}
	if members == nil {
		return "", &UnknownRaftAddrError{RaftAddr: raftAddr}
	}
	m, err := members(ctx)
	if err != nil {
		return "", err
	}
	if m != nil {
		for _, replicas := range []map[uint64]string{m.Nodes, m.Observers} {
			for replicaID, addr := range replicas {
				if addr == raftAddr {
					return strconv.FormatUint(replicaID, 10), nil
				}
			}
		}
	}
	return "", &UnknownRaftAddrError{RaftAddr: raftAddr}
}
