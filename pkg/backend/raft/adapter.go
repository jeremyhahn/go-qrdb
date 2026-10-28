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

// Package raft provides the HostAdapter that wraps a *quicraft.Host to
// satisfy the service.Host interface. It adds write-mode dispatch,
// consistency-level reads, and shard-state queries that the bare
// quicraft.Host does not expose.
//
// Embedders (go-quicbaas, qrdbd, sdk/go/node) create a HostAdapter once
// and pass it to lifecycle.BuildServices as the Infra.Host, then to
// server.New via server.WithHost.
package raft

import (
	"context"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// QuicraftHost is the subset of *quicraft.Host methods that HostAdapter
// delegates to. Extracting this interface enables unit testing without
// a running Raft cluster.
type QuicraftHost interface {
	SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
	SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error)
	// ShardMembership returns the shard membership from local in-memory state
	// without a linearizable ReadIndex round-trip. Suitable for health checks.
	ShardMembership(shardID uint64) (*quicraft.Membership, error)
	RequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error)
	// IsShardJoining reports whether shardID is still in its join bootstrap
	// window: started locally with join=true and not yet given its first
	// snapshot. Used by the placement reconciler (JoinObserver) to promote a
	// dynamically added replica from ReplicaStateObserver to
	// ReplicaStateObserverReady once it has caught up.
	IsShardJoining(shardID uint64) bool
	// LeaderID returns the shard's current leader replica ID and whether
	// a leader is known. Read by the leader wait (see throughElection).
	LeaderID(shardID uint64) (uint64, bool, error)
}

// HostAdapter wraps a QuicraftHost to satisfy the service.Host interface.
// It adds write-mode dispatch, consistency-level reads, and shard state
// queries that the bare quicraft.Host does not provide.
//
// In production, inner is *quicraft.Host which is embedded to promote
// all remaining Host methods (ID, RaftAddress, ShardCount, etc.).
type HostAdapter struct {
	*quicraft.Host
	inner QuicraftHost
	gate  *featuregate.Gate
	// leaderWait bounds how long a request holds while this node knows no
	// leader for its shard, and a read while the leader has not yet committed
	// in its term (see throughElection and readThroughLeaderChange).
	leaderWait time.Duration
}

// Option configures a HostAdapter.
type Option func(*HostAdapter)

// WithFeatureGate makes the adapter answer FeatureEnabled from gate. Every
// adapter over a host with a transport -- one whose groups can hold other
// nodes, and so other binaries -- needs one; see FeatureEnabled. A nil gate
// is the host with no transport NewFeatureGate returns it for, and leaves
// the adapter ungated.
func WithFeatureGate(gate *featuregate.Gate) Option {
	return func(a *HostAdapter) { a.gate = gate }
}

// NewFeatureGate returns the feature gate for the groups of host, whose
// system-shard replica is replicaID on systemShardID -- or nil when host has
// no transport: its groups hold only this process, so every replica runs
// this binary and needs no gate.
func NewFeatureGate(host *quicraft.Host, replicaID, systemShardID uint64) (*featuregate.Gate, error) {
	if host.NodeIdentity() == "" {
		return nil, nil
	}
	return featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: replicaID},
		SystemShardID: systemShardID,
		Members:       featuregate.HostMembership(host),
	})
}

// New creates a HostAdapter backed by the given quicraft.Host.
func New(h *quicraft.Host, opts ...Option) *HostAdapter {
	a := &HostAdapter{Host: h, inner: h, leaderWait: defaultLeaderWaitTimeout}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// HealthCheck reports the host's health, degraded when the feature gate has
// found that the cluster activated a feature this binary lacks: the node
// cannot take part in the cluster, and a composer that cannot refuse to
// start (an embedded node) surfaces that here, on every transport's health
// check, rather than only in its log.
func (a *HostAdapter) HealthCheck() *quicraft.HealthStatus {
	return a.withGateVerdict(a.Host.HealthCheck())
}

// withGateVerdict degrades an "ok" verdict when the feature gate has refused
// this binary; a worse verdict is never masked.
func (a *HostAdapter) withGateVerdict(hs *quicraft.HealthStatus) *quicraft.HealthStatus {
	if a.gate == nil || a.gate.Refusal() == nil || hs.Status != healthOK {
		return hs
	}
	degraded := *hs
	degraded.Status = healthDegraded
	return &degraded
}

// Health verdicts HealthCheck reads and reports (see quicraft.HealthStatus).
const (
	healthOK       = "ok"
	healthDegraded = "degraded"
)

// AdmitReplica refuses, with *featuregate.UnsupportedReplicaError, to add a
// replica of shardID on the node at raftAddr when that node does not
// advertise every feature the group uses (see featuregate.Gate.AdmitReplica).
// Without a gate no feature is ever enabled, so every node is admitted.
func (a *HostAdapter) AdmitReplica(shardID uint64, raftAddr string) error {
	if a.gate == nil {
		return nil
	}
	return a.gate.AdmitReplica(shardID, raftAddr)
}

// FeatureEnabled reports whether every replica of shardID's group can apply
// commands that use feature. With a feature gate the gate decides. Without
// one, a host with no transport -- every replica is this process -- and a
// test adapter with no host are enabled, while a host with a transport is
// not: its groups can hold nodes running an older binary, and without a
// gate nothing establishes that they do not.
func (a *HostAdapter) FeatureEnabled(shardID uint64, feature feature.Feature) bool {
	if a.gate != nil {
		return a.gate.Enabled(shardID, feature)
	}
	return a.Host == nil || a.Host.NodeIdentity() == ""
}

// NewWithInner creates a HostAdapter backed by any QuicraftHost. Useful
// for testing: pass a mock instead of a real *quicraft.Host. The embedded
// *quicraft.Host is set to nil; callers must not invoke methods promoted
// from the nil host (e.g. Close, LeaderID) without providing a
// concrete *quicraft.Host.
func NewWithInner(host *quicraft.Host, inner QuicraftHost) *HostAdapter {
	return &HostAdapter{Host: host, inner: inner, leaderWait: defaultLeaderWaitTimeout}
}

// SyncPropose proposes cmd through Raft consensus and waits for it to be
// applied, holding through a moment in which this node knows no leader for
// shardID rather than refusing at once (see throughElection).
func (a *HostAdapter) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	return throughElection(ctx, a.inner, shardID, a.leaderWait, func() (sm.Result, error) {
		return a.inner.SyncPropose(ctx, shardID, cmd)
	})
}

// ProposeWithMode proposes a write with the specified durability guarantee.
// Sync mode uses full Raft consensus. WALDurable and Batched modes
// currently fall through to Sync; a batch.Aggregator can be composed
// separately for true batched writes.
func (a *HostAdapter) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
	switch mode {
	case writemode.Sync:
		return a.SyncPropose(ctx, shardID, cmd)
	case writemode.WALDurable, writemode.Batched:
		// WALDurable and Batched require additional infrastructure
		// (proposer adapter, batch aggregator). For now, fall through
		// to synchronous consensus to guarantee correctness.
		return a.SyncPropose(ctx, shardID, cmd)
	default:
		return a.SyncPropose(ctx, shardID, cmd)
	}
}

// StaleRead reads from the local state machine without Raft coordination.
func (a *HostAdapter) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return a.inner.StaleRead(ctx, shardID, query)
}

// SyncRead performs a linearizable read against the state machine, holding
// through a moment in which this node knows no leader to confirm the read
// index with or the leader has not yet committed in its term, and reissuing
// a read a leadership change stranded (see readThroughLeaderChange).
func (a *HostAdapter) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return readThroughLeaderChange(ctx, a.inner, shardID, a.leaderWait, func() (interface{}, error) {
		return a.inner.SyncRead(ctx, shardID, query)
	})
}

// FollowerRead reads from the local state machine after ensuring it has
// applied up to the committed index. This allows any node (leader or
// follower) to serve reads without a ReadIndex round-trip to the leader,
// providing stronger guarantees than StaleRead with lower latency than
// SyncRead on followers. Like SyncRead it holds through a moment in which
// this node knows no leader or the leader has not yet committed in its term,
// and reissues a read a leadership change stranded (see
// readThroughLeaderChange).
func (a *HostAdapter) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return readThroughLeaderChange(ctx, a.inner, shardID, a.leaderWait, func() (interface{}, error) {
		return a.inner.FollowerRead(ctx, shardID, query)
	})
}

// ReadWithConsistency routes a read to the appropriate quicraft method
// based on the requested consistency level and AllowStale flag.
//
// An explicit, unambiguous consistency level takes precedence over the
// AllowStale hint, so a caller that explicitly requests Linearizable/LeaseRead/
// FollowerRead is never silently downgraded to a stale read by a stray
// AllowStale flag:
//
//   - Linearizable, LeaseRead: SyncRead — linearizable via ReadIndex.
//   - FollowerRead: ReadIndex on any node (leader or follower).
//   - Eventual: StaleRead — local read without coordination.
//   - otherwise: AllowStale → StaleRead; else SyncRead.
//
// Strong is the zero value of consistency.Level and cannot be distinguished
// from an unset level, so it falls through to the AllowStale check (an explicit
// AllowStale still yields a stale read). Transports map an AllowStale-only read
// to Eventual before it reaches here, so the wire path never produces an
// explicit-Strong + AllowStale combination.
func (a *HostAdapter) ReadWithConsistency(ctx context.Context, shardID uint64, query interface{}, opts consistency.ReadOptions) (interface{}, error) {
	switch opts.Consistency {
	case consistency.Linearizable, consistency.LeaseRead:
		return a.SyncRead(ctx, shardID, query)
	case consistency.FollowerRead:
		return a.FollowerRead(ctx, shardID, query)
	case consistency.Eventual:
		return a.inner.StaleRead(ctx, shardID, query)
	}
	if opts.AllowStale {
		return a.inner.StaleRead(ctx, shardID, query)
	}
	return a.SyncRead(ctx, shardID, query)
}

// RequestSnapshot requests an asynchronous snapshot for the specified shard.
func (a *HostAdapter) RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error) {
	return a.inner.RequestSnapshot(ctx, shardID, quicraft.SnapshotOption{})
}

// GetShardRaftState returns the config change index and current term for
// a shard. Uses the non-linearizable ShardMembership to avoid blocking
// on a ReadIndex round-trip — callers like the health-check handler only
// need to confirm the shard is loaded and has a known leader, not a
// strongly consistent membership view.
func (a *HostAdapter) GetShardRaftState(shardID uint64) (configChangeIndex uint64, term uint64, err error) {
	membership, err := a.inner.ShardMembership(shardID)
	if err != nil {
		return 0, 0, err
	}
	return membership.ConfigChangeID, 0, nil
}

// SyncGetShardMembership returns the shard's committed membership through
// inner, so a test double answers it too. The read confirms a read index
// with the leader, so it is held exactly as SyncRead is: through a moment in
// which this node knows no leader -- the election a membership change that
// removed the leader starts -- through the new leader's first commit in its
// term, and reissued when a leadership change strands it (see
// readThroughLeaderChange). A membership change that follows a leader
// removing itself reads membership while the remaining replicas elect, and an
// admin write resolving a member's identity reads it; neither may fail its
// caller during a normal election.
func (a *HostAdapter) SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error) {
	return readThroughLeaderChange(ctx, a.inner, shardID, a.leaderWait, func() (*quicraft.Membership, error) {
		return a.inner.SyncGetShardMembership(ctx, shardID)
	})
}

// NodeIdentity returns the host's own Raft member identity, or "" when no
// concrete host backs the adapter (a test double) or it has no transport.
func (a *HostAdapter) NodeIdentity() string {
	if a.Host == nil {
		return ""
	}
	return a.Host.NodeIdentity()
}

// IsShardJoining reports whether shardID is still in its join bootstrap
// window on this host: started locally with join=true and not yet given its
// first snapshot. Satisfies server.JoinObserver.
func (a *HostAdapter) IsShardJoining(shardID uint64) bool {
	return a.inner.IsShardJoining(shardID)
}

// BootstrapAdapter wraps HostAdapter and redirects SyncRead to StaleRead.
// Used during auth bootstrap before the Raft ReadIndex pipeline is fully
// warmed up. This is safe because:
//   - bootstrap runs on the leader (or waits for leadership)
//   - no concurrent writers exist during startup
//   - SyncPropose still goes through Raft consensus for writes
type BootstrapAdapter struct {
	*HostAdapter
}

// NewBootstrapAdapter creates a BootstrapAdapter that wraps the given
// HostAdapter. SyncRead is redirected to StaleRead so the ReadIndex
// pipeline need not be warmed up before bootstrap runs.
func NewBootstrapAdapter(a *HostAdapter) *BootstrapAdapter {
	return &BootstrapAdapter{HostAdapter: a}
}

// SyncRead performs a local stale read instead of a linearizable ReadIndex
// read. During bootstrap the local state machine is authoritative since
// the node is the leader and no other writers have started yet.
func (b *BootstrapAdapter) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return b.StaleRead(ctx, shardID, query)
}
