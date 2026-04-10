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

package backend

import (
	"context"
	"sync"
	"sync/atomic"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// LocalBackend implements service.Host without Raft consensus. All reads
// and writes are applied directly to the underlying SMAdapter. This is
// used for memory, file, and direct PebbleDB storage modes where
// distributed consensus is not required.
type LocalBackend struct {
	sm        SMAdapter
	index     atomic.Uint64
	proposeMu sync.Mutex
	closed    atomic.Bool
}

// NewLocalBackend creates a LocalBackend that delegates to the given
// state machine adapter.
func NewLocalBackend(sm SMAdapter) *LocalBackend {
	return &LocalBackend{sm: sm}
}

// ID returns the host identifier. LocalBackend always identifies as
// "local" since it is not part of a Raft cluster.
func (b *LocalBackend) ID() string {
	return "local"
}

// RaftAddress returns a synthetic address indicating embedded local mode.
func (b *LocalBackend) RaftAddress() string {
	return "local://embedded"
}

// ShardCount returns 1 since the LocalBackend operates as a single
// logical shard.
func (b *LocalBackend) ShardCount() int {
	return 1
}

// GetNodeHostInfo returns an empty NodeHostInfo since there is no Raft
// host to report on.
func (b *LocalBackend) GetNodeHostInfo(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
	return &quicraft.NodeHostInfo{}
}

// HealthCheck returns a healthy status since the LocalBackend is always
// operational (no leader election or consensus required).
func (b *LocalBackend) HealthCheck() *quicraft.HealthStatus {
	return &quicraft.HealthStatus{Status: "ok", DiskOK: true, ShardCount: 1}
}

// Drain is a no-op for LocalBackend since there is no Raft host to
// drain.
func (b *LocalBackend) Drain() error {
	return nil
}

// IsDraining always returns false for LocalBackend.
func (b *LocalBackend) IsDraining() bool {
	return false
}

// SyncPropose applies a command directly to the state machine under a
// mutex to preserve sequential-apply semantics.
func (b *LocalBackend) SyncPropose(ctx context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	if b.closed.Load() {
		return sm.Result{}, &BackendClosedError{}
	}
	b.proposeMu.Lock()
	defer b.proposeMu.Unlock()

	idx := b.index.Add(1)
	entries := []sm.Entry{{Index: idx, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := b.sm.Update(ctx, entries, results); err != nil {
		return sm.Result{}, err
	}
	return results[0], nil
}

// SyncRead performs a direct lookup on the state machine.
func (b *LocalBackend) SyncRead(ctx context.Context, _ uint64, query interface{}) (interface{}, error) {
	if b.closed.Load() {
		return nil, &BackendClosedError{}
	}
	return b.sm.Lookup(ctx, query)
}

// RequestAddNode returns ClusterOperationError since LocalBackend does
// not support Raft cluster membership changes.
func (b *LocalBackend) RequestAddNode(_ context.Context, _, _ uint64, _ string, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "AddNode"}
}

// RequestRemoveNode returns ClusterOperationError since LocalBackend
// does not support Raft cluster membership changes.
func (b *LocalBackend) RequestRemoveNode(_ context.Context, _, _ uint64, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "RemoveNode"}
}

// RequestSnapshot returns ClusterOperationError since LocalBackend does
// not support Raft snapshots.
func (b *LocalBackend) RequestSnapshot(_ context.Context, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "RequestSnapshot"}
}

// ProposeWithMode applies the command directly regardless of write mode,
// since write modes are a Raft-only concept.
func (b *LocalBackend) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
	return b.SyncPropose(ctx, shardID, cmd)
}

// StaleRead is identical to SyncRead for LocalBackend since all reads
// are local.
func (b *LocalBackend) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return b.SyncRead(ctx, shardID, query)
}

// ReadWithConsistency performs a direct local read regardless of the
// requested consistency level since all state is local.
func (b *LocalBackend) ReadWithConsistency(ctx context.Context, shardID uint64, query interface{}, _ consistency.ReadOptions) (interface{}, error) {
	return b.SyncRead(ctx, shardID, query)
}

// FollowerRead falls through to SyncRead on a LocalBackend since there
// is no Raft cluster to distinguish leader from follower. All reads are
// already local and consistent.
func (b *LocalBackend) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	return b.SyncRead(ctx, shardID, query)
}

// GetLeaderID always returns (1, true, nil) since the LocalBackend is
// always the leader of its single-node "cluster".
func (b *LocalBackend) GetLeaderID(_ uint64) (uint64, bool, error) {
	return 1, true, nil
}

// GetShardRaftState returns zeros since there is no Raft state.
func (b *LocalBackend) GetShardRaftState(_ uint64) (uint64, uint64, error) {
	return 0, 0, nil
}

// StartShard returns ClusterOperationError since the LocalBackend does
// not support dynamic shard creation.
func (b *LocalBackend) StartShard(_ map[uint64]string, _ bool, _ sm.CreateFunc, _ config.Config) error {
	return &ClusterOperationError{Op: "StartShard"}
}

// StopShard is a no-op for LocalBackend.
func (b *LocalBackend) StopShard(_ uint64) error {
	return nil
}

// Close shuts down the underlying state machine and marks the backend
// as closed.
func (b *LocalBackend) Close() error {
	if b.closed.Swap(true) {
		return &BackendClosedError{}
	}
	return b.sm.Close(context.Background())
}
