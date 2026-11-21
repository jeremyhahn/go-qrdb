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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// catalogNames resolves placement database ids to names from the tenant's
// catalog: the system store for a shared tenant, the tenant's home store for
// an isolated one. The placement table itself names databases by opaque id
// only (docs/multi-tenancy/architecture.md decision S5).
type catalogNames struct {
	catalog database.Catalog
}

var _ placement.NameResolver = catalogNames{}

// DatabaseNames implements placement.NameResolver. The home group's id
// always resolves, so a sealed tenant's home group still starts; a catalog
// that cannot be read yet (the tenant sealed, its home not yet open)
// resolves nothing else, and the reconciler retries on its next pass.
func (c catalogNames) DatabaseNames(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{database.TenantHomeID: database.TenantHomeName}
	metas, err := c.catalog.ListTenant(ctx, tenantID)
	if err != nil {
		return out
	}
	for _, m := range metas {
		if m.ID != "" {
			out[m.ID] = m.Name
		}
	}
	return out
}

// setPlacementNames sets the catalog resolver on the node's placement store
// and on its change feed.
func setPlacementNames(n *Node, catalog database.Catalog) {
	if catalog == nil {
		return
	}
	names := catalogNames{catalog: catalog}
	if n.PlacementStore != nil {
		n.PlacementStore.SetNameResolver(names)
	}
	if feed, ok := n.PlacementChanges.(*placement.Store); ok && feed != nil {
		feed.SetNameResolver(names)
	}
}
