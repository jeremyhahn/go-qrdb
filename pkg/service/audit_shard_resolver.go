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
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
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

// AuditRouteStore records each barrier tenant's audit route: the
// replicated, linearizable store on the system shard, so every node reads
// the same route and the first writer wins. The lifecycle's system-shard
// kvstore.AtomicKVStore satisfies it.
type AuditRouteStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
}

// The recorded audit routes of a barrier tenant.
const (
	// AuditRouteSystem keeps the tenant's audit chain on the system shard,
	// where its chain already had records before home groups existed.
	AuditRouteSystem = "system"
	// AuditRouteHome puts the tenant's audit chain in its home group.
	AuditRouteHome = "home"
)

// auditRoutePrefix is the reserved system key prefix of the recorded routes.
const auditRoutePrefix = "__sys:audit/route/"

// AuditRouteKey is the system key that records tenantID's audit route.
func AuditRouteKey(tenantID string) string { return auditRoutePrefix + tenantID }

// AuditAppendResolver is implemented by a resolver whose write route can
// differ from its read route: ResolveAuditAppend names the shard an event
// is proposed to, recording the route first when none is recorded.
// ReplicatedLogger.Emit uses it when the resolver implements it.
type AuditAppendResolver interface {
	ResolveAuditAppend(ctx context.Context, tenantID string) (Host, uint64, error)
}

// auditHomeGroupSource boxes the attached source and route store for the
// atomic pointer.
type auditHomeGroupSource struct {
	AuditHomeGroupSource
	routes AuditRouteStore
	// recorded caches the routes read from or written to routes: a recorded
	// route never changes.
	recorded sync.Map
}

// SetHomeGroupSource attaches the home-group source isolated tenants
// resolve to and the store their audit routes are recorded in. The
// lifecycle calls it once the node's Raft tenant homes are built; a nil
// source or store detaches it.
func (r *SystemShardAuditResolver) SetHomeGroupSource(source AuditHomeGroupSource, routes AuditRouteStore) {
	if source == nil || routes == nil {
		r.homes.Store(nil)
		return
	}
	r.homes.Store(&auditHomeGroupSource{AuditHomeGroupSource: source, routes: routes})
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

// ResolveAuditShard implements AuditShardResolver. It is the read route:
// Query and Verify of the audit chain, and every verifiable log but the
// audit instance (see ResolveAuditInstance). It never records a route.
func (r *SystemShardAuditResolver) ResolveAuditShard(ctx context.Context, tenantID string) (Host, uint64, error) {
	return r.resolve(ctx, tenantID, false, !r.refuseBarrier)
}

// ResolveAuditAppend implements AuditAppendResolver: the route an audit
// event is proposed on. For a barrier tenant with home groups attached it
// records the route before the first event, so the route is a replicated
// fact rather than a per-node guess.
func (r *SystemShardAuditResolver) ResolveAuditAppend(ctx context.Context, tenantID string) (Host, uint64, error) {
	return r.resolve(ctx, tenantID, true, true)
}

// ResolveAuditInstance resolves tenantID's audit instance (system/audit)
// on the log surface: the audit chain's route, so a barrier tenant whose
// chain is recorded on the system shard is refused there
// (*BarrierTenantLogNotSupportedError) rather than answered by an empty
// instance in its home group.
func (r *SystemShardAuditResolver) ResolveAuditInstance(ctx context.Context, tenantID string) (Host, uint64, error) {
	return r.resolve(ctx, tenantID, false, true)
}

// resolve names tenantID's route. With home groups attached:
//
//   - a barrier tenant follows its audit route when chain is set. A recorded
//     route wins. Without one, a tenant whose replicated chain already has
//     records on the system shard keeps it there; any other tenant goes home
//     once featuregate.VerifiableLogAudit is active on the system shard,
//     and stays on the system shard before. A write records what it picks;
//     no route ever changes once recorded, and no record is moved.
//   - a volume or raft tenant never had a chain on the system shard. Its
//     reads go home; its writes go home once VerifiableLogAudit is active
//     and are refused (*AuditHomeNotActiveError) before.
//
// Every decision is made here, by the proposer, before anything is
// proposed: nothing at apply time depends on it.
func (r *SystemShardAuditResolver) resolve(ctx context.Context, tenantID string, write, chain bool) (Host, uint64, error) {
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
		case tenant.IsolationBarrier:
			if !chain {
				return homes.HomeGroup(ctx, routed)
			}
			return r.barrierRoute(ctx, homes, routed, write)
		case tenant.IsolationVolume, tenant.IsolationRaft:
			if write && !r.auditInstanceActive() {
				return nil, 0, &AuditHomeNotActiveError{TenantID: routed}
			}
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

// barrierRoute resolves a barrier tenant's audit chain with home groups
// attached; see resolve.
func (r *SystemShardAuditResolver) barrierRoute(ctx context.Context, homes *auditHomeGroupSource, tenantID string, write bool) (Host, uint64, error) {
	route, err := r.auditRoute(ctx, homes, tenantID, write)
	if err != nil {
		return nil, 0, err
	}
	if route == AuditRouteHome {
		if write && !r.auditInstanceActive() {
			return nil, 0, &AuditHomeNotActiveError{TenantID: tenantID}
		}
		return homes.HomeGroup(ctx, tenantID)
	}
	if r.refuseBarrier {
		return nil, 0, &BarrierTenantLogNotSupportedError{TenantID: tenantID}
	}
	return r.host, r.shardID, nil
}

// auditRoute returns tenantID's recorded route or, when none is recorded,
// derives one; a write records the derived route, first writer wins.
func (r *SystemShardAuditResolver) auditRoute(ctx context.Context, homes *auditHomeGroupSource, tenantID string, write bool) (string, error) {
	if cached, ok := homes.recorded.Load(tenantID); ok {
		if route, ok := cached.(string); ok {
			return route, nil
		}
		// Not a route: drop it and read the recorded one.
		homes.recorded.Delete(tenantID)
	}
	key := AuditRouteKey(tenantID)
	recorded, err := homes.routes.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if recorded != nil {
		homes.recorded.Store(tenantID, string(recorded))
		return string(recorded), nil
	}
	route := AuditRouteSystem
	if r.auditInstanceActive() {
		hasChain, chainErr := r.hasSystemChain(ctx, tenantID)
		if chainErr != nil {
			return "", chainErr
		}
		if !hasChain {
			route = AuditRouteHome
		}
	}
	if !write {
		return route, nil
	}
	won, err := homes.routes.PutIfAbsent(ctx, key, []byte(route))
	if err != nil {
		return "", err
	}
	if !won {
		if recorded, err = homes.routes.Get(ctx, key); err != nil {
			return "", err
		}
		route = string(recorded)
	}
	homes.recorded.Store(tenantID, route)
	return route, nil
}

// hasSystemChain reports whether tenantID's replicated audit chain already
// has records on the system shard.
func (r *SystemShardAuditResolver) hasSystemChain(ctx context.Context, tenantID string) (bool, error) {
	result, err := r.host.SyncRead(ctx, r.shardID, audit.VerifyRequest{TenantID: tenantID})
	if err != nil {
		return false, &ReadError{Err: err}
	}
	report, ok := result.(*audit.ChainContinuityReport)
	if !ok {
		return false, &unexpectedResultTypeError{}
	}
	return report.Head > 0, nil
}

// auditInstanceActive reports whether featuregate.VerifiableLogAudit is
// active on the system shard: every replica advertises the audit instance.
// A host that gates nothing is a single-binary composition, where it is.
func (r *SystemShardAuditResolver) auditInstanceActive() bool {
	gated, ok := r.host.(FeatureGatedHost)
	return !ok || gated.FeatureEnabled(r.shardID, featuregate.VerifiableLogAudit)
}
