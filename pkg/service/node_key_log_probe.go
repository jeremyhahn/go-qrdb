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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantDatabaseLister is the part of database.Manager a NodeKeyLogProbe
// needs: the catalog's databases of the tenant in the context.
type TenantDatabaseLister interface {
	ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error)
}

// NodeKeyLogProbe lists a tenant's databases whose data is written through
// the shared, node-keyed WAL, for the tenant service's refusal to tighten
// such a tenant to strict (tenant.NodeKeyLogProbe).
//
// A database is classified by its data route, not by its backend alone:
//
//   - a raft-backed database without a dedicated WAL logs on the node-keyed
//     WAL, whatever its tenant's isolation;
//   - every database of a barrier tenant served from the shared system store
//     (database.ServedBySharedStore), a pebble-backed one included, is
//     written through the shared system shard, whose log is the node-keyed
//     WAL.
type NodeKeyLogProbe struct {
	lister  TenantDatabaseLister
	tenants TenantIsolationLookup
}

// TenantIsolationLookup reads a tenant's isolation, so the probe can tell
// a barrier tenant's databases served from the shared system store.
type TenantIsolationLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

var _ tenant.NodeKeyLogProbe = (*NodeKeyLogProbe)(nil)

// NewNodeKeyLogProbe returns a probe over lister, typically the node's
// database.Manager, and tenants, the tenant registry. A nil tenants lookup
// fails every probe closed rather than miss a barrier tenant's databases.
func NewNodeKeyLogProbe(lister TenantDatabaseLister, tenants TenantIsolationLookup) *NodeKeyLogProbe {
	return &NodeKeyLogProbe{lister: lister, tenants: tenants}
}

// ErrNodeKeyLogProbeNoTenants is returned by a probe built without a tenant
// lookup: it cannot tell a barrier tenant's routes, so it refuses to answer.
var ErrNodeKeyLogProbeNoTenants = errors.New("service: node-key log probe has no tenant lookup")

// NodeKeyLogDatabases implements tenant.NodeKeyLogProbe. It reads the
// replicated catalog, so a database created through another node counts.
func (p *NodeKeyLogProbe) NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error) {
	if p.tenants == nil {
		return nil, ErrNodeKeyLogProbeNoTenants
	}
	info, err := p.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	sharedStore := info != nil && isolationDedicatedWALDataPath[info.EffectiveIsolationType()] == database.ServedBySharedStore
	infos, err := p.lister.ListForTenant(tenant.WithTenantID(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range infos {
		if infos[i].TenantID != tenantID {
			continue
		}
		if onNodeKeyLog(infos[i], sharedStore) {
			out = append(out, infos[i].Name)
		}
	}
	return out, nil
}

// onNodeKeyLog reports whether db's data is written through the node-keyed
// WAL: a raft database without a dedicated WAL, or any database of a tenant
// served from the shared system store.
func onNodeKeyLog(db database.DatabaseInfo, sharedStore bool) bool {
	if db.Backend == database.BackendRaft && !db.DedicatedWAL {
		return true
	}
	return sharedStore
}
