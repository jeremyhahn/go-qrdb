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

// NodeKeyLogProbe lists a tenant's raft databases whose log is on the
// shared, node-keyed WAL, for the tenant service's refusal to tighten such a
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
		if infos[i].TenantID == tenantID && infos[i].Backend == database.BackendRaft && !infos[i].DedicatedWAL {
			out = append(out, infos[i].Name)
		}
	}
	return out, nil
}
