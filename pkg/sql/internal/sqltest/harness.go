// Copyright 2026 Jeremy Hahn
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

// Package sqltest provides a minimal, in-memory service.TableServicer /
// service.SequenceServicer / service.Registry test harness shared by the
// pkg/sql/query, pkg/sql/mysql, and pkg/sql/pgsql test suites. All three
// need a REAL relational table catalog -- not a hand-wired stand-in for
// TableServicer -- to prove that a service.Registry threaded into their
// respective providers actually resolves CREATE TABLE-produced tables,
// rather than merely accepting the option.
//
// It is internal to pkg/sql so it is only importable from packages rooted
// there, matching the boundary of the code it exists to test.
package sqltest

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// MemStore: a single-map, in-memory implementation of service.KVServicer
// plus the narrow MVCC-revision surface (GetRevision, Range) and
// TransactionServicer.Execute that service.TableService and
// service.SequenceService need internally.
//
// Every embedded interface is nil, mirroring the "keeps the mock honest"
// pattern used throughout this codebase's SQL test suites: any method this
// file does not explicitly implement panics if a test path ever reaches it,
// rather than silently returning a zero value.
// ---------------------------------------------------------------------------

type memRecord struct {
	value          []byte
	modRevision    int64
	createRevision int64
	version        int64
}

// MemStore implements service.KVServicer, the GetRevision/Range slice of
// service.MVCCKVServicer, and service.TransactionServicer.Execute (via
// Txn), all backed by a single mutex-guarded map.
type MemStore struct {
	service.MVCCKVServicer // nil; see the package doc comment

	mu   sync.Mutex
	data map[string]*memRecord
	rev  int64
}

// NewMemStore creates an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string]*memRecord)}
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Put stores value under key.
func (s *MemStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putLocked(key, value)
	return nil
}

func (s *MemStore) putLocked(key string, value []byte) {
	s.rev++
	cr, ver := s.rev, int64(1)
	if existing, had := s.data[key]; had {
		cr, ver = existing.createRevision, existing.version+1
	}
	s.data[key] = &memRecord{value: cloneBytes(value), modRevision: s.rev, createRevision: cr, version: ver}
}

// PutIfAbsent stores value under key only if key does not already exist.
func (s *MemStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.rev++
	s.data[key] = &memRecord{value: cloneBytes(value), modRevision: s.rev, createRevision: s.rev, version: 1}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, under the same lock that makes PutIfAbsent indivisible.
func (s *MemStore) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok || !bytes.Equal(rec.value, expected) {
		return false, nil
	}
	delete(s.data, key)
	return true, nil
}

// Get returns the value stored under key, or *service.KeyNotFoundError.
func (s *MemStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return cloneBytes(rec.value), nil
}

// Delete removes key.
func (s *MemStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// List returns every key with the given prefix, sorted.
func (s *MemStore) List(_ context.Context, prefix string) ([]string, error) {
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

// Scan returns every key/value pair with the given prefix.
func (s *MemStore) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
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

// GetStale is Get without a consistency distinction; this store has no
// replicas to be stale relative to.
func (s *MemStore) GetStale(ctx context.Context, key string) ([]byte, error) { return s.Get(ctx, key) }

// ListStale is List without a consistency distinction.
func (s *MemStore) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return s.List(ctx, prefix)
}

// ScanStale is Scan without a consistency distinction.
func (s *MemStore) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

// GetRevision returns key's value together with its MVCC revision metadata.
func (s *MemStore) GetRevision(_ context.Context, key string) (*service.KeyValue, error) {
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

// Range returns every key/value pair in [start, end), or all keys >= start
// when end is nil.
func (s *MemStore) Range(_ context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ro := service.ApplyRangeOptions(opts)

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

func (s *MemStore) evalCompareLocked(c service.Compare) bool {
	rec, ok := s.data[string(c.Key)]
	var actual int64
	if ok {
		switch c.Target {
		case service.CompareModRevision:
			actual = rec.modRevision
		case service.CompareCreateRevision:
			actual = rec.createRevision
		case service.CompareVersion:
			actual = rec.version
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
	}
	return false
}

func (s *MemStore) applyOpsLocked(ops []service.Op) {
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
func (s *MemStore) txnExecute(compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
func (s *MemStore) batchExecute(ops []service.Op) ([]service.OpResponse, error) {
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
// Txn / Batch: TransactionServicer / BatchServicer adapters over a shared
// *MemStore.
// ---------------------------------------------------------------------------

// Txn implements service.TransactionServicer.Execute/ExecuteWithMode over a
// shared MemStore. Every other TransactionServicer method is inherited from
// the embedded nil interface and panics if called.
type Txn struct {
	service.TransactionServicer // nil; only Execute/ExecuteWithMode are implemented
	store                       *MemStore
}

// Execute implements service.TransactionServicer.
func (t *Txn) Execute(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	return t.store.txnExecute(compares, successOps, failureOps)
}

// ExecuteWithMode implements service.TransactionServicer.
func (t *Txn) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return t.Execute(ctx, compares, successOps, failureOps)
}

// Batch implements service.BatchServicer.Execute/ExecuteWithMode over a
// shared MemStore. Every other BatchServicer method is inherited from the
// embedded nil interface and panics if called.
type Batch struct {
	service.BatchServicer // nil; only Execute/ExecuteWithMode are implemented
	store                 *MemStore
}

// Execute implements service.BatchServicer.
func (b *Batch) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	return b.store.batchExecute(ops)
}

// ExecuteWithMode implements service.BatchServicer.
func (b *Batch) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return b.Execute(ctx, ops)
}

// ---------------------------------------------------------------------------
// Harness: a real service.TableService/SequenceService wired to a shared
// MemStore, plus a service.Registry serving them for any (tenant, database)
// pair.
// ---------------------------------------------------------------------------

// Harness wires a REAL service.TableService and service.SequenceService to
// a shared MemStore, and exposes a service.Registry (Registry) that serves
// them for any (tenant, database) pair. This is what lets CREATE TABLE-style
// setup in a test run through the genuine catalog compare-and-swap logic,
// and a subsequently-built provider.Provider (via provider.WithRegistry)
// resolve the resulting table for real, rather than against a hand-wired
// stand-in for TableServicer.
type Harness struct {
	Store    *MemStore
	TableSvc service.TableServicer
	SeqSvc   service.SequenceServicer
	PrivSvc  service.PrivilegeServicer
	KVSvc    service.KVServicer
	MVCCSvc  service.MVCCKVServicer
	BatchSvc service.BatchServicer
	Registry service.Registry
}

// NewHarness creates a Harness backed by a fresh, empty MemStore.
func NewHarness() *Harness {
	store := NewMemStore()
	txn := &Txn{store: store}
	seq := service.NewSequenceService(store, txn)
	tableSvc := service.NewTableService(store, store, txn, seq)
	privSvc := service.NewPrivilegeService(store, store, txn)
	batch := &Batch{store: store}

	h := &Harness{
		Store:    store,
		TableSvc: tableSvc,
		SeqSvc:   seq,
		PrivSvc:  privSvc,
		KVSvc:    store,
		MVCCSvc:  store,
		BatchSvc: batch,
	}
	h.Registry = &FakeRegistry{
		TableServiceFn:       func(string, string) (service.TableServicer, error) { return h.TableSvc, nil },
		KVServiceFn:          func(string, string) (service.KVServicer, error) { return h.KVSvc, nil },
		MVCCServiceFn:        func(string, string) (service.MVCCKVServicer, error) { return h.MVCCSvc, nil },
		BatchServiceFn:       func(string, string) (service.BatchServicer, error) { return h.BatchSvc, nil },
		TransactionServiceFn: func(string, string) (service.TransactionServicer, error) { return txn, nil },
		SequenceServiceFn:    func(string, string) (service.SequenceServicer, error) { return h.SeqSvc, nil },
		PrivilegeServiceFn:   func(string, string) (service.PrivilegeServicer, error) { return h.PrivSvc, nil },
	}
	return h
}

// CreateTable creates a table through the real TableService, failing the
// caller-supplied test immediately on error.
func (h *Harness) CreateTable(ctx context.Context, database, name string, columns []service.ColumnDef) *service.TableDef {
	def, err := h.TableSvc.CreateTable(ctx, &service.CreateTableRequest{
		Database: database,
		Name:     name,
		Columns:  columns,
	})
	if err != nil {
		panic("sqltest: CreateTable(" + database + "." + name + "): " + err.Error())
	}
	return def
}

// ---------------------------------------------------------------------------
// FakeRegistry: a service.Registry that delegates configured methods to
// function fields. Every unconfigured method is inherited from the
// embedded nil interface and panics if called, keeping the fake honest
// about what a given test path touches.
// ---------------------------------------------------------------------------

// FakeRegistry implements service.Registry over configurable function
// fields.
type FakeRegistry struct {
	service.Registry // nil; only the configured *Fn fields are implemented

	KVServiceFn          func(tenant, db string) (service.KVServicer, error)
	MVCCServiceFn        func(tenant, db string) (service.MVCCKVServicer, error)
	TableServiceFn       func(tenant, db string) (service.TableServicer, error)
	BatchServiceFn       func(tenant, db string) (service.BatchServicer, error)
	TransactionServiceFn func(tenant, db string) (service.TransactionServicer, error)
	SequenceServiceFn    func(tenant, db string) (service.SequenceServicer, error)
	PrivilegeServiceFn   func(tenant, db string) (service.PrivilegeServicer, error)
}

// KVService implements service.Registry.
func (r *FakeRegistry) KVService(tenant, db string) (service.KVServicer, error) {
	return r.KVServiceFn(tenant, db)
}

// MVCCService implements service.Registry.
func (r *FakeRegistry) MVCCService(tenant, db string) (service.MVCCKVServicer, error) {
	return r.MVCCServiceFn(tenant, db)
}

// TableService implements service.Registry.
func (r *FakeRegistry) TableService(tenant, db string) (service.TableServicer, error) {
	return r.TableServiceFn(tenant, db)
}

// BatchService implements service.Registry.
func (r *FakeRegistry) BatchService(tenant, db string) (service.BatchServicer, error) {
	return r.BatchServiceFn(tenant, db)
}

// TransactionService implements service.Registry.
func (r *FakeRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	return r.TransactionServiceFn(tenant, db)
}

// SequenceService implements service.Registry.
func (r *FakeRegistry) SequenceService(tenant, db string) (service.SequenceServicer, error) {
	return r.SequenceServiceFn(tenant, db)
}

// PrivilegeService implements service.Registry.
func (r *FakeRegistry) PrivilegeService(tenant, db string) (service.PrivilegeServicer, error) {
	return r.PrivilegeServiceFn(tenant, db)
}

// ---------------------------------------------------------------------------
// FakeCollectionService: enough of service.CollectionServicer for
// visibility tests (ListCollections, GetCollectionInfo).
// ---------------------------------------------------------------------------

// FakeCollectionService implements service.CollectionServicer sufficiently
// for provider-visibility tests. Every method beyond ListCollections and
// GetCollectionInfo is inherited from the embedded nil interface and
// panics if called.
type FakeCollectionService struct {
	service.CollectionServicer // nil; only ListCollections/GetCollectionInfo are implemented

	Infos []service.CollectionInfo
}

// ListCollections implements service.CollectionServicer.
func (f *FakeCollectionService) ListCollections(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return &service.ListCollectionsResponse{Collections: f.Infos}, nil
}

// GetCollectionInfo implements service.CollectionServicer.
func (f *FakeCollectionService) GetCollectionInfo(_ context.Context, database, name string) (*service.CollectionInfo, error) {
	for i := range f.Infos {
		if f.Infos[i].Name == name {
			info := f.Infos[i]
			return &info, nil
		}
	}
	return nil, &service.CollectionNotFoundError{Database: database, Collection: name}
}
