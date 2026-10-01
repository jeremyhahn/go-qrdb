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

package provider

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// memStore
//
// memStore is a single-map, in-memory implementation of service.KVServicer
// and the MVCC-revision-bearing surface (GetRevision, Range) that
// service.TableService and service.SequenceService need internally, plus
// enough of TransactionServicer/BatchServicer (via the memTxn/memBatch
// adapters below) to run the REAL service.TableService/SequenceService
// against it. This is what lets the DDL tests in this package exercise
// genuine end-to-end behavior -- CREATE TABLE through the real catalog
// compare-and-swap logic, rows written and decoded through the real
// catalog.RowCodec, DROP TABLE reclaiming real rows -- rather than a
// hand-wired stand-in for TableServicer.
//
// Every embedded interface is nil, matching the "keeps the mock honest"
// pattern already used by pkg/sql/query's mockKV/mockRegistry: any method
// this file does not explicitly implement panics if a test path ever
// reaches it, rather than silently returning a zero value.
// ---------------------------------------------------------------------------

type memRecord struct {
	value          []byte
	modRevision    int64
	createRevision int64
	version        int64
}

type memStore struct {
	service.MVCCKVServicer // nil; see the type doc comment

	mu   sync.Mutex
	data map[string]*memRecord
	rev  int64

	// rejectTxn, when non-nil, makes every transaction whose success ops it
	// matches lose its compare-and-swap without applying anything -- the
	// way a test forces one specific catalog transaction to fail.
	rejectTxn func(ops []service.Op) bool

	// onCountOnly, when non-nil, is called on every count-only Range,
	// whatever its consistency -- the read a table's rows are counted
	// with -- so a test can count them.
	onCountOnly func()
}

func newMemStore() *memStore {
	return &memStore{data: make(map[string]*memRecord)}
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// ---------------------------------------------------------------------------
// KVServicer
// ---------------------------------------------------------------------------

func (s *memStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putLocked(key, value)
	return nil
}

func (s *memStore) putLocked(key string, value []byte) {
	s.rev++
	cr, ver := s.rev, int64(1)
	if existing, had := s.data[key]; had {
		cr, ver = existing.createRevision, existing.version+1
	}
	s.data[key] = &memRecord{value: cloneBytes(value), modRevision: s.rev, createRevision: cr, version: ver}
}

func (s *memStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.rev++
	s.data[key] = &memRecord{value: cloneBytes(value), modRevision: s.rev, createRevision: s.rev, version: 1}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (s *memStore) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current.value, expected) {
		return false, nil
	}
	delete(s.data, key)
	return true, nil
}

// CompareAndSwap is the edit half of the same guarded set: it replaces the
// value only when the stored one still equals expected, and does NOT create an
// absent key. Implemented for real because this store is asserted to
// kvstore.AtomicKVStore at RUN time — a stub would make the assertion succeed
// while the behaviour it promises is absent (go-qrdb#56).
func (s *memStore) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data[key]
	if !ok || !bytes.Equal(current.value, expected) {
		return false, nil
	}
	valueCopy := make([]byte, len(next))
	copy(valueCopy, next)
	current.value = valueCopy
	return true, nil
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return cloneBytes(rec.value), nil
}

func (s *memStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *memStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *memStore) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte)
	for k, rec := range s.data {
		if strings.HasPrefix(k, prefix) {
			out[k] = cloneBytes(rec.value)
		}
	}
	return out, nil
}

func (s *memStore) GetStale(ctx context.Context, key string) ([]byte, error) {
	return s.Get(ctx, key)
}

func (s *memStore) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return s.List(ctx, prefix)
}

func (s *memStore) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

// ---------------------------------------------------------------------------
// MVCCKVServicer (partial: GetRevision, Range and RangeWithConsistency --
// all TableService/SequenceService and RelationalTable.RowCount need)
// ---------------------------------------------------------------------------

func (s *memStore) GetRevision(_ context.Context, key string) (*service.KeyValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &service.KeyValue{
		Key: []byte(key), Value: cloneBytes(rec.value),
		ModRevision: rec.modRevision, CreateRevision: rec.createRevision, Version: rec.version,
	}, nil
}

// CurrentRevision returns the store's latest committed revision, backing
// WriteSet.bind's snapshot pin.
func (s *memStore) CurrentRevision(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev, nil
}

// GetAtRevision approximates a historical read for a store that keeps only
// the latest value per key: a key that did not exist yet at revision (its
// createRevision is later) is reported not found, exactly as a true
// multi-version store would; a key that existed at or before revision but
// has since been further modified returns its CURRENT value rather than
// its true value as of revision, since no older version is retained. This
// approximation is sufficient for this package's transaction tests, which
// capture a commit-time compare precondition once, at first touch, rather
// than depending on repeated historically-accurate reads of the same key.
func (s *memStore) GetAtRevision(_ context.Context, key string, revision int64) (*service.KeyValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok || rec.createRevision > revision {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &service.KeyValue{
		Key: []byte(key), Value: cloneBytes(rec.value),
		ModRevision: rec.modRevision, CreateRevision: rec.createRevision, Version: rec.version,
	}, nil
}

func (s *memStore) Range(_ context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ro := service.ApplyRangeOptions(opts)
	if ro.CountOnly && s.onCountOnly != nil {
		s.onCountOnly()
	}

	startS := string(start)
	var endS string
	unbounded := end == nil
	if !unbounded {
		endS = string(end)
	}

	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var kvs []*service.KeyValue
	var count int64
	for _, k := range keys {
		if k < startS {
			continue
		}
		if !unbounded && k >= endS {
			continue
		}
		count++
		if ro.CountOnly {
			continue
		}
		if ro.Limit > 0 && int64(len(kvs)) >= ro.Limit {
			continue
		}
		rec := s.data[k]
		kvs = append(kvs, &service.KeyValue{
			Key: []byte(k), Value: cloneBytes(rec.value),
			ModRevision: rec.modRevision, CreateRevision: rec.createRevision, Version: rec.version,
		})
	}
	return kvs, count, s.rev, nil
}

// RangeWithConsistency is Range: memStore is one in-memory replica, so a
// local (stale) read and a linearizable one observe the same state, exactly
// as on a single-node store.
func (s *memStore) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.Range(ctx, start, end, opts...)
}

// ---------------------------------------------------------------------------
// Transaction/Batch execution shared by memTxn and memBatch
// ---------------------------------------------------------------------------

func (s *memStore) evalCompareLocked(c service.Compare) bool {
	rec, ok := s.data[string(c.Key)]
	var actual int64
	var actualValue []byte
	if ok {
		switch c.Target {
		case service.CompareModRevision:
			actual = rec.modRevision
		case service.CompareCreateRevision:
			actual = rec.createRevision
		case service.CompareVersion:
			actual = rec.version
		case service.CompareValue:
			actualValue = rec.value
		}
	}
	if c.Result != service.CompareEqual {
		return false
	}
	switch c.Target {
	case service.CompareModRevision:
		return actual == c.ModRevision
	case service.CompareCreateRevision:
		return actual == c.CreateRevision
	case service.CompareVersion:
		return actual == c.Version
	case service.CompareValue:
		// An absent key's value reads as nil (bytes.Equal(nil, []byte{}) is
		// true), matching pkg/mvcc.Evaluate/pebbleEvaluateCompare -- see
		// RelationalTable.generationCompare's doc comment for why this
		// distinction matters for go-qrdb#547's generation fencing.
		return bytes.Equal(actualValue, c.Value)
	}
	return false
}

func (s *memStore) applyOpsLocked(ops []service.Op) {
	s.rev++
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case service.OpPut:
			cr, ver := s.rev, int64(1)
			if existing, had := s.data[key]; had {
				cr, ver = existing.createRevision, existing.version+1
			}
			s.data[key] = &memRecord{value: cloneBytes(op.Value), modRevision: s.rev, createRevision: cr, version: ver}
		case service.OpDelete:
			delete(s.data, key)
		}
	}
}

// txnExecute evaluates compares and applies successOps or failureOps,
// mirroring service.TransactionServicer.Execute's atomicity contract.
func (s *memStore) txnExecute(compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rejectTxn != nil && s.rejectTxn(successOps) {
		return &service.TransactionResponse{Succeeded: false}, nil
	}
	ok := true
	for _, c := range compares {
		if !s.evalCompareLocked(c) {
			ok = false
			break
		}
	}
	ops := successOps
	if !ok {
		ops = failureOps
	}
	s.applyOpsLocked(ops)
	return &service.TransactionResponse{Succeeded: ok, Revision: s.rev}, nil
}

// batchExecute applies ops unconditionally within a single revision,
// mirroring service.BatchServicer.Execute's atomicity contract.
func (s *memStore) batchExecute(ops []service.Op) ([]service.OpResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyOpsLocked(ops)
	resp := make([]service.OpResponse, len(ops))
	for i := range ops {
		resp[i] = service.OpResponse{Succeeded: true}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// memTxn: service.TransactionServicer backed by a shared *memStore
// ---------------------------------------------------------------------------

type memTxn struct {
	service.TransactionServicer // nil; only Execute/ExecuteWithMode are implemented
	store                       *memStore
}

func (t *memTxn) Execute(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	return t.store.txnExecute(compares, successOps, failureOps)
}

func (t *memTxn) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return t.Execute(ctx, compares, successOps, failureOps)
}

// ---------------------------------------------------------------------------
// memBatch: service.BatchServicer backed by a shared *memStore
// ---------------------------------------------------------------------------

type memBatch struct {
	service.BatchServicer // nil; only Execute/ExecuteWithMode are implemented
	store                 *memStore
}

func (b *memBatch) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	return b.store.batchExecute(ops)
}

func (b *memBatch) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return b.Execute(ctx, ops)
}

// ---------------------------------------------------------------------------
// ddlHarness: a real service.TableService/SequenceService wired to a shared
// memStore, plus a service.Registry serving them for any (tenant, database)
// pair -- what the CREATE/DROP/ALTER TABLE round-trip tests in this package
// build every Database/RelationalTable DDL path against.
// ---------------------------------------------------------------------------

type ddlHarness struct {
	store    *memStore
	tableSvc service.TableServicer
	kvSvc    service.KVServicer
	mvccSvc  service.MVCCKVServicer
	batchSvc service.BatchServicer
	txnSvc   service.TransactionServicer
	seqSvc   service.SequenceServicer
	registry service.Registry
}

func newDDLHarness() *ddlHarness {
	store := newMemStore()
	txn := &memTxn{store: store}
	seq := service.NewSequenceService(store, txn)
	// The harness's one store is also the system store reclaim-publishing
	// DDL writes its index entries to; without WithReclaimIndex, TRUNCATE
	// TABLE and RENAME INDEX refuse outright (go-qrdb#547).
	tableSvc := service.NewTableService(store, store, txn, seq,
		service.WithReclaimIndex(txn, service.StorePlacementShared, "harness"))
	batchSvc := &memBatch{store: store}

	return &ddlHarness{
		store:    store,
		tableSvc: tableSvc,
		kvSvc:    store,
		mvccSvc:  store,
		batchSvc: batchSvc,
		txnSvc:   txn,
		seqSvc:   seq,
		registry: &fakeRelationalRegistry{
			tableSvcFn: func(string, string) (service.TableServicer, error) { return tableSvc, nil },
			kvSvc:      store,
			mvccSvc:    store,
			batchSvc:   batchSvc,
			txnSvc:     txn,
			seqSvc:     seq,
		},
	}
}

// newDDLDatabase returns a *Database wired to h's registry, ready to serve
// Database-level DDL (CreateTable, DropTable, RenameTable, ...) for
// database name dbName.
func (h *ddlHarness) newDDLDatabase(dbName string) *Database {
	return &Database{name: dbName, kvSvc: h.kvSvc, registry: h.registry}
}

// newDDLProvider returns a *Provider serving the single database dbName,
// wired to h's registry, for running real SQL DDL statements through
// pkg/sql/engine end to end.
func (h *ddlHarness) newDDLProvider(dbName string) *Provider {
	return NewProviderWithEngine(dbName, h.kvSvc, "", WithRegistry(h.registry))
}

// ---------------------------------------------------------------------------
// SQL execution helpers, mirroring pkg/sql/query's run() closely enough for
// DDL round-trip tests to drive real CREATE/ALTER/DROP TABLE statements
// through go-mysql-server's analyzer and executor rather than calling
// Database/RelationalTable methods directly.
// ---------------------------------------------------------------------------

// execQuery runs query against dbName through a fresh engine built over
// prov, returning the result schema and materialized rows.
func execQuery(prov *Provider, dbName, query string) (sqle.Schema, []sqle.Row, error) {
	eng := engine.New(engine.Config{Provider: prov})
	defer func() { _ = eng.Close() }()

	sqlCtx := sqle.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return nil, nil, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(sqlCtx)
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			_ = iter.Close(sqlCtx)
			return nil, nil, nerr
		}
		rows = append(rows, row)
	}
	if cerr := iter.Close(sqlCtx); cerr != nil {
		return nil, nil, cerr
	}
	return schema, rows, nil
}

// mustExecQuery runs execQuery and fails the test immediately on error.
func mustExecQuery(t *testing.T, prov *Provider, dbName, query string) (sqle.Schema, []sqle.Row) {
	t.Helper()
	schema, rows, err := execQuery(prov, dbName, query)
	if err != nil {
		t.Fatalf("query %q: unexpected error: %v", query, err)
	}
	return schema, rows
}
