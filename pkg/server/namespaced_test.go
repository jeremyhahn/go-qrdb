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

package server

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// Namespace prefix composition (L5: single-sourced via tenant.ComposeNamespace
// — see pkg/tenant/database_test.go for the fallback-formula cases; the tests
// below pin that restServiceRegistry actually calls it and produces the
// unchanged default-case physical prefix through the full resolve path.

// ---------------------------------------------------------------------------
// fakeCatalog
// ---------------------------------------------------------------------------

// fakeCatalog reports existence from a (tenant -> set of db names) map.
type fakeCatalog struct {
	dbs map[string]map[string]bool
}

func (c *fakeCatalog) DatabaseExists(tenantID, name string) bool {
	return c.dbs[tenantID][name]
}

// ---------------------------------------------------------------------------
// Existence-based resolve via the catalog
// ---------------------------------------------------------------------------

func TestResolve_CatalogExistence(t *testing.T) {
	cat := &fakeCatalog{dbs: map[string]map[string]bool{
		"system": {"reports": true},
		"acme":   {"reports": true},
	}}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", WithDatabaseCatalog(cat)).(*restServiceRegistry)

	// Named db that exists for the system tenant resolves.
	if _, err := r.KVService("system", "reports"); err != nil {
		t.Fatalf("system/reports should resolve: %v", err)
	}
	// Named db that does not exist for the tenant → 404.
	_, err := r.KVService("acme", "ghost")
	var dnf *rest.DatabaseNotFoundError
	if !errors.As(err, &dnf) {
		t.Fatalf("acme/ghost: want DatabaseNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KV isolation through the full registry (the three required scenarios)
// ---------------------------------------------------------------------------

// kvRegistry builds a registry whose default database is backed by a single
// shared in-memory KV store, plus a catalog that knows the named databases.
func kvRegistry(t *testing.T) (*restServiceRegistry, *mockKVService) {
	t.Helper()
	store := newMockKVService()
	reg := NewDatabaseServiceRegistry("system")
	reg.Register("system", &DatabaseServices{Name: "system", KV: store})
	cat := &fakeCatalog{dbs: map[string]map[string]bool{
		"system": {"reports": true},
		"acme":   {"reports": true},
	}}
	r := NewRESTServiceRegistry(reg, nil, "system", WithDatabaseCatalog(cat)).(*restServiceRegistry)
	return r, store
}

func TestKVIsolation_SystemBootstrap_Prefixed(t *testing.T) {
	r, store := kvRegistry(t)
	svc, err := r.KVService("system", "system")
	if err != nil {
		t.Fatal(err)
	}
	// The bootstrap (system, system) pair is prefixed like any other pair —
	// it does NOT get the shared store root. This closes the data-isolation
	// leak where the bootstrap database could scan every other database/tenant.
	if svc == service.KVServicer(store) {
		t.Fatalf("system/system must be wrapped, not the raw shared store")
	}
	if err := svc.Put(context.Background(), "k1", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.store["system/system/k1"]; !ok {
		t.Fatalf("expected prefixed key %q in store, have %v", "system/system/k1", store.store)
	}
	if _, ok := store.store["k1"]; ok {
		t.Fatalf("bootstrap key must NOT be written unprefixed at the store root: %v", store.store)
	}
}

func TestKVIsolation_NamedDB_IsolatedFromBootstrap(t *testing.T) {
	r, store := kvRegistry(t)
	ctx := context.Background()

	sys, _ := r.KVService("system", "system")
	rep, _ := r.KVService("system", "reports")

	if err := rep.Put(ctx, "k1", []byte("v-reports")); err != nil {
		t.Fatal(err)
	}
	// Physical key is namespaced under "system/reports/".
	if _, ok := store.store["system/reports/k1"]; !ok {
		t.Fatalf("expected physical key %q, have %v", "system/reports/k1", store.store)
	}
	// The bootstrap database cannot see the reports key.
	if _, err := sys.Get(ctx, "k1"); err == nil {
		t.Fatal("bootstrap db must not see reports/k1")
	}
	// reports reads back its own value with the logical key.
	got, err := rep.Get(ctx, "k1")
	if err != nil || string(got) != "v-reports" {
		t.Fatalf("reports Get: got %q err %v", got, err)
	}
}

func TestKVIsolation_CrossTenant_SameDBName(t *testing.T) {
	r, store := kvRegistry(t)
	ctx := context.Background()

	sysRep, _ := r.KVService("system", "reports")
	acmeRep, _ := r.KVService("acme", "reports")

	if err := sysRep.Put(ctx, "k", []byte("sys")); err != nil {
		t.Fatal(err)
	}
	if err := acmeRep.Put(ctx, "k", []byte("acme")); err != nil {
		t.Fatal(err)
	}
	// Distinct physical keys: "system/reports/k" vs "acme/reports/k".
	if string(store.store["system/reports/k"]) != "sys" {
		t.Errorf("system reports physical key wrong: %v", store.store)
	}
	if string(store.store["acme/reports/k"]) != "acme" {
		t.Errorf("acme reports physical key wrong: %v", store.store)
	}
	// Each tenant reads only its own value.
	if v, _ := sysRep.Get(ctx, "k"); string(v) != "sys" {
		t.Errorf("system reports Get = %q", v)
	}
	if v, _ := acmeRep.Get(ctx, "k"); string(v) != "acme" {
		t.Errorf("acme reports Get = %q", v)
	}
}

// ---------------------------------------------------------------------------
// MVCC wrapper
// ---------------------------------------------------------------------------

type recMVCC struct {
	service.MVCCKVServicer
	lastKey            string
	lastStart, lastEnd []byte
}

func (m *recMVCC) PutWithLease(_ context.Context, key string, _ []byte, _ int64) (*service.KeyValue, error) {
	m.lastKey = key
	return &kv.KeyValue{Key: []byte(key)}, nil
}
func (m *recMVCC) GetRevision(_ context.Context, key string) (*service.KeyValue, error) {
	m.lastKey = key
	return &kv.KeyValue{Key: []byte(key)}, nil
}
func (m *recMVCC) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	m.lastStart, m.lastEnd = start, end
	return []*service.KeyValue{{Key: append([]byte("acme/reports/"), 'x')}}, 1, 1, nil
}

func TestNamespacedMVCC_PrefixesAndStrips(t *testing.T) {
	rec := &recMVCC{}
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)
	ctx := context.Background()

	kvp, err := w.PutWithLease(ctx, "k", []byte("v"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastKey != "acme/reports/k" {
		t.Errorf("inner key = %q, want acme/reports/k", rec.lastKey)
	}
	if string(kvp.Key) != "k" {
		t.Errorf("returned key = %q, want k", kvp.Key)
	}

	kvs, _, _, err := w.Range(ctx, []byte("a"), []byte("z"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.lastStart) != "acme/reports/a" || string(rec.lastEnd) != "acme/reports/z" {
		t.Errorf("range bounds = %q,%q", rec.lastStart, rec.lastEnd)
	}
	if string(kvs[0].Key) != "x" {
		t.Errorf("range returned key = %q, want x", kvs[0].Key)
	}
}

func TestNamespacedMVCC_NilEndConfinedToNamespace(t *testing.T) {
	rec := &recMVCC{}
	w := newNamespacedMVCCService(rec, "p/", nil)
	if _, _, _, err := w.Range(context.Background(), []byte("a"), nil); err != nil {
		t.Fatal(err)
	}
	// A nil end must NOT be forwarded as nil: pkg/mvcc treats a nil end as the
	// end of the entire shared store, which would expose other namespaces. It
	// must instead become the namespace upper bound (the prefix successor).
	if want := "p0"; string(rec.lastEnd) != want {
		t.Errorf("nil end must be confined to namespace upper bound %q, got %q", want, rec.lastEnd)
	}
}

// ---------------------------------------------------------------------------
// TimeSeries wrapper
// ---------------------------------------------------------------------------

type recTS struct {
	service.TimeSeriesServicer
	lastMetric string
}

func (m *recTS) Write(_ context.Context, metric string, _ float64, _ time.Time, _ map[string]string) error {
	m.lastMetric = metric
	return nil
}
func (m *recTS) Query(_ context.Context, metric string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.lastMetric = metric
	return []timeseries.DataPoint{{Metric: metric}}, nil
}
func (m *recTS) ListMetrics(prefix string, _ int) ([]string, error) {
	m.lastMetric = prefix
	// Returns realistic stored names under the namespace prefix.
	return []string{"p/cpu", "p/mem"}, nil
}

func TestNamespacedTimeSeries_PrefixesAndStrips(t *testing.T) {
	rec := &recTS{}
	w := newNamespacedTimeSeriesService(rec, "acme/reports/")
	ctx := context.Background()

	if err := w.Write(ctx, "cpu", 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "acme/reports/cpu" {
		t.Errorf("inner metric = %q", rec.lastMetric)
	}

	pts, err := w.Query(ctx, "cpu", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if pts[0].Metric != "cpu" {
		t.Errorf("returned metric = %q, want cpu", pts[0].Metric)
	}
}

func TestNamespacedTimeSeries_ListMetricsStrips(t *testing.T) {
	rec := &recTS{}
	w := newNamespacedTimeSeriesService(rec, "p/")
	names, err := w.ListMetrics("c", 10)
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/c" {
		t.Errorf("inner prefix = %q, want p/c", rec.lastMetric)
	}
	if names[0] != "cpu" || names[1] != "mem" {
		t.Errorf("stripped names = %v, want [cpu mem]", names)
	}
}

// ---------------------------------------------------------------------------
// Index wrapper
// ---------------------------------------------------------------------------

type recIndex struct {
	service.IndexServicer
	lastName string
}

func (m *recIndex) Query(_ context.Context, name string, _ []byte) ([][]byte, error) {
	m.lastName = name
	return [][]byte{[]byte("p/doc1")}, nil
}
func (m *recIndex) ListIndexes(_ context.Context) ([]string, error) {
	return []string{"p/byEmail", "other/byName"}, nil
}

func TestNamespacedIndex_PrefixesAndStrips(t *testing.T) {
	rec := &recIndex{}
	w := newNamespacedIndexService(rec, "p/")
	ctx := context.Background()

	keys, err := w.Query(ctx, "byEmail", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastName != "p/byEmail" {
		t.Errorf("inner index name = %q", rec.lastName)
	}
	if string(keys[0]) != "doc1" {
		t.Errorf("returned entry key = %q, want doc1", keys[0])
	}
}

func TestNamespacedIndex_ListFiltersAndStrips(t *testing.T) {
	rec := &recIndex{}
	w := newNamespacedIndexService(rec, "p/")
	names, err := w.ListIndexes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "byEmail" {
		t.Errorf("ListIndexes = %v, want [byEmail]", names)
	}
}

// ---------------------------------------------------------------------------
// Lease wrapper
// ---------------------------------------------------------------------------

type recLease struct {
	service.LeaseServicer
}

// owner is the ownership record namespace p/ attaches to lease id.
func owner(id int64) []byte {
	return []byte(leaseOwnerKeyPrefix + "p/" + strconv.FormatInt(id, 10))
}

func (recLease) TimeToLive(_ context.Context, id int64) (*lease.Lease, error) {
	return &lease.Lease{ID: id, Keys: [][]byte{owner(id), []byte("p/k1"), []byte("other/k2")}}, nil
}
func (recLease) Leases(_ context.Context) ([]*lease.Lease, error) {
	return []*lease.Lease{
		{ID: 1, Keys: [][]byte{owner(1), []byte("p/k1")}},
		{ID: 2, Keys: [][]byte{[]byte(leaseOwnerKeyPrefix + "other/2"), []byte("other/k2")}},
		{ID: 3, Keys: nil}, // granted outside any namespaced view: nobody's
	}, nil
}

func TestNamespacedLease_TimeToLiveStripsAndFiltersKeys(t *testing.T) {
	w := newNamespacedLeaseService(recLease{}, nil, "p/")
	l, err := w.TimeToLive(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Keys) != 1 || string(l.Keys[0]) != "k1" {
		t.Errorf("TimeToLive keys = %v, want [k1] (the ownership record and other namespaces' keys hidden)", l.Keys)
	}
}

// Leases lists only the leases this namespace granted. It used to return the
// global lease set, which let every tenant list -- and, by ID, revoke and
// keep alive -- every other tenant's leases.
func TestNamespacedLease_LeasesListsOnlyOwnedLeases(t *testing.T) {
	w := newNamespacedLeaseService(recLease{}, nil, "p/")
	ls, err := w.Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 || ls[0].ID != 1 {
		t.Fatalf("Leases = %+v, want only lease 1", ls)
	}
	if len(ls[0].Keys) != 1 || string(ls[0].Keys[0]) != "k1" {
		t.Errorf("lease 1 keys = %v, want [k1]", ls[0].Keys)
	}
}

// ---------------------------------------------------------------------------
// Batch wrapper
// ---------------------------------------------------------------------------

type recBatch struct {
	service.BatchServicer
	lastKeys         []string
	lastImportPrefix string

	// lastImportSessionCall records the sessionID the inner service actually
	// received for whichever of ImportAppend/ImportCommit/ImportAbort was
	// last called, or "" if none has been. The cross-database tests below
	// assert against this to prove checkSessionNamespace refused BEFORE
	// forwarding -- a call the inner service never sees -- rather than only
	// checking the wrapper's returned error.
	lastImportSessionCall string
}

func (m *recBatch) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	for _, op := range ops {
		m.lastKeys = append(m.lastKeys, string(op.Key))
	}
	return []service.OpResponse{{Kvs: []*kv.KeyValue{{Key: []byte("p/k")}}}}, nil
}

func (m *recBatch) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	m.lastImportPrefix = opts.KeyPrefix
	return "", nil
}

func (m *recBatch) ImportAppend(_ context.Context, sessionID string, _ uint64, _ []byte) error {
	m.lastImportSessionCall = sessionID
	return nil
}

func (m *recBatch) ImportCommit(_ context.Context, sessionID string) (*service.ImportResult, error) {
	m.lastImportSessionCall = sessionID
	return nil, nil
}

func (m *recBatch) ImportAbort(_ context.Context, sessionID string) error {
	m.lastImportSessionCall = sessionID
	return nil
}

// encodeTestSessionID mirrors pkg/service's own (unexported) encodeSessionID
// exactly -- base64.RawURLEncoding of the internal id -- so these tests can
// construct an external session ID for a chosen internal namespace prefix
// without reaching into pkg/service's internals.
func encodeTestSessionID(internalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(internalID))
}

func TestNamespacedBatch_PrefixesAndStrips(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "p/")
	resp, err := w.Execute(context.Background(), []service.Op{{Key: []byte("k")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.lastKeys) != 1 || rec.lastKeys[0] != "p/k" {
		t.Errorf("inner op keys = %v, want [p/k]", rec.lastKeys)
	}
	if string(resp[0].Kvs[0].Key) != "k" {
		t.Errorf("returned kv key = %q, want k", resp[0].Kvs[0].Key)
	}
}

func TestNamespacedBatch_ImportSetsPrefix(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "p/")
	if _, err := w.ImportBegin(context.Background(), service.ImportOptions{}); err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if rec.lastImportPrefix != "p/" {
		t.Errorf("inner import KeyPrefix = %q, want p/", rec.lastImportPrefix)
	}
}

func TestNamespacedBatch_ImportComposesPrefix(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "t/db/")
	if _, err := w.ImportBegin(context.Background(), service.ImportOptions{KeyPrefix: "extra/"}); err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if rec.lastImportPrefix != "t/db/extra/" {
		t.Errorf("inner import KeyPrefix = %q, want t/db/extra/", rec.lastImportPrefix)
	}
}

// TestNamespacedBatch_ImportAppendAllowsSameDatabaseSession is the control
// for the cross-database refusal tests below: a session ID whose internal
// form DOES start with this wrapper's own namespace prefix is forwarded.
func TestNamespacedBatch_ImportAppendAllowsSameDatabaseSession(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "acme/db1/")
	sessionID := encodeTestSessionID("acme/db1/sess1")

	if err := w.ImportAppend(context.Background(), sessionID, 0, []byte("x")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	if rec.lastImportSessionCall != sessionID {
		t.Errorf("inner ImportAppend was not called with the same-database session")
	}
}

// TestNamespacedBatch_ImportCommitAndAbortAllowSameDatabaseSession is the
// control for the cross-database refusals of ImportCommit and ImportAbort: a
// session minted in this wrapper's own namespace reaches the inner service
// unchanged, so the namespace check refuses only what it should.
func TestNamespacedBatch_ImportCommitAndAbortAllowSameDatabaseSession(t *testing.T) {
	sessionID := encodeTestSessionID("acme/db1/sess1")

	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "acme/db1/")
	if _, err := w.ImportCommit(context.Background(), sessionID); err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if rec.lastImportSessionCall != sessionID {
		t.Errorf("inner ImportCommit got session %q, want %q", rec.lastImportSessionCall, sessionID)
	}

	rec = &recBatch{}
	w = newNamespacedBatchService(rec, "acme/db1/")
	if err := w.ImportAbort(context.Background(), sessionID); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	if rec.lastImportSessionCall != sessionID {
		t.Errorf("inner ImportAbort got session %q, want %q", rec.lastImportSessionCall, sessionID)
	}
}

// TestNamespacedBatch_ImportAppendRefusesCrossDatabaseSession proves a
// session ID minted through one namespaced database wrapper's ImportBegin
// (acme/db1/) is refused by ImportAppend on a DIFFERENT namespaced database
// wrapper of the same tenant (acme/db2/) -- go-qrdb#576's security review:
// checkSessionOwner inside the inner service checks only (tenant, identity),
// never which database ImportBegin was called through, so without
// checkSessionNamespace a session begun in one database could be appended
// to, committed, or aborted through another.
func TestNamespacedBatch_ImportAppendRefusesCrossDatabaseSession(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "acme/db2/")
	sessionID := encodeTestSessionID("acme/db1/sess1")

	err := w.ImportAppend(context.Background(), sessionID, 0, []byte("x"))
	if !errors.As(err, new(*service.ImportSessionNotFoundError)) {
		t.Fatalf("error = %T (%v), want *service.ImportSessionNotFoundError", err, err)
	}
	if rec.lastImportSessionCall != "" {
		t.Errorf("inner ImportAppend was called with a cross-database session; it must be refused before forwarding")
	}
}

// TestNamespacedBatch_ImportCommitRefusesCrossDatabaseSession is
// TestNamespacedBatch_ImportAppendRefusesCrossDatabaseSession for
// ImportCommit.
func TestNamespacedBatch_ImportCommitRefusesCrossDatabaseSession(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "acme/db2/")
	sessionID := encodeTestSessionID("acme/db1/sess1")

	_, err := w.ImportCommit(context.Background(), sessionID)
	if !errors.As(err, new(*service.ImportSessionNotFoundError)) {
		t.Fatalf("error = %T (%v), want *service.ImportSessionNotFoundError", err, err)
	}
	if rec.lastImportSessionCall != "" {
		t.Errorf("inner ImportCommit was called with a cross-database session; it must be refused before forwarding")
	}
}

// TestNamespacedBatch_ImportAbortRefusesCrossDatabaseSession is
// TestNamespacedBatch_ImportAppendRefusesCrossDatabaseSession for
// ImportAbort.
func TestNamespacedBatch_ImportAbortRefusesCrossDatabaseSession(t *testing.T) {
	rec := &recBatch{}
	w := newNamespacedBatchService(rec, "acme/db2/")
	sessionID := encodeTestSessionID("acme/db1/sess1")

	err := w.ImportAbort(context.Background(), sessionID)
	if !errors.As(err, new(*service.ImportSessionNotFoundError)) {
		t.Fatalf("error = %T (%v), want *service.ImportSessionNotFoundError", err, err)
	}
	if rec.lastImportSessionCall != "" {
		t.Errorf("inner ImportAbort was called with a cross-database session; it must be refused before forwarding")
	}
}

// ---------------------------------------------------------------------------
// Transaction wrapper
// ---------------------------------------------------------------------------

type recTxn struct {
	service.TransactionServicer
	cmpKey, opKey string
}

func (m *recTxn) Execute(_ context.Context, cmp []service.Compare, ok, _ []service.Op) (*service.TransactionResponse, error) {
	if len(cmp) > 0 {
		m.cmpKey = string(cmp[0].Key)
	}
	if len(ok) > 0 {
		m.opKey = string(ok[0].Key)
	}
	return &service.TransactionResponse{Responses: []service.OpResponse{{Kvs: []*kv.KeyValue{{Key: []byte("p/k")}}}}}, nil
}

func TestNamespacedTransaction_PrefixesAndStrips(t *testing.T) {
	rec := &recTxn{}
	w := newNamespacedTransactionService(rec, "p/")
	resp, err := w.Execute(context.Background(),
		[]service.Compare{{Key: []byte("c")}},
		[]service.Op{{Key: []byte("o")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.cmpKey != "p/c" || rec.opKey != "p/o" {
		t.Errorf("inner keys cmp=%q op=%q", rec.cmpKey, rec.opKey)
	}
	if string(resp.Responses[0].Kvs[0].Key) != "k" {
		t.Errorf("returned kv key = %q, want k", resp.Responses[0].Kvs[0].Key)
	}
}

func TestNamespacedTransaction_EntityIDPrefixed(t *testing.T) {
	var gotID string
	rec := &recTxnEntity{fn: func(id string) { gotID = id }}
	w := newNamespacedTransactionService(rec, "p/")
	_, err := w.ExecuteEntity(context.Background(), &service.TransactionEntityRequest{
		Success: []service.TransactionEntityOp{{ID: "bob"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotID != "p/bob" {
		t.Errorf("entity op ID = %q, want p/bob", gotID)
	}
}

type recTxnEntity struct {
	service.TransactionServicer
	fn func(string)
}

func (m *recTxnEntity) ExecuteEntity(_ context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	if len(req.Success) > 0 {
		m.fn(req.Success[0].ID)
	}
	return &service.TransactionEntityResponse{}, nil
}

// ---------------------------------------------------------------------------
// Watch wrapper
// ---------------------------------------------------------------------------

type recWatch struct {
	service.WatchServicer
	lastKey string
	w       *fakeWatcher
}

func (m *recWatch) Watch(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
	m.lastKey = key
	m.w = newFakeWatcher()
	return m.w, nil
}

type fakeWatcher struct {
	ch chan watch.WatchResponse
}

func newFakeWatcher() *fakeWatcher                        { return &fakeWatcher{ch: make(chan watch.WatchResponse, 1)} }
func (f *fakeWatcher) Events() <-chan watch.WatchResponse { return f.ch }
func (f *fakeWatcher) WatchID() int64                     { return 1 }
func (f *fakeWatcher) Close() error                       { close(f.ch); return nil }
func (f *fakeWatcher) Err() error                         { return nil }

func TestNamespacedWatch_PrefixesKeyAndStripsEvents(t *testing.T) {
	rec := &recWatch{}
	w := newNamespacedWatchService(rec, "p/")
	wtr, err := w.Watch(context.Background(), "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastKey != "p/k" {
		t.Errorf("inner watched key = %q, want p/k", rec.lastKey)
	}
	rec.w.ch <- watch.WatchResponse{Events: []*watch.Event{{Kv: &kv.KeyValue{Key: []byte("p/k")}}}}
	resp := <-wtr.Events()
	if string(resp.Events[0].Kv.Key) != "k" {
		t.Errorf("delivered key = %q, want k", resp.Events[0].Kv.Key)
	}
	_ = wtr.Close()
}

func TestNamespacedWatch_WatcherForwardsID(t *testing.T) {
	rec := &recWatch{}
	w := newNamespacedWatchService(rec, "p/")
	wtr, _ := w.Watch(context.Background(), "k", nil)
	if wtr.WatchID() != 1 {
		t.Errorf("WatchID = %d, want 1", wtr.WatchID())
	}
	_ = wtr.Close()
}

// ---------------------------------------------------------------------------
// Vector wrapper
// ---------------------------------------------------------------------------

type recVector struct {
	service.VectorServicer
	lastNS string
}

func (m *recVector) CreateNamespace(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	m.lastNS = req.Namespace
	return &service.CreateNamespaceResponse{Namespace: req.Namespace}, nil
}
func (m *recVector) ListNamespaces(_ context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	m.lastNS = req.Prefix
	return &service.ListNamespacesResponse{Namespaces: []*service.NamespaceInfo{{Namespace: req.Prefix + "docs"}}}, nil
}

func TestNamespacedVector_CreateNamespacePrefixesAndStrips(t *testing.T) {
	rec := &recVector{}
	w := newNamespacedVectorService(rec, "acme/reports/")
	resp, err := w.CreateNamespace(context.Background(), &service.CreateNamespaceRequest{Namespace: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastNS != "acme/reports/docs" {
		t.Errorf("inner namespace = %q", rec.lastNS)
	}
	if resp.Namespace != "docs" {
		t.Errorf("returned namespace = %q, want docs", resp.Namespace)
	}
}

func TestNamespacedVector_ListNamespacesStrips(t *testing.T) {
	rec := &recVector{}
	w := newNamespacedVectorService(rec, "p/")
	resp, err := w.ListNamespaces(context.Background(), &service.ListNamespacesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastNS != "p/" {
		t.Errorf("inner list prefix = %q, want p/", rec.lastNS)
	}
	if resp.Namespaces[0].Namespace != "docs" {
		t.Errorf("listed namespace = %q, want docs", resp.Namespaces[0].Namespace)
	}
}

// TestPrefixBytes_AbsentKeyStaysNil pins the single-key-op contract: an
// absent key — nil OR empty-but-non-nil (what wire decoders such as REST's
// jsonToOps produce for an unset field) — must stay nil. Prefixing an empty
// RangeEnd fabricated a bound equal to the bare namespace prefix, silently
// turning a batch/txn single-key GET into an inverted (empty) range.
func TestPrefixBytes_AbsentKeyStaysNil(t *testing.T) {
	if got := prefixBytes(nil, "t/db/"); got != nil {
		t.Fatalf("prefixBytes(nil) = %q, want nil", got)
	}
	if got := prefixBytes([]byte(""), "t/db/"); got != nil {
		t.Fatalf("prefixBytes(empty) = %q, want nil", got)
	}
	if got := prefixBytes([]byte("k"), "t/db/"); string(got) != "t/db/k" {
		t.Fatalf("prefixBytes(k) = %q, want t/db/k", got)
	}
}

// TestNamespacedBatch_GetWithEmptyRangeEnd pins the end-to-end shape of the
// batch fix: a GET op whose RangeEnd is empty-but-non-nil must reach the
// inner BatchServicer as a single-key op (nil RangeEnd), not as a range.
func TestNamespacedBatch_GetWithEmptyRangeEnd(t *testing.T) {
	svc := newNamespacedBatchService(nil, "t/db/")
	ops := svc.prefixOps([]service.Op{{
		Type:     service.OpGet,
		Key:      []byte("k"),
		RangeEnd: []byte(""),
	}})
	if string(ops[0].Key) != "t/db/k" {
		t.Fatalf("prefixed key = %q, want t/db/k", ops[0].Key)
	}
	if ops[0].RangeEnd != nil {
		t.Fatalf("prefixed RangeEnd = %q, want nil (single-key op)", ops[0].RangeEnd)
	}
}

// TestPrefixKeyBytes_EmptyKeyConfinedToBarePrefix pins the M6 fix: an absent
// (nil or empty) single-key Op.Key must map to the bare namespace prefix, not
// to nil. prefixBytes (used for the not-a-key RangeEnd) leaves an empty key
// nil, and a nil single-key Op.Key is unconfined — it addresses the bare
// store root, where two tenants'/databases' empty-key ops would collide.
func TestPrefixKeyBytes_EmptyKeyConfinedToBarePrefix(t *testing.T) {
	if got := prefixKeyBytes(nil, "t/db/"); string(got) != "t/db/" {
		t.Fatalf("prefixKeyBytes(nil) = %q, want bare prefix %q", got, "t/db/")
	}
	if got := prefixKeyBytes([]byte(""), "t/db/"); string(got) != "t/db/" {
		t.Fatalf("prefixKeyBytes(empty) = %q, want bare prefix %q", got, "t/db/")
	}
	if got := prefixKeyBytes([]byte("k"), "t/db/"); string(got) != "t/db/k" {
		t.Fatalf("prefixKeyBytes(k) = %q, want t/db/k", got)
	}
}

// TestNamespacedBatch_PutWithEmptyKeyConfinedToNamespace pins the M6 fix at
// the batch service: a single-key op (e.g. a Put) with an empty key must be
// confined to the bare "<tenant>/<db>/" namespace prefix rather than
// forwarded as a nil key, which addresses the bare store root and collides
// across tenants/databases. The (nil) RangeEnd stays nil, unaffected.
func TestNamespacedBatch_PutWithEmptyKeyConfinedToNamespace(t *testing.T) {
	svc := newNamespacedBatchService(nil, "t/db/")
	ops := svc.prefixOps([]service.Op{{
		Type: service.OpPut,
		Key:  []byte(""),
	}})
	if string(ops[0].Key) != "t/db/" {
		t.Fatalf("prefixed key = %q, want bare namespace prefix %q (confined, not escaped to store root)", ops[0].Key, "t/db/")
	}
	if ops[0].RangeEnd != nil {
		t.Fatalf("RangeEnd = %q, want nil", ops[0].RangeEnd)
	}
}

// TestNamespacedTransaction_GetWithEmptyRangeEnd mirrors
// TestNamespacedBatch_GetWithEmptyRangeEnd for the transaction service: a
// normal single-key GET's empty-but-non-nil RangeEnd must stay nil so it is
// not misread as an inverted (empty) range.
func TestNamespacedTransaction_GetWithEmptyRangeEnd(t *testing.T) {
	svc := newNamespacedTransactionService(nil, "t/db/")
	ops := svc.prefixOps([]service.Op{{
		Type:     service.OpGet,
		Key:      []byte("k"),
		RangeEnd: []byte(""),
	}})
	if string(ops[0].Key) != "t/db/k" {
		t.Fatalf("prefixed key = %q, want t/db/k", ops[0].Key)
	}
	if ops[0].RangeEnd != nil {
		t.Fatalf("prefixed RangeEnd = %q, want nil (single-key op)", ops[0].RangeEnd)
	}
}

// TestNamespacedTransaction_PutWithEmptyKeyConfinedToNamespace mirrors
// TestNamespacedBatch_PutWithEmptyKeyConfinedToNamespace for the transaction
// service's single-key ops (success/failure branches).
func TestNamespacedTransaction_PutWithEmptyKeyConfinedToNamespace(t *testing.T) {
	svc := newNamespacedTransactionService(nil, "t/db/")
	ops := svc.prefixOps([]service.Op{{
		Type: service.OpPut,
		Key:  []byte(""),
	}})
	if string(ops[0].Key) != "t/db/" {
		t.Fatalf("prefixed key = %q, want bare namespace prefix %q (confined, not escaped to store root)", ops[0].Key, "t/db/")
	}
	if ops[0].RangeEnd != nil {
		t.Fatalf("RangeEnd = %q, want nil", ops[0].RangeEnd)
	}
}

// TestNamespacedTransaction_EmptyCompareKeyRejected pins the L4 fix: an empty
// transaction Compare key must be rejected with a typed error rather than
// silently namespaced into a comparison against the literal bare-prefix key
// "<tenant>/<db>/", which would change a malformed request's meaning instead
// of preserving the state machine's own rejection of it.
func TestNamespacedTransaction_EmptyCompareKeyRejected(t *testing.T) {
	rec := &recTxn{}
	w := newNamespacedTransactionService(rec, "t/db/")

	_, err := w.Execute(context.Background(), []service.Compare{{Key: []byte("")}}, nil, nil)
	if !errors.Is(err, ErrEmptyCompareKey) {
		t.Fatalf("Execute err = %v, want ErrEmptyCompareKey", err)
	}
	if rec.cmpKey != "" {
		t.Fatalf("inner Execute must not be called on a rejected compare, got cmpKey=%q", rec.cmpKey)
	}

	_, err = w.ExecuteWithMode(context.Background(), []service.Compare{{Key: nil}}, nil, nil, writemode.Sync)
	if !errors.Is(err, ErrEmptyCompareKey) {
		t.Fatalf("ExecuteWithMode err = %v, want ErrEmptyCompareKey", err)
	}
}

// TestNamespacedTransaction_NonEmptyCompareKeyStillPrefixed guards against a
// regression where the L4 fix over-corrects and stops prefixing well-formed
// compare keys.
func TestNamespacedTransaction_NonEmptyCompareKeyStillPrefixed(t *testing.T) {
	rec := &recTxn{}
	w := newNamespacedTransactionService(rec, "t/db/")
	if _, err := w.Execute(context.Background(), []service.Compare{{Key: []byte("c")}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if rec.cmpKey != "t/db/c" {
		t.Fatalf("inner compare key = %q, want t/db/c", rec.cmpKey)
	}
}
