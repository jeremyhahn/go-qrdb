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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// volumeStoreResolver tells the DROP DATABASE reclamation
// (service.KeyspaceReclaimService) which store holds a tenant's
// composite-namespaced keys.
//
// Only ONE isolation mode moves them off the system store: IsolationVolume,
// whose databases share a dedicated per-tenant Host. The others either share
// the system store (prefix and barrier isolation, and the system tenant) or
// give the database a store of its own, where the composite prefix is never
// written -- so scanning the system store for it finds nothing and costs one
// read per prefix. That is why the answer is a single boolean rather than a
// full copy of restServiceRegistry.resolve's branching: the reclamation needs
// to know where the keys ARE, not which service set may serve a request.
type volumeStoreResolver struct {
	hosts        tenantServicesSource
	tenantSvc    tenant.TenantServicer
	systemTenant string
}

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
var _ service.StoreResolver = (*volumeStoreResolver)(nil)

// ReclaimTarget returns the per-tenant Host's engines for a volume-isolated
// tenant, and reports dedicated=false for every other tenant so the caller
// uses the system store.
//
// An unknown tenant is NOT an error. A database outlives the tenant record in
// exactly the case that matters -- a teardown -- and refusing to reclaim
// because the record has already gone would leak the keyspace permanently.
// The system store is the right answer there, because a tenant that was never
// provisioned was never given a Host.
func (r *volumeStoreResolver) ReclaimTarget(ctx context.Context, tenantID string) (service.ReclaimTarget, bool, error) {
	if isNilTenantServices(r.hosts) || r.tenantSvc == nil || tenantID == "" || tenantID == r.systemTenant {
		return service.ReclaimTarget{}, false, nil
	}
	info, err := r.tenantSvc.GetTenant(ctx, tenantID)
	if err != nil || info == nil || info.EffectiveIsolationType() != tenant.IsolationVolume {
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
