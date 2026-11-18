// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package node

import (
	"context"
	"log/slog"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// pendingUserReaperOption wires the pending-user reaper exactly as qrdbd
// does: the sweep interval comes from auth.pending_user_sweep_interval
// ("off" disables it), and on a clustered node each tenant is swept only by
// the leader of the group that serves it: an isolated tenant by its home
// group's leader, every other tenant by the KV shard's leader. A cluster so
// deletes each lapsed account once per pass, and an isolated tenant the KV
// leader holds sealed is still swept where it is served. A standalone node
// sweeps unconditionally.
func (n *Node) pendingUserReaperOption(cfg *qrdbconfig.Config, standalone bool, resolver tenant.StoreResolver) server.ServerOption {
	var homes databaseLeader
	if n.raftBackend != nil {
		homes = n.raftBackend
	}
	locator, _ := resolver.(tenant.HomeLocator)
	return server.WithPendingUserReaper(pendingUserSweep(cfg), pendingUserLeader(n.host, cfg.Raft.ReplicaID, standalone, locator, homes))
}

// pendingUserSweep resolves auth.pending_user_sweep_interval to the server's
// reaper interval: negative (no reaper) for "off" or an unparseable value.
func pendingUserSweep(cfg *qrdbconfig.Config) time.Duration {
	sweep, err := qrdbconfig.ParsePendingUserSweepInterval(cfg.Auth.PendingUserSweepInterval)
	if err != nil {
		slog.Error("pending user reaper disabled", slog.Any("error", err))
		return -1
	}
	return sweep
}

// databaseLeader reports whether this node leads one tenant database's Raft
// group; *server.RaftBackendFactory satisfies it.
type databaseLeader interface {
	LeadsDatabase(tenantID, db string) bool
}

// pendingUserLeader builds the per-tenant sweep gate (see
// server.PendingSweepLeader): an isolated tenant is swept on its home
// group's leader (homes), every other tenant on the KV shard's leader. It is
// nil for a standalone node (or one without a Raft host), which the server
// reads as "always sweep".
func pendingUserLeader(host any, replicaID uint64, standalone bool, locator tenant.HomeLocator, homes databaseLeader) func(ctx context.Context, tenantID string) bool {
	if standalone {
		return nil
	}
	lr, ok := host.(leaderReporter)
	if !ok || lr == nil {
		return nil
	}
	if replicaID == 0 {
		replicaID = 1 // buildClusteredHost's default replica.
	}
	leadsSystem := func() bool {
		id, known, err := lr.LeaderID(shard.ShardKV)
		return err == nil && known && id == replicaID
	}
	var leadsHome func(string) bool
	if homes != nil {
		leadsHome = func(tenantID string) bool { return homes.LeadsDatabase(tenantID, database.TenantHomeName) }
	}
	return server.PendingSweepLeader(leadsSystem, locator, leadsHome)
}
