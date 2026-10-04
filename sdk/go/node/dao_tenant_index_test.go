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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

// TestSharedPrefixTenants_DirectIndexQueryIsTenantScoped is #40's priority 1:
// a shared-prefix tenant's DIRECT index query is answered from a real,
// engine-maintained, per-tenant index -- and that index never answers with
// another tenant's rows.
//
// It is the flip of the interim contract ruled on #41 (option A), under which
// this test asserted kvstore.ErrIndexUnsupported: the engine then selected the
// indexes a write maintains by the key's FIRST path segment, which a tenant
// namespace displaces, so the adapter refused every namespace-qualified index.
// The engine now selects by key scope on every path and the adapter declares
// the scope the namespaced store's keys actually carry.
func TestSharedPrefixTenants_DirectIndexQueryIsTenantScoped(t *testing.T) {
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
	require.NoError(t, err, "a shared-prefix tenant's direct index query must be answered, not refused")
	require.Len(t, got, 1, "acme's index returned %d rows; want its own one", len(got))
	require.Equal(t, "acme-1", got[0].Title)

	got, err = globexDAO.QueryByIndex(ctx, "State", "ready")
	require.NoError(t, err)
	require.Len(t, got, 1, "globex's index returned %d rows; want its own one", len(got))
	require.Equal(t, "globex-1", got[0].Title)

	// An update moves the entry within the tenant's own index only.
	got[0].State = "done"
	require.NoError(t, globexDAO.Save(ctx, got[0]))
	acmeReady, err := acmeDAO.QueryByIndex(ctx, "State", "ready")
	require.NoError(t, err)
	require.Len(t, acmeReady, 1, "another tenant's update must not touch acme's index")
	globexDone, err := globexDAO.QueryByIndex(ctx, "State", "done")
	require.NoError(t, err)
	require.Len(t, globexDone, 1)
	acmeDone, err := acmeDAO.QueryByIndex(ctx, "State", "done")
	require.NoError(t, err)
	require.Empty(t, acmeDone, "acme's index answered with globex's updated row")
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

	sys, err := resolver.Resolve(ctx, tenantid.System)
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
