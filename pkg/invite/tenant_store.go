// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package invite

import (
	"context"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantStore issues and redeems invitations in each tenant's own store.
//
// Every operation names its tenant explicitly and routes through a
// tenant.StoreResolver, so a barrier-isolated tenant's invitations are written
// into that tenant's barrier (its own DEK and namespace), a prefix or system
// tenant uses the shared store, and a sealed tenant fails closed. That is the
// same routing TenantScopeStore uses for permission scopes, and it is what
// makes isolation a property of the storage rather than of a field the caller
// remembers to check.
//
// The isolation this buys is concrete: a token issued for tenant A is not
// merely labelled with A, it is not present in tenant B's store at all. A
// redemption attempted against the wrong tenant resolves to a different store
// and returns ErrNotFound — the same answer a fabricated token gets, so a
// caller cannot use cross-tenant probing to discover that a token exists
// somewhere.
//
// TenantStore holds no invitation state of its own. It caches one DAOStore per
// tenant because building a store registers entity indexes, which is wasteful
// to repeat per request; the cached value is keyed by tenant and never shared
// across them.
type TenantStore struct {
	resolver tenant.StoreResolver

	mu     sync.RWMutex
	stores map[string]*DAOStore
}

// NewTenantStore constructs a TenantStore over the given resolver.
func NewTenantStore(resolver tenant.StoreResolver) (*TenantStore, error) {
	if resolver == nil {
		return nil, ErrNilStoreResolver
	}
	return &TenantStore{
		resolver: resolver,
		stores:   make(map[string]*DAOStore),
	}, nil
}

// Create issues an invitation for a user in the named tenant and returns the
// plaintext token, which is returned exactly once.
func (s *TenantStore) Create(ctx context.Context, userID, tenantID string, ttl time.Duration) (string, *Entity, error) {
	store, err := s.storeFor(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}
	return store.Create(ctx, userID, tenantID, ttl)
}

// Lookup resolves a token within the named tenant without consuming it.
func (s *TenantStore) Lookup(ctx context.Context, tenantID, token string) (*Entity, error) {
	store, err := s.storeFor(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return store.Lookup(ctx, token)
}

// Redeem consumes a token within the named tenant exactly once.
func (s *TenantStore) Redeem(ctx context.Context, tenantID, token string) (*Entity, error) {
	store, err := s.storeFor(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return store.Redeem(ctx, token)
}

// GC removes redeemed and expired invitations from the named tenant's store.
//
// It is per-tenant rather than global because there is no store that holds
// every tenant's invitations — that is the point of the routing — so a caller
// sweeping the whole deployment iterates the tenants it knows about.
func (s *TenantStore) GC(ctx context.Context, tenantID string, now time.Time) (int, error) {
	store, err := s.storeFor(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	return store.GC(ctx, now)
}

// storeFor returns the invitation store backing the named tenant, building and
// caching it on first use.
//
// A tenant whose barrier is sealed surfaces the resolver's error unchanged, so
// the caller fails closed rather than treating a sealed tenant as one with no
// invitations.
func (s *TenantStore) storeFor(ctx context.Context, tenantID string) (*DAOStore, error) {
	s.mu.RLock()
	cached, ok := s.stores[tenantID]
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}

	kv, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	store, err := NewDAOStore(kv)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another goroutine may have built one while this call was resolving. Keep
	// the first, so every caller for a tenant shares one store and its indexes
	// are registered once.
	if existing, raced := s.stores[tenantID]; raced {
		return existing, nil
	}
	s.stores[tenantID] = store
	return store, nil
}
