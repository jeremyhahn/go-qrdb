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

	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// WireTrustAnchors installs every tenant's registered trust anchors on a
// composer's client-auth trust pool -- the TLS-layer widening and the
// issuer-tenant binding at once -- and subscribes the pool's cache
// invalidation to anchor changes, so an anchor added or withdrawn on this
// node is reflected on the very next handshake. A nil pool, or a node built
// without a certificate provider (no anchors), is a no-op.
func (n *Node) WireTrustAnchors(pool *clienttrust.Pool) {
	if pool == nil || n.TrustAnchors == nil {
		return
	}
	pool.SetTrustAnchors(n.TrustAnchors, clienttrust.TenantListerFunc(n.trustAnchorTenants))
	n.ClientTrustChanges.Subscribe(pool.Invalidate)
}

// trustAnchorTenants lists every tenant that may hold trust anchors: each
// live tenant, and the system tenant whether or not it is listed.
func (n *Node) trustAnchorTenants(ctx context.Context) ([]string, error) {
	ids := []string{tenantid.System}
	if n.Tenant == nil {
		return ids, nil
	}
	tenants, err := n.Tenant.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tenants {
		if !tenantid.IsSystem(t.ID) {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}
