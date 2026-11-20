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
// the system shard or on a dedicated per-tenant host this resolver does not
// yet reach. *tenant.TenantService satisfies it directly.
type AuditIsolationLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// SystemShardAuditResolver resolves every prefix/barrier/system tenant's
// replicated audit chain to one fixed (Host, shardID): the system shard,
// where ruling 2 of docs/audit/architecture.md#cluster-wide-replicated-trail
// already puts the system tenant's own platform events, and where every
// prefix/barrier tenant's resolved IAM store already lives
// (tenant.StoreResolver's sharedFor). An unregistered tenant id -- no
// metadata at all -- resolves here too, mirroring tenant.StoreResolver's own
// fall-through for the same case.
//
// A dedicated-isolation tenant (volume or raft) runs its own separate Raft
// host, built lazily after its barrier unseals. Routing ITS audit trail to
// that host instead is follow-on work: this resolver refuses such a tenant
// explicitly (*DedicatedTenantAuditNotSupportedError) rather than silently
// writing its events to the system shard's chain under its own tenant
// segment, which would be a correctness defect -- the wrong replica set and
// the wrong barrier -- not merely an absent feature.
type SystemShardAuditResolver struct {
	host      Host
	shardID   uint64
	isolation AuditIsolationLookup
	// logs routes a barrier tenant to its home group through groups; see
	// NewSystemShardLogResolver.
	logs   bool
	groups LogGroupSource
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
// routes through: NewSystemShardAuditResolver's, except that a barrier tenant
// resolves to its own home group through groups. A log's entries and its
// sealed head-signing key are then applied into, and read from, the log
// registry of the tenant's barrier-sealed store, never the system shard's,
// and a sealed tenant barrier refuses them with the error its other reads
// return. Snapshot and restore of that group carry the logs with it. A nil
// groups fails a barrier tenant closed with *BarrierStoreUnavailableError.
func NewSystemShardLogResolver(host Host, shardID uint64, isolation AuditIsolationLookup, groups LogGroupSource) *SystemShardAuditResolver {
	return &SystemShardAuditResolver{host: host, shardID: shardID, isolation: isolation, logs: true, groups: groups}
}

// LogShardResolver resolves the Raft group a verifiable log is applied in and
// read from: ResolveAuditShard for a log in a database without a barrier of
// its own, ResolveDatabaseLogShard for one in a barrier-enabled database.
type LogShardResolver interface {
	AuditShardResolver
	ResolveDatabaseLogShard(ctx context.Context, tenantID, database string) (Host, uint64, error)
}

// LogGroupSource resolves the Raft group of a barrier-sealed store: a barrier
// tenant's home group (its records, log and snapshots under the tenant's key)
// or a barrier-enabled database's own group (under the database's key). Each
// refuses, while the barrier is sealed, with the error that barrier's other
// reads return.
type LogGroupSource interface {
	TenantHomeGroup(ctx context.Context, tenantID string) (Host, uint64, error)
	DatabaseGroup(ctx context.Context, tenantID, database string) (Host, uint64, error)
}

// ResolveDatabaseLogShard resolves the group of tenantID's barrier-enabled
// database: the database's own barrier-sealed group. Without a group source
// the database's store is unreachable here and the log fails closed with
// *BarrierStoreUnavailableError, never falling back to the system shard.
func (r *SystemShardAuditResolver) ResolveDatabaseLogShard(ctx context.Context, tenantID, database string) (Host, uint64, error) {
	routed := tenantid.Normalize(tenantID)
	if r.groups == nil {
		return nil, 0, &BarrierStoreUnavailableError{TenantID: routed, Database: database}
	}
	return r.groups.DatabaseGroup(ctx, routed, database)
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
	switch info.EffectiveIsolationType() {
	case tenant.IsolationVolume, tenant.IsolationRaft:
		return nil, 0, &DedicatedTenantAuditNotSupportedError{
			TenantID:      routed,
			IsolationType: string(info.EffectiveIsolationType()),
		}
	case tenant.IsolationBarrier:
		if r.logs {
			if r.groups == nil {
				return nil, 0, &BarrierStoreUnavailableError{TenantID: routed}
			}
			return r.groups.TenantHomeGroup(ctx, routed)
		}
		return r.host, r.shardID, nil
	default:
		return r.host, r.shardID, nil
	}
}
