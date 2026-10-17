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

package sdk

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// APIProtocol names one of the client-facing API protocols a replica serves.
// Re-exported from pkg/cluster so SDK consumers never import pkg/*.
type APIProtocol = cluster.APIProtocol

// The remotely dialable API protocols a replica may advertise. Unix domain
// sockets are deliberately excluded: a socket path is only meaningful on the
// node that owns it.
const (
	// APIProtocolREST is the HTTP/REST API.
	APIProtocolREST = cluster.APIProtocolREST

	// APIProtocolGRPC is the gRPC API.
	APIProtocolGRPC = cluster.APIProtocolGRPC

	// APIProtocolQUIC is the native binary QUIC API.
	APIProtocolQUIC = cluster.APIProtocolQUIC

	// APIProtocolMCP is the Model Context Protocol API.
	APIProtocolMCP = cluster.APIProtocolMCP
)

// APIProtocols lists every remotely dialable protocol a member may advertise.
var APIProtocols = cluster.APIProtocols

// ParseAPIProtocol converts a wire name into an APIProtocol.
var ParseAPIProtocol = cluster.ParseAPIProtocol

// ---------------------------------------------------------------------------
// Node registry re-exports
//
// A ClusterNode is what a replica publishes about itself into the cluster
// registry; a Member (below) is what the Raft configuration says about a
// replica, enriched with that registration. Re-exported so SDK consumers never
// import pkg/*.
// ---------------------------------------------------------------------------

// ClusterNode is a node's self-published registry entry: its Raft address plus
// the API endpoints it serves.
type ClusterNode = cluster.Member

// NodeAdvertiser publishes a ClusterNode into the registry and keeps its lease
// alive. Build one with Client.NewNodeAdvertiser.
type NodeAdvertiser = cluster.Advertiser

// AdvertiseConfig tunes a NodeAdvertiser's lease TTL and refresh cadence.
type AdvertiseConfig = cluster.AdvertiseConfig

// AdvertiseOption customizes a NodeAdvertiser.
type AdvertiseOption = cluster.AdvertiseOption

// WithAdvertiseLogger sets a NodeAdvertiser's logger.
var WithAdvertiseLogger = cluster.WithAdvertiseLogger

// ValidateAPIEndpoints rejects an endpoint map with an unknown protocol or an
// empty address.
var ValidateAPIEndpoints = cluster.ValidateAPIEndpoints

// BuildAPIEndpoints assembles the endpoint map a node advertises, from that
// node's own advertise host and its configured per-protocol ports. A protocol
// whose port is absent or non-positive is omitted, which is how a node states
// that it does not serve it, and a wildcard bind host yields a nil map rather
// than an address no peer can dial.
//
// Ports come from configuration only — never from arithmetic on the Raft
// address — so NAT, split networking and non-default ports all work. It is
// re-exported so an embedding process that serves its own APIs in front of an
// embedded node (go-xkms among them) advertises through exactly the same code
// path the qrdbd daemon uses, instead of hand-rolling a second one.
var BuildAPIEndpoints = cluster.BuildAPIEndpoints

// AdvertisableHost reports whether host is a concrete address peers can dial.
// A wildcard bind host ("", "0.0.0.0", "::") is not.
var AdvertisableHost = cluster.AdvertisableHost

// HostFromAddress extracts the host portion of a "host:port" address. It is the
// documented default advertise host: a node's Raft address is by definition an
// address peers already use to reach it. It returns "" when addr has no
// host:port shape or the host is a wildcard.
var HostFromAddress = cluster.HostFromAddress

// UnknownAPIProtocolError is returned for a protocol outside APIProtocols.
type UnknownAPIProtocolError = cluster.UnknownAPIProtocolError

// EmptyAPIEndpointError is returned when a known protocol is advertised with an
// empty address.
type EmptyAPIEndpointError = cluster.EmptyAPIEndpointError

// DefaultMembershipPollInterval is the polling cadence WatchMembership uses
// to detect shard membership and leadership changes. Polling keeps the
// facade transport-agnostic: it works over every transport, including the
// unary-only REST transport, without a dedicated streaming RPC.
const DefaultMembershipPollInterval = time.Second

// ClusterClient is a thin facade over the control-plane AdminClient for
// cluster membership and leadership operations. It adds no new wire RPCs:
// AddReplica/RemoveReplica delegate to the existing AddMember/RemoveMember,
// Members/Leader read from Peers()/GetShardState(), and WatchMembership
// polls those same calls and emits deltas.
type ClusterClient struct {
	admin        transport.AdminClient
	registry     cluster.MembershipStore
	pollInterval time.Duration
}

// Cluster returns a ClusterClient for shard membership and leadership
// operations. It requires a transport that implements AdminClient.
func (c *Client) Cluster() *ClusterClient {
	return &ClusterClient{
		admin:        capability[transport.AdminClient](c, "Cluster"),
		registry:     c.NewMembershipStore(),
		pollInterval: DefaultMembershipPollInterval,
	}
}

// WithPollInterval overrides the WatchMembership polling cadence. A larger
// interval reduces control-plane load and allocation churn at the cost of
// slower change detection. Non-positive values are ignored. Returns the
// receiver for chaining.
func (cc *ClusterClient) WithPollInterval(d time.Duration) *ClusterClient {
	if d > 0 {
		cc.pollInterval = d
	}
	return cc
}

// Member describes a single Raft replica within a shard.
type Member struct {
	// ReplicaID is the replica's identifier within the shard.
	ReplicaID uint64

	// Address is the replica's Raft network address.
	Address string

	// APIEndpoints maps each client-facing API protocol the replica serves to
	// the address clients should dial for it. It lets a consumer that has
	// located the leader turn a replica ID into a reachable API address, which
	// the Raft address alone can never do.
	//
	// The map is nil when the replica advertises nothing: it predates API
	// advertisement, its operator configured no advertise address, or the
	// cluster does not run a node advertiser. Consumers must treat an absent
	// endpoint as "unknown" and degrade, not as an error.
	APIEndpoints map[APIProtocol]string

	// IsLeader is true when this replica is the shard's current leader.
	IsLeader bool
}

// APIEndpoint returns the replica's advertised address for protocol p and
// whether it advertises that protocol at all.
func (m Member) APIEndpoint(p APIProtocol) (string, bool) {
	addr, ok := m.APIEndpoints[p]
	if !ok || addr == "" {
		return "", false
	}
	return addr, true
}

// MembershipEvent is emitted by WatchMembership when a shard's membership or
// leadership changes. It carries the full current membership plus the delta
// since the previous event.
type MembershipEvent struct {
	// ShardID is the shard the event pertains to.
	ShardID uint64

	// Members is the full current membership snapshot.
	Members []Member

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// Added lists replicas present now but not in the prior snapshot.
	Added []Member

	// Removed lists replicas present in the prior snapshot but not now.
	Removed []Member

	// LeaderChanged is true when the leader differs from the prior snapshot.
	LeaderChanged bool
}

// Members returns the current membership of the given shard. It filters the
// node's Peers() view to the requested shard and enriches each replica with the
// API endpoints that replica advertised into the cluster registry.
//
// The two views answer different questions and neither substitutes for the
// other: Raft knows which replicas exist and where they speak Raft, while the
// registry — written by each node from its own configuration — knows where each
// replica serves its client APIs. The join is keyed by replica ID.
//
// Enrichment is fail-soft by design. A registry read error, a cluster that runs
// no node advertiser, or a replica that advertises nothing all yield members
// with a nil APIEndpoints map, which is exactly the pre-advertisement result.
// Membership listing must never fail because endpoint discovery did.
func (cc *ClusterClient) Members(ctx context.Context, shardID uint64) ([]Member, error) {
	resp, err := cc.admin.Peers(ctx)
	if err != nil {
		return nil, err
	}
	for _, sp := range resp.Shards {
		if sp.ShardID != shardID {
			continue
		}
		endpoints := cc.advertisedEndpoints(ctx)
		members := make([]Member, len(sp.Peers))
		for i, p := range sp.Peers {
			members[i] = Member{
				ReplicaID:    p.ReplicaID,
				Address:      p.Address,
				APIEndpoints: endpoints[p.ReplicaID],
				IsLeader:     p.IsLeader,
			}
		}
		return members, nil
	}
	return nil, &ShardNotFoundError{ShardID: shardID}
}

// advertisedEndpoints reads the cluster registry and indexes each member's
// advertised API endpoints by replica ID. It returns an empty map — never an
// error — when the registry is unavailable, so callers degrade to the
// Raft-only view instead of failing.
func (cc *ClusterClient) advertisedEndpoints(ctx context.Context) map[uint64]map[APIProtocol]string {
	if cc.registry == nil {
		return nil
	}
	registered, err := cc.registry.ListMembers(ctx)
	if err != nil {
		return nil
	}
	out := make(map[uint64]map[APIProtocol]string, len(registered))
	for _, m := range registered {
		if len(m.APIEndpoints) == 0 {
			continue
		}
		out[m.NodeID] = m.APIEndpoints
	}
	return out
}

// LeaderEndpoint returns the address to dial to reach the shard's current
// leader over protocol p. It is the one call a consumer needs to turn "this
// write must go to the leader" into "send it here".
//
// It returns a *NoLeaderError when the shard has no elected leader and a
// *LeaderEndpointUnknownError when the leader exists but advertises no address
// for p. Both are typed so a caller can distinguish "wait for an election" from
// "fall back to returning a redirect the client resolves itself".
func (cc *ClusterClient) LeaderEndpoint(ctx context.Context, shardID uint64, p APIProtocol) (string, error) {
	if !p.Valid() {
		return "", &cluster.UnknownAPIProtocolError{Name: string(p)}
	}
	members, err := cc.Members(ctx, shardID)
	if err != nil {
		return "", err
	}
	for _, m := range members {
		if !m.IsLeader {
			continue
		}
		addr, ok := m.APIEndpoint(p)
		if !ok {
			return "", &LeaderEndpointUnknownError{ShardID: shardID, ReplicaID: m.ReplicaID, Protocol: p}
		}
		return addr, nil
	}
	return "", &NoLeaderError{ShardID: shardID}
}

// Leader returns the current leader replica ID for the given shard and
// whether a leader is currently known.
func (cc *ClusterClient) Leader(ctx context.Context, shardID uint64) (replicaID uint64, hasLeader bool, err error) {
	st, err := cc.admin.GetShardState(ctx, shardID)
	if err != nil {
		return 0, false, err
	}
	return st.LeaderID, st.HasLeader, nil
}

// AddReplica adds a replica to the shard. It delegates to the already-wired
// AddMember control-plane operation. identity is the replica's Raft member
// identity, or "" to use the one the cluster already committed for it; see
// transport.AdminClient.AddMember.
func (cc *ClusterClient) AddReplica(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	return cc.admin.AddMember(ctx, shardID, replicaID, raftAddr, identity)
}

// RemoveReplica removes a replica from the shard. It delegates to the
// already-wired RemoveMember control-plane operation.
func (cc *ClusterClient) RemoveReplica(ctx context.Context, shardID, replicaID uint64) error {
	return cc.admin.RemoveMember(ctx, shardID, replicaID)
}

// PromoteReplica promotes a committed observer of the shard to a voter. It
// delegates to the already-wired PromoteMember control-plane operation.
func (cc *ClusterClient) PromoteReplica(ctx context.Context, shardID, replicaID uint64) error {
	return cc.admin.PromoteMember(ctx, shardID, replicaID)
}

// TransferLeadership asks the node this client talks to, which must lead the
// shard, to hand leadership to replicaID, a current voter, and returns once
// replicaID leads. A replica that does not take over within the bound -- one
// that is stopped, say -- fails with *LeaderTransferTimeoutError on every
// transport. Over the embedded transport the other refusals are typed too: an
// unknown shard or replica is *UnknownShardError or *UnknownReplicaError; an
// observer, witness or removed replica is *LeaderTransferTargetError; and a
// node that does not lead the shard is *transport.NotLeaderError naming the
// one that does. Over a wire transport the serving node forwards the call to
// the shard's leader, so any node will do, and those other refusals arrive as
// the transport's error for their class (not found, failed precondition).
//
// It delegates to transport.LeaderTransferrer, which every transport
// implements. A wire transport carries the timeout as a *transport.RequestError
// whose Reason is LEADER_TRANSFER_TIMEOUT; this method rebuilds the
// *LeaderTransferTimeoutError from its metadata, so errors.As matches on every
// transport alike. A transport without the capability answers
// *transport.NotImplementedError.
func (cc *ClusterClient) TransferLeadership(ctx context.Context, shardID, replicaID uint64) error {
	transferrer, ok := cc.admin.(transport.LeaderTransferrer)
	if !ok {
		return &transport.NotImplementedError{Method: "TransferLeadership"}
	}
	return leaderTransferError(transferrer.TransferLeadership(ctx, shardID, replicaID))
}

// leaderTransferError rebuilds the typed *LeaderTransferTimeoutError from a
// wire transport's LEADER_TRANSFER_TIMEOUT refusal and returns every other
// error unchanged. A refusal whose metadata does not decode is returned as
// sent: the caller still sees the server's message and status, and nothing
// is invented in place of fields the server did not send.
func leaderTransferError(err error) error {
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.Reason != replicaforward.ReasonLeaderTransferTimeout {
		return err
	}
	timeout, parseErr := replicaforward.ParseLeaderTransferTimeout(reqErr.Metadata)
	if parseErr != nil {
		return err
	}
	return timeout
}

// WatchMembership returns a channel that emits a MembershipEvent whenever the
// shard's membership or leadership changes. The first event delivers the
// initial snapshot. The watcher polls at DefaultMembershipPollInterval and
// stops when ctx is cancelled, at which point the channel is closed.
//
// Polling (rather than a streaming RPC) keeps this method usable over every
// transport, including the unary-only REST transport.
func (cc *ClusterClient) WatchMembership(ctx context.Context, shardID uint64) (<-chan MembershipEvent, error) {
	// Validate the shard exists before starting the watcher so callers get
	// an immediate error for an unknown shard rather than a silent channel.
	initial, err := cc.Members(ctx, shardID)
	if err != nil {
		return nil, err
	}

	out := make(chan MembershipEvent, 1)
	prev := membershipSet(initial)
	prevLeader := leaderOf(initial)

	out <- MembershipEvent{
		ShardID:  shardID,
		Members:  initial,
		LeaderID: prevLeader,
		Added:    initial,
	}

	go func() {
		defer close(out)
		ticker := time.NewTicker(cc.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			current, err := cc.Members(ctx, shardID)
			if err != nil {
				// A transient read error (or a shard that briefly drops out)
				// is skipped; the next tick retries. Cancellation exits above.
				continue
			}

			curSet := membershipSet(current)
			curLeader := leaderOf(current)
			added := diffMembers(current, prev)
			removed := diffMembers(prevSlice(prev), curSet)
			leaderChanged := curLeader != prevLeader

			if len(added) == 0 && len(removed) == 0 && !leaderChanged {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case out <- MembershipEvent{
				ShardID:       shardID,
				Members:       current,
				LeaderID:      curLeader,
				Added:         added,
				Removed:       removed,
				LeaderChanged: leaderChanged,
			}:
			}

			prev = curSet
			prevLeader = curLeader
		}
	}()

	return out, nil
}

// membershipSet indexes members by replica ID.
func membershipSet(members []Member) map[uint64]Member {
	set := make(map[uint64]Member, len(members))
	for _, m := range members {
		set[m.ReplicaID] = m
	}
	return set
}

// prevSlice materializes a member set back into a slice for diffing.
func prevSlice(set map[uint64]Member) []Member {
	members := make([]Member, 0, len(set))
	for _, m := range set {
		members = append(members, m)
	}
	return members
}

// diffMembers returns members in the slice that are absent from the set.
func diffMembers(members []Member, set map[uint64]Member) []Member {
	var diff []Member
	for _, m := range members {
		if _, ok := set[m.ReplicaID]; !ok {
			diff = append(diff, m)
		}
	}
	return diff
}

// leaderOf returns the replica ID of the leader in members, or 0 if none.
func leaderOf(members []Member) uint64 {
	for _, m := range members {
		if m.IsLeader {
			return m.ReplicaID
		}
	}
	return 0
}
