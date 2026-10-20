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

package tenant

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// HomeName is the name of an isolated tenant's home store. In raft mode it
// names the tenant's home group (database.TenantHomeName is this constant);
// on a standalone node it is the keyspace the home store occupies inside the
// tenant barrier. Because it is a reserved database name, no database of the
// tenant can share that keyspace.
const HomeName = "__home"

// LocalHomes is the HomeStoreSource of a standalone node. An isolated
// tenant's home store is its own barrier's backend, at the tenant's data
// root, under the HomeName keyspace: every value is encrypted under the
// tenant's key and none is in the system shard. It is the standalone half
// of the one HomeStore seam; raft mode attaches the home groups instead.
type LocalHomes struct {
	registry barrierLookup

	// admission, when set, admits every operation on the stores HomeStore
	// builds; see WithHomeAdmission.
	admission HomeAdmission

	// stores caches the store built over each tenant's current barrier,
	// keyed by tenant ID; see HomeStore.
	stores sync.Map // map[string]*localHomeEntry
}

// localHomeEntry is one cached home store and the barrier it was built over.
type localHomeEntry struct {
	barrier *seal.TenantBarrier
	store   kvstore.KVStore

	// guarded is the barrier's own KV store an admitted store runs each
	// operation against; nil when LocalHomes has no admission.
	guarded barrierHomeKVStore
}

// Compile-time interface check.
var _ HomeStoreSource = (*LocalHomes)(nil)

// NewLocalHomes returns the standalone home-store source over registry,
// configured by opts. It returns ErrNilRegistry when registry is nil.
func NewLocalHomes(registry *seal.BarrierRegistry, opts ...LocalHomesOption) (*LocalHomes, error) {
	if registry == nil {
		return nil, ErrNilRegistry
	}
	h := &LocalHomes{registry: registry}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// EnsureHome implements HomeStoreSource. The store is the tenant barrier's
// backend, which exists once the barrier is registered, so there is nothing
// to provision: it reports whether this node holds the tenant's barrier.
func (h *LocalHomes) EnsureHome(_ context.Context, tenantID string) error {
	_, err := h.registry.GetTenant(tenantID)
	return err
}

// HomeStore implements HomeStoreSource. It fails where this node holds no
// barrier for the tenant; a sealed barrier surfaces seal.ErrSealed on every
// operation, read live. The store is rebuilt whenever the registry holds a
// different barrier for the tenant than the one it was built over (a
// recycle, a restore).
func (h *LocalHomes) HomeStore(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	entry, err := h.entry(tenantID)
	if err != nil {
		return nil, err
	}
	return entry.store, nil
}

// entry returns the cached home store over the barrier the registry holds
// for tenantID now, building it when the barrier differs from the one the
// cache was built over. With admission, the store is an admittedHomeStore
// and the barrier's own store must serve atomic and guarded writes, else
// ErrHomeStoreUnguarded.
func (h *LocalHomes) entry(tenantID string) (*localHomeEntry, error) {
	tb, err := h.registry.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	if cached, ok := h.stores.Load(tenantID); ok {
		if entry, ok := cached.(*localHomeEntry); ok && entry.barrier == tb {
			return entry, nil
		}
	}
	inner, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		return nil, err
	}
	entry := &localHomeEntry{barrier: tb}
	if h.admission != nil {
		guarded, ok := inner.(barrierHomeKVStore)
		if !ok {
			return nil, ErrHomeStoreUnguarded
		}
		entry.guarded = guarded
		inner = &admittedHomeStore{homes: h, tenantID: tenantID}
	}
	if entry.store, err = kvstore.NewNamespacedKVStore(inner, HomeName+"/"); err != nil {
		return nil, err
	}
	h.stores.Store(tenantID, entry)
	return entry, nil
}
