// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantDatabaseLister is the part of database.Manager a NodeKeyLogProbe
// needs: the catalog's databases of the tenant in the context.
type TenantDatabaseLister interface {
	ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error)
}

// NodeKeyLogProbe lists a tenant's databases whose data is on a log under
// the node key (onNodeKeyLog), for the tenant service's refusal to tighten such a
// tenant to strict (tenant.NodeKeyLogProbe).
type NodeKeyLogProbe struct {
	lister TenantDatabaseLister
}

var _ tenant.NodeKeyLogProbe = (*NodeKeyLogProbe)(nil)

// NewNodeKeyLogProbe returns a probe over lister, typically the node's
// database.Manager.
func NewNodeKeyLogProbe(lister TenantDatabaseLister) *NodeKeyLogProbe {
	return &NodeKeyLogProbe{lister: lister}
}

// NodeKeyLogDatabases implements tenant.NodeKeyLogProbe. It reads the
// replicated catalog, so a database created through another node counts.
func (p *NodeKeyLogProbe) NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error) {
	infos, err := p.lister.ListForTenant(tenant.WithTenantID(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range infos {
		if infos[i].TenantID == tenantID && onNodeKeyLog(infos[i].Backend, infos[i].DataDir, infos[i].Barrier, infos[i].DedicatedWAL) {
			out = append(out, infos[i].Name)
		}
	}
	return out, nil
}

// onNodeKeyLog reports whether a raft- or barrier-isolated tenant's
// database writes its data to a log under the node key, judged by the route
// restServiceRegistry.resolve serves it from rather than by its backend
// field alone:
//   - a raft-backed database is a raft group, on the shared node-keyed WAL
//     unless it has a dedicated one;
//   - any other database is served from the shared system shard, whose WAL
//     is under the node key, unless its own barrier wraps the store it is
//     served from (an explicit data dir, database.DatabaseConfig.
//     BarrierOnDataPath).
//
// A barrier tenant never serves a raft database from its own group (its
// dedicated-WAL create is refused, isolationDedicatedWALDataPath), so the
// raft rule only spares a raft tenant's dedicated-WAL databases. A volume
// tenant is never asked: its log is under its own key.
func onNodeKeyLog(backend database.BackendType, dataDir string, barrier *database.DatabaseBarrierConfig, dedicatedWAL bool) bool {
	cfg := database.DatabaseConfig{Backend: backend, DataDir: dataDir, Barrier: barrier, DedicatedWAL: dedicatedWAL}
	if cfg.Backend == database.BackendRaft {
		return !cfg.DedicatedWAL
	}
	return cfg.Barrier == nil || !cfg.Barrier.Enabled || !cfg.BarrierOnDataPath()
}
