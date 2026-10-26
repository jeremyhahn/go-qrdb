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
	"sort"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Replica state hashes
//
// A read of this node's replicas: for every shard the node replicates, the
// replica's applied index and the SHA-256 state digest (pkg/statedigest) of
// its state machine at exactly that index. Each replica answers a
// statedigest.Query through Host.StaleRead, so nothing is proposed and no
// leader is consulted; a caller compares replicas by asking every node.
//
// It is a cross-scope read: the admin:state_hash permission is ScopeCross,
// and the service refuses a caller bound to any tenant but the system tenant
// as well, so an in-process caller scoped to a tenant is refused exactly as a
// remote one is.
// ---------------------------------------------------------------------------

// ReplicaStateHasher is implemented by an AdminServicer that can report its
// node's replica state hashes. It is a capability interface rather than a
// method of AdminServicer for the reason MemberPromoter is one: the
// transports type-assert for it (adminSvc.(ReplicaStateHasher)) rather than
// every AdminServicer implementation growing the method.
type ReplicaStateHasher interface {
	// StateHashes returns the state digest of every replica this node runs
	// whose shard is in shardIDs; an empty shardIDs selects every shard. A
	// shard ID this node does not replicate selects nothing. One replica's
	// failure is reported in its own entry and never fails the call.
	StateHashes(ctx context.Context, shardIDs []uint64) (*StateHashesResponse, error)
}

// StateHashesResponse is one node's replica state hashes.
type StateHashesResponse struct {
	// NodeID is the answering node's host ID.
	NodeID string `json:"node_id"`

	// Replicas holds one entry per selected replica, ordered by TenantID,
	// then ShardID.
	Replicas []ReplicaStateHash `json:"replicas"`
}

// ReplicaStateHash is the state digest of one replica.
type ReplicaStateHash struct {
	// TenantID is "" for a shard of the node's own (system) host, otherwise
	// the isolated tenant whose host runs the shard.
	TenantID string `json:"tenant_id"`

	// ShardID and ReplicaID name the shard and this node's replica of it.
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`

	// AppliedIndex is the applied index the digest was computed at.
	AppliedIndex uint64 `json:"applied_index"`

	// Records is how many records the digest covers.
	Records uint64 `json:"records"`

	// Hash is the SHA-256 state digest, 64 lowercase hex digits.
	Hash string `json:"hash"`

	// Error is empty on success; otherwise it says why this replica has no
	// digest, and AppliedIndex, Records and Hash are zero.
	Error string `json:"error"`
}

// TenantReplicaHosts lists the isolated-tenant hosts already started on this
// node, keyed by tenant ID. It never starts one: a tenant whose host is not
// running here has no replicas here to hash.
type TenantReplicaHosts interface {
	StartedHosts() map[string]Host
}

// TenantReplicaHostsWiring is implemented by an admin service that hashes
// isolated tenants' replicas as well as its own host's. The server binds its
// tenant host registry through it once both exist.
type TenantReplicaHostsWiring interface {
	SetTenantReplicaHosts(hosts TenantReplicaHosts)
}

// ReplicaStateHashesUnsupportedError is returned by a transport binding when
// its configured AdminServicer does not implement ReplicaStateHasher. The
// real AdminService always does; this is reached only by a test double or an
// alternate AdminServicer implementation that has not grown the method.
type ReplicaStateHashesUnsupportedError struct{}

// Error returns a human-readable description.
func (e *ReplicaStateHashesUnsupportedError) Error() string {
	return "service: this admin service does not report replica state hashes"
}

// StateDigestUnsupportedError is a replica's entry error when its state
// machine answered a statedigest.Query with something other than a digest:
// a state-machine type that does not implement the query.
type StateDigestUnsupportedError struct{}

// Error returns a human-readable description.
func (e *StateDigestUnsupportedError) Error() string {
	return "state machine does not support state digests"
}

var (
	_ ReplicaStateHasher       = (*AdminService)(nil)
	_ TenantReplicaHostsWiring = (*AdminService)(nil)
)

// tenantReplicaHostsBox lets an interface value sit behind an atomic.Pointer.
type tenantReplicaHostsBox struct{ hosts TenantReplicaHosts }

// SetTenantReplicaHosts binds the isolated-tenant hosts StateHashes reports
// beside the node's own. It is set late, once the server's tenant host
// registry exists, and is safe to call concurrently with StateHashes; nil
// unbinds them.
func (s *AdminService) SetTenantReplicaHosts(hosts TenantReplicaHosts) {
	if hosts == nil {
		s.tenantReplicaHosts.Store(nil)
		return
	}
	s.tenantReplicaHosts.Store(&tenantReplicaHostsBox{hosts: hosts})
}

// StateHashes implements ReplicaStateHasher.
func (s *AdminService) StateHashes(ctx context.Context, shardIDs []uint64) (*StateHashesResponse, error) {
	if _, err := tenant.EffectiveTenant(ctx, tenantid.System, "read replica state hashes"); err != nil {
		return nil, err
	}
	want := shardFilter(shardIDs)
	replicas := hostStateHashes(ctx, "", s.host, want)
	if box := s.tenantReplicaHosts.Load(); box != nil {
		for tenantID, h := range box.hosts.StartedHosts() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			replicas = append(replicas, hostStateHashes(ctx, tenantID, h, want)...)
		}
	}
	sort.Slice(replicas, func(i, j int) bool {
		if replicas[i].TenantID != replicas[j].TenantID {
			return replicas[i].TenantID < replicas[j].TenantID
		}
		return replicas[i].ShardID < replicas[j].ShardID
	})
	return &StateHashesResponse{NodeID: s.host.ID(), Replicas: replicas}, nil
}

// shardFilter returns the set of selected shard IDs, nil for every shard.
func shardFilter(shardIDs []uint64) map[uint64]struct{} {
	if len(shardIDs) == 0 {
		return nil
	}
	want := make(map[uint64]struct{}, len(shardIDs))
	for _, id := range shardIDs {
		want[id] = struct{}{}
	}
	return want
}

// hostStateHashes hashes every replica of host whose shard want selects (nil
// selects all), labelling each entry with tenantID.
func hostStateHashes(ctx context.Context, tenantID string, host Host, want map[uint64]struct{}) []ReplicaStateHash {
	info := host.NodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	if info == nil {
		return nil
	}
	out := make([]ReplicaStateHash, 0, len(info.ShardInfoList))
	for _, si := range info.ShardInfoList {
		if want != nil {
			if _, ok := want[si.ShardID]; !ok {
				continue
			}
		}
		entry := ReplicaStateHash{TenantID: tenantID, ShardID: si.ShardID, ReplicaID: si.ReplicaID}
		d, err := replicaStateDigest(ctx, host, si.ShardID)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.AppliedIndex = d.AppliedIndex
			entry.Records = d.Records
			entry.Hash = d.Hex()
		}
		out = append(out, entry)
	}
	return out
}

// replicaStateDigest asks one local replica for its state digest.
func replicaStateDigest(ctx context.Context, host Host, shardID uint64) (statedigest.Digest, error) {
	result, err := host.StaleRead(ctx, shardID, statedigest.Query{})
	if err != nil {
		return statedigest.Digest{}, err
	}
	d, ok := result.(statedigest.Digest)
	if !ok {
		return statedigest.Digest{}, &StateDigestUnsupportedError{}
	}
	return d, nil
}
