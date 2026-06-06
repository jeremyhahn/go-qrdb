// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package permission

import (
	"context"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopeKeyPrefix namespaces per-tenant scope definitions inside whatever
// store the StoreResolver hands back for a tenant. The "__sys:" prefix
// matches the convention used by the IAM stores (see pkg/tenant
// store_resolver tests) so these records inherit sub-phase-A isolation:
// for a barrier-isolated tenant they are encrypted with the tenant's DEK
// and live under the tenant's namespace in its own backend; for a prefix /
// system tenant they live in the shared store.
const scopeKeyPrefix = "__sys:perm_scope/"

// scopeKey returns the storage key for a permission's per-tenant scope
// definition.
func scopeKey(p Permission) string {
	return scopeKeyPrefix + string(p)
}

// permissionFromScopeKey is the inverse of scopeKey. It reports false when
// key is not a scope-definition key.
func permissionFromScopeKey(key string) (Permission, bool) {
	if !strings.HasPrefix(key, scopeKeyPrefix) {
		return "", false
	}
	return Permission(key[len(scopeKeyPrefix):]), true
}

// ScopeResolver resolves a permission's scope in the context of a specific
// tenant. It is the tenant-aware counterpart to the process-global
// LookupScope and is the interface the RBAC authorizer depends on so that
// a caller-defined permission resolves in the caller's own tenant.
//
// *TenantScopeStore implements ScopeResolver.
type ScopeResolver interface {
	// ResolveScope returns the scope that governs permission p for the
	// given tenant, applying the precedence documented on
	// TenantScopeStore.ResolveScope. It fails closed: any error MUST be
	// treated as a denial by the caller, never as ScopeSelf admission.
	ResolveScope(ctx context.Context, tenantID string, p Permission) (Scope, error)
}

// Compile-time interface check.
var _ ScopeResolver = (*TenantScopeStore)(nil)

// TenantScopeStore persists and resolves caller-defined, per-tenant
// permission scopes. Two tenants may define the SAME permission name with
// DIFFERENT scopes without colliding, because each tenant's definitions are
// stored in that tenant's own store as selected by the StoreResolver
// (sub-phase A): a barrier-isolated tenant's scope definitions are
// encrypted in its barrier; a prefix / system tenant's live in the shared
// store, separated by the "{tenantID}/" key segments the underlying stores
// already embed.
//
// Resolution precedence (see ResolveScope) is:
//
//  1. the tenant's own scope definition (this store), then
//  2. the built-in process-global default layer (LookupScope), then
//  3. ScopeSelf — the safe, tenant-confined default.
//
// TenantScopeStore holds no mutable state of its own; the global default
// layer it consults via LookupScope is lock-free for readers (RWMutex
// read-locked). All persistence is routed through the StoreResolver, so it
// honors the tenant's isolation mode and fails closed (ErrTenantSealed)
// for a sealed barrier tenant.
type TenantScopeStore struct {
	resolver tenant.StoreResolver
}

// NewTenantScopeStore constructs a TenantScopeStore over the given
// StoreResolver. The resolver must be non-nil.
func NewTenantScopeStore(resolver tenant.StoreResolver) (*TenantScopeStore, error) {
	if resolver == nil {
		return nil, ErrNilStoreResolver
	}
	return &TenantScopeStore{resolver: resolver}, nil
}

// SetScope persists scope as the per-tenant definition for permission p in
// the given tenant. The write is routed through the StoreResolver, so it
// lands in the tenant's barrier when the tenant is barrier-isolated and in
// the shared store otherwise. A sealed barrier tenant fails closed
// (tenant.ErrTenantSealed). The scope must be a defined constant.
func (s *TenantScopeStore) SetScope(ctx context.Context, tenantID string, p Permission, scope Scope) error {
	if p == "" {
		return &EmptyPermissionError{}
	}
	if !scope.Valid() {
		return &InvalidScopeError{Value: scope.String()}
	}
	store, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return err
	}
	return store.Put(ctx, scopeKey(p), []byte(scope.String()))
}

// GetScope returns the per-tenant scope definition for permission p in the
// given tenant. The bool reports whether a definition exists for that
// tenant; when false, the returned Scope is the safe default ScopeSelf and
// the error is nil. The read is routed through the StoreResolver and fails
// closed (tenant.ErrTenantSealed) for a sealed barrier tenant. A stored
// value that cannot be decoded yields (ScopeSelf, false, *ScopeDecodeError)
// — failing closed rather than widening reach.
func (s *TenantScopeStore) GetScope(ctx context.Context, tenantID string, p Permission) (Scope, bool, error) {
	if p == "" {
		return ScopeSelf, false, &EmptyPermissionError{}
	}
	store, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return ScopeSelf, false, err
	}
	raw, err := store.Get(ctx, scopeKey(p))
	if err != nil {
		return ScopeSelf, false, err
	}
	if raw == nil {
		// kvstore.Get returns (nil, nil) for a missing key — no per-tenant
		// override exists.
		return ScopeSelf, false, nil
	}
	scope, perr := ParseScope(string(raw))
	if perr != nil {
		return ScopeSelf, false, &ScopeDecodeError{Permission: p, Err: perr}
	}
	return scope, true, nil
}

// ListScopes returns every per-tenant scope definition for the given
// tenant as a map from permission to scope. The read is routed through the
// StoreResolver and fails closed (tenant.ErrTenantSealed) for a sealed
// barrier tenant. A single undecodable record fails the whole listing
// closed with a *ScopeDecodeError so a caller never sees a partial,
// silently-narrowed view.
func (s *TenantScopeStore) ListScopes(ctx context.Context, tenantID string) (map[Permission]Scope, error) {
	store, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make(map[Permission]Scope)
	scanErr := store.Scan(ctx, scopeKeyPrefix, func(key string, value []byte) error {
		p, ok := permissionFromScopeKey(key)
		if !ok {
			return nil
		}
		scope, perr := ParseScope(string(value))
		if perr != nil {
			return &ScopeDecodeError{Permission: p, Err: perr}
		}
		out[p] = scope
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}
	return out, nil
}

// ResolveScope returns the scope that governs permission p for the given
// tenant, applying the precedence:
//
//  1. the tenant's own scope definition (GetScope), then
//  2. the built-in process-global default layer (LookupScope), then
//  3. ScopeSelf — the safe, tenant-confined default.
//
// SECURITY: ResolveScope fails closed. If the tenant store cannot be
// reached — most importantly when a barrier tenant is sealed
// (tenant.ErrTenantSealed) — the error is returned and the caller MUST
// deny. It never falls back to the global layer on a storage error,
// because doing so could leak a built-in scope onto a tenant whose own
// (possibly narrower) definition is currently unreadable.
//
// Implements ScopeResolver.
func (s *TenantScopeStore) ResolveScope(ctx context.Context, tenantID string, p Permission) (Scope, error) {
	scope, ok, err := s.GetScope(ctx, tenantID, p)
	if err != nil {
		return ScopeSelf, err
	}
	if ok {
		return scope, nil
	}
	// No per-tenant override: fall back to the built-in default layer,
	// which itself defaults to ScopeSelf for unregistered permissions.
	return LookupScope(p), nil
}
