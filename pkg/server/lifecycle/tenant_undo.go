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
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantUndoRequired returns the activation check tenant.WithTenantUndoGate
// takes on host. A standalone node is its own whole cluster, so the feature is
// active from its first start. A clustered node waits for every member to
// advertise featuregate.TenantUndo, judged cluster-wide. A nil host, or one
// that gates no features, gets nil: the gate stays off and an undo retires
// the id as a deletion does.
func tenantUndoRequired(host service.Host) func() error {
	if host == nil {
		return nil
	}
	if standaloneHost(host) {
		return func() error { return nil }
	}
	gated, ok := host.(service.FeatureGatedHost)
	if !ok {
		return nil
	}
	return func() error {
		if gated.FeatureEnabled(0, featuregate.TenantUndo) {
			return nil
		}
		return &featuregate.NotActiveError{Feature: featuregate.TenantUndo}
	}
}

// undoCleanupRoster returns the serving nodes an undone id's reuse waits on,
// by the identity each acknowledges under (nodeIdentity): this node alone on
// a standalone node, else every member of the system shard's committed
// membership -- voters, observers and witnesses, the members feature
// activation counts. A nil host gets nil, which refuses every reuse as
// pending.
func undoCleanupRoster(host service.Host) tenant.UndoCleanupRoster {
	if host == nil {
		return nil
	}
	if standaloneHost(host) {
		return func(context.Context) ([]string, error) { return []string{LocalNodeID}, nil }
	}
	return func(ctx context.Context) ([]string, error) {
		m, err := host.SyncGetShardMembership(ctx, shard.ShardKV)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(m.Nodes)+len(m.Observers)+len(m.Witnesses))
		for _, set := range []map[uint64]string{m.Nodes, m.Observers, m.Witnesses} {
			for _, addr := range set {
				seen[addr] = struct{}{}
			}
		}
		roster := make([]string, 0, len(seen))
		for addr := range seen {
			roster = append(roster, addr)
		}
		sort.Strings(roster)
		return roster, nil
	}
}
