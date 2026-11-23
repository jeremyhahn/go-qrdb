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
	"slices"
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
// audit stays on the system shard there, except that a barrier tenant whose
// effective isolation policy is strict gets no append there
// (*tenant.SystemAuditRouteError: the chain is under the node key) and is
// refused strict isolation by the tightening probe (SystemAuditRoute).
type SystemShardAuditResolver struct {
	host      Host
	shardID   uint64
	isolation AuditIsolationLookup
	// refuseBarrier also refuses a barrier tenant on a node without a home
	// group; see NewSystemShardLogResolver.
	refuseBarrier bool
	// homes, once attached, routes isolated tenants to their home group.
	homes atomic.Pointer[auditHomeGroupSource]
	// holdMu guards inflight: per tenant, the appends this resolver
	// routed by a recorded system route whose proposal has not returned
	// (HoldAuditAppend). releaseUnchainedSystemRoute retires no route
	// while its count is non-zero.
	holdMu   sync.Mutex
	inflight map[string]int
}

// AuditRouteStore records each barrier tenant's audit route: the
// replicated, linearizable store on the system shard, so every node reads
// the same route and the first writer wins. The lifecycle's system-shard
// kvstore.AtomicKVStore satisfies it.
type AuditRouteStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	// CompareAndDelete removes key only while it still holds value and
	// reports whether it did. It releases a recorded system route that
	// carries no chain once a tightening refuses it.
	CompareAndDelete(ctx context.Context, key string, value []byte) (bool, error)
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

// AuditAppendHolder is a resolver that holds the route an append resolved
// by until its proposal returns (SystemShardAuditResolver.HoldAuditAppend).
// The appender calls release once the proposal has returned, and proposes
// under guard (WithWriteGuard): on a system route it compares the tenant's
// isolation epochs, so an append in flight across a strict tightening is
// refused at apply time; elsewhere it is the zero guard.
type AuditAppendHolder interface {
	HoldAuditAppend(ctx context.Context, tenantID string) (host Host, shardID uint64, release func(), guard WriteGuard, err error)
}

// auditHomeGroupSource boxes the attached source and route store for the
// atomic pointer.
type auditHomeGroupSource struct {
	AuditHomeGroupSource
	routes AuditRouteStore
	// recorded caches the home routes read from or written to routes: a
	// recorded home route never changes. A system route is never cached.
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
//     no route ever changes once recorded, and no record is moved. A strict
//     tenant's append is checked against the route that won the record and
//     fenced against an in-flight tightening by its marker
//     (refuseStrictSystemAppend).
//   - a volume or raft tenant never had a chain on the system shard. Its
//     reads go home; its writes go home once VerifiableLogAudit is active
//     and are refused (*AuditHomeNotActiveError) before.
//
// Every decision is made here, by the proposer, before anything is
// proposed: nothing at apply time depends on it.
func (r *SystemShardAuditResolver) resolve(ctx context.Context, tenantID string, write, chain bool) (Host, uint64, error) {
	host, shardID, _, err := r.resolveRoute(ctx, tenantID, write, chain)
	return host, shardID, err
}

// resolveRoute is resolve, also reporting the recorded audit route a
// barrier tenant's chain resolved by (AuditRouteSystem or AuditRouteHome),
// or "" for every other resolution.
func (r *SystemShardAuditResolver) resolveRoute(ctx context.Context, tenantID string, write, chain bool) (Host, uint64, string, error) {
	routed := tenantid.Normalize(tenantID)
	if tenantid.IsSystem(routed) || r.isolation == nil {
		return r.host, r.shardID, "", nil
	}
	info, err := r.isolation.GetTenant(ctx, routed)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			// No metadata: served as a logical-prefix tenant, the same
			// fall-through tenant.StoreResolver.Resolve performs.
			return r.host, r.shardID, "", nil
		}
		return nil, 0, "", err
	}
	isolation := info.EffectiveIsolationType()
	if homes := r.homes.Load(); homes != nil {
		switch isolation {
		case tenant.IsolationBarrier:
			if !chain {
				host, shardID, err := homes.HomeGroup(ctx, routed)
				return host, shardID, "", err
			}
			return r.barrierRoute(ctx, homes, routed, write)
		case tenant.IsolationVolume, tenant.IsolationRaft:
			if write && !r.auditInstanceActive() {
				return nil, 0, "", &AuditHomeNotActiveError{TenantID: routed}
			}
			host, shardID, err := homes.HomeGroup(ctx, routed)
			return host, shardID, "", err
		}
		return r.host, r.shardID, "", nil
	}
	switch isolation {
	case tenant.IsolationVolume, tenant.IsolationRaft:
		return nil, 0, "", &DedicatedTenantAuditNotSupportedError{
			TenantID:      routed,
			IsolationType: string(info.EffectiveIsolationType()),
		}
	case tenant.IsolationBarrier:
		if r.refuseBarrier {
			return nil, 0, "", &BarrierTenantLogNotSupportedError{TenantID: routed}
		}
		if write {
			// Without a home group the system shard is the only route, and
			// its chain is under the node key: a strict tenant gets no
			// append there (decision S9). Reads stay allowed.
			strict, err := r.strictPolicy(ctx, info)
			if err != nil {
				return nil, 0, "", err
			}
			if strict {
				return nil, 0, "", &tenant.SystemAuditRouteError{TenantID: routed}
			}
		}
		return r.host, r.shardID, "", nil
	default:
		return r.host, r.shardID, "", nil
	}
}

// maxAuditRouteHoldAttempts bounds HoldAuditAppend's re-resolution when
// the system route it resolved is retired before the hold counts itself.
const maxAuditRouteHoldAttempts = 3

// HoldAuditAppend resolves tenantID's audit append as ResolveAuditAppend
// does and, when it resolved by a recorded system route, holds that route
// until release is called: releaseUnchainedSystemRoute does not retire it
// meanwhile, so a tightening's probe (SystemAuditRoute) still reports it.
// The hold re-reads the route after counting itself, immediately before
// the caller proposes; a route retired in between is resolved again.
// release is never nil on success and is safe to call more than once.
//
// The hold is per resolver: a release on another node, or on another
// resolver of this node, does not see it, and a proposal that returned on a
// timeout can still apply. The returned guard backs it at apply time: on a
// system route it compares the tenant's and the floor's isolation epochs,
// read before the strict check, so an append that applies after a
// tightening wrote its epoch is refused on every replica, and one that
// applied before is on the chain the tightening's probe reads. A home route
// or the system tenant gets the zero guard. Guards are enforced only once
// featuregate.GuardedWrites is on (docs/audit/architecture.md).
func (r *SystemShardAuditResolver) HoldAuditAppend(ctx context.Context, tenantID string) (Host, uint64, func(), WriteGuard, error) {
	routed := tenantid.Normalize(tenantID)
	for range maxAuditRouteHoldAttempts {
		// Read before the strict check resolveRoute runs: a tightening
		// writes the epoch before its probe, so an append whose guard holds
		// at apply time applied before the probe looked.
		epochs, guarded, err := r.isolationEpochs(ctx, routed)
		if err != nil {
			return nil, 0, nil, WriteGuard{}, err
		}
		host, shardID, route, err := r.resolveRoute(ctx, routed, true, true)
		if err != nil {
			return nil, 0, nil, WriteGuard{}, err
		}
		homes := r.homes.Load()
		if route != AuditRouteSystem || homes == nil {
			return host, shardID, func() {}, WriteGuard{}, nil
		}
		release := r.holdSystemRoute(routed)
		recorded, err := homes.routes.Get(ctx, AuditRouteKey(routed))
		if err != nil {
			release()
			return nil, 0, nil, WriteGuard{}, err
		}
		if string(recorded) == AuditRouteSystem {
			if !guarded {
				return host, shardID, release, WriteGuard{}, nil
			}
			guard, err := isolationEpochGuard(routed, epochs)
			if err != nil {
				release()
				return nil, 0, nil, WriteGuard{}, err
			}
			return host, shardID, release, guard, nil
		}
		release()
	}
	return nil, 0, nil, WriteGuard{}, &tenant.SystemAuditRouteError{TenantID: routed}
}

// isolationEpochs reads tenantID's and the floor's isolation epochs when the
// lookup can (tenant.IsolationEpochReader) and tenantID is not the system
// tenant; guarded reports whether it read them. A lookup without the reader
// gets no guard, as refuseStrictSystemAppend's optional
// IsolationTightening read does.
func (r *SystemShardAuditResolver) isolationEpochs(ctx context.Context, tenantID string) (map[string][]byte, bool, error) {
	if tenantid.IsSystem(tenantID) {
		return nil, false, nil
	}
	reader, ok := r.isolation.(tenant.IsolationEpochReader)
	if !ok {
		return nil, false, nil
	}
	epochs, err := reader.IsolationEpochs(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	return epochs, true, nil
}

// isolationEpochGuard compares each of tenant.IsolationEpochKeys(tenantID)
// to the value in epochs; an absent key compares as absent.
func isolationEpochGuard(tenantID string, epochs map[string][]byte) (WriteGuard, error) {
	keys := tenant.IsolationEpochKeys(tenantID)
	compares := make([]Compare, 0, len(keys))
	for _, key := range keys {
		compares = append(compares, Compare{
			Key:    []byte(key),
			Target: CompareValue,
			Result: CompareEqual,
			Value:  epochs[key],
		})
	}
	return NewWriteGuard(compares...)
}

// auditGuardRefusal names a system-routed append's refused epoch guard: the
// *tenant.SystemAuditRouteError refuseStrictSystemAppend returns when e is
// on one of tenantID's isolation epoch keys and the tenant is now strict or
// being tightened, else nil (the append reports the retryable refusal).
func (r *SystemShardAuditResolver) auditGuardRefusal(ctx context.Context, tenantID string, e *WriteGuardRefusedError) error {
	routed := tenantid.Normalize(tenantID)
	if e == nil || !slices.Contains(tenant.IsolationEpochKeys(routed), e.Key) {
		return nil
	}
	var refused *tenant.SystemAuditRouteError
	if err := r.refuseStrictSystemAppend(ctx, routed); errors.As(err, &refused) {
		return err
	}
	return nil
}

// holdSystemRoute counts one in-flight append on tenantID's system route
// and returns the func that uncounts it, once.
func (r *SystemShardAuditResolver) holdSystemRoute(tenantID string) func() {
	r.holdMu.Lock()
	if r.inflight == nil {
		r.inflight = make(map[string]int)
	}
	r.inflight[tenantID]++
	r.holdMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.holdMu.Lock()
			defer r.holdMu.Unlock()
			if r.inflight[tenantID]--; r.inflight[tenantID] <= 0 {
				delete(r.inflight, tenantID)
			}
		})
	}
}

// barrierRoute resolves a barrier tenant's audit chain with home groups
// attached; see resolve. An append to a system route by a tenant whose
// effective isolation policy is strict is refused
// (*tenant.SystemAuditRouteError): the system shard's chain is under the
// node key. The strict check runs on the route that won the record, so an
// append that loses the first-writer-wins race to another writer's system
// route is checked too, and it is fenced against an in-flight tightening by
// the tightening's marker (refuseStrictSystemAppend). A refused system route
// that carries no chain is released (releaseUnchainedSystemRoute), so the
// tenant's next append derives its route afresh and is never stranded on a
// route it can no longer append to; reads stay allowed.
func (r *SystemShardAuditResolver) barrierRoute(ctx context.Context, homes *auditHomeGroupSource, tenantID string, write bool) (Host, uint64, string, error) {
	route, err := r.auditRoute(ctx, homes, tenantID, write)
	if err != nil {
		return nil, 0, "", err
	}
	if route == AuditRouteHome {
		if write && !r.auditInstanceActive() {
			return nil, 0, "", &AuditHomeNotActiveError{TenantID: tenantID}
		}
		host, shardID, err := homes.HomeGroup(ctx, tenantID)
		return host, shardID, route, err
	}
	if r.refuseBarrier {
		return nil, 0, "", &BarrierTenantLogNotSupportedError{TenantID: tenantID}
	}
	if write {
		// route is the recorded winner: this writer's own, or another
		// writer's that won the first-writer-wins record.
		if err := r.refuseStrictSystemAppend(ctx, tenantID); err != nil {
			return nil, 0, "", r.releaseUnchainedSystemRoute(ctx, homes, tenantID, err)
		}
	}
	return r.host, r.shardID, route, nil
}

// refuseStrictSystemAppend refuses (*tenant.SystemAuditRouteError) an append
// to the system shard's chain, which is under the node key, by a tenant whose
// effective isolation policy is strict or is being tightened to strict
// (decision S9). It is the append's half of the handshake tightenToStrict
// runs: the tightening writes its marker and then probes SystemAuditRoute,
// and this reads the marker and then the tenant afresh. Either the
// tightening sees the recorded system route and refuses, or this sees the
// marker or the committed strict policy and refuses. auditRoute runs it
// again after its system claim lands, so a claim recorded after the
// tightening's probe is always seen; a refused claim with no chain behind it
// is released (releaseUnchainedSystemRoute). A read that fails is returned:
// the append fails closed.
func (r *SystemShardAuditResolver) refuseStrictSystemAppend(ctx context.Context, tenantID string) error {
	refused := &tenant.SystemAuditRouteError{TenantID: tenantID}
	if reader, ok := r.isolation.(tenant.IsolationTighteningReader); ok {
		tightening, err := reader.IsolationTightening(ctx, tenantID)
		if err != nil {
			return err
		}
		if tightening {
			return refused
		}
	}
	// Fresh: a tightening that removed its marker after the read above
	// committed strict before removing it.
	info, err := r.isolation.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	strict, err := r.strictPolicy(ctx, info)
	if err != nil {
		return err
	}
	if strict {
		return refused
	}
	return nil
}

// releaseUnchainedSystemRoute handles refused, the strict refusal of an
// append to tenantID's recorded system route. When the system shard holds no
// chain for the tenant the route carries nothing, so it is released: the
// recorded "system" is compare-and-deleted and the cached entry dropped, and
// the next append derives its route again (home once the audit instance is
// active). A chain already on the system shard keeps its route (S9). It
// returns refused, or the error of a read or release that failed.
func (r *SystemShardAuditResolver) releaseUnchainedSystemRoute(ctx context.Context, homes *auditHomeGroupSource, tenantID string, refused error) error {
	var routeErr *tenant.SystemAuditRouteError
	if !errors.As(refused, &routeErr) {
		return refused
	}
	hasChain, err := r.hasSystemChain(ctx, tenantID)
	if err != nil {
		return err
	}
	if hasChain {
		return refused
	}
	// A zero committed head does not prove no append is in flight: an
	// append held on the route (HoldAuditAppend) may still propose to the
	// system shard, so the route stays while one is held. The hold takes
	// holdMu to count itself and then re-reads the route, so it either
	// counts before this check or sees the route gone.
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	if r.inflight[tenantID] > 0 {
		return refused
	}
	if _, err := homes.routes.CompareAndDelete(ctx, AuditRouteKey(tenantID), []byte(AuditRouteSystem)); err != nil {
		return err
	}
	return refused
}

// auditRoute returns tenantID's recorded route or, when none is recorded,
// derives one; a write records the derived route, first writer wins. A
// fresh system route is checked by refuseStrictSystemAppend before the claim
// and again after a won claim lands, so a tightening that committed between
// the two cannot leave a strict tenant recorded on the system route: the
// post-claim refusal releases it (releaseUnchainedSystemRoute).
func (r *SystemShardAuditResolver) auditRoute(ctx context.Context, homes *auditHomeGroupSource, tenantID string, write bool) (string, error) {
	// Only a home route is cached: it is never released. A system route
	// can be retired by any resolver sharing the route store
	// (releaseUnchainedSystemRoute) and re-recorded home, so it is read
	// from the store on every call.
	if cached, ok := homes.recorded.Load(tenantID); ok {
		if route, ok := cached.(string); ok && route == AuditRouteHome {
			return route, nil
		}
		// Not a home route: drop it and read the recorded one.
		homes.recorded.Delete(tenantID)
	}
	key := AuditRouteKey(tenantID)
	recorded, err := homes.routes.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if recorded != nil {
		if string(recorded) == AuditRouteHome {
			homes.recorded.Store(tenantID, AuditRouteHome)
		}
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
	checkSystem := route == AuditRouteSystem && !r.refuseBarrier
	if checkSystem {
		// A strict tenant never records a fresh system route. A loss to
		// another writer's recorded system route is checked by barrierRoute.
		if err := r.refuseStrictSystemAppend(ctx, tenantID); err != nil {
			return "", err
		}
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
	} else if checkSystem {
		// The claim landed: check it again, so a tightening that committed
		// while this writer paused before the claim is seen.
		if err := r.refuseStrictSystemAppend(ctx, tenantID); err != nil {
			return "", r.releaseUnchainedSystemRoute(ctx, homes, tenantID, err)
		}
	}
	if route == AuditRouteHome {
		homes.recorded.Store(tenantID, route)
	}
	return route, nil
}

// strictPolicy reports whether info's effective isolation policy is strict:
// its own policy, or the cluster floor when the isolation lookup reads one
// (tenant.ClusterIsolationPolicyReader).
func (r *SystemShardAuditResolver) strictPolicy(ctx context.Context, info *tenant.TenantInfo) (bool, error) {
	var floor tenant.ClusterIsolationPolicy
	if reader, ok := r.isolation.(tenant.ClusterIsolationPolicyReader); ok {
		var err error
		if floor, err = reader.ClusterIsolationPolicy(ctx); err != nil {
			return false, err
		}
	}
	return tenant.EffectiveIsolationPolicy(floor, info.IsolationPolicy) == tenant.ClusterIsolationPolicyStrict, nil
}

// SystemAuditRoute implements tenant.AuditRouteProbe: it reports whether
// tenantID is a barrier tenant whose audit chain is routed to the system
// shard -- a recorded system route or, with none recorded, a chain that
// already has records there. On a node without a home group a barrier
// tenant's appends can only go to the system shard, so it is always on a
// system route there, chain or not. It only reads: no route is recorded. A
// tenant that is not a barrier tenant is never on a system route.
func (r *SystemShardAuditResolver) SystemAuditRoute(ctx context.Context, tenantID string) (bool, error) {
	routed := tenantid.Normalize(tenantID)
	if tenantid.IsSystem(routed) || r.isolation == nil {
		return false, nil
	}
	info, err := r.isolation.GetTenant(ctx, routed)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			return false, nil
		}
		return false, err
	}
	if info.EffectiveIsolationType() != tenant.IsolationBarrier {
		return false, nil
	}
	if homes := r.homes.Load(); homes != nil {
		recorded, err := homes.routes.Get(ctx, AuditRouteKey(routed))
		if err != nil {
			return false, err
		}
		if recorded != nil {
			return string(recorded) == AuditRouteSystem, nil
		}
		return r.hasSystemChain(ctx, routed)
	}
	// No home group: every append goes to the system shard.
	return true, nil
}

var _ tenant.AuditRouteProbe = (*SystemShardAuditResolver)(nil)

// hasSystemChain reports whether tenantID's replicated audit chain already
// has records on the system shard.
func (r *SystemShardAuditResolver) hasSystemChain(ctx context.Context, tenantID string) (bool, error) {
	if r.host == nil {
		// No system shard is wired, so it holds no chain.
		return false, nil
	}
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
