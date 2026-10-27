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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// leaderIDSource is the one method the reclaim sweeper's leadership check
// needs from the Raft host.
type leaderIDSource interface {
	LeaderID(shardID uint64) (uint64, bool, error)
}

// systemShardLeader reports whether replicaID currently leads the system
// shard -- the owner of shared-store reclaim jobs.
func systemShardLeader(host leaderIDSource, replicaID uint64) func() bool {
	return func() bool {
		leaderID, ok, err := host.LeaderID(shard.ShardKV)
		return err == nil && ok && leaderID == replicaID
	}
}

// startReclaimSweeper starts the reclaim-job sweeper (go-qrdb#547) on EVERY
// daemon, clustered or standalone. A standalone daemon has no Raft host to
// ask (isSystemLeader nil): it is a single process with no peers, so it owns
// every shared-store job itself -- nil is ReclaimSweeper.Run's own "always
// the leader". Gating the start on a host, as the lease-expiry sweeper's is,
// would leave a standalone daemon's TRUNCATEs unreclaimed forever.
func startReclaimSweeper(ctx context.Context, sweeper *service.ReclaimSweeper, interval time.Duration, isSystemLeader func() bool, logger *slog.Logger) {
	if sweeper == nil {
		return
	}
	go sweeper.Run(ctx, interval, isSystemLeader, logger)
}
