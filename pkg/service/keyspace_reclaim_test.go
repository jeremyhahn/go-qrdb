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

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// This file covers go-qrdb#412: DROP DATABASE reclaimed the database
// directory and left a shared-store database's data in the shared store.
//
// The store here is memoryKVSvc, the same in-memory KVServicer the collection
// tests use, keyed exactly as production keys it: the composite
// "<tenant>/<db>/" namespace for everything resolved through service.Registry
// and root-level "__sys:collections/{db}/" and "__col:{db}:" for the
// collection surface, which is served by one server-wide CollectionServicer
// over the system shard and therefore carries no tenant.

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// reclaimIndexCatalog is an in-memory secondary index catalog. It is separate
// from memoryIndexSvc because a reclamation enumerates the catalog, which
// memoryIndexSvc cannot do.
type reclaimIndexCatalog struct {
	mu       sync.Mutex
	names    map[string]struct{}
	dropped  []string
	listErr  error
	dropErr  error
	dropFail string
}

func newReclaimIndexCatalog(names ...string) *reclaimIndexCatalog {
	c := &reclaimIndexCatalog{names: make(map[string]struct{}, len(names))}
	for _, n := range names {
		c.names[n] = struct{}{}
	}
	return c
}

func (c *reclaimIndexCatalog) ListIndexes(context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := make([]string, 0, len(c.names))
	for n := range c.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (c *reclaimIndexCatalog) DropIndex(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropErr != nil && (c.dropFail == "" || c.dropFail == name) {
		return c.dropErr
	}
	c.dropped = append(c.dropped, name)
	delete(c.names, name)
	return nil
}

func (c *reclaimIndexCatalog) remaining() []string {
	names, _ := c.ListIndexes(context.Background())
	return names
}

// reclaimVectorStore is an in-memory vector namespace catalog.
type reclaimVectorStore struct {
	mu      sync.Mutex
	spaces  map[string]struct{}
	listErr error
	delErr  error
}

func newReclaimVectorStore(names ...string) *reclaimVectorStore {
	v := &reclaimVectorStore{spaces: make(map[string]struct{}, len(names))}
	for _, n := range names {
		v.spaces[n] = struct{}{}
	}
	return v
}

func (v *reclaimVectorStore) ListNamespaces(context.Context, *ListNamespacesRequest) (*ListNamespacesResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.listErr != nil {
		return nil, v.listErr
	}
	out := make([]*NamespaceInfo, 0, len(v.spaces))
	for n := range v.spaces {
		out = append(out, &NamespaceInfo{Namespace: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return &ListNamespacesResponse{Namespaces: out}, nil
}

func (v *reclaimVectorStore) DeleteNamespace(_ context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.delErr != nil {
		return nil, v.delErr
	}
	delete(v.spaces, req.Namespace)
	return &DeleteNamespaceResponse{}, nil
}

func (v *reclaimVectorStore) remaining() []string {
	resp, _ := v.ListNamespaces(context.Background(), nil)
	out := make([]string, 0, len(resp.Namespaces))
	for _, n := range resp.Namespaces {
		out = append(out, n.Namespace)
	}
	return out
}

// fixedStoreResolver reports one answer for every tenant.
type fixedStoreResolver struct {
	target    ReclaimTarget
	dedicated bool
	err       error
	calls     int
}

func (r *fixedStoreResolver) ReclaimTarget(context.Context, string) (ReclaimTarget, bool, error) {
	r.calls++
	return r.target, r.dedicated, r.err
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// populatedStore returns a store keyed exactly as production keys it for
// tenant/database "acme"/"reports", plus a second database and a second
// tenant that must survive every reclamation.
//
// Every key shape a database owns is represented, because the failure this
// pins is a teardown that reclaimed a SUBSET: the composite namespace alone
// leaves every document behind, and the key prefixes alone leave the index
// catalog and the vector namespaces behind.
func populatedStore() *memoryKVSvc {
	kv := newMemoryKVSvc()
	ctx := context.Background()
	ns := tenant.ComposeNamespace("acme", "reports")

	put := func(k string) { _ = kv.Put(ctx, k, []byte("v")) }

	// Namespaced: plain KV, relational catalog, a row, an index entry, every
	// sequence shape, a privilege record.
	put(ns + "greeting")
	put(ns + "__sys:tables/reports/orders")
	put(ns + "__tbl:reports:7/pk1")
	put(ns + "__idx:reports:7:by_customer/acme")
	put(ns + "__seq:reports:__tableid")
	put(ns + "__seq:reports:__autoinc:7")
	put(ns + "__seq:reports:__rowid:7")
	put(ns + "__seq:reports:__usq:invoice_no")
	put(ns + "__seq:reports:__collection:users")
	put(ns + "__sys:privs/reports/alice")

	// Namespaced time series: the namespace lands INSIDE the series prefixes
	// because it is the metric NAME the namespaced view prefixes.
	put(timeseries.KeyPrefixPoints + ns + "cpu:0001:abcd")
	put(timeseries.KeyPrefixMetricIndex + ns + "cpu")
	put(timeseries.KeyPrefixRetention + ns + "cpu")

	// Root-keyed collection surface: database segment, no tenant.
	put(collectionMetadataPrefix + "reports/users")
	put(collectionDataPrefix + "reports:users/1")
	put(collectionDataPrefix + "reports:users/2")

	// Must survive: another database of the same tenant, the same database
	// name under another tenant, a database-agnostic system record, and the
	// tag index, which carries no metric and so belongs to no database.
	put(tenant.ComposeNamespace("acme", "ledger") + "keep")
	put(tenant.ComposeNamespace("other", "reports") + "keep")
	put(collectionMetadataPrefix + "ledger/users")
	put(collectionDataPrefix + "ledger:users/1")
	put("__sys:roles/admin")
	put(timeseries.KeyPrefixTagIndex + "host:web1")

	return kv
}

// survivors is the set of keys populatedStore holds that no reclamation of
// ("acme", "reports") may touch.
func survivors() []string {
	keep := []string{
		"__sys:roles/admin",
		collectionDataPrefix + "ledger:users/1",
		collectionMetadataPrefix + "ledger/users",
		tenant.ComposeNamespace("acme", "ledger") + "keep",
		tenant.ComposeNamespace("other", "reports") + "keep",
		timeseries.KeyPrefixTagIndex + "host:web1",
	}
	sort.Strings(keep)
	return keep
}

// allKeys returns every key in the store, sorted -- INCLUDING internal keys.
//
// It calls ListAll, not List, and that is not a convenience. memoryKVSvc.List
// applies the internal-key discovery guard exactly as the production
// KVServicer does, so a `List("")` here would not see "__sys:roles/admin" or
// the time-series keys and these assertions would silently stop checking the
// residue they exist to check. An assertion about what is left in a store has
// to see the whole store.
func allKeys(t *testing.T, kv *memoryKVSvc) []string {
	t.Helper()
	keys, err := kv.ListAll(context.Background(), "")
	require.NoError(t, err)
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// The defect
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_ReclaimsEveryShapeADatabaseOwns is the failing-first
// test for go-qrdb#412. Before the fix nothing reclaimed any of these keys:
// Manager.Drop removed the database's directory, which for a shared-store
// database holds configuration and no data at all.
func TestKeyspaceReclaim_ReclaimsEveryShapeADatabaseOwns(t *testing.T) {
	kv := populatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))

	assert.Equal(t, survivors(), allKeys(t, kv),
		"dropping a shared-store database must leave exactly the keys that belong to other databases and other tenants")
}

// TestKeyspaceReclaim_ReclaimsTheCollectionSurface pins the half of the
// keyspace a composite-prefix scan alone would miss.
//
// Collections are served by ONE server-wide CollectionServicer over the
// system shard's root KV rather than through service.Registry, so their keys
// carry the database as a segment and carry no tenant. A teardown that
// reclaimed only "<tenant>/<db>/" would report success with every document
// still in the store.
func TestKeyspaceReclaim_ReclaimsTheCollectionSurface(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))

	for _, prefix := range CollectionKeyPrefixes("reports") {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, keys, "collection keys under %q survived the drop", prefix)
	}
	ledger, err := kv.ListAll(ctx, collectionDataPrefix+"ledger:")
	require.NoError(t, err)
	assert.Len(t, ledger, 1, "another database's documents must not be reclaimed")
}

// TestKeyspaceReclaim_ReclaimsEverySequenceUnderTheDatabase is the inheritance
// go-qrdb#411 handed this rung: the residue is not one counter but the whole
// "__seq:{db}:" family, including the per-DATABASE "__tableid" that DROP
// TABLE could never reclaim because it is not per table.
func TestKeyspaceReclaim_ReclaimsEverySequenceUnderTheDatabase(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	ns := tenant.ComposeNamespace("acme", "reports")

	before, err := kv.ListAll(ctx, ns+"__seq:")
	require.NoError(t, err)
	require.Len(t, before, 5, "fixture must carry __tableid, __autoinc, __rowid, __usq and __collection")

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))

	after, err := kv.ListAll(ctx, ns+"__seq:")
	require.NoError(t, err)
	assert.Empty(t, after, "every sequence counter under the database must be reclaimed, __tableid included")
}

// TestKeyspaceReclaim_ReclaimsBothIndexNameShapes covers the catalog the key
// prefixes cannot reach. The state machine's index catalog is a namespace of
// its own, and index names take the same two shapes the keys do: composite
// namespace for a registry-resolved index, "{db}.{collection}.{field}" for a
// collection field index.
func TestKeyspaceReclaim_ReclaimsBothIndexNameShapes(t *testing.T) {
	ns := tenant.ComposeNamespace("acme", "reports")
	catalog := newReclaimIndexCatalog(
		ns+"by_region",
		"reports.users.email",
		tenant.ComposeNamespace("acme", "ledger")+"by_region",
		"ledger.users.email",
		"__sys_internal",
	)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Index: catalog})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))

	assert.Equal(t, []string{
		"__sys_internal",
		tenant.ComposeNamespace("acme", "ledger") + "by_region",
		"ledger.users.email",
	}, catalog.remaining(), "only the dropped database's indexes may be dropped")
}

// TestKeyspaceReclaim_ReclaimsVectorNamespaces covers the second namespace a
// key delete cannot reach: vectors live in their own per-node store.
func TestKeyspaceReclaim_ReclaimsVectorNamespaces(t *testing.T) {
	ns := tenant.ComposeNamespace("acme", "reports")
	vec := newReclaimVectorStore(
		ns+"docs",
		ns+"images",
		tenant.ComposeNamespace("acme", "ledger")+"docs",
		tenant.ComposeNamespace("other", "reports")+"docs",
	)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Vector: vec})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))

	assert.Equal(t, []string{
		tenant.ComposeNamespace("acme", "ledger") + "docs",
		tenant.ComposeNamespace("other", "reports") + "docs",
	}, vec.remaining())
}

// ---------------------------------------------------------------------------
// Name reuse
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_RecreatedNameInheritsNothing is the name-reuse question
// go-qrdb#411 answered for a collection's counter, asked one level up.
//
// The answer measured before the fix was worse than "the counter resumes": a
// recreated database inherited its predecessor's keys, collections and
// documents VERBATIM, because nothing was ever deleted and the composite
// namespace is derived from the name. This asserts the post-fix state -- a
// recreated name starts empty -- from the same store.
func TestKeyspaceReclaim_RecreatedNameInheritsNothing(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))

	// Everything a database of that name would address, from a fresh create.
	ns := tenant.ComposeNamespace("acme", "reports")
	for _, prefix := range append(NamespacedKeyPrefixes(ns), CollectionKeyPrefixes("reports")...) {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, keys, "a database recreated under this name would inherit %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Idempotence and interruption
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_IsIdempotent holds the contract a re-issued drop
// depends on: an already-empty keyspace is success, not a not-found error.
func TestKeyspaceReclaim_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	catalog := newReclaimIndexCatalog(tenant.ComposeNamespace("acme", "reports") + "by_region")
	vec := newReclaimVectorStore(tenant.ComposeNamespace("acme", "reports") + "docs")
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv, Index: catalog, Vector: vec})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))
	first := allKeys(t, kv)

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"),
		"reclaiming an already-empty keyspace must succeed so a re-issued drop can finish an interrupted one")
	assert.Equal(t, first, allKeys(t, kv))
	assert.Empty(t, catalog.remaining())
	assert.Empty(t, vec.remaining())
}

// TestKeyspaceReclaim_RetryAfterAFailureCompletes drives the interruption
// end to end: a reclamation that fails part way leaves a live, partly emptied
// database, and re-issuing it finishes the job.
func TestKeyspaceReclaim_RetryAfterAFailureCompletes(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	catalog := newReclaimIndexCatalog(tenant.ComposeNamespace("acme", "reports") + "by_region")
	catalog.dropErr = errors.New("index catalog unavailable")
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv, Index: catalog})

	err := svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports")
	var idxErr *ReclaimIndexError
	require.ErrorAs(t, err, &idxErr, "an index that cannot be dropped must fail the reclamation, not be skipped")

	// The keys the failed attempt did reach are gone; the index is not.
	assert.NotEmpty(t, catalog.remaining())

	catalog.dropErr = nil
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))
	assert.Equal(t, survivors(), allKeys(t, kv))
	assert.Empty(t, catalog.remaining())
}

// ---------------------------------------------------------------------------
// Failure is refusal, not a silent skip
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_FailuresAreTyped covers each failure surface. Every one
// fails the whole reclamation: reporting a successful DROP DATABASE over data
// that is still in the store is the defect itself.
func TestKeyspaceReclaim_FailuresAreTyped(t *testing.T) {
	boom := errors.New("boom")
	ns := tenant.ComposeNamespace("acme", "reports")

	tests := []struct {
		name   string
		target func() ReclaimTarget
		assert func(t *testing.T, err error)
	}{
		{
			name: "list fails",
			target: func() ReclaimTarget {
				return ReclaimTarget{KV: &failingReclaimKV{listErr: boom}}
			},
			assert: func(t *testing.T, err error) {
				var want *ReclaimPrefixError
				require.ErrorAs(t, err, &want)
				assert.Equal(t, "list", want.Op)
				assert.ErrorIs(t, err, boom)
			},
		},
		{
			name: "delete fails",
			target: func() ReclaimTarget {
				return ReclaimTarget{KV: &failingReclaimKV{keys: []string{"k"}, deleteErr: boom}}
			},
			assert: func(t *testing.T, err error) {
				var want *ReclaimPrefixError
				require.ErrorAs(t, err, &want)
				assert.Equal(t, "delete", want.Op)
			},
		},
		{
			name: "index list fails",
			target: func() ReclaimTarget {
				c := newReclaimIndexCatalog()
				c.listErr = boom
				return ReclaimTarget{KV: newMemoryKVSvc(), Index: c}
			},
			assert: func(t *testing.T, err error) {
				var want *ReclaimIndexError
				require.ErrorAs(t, err, &want)
				assert.Equal(t, "list", want.Op)
			},
		},
		{
			name: "vector list fails",
			target: func() ReclaimTarget {
				v := newReclaimVectorStore()
				v.listErr = boom
				return ReclaimTarget{KV: newMemoryKVSvc(), Vector: v}
			},
			assert: func(t *testing.T, err error) {
				var want *ReclaimVectorError
				require.ErrorAs(t, err, &want)
				assert.Equal(t, "list", want.Op)
			},
		},
		{
			name: "vector delete fails",
			target: func() ReclaimTarget {
				v := newReclaimVectorStore(ns + "docs")
				v.delErr = boom
				return ReclaimTarget{KV: newMemoryKVSvc(), Vector: v}
			},
			assert: func(t *testing.T, err error) {
				var want *ReclaimVectorError
				require.ErrorAs(t, err, &want)
				assert.Equal(t, "delete", want.Op)
				assert.Equal(t, ns+"docs", want.Namespace)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewKeyspaceReclaimService(tc.target())
			err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports")
			require.Error(t, err)
			tc.assert(t, err)
		})
	}
}

// failingReclaimKV is a ReclaimKV whose two verbs can be made to fail.
type failingReclaimKV struct {
	keys      []string
	listErr   error
	deleteErr error
}

func (f *failingReclaimKV) List(context.Context, string) ([]string, error) {
	return f.keys, f.listErr
}

func (f *failingReclaimKV) Delete(context.Context, string) error { return f.deleteErr }

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_RefusesAnUnscopedRequest holds the guard that keeps a
// teardown from becoming a store wipe: the unscoped form of "delete
// everything this database owns" is "delete everything".
func TestKeyspaceReclaim_RefusesAnUnscopedRequest(t *testing.T) {
	kv := populatedStore()
	before := allKeys(t, kv)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "")

	var want *ReclaimScopeError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, before, allKeys(t, kv), "a refused reclamation must not have deleted anything")
}

// TestKeyspaceReclaim_EmptyTenantIsTheSystemTenant pins that an unowned
// database -- the bootstrap databases a node creates before any tenant exists
// carry no TenantID -- reclaims the namespace tenant.ComposeNamespace gives
// it rather than an "//" prefix that matches nothing.
func TestKeyspaceReclaim_EmptyTenantIsTheSystemTenant(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	ns := tenant.ComposeNamespace("", "bootstrap")
	require.NoError(t, kv.Put(ctx, ns+"k", []byte("v")))

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "", "bootstrap"))

	assert.Empty(t, allKeys(t, kv))
}

// ---------------------------------------------------------------------------
// Cost
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_DeletesEveryKeyIndividually pins the cost as well as
// the effect: the reclamation names every key, because no engine here offers
// a range delete and the batch encoder speaks a dialect the KV engine does
// not (see reclaimKeys).
func TestKeyspaceReclaim_DeletesEveryKeyIndividually(t *testing.T) {
	kv := populatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))

	assert.Equal(t, survivors(), allKeys(t, kv))
}

// ---------------------------------------------------------------------------
// Store resolution
// ---------------------------------------------------------------------------

// TestKeyspaceReclaim_RoutesToTheTenantsOwnStore covers the volume-isolated
// tenant: its databases' namespaced keys are on a dedicated per-tenant Host,
// so scanning the system store would report success over untouched data.
func TestKeyspaceReclaim_RoutesToTheTenantsOwnStore(t *testing.T) {
	ctx := context.Background()
	system := populatedStore()
	volume := newMemoryKVSvc()
	ns := tenant.ComposeNamespace("acme", "reports")
	require.NoError(t, volume.Put(ctx, ns+"on-the-volume", []byte("v")))

	resolver := &fixedStoreResolver{target: ReclaimTarget{KV: volume}, dedicated: true}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	svc.SetStoreResolver(resolver)

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports"))

	assert.Empty(t, allKeys(t, volume), "the tenant's own store must be reclaimed")
	// The collection surface is in the SYSTEM store for every database, so it
	// is reclaimed there even when the namespaced keys are elsewhere; the
	// namespaced keys in the system store belong to no one and are left.
	remaining, err := system.ListAll(ctx, collectionDataPrefix+"reports:")
	require.NoError(t, err)
	assert.Empty(t, remaining, "collections live in the system store for every database")
	assert.Equal(t, 1, resolver.calls)
}

// TestKeyspaceReclaim_UsesTheSystemStoreForANonDedicatedTenant is the control
// for the branch above.
func TestKeyspaceReclaim_UsesTheSystemStoreForANonDedicatedTenant(t *testing.T) {
	kv := populatedStore()
	resolver := &fixedStoreResolver{dedicated: false}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	svc.SetStoreResolver(resolver)

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))

	assert.Equal(t, survivors(), allKeys(t, kv))
}

// TestKeyspaceReclaim_ResolverFailureFailsTheDrop pins the fail-closed
// posture. A resolver that cannot say where the keys are must not be read as
// "they are in the system store": that would delete nothing and report
// success.
func TestKeyspaceReclaim_ResolverFailureFailsTheDrop(t *testing.T) {
	kv := populatedStore()
	before := allKeys(t, kv)
	resolver := &fixedStoreResolver{dedicated: true, err: errors.New("host unreachable")}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	svc.SetStoreResolver(resolver)

	err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports")

	var want *ReclaimStoreUnavailableError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "acme", want.TenantID)
	// The collection reclamation runs first and is not affected; the
	// namespaced keys are untouched and the drop is retryable.
	after := allKeys(t, kv)
	assert.Contains(t, after, tenant.ComposeNamespace("acme", "reports")+"greeting")
	assert.Less(t, len(after), len(before))
}

// TestKeyspaceReclaim_NilEnginesAreSkipped covers a composition that serves
// no indexes or vectors: the reclamation must not panic or refuse.
func TestKeyspaceReclaim_NilEnginesAreSkipped(t *testing.T) {
	svc := NewKeyspaceReclaimService(ReclaimTarget{})
	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"))
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (f *failingReclaimKV) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return f.List(a0, a1)
}
