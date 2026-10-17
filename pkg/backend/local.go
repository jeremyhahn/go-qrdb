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
	sm    SMAdapter
	index atomic.Uint64

	// applier is the token that admits one goroutine at a time to sm.Update,
	// held across the whole Update, I/O included, so applies stay strictly
	// sequential. It is a one-slot channel rather than a sync.Mutex so a
	// proposer can wait for the token and for its own proposal to be applied
	// in the same select (see SyncPropose).
	applier chan struct{}

	// queueMu guards queue, the proposals waiting for the next group commit.
	// It is held only to append to queue or take it, never across an apply.
	queueMu sync.Mutex
	queue   []*proposal

	closed atomic.Bool
}

// proposal is one SyncPropose waiting for the group commit that covers it.
// result and err are written by the applier before it closes done, and read
// by the proposer only after done is closed.
type proposal struct {
	cmd    []byte
	result sm.Result
	err    error
	done   chan struct{}
}

// NewLocalBackend creates a LocalBackend that delegates to the given
// state machine adapter.
func NewLocalBackend(sm SMAdapter) *LocalBackend {
	return &LocalBackend{sm: sm, applier: make(chan struct{}, 1)}
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

// GetShardInfo reports the LocalBackend's one logical shard. A single process
// with no peers leads it: work only a shard's leader may do -- the compaction
// schedule -- runs here.
func (b *LocalBackend) GetShardInfo(shardID uint64) (*quicraft.ShardInfo, error) {
	return &quicraft.ShardInfo{ShardID: shardID, ReplicaID: 1, LeaderID: 1, IsLeader: true}, nil
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

// SyncPropose applies cmd to the state machine and returns once the Update
// covering it has returned: for a persistent engine, once the commit that makes
// cmd durable, and every fsync in it, has completed.
//
// Concurrent proposals are group-committed. Applies stay strictly sequential --
// one goroutine at a time holds the applier token across a whole Update -- but
// a proposal that arrives while an Update is running queues, and the next
// goroutine to take the token applies EVERY queued proposal as one Update:
// consecutive indices in arrival order, one storage batch, so one commit's
// worth of fsyncs. Proposals that pile up behind a slow fsync therefore share
// the next commit instead of paying one each, which is what let the lease
// sweep, keepalives and client writes fall seconds behind on a busy disk
// (DS-1689). It is the group commit of InnoDB's redo log and etcd's WAL; no
// proposal is acknowledged before the commit covering it returns.
//
// An Update error fails every proposal in its group, each with that error:
// none of them was acknowledged as applied, the same answer a lone proposal
// gets.
func (b *LocalBackend) SyncPropose(ctx context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	if b.closed.Load() {
		return sm.Result{}, &BackendClosedError{}
	}
	p := &proposal{cmd: cmd, done: make(chan struct{})}
	b.queueMu.Lock()
	b.queue = append(b.queue, p)
	b.queueMu.Unlock()

	select {
	case <-p.done:
		// The group an earlier token holder applied included p.
	case b.applier <- struct{}{}:
		b.applyQueuedHoldingToken(ctx, p)
	}
	return p.result, p.err
}

// applyQueuedHoldingToken applies the queue as one group unless p has already
// been applied, then releases the applier token the caller took. A token
// holder completes its whole group before releasing the token, so while this
// goroutine holds it p is either done or still queued.
func (b *LocalBackend) applyQueuedHoldingToken(ctx context.Context, p *proposal) {
	defer func() { <-b.applier }()
	select {
	case <-p.done:
		return
	default:
	}

	b.queueMu.Lock()
	group := b.queue
	b.queue = nil
	b.queueMu.Unlock()

	completed := false
	defer func() {
		// A panicking Update must not strand the rest of the group waiting on
		// done forever: complete them with an error, then let the panic go on.
		if !completed {
			for _, q := range group {
				q.err = &ApplyAbortedError{}
				close(q.done)
			}
		}
	}()

	entries := make([]sm.Entry, len(group))
	for i, q := range group {
		entries[i] = sm.Entry{Index: b.index.Add(1), Cmd: q.cmd}
	}
	results := make([]sm.Result, len(group))
	// The group is applied whatever becomes of the context of the proposer
	// that happened to take the token: the others' proposals are in it too.
	err := b.sm.Update(context.WithoutCancel(ctx), entries, results)
	completed = true
	for i, q := range group {
		if err != nil {
			q.err = err
		} else {
			q.result = results[i]
		}
		close(q.done)
	}
}

// SyncRead performs a direct lookup on the state machine.
func (b *LocalBackend) SyncRead(ctx context.Context, _ uint64, query interface{}) (interface{}, error) {
	if b.closed.Load() {
		return nil, &BackendClosedError{}
	}
	return b.sm.Lookup(ctx, query)
}

// RequestAddNodeWithIdentity returns ClusterOperationError since
// LocalBackend does not support Raft cluster membership changes.
func (b *LocalBackend) RequestAddNodeWithIdentity(_ context.Context, _, _ uint64, _, _ string, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "AddNode"}
}

// RequestAddObserverWithIdentity returns ClusterOperationError since
// LocalBackend does not support Raft cluster membership changes.
func (b *LocalBackend) RequestAddObserverWithIdentity(_ context.Context, _, _ uint64, _, _ string, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "AddObserver"}
}

// SyncGetShardMembership returns ClusterOperationError since LocalBackend
// has no Raft membership.
func (b *LocalBackend) SyncGetShardMembership(_ context.Context, _ uint64) (*quicraft.Membership, error) {
	return nil, &ClusterOperationError{Op: "GetShardMembership"}
}

// NodeIdentity returns "": LocalBackend has no Raft transport.
func (b *LocalBackend) NodeIdentity() string { return "" }

// RequestRemoveNode returns ClusterOperationError since LocalBackend
// does not support Raft cluster membership changes.
func (b *LocalBackend) RequestRemoveNode(_ context.Context, _, _ uint64, _ uint64) (*quicraft.RequestState, error) {
	return nil, &ClusterOperationError{Op: "RemoveNode"}
}

// RequestLeaderTransfer returns ClusterOperationError because a standalone
// node has no other replica to lead.
func (b *LocalBackend) RequestLeaderTransfer(_ context.Context, _, _ uint64) error {
	return &ClusterOperationError{Op: "LeaderTransfer"}
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
