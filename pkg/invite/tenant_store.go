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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
// That claim was FALSE for shared-prefix tenants until #41 (2026-08-19), and
// the correction is worth recording because the guarantee is load-bearing.
// StoreResolver returned the shared store itself for every prefix tenant, so
// "a different store" was the SAME store: every prefix tenant's invitations
// occupied one keyspace, and Redeem for tenant B resolved and consumed an
// invitation issued for tenant A. Entity.TenantID was written and never
// compared, so nothing else caught it either — the scoping was meant to come
// from the store separation that did not exist.
//
// Exploiting it required knowing the token, so it was not a probing hole; the
// failure was that an invitation's tenant scope was not enforced at all.
// StoreResolver now confines each prefix tenant to its own tenant-only
// "{tenant}/" identity keyspace, which is what makes the paragraph above true
// rather than aspirational. Note that is deliberately NOT
// tenant.ComposeNamespace, which is the data plane's "{tenant}/{database}/"
// formula: an invitation is identity and belongs to the tenant, not to one of
// its databases.
//
// TenantStore holds no invitation state of its own. It caches one DAOStore per
// resolved KVStore because building a store registers entity indexes, which is
// wasteful to repeat per request.
//
// Keyed by the RESOLVED store, not by tenant id, and the resolver runs on
// EVERY operation before the cache is consulted. Caching by tenant id let a
// lookup for an unknown tenant pin that tenant to the shared store forever:
// if it was later created with barrier isolation, every invitation operation
// kept using the cached unencrypted store and never saw ErrTenantSealed. The
// user and role routers key their caches the same way for the same reason.
type TenantStore struct {
	resolver tenant.StoreResolver

	stores sync.Map // map[kvstore.KVStore]*DAOStore
}

// NewTenantStore constructs a TenantStore over the given resolver.
func NewTenantStore(resolver tenant.StoreResolver) (*TenantStore, error) {
	if resolver == nil {
		return nil, ErrNilStoreResolver
	}
	return &TenantStore{resolver: resolver}, nil
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

// storeFor returns the invitation store backing the named tenant.
//
// The resolver runs FIRST, on every call, before any cache is consulted. That
// ordering is the fail-closed guarantee: a tenant whose barrier is sealed
// surfaces tenant.ErrTenantSealed here, and a tenant whose isolation changed
// since the last call gets its NEW store. Consulting a cache keyed by tenant
// id first would pin an unknown tenant to the shared store and keep using it
// after that tenant was created with barrier isolation — operating on
// plaintext while the barrier sat sealed.
func (s *TenantStore) storeFor(ctx context.Context, tenantID string) (*DAOStore, error) {
	kv, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.storeForKV(kv)
}

// storeForKV returns the cached DAOStore for a resolved KVStore, building one
// on first use. Caching by store identity means each tenant's resolved store
// maps to exactly one DAOStore and its entity indexes are registered once.
//
// A store whose dynamic type is NOT comparable skips the cache and gets a
// fresh DAOStore. kvstore.KVStore is an interface and nothing requires its
// implementations to be comparable — a named slice or map type with value
// receivers satisfies it perfectly well — and using such a value as a
// sync.Map key panics with "hash of unhashable type" on every operation.
// The cache is an optimization; correctness must not depend on the caller's
// choice of representation.
func (s *TenantStore) storeForKV(kv kvstore.KVStore) (*DAOStore, error) {
	if !kvstore.UsableAsMapKey(kv) {
		return NewDAOStore(kv)
	}
	if cached, ok := s.stores.Load(kv); ok {
		return cached.(*DAOStore), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	store, err := NewDAOStore(kv)
	if err != nil {
		return nil, err
	}
	actual, _ := s.stores.LoadOrStore(kv, store)
	return actual.(*DAOStore), nil //nolint:errcheck // type assertion on a value this container only ever holds
}
