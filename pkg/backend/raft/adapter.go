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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
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
	// GetShardMembership returns the shard membership from local in-memory state
	// without a linearizable ReadIndex round-trip. Suitable for health checks.
	GetShardMembership(shardID uint64) (*quicraft.Membership, error)
	RequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error)
	// IsShardJoining reports whether shardID is still in its join bootstrap
	// window: started locally with join=true and not yet given its first
	// snapshot. Used by the placement reconciler (JoinObserver) to promote a
	// dynamically added replica from ReplicaStateObserver to
	// ReplicaStateObserverReady once it has caught up.
	IsShardJoining(shardID uint64) bool
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
}

// New creates a HostAdapter backed by the given quicraft.Host.
func New(h *quicraft.Host) *HostAdapter {
	return &HostAdapter{Host: h, inner: h}
}

// NewWithInner creates a HostAdapter backed by any QuicraftHost. Useful
// for testing: pass a mock instead of a real *quicraft.Host. The embedded
// *quicraft.Host is set to nil; callers must not invoke methods promoted
// from the nil host (e.g. Close, GetLeaderID) without providing a
// concrete *quicraft.Host.
func NewWithInner(host *quicraft.Host, inner QuicraftHost) *HostAdapter {
	return &HostAdapter{Host: host, inner: inner}
}

// ProposeWithMode proposes a write with the specified durability guarantee.
// Sync mode uses full Raft consensus. WALDurable and Batched modes
// currently fall through to Sync; a batch.Aggregator can be composed
// separately for true batched writes.
func (a *HostAdapter) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
	switch mode {
	case writemode.Sync:
		return a.inner.SyncPropose(ctx, shardID, cmd)
	case writemode.WALDurable, writemode.Batched:
		// WALDurable and Batched require additional infrastructure
		// (proposer adapter, batch aggregator). For now, fall through
		// to synchronous consensus to guarantee correctness.
		return a.inner.SyncPropose(ctx, shardID, cmd)
	default:
		return a.inner.SyncPropose(ctx, shardID, cmd)
	}
}

// StaleRead reads from the local state machine without Raft coordination.
func (a *HostAdapter) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return a.inner.StaleRead(ctx, shardID, query)
}

// SyncRead performs a linearizable read against the state machine.
func (a *HostAdapter) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return a.inner.SyncRead(ctx, shardID, query)
}

// FollowerRead reads from the local state machine after ensuring it has
// applied up to the committed index. This allows any node (leader or
// follower) to serve reads without a ReadIndex round-trip to the leader,
// providing stronger guarantees than StaleRead with lower latency than
// SyncRead on followers.
func (a *HostAdapter) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return a.inner.FollowerRead(ctx, shardID, query)
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
		return a.inner.SyncRead(ctx, shardID, query)
	case consistency.FollowerRead:
		return a.inner.FollowerRead(ctx, shardID, query)
	case consistency.Eventual:
		return a.inner.StaleRead(ctx, shardID, query)
	}
	if opts.AllowStale {
		return a.inner.StaleRead(ctx, shardID, query)
	}
	return a.inner.SyncRead(ctx, shardID, query)
}

// RequestSnapshot requests an asynchronous snapshot for the specified shard.
func (a *HostAdapter) RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error) {
	return a.inner.RequestSnapshot(ctx, shardID, quicraft.SnapshotOption{})
}

// GetShardRaftState returns the config change index and current term for
// a shard. Uses the non-linearizable GetShardMembership to avoid blocking
// on a ReadIndex round-trip — callers like the health-check handler only
// need to confirm the shard is loaded and has a known leader, not a
// strongly consistent membership view.
func (a *HostAdapter) GetShardRaftState(shardID uint64) (configChangeIndex uint64, term uint64, err error) {
	membership, err := a.inner.GetShardMembership(shardID)
	if err != nil {
		return 0, 0, err
	}
	return membership.ConfigChangeID, 0, nil
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
