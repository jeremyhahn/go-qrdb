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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TenantHostRegistryHolder is a late-bound, concurrency-safe pointer to a
// *TenantHostRegistry, for a composer that must wire
// seal.WithCloseStoresFunc before the registry it closes through exists.
//
// Decision D6's build order (docs/volume-pools/architecture.md) requires
// seal.VolumeLifecycle to be constructed BEFORE the node's BarrierRegistry,
// which is itself built before BuildTenantInfra constructs the
// TenantHostRegistry -- so the real closer a composer wires into
// seal.WithCloseStoresFunc cannot capture the registry directly at
// VolumeLifecycle construction time. A holder closes that gap: the
// composer creates it up front, wires a closure that reads it (via
// CloseTenantStoresForPool) into WithCloseStoresFunc, and calls Store once
// BuildTenantInfra returns. The zero value holds nothing, and
// CloseTenantStoresForPool treats a nil load as "nothing built yet to
// close," not an error -- matching a seal event reached before boot
// finishes building the tenant infrastructure.
type TenantHostRegistryHolder struct {
	p atomic.Pointer[TenantHostRegistry]
}

// Store publishes registry as the holder's current value. Called once,
// after BuildTenantInfra returns its Hosts field.
func (h *TenantHostRegistryHolder) Store(registry *TenantHostRegistry) {
	if h == nil {
		return
	}
	h.p.Store(registry)
}

// Load returns the holder's current value, or nil before Store has run.
func (h *TenantHostRegistryHolder) Load() *TenantHostRegistry {
	if h == nil {
		return nil
	}
	return h.p.Load()
}

// CloseTenantStoresForPool closes every per-tenant store this node has
// open for a tenant assigned to pool -- the real seal.WithCloseStoresFunc
// implementation every composer (cmd/qrdbd, sdk/go/node) wires into
// seal.NewVolumeLifecycle. VolumeLifecycle runs it BEFORE it locks pool's
// Lockable volume (decision D6): "stores on the volume are closed -> ...
// locks ... -> the system barrier's root key is wiped"
// (docs/volume-pools/architecture.md).
//
// hosts.Close is a no-op for a tenant with no live Host open: an
// IsolationRaft tenant, which runs its shard on the shared system Host and
// never has a per-tenant one, or an IsolationVolume tenant whose Host was
// never built on this node. Every assigned tenant is therefore passed
// through Close uniformly, without asking which isolation type it is.
//
// allocator is consulted fresh on every call (not captured once at
// construction) because WithVolumeAllocator attaches it to the
// BarrierRegistry slightly after VolumeLifecycle is built but well before
// any seal event can occur; a nil allocator -- no volume pool catalog on
// this node -- is a no-op, matching hosts == nil. A nil hosts -- this
// node's IsolationVolume registry was never built (volume pools not
// configured, or the holder has not been Store'd yet) -- is a no-op for
// the same reason: nothing has a per-tenant store open to close.
func CloseTenantStoresForPool(ctx context.Context, allocator volume.Allocator, hosts *TenantHostRegistry, pool string) error {
	if hosts == nil || allocator == nil {
		return nil
	}
	assignments, err := allocator.ListAll(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range assignments {
		if a.Pool != pool {
			continue
		}
		if cerr := hosts.Close(a.TenantID); cerr != nil {
			errs = append(errs, cerr)
		}
	}
	return errors.Join(errs...)
}
