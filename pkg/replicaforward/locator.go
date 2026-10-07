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

package replicaforward

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// MemberSource lists the cluster's member records. The membership store every
// raft node publishes its record to implements it.
type MemberSource interface {
	ListMembers(ctx context.Context) ([]cluster.Member, error)
}

// Target is one node a request is forwarded to.
type Target struct {
	ReplicaID uint64
	RaftAddr  string
	// Endpoint is the target's API endpoint for the protocol forwarded over.
	Endpoint string
}

// Outcome classifies what a target did with a forwarded request.
type Outcome uint8

const (
	// Served means the target answered the request: whatever it answered --
	// success or an application error -- is the caller's answer.
	Served Outcome = iota

	// NotLeader means the target holds a replica but is not its group's
	// leader and did not serve the write. Result.LeaderRaftAddr names the
	// leader when the target knows it.
	NotLeader

	// NotAReplica means the target holds no replica either: its placement
	// changed in flight.
	NotAReplica

	// Unreachable means the request never reached the target: the
	// connection could not be opened. A failure after the request was sent
	// is an error from send, never Unreachable, so a write is never sent
	// twice.
	Unreachable
)

// Result is a target's classified answer.
type Result struct {
	Outcome        Outcome
	LeaderRaftAddr string
}

// SendFunc forwards the request to t over one transport and classifies the
// answer. It relays a Served answer to the caller itself. An error is a
// failure after the request may have reached t; it ends the forward without
// a retry.
type SendFunc func(ctx context.Context, t Target) (Result, error)

// Locator resolves forward targets from the placement refusal, the member
// records and the leader cache, and runs the one bounded retry.
type Locator struct {
	members MemberSource
	cache   *LeaderCache
	self    string
	logger  *slog.Logger
}

// NewLocator returns a Locator. selfRaftAddr is this node's Raft address; a
// target at it is never chosen. A nil logger is slog.Default().
func NewLocator(members MemberSource, cache *LeaderCache, selfRaftAddr string, logger *slog.Logger) *Locator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Locator{members: members, cache: cache, self: selfRaftAddr, logger: logger}
}

// Cache returns the leader cache the Locator reads and updates.
func (l *Locator) Cache() *LeaderCache { return l.cache }

// MemberByAddress resolves the member registered at raftAddr: a single
// address lookup, independent of placement and any replica set. The shared
// system shard has no replica set to resolve through, so a leader-only
// write's refusal (service.AdminCredentialNotLeaderError and its siblings)
// names just the leader's Raft address; this is how a caller turns that
// address into the leader's per-protocol endpoint, reading the SAME member
// records every per-database forward already reads.
func (l *Locator) MemberByAddress(ctx context.Context, raftAddr string) (cluster.Member, bool, error) {
	v, err := l.view(ctx)
	if err != nil {
		return cluster.Member{}, false, err
	}
	m, ok := v.byAddr[raftAddr]
	return m, ok, nil
}

// maxAnswers is how many targets a forward may reach: the first, and the
// one retry a refusal earns.
const maxAnswers = 2

// Forward forwards a refused request over proto: first to the cached leader
// or, on a miss, the placement's leader hint resolved through the member
// records; then, after a refusal, at most once more, to the leader a
// NotLeader names or the next replica in placement order. A replica that is
// unreachable -- the request never left this node -- does not spend the
// retry; it is skipped for the next replica, each replica at most once. It
// returns the target that served, or *NoTargetServedError when none did --
// the caller then answers with the original refusal -- or send's error.
//
// Every forward is counted in qrdb_replica_forward_total and timed in
// qrdb_replica_forward_duration_seconds, by protocol.
func (l *Locator) Forward(ctx context.Context, refusal *placement.NotAReplicaError, proto cluster.APIProtocol, send SendFunc) (Target, error) {
	start := time.Now()
	sends := 0
	t, err := l.forward(ctx, refusal, proto, func(ctx context.Context, t Target) (Result, error) {
		sends++
		return send(ctx, t)
	})
	outcome := outcomeError
	var none *NoTargetServedError
	switch {
	case err == nil && sends == 1:
		outcome = outcomeOK
	case err == nil:
		outcome = outcomeRetriedOK
	case errors.As(err, &none):
		outcome = outcomeRefused
	}
	observe(proto, outcome, start)
	return t, err
}

// forward is Forward without its accounting.
func (l *Locator) forward(ctx context.Context, refusal *placement.NotAReplicaError, proto cluster.APIProtocol, send SendFunc) (Target, error) {
	var view *memberView
	next, ok := l.cached(refusal, proto)
	if !ok {
		v, err := l.view(ctx)
		if err != nil {
			return Target{}, err
		}
		view = v
		if next, ok = view.nextTarget(refusal, proto, l.self, nil, Result{}); !ok {
			return Target{}, l.noTarget(refusal, proto)
		}
	}

	tried := map[string]bool{}
	answers := 0
	for {
		tried[next.RaftAddr] = true
		res, err := send(ctx, next)
		if err != nil {
			l.cache.Evict(refusal.ShardID)
			return Target{}, err
		}
		if res.Outcome == Served {
			l.remember(refusal, next, proto, view)
			l.logger.Debug("replicaforward: forwarded", "tenant", refusal.TenantID, "database", refusal.Database,
				"protocol", proto, "target", next.RaftAddr, "attempt", len(tried))
			return next, nil
		}
		l.cache.Evict(refusal.ShardID)
		if res.Outcome != Unreachable {
			if answers++; answers == maxAnswers {
				return Target{}, l.noTarget(refusal, proto)
			}
		}
		if view == nil {
			v, verr := l.view(ctx)
			if verr != nil {
				return Target{}, verr
			}
			view = v
		}
		if next, ok = view.nextTarget(refusal, proto, l.self, tried, res); !ok {
			return Target{}, l.noTarget(refusal, proto)
		}
	}
}

// noTarget logs and returns the forward's failure to find a replica that
// served.
func (l *Locator) noTarget(refusal *placement.NotAReplicaError, proto cluster.APIProtocol) error {
	l.logger.Warn("replicaforward: no target", "tenant", refusal.TenantID, "database", refusal.Database, "protocol", proto)
	return &NoTargetServedError{TenantID: refusal.TenantID, Database: refusal.Database, Protocol: proto}
}

// cached returns the shard's cached leader as a target for proto.
func (l *Locator) cached(refusal *placement.NotAReplicaError, proto cluster.APIProtocol) (Target, bool) {
	c, ok := l.cache.Get(refusal.ShardID)
	if !ok || c.RaftAddr == l.self {
		return Target{}, false
	}
	ep, ok := c.Endpoints[proto]
	if !ok || ep == "" {
		return Target{}, false
	}
	return Target{ReplicaID: c.ReplicaID, RaftAddr: c.RaftAddr, Endpoint: ep}, true
}

// remember caches t as the shard's leader, with its member endpoints.
func (l *Locator) remember(refusal *placement.NotAReplicaError, t Target, proto cluster.APIProtocol, view *memberView) {
	endpoints := map[cluster.APIProtocol]string{proto: t.Endpoint}
	if view != nil {
		if m, ok := view.byAddr[t.RaftAddr]; ok && len(m.APIEndpoints) > 0 {
			endpoints = m.APIEndpoints
		}
	} else if c, ok := l.cache.Get(refusal.ShardID); ok && c.RaftAddr == t.RaftAddr {
		endpoints = c.Endpoints
	}
	l.cache.Put(refusal.ShardID, CachedLeader{
		TenantID: refusal.TenantID, Database: refusal.Database,
		ReplicaID: t.ReplicaID, RaftAddr: t.RaftAddr, Endpoints: endpoints,
	})
}

// view reads the member records once.
func (l *Locator) view(ctx context.Context) (*memberView, error) {
	members, err := l.members.ListMembers(ctx)
	if err != nil {
		return nil, &MemberLookupError{Err: err}
	}
	v := &memberView{byAddr: make(map[string]cluster.Member, len(members))}
	for _, m := range members {
		v.byAddr[m.Address] = m
	}
	return v, nil
}

// memberView indexes member records by Raft address.
type memberView struct {
	byAddr map[string]cluster.Member
}

// target returns the replica at raftAddr as a target for proto.
func (v *memberView) target(replicaID uint64, raftAddr string, proto cluster.APIProtocol) (Target, bool) {
	m, ok := v.byAddr[raftAddr]
	if !ok {
		return Target{}, false
	}
	ep, ok := m.APIEndpoint(proto)
	if !ok {
		return Target{}, false
	}
	return Target{ReplicaID: replicaID, RaftAddr: raftAddr, Endpoint: ep}, true
}

// candidates returns the refusal's replicas as targets for proto, the leader
// hint first and the rest in placement order, skipping self and any replica
// whose member does not advertise proto.
func (v *memberView) candidates(refusal *placement.NotAReplicaError, proto cluster.APIProtocol, self string) []Target {
	var out []Target
	add := func(r placement.Replica) {
		if r.RaftAddr == self {
			return
		}
		if t, ok := v.target(r.ReplicaID, r.RaftAddr, proto); ok {
			out = append(out, t)
		}
	}
	for _, r := range refusal.Replicas {
		if r.ReplicaID == refusal.LeaderReplicaID {
			add(r)
		}
	}
	for _, r := range refusal.Replicas {
		if r.ReplicaID != refusal.LeaderReplicaID {
			add(r)
		}
	}
	return out
}

// nextTarget chooses the next target after the last one answered res: the
// leader a NotLeader names when its member is known and it was not tried,
// otherwise the first candidate not yet tried -- the leader hint first.
func (v *memberView) nextTarget(refusal *placement.NotAReplicaError, proto cluster.APIProtocol, self string, tried map[string]bool, res Result) (Target, bool) {
	if res.Outcome == NotLeader && res.LeaderRaftAddr != "" && res.LeaderRaftAddr != self && !tried[res.LeaderRaftAddr] {
		replicaID := uint64(0)
		for _, r := range refusal.Replicas {
			if r.RaftAddr == res.LeaderRaftAddr {
				replicaID = r.ReplicaID
			}
		}
		if t, ok := v.target(replicaID, res.LeaderRaftAddr, proto); ok {
			return t, true
		}
	}
	for _, c := range v.candidates(refusal, proto, self) {
		if !tried[c.RaftAddr] {
			return c, true
		}
	}
	return Target{}, false
}
