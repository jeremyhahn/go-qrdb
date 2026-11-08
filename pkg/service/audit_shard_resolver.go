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

package service

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// AuditShardResolver resolves which (Host, shardID) pair serves tenantID's
// replicated audit chain -- the same routing question
// tenant.StoreResolver.Resolve answers for IAM, but naming a Raft propose
// target instead of a kvstore.KVStore, because the chain append rides a
// dedicated opcode (protocol.OpAuditAppend) a KVStore cannot carry.
type AuditShardResolver interface {
	ResolveAuditShard(ctx context.Context, tenantID string) (Host, uint64, error)
}

// AuditIsolationLookup is the minimal tenant-metadata lookup
// SystemShardAuditResolver needs: whether a tenant's audit trail belongs on
// the system shard or in the tenant's isolated home group.
// *tenant.TenantService satisfies it directly.
type AuditIsolationLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// AuditHomeGroupSource names the Raft propose target of an isolated
// tenant's home group: the group its audit trail and verifiable logs apply
// in (docs/multi-tenancy/architecture.md decision S9). The lifecycle's
// TenantHomes satisfies it on a node with a Raft host.
type AuditHomeGroupSource interface {
	HomeGroup(ctx context.Context, tenantID string) (Host, uint64, error)
}

// SystemShardAuditResolver resolves the system tenant's, every prefix
// tenant's and every unregistered tenant's replicated audit chain to one
// fixed (Host, shardID): the system shard, where ruling 2 of
// docs/audit/architecture.md#cluster-wide-replicated-trail puts the system
// tenant's own platform events, and where every prefix tenant's resolved IAM
// store already lives (tenant.StoreResolver's sharedFor). An unregistered
// tenant id -- no metadata at all -- resolves here too, mirroring
// tenant.StoreResolver's own fall-through for the same case.
//
// An isolated tenant (barrier, volume or raft) resolves to its home group
// through the attached AuditHomeGroupSource, so its trail is replicated by
// its own group under its own barrier. On a node without a home group (no
// source attached: standalone, or before firstboot) a volume or raft tenant
// is refused explicitly (*DedicatedTenantAuditNotSupportedError) rather than
// silently written to the system shard's chain under its own tenant
// segment, which would be a correctness defect -- the wrong replica set and
// the wrong barrier -- not merely an absent feature; a barrier tenant's
// audit stays on the system shard there.
type SystemShardAuditResolver struct {
	host      Host
	shardID   uint64
	isolation AuditIsolationLookup
	// refuseBarrier also refuses a barrier tenant on a node without a home
	// group; see NewSystemShardLogResolver.
	refuseBarrier bool
	// homes, once attached, routes isolated tenants to their home group.
	homes atomic.Pointer[auditHomeGroupSource]
}

// auditHomeGroupSource boxes the attached source for the atomic pointer.
type auditHomeGroupSource struct{ AuditHomeGroupSource }

// SetHomeGroupSource attaches the home-group source isolated tenants
// resolve to. The lifecycle calls it once the node's Raft tenant homes are
// built; a nil source detaches it.
func (r *SystemShardAuditResolver) SetHomeGroupSource(source AuditHomeGroupSource) {
	if source == nil {
		r.homes.Store(nil)
		return
	}
	r.homes.Store(&auditHomeGroupSource{source})
}

// NewSystemShardAuditResolver returns a resolver that names (host, shardID)
// for every tenant isolation does not identify as dedicated. isolation may
// be nil, in which case every tenant is routed to the system shard without a
// dedicated-isolation check -- the correct behavior for a daemon
// composition that has not wired tenant metadata (standalone, or a node
// before firstboot).
func NewSystemShardAuditResolver(host Host, shardID uint64, isolation AuditIsolationLookup) *SystemShardAuditResolver {
	return &SystemShardAuditResolver{host: host, shardID: shardID, isolation: isolation}
}

// NewSystemShardLogResolver returns the resolver the verifiable-log service
// routes through: NewSystemShardAuditResolver's, except that on a node
// without a home group a barrier tenant is refused with
// *BarrierTenantLogNotSupportedError. There a log's entries and its sealed
// head-signing key would be applied into the system shard's log registry,
// which the system barrier protects, not the tenant's: routing a barrier
// tenant there would keep its private key readable while its own barrier is
// sealed. With a home group attached the tenant's logs apply in it.
func NewSystemShardLogResolver(host Host, shardID uint64, isolation AuditIsolationLookup) *SystemShardAuditResolver {
	return &SystemShardAuditResolver{host: host, shardID: shardID, isolation: isolation, refuseBarrier: true}
}

// ResolveAuditShard implements AuditShardResolver.
func (r *SystemShardAuditResolver) ResolveAuditShard(ctx context.Context, tenantID string) (Host, uint64, error) {
	routed := tenantid.Normalize(tenantID)
	if tenantid.IsSystem(routed) || r.isolation == nil {
		return r.host, r.shardID, nil
	}
	info, err := r.isolation.GetTenant(ctx, routed)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			// No metadata: served as a logical-prefix tenant, the same
			// fall-through tenant.StoreResolver.Resolve performs.
			return r.host, r.shardID, nil
		}
		return nil, 0, err
	}
	isolation := info.EffectiveIsolationType()
	if homes := r.homes.Load(); homes != nil {
		switch isolation {
		case tenant.IsolationBarrier, tenant.IsolationVolume, tenant.IsolationRaft:
			return homes.HomeGroup(ctx, routed)
		}
		return r.host, r.shardID, nil
	}
	switch isolation {
	case tenant.IsolationVolume, tenant.IsolationRaft:
		return nil, 0, &DedicatedTenantAuditNotSupportedError{
			TenantID:      routed,
			IsolationType: string(info.EffectiveIsolationType()),
		}
	case tenant.IsolationBarrier:
		if r.refuseBarrier {
			return nil, 0, &BarrierTenantLogNotSupportedError{TenantID: routed}
		}
		return r.host, r.shardID, nil
	default:
		return r.host, r.shardID, nil
	}
}
