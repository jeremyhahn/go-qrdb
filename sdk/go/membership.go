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
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconfigurerProbeTimeout bounds control-plane probes (leader/peer lookups)
// and best-effort lease revokes so they cannot block the membership watch loop.
const reconfigurerProbeTimeout = 10 * time.Second

// watchForwardBuffer sizes the membership-event forwarding channel so a
// momentarily slow consumer does not backpressure the watch stream.
const watchForwardBuffer = 64

// NewMembershipManager wires a cluster.MembershipManager onto this client's
// lease + watch + KV + admin surface. It is the canonical way for a daemon or
// embedder to enable opt-in dynamic Raft membership: the same in-process or
// network client that serves data also drives membership, so there is one
// lessor and one watch subsystem behind both the data plane and membership.
//
// Pass cluster.WithReconfigurer is not required — this constructor supplies a
// reconfigurer built from the client's Admin surface automatically. Additional
// cluster.MembershipOption values (e.g. WithMembershipLogger) are forwarded.
func (c *Client) NewMembershipManager(self cluster.Member, shardID uint64, cfg cluster.MembershipConfig, opts ...cluster.MembershipOption) (*cluster.MembershipManager, error) {
	store := c.NewMembershipStore()
	reconf := c.NewMembershipReconfigurer()
	opts = append([]cluster.MembershipOption{cluster.WithReconfigurer(reconf)}, opts...)
	return cluster.NewMembershipManager(store, self, shardID, cfg, opts...)
}

// NewNodeAdvertiser wires a cluster.Advertiser onto this client's lease + MVCC
// surface. The advertiser publishes the local node's descriptor — its Raft
// address and the API endpoints it serves — into the cluster registry and keeps
// the backing lease alive, so every peer can map a replica ID onto an address
// it can actually dial.
//
// It is the advertisement half of NewMembershipManager without the Raft
// reconfiguration half. Embedders that run a fixed-membership cluster (xkms
// among them) want their API endpoints discoverable but must not be forced to
// opt into automatic membership changes to get them.
//
// self.APIEndpoints must contain only known protocols with non-empty
// addresses; the values come from the node's own configuration and are never
// derived from the Raft address.
func (c *Client) NewNodeAdvertiser(self cluster.Member, cfg cluster.AdvertiseConfig, opts ...cluster.AdvertiseOption) (*cluster.Advertiser, error) {
	return cluster.NewAdvertiser(c.NewMembershipStore(), self, cfg, opts...)
}

// NewMembershipStore returns a cluster.MembershipStore backed by this client's
// lease, MVCC (put-with-lease), and watch surfaces. Member entries are stored
// under cluster.MembershipPrefix and attached to a lease so that node death
// triggers automatic expiry and a watch delete.
func (c *Client) NewMembershipStore() cluster.MembershipStore {
	return &sdkMembershipStore{client: c}
}

// NewMembershipReconfigurer returns a cluster.Reconfigurer backed by this
// client's Admin surface (AddMember / RemoveMember / shard state / peers).
func (c *Client) NewMembershipReconfigurer() cluster.Reconfigurer {
	return &sdkReconfigurer{client: c}
}

// sdkMembershipStore implements cluster.MembershipStore over the qrdb SDK.
type sdkMembershipStore struct {
	client *Client

	mu      sync.Mutex
	leaseID int64
	granted bool
}

// PutMember registers (first call) or refreshes (subsequent calls) the member
// entry. The first call grants a lease and writes the lease-attached entry;
// later calls keep the lease alive.
func (s *sdkMembershipStore) PutMember(ctx context.Context, nodeID uint64, data []byte, ttl time.Duration) error {
	// Read local state, then perform RPCs OUTSIDE the lock so a slow transport
	// does not serialize the store (and does not delay DeleteMember on
	// shutdown). Local state is committed under the lock afterward.
	s.mu.Lock()
	leaseID, granted := s.leaseID, s.granted
	s.mu.Unlock()

	if !granted {
		ttlSeconds := int64(ttl.Seconds())
		if ttlSeconds < 1 {
			ttlSeconds = 1
		}
		info, err := s.client.Lease().LeaseGrant(ctx, ttlSeconds)
		if err != nil {
			return err
		}
		if _, err := s.client.MVCC().PutWithLease(ctx, memberKey(nodeID), data, info.ID); err != nil {
			// Best-effort cleanup of the orphaned lease on a fresh context so
			// it runs even when the caller's context is already cancelled.
			revokeQuietly(s.client, info.ID)
			return err
		}
		s.mu.Lock()
		if !s.granted {
			s.leaseID = info.ID
			s.granted = true
			s.mu.Unlock()
			return nil
		}
		// A concurrent PutMember already granted; keep theirs, revoke ours.
		s.mu.Unlock()
		revokeQuietly(s.client, info.ID)
		return nil
	}

	if _, err := s.client.Lease().LeaseKeepAlive(ctx, leaseID); err != nil {
		// The lease is gone (expired during a GC pause / partition, or
		// revoked). Drop local state so the next refresh re-grants and
		// rewrites the member entry, restoring self-healing instead of
		// keepalive-ing a dead lease forever.
		s.mu.Lock()
		if s.leaseID == leaseID {
			s.granted = false
			s.leaseID = 0
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// DeleteMember revokes the member's lease, deleting the entry and notifying
// watchers. It clears local lease state so a second call (e.g. a double Close)
// is a no-op rather than revoking a stale lease ID.
func (s *sdkMembershipStore) DeleteMember(ctx context.Context, _ uint64) error {
	s.mu.Lock()
	id, granted := s.leaseID, s.granted
	s.granted = false
	s.leaseID = 0
	s.mu.Unlock()
	if !granted {
		return nil
	}
	return s.client.Lease().LeaseRevoke(ctx, id)
}

// ListMembers returns the current membership snapshot by scanning the
// membership prefix. The manager seeds itself from this on Start (and after a
// watch re-subscribe) so members that registered before this node began
// watching are visible — a keepalive refreshes a lease without re-emitting a
// PUT, so a watch-only view would miss them.
func (s *sdkMembershipStore) ListMembers(ctx context.Context) ([]cluster.Member, error) {
	pairs, err := s.client.KV().Scan(ctx, cluster.MembershipPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]cluster.Member, 0, len(pairs))
	for _, data := range pairs {
		var m cluster.Member
		if json.Unmarshal(data, &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// revokeQuietly revokes a lease on a short fresh context, ignoring errors.
func revokeQuietly(client *Client, leaseID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), reconfigurerProbeTimeout)
	defer cancel()
	_ = client.Lease().LeaseRevoke(ctx, leaseID)
}

// WatchMembers watches the membership prefix and translates qrdb watch events
// into cluster.MemberEvent values.
func (s *sdkMembershipStore) WatchMembers(ctx context.Context) (<-chan cluster.MemberEvent, error) {
	src, err := s.client.Watch().WatchPrefix(ctx, cluster.MembershipPrefix, transport.WatchOptions{PrevKV: true})
	if err != nil {
		return nil, err
	}
	// Buffer so a momentarily slow consumer does not block the forwarding
	// goroutine and backpressure the underlying watch stream.
	out := make(chan cluster.MemberEvent, watchForwardBuffer)
	go func() {
		defer close(out)
		for resp := range src {
			for _, ev := range resp.Events {
				event, ok := translateMemberEvent(ev)
				if !ok {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- event:
				}
			}
		}
	}()
	return out, nil
}

// translateMemberEvent converts a qrdb watch event into a cluster.MemberEvent.
func translateMemberEvent(ev *transport.WatchEvent) (cluster.MemberEvent, bool) {
	switch ev.Type {
	case transport.EventPut:
		if ev.Kv == nil {
			return cluster.MemberEvent{}, false
		}
		var m cluster.Member
		if err := json.Unmarshal(ev.Kv.Value, &m); err != nil {
			return cluster.MemberEvent{}, false
		}
		return cluster.MemberEvent{Type: cluster.MemberJoined, Member: m}, true
	case transport.EventDelete:
		if ev.PrevKv != nil {
			var m cluster.Member
			if err := json.Unmarshal(ev.PrevKv.Value, &m); err == nil {
				return cluster.MemberEvent{Type: cluster.MemberLeft, Member: m}, true
			}
		}
		key := ""
		if ev.Kv != nil {
			key = string(ev.Kv.Key)
		} else if ev.PrevKv != nil {
			key = string(ev.PrevKv.Key)
		}
		return cluster.MemberEvent{Type: cluster.MemberLeft, Member: cluster.Member{NodeID: nodeIDFromKey(key)}}, true
	default:
		return cluster.MemberEvent{}, false
	}
}

// memberKey builds the membership key for a node.
func memberKey(nodeID uint64) string {
	return cluster.MembershipPrefix + strconv.FormatUint(nodeID, 10)
}

// nodeIDFromKey parses the node ID out of a membership key. Returns 0 when the
// key does not parse.
func nodeIDFromKey(key string) uint64 {
	id, err := strconv.ParseUint(strings.TrimPrefix(key, cluster.MembershipPrefix), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// sdkReconfigurer implements cluster.Reconfigurer over the qrdb SDK Admin
// surface.
type sdkReconfigurer struct {
	client *Client
}

// AddMember adds the replica with the identity the cluster has already
// committed for its host: dynamic membership acts on nodes that registered
// themselves, so it has no operator-supplied identity to offer.
func (r *sdkReconfigurer) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	return r.client.Admin().AddMember(ctx, shardID, replicaID, raftAddr, "")
}

func (r *sdkReconfigurer) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	return r.client.Admin().RemoveMember(ctx, shardID, replicaID)
}

func (r *sdkReconfigurer) Leader(shardID uint64) (uint64, bool, error) {
	// Bounded context: the Reconfigurer interface is context-free, but these
	// probes run on the membership watch loop, so a hung control-plane RPC
	// must not block it indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), reconfigurerProbeTimeout)
	defer cancel()
	st, err := r.client.Admin().GetShardState(ctx, shardID)
	if err != nil {
		return 0, false, err
	}
	return st.LeaderID, st.HasLeader, nil
}

func (r *sdkReconfigurer) IsMember(shardID, replicaID uint64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reconfigurerProbeTimeout)
	defer cancel()
	resp, err := r.client.Admin().Peers(ctx)
	if err != nil {
		return false, err
	}
	for _, sp := range resp.Shards {
		if sp.ShardID != shardID {
			continue
		}
		for _, p := range sp.Peers {
			if p.ReplicaID == replicaID {
				return true, nil
			}
		}
	}
	return false, nil
}
