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
	"errors"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// StoreResolver maps a tenant ID to the kvstore.KVStore that backs that
// tenant's IAM data (users, roles).
//
// The resolution depends on the tenant's isolation type:
//
//   - prefix / system / unknown / empty tenants resolve to the SHARED
//     system KVStore. Their records live in the one shared store and are
//     separated only logically by the "...tenant/{tenantID}..." key
//     segments the IAM stores already embed.
//
//   - barrier tenants resolve to a per-tenant store backed by that
//     tenant's own TenantBarrier: every value is encrypted with the
//     tenant's DEK and stored under the tenant's "{tenantID}/" namespace
//     in an independent backend. A barrier tenant's IAM data is therefore
//     cryptographically and physically isolated from the shared store and
//     from every other tenant.
//
// SECURITY: resolution fails closed for barrier tenants. When a barrier
// tenant's barrier is sealed, Resolve returns ErrTenantSealed and NEVER
// falls back to the shared system store.
type StoreResolver interface {
	// Resolve returns the KVStore that backs the given tenant's IAM data.
	// It returns ErrTenantSealed when the tenant is barrier-isolated and
	// its barrier is sealed.
	Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error)
}

// isolationLookup is the minimal slice of TenantServicer the resolver
// needs: the persisted isolation type for a tenant. TenantService
// satisfies it directly.
type isolationLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error)
}

// barrierLookup is the minimal slice of *seal.BarrierRegistry the resolver
// needs. *seal.BarrierRegistry satisfies it directly.
type barrierLookup interface {
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
}

// Compile-time interface check.
var _ StoreResolver = (*defaultStoreResolver)(nil)

// defaultStoreResolver is the production StoreResolver. It holds the shared
// system KVStore, the barrier registry, and a tenant-metadata lookup for
// resolving isolation types. Per-tenant barrier KVStore wrappers are cached
// in a sync.Map keyed by tenant ID; the cached wrapper reads its barrier's
// seal state LIVE on every operation, so the sealed/unsealed decision is
// never cached.
type defaultStoreResolver struct {
	shared   kvstore.KVStore
	registry barrierLookup
	tenants  isolationLookup

	wrappers sync.Map // map[string]kvstore.KVStore — barrier-tenant wrappers
}

// NewStoreResolver constructs the default StoreResolver. shared is the
// system KVStore used for prefix/system tenants; registry resolves a
// barrier tenant's TenantBarrier; tenants resolves a tenant's isolation
// type. All three must be non-nil.
func NewStoreResolver(shared kvstore.KVStore, registry *seal.BarrierRegistry, tenants TenantServicer) (StoreResolver, error) {
	if shared == nil {
		return nil, ErrNilStore
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	if tenants == nil {
		return nil, ErrNilTenantServicer
	}
	return &defaultStoreResolver{
		shared:   shared,
		registry: registry,
		tenants:  tenants,
	}, nil
}

// Resolve implements StoreResolver. See the StoreResolver doc comment for
// the fail-closed contract.
func (r *defaultStoreResolver) Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	// The system tenant and an empty/unset tenant always use the shared
	// store. The system tenant is structurally a prefix tenant so the IAM
	// stores work during bootstrap while tenant barriers are still sealed.
	if tenantID == "" || tenantID == SystemTenantID {
		return r.shared, nil
	}

	info, err := r.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		// An unknown tenant (no metadata) is treated as a logical-prefix
		// tenant against the shared store: this preserves the pre-isolation
		// behavior and keeps lookups by global ID working for callers that
		// have not (yet) registered tenant metadata. Only a tenant that is
		// explicitly barrier-isolated is routed through a barrier.
		if errors.Is(err, ErrTenantNotFound) {
			return r.shared, nil
		}
		return nil, err
	}

	// Migration / legacy: an empty IsolationType resolves to the shared
	// store (logical prefix). Barrier isolation is strictly opt-in.
	if info.IsolationType != IsolationBarrier {
		return r.shared, nil
	}

	return r.resolveBarrier(tenantID)
}

// resolveBarrier returns the cached per-tenant barrier KVStore for a
// barrier-isolated tenant, constructing and caching it on first use. The
// seal state is read live, so a cached wrapper still surfaces ErrSealed
// from the underlying barrier once it is sealed — Resolve translates that
// into ErrTenantSealed up front to fail closed.
func (r *defaultStoreResolver) resolveBarrier(tenantID string) (kvstore.KVStore, error) {
	tb, err := r.registry.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}

	// Read the seal state LIVE — never cache the sealed/unsealed decision.
	// A sealed barrier tenant fails closed: callers must not fall back to
	// the shared store.
	if tb.IsSealed() {
		return nil, ErrTenantSealed
	}

	if cached, ok := r.wrappers.Load(tenantID); ok {
		return cached.(kvstore.KVStore), nil
	}

	wrapper, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		return nil, err
	}
	actual, _ := r.wrappers.LoadOrStore(tenantID, wrapper)
	return actual.(kvstore.KVStore), nil
}
