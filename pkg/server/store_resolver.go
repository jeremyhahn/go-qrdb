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
	tenantSvc    tenant.TenantServicer
	systemTenant string
}

// raftFenceSource is the one method the resolver needs from
// RaftBackendFactory: a dedicated shard's engines, resolved even while its
// replicated placement entry is marked dropping (see FenceServices).
type raftFenceSource interface {
	FenceServices(ctx context.Context, tenantID, db string) (*DatabaseServices, error)
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
	return service.ReclaimTarget{
		KV:     svcs.KV,
		Index:  svcs.Index,
		Vector: svcs.Vector,
	}, true, nil
}

// FenceTarget returns the store holding the (tenantID, db) database's data,
// for its drop fence: the per-tenant Host for a volume-isolated tenant, the
// database's own shard for a raft-isolated one, and dedicated=false -- the
// system store -- for everything else.
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
// the database's writes are not guarded in.
func (r *storeResolver) FenceTarget(ctx context.Context, tenantID, db string) (service.FenceStore, bool, error) {
	if r.tenantSvc == nil || tenantID == "" || tenantID == r.systemTenant {
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
	case tenant.IsolationRaft:
		if r.raft == nil {
			return service.FenceStore{}, true, &rest.IsolationBackendUnavailableError{TenantID: tenantID, Isolation: string(isolation)}
		}
		svcs, err := r.raft.FenceServices(ctx, tenantID, db)
		if errors.Is(err, ErrRaftDatabaseUnknown) {
			return service.FenceStore{PerDatabase: true}, true, nil
		}
		if err != nil {
			return service.FenceStore{}, true, err
		}
		return service.FenceStore{KV: svcs.KV, PerDatabase: true}, true, nil
	default:
		return service.FenceStore{}, false, nil
	}
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
