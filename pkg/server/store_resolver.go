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

package server

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// storeResolver tells DROP DATABASE (service.KeyspaceReclaimService) which
// store holds a database's data: where its keys are reclaimed from, and where
// its drop fence is written.
//
// For the reclamation only ONE isolation mode moves keys off the system
// store: IsolationVolume, whose databases share a dedicated per-tenant Host.
// The others either share the system store (prefix and barrier isolation,
// and the system tenant) or give the database a store of its own, where the
// composite prefix is never written -- so scanning the system store for it
// finds nothing and costs one read per prefix.
//
// The drop fence adds IsolationRaft: a raft-isolated database's writes are
// proposed to its own shard and carry their guard there, so the fence that
// guard reads has to be written into that shard, not the system store.
//
// That is why the answers are a store and a boolean rather than a full copy
// of restServiceRegistry.resolve's branching: the teardown needs to know
// where the data IS, not which service set may serve a request.
type storeResolver struct {
	hosts        tenantServicesSource
	raft         raftFenceSource
	barrier      homeFenceSource
	databases    ownedDatabaseSource
	tenantSvc    tenant.TenantServicer
	systemTenant string
	// evictBundles drops (tenantID, db)'s cached service bundles from
	// every transport's registry once its drop fence target is resolved.
	evictBundles func(tenantID, db string)
}

// ownedDatabaseSource is the catalog FenceTarget asks for a database the
// Manager has open (database.Manager.GetOwnedIgnoringFence: the drop
// asks after its local fence is set), to find one materialized in its own
// store.
type ownedDatabaseSource interface {
	GetOwnedIgnoringFence(name, tenantID string) (*database.Database, error)
}

// materializedFenceTarget answers FenceTarget for a database the Manager
// opened in its own store (a materializedStore backend): its drop fence lives
// in that store, where its writes and its scheduled compactions are guarded.
// The store is the incarnation -- a drop removes its binding root and a
// create under the name opens a new one -- so it is a per-database target,
// as a dedicated-raft database's shard is: the marker is absent while the
// database is live. A database open under another incarnation is refused with
// *database.DropSupersededError. ok is false for any other database, and for
// one not open yet: a create marks its incarnation live before the Manager
// holds it, in the store a per-database drop tombstones (claimReclaimStore).
//
// The lookup fails closed: only *database.NotFoundError (no record) and
// *database.AlreadyExistsError (a create still holds the name's claim, so
// Open lost to it) mean "not materialized". Any other lookup error
// -- a catalog read that failed, a store that did not answer -- is returned
// with ok true, so the drop fails instead of falling through to the shared
// store and tombstoning a marker the database never reads while its own
// marker stays live.
func (r *storeResolver) materializedFenceTarget(tenantID, db, incarnation string) (service.FenceStore, bool, error) {
	if r.databases == nil || db == "" {
		return service.FenceStore{}, false, nil
	}
	dbase, err := r.databases.GetOwnedIgnoringFence(db, tenantID)
	var notFound *database.NotFoundError
	var creating *database.AlreadyExistsError
	if errors.As(err, &notFound) || errors.As(err, &creating) {
		return service.FenceStore{}, false, nil
	}
	if err != nil {
		return service.FenceStore{}, true, err
	}
	if dbase == nil {
		return service.FenceStore{}, false, nil
	}
	ms, ok := dbase.Backend().(materializedStore)
	if !ok {
		return service.FenceStore{}, false, nil
	}
	if incarnation != "" && dbase.Incarnation() != incarnation {
		return service.FenceStore{}, true, &database.DropSupersededError{Name: db, TenantID: tenantID}
	}
	return service.FenceStore{KV: service.NewKVService(ms.Host(), ms.ShardID()), PerDatabase: true}, true, nil
}

// raftFenceSource is the one method the resolver needs from
// RaftBackendFactory: a dedicated shard's engines, resolved even while its
// replicated placement entry is marked dropping (see FenceServices).
type raftFenceSource interface {
	FenceServices(ctx context.Context, tenantID, db, incarnation string) (*DatabaseServices, error)
}

// homeFenceSource is the one method the resolver needs from
// RaftBackendFactory for a barrier tenant: the engines of the tenant's home
// group (database.TenantHomeName), the group the tenant's logs are proposed
// to (service.LogGroupSource.TenantHomeGroup) and so the group whose state
// machine evaluates their drop fence (OpGuarded).
type homeFenceSource interface {
	Services(ctx context.Context, tenantID, db string) (*DatabaseServices, error)
}

// raftGroupSource is the raft fence source that also serves non-raft
// tenants' raft databases from their own groups (*RaftBackendFactory).
type raftGroupSource interface {
	// HostsDatabase reports whether a non-raft tenant's database runs its own Raft group.
	// A placement that names only other nodes, or cannot be read, is an
	// error (*placement.NotAReplicaError), never "not a group".
	HostsDatabase(tenantID, db string) (bool, error)
}

// Compile-time check: the production source is the factory.
var _ raftFenceSource = (*RaftBackendFactory)(nil)

// tenantServicesSource is the one method the resolver needs from
// TenantHostRegistry: resolve a tenant's per-tenant Host to its un-namespaced
// data-plane engines. Taking the narrow interface rather than the concrete
// registry is what lets the routing decision be tested without standing up a
// per-tenant barrier, a Raft Host and an on-disk volume.
type tenantServicesSource interface {
	Services(ctx context.Context, tenantID string) (*DatabaseServices, error)
}

// Compile-time check: the production source is the registry.
var _ tenantServicesSource = (*TenantHostRegistry)(nil)

// Compile-time interface check.
var _ service.StoreResolver = (*storeResolver)(nil)

// ReclaimTarget returns the per-tenant Host's engines for a volume-isolated
// tenant, and reports dedicated=false for every other tenant so the caller
// uses the system store.
//
// An unknown tenant (tenant.ErrTenantNotFound) is NOT an error. A database
// outlives the tenant record in exactly the case that matters -- a teardown
// -- and refusing to reclaim because the record has already gone would leak
// the keyspace permanently. The system store is the right answer there,
// because a tenant that was never provisioned was never given a Host. Any
// other lookup failure is returned.
func (r *storeResolver) ReclaimTarget(ctx context.Context, tenantID string) (service.ReclaimTarget, bool, error) {
	if isNilTenantServices(r.hosts) || r.tenantSvc == nil || tenantID == "" || tenantID == r.systemTenant {
		return service.ReclaimTarget{}, false, nil
	}
	info, err := r.tenantSvc.GetTenant(ctx, tenantID)
	if errors.Is(err, tenant.ErrTenantNotFound) {
		return service.ReclaimTarget{}, false, nil
	}
	if err != nil {
		// A lookup that FAILED is not a tenant that does not exist: the
		// tenant may well be volume-isolated, and scanning the system store
		// for its keys would report a drop over data still on its Host.
		return service.ReclaimTarget{}, true, err
	}
	if info == nil || info.EffectiveIsolationType() != tenant.IsolationVolume {
		return service.ReclaimTarget{}, false, nil
	}
	svcs, err := r.hosts.Services(ctx, tenantID)
	if err != nil {
		// The tenant IS volume-isolated and its Host is unreachable, so the
		// system store is the wrong place to look and reporting success would
		// report a drop over data that is still there. Surface it: the drop
		// fails, the database and its data are untouched, and re-issuing the
		// drop once the Host is available completes it.
		return service.ReclaimTarget{}, true, err
	}
	hostCompaction, _ := svcs.MVCC.(service.CompactionStateStore)
	return service.ReclaimTarget{
		KV:         svcs.KV,
		Index:      svcs.Index,
		Vector:     svcs.Vector,
		Compaction: hostCompaction,
	}, true, nil
}

// FenceTarget returns the store holding the (tenantID, db) database's data,
// for its drop fence: the per-tenant Host for a volume-isolated tenant, the
// database's own shard for a raft-isolated one, the tenant's home group for
// a barrier-isolated one on a node that has it (its logs are proposed and
// fenced there; go-qrdb#1655), and dedicated=false -- the system store --
// for everything else.
//
// Only a tenant that does not exist (tenant.ErrTenantNotFound) falls back
// to the system store, for the reason ReclaimTarget gives: a database
// outlives its tenant record exactly during a teardown, and a tenant never
// provisioned was never given a store of its own. Any OTHER lookup failure
// is returned: answering it with the system store would fence, or mark
// live, a store the database's writes are not guarded in. So is an
// isolation mode whose store this composition cannot reach.
//
// A raft-isolated database whose shard is unknown to this node reports a
// dedicated store that does not exist. That is the state a database is in
// while it is being created, before its shard is placed, and there is no
// marker to set in a shard that does not exist yet; a drop refuses it (see
// KeyspaceReclaimService.FenceDatabaseKeyspace) rather than fencing a store
// the database's writes are not guarded in. A shard the name resolves to
// while the catalog records another incarnation, or none, is not
// incarnation's: it is refused with *database.DropSupersededError (see
// RaftBackendFactory.FenceServices).
func (r *storeResolver) FenceTarget(ctx context.Context, tenantID, db, incarnation string) (service.FenceStore, bool, error) {
	store, ok, err := r.fenceTarget(ctx, tenantID, db, incarnation)
	if err == nil && r.evictBundles != nil {
		// The drop fences the database here: from now on no request may
		// be served from a bundle cached over its store, so every cache
		// drops and closes its entry (a recreate builds a fresh one).
		r.evictBundles(tenantID, db)
	}
	return store, ok, err
}

// fenceTarget is FenceTarget without the bundle eviction.
func (r *storeResolver) fenceTarget(ctx context.Context, tenantID, db, incarnation string) (service.FenceStore, bool, error) {
	if store, ok, err := r.materializedFenceTarget(tenantID, db, incarnation); ok {
		return store, true, err
	}
	if tenantID != "" && tenantID == r.systemTenant {
		// A system database with its own Raft group is fenced in that group.
		if groups, ok := r.raft.(raftGroupSource); ok && groups != nil {
			hosted, err := groups.HostsDatabase(tenantID, db)
			if err != nil {
				return service.FenceStore{}, true, err
			}
			if hosted {
				return r.raftFenceTarget(ctx, tenantID, db, incarnation, tenant.IsolationPrefix)
			}
		}
		return service.FenceStore{}, false, nil
	}
	if r.tenantSvc == nil || tenantID == "" {
		return service.FenceStore{}, false, nil
	}
	info, err := r.tenantSvc.GetTenant(ctx, tenantID)
	if errors.Is(err, tenant.ErrTenantNotFound) {
		return service.FenceStore{}, false, nil
	}
	if err != nil {
		return service.FenceStore{}, true, err
	}
	if info == nil {
		return service.FenceStore{}, false, nil
	}
	isolation := info.EffectiveIsolationType()
	group := isolation == tenant.IsolationRaft
	if groups, ok := r.raft.(raftGroupSource); ok && !group && groups != nil {
		// OD-1 A: a prefix, barrier or volume tenant's backend: raft database
		// runs its own group, so its fence goes there -- the same rule the
		// request path applies (restServiceRegistry.isRaftGroupDatabase).
		group, err = groups.HostsDatabase(tenantID, db)
		if err != nil {
			return service.FenceStore{}, true, err
		}
	}
	if group {
		return r.raftFenceTarget(ctx, tenantID, db, incarnation, isolation)
	}
	switch isolation {
	case tenant.IsolationVolume:
		if isNilTenantServices(r.hosts) {
			return service.FenceStore{}, true, &rest.IsolationBackendUnavailableError{TenantID: tenantID, Isolation: string(isolation)}
		}
		svcs, err := r.hosts.Services(ctx, tenantID)
		if err != nil {
			return service.FenceStore{}, true, err
		}
		return service.FenceStore{KV: svcs.KV}, true, nil
	default:
		// Prefix and barrier isolation: a barrier tenant's database data
		// is served from the system store (restServiceRegistry.resolveShared)
		// and guarded there by the system-store marker. Its logs are fenced
		// in its home group, by LogFenceTarget.
		return service.FenceStore{}, false, nil
	}
}

// LogFenceTarget returns the store holding the drop-fence marker the
// (tenantID, db) database's verifiable-log writes are guarded by, when that
// is not the store FenceTarget answers. A barrier tenant's logs are proposed
// to its home group, whose state machine evaluates their drop fence, so the
// log marker goes there; the home group holds every one of the tenant's
// databases, so it is not per-database. A node with no home group (no Raft
// backend, or the group unknown) proposes them to the tenant barrier's own
// store, which checks the incarnation is live rather than reading a marker,
// and answers false like every other isolation: no second marker.
func (r *storeResolver) LogFenceTarget(ctx context.Context, tenantID, db, incarnation string) (service.FenceStore, bool, error) {
	if r.tenantSvc == nil || r.barrier == nil || tenantID == "" || tenantID == r.systemTenant {
		return service.FenceStore{}, false, nil
	}
	info, err := r.tenantSvc.GetTenant(ctx, tenantID)
	if errors.Is(err, tenant.ErrTenantNotFound) {
		return service.FenceStore{}, false, nil
	}
	if err != nil {
		return service.FenceStore{}, true, err
	}
	if info == nil || info.EffectiveIsolationType() != tenant.IsolationBarrier {
		return service.FenceStore{}, false, nil
	}
	svcs, err := r.barrier.Services(ctx, tenantID, database.TenantHomeName)
	if errors.Is(err, ErrRaftDatabaseUnknown) {
		return service.FenceStore{}, false, nil
	}
	if err != nil {
		return service.FenceStore{}, true, err
	}
	return service.FenceStore{KV: svcs.KV}, true, nil
}

// raftFenceTarget is the fence store of a database that runs its own Raft
// group: the group's own store, fenced per database.
func (r *storeResolver) raftFenceTarget(ctx context.Context, tenantID, db, incarnation string, isolation tenant.IsolationType) (service.FenceStore, bool, error) {
	if r.raft == nil {
		return service.FenceStore{}, true, &rest.IsolationBackendUnavailableError{TenantID: tenantID, Isolation: string(isolation)}
	}
	svcs, err := r.raft.FenceServices(ctx, tenantID, db, incarnation)
	if errors.Is(err, ErrRaftDatabaseUnknown) {
		return service.FenceStore{PerDatabase: true}, true, nil
	}
	if err != nil {
		return service.FenceStore{}, true, err
	}
	return service.FenceStore{KV: svcs.KV, PerDatabase: true}, true, nil
}

// isNilTenantServices reports whether the source is absent, including the
// typed-nil case a *TenantHostRegistry stored in the interface produces when
// the server was configured without one.
func isNilTenantServices(s tenantServicesSource) bool {
	if s == nil {
		return true
	}
	reg, ok := s.(*TenantHostRegistry)
	return ok && reg == nil
}
