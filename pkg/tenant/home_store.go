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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// The tenant home store — where an isolated tenant's records live.
//
// See docs/multi-tenancy/architecture.md "Strict isolation". Every isolated
// tenant (barrier, volume, raft) holds its identity records (users, roles,
// factors, bindings, invitations, permission scopes) and its DEK keyring in
// its OWN store, never in the system shard, whatever its isolation policy:
// the policy decides operator verbs, backups and relay, not where a record
// lives. In raft mode that store is the tenant home group: a Raft group of
// the tenant's own, replicated across the serving set, with its log and
// snapshots under the tenant's key. In standalone mode it is the tenant
// barrier's own backend at the tenant's data root. The system shard keeps
// only the irreducible records the architecture lists (registry entry,
// tombstone, placement, volume-pool slot, seal record, public CA, floor).
//
// This package owns the routing decision and the fail-closed contract; the
// store itself is built by the composer that owns the data plane and is
// attached late (SetHomeStoreSource), because the resolver exists before the
// stores that host the homes do.
// ---------------------------------------------------------------------------

// HomeStoreSource provides isolated tenants' home stores.
type HomeStoreSource interface {
	// EnsureHome provisions tenantID's home store when it does not exist and
	// returns once it can serve, or with an error. It is idempotent.
	EnsureHome(ctx context.Context, tenantID string) error

	// HomeStore returns tenantID's home store. The store is the tenant's
	// own: keys are not namespaced by tenant inside it.
	HomeStore(ctx context.Context, tenantID string) (kvstore.KVStore, error)
}

// HomeRouter is the part of the StoreResolver this package returns that
// composers use to attach and provision isolated tenants' home stores.
type HomeRouter interface {
	// SetHomeStoreSource attaches the source isolated tenants resolve to. A
	// nil src detaches it, after which isolated tenants fail closed again.
	SetHomeStoreSource(src HomeStoreSource)

	// EnsureHome provisions tenantID's home store when the tenant is
	// isolated, and does nothing for a shared (prefix) tenant.
	EnsureHome(ctx context.Context, tenantID string) error
}

// HomeLocator reports whether a tenant's records are held in its home store
// rather than in the system shard. It answers with the same decision Resolve
// routes by, so a consumer that keeps a record about a tenant in the system
// shard can decline to for exactly the tenants whose records are not there.
type HomeLocator interface {
	// InHome reports whether tenantID resolves to its home store. The system
	// tenant, an unregistered tenant and a shared (prefix) tenant do not.
	InHome(ctx context.Context, tenantID string) (bool, error)
}

// Compile-time interface checks.
var (
	_ HomeRouter  = (*defaultStoreResolver)(nil)
	_ HomeLocator = (*defaultStoreResolver)(nil)
)

// homeSourceRef boxes a HomeStoreSource so the resolver can hold it in an
// atomic.Pointer.
type homeSourceRef struct{ HomeStoreSource }

// SetHomeStoreSource implements HomeRouter.
func (r *defaultStoreResolver) SetHomeStoreSource(src HomeStoreSource) {
	if src == nil {
		r.home.Store(nil)
		return
	}
	r.home.Store(&homeSourceRef{src})
}

// EnsureHome implements HomeRouter.
func (r *defaultStoreResolver) EnsureHome(ctx context.Context, tenantID string) error {
	info, err := r.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if _, routed := barrierRoutedIsolation[info.EffectiveIsolationType()]; !routed {
		return nil
	}
	ref := r.home.Load()
	if ref == nil {
		return &TenantHomeUnavailableError{TenantID: tenantID, Err: ErrTenantHomeNotAttached}
	}
	if err := ref.EnsureHome(ctx, tenantID); err != nil {
		return &TenantHomeUnavailableError{TenantID: tenantID, Err: err}
	}
	return nil
}

// InHome implements HomeLocator, by the decision Resolve makes: every
// registered tenant of an isolated type resolves to its home store.
func (r *defaultStoreResolver) InHome(ctx context.Context, tenantID string) (bool, error) {
	if tenantid.IsSystem(tenantID) {
		return false, nil
	}
	info, err := r.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			return false, nil
		}
		return false, err
	}
	_, routed := barrierRoutedIsolation[info.EffectiveIsolationType()]
	return routed, nil
}

// resolveHome returns an isolated tenant's home store, failing closed with
// ErrTenantSealed when its barrier is sealed or absent here, and with a
// *TenantHomeUnavailableError -- which matches ErrTenantSealed, so it flows
// through the transports as a retryable unavailable -- when there is no home.
func (r *defaultStoreResolver) resolveHome(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	if err := r.requireUnsealed(tenantID); err != nil {
		return nil, err
	}
	ref := r.home.Load()
	if ref == nil {
		return nil, &TenantHomeUnavailableError{TenantID: tenantID, Err: ErrTenantHomeNotAttached}
	}
	store, err := ref.HomeStore(ctx, tenantID)
	if err != nil {
		return nil, &TenantHomeUnavailableError{TenantID: tenantID, Err: err}
	}
	return store, nil
}
