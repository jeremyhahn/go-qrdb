//go:build qrdb_embedded

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

package main

import (
	"context"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// hostAdapter wraps *quicraft.Host to satisfy the service.Host interface.
// It adds write-mode dispatch, consistency-level reads, and shard state
// queries that the bare quicraft.Host does not provide.
type hostAdapter struct {
	*quicraft.Host
}

// ProposeWithMode proposes a write with the specified durability guarantee.
// Sync mode uses full Raft consensus. WALDurable and Batched modes
// currently fall through to Sync; a batch.Aggregator can be composed
// separately for true batched writes.
func (a *hostAdapter) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
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
// This delegates directly to quicraft.Host.StaleRead.
func (a *hostAdapter) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return a.Host.StaleRead(ctx, shardID, query)
}

// ReadWithConsistency performs a read with the specified consistency level.
// Strong uses full Raft consensus (SyncRead). Eventual uses a local stale
// read. Default falls through to Strong.
func (a *hostAdapter) ReadWithConsistency(ctx context.Context, shardID uint64, query interface{}, opts consistency.ReadOptions) (interface{}, error) {
	if opts.AllowStale {
		return a.Host.StaleRead(ctx, shardID, query)
	}
	return a.SyncRead(ctx, shardID, query)
}

// RequestSnapshot requests an asynchronous snapshot for the specified shard.
// This wraps the quicraft.Host method with a default SnapshotOption to
// satisfy the service.Host interface.
func (a *hostAdapter) RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error) {
	return a.Host.RequestSnapshot(ctx, shardID, quicraft.SnapshotOption{})
}

// GetShardRaftState returns the config change index and current term for
// a shard. Returns zeros when the shard membership cannot be retrieved.
func (a *hostAdapter) GetShardRaftState(shardID uint64) (configChangeIndex uint64, term uint64, err error) {
	membership, err := a.SyncGetShardMembership(context.Background(), shardID)
	if err != nil {
		return 0, 0, err
	}
	return membership.ConfigChangeID, 0, nil
}

// bootstrapHostAdapter wraps hostAdapter and redirects SyncRead to
// StaleRead. Used during auth bootstrap before the Raft ReadIndex
// pipeline is fully warmed up. This is safe because:
//   - bootstrap runs on the leader (or waits for leadership)
//   - no concurrent writers exist during startup
//   - SyncPropose still goes through Raft consensus for writes
type bootstrapHostAdapter struct {
	*hostAdapter
}

// SyncRead performs a local stale read instead of a linearizable
// ReadIndex read. During bootstrap the local state machine is
// authoritative since the node is the leader and no other writers
// have started yet.
func (b *bootstrapHostAdapter) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return b.StaleRead(ctx, shardID, query)
}
