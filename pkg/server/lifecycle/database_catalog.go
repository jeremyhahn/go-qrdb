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

package lifecycle

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// openDatabaseCatalog installs catalog on m and opens every database it
// records. Listing the catalog is a linearizable read of the system shard,
// and BuildServices runs before its callers wait for a leader, so it first
// waits for the system shard to have one, with no budget of its own: a peer
// is unsealed by its own operator, and until a quorum is, no leader can be
// elected however long the read retries (server.AwaitShardLeader, the rule
// the daemon's own boot follows). Once a leader is known the read waits out
// the shard settling -- a new leader's ReadIndex warm-up, or a leader change
// -- within server.SystemShardSettleBudget rather than failing the boot on
// it. OpenAll opens nothing before its List succeeds, so a retried attempt
// opens nothing twice.
func openDatabaseCatalog(ctx context.Context, leaders server.LeaderSource, m *database.Manager, catalog database.Catalog) error {
	m.SetCatalog(catalog)
	if err := server.AwaitShardLeader(ctx, leaders, server.SystemShardSettleBudget, shard.ShardKV); err != nil {
		return &DatabaseCatalogError{Err: err}
	}
	if err := server.WhileSystemShardSettling(ctx, leaders, "open database catalog", m.OpenAll); err != nil {
		return &DatabaseCatalogError{Err: err}
	}
	return nil
}
