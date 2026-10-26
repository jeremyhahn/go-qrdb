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

package server

import (
	"context"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// raftPromoter adapts the shared *quicraft.Host to the Promoter interface
// TenantShardReconciler uses to promote a caught-up (ReplicaStateObserverReady)
// non-voting observer to a full voting member. It wraps RequestAddNodeWithIdentity +
// rs.ResultC() into a single blocking call, mirroring
// cmd/qrdbd's raftFollowerAdder (the DatabaseService.RaftHost adapter) --
// PromoteObserver reuses the exact same RequestAddNodeWithIdentity config change
// AddFollowerNode issues for a founding member, because go-quicraft treats
// RequestAddNodeWithIdentity targeting an EXISTING observer's replica ID as an
// observer -> voter promotion rather than a fresh add (see go-quicraft
// host.go's requestConfigChange pre-registration comment).
type raftPromoter struct {
	host *quicraft.Host
}

// newRaftPromoter creates a raftPromoter over the shared host. host must be
// non-nil.
func newRaftPromoter(host *quicraft.Host) *raftPromoter {
	return &raftPromoter{host: host}
}

// LeaderReplicaID returns shardID's current leader replica ID via the
// shared Raft host.
func (p *raftPromoter) LeaderReplicaID(_ context.Context, shardID uint64) (uint64, bool, error) {
	return p.host.LeaderID(shardID)
}

// PromoteObserver calls RequestAddNodeWithIdentity on the shared Raft host, targeting an
// existing observer's replicaID, and blocks until the config change commits
// or ctx is cancelled. Only the shard's current leader can successfully
// issue this call; TenantShardReconciler.maybePromoteReadyObservers confirms
// local leadership via LeaderReplicaID before calling it.
//
// The observer is already in the shard's committed membership, so its
// committed identity is carried into the promotion.
func (p *raftPromoter) PromoteObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	identity, err := service.ResolveMemberIdentity(ctx, p.host, shardID, replicaID, raftAddr, "")
	if err != nil {
		return err
	}
	rs, err := p.host.RequestAddNodeWithIdentity(ctx, shardID, replicaID, raftAddr, identity, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}
