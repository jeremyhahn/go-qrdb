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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
// fall-through for the same case. The verifiable-log service reads a
// tenant's audit instance (system/audit) through this same resolver, so the
// instance is written and read in the same group.
//
// The resolver NewSystemShardLogResolver returns routes a barrier tenant's
// own logs to its home group through groups instead: their entries and
// sealed head-signing keys are applied into, and read from, the log registry
// of the tenant's barrier-sealed store, never the system shard's, and a
// sealed tenant barrier refuses them with the error its other reads return.
// Snapshot and restore of that group carry the logs with it.
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
	// barrierHome routes a barrier tenant to its home group through groups;
	// see NewSystemShardLogResolver.
	barrierHome bool
	groups      LogGroupSource
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
// routes a tenant's own logs through: NewSystemShardAuditResolver's, except
// that a barrier tenant resolves to its home group through groups. A nil
// groups fails a barrier tenant closed with *BarrierStoreUnavailableError.
func NewSystemShardLogResolver(host Host, shardID uint64, isolation AuditIsolationLookup, groups LogGroupSource) *SystemShardAuditResolver {
	return &SystemShardAuditResolver{host: host, shardID: shardID, isolation: isolation, barrierHome: true, groups: groups}
}

// LogGroupSource resolves the Raft group of a barrier tenant's home store: its
// records, log and snapshots under the tenant's key. It refuses, while the
// tenant's barrier is sealed, with the error that barrier's other reads return.
type LogGroupSource interface {
	TenantHomeGroup(ctx context.Context, tenantID string) (Host, uint64, error)
}

// LogBarrier is the tenant barrier a LogBarrierStoreSource serves a
// tenant's logs from: its seal state, read live. Two calls naming the same
// barrier instance return the same (==) value, so a recycled or restored
// barrier is seen as a new one.
type LogBarrier interface {
	IsSealed() bool
}

// LogBarrierStoreSource is the source of a barrier-isolated tenant's log
// store on a node whose tenant has no home group (a standalone node): the
// tenant barrier the node holds for the tenant now and the KV store over it
// (each value encrypted under the tenant's key). The log route consults it
// for an IsolationBarrier tenant only when no home group serves the tenant;
// in Raft mode the home group wins.
type LogBarrierStoreSource interface {
	TenantLogStore(ctx context.Context, tenantID string) (LogBarrier, kvstore.KVStore, error)
}

// BarrierLogKeyspace is the reserved keyspace a barrier tenant's verifiable
// logs occupy inside the tenant barrier on a standalone node. Like
// tenant.HomeName it is a reserved name ("__" first), so neither the home
// store (tenant.HomeName + "/") nor any database of the tenant shares it. It
// is defined in every build, the log feature's or not: a tenant's encryption
// migration re-encrypts the keyspace whichever binary runs it.
const BarrierLogKeyspace = "__vlog"

// LogStoreAdmission admits each operation on a barrier tenant's standalone
// log store through the gate its home store's operations pass
// (tenant.HomeAdmission): an encryption migration fences new operations and
// drains admitted ones before it re-encrypts the tenant's records, the log
// keyspace among them. A LogBarrierStoreSource that implements it is
// consulted before every log read and write; release ends the operation.
type LogStoreAdmission interface {
	AdmitTenantLog(tenantID string) (release func(), err error)
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
		if !r.barrierHome {
			return r.host, r.shardID, nil
		}
		if r.groups == nil {
			return nil, 0, &BarrierStoreUnavailableError{TenantID: routed}
		}
		return r.groups.TenantHomeGroup(ctx, routed)
	default:
		return r.host, r.shardID, nil
	}
}

// TenantLogStore implements LogBarrierStoreSource through the resolver's
// group source when it is one too (a standalone node's, with the tenant
// barriers attached); otherwise the tenant's barrier store is unavailable.
func (r *SystemShardAuditResolver) TenantLogStore(ctx context.Context, tenantID string) (LogBarrier, kvstore.KVStore, error) {
	routed := tenantid.Normalize(tenantID)
	src, ok := r.groups.(LogBarrierStoreSource)
	if !ok {
		return nil, nil, &BarrierStoreUnavailableError{TenantID: routed}
	}
	return src.TenantLogStore(ctx, routed)
}

// AdmitTenantLog implements LogStoreAdmission through the resolver's group
// source when it is one too; otherwise every operation is admitted.
func (r *SystemShardAuditResolver) AdmitTenantLog(tenantID string) (func(), error) {
	if gate, ok := r.groups.(LogStoreAdmission); ok {
		return gate.AdmitTenantLog(tenantid.Normalize(tenantID))
	}
	return func() {}, nil
}
