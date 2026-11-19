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

//go:build !integration && codec_json && raft

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// indexedMember is the entity the resolver-store index regression exercises.
// Team carries an `index:"true"` tag, which is what routes a Where("Team = ?")
// query away from a full scan and into KVStore.QueryIndex — the path that
// silently returned (nil, nil) on every resolver-returned store.
type indexedMember struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Team string `json:"team" index:"true"`
}

// EntityID implements dao.Entity.
func (m *indexedMember) EntityID() uint64 { return m.ID }

// SetEntityID implements dao.Entity.
func (m *indexedMember) SetEntityID(id uint64) { m.ID = id }

// newIndexedMember is the dao.New entity factory for indexedMember.
func newIndexedMember() *indexedMember { return &indexedMember{} }

// seedIndexedMembers writes the fixture set through the DAO and returns the
// number of members whose Team is "alpha". Writing THROUGH the DAO is the
// point: index maintenance must be a property of the store, not of a test
// helper that populates index entries by hand. The pre-existing pkg/dao index
// tests seed their fake's index map directly (generic_dao_index_test.go
// addIndexEntry), which is exactly why they never caught this.
func seedIndexedMembers(ctx context.Context, t *testing.T, d dao.GenericDAO[*indexedMember]) int {
	t.Helper()
	members := []*indexedMember{
		{Name: "ana", Team: "alpha"},
		{Name: "ben", Team: "alpha"},
		{Name: "cal", Team: "beta"},
	}
	for _, m := range members {
		require.NoErrorf(t, d.Save(ctx, m), "Save(%s)", m.Name)
	}
	return 2
}

// resolverStoreCase names one store the production tenant.StoreResolver can
// hand a consumer, plus how to obtain a tenant that resolves to it.
type resolverStoreCase struct {
	name     string
	tenantID string
	// setup provisions whatever the tenant needs before Resolve is called.
	setup func(ctx context.Context, t *testing.T, n *Node)
	// indexCapable is true when the store maintains real secondary indexes.
	//
	// It splits the two halves of the contract #32 states — "either it
	// answers correctly, or it fails with a typed error" — and neither half
	// permits an empty result with a nil error:
	//
	//   true  → a direct QueryByIndex must return the matching entities.
	//   false → a direct QueryByIndex must return kvstore.ErrIndexUnsupported,
	//           and a PLANNED query must still return the right entities,
	//           because pkg/dao detects the refusal at construction and plans
	//           a full scan instead of an index lookup.
	indexCapable bool
}

// resolverStoreCases enumerates every store reachable through
// tenant.StoreResolver.Resolve on a standalone node:
//
//   - the system tenant resolves to the shared store, which is
//     lifecycle.kvStoreAdapter over the replicated KV service;
//   - an isolated tenant resolves to its home store (tenant.LocalHomes): a
//     namespace over seal.tenantBarrierKVStore, which encrypts under the
//     tenant's own barrier in its data root.
func resolverStoreCases() []resolverStoreCase {
	return []resolverStoreCase{
		{
			name:     "shared_store_system_tenant",
			tenantID: tenantid.System,
			setup:    func(context.Context, *testing.T, *Node) {},
			// Flipped back by #40. The shared store carries the node's
			// IndexServicer, reaching the same KVEngine that backs its
			// writes, so #32 made its indexes real; #41 then confined every
			// shared-prefix tenant — the system tenant included — to its own
			// keyspace, and the index was lost while the engine matched
			// writes to indexes by the key's FIRST path segment, which the
			// namespace displaces. The engine now maintains an index for
			// exactly its declared key scope, and the adapter declares the
			// scope the namespaced keys carry, so the confined store indexes
			// again.
			//
			// The system tenant is still NOT special-cased back to an
			// unnamespaced store: tenant.ComposeNamespace's own documentation
			// forbids that, because an empty prefix "would give it the entire
			// shared store root as its keyspace".
			indexCapable: true,
		},
		{
			name:     "home_store_isolated_tenant",
			tenantID: "acme",
			// Stores ciphertext under the tenant's barrier, so nothing
			// beneath it can read a field to index. It refuses instead of
			// answering empty; see seal.tenantBarrierKVStore.RegisterEntityIndexes.
			indexCapable: false,
			setup: func(ctx context.Context, t *testing.T, n *Node) {
				t.Helper()
				createDefaultPool(t, n)
				_, err := n.Services().Tenant.CreateTenant(ctx, "acme", "Acme Corp",
					tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
				require.NoError(t, err, "CreateTenant(IsolationVolume) must succeed")
			},
		},
	}
}

// resolveStore boots a standalone node, provisions the case's tenant, and
// returns the KVStore the production resolver hands back for it.
func resolveStore(ctx context.Context, t *testing.T, tc resolverStoreCase) kvstore.KVStore {
	t.Helper()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")

	tc.setup(ctx, t, n)

	resolver := n.Services().StoreResolver
	require.NotNil(t, resolver, "StoreResolver must be wired on a tenant-isolated node")

	store, err := resolver.Resolve(ctx, tc.tenantID)
	require.NoErrorf(t, err, "Resolve(%q)", tc.tenantID)
	require.NotNil(t, store)
	return store
}

// TestDAOIndexOverResolverStore_QueryByIndexIsNonEmpty is the regression that
// makes silent-empty impossible. For every store the production StoreResolver
// returns, it writes entities carrying an INDEXED field through the DAO and
// then requires QueryByIndex to come back NON-EMPTY with the right members.
//
// The assertion is deliberately "non-empty and correct", never "no error".
// Before this change every one of these stores implemented QueryIndex as
// `return nil, nil`, so the DAO answered every index query with an empty slice
// and a nil error: a caller reading its own work queue concludes there is no
// work, forever, and no failure counter anywhere moves. A test that accepted
// (nil, nil) — or that only asserted err == nil — is what let that survive.
func TestDAOIndexOverResolverStore_QueryByIndexIsNonEmpty(t *testing.T) {
	for _, tc := range resolverStoreCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := resolveStore(ctx, t, tc)

			d, err := dao.New(store, "member", newIndexedMember)
			require.NoError(t, err, "dao.New over a resolver-returned store")

			wantAlpha := seedIndexedMembers(ctx, t, d)

			got, err := d.QueryByIndex(ctx, "Team", "alpha")

			if !tc.indexCapable {
				// The refusal must be the typed one, testable with
				// errors.Is, and it must carry no results. A nil error here
				// would mean the store reported success for a query it
				// cannot perform — the defect this whole change removes.
				require.ErrorIs(t, err, kvstore.ErrIndexUnsupported,
					"a store that maintains no indexes must refuse with the typed error, "+
						"not answer empty")
				require.Empty(t, got)
				return
			}

			require.NoError(t, err, "QueryByIndex must not error")
			require.Len(t, got, wantAlpha,
				"QueryByIndex(Team=alpha) returned %d entities; a resolver-returned store "+
					"must answer an index query it accepted, never silently return empty",
				len(got))
			for _, m := range got {
				require.Equal(t, "alpha", m.Team)
			}
		})
	}
}

// TestDAOIndexOverResolverStore_PlannedQueryIsNonEmpty covers the second entry
// point into the same defect: QueryBuilder.executeQuery consults a catalog
// that pkg/dao builds from the entity's STRUCT TAGS by reflection, not from
// what the store can serve. A tagged field therefore routes to QueryIndex and
// yields nothing, while the identical query on an UNtagged field falls back to
// a full scan and is correct — adding the index is what breaks the query.
//
// The two sub-assertions pin exactly that inversion: the tagged-field query
// must return the same members the equivalent full scan does.
func TestDAOIndexOverResolverStore_PlannedQueryIsNonEmpty(t *testing.T) {
	for _, tc := range resolverStoreCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := resolveStore(ctx, t, tc)

			d, err := dao.New(store, "member", newIndexedMember)
			require.NoError(t, err)

			wantAlpha := seedIndexedMembers(ctx, t, d)

			// Indexed field: routed through the planner to QueryIndex.
			indexed, err := d.Find(ctx).Where("Team = ?", "alpha").Execute()
			require.NoError(t, err, "planned query on an indexed field must not error")
			require.Len(t, indexed, wantAlpha,
				"Find().Where(Team = alpha) returned %d entities; the planner routed to "+
					"the store's index and the store answered empty", len(indexed))

			// Unindexed field: full scan, and therefore already correct today.
			// It is the control: if this also came back empty the fixture is
			// broken and the indexed assertion above would prove nothing.
			scanned, err := d.Find(ctx).Where("Name = ?", "ana").Execute()
			require.NoError(t, err)
			require.Len(t, scanned, 1,
				"control: the unindexed full-scan path must find the seeded member")
		})
	}
}
