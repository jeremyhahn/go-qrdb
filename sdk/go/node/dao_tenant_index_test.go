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

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// workItem stands in for the consumer's tenant-first entity: the SDLC fabric
// keys WorkItem/PullRequest/Review per tenant and dispatches by an INDEXED
// field (state, head SHA), which is what makes tenant-scoped index behaviour
// load-bearing rather than cosmetic.
type workItem struct {
	ID    uint64 `json:"id"`
	Title string `json:"title"`
	State string `json:"state" index:"true"`
}

// EntityID implements dao.Entity.
func (w *workItem) EntityID() uint64 { return w.ID }

// SetEntityID implements dao.Entity.
func (w *workItem) SetEntityID(id uint64) { w.ID = id }

// newWorkItem is the dao.New entity factory.
func newWorkItem() *workItem { return &workItem{} }

// newSharedTenantNode boots a standalone node and creates two SHARED-PREFIX
// tenants, which is the default isolation and the consumer's primary path:
// per the tenancy architecture a tenant is not isolated unless it asks to be,
// and a prefix tenant lives in the system store separated by keyspace.
func newSharedTenantNode(ctx context.Context, t *testing.T) *Node {
	t.Helper()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(ctx, cfg, nil, WithTenantIsolation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")

	for _, id := range []string{"acme", "globex"} {
		_, err := n.Services().Tenant.CreateTenant(ctx, id, id, tenant.IsolationPrefix)
		require.NoErrorf(t, err, "CreateTenant(%s, IsolationPrefix)", id)
	}
	return n
}

// TestSharedPrefixTenants_ResolveAndAreDistinguishable records what the
// resolver actually hands a consumer for two shared-prefix tenants.
//
// This is a characterization test, written before any change: everything else
// in this file depends on knowing whether the two tenants receive stores that
// are separated in any way, or literally the same object.
func TestSharedPrefixTenants_ResolveAndAreDistinguishable(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver
	require.NotNil(t, resolver)

	acme, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)
	globex, err := resolver.Resolve(ctx, "globex")
	require.NoError(t, err)

	require.NotSame(t, acme, globex,
		"two shared-prefix tenants resolve to the SAME store object, so nothing "+
			"about the handle carries which tenant it belongs to: any DAO built "+
			"over it writes into one undifferentiated keyspace")
}

// TestSharedPrefixTenants_DataIsIsolated is the property that has to hold
// before tenant-scoped INDEXES can mean anything: a DAO built for tenant A
// must not see tenant B's rows at all.
//
// Asserting it through a full-scan query (Count, and an unindexed field) is
// deliberate — this is about the KEYSPACE, not the index. If the data is
// shared, a tenant-prefixed index would be a lock on a door with no wall.
func TestSharedPrefixTenants_DataIsIsolated(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	acmeStore, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)
	globexStore, err := resolver.Resolve(ctx, "globex")
	require.NoError(t, err)

	acmeDAO, err := dao.New(acmeStore, "workitem", newWorkItem)
	require.NoError(t, err)
	globexDAO, err := dao.New(globexStore, "workitem", newWorkItem)
	require.NoError(t, err)

	require.NoError(t, acmeDAO.Save(ctx, &workItem{Title: "acme-only", State: "ready"}))

	count, err := globexDAO.Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count,
		"tenant globex sees %d of tenant acme's work items; a shared-prefix "+
			"tenant's records must be confined to its own keyspace", count)

	got, err := globexDAO.Find(ctx).Where("Title = ?", "acme-only").Execute()
	require.NoError(t, err)
	require.Empty(t, got, "a full scan for tenant globex returned tenant acme's row")
}

// TestSharedPrefixTenants_DirectIndexQueryRefuses pins the INTERIM contract
// ruled on #41 (option A, 2026-08-19), and it is deliberately the inverse of
// what this test asserted while it was a characterization test.
//
// Keyspace isolation and engine-backed indexes cannot coexist for
// shared-prefix tenants under the current engine design.
// KVEngine.entityTypeFromKey takes the FIRST path segment, so a namespaced key
// "acme/system/workitem/1" reads as entity type "acme" rather than "workitem":
// the adapter refuses the namespace-qualified index name, and even without
// that guard the engine would create the index and never match a write to it.
//
// Isolation is the ruled priority, so the index is given up rather than the
// confinement, and correctness is preserved by the DAO planning a full scan —
// see TestSharedPrefixTenants_PlannedIndexQueryIsTenantScoped, which asserts
// the user-visible query still returns the right rows.
//
// THIS ASSERTION MUST FLIP BACK. Teaching the engine about namespaced keys is
// #40's priority-1 mechanism; when it lands, a shared-prefix tenant's direct
// index query is answered from a real per-tenant index and this test asserts
// a NON-EMPTY result again. It is an interim contract on a ruling, not a
// statement that refusing is correct.
func TestSharedPrefixTenants_DirectIndexQueryRefuses(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	acmeStore, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)
	globexStore, err := resolver.Resolve(ctx, "globex")
	require.NoError(t, err)

	acmeDAO, err := dao.New(acmeStore, "workitem", newWorkItem)
	require.NoError(t, err)
	globexDAO, err := dao.New(globexStore, "workitem", newWorkItem)
	require.NoError(t, err)

	require.NoError(t, acmeDAO.Save(ctx, &workItem{Title: "acme-1", State: "ready"}))
	require.NoError(t, globexDAO.Save(ctx, &workItem{Title: "globex-1", State: "ready"}))

	got, err := acmeDAO.QueryByIndex(ctx, "State", "ready")
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported,
		"a namespaced shared store cannot maintain an engine-backed index, so the "+
			"direct index path must refuse with the typed error (#40 flips this)")
	require.Empty(t, got, "a refused query carries no results")
}

// TestSharedPrefixTenants_PlannedIndexQueryIsTenantScoped covers the planner
// entry point, which is how a consumer's dispatch loop actually queries. The
// capability probe must not have downgraded this path to a full scan that
// silently reads across tenants.
func TestSharedPrefixTenants_PlannedIndexQueryIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	acmeStore, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)
	globexStore, err := resolver.Resolve(ctx, "globex")
	require.NoError(t, err)

	acmeDAO, err := dao.New(acmeStore, "workitem", newWorkItem)
	require.NoError(t, err)
	globexDAO, err := dao.New(globexStore, "workitem", newWorkItem)
	require.NoError(t, err)

	require.NoError(t, acmeDAO.Save(ctx, &workItem{Title: "acme-1", State: "ready"}))
	require.NoError(t, globexDAO.Save(ctx, &workItem{Title: "globex-1", State: "ready"}))

	got, err := acmeDAO.Find(ctx).Where("State = ?", "ready").Execute()
	require.NoError(t, err)
	require.Len(t, got, 1, "acme's planned index query returned %d rows; want its own one", len(got))
	require.Equal(t, "acme-1", got[0].Title)
}

// TestSharedPrefixTenants_IndexRefusalIsNotSilent restates the #32 invariant
// for this class: whatever the outcome, the forbidden answer is an empty
// result with a nil error over an index that holds matches.
func TestSharedPrefixTenants_IndexRefusalIsNotSilent(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	store, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)
	d, err := dao.New(store, "workitem", newWorkItem)
	require.NoError(t, err)
	require.NoError(t, d.Save(ctx, &workItem{Title: "acme-1", State: "ready"}))

	got, err := d.QueryByIndex(ctx, "State", "ready")
	require.Falsef(t, len(got) == 0 && err == nil,
		"QueryByIndex returned (empty, nil) for a value that IS present: a store "+
			"must answer the query or refuse it, never report success for a "+
			"lookup it did not perform")
	if err != nil {
		require.ErrorIs(t, err, kvstore.ErrIndexUnsupported,
			"a refusal must be the typed one")
	}
}

// TestSharedPrefixTenants_IAMSurvivesNamespacing is the end-to-end guard on
// the #41 ruling's central risk: the fix must not relocate IAM.
//
// TestSharedPrefixTenants_ReservedKeysAreTenantConfined asserts the design
// this landed on, end to end through the real resolver: there is NO exemption
// for the reserved "__sys:" prefix, so IAM, permission-scope and every other
// reserved record is confined to its tenant like anything else.
//
// This test previously asserted the OPPOSITE — that reserved keys were global
// — under a draft that exempted them to preserve existing key locations across
// an upgrade. Pre-release, compatibility is a non-goal, and the exemption was
// also the weaker design: it was a second tenancy mechanism keyed on a string
// prefix, and it let a crafted tenant id, a crafted DAO entity type, and
// pkg/permission's tenantless scope keys all reach across tenants.
func TestSharedPrefixTenants_ReservedKeysAreTenantConfined(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	sys, err := resolver.Resolve(ctx, tenant.SystemTenantID)
	require.NoError(t, err)
	acme, err := resolver.Resolve(ctx, "acme")
	require.NoError(t, err)

	const iamKey = kvstore.ReservedPrefixSystem + "users/id/probe"
	require.NoError(t, sys.Put(ctx, iamKey, []byte("system-admin")))

	got, err := sys.Get(ctx, iamKey)
	require.NoError(t, err)
	require.Equal(t, "system-admin", string(got), "the owning tenant reads its own record")

	crossed, err := acme.Get(ctx, iamKey)
	require.NoError(t, err)
	require.Nil(t, crossed,
		"a reserved-prefix record must not be readable from another tenant's store")

	// A permission scope is the sharpest case: pkg/permission keys
	// "__sys:perm_scope/{permission}" with NO tenant segment of its own, so
	// under an exemption one tenant's scope definition applied to every
	// tenant. Confinement is what makes that key tenant-specific.
	const scopeKey = kvstore.ReservedPrefixSystem + "perm_scope/db:read"
	require.NoError(t, acme.Put(ctx, scopeKey, []byte("acme-scope")))
	sysScope, err := sys.Get(ctx, scopeKey)
	require.NoError(t, err)
	require.Nil(t, sysScope,
		"a permission scope defined by one tenant must not apply to another")

	// And the DAO surface stays confined, which was the original defect.
	acmeDAO, err := dao.New(acme, "workitem", newWorkItem)
	require.NoError(t, err)
	require.NoError(t, acmeDAO.Save(ctx, &workItem{Title: "acme-1", State: "ready"}))
	sysDAO, err := dao.New(sys, "workitem", newWorkItem)
	require.NoError(t, err)
	count, err := sysDAO.Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
