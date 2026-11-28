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
	"fmt"

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
//
// It forwards the four reads the audit and log resolvers make: GetTenant,
// ClusterIsolationPolicy (the cluster floor), IsolationTightening (the
// in-flight strict-tightening marker the system-route append check reads)
// and IsolationEpochs (the tenant's and the floor's isolation epochs a
// system-routed append's apply-time guard compares).
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

// ClusterIsolationPolicy implements tenant.ClusterIsolationPolicyReader, the
// floor the audit resolver reads a tenant's effective isolation policy
// against. With no tenant service there is no floor: operator-access.
func (l *nodeAuditIsolationLookup) ClusterIsolationPolicy(ctx context.Context) (tenant.ClusterIsolationPolicy, error) {
	if l.node.Tenant == nil {
		return tenant.ClusterIsolationPolicyOperatorAccess, nil
	}
	return l.node.Tenant.ClusterIsolationPolicy(ctx)
}

// IsolationTightening implements tenant.IsolationTighteningReader: whether a
// tightening of tenantID to strict is in flight, the marker the audit
// resolver's system-route append check reads before the tenant itself. With
// no tenant service there is no tightening. A tenant service that does not
// report the marker is an error, not false: the append fails closed rather
// than racing a tightening it cannot see.
func (l *nodeAuditIsolationLookup) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	if l.node.Tenant == nil {
		return false, nil
	}
	reader, ok := l.node.Tenant.(tenant.IsolationTighteningReader)
	if !ok {
		return false, fmt.Errorf("lifecycle: tenant service %T does not report strict tightening for tenant %q", l.node.Tenant, tenantID)
	}
	return reader.IsolationTightening(ctx, tenantID)
}

var _ tenant.IsolationEpochReader = (*nodeAuditIsolationLookup)(nil)

// IsolationEpochs implements tenant.IsolationEpochReader: the values of
// tenant.IsolationEpochKeys(tenantID) a system-routed append's guard
// compares at apply time. With no tenant service there is no epoch: an
// empty map. A tenant service that does not report the epochs is an error,
// as for IsolationTightening: the append fails closed.
func (l *nodeAuditIsolationLookup) IsolationEpochs(ctx context.Context, tenantID string) (map[string][]byte, error) {
	if l.node.Tenant == nil {
		return map[string][]byte{}, nil
	}
	reader, ok := l.node.Tenant.(tenant.IsolationEpochReader)
	if !ok {
		return nil, fmt.Errorf("lifecycle: tenant service %T does not report isolation epochs for tenant %q", l.node.Tenant, tenantID)
	}
	return reader.IsolationEpochs(ctx, tenantID)
}

var _ tenant.ClusterIsolationPolicyReader = (*nodeAuditIsolationLookup)(nil)
var _ tenant.IsolationTighteningReader = (*nodeAuditIsolationLookup)(nil)

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
