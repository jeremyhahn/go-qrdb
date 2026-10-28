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
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// This file covers go-qrdb#412: DROP DATABASE reclaimed the database
// directory and left a shared-store database's data in the shared store.
//
// It also covers go-qrdb#481, which is the boundary of #412's answer: the
// reclamation reached ROOT-KEYED collection keys, which carry a database and
// no tenant, so one tenant's drop deleted every tenant's pre-tenant-namespace
// collection residue for a database of that name. Those keys are now reclaimed
// by nothing, which is what go-qrdb#441 decided; the namespaced keys a
// collection writes TODAY are still reclaimed.
//
// The store here is memoryKVSvc, the same in-memory KVServicer the collection
// tests use, keyed exactly as production keys it: the composite
// "<tenant>/<db>/" namespace for everything resolved through service.Registry,
// plus root-level "__sys:collections/{db}/" and "__col:{db}:" residue from
// before the collection surface moved behind that resolver.

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
	target      ReclaimTarget
	dedicated   bool
	perDatabase bool
	err         error
	calls       int
}

func (r *fixedStoreResolver) ReclaimTarget(context.Context, string) (ReclaimTarget, bool, error) {
	r.calls++
	return r.target, r.dedicated, r.err
}

// FenceTarget answers with the same store ReclaimTarget does: the fixture
// routes a database's fence to where its keys are.
func (r *fixedStoreResolver) FenceTarget(context.Context, string, string) (FenceStore, bool, error) {
	r.calls++
	return FenceStore{KV: r.target.KV, PerDatabase: r.perDatabase}, r.dedicated, r.err
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

	// Namespaced collection surface: this is where a collection created TODAY
	// writes, and it is reclaimed by the composite prefix like everything else.
	put(ns + collectionMetadataPrefix + "reports/users")
	put(ns + collectionDataPrefix + "reports:users/1")

	// Root-keyed collection residue: database segment, NO tenant. Written
	// before the collection surface moved behind the per-(tenant, database)
	// resolver, and therefore attributable to no tenant -- so it must survive
	// this tenant's drop exactly as it survives every other tenant's.
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

		// The pre-tenant-namespace collection residue for "reports". It
		// carries no tenant, so the tenant dropping "reports" does not own it
		// and no scan can find out who does. go-qrdb#441 left it in place,
		// inert and exportable; go-qrdb#481 is what happened when a drop began
		// deleting it anyway.
		collectionDataPrefix + "reports:users/1",
		collectionDataPrefix + "reports:users/2",
		collectionMetadataPrefix + "reports/users",
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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))

	assert.Equal(t, survivors(), allKeys(t, kv),
		"dropping a shared-store database must leave exactly the keys that belong to other databases and other tenants")
}

// TestKeyspaceReclaim_ReclaimsTheNamespacedCollectionSurface pins the half of
// the collection surface a drop DOES own: a collection created today is served
// by a per-(tenant, database) view, so its catalog entry and its documents sit
// inside the composite namespace and are the dropping tenant's to remove.
func TestKeyspaceReclaim_ReclaimsTheNamespacedCollectionSurface(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	ns := tenant.ComposeNamespace("acme", "reports")

	before, err := kv.ListAll(ctx, ns+collectionDataPrefix)
	require.NoError(t, err)
	require.NotEmpty(t, before, "fixture must carry a namespaced collection document")

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	for _, prefix := range []string{ns + collectionMetadataPrefix, ns + collectionDataPrefix} {
		keys, lErr := kv.ListAll(ctx, prefix)
		require.NoError(t, lErr)
		assert.Empty(t, keys, "namespaced collection keys under %q survived the drop", prefix)
	}
}

// TestKeyspaceReclaim_LeavesUnattributableCollectionResidue is go-qrdb#481's
// failing-first test.
//
// Two tenants, a database of the same name, and pre-tenant-namespace
// collection records that name that database and no tenant. One tenant drops
// it. Before the fix, step 1 of the reclamation prefix-deleted
// "__col:reports:" and "__sys:collections/reports/" from the shared system
// store unconditionally, so the OTHER tenant's records went with it -- taking
// the export-and-reimport upgrade that go-qrdb#441 documented as the only path
// for that data.
//
// Both halves are asserted. A fix that stops deleting the residue by also
// stopping the legitimate reclamation is not a fix, so the dropping tenant's
// own namespaced collection data must still be gone.
func TestKeyspaceReclaim_LeavesUnattributableCollectionResidue(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	put := func(k string) { require.NoError(t, kv.Put(ctx, k, []byte("v"))) }

	nsA := tenant.ComposeNamespace("acme", "reports")
	nsB := tenant.ComposeNamespace("globex", "reports")

	// Each tenant has a namespaced collection in its own "reports".
	put(nsA + collectionMetadataPrefix + "reports/orders")
	put(nsA + collectionDataPrefix + "reports:orders/1")
	put(nsB + collectionMetadataPrefix + "reports/orders")
	put(nsB + collectionDataPrefix + "reports:orders/1")

	// And a single pile of legacy records that names "reports" and no tenant.
	// Which of the two wrote them is not recoverable from anywhere, which is
	// exactly why neither tenant's drop may delete them.
	legacy := LegacyCollectionKeyPrefixes("reports")
	put(collectionMetadataPrefix + "reports/orders")
	put(collectionDataPrefix + "reports:orders/1")
	put(collectionDataPrefix + "reports:orders/2")

	catalog := newReclaimIndexCatalog(nsA+"by_region", nsB+"by_region", "reports.orders.sku")
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv, Index: catalog})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	// 1. The unattributable residue is untouched -- all of it, including the
	//    dropping tenant's share of it, because there is no such thing as a
	//    share of it.
	for _, prefix := range legacy {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, keys,
			"a drop deleted pre-tenant-namespace collection records under %q; they belong to "+
				"no tenant this call can identify, and they are the only thing left to export", prefix)
	}
	assert.Contains(t, catalog.remaining(), "reports.orders.sku",
		"a legacy collection index name carries no tenant either")

	// 2. The other tenant's database of the same name is entirely untouched.
	for _, prefix := range []string{nsB + collectionMetadataPrefix, nsB + collectionDataPrefix} {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, keys, "the other tenant lost %q to a drop it did not issue", prefix)
	}
	assert.Contains(t, catalog.remaining(), nsB+"by_region")

	// 3. And the drop still did its job: the dropping tenant's namespaced
	//    collection data is gone.
	for _, prefix := range []string{nsA + collectionMetadataPrefix, nsA + collectionDataPrefix} {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, keys, "the dropping tenant's own namespaced collection data survived under %q", prefix)
	}
	assert.NotContains(t, catalog.remaining(), nsA+"by_region")
}

// TestKeyspaceReclaim_TenantTeardownLeavesTheResidueToo is the same decision
// asked one scope up. A tenant delete cannot attribute the legacy keys either,
// and DeleteTenant's bottom-up refusal means the database drops have already
// run -- so if a drop leaves them, nothing removes them, which is what
// go-qrdb#441 says should happen.
func TestKeyspaceReclaim_TenantTeardownLeavesTheResidueToo(t *testing.T) {
	ctx := context.Background()
	kv := populatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimTenantKeyspace(ctx, "acme"))

	for _, prefix := range LegacyCollectionKeyPrefixes("reports") {
		keys, err := kv.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, keys, "a tenant teardown deleted unattributable residue under %q", prefix)
	}
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
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))

	assert.Equal(t, []string{
		"__sys_internal",
		tenant.ComposeNamespace("acme", "ledger") + "by_region",
		"ledger.users.email",
		"reports.users.email",
	}, catalog.remaining(),
		"only the dropped database's NAMESPACED indexes may be dropped; "+
			"\"reports.users.email\" names a database and no tenant, so it is not this tenant's to drop")
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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))

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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	// Everything a database of that name would address, from a fresh create.
	// The legacy root-keyed prefixes are NOT in this list, and that is not a
	// gap: a recreated database is served through the per-(tenant, database)
	// resolver, so it addresses the composite namespace only and cannot
	// compose a bare "__col:reports:" key at all.
	ns := tenant.ComposeNamespace("acme", "reports")
	for _, prefix := range NamespacedKeyPrefixes(ns) {
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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))
	first := allKeys(t, kv)

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""),
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

	err := svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "")
	var idxErr *ReclaimIndexError
	require.ErrorAs(t, err, &idxErr, "an index that cannot be dropped must fail the reclamation, not be skipped")

	// The keys the failed attempt did reach are gone; the index is not.
	assert.NotEmpty(t, catalog.remaining())

	catalog.dropErr = nil
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))
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
			err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", "")
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
	putErr    error
}

func (f *failingReclaimKV) List(context.Context, string) ([]string, error) {
	return f.keys, f.listErr
}

func (f *failingReclaimKV) Delete(context.Context, string) error { return f.deleteErr }

func (f *failingReclaimKV) Put(context.Context, string, []byte) error { return f.putErr }

func (f *failingReclaimKV) Get(_ context.Context, key string) ([]byte, error) {
	return nil, &KeyNotFoundError{Key: key}
}

func (f *failingReclaimKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, f.putErr
}

func (f *failingReclaimKV) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, f.putErr
}

func (f *failingReclaimKV) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, f.deleteErr
}

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

	err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "", "")

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
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "", "bootstrap", ""))

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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))

	assert.Equal(t, survivors(), allKeys(t, kv))
}

type countingRangeKV struct {
	*memoryKVSvc
	ranges  int
	deletes int
}

func (c *countingRangeKV) Delete(ctx context.Context, key string) error {
	c.deletes++
	return c.memoryKVSvc.Delete(ctx, key)
}

func (c *countingRangeKV) DeleteRange(ctx context.Context, start, end []byte) error {
	c.ranges++
	return c.memoryKVSvc.DeleteRange(ctx, start, end)
}

func TestKeyspaceReclaim_UsesRangeDeletePerPrefix(t *testing.T) {
	kv := &countingRangeKV{memoryKVSvc: populatedStore()}
	ctx := context.Background()
	ns := tenant.ComposeNamespace("acme", "reports")
	for i := 0; i < reclaimRangeDeleteChunkSize+1; i++ {
		require.NoError(t, kv.Put(ctx, ns+"bulk/"+strconv.Itoa(i), []byte("v")))
	}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	assert.Equal(t, survivors(), allKeys(t, kv.memoryKVSvc))
	assert.Greater(t, kv.ranges, 1)
	assert.Less(t, kv.ranges, reclaimRangeDeleteChunkSize)
	assert.Zero(t, kv.deletes)
}

// refusingRangeKV range-deletes its first allowed calls and then answers err
// without deleting anything, as a shard that has not activated the gate does.
type refusingRangeKV struct {
	countingRangeKV
	allowed int
	err     error
}

func (r *refusingRangeKV) DeleteRange(ctx context.Context, start, end []byte) error {
	if r.ranges >= r.allowed {
		return r.err
	}
	return r.countingRangeKV.DeleteRange(ctx, start, end)
}

func populatedRefusingRangeKV(t *testing.T, allowed int, err error) *refusingRangeKV {
	t.Helper()
	kv := &refusingRangeKV{countingRangeKV: countingRangeKV{memoryKVSvc: populatedStore()}, allowed: allowed, err: err}
	ns := tenant.ComposeNamespace("acme", "reports")
	for i := 0; i < reclaimRangeDeleteChunkSize+1; i++ {
		require.NoError(t, kv.Put(context.Background(), ns+"bulk/"+strconv.Itoa(i), []byte("v")))
	}
	return kv
}

func TestKeyspaceReclaim_GateNotActiveFallsBackToPerKeyDeletes(t *testing.T) {
	kv := populatedRefusingRangeKV(t, 0, &feature.NotActiveError{Feature: featuregate.RangeDelete, ShardID: 1})
	ctx := context.Background()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	assert.Equal(t, survivors(), allKeys(t, kv.memoryKVSvc))
	assert.Zero(t, kv.ranges)
	assert.Greater(t, kv.deletes, reclaimRangeDeleteChunkSize)
}

func TestKeyspaceReclaim_GateNotActiveMidPrefixDeletesTheRest(t *testing.T) {
	kv := populatedRefusingRangeKV(t, 1, &feature.NotActiveError{Feature: featuregate.RangeDelete, ShardID: 1})
	ctx := context.Background()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	assert.Equal(t, survivors(), allKeys(t, kv.memoryKVSvc))
	assert.Equal(t, 1, kv.ranges)
	assert.Positive(t, kv.deletes)
	assert.Less(t, kv.deletes, reclaimRangeDeleteChunkSize, "the range-deleted chunk must not be deleted again")
}

func TestKeyspaceReclaim_RangeDeleteFailureFailsTheDrop(t *testing.T) {
	boom := errors.New("propose failed")
	kv := populatedRefusingRangeKV(t, 0, boom)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", "")

	var prefixErr *ReclaimPrefixError
	require.ErrorAs(t, err, &prefixErr)
	assert.Equal(t, "delete_range", prefixErr.Op)
	assert.ErrorIs(t, err, boom)
	assert.Zero(t, kv.deletes, "a failure that is not the gate's refusal must not be guessed around")
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

	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""))

	assert.Empty(t, allKeys(t, volume), "the tenant's own store must be reclaimed")
	// The system store is not touched at all. Everything the drop owns is
	// namespaced, and for this tenant the namespace lives on the volume; the
	// only thing of that database's NAME in the system store is
	// unattributable residue that belongs to no tenant this call can identify.
	remaining, err := system.ListAll(ctx, collectionDataPrefix+"reports:")
	require.NoError(t, err)
	assert.NotEmpty(t, remaining, "a drop routed to a dedicated store must not reach into the shared one")
	assert.Equal(t, 1, resolver.calls)
}

// TestKeyspaceReclaim_UsesTheSystemStoreForANonDedicatedTenant is the control
// for the branch above.
func TestKeyspaceReclaim_UsesTheSystemStoreForANonDedicatedTenant(t *testing.T) {
	kv := populatedStore()
	resolver := &fixedStoreResolver{dedicated: false}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	svc.SetStoreResolver(resolver)

	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))

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

	err := svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", "")

	var want *ReclaimStoreUnavailableError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "acme", want.TenantID)
	// The resolution is now the FIRST thing the reclamation does, so a drop
	// that cannot find its store deletes nothing at all and is retryable in
	// full. Before go-qrdb#481 an unscoped collection pass ran ahead of it and
	// had already deleted keys by the time this error surfaced.
	assert.Equal(t, before, allKeys(t, kv),
		"a drop that could not resolve its store must not have deleted anything")
}

// TestKeyspaceReclaim_NilEnginesAreSkipped covers a composition that serves
// no indexes or vectors: the reclamation must not panic or refuse.
func TestKeyspaceReclaim_NilEnginesAreSkipped(t *testing.T) {
	svc := NewKeyspaceReclaimService(ReclaimTarget{})
	require.NoError(t, svc.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports", ""))
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (f *failingReclaimKV) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return f.List(a0, a1)
}

// ---------------------------------------------------------------------------
// The tenant scope (go-qrdb#413)
//
// Deleting a tenant reclaims everything the tenant owns, not everything one
// of its databases owns. The scope is one segment shorter -- "<tenant>/" is
// the PARENT of every "<tenant>/<db>/" its databases use and also the root of
// its identity records -- plus the placement range, which is keyed by tenant
// outside the namespace entirely.
// ---------------------------------------------------------------------------

// tenantPopulatedStore holds one key of every shape a TENANT owns, plus keys
// that must survive: another tenant's, a database-agnostic system record, and
// the tag index, which carries no metric and so belongs to nobody.
func tenantPopulatedStore() *memoryKVSvc {
	kv := newMemoryKVSvc()
	ctx := context.Background()
	put := func(k string) { _ = kv.Put(ctx, k, []byte("v")) }

	id := tenant.IdentityNamespace("acme")
	// Identity: tenant-wide, one segment above any database.
	put(id + "user/alice")
	put(id + "role/admin")
	// Two databases of the same tenant, both under the same tenant segment.
	put(tenant.ComposeNamespace("acme", "reports") + "greeting")
	put(tenant.ComposeNamespace("acme", "ledger") + "greeting")
	// Time series, whose namespace lands inside the series prefixes.
	put(timeseries.KeyPrefixPoints + tenant.ComposeNamespace("acme", "reports") + "cpu:1:a")
	put(timeseries.KeyPrefixMetricIndex + tenant.ComposeNamespace("acme", "reports") + "cpu")
	put(timeseries.KeyPrefixRetention + tenant.ComposeNamespace("acme", "reports") + "cpu")
	// A dedicated-raft database's placement entry: keyed by tenant, OUTSIDE
	// the tenant's namespace. Removing it is what stops the shard on every
	// node holding a replica.
	put(placement.PlacementPrefix + "acme/reports")
	// Collection work journal entries: keyed by tenant, outside the tenant's
	// namespace, in the shared store.
	put(CollectionWorkPrefix + "acme/reports/events")
	put(CollectionWorkPrefix + "acme/ledger/a/b")
	// Key B: acme's own login CA -- must be reclaimed with the rest of the
	// tenant (go-qrdb#560 review, non-blocking).
	put("__sys:cert_ca/acme/material")
	// A strict tenant's CA lives in its home store; the system store holds
	// only the published copy relying parties trust, which goes with it.
	put("__sys:cert_ca_pub/acme/certificate")

	// Must survive.
	put(tenant.IdentityNamespace("other") + "user/bob")
	put(tenant.ComposeNamespace("other", "reports") + "keep")
	put(placement.PlacementPrefix + "other/reports")
	put(CollectionWorkPrefix + "other/reports/events")
	put(CollectionWorkPrefix + "acme2/reports/events")
	put("__sys:roles/admin")
	put(timeseries.KeyPrefixTagIndex + "host:web1")
	// A different tenant's Key B CA, and the cluster CA (Key A, no tenant
	// segment at all): neither belongs to acme's reclamation.
	put("__sys:cert_ca/other/material")
	put("__sys:cert_ca_pub/acme2/certificate")
	put("__sys:cluster_ca/material")
	return kv
}

// tenantSurvivors is the set tenantPopulatedStore holds that no reclamation
// of "acme" may touch.
func tenantSurvivors() []string {
	keep := []string{
		"__sys:cert_ca/other/material",
		"__sys:cert_ca_pub/acme2/certificate",
		"__sys:cluster_ca/material",
		"__sys:roles/admin",
		CollectionWorkPrefix + "acme2/reports/events",
		CollectionWorkPrefix + "other/reports/events",
		placement.PlacementPrefix + "other/reports",
		tenant.ComposeNamespace("other", "reports") + "keep",
		tenant.IdentityNamespace("other") + "user/bob",
		timeseries.KeyPrefixTagIndex + "host:web1",
	}
	sort.Strings(keep)
	return keep
}

// TestReclaimTenant_ReclaimsEveryShapeATenantOwns is the failing-first test
// for go-qrdb#413: before this existed, DeleteTenant removed the metadata and
// the barrier registration and left every one of these keys in place.
func TestReclaimTenant_ReclaimsEveryShapeATenantOwns(t *testing.T) {
	kv := tenantPopulatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.ReclaimTenantKeyspace(context.Background(), "acme"))

	assert.Equal(t, tenantSurvivors(), allKeys(t, kv),
		"deleting a tenant must leave exactly the keys that belong to other tenants and to no tenant")
}

// TestReclaimTenant_ReclaimsBothPlanes states the property that makes one
// range enough for two planes: identity is tenant-wide ("<tenant>/") and data
// is per-database ("<tenant>/<db>/"), and the first contains the second.
func TestReclaimTenant_ReclaimsBothPlanes(t *testing.T) {
	id := tenant.IdentityNamespace("acme")
	ns := tenant.ComposeNamespace("acme", "reports")
	require.True(t, strings.HasPrefix(ns, id),
		"the composite namespace %q must sit under the identity namespace %q, "+
			"or a tenant reclamation needs a second range per database", ns, id)
}

// TestReclaimTenant_ReclaimsTheIndexCatalog: index names are namespaced too,
// and the catalog is not a range of keys, so it is enumerated separately.
func TestReclaimTenant_ReclaimsTheIndexCatalog(t *testing.T) {
	idx := newReclaimIndexCatalog(
		tenant.ComposeNamespace("acme", "reports")+"by_customer",
		tenant.ComposeNamespace("acme", "ledger")+"by_date",
		tenant.ComposeNamespace("other", "reports")+"by_customer",
		"unrelated",
	)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Index: idx})

	require.NoError(t, svc.ReclaimTenantKeyspace(context.Background(), "acme"))

	assert.Equal(t, []string{
		tenant.ComposeNamespace("other", "reports") + "by_customer",
		"unrelated",
	}, idx.remaining())
}

// TestReclaimTenant_ReclaimsVectorNamespaces: vectors live in their own
// per-node store and likewise survive a key-prefix delete.
func TestReclaimTenant_ReclaimsVectorNamespaces(t *testing.T) {
	vec := newReclaimVectorStore(
		tenant.ComposeNamespace("acme", "reports")+"docs",
		tenant.ComposeNamespace("acme", "ledger")+"docs",
		tenant.ComposeNamespace("other", "reports")+"docs",
	)
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Vector: vec})

	require.NoError(t, svc.ReclaimTenantKeyspace(context.Background(), "acme"))

	assert.Equal(t, []string{tenant.ComposeNamespace("other", "reports") + "docs"}, vec.remaining())
}

// TestReclaimTenant_RefusesTheSystemTenant is the guard that keeps a tenant
// reclamation from emptying the store: "system/" holds the bootstrap database
// and every system identity record.
func TestReclaimTenant_RefusesTheSystemTenant(t *testing.T) {
	for _, id := range []string{tenantid.System, ""} {
		kv := tenantPopulatedStore()
		before := allKeys(t, kv)
		svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

		var scope *ReclaimScopeError
		err := svc.ReclaimTenantKeyspace(context.Background(), id)
		require.ErrorAs(t, err, &scope, "ReclaimTenantKeyspace(%q) must be refused", id)
		assert.Equal(t, before, allKeys(t, kv), "a refused reclamation must delete nothing")
	}
}

// TestReclaimTenant_IsIdempotent: a keyspace that is already empty is the
// success case, which is what lets a re-issued delete finish an interrupted
// one.
func TestReclaimTenant_IsIdempotent(t *testing.T) {
	kv := tenantPopulatedStore()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	ctx := context.Background()

	require.NoError(t, svc.ReclaimTenantKeyspace(ctx, "acme"))
	require.NoError(t, svc.ReclaimTenantKeyspace(ctx, "acme"))
	assert.Equal(t, tenantSurvivors(), allKeys(t, kv))
}

// TestReclaimTenant_UsesTheSystemStore pins the one way the tenant scope
// differs from the database scope: no StoreResolver hop. A volume tenant's
// IAM ciphertext is in the shared store, and the dedicated store its data
// lived on has been destroyed wholesale by the time this runs -- routing
// there would ask a store that no longer exists to delete keys that are
// already gone.
func TestReclaimTenant_UsesTheSystemStore(t *testing.T) {
	system := tenantPopulatedStore()
	dedicated := newMemoryKVSvc()
	require.NoError(t, dedicated.Put(context.Background(),
		tenant.ComposeNamespace("acme", "reports")+"onDedicated", []byte("v")))

	resolver := &fixedStoreResolver{target: ReclaimTarget{KV: dedicated}, dedicated: true}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	svc.SetStoreResolver(resolver)

	require.NoError(t, svc.ReclaimTenantKeyspace(context.Background(), "acme"))

	assert.Equal(t, 0, resolver.calls, "the tenant scope must not consult the store resolver")
	assert.Equal(t, tenantSurvivors(), allKeys(t, system))
}

// TestReclaimTenant_FailuresAreTyped: every failure fails the delete rather
// than being logged and skipped, and each names which plane it came from.
func TestReclaimTenant_FailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("boom")

	t.Run("keys", func(t *testing.T) {
		svc := NewKeyspaceReclaimService(ReclaimTarget{
			KV: &failingReclaimKV{listErr: sentinel},
		})
		var pe *ReclaimPrefixError
		require.ErrorAs(t, svc.ReclaimTenantKeyspace(ctx, "acme"), &pe)
		assert.Equal(t, "list", pe.Op)
	})

	t.Run("indexes", func(t *testing.T) {
		idx := newReclaimIndexCatalog()
		idx.listErr = sentinel
		svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Index: idx})
		var ie *ReclaimIndexError
		require.ErrorAs(t, svc.ReclaimTenantKeyspace(ctx, "acme"), &ie)
		assert.Equal(t, "list", ie.Op)
	})

	t.Run("vectors", func(t *testing.T) {
		vec := newReclaimVectorStore()
		vec.listErr = sentinel
		svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Vector: vec})
		var ve *ReclaimVectorError
		require.ErrorAs(t, svc.ReclaimTenantKeyspace(ctx, "acme"), &ve)
		assert.Equal(t, "list", ve.Op)
	})
}

// TestTenantKeyPrefixes_CoverEveryOwnedShape is the declaration side of the
// same property, asserted against keys built from the layout constants rather
// than from literals.
func TestTenantKeyPrefixes_CoverEveryOwnedShape(t *testing.T) {
	prefixes := TenantKeyPrefixes("acme")
	require.NotEmpty(t, prefixes)

	owned := []string{
		tenant.IdentityNamespace("acme") + "user/alice",
		tenant.ComposeNamespace("acme", "reports") + "greeting",
		timeseries.KeyPrefixPoints + tenant.ComposeNamespace("acme", "reports") + "cpu:1:a",
		timeseries.KeyPrefixMetricIndex + tenant.ComposeNamespace("acme", "reports") + "cpu",
		timeseries.KeyPrefixRetention + tenant.ComposeNamespace("acme", "reports") + "cpu",
		placement.PlacementPrefix + "acme/reports",
		CollectionWorkPrefix + "acme/reports/events",
		// Key B: acme's own login CA (go-qrdb#560 review, non-blocking --
		// tenant deletion must remove it, not leave an orphaned CA an old
		// leaf naming acme could still authenticate against).
		"__sys:cert_ca/acme/material",
		"__sys:cert_ca_pub/acme/certificate",
	}
	for _, key := range owned {
		assert.True(t, hasAnyPrefix(key, prefixes), "no tenant prefix covers %q", key)
	}

	notOwned := []string{
		tenant.IdentityNamespace("other") + "user/bob",
		"__sys:roles/admin",
		timeseries.KeyPrefixTagIndex + "host:web1",
		placement.PlacementPrefix + "other/reports",
		CollectionWorkPrefix + "other/reports/events",
		CollectionWorkPrefix + "acme2/reports/events",
		// A DIFFERENT tenant's Key B CA, and the cluster CA (Key A, which
		// has no tenant segment at all): neither belongs to acme's
		// reclamation.
		"__sys:cert_ca/other/material",
		"__sys:cert_ca/acme2/material",
		"__sys:cert_ca_pub/other/certificate",
		"__sys:cert_ca_pub/acme2/certificate",
		"__sys:cluster_ca/material",
	}
	for _, key := range notOwned {
		assert.False(t, hasAnyPrefix(key, prefixes), "a tenant prefix wrongly covers %q", key)
	}
}

// TestTenantKeyPrefixes_SystemTenantAndEmptyReturnNothing: an unscoped
// reclamation must reclaim nothing, never everything.
func TestTenantKeyPrefixes_SystemTenantAndEmptyReturnNothing(t *testing.T) {
	assert.Nil(t, TenantKeyPrefixes(tenantid.System))
	assert.Nil(t, TenantKeyPrefixes(""))
	assert.Nil(t, TenantIndexNamePrefixes(tenantid.System))
	assert.Nil(t, TenantIndexNamePrefixes(""))
	assert.Equal(t, []string{tenant.IdentityNamespace("acme")}, TenantIndexNamePrefixes("acme"))
}
