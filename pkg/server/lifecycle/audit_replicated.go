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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// nodeAuditIsolationLookup reads n.Tenant LAZILY, at call time, rather than
// a snapshot taken when BuildServices wired the replicated audit sink. It
// exists because the resolver is constructed before n.Tenant is populated
// (several branches later in BuildServices, depending on SealBarrier /
// BarrierRegistry), but every ResolveAuditShard call happens long after
// BuildServices has returned -- by which point n.Tenant, if this
// composition has one at all, is set.
//
// Before n.Tenant exists (no SealBarrier, or an Emit/Query racing the tail
// of BuildServices itself, which never happens in practice since nothing
// can call through n before it is returned) every tenant is routed as a
// logical-prefix tenant, mirroring SystemShardAuditResolver's own
// nil-isolation behavior.
type nodeAuditIsolationLookup struct {
	node *Node
}

// GetTenant implements service.AuditIsolationLookup.
func (l *nodeAuditIsolationLookup) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if l.node.Tenant == nil {
		return nil, tenant.ErrTenantNotFound
	}
	return l.node.Tenant.GetTenant(ctx, tenantID)
}

// nodeAuditEntitlementLookup reads n.Tenant lazily, exactly like
// nodeAuditIsolationLookup above. ReplicatedAuditEnabled is not part of the
// tenant.TenantServicer interface (it is a concrete-type method on
// *tenant.TenantService, the same shape as CheckIsolation/CheckBarrierMode),
// so a composition whose Node.Tenant is a different TenantServicer
// implementation (e.g. the KV-routed service some compositions use) degrades
// to "not entitled" rather than a type-assertion panic -- the secure
// default, and the same answer a tenant with no metadata record gets.
type nodeAuditEntitlementLookup struct {
	node *Node
}

// ReplicatedAuditEnabled implements service.AuditEntitlementLookup.
func (l *nodeAuditEntitlementLookup) ReplicatedAuditEnabled(ctx context.Context, tenantID string) (bool, error) {
	ent, ok := l.node.Tenant.(service.AuditEntitlementLookup)
	if !ok {
		return false, nil
	}
	return ent.ReplicatedAuditEnabled(ctx, tenantID)
}
