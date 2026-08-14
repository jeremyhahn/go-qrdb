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

package driver

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// memStore/registry: a minimal, in-memory service.KVServicer/
// MVCCKVServicer/TransactionServicer/BatchServicer/Registry test double,
// mirroring the pattern pkg/sql/internal/sqltest and pkg/sql/provider's own
// test harnesses use -- duplicated here rather than imported because
// sqltest lives under an internal/ directory rooted at pkg/sql, which
// sdk/go (a separate Go module) cannot import regardless of visibility.
//
// This is what lets these tests drive CREATE TABLE-produced relational
// tables -- and therefore real interactive-transaction buffering (see
// pkg/sql/provider.WriteSet) -- through the embedded driver's actual
// database/sql surface, rather than only the legacy KV table every other
// test in this package exercises (which explicitly refuses to participate
// in an interactive transaction -- see provider.NonTransactionalTableError).
// ---------------------------------------------------------------------------

type memRecord struct {
	value          []byte
	modRevision    int64
	createRevision int64
	version        int64
}

type memStore struct {
	service.MVCCKVServicer // nil; only the methods below are implemented

	mu   sync.Mutex
	data map[string]*memRecord
	rev  int64
}

func newMemStore() *memStore { return &memStore{data: make(map[string]*memRecord)} }

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

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

func (s *memStore) GetStale(ctx context.Context, key string) ([]byte, error) { return s.Get(ctx, key) }
func (s *memStore) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return s.List(ctx, prefix)
}
func (s *memStore) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

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

func (s *memStore) CurrentRevision(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev, nil
}

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

func (s *memStore) evalCompareLocked(c service.Compare) bool {
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

func (s *memStore) txnExecute(compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
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

// memRegistry implements service.Registry by returning the same shared
// instances for any (tenant, database) pair, simulating a single-database
// embedded deployment.
type memRegistry struct {
	service.Registry // nil; only the methods below are implemented

	kvSvc    service.KVServicer
	mvccSvc  service.MVCCKVServicer
	tableSvc service.TableServicer
	txnSvc   service.TransactionServicer
	batchSvc service.BatchServicer
	seqSvc   service.SequenceServicer
	privSvc  service.PrivilegeServicer
}

func (r *memRegistry) KVService(string, string) (service.KVServicer, error) { return r.kvSvc, nil }
func (r *memRegistry) MVCCService(string, string) (service.MVCCKVServicer, error) {
	return r.mvccSvc, nil
}
func (r *memRegistry) TableService(string, string) (service.TableServicer, error) {
	return r.tableSvc, nil
}
func (r *memRegistry) TransactionService(string, string) (service.TransactionServicer, error) {
	return r.txnSvc, nil
}
func (r *memRegistry) BatchService(string, string) (service.BatchServicer, error) {
	return r.batchSvc, nil
}
func (r *memRegistry) SequenceService(string, string) (service.SequenceServicer, error) {
	return r.seqSvc, nil
}
func (r *memRegistry) PrivilegeService(string, string) (service.PrivilegeServicer, error) {
	return r.privSvc, nil
}

// newRelationalRegistry builds a memRegistry backed by a real
// service.TableService/SequenceService over a fresh memStore, ready to
// pass to WithRegistry.
func newRelationalRegistry() *memRegistry {
	store := newMemStore()
	txn := &memTxn{store: store}
	seq := service.NewSequenceService(store, txn)
	tableSvc := service.NewTableService(store, store, txn, seq)
	batch := &memBatch{store: store}

	return &memRegistry{
		kvSvc: store, mvccSvc: store, tableSvc: tableSvc,
		txnSvc: txn, batchSvc: batch, seqSvc: seq,
	}
}

// openRelationalTestDB opens an embedded database wired with a fresh
// relational-table Registry (see newRelationalRegistry), returning both
// the *sql.DB and the registry so a second, independent connector sharing
// the SAME underlying store can be built (for concurrency/conflict tests).
func openRelationalTestDB(t *testing.T, name string) (*sql.DB, *memRegistry) {
	t.Helper()
	reg := newRelationalRegistry()
	cfg := &DSNConfig{Scheme: SchemeEmbedded, Database: name, Backend: "memory", Engine: "kv"}
	connector := NewEmbeddedConnector(cfg, WithRegistry(reg))
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db, reg
}

// openRelationalTestDBWithRegistry opens a second connector sharing an
// already-built registry (and therefore the same underlying store),
// simulating a second concurrent client connection.
func openRelationalTestDBWithRegistry(t *testing.T, name string, reg *memRegistry) *sql.DB {
	t.Helper()
	cfg := &DSNConfig{Scheme: SchemeEmbedded, Database: name, Backend: "memory", Engine: "kv"}
	connector := NewEmbeddedConnector(cfg, WithRegistry(reg))
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEmbeddedRelational_Insert_SelectSeesOwnWrite_Rollback_SelectDoesNotSeeIt(t *testing.T) {
	db, _ := openRelationalTestDB(t, "reltx1")

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO widgets (id, name) VALUES (1, 'gizmo')"); err != nil {
		t.Fatalf("Exec insert: %v", err)
	}

	var name string
	if err := tx.QueryRow("SELECT name FROM widgets WHERE id = 1").Scan(&name); err != nil {
		t.Fatalf("the transaction must see its own uncommitted write: %v", err)
	}
	if name != "gizmo" {
		t.Errorf("name = %q, want gizmo", name)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM widgets WHERE id = 1").Scan(&n); err != nil {
		t.Fatalf("QueryRow count: %v", err)
	}
	if n != 0 {
		t.Errorf("count after rollback = %d, want 0", n)
	}
}

func TestEmbeddedRelational_Commit_Persists(t *testing.T) {
	db, _ := openRelationalTestDB(t, "reltx2")

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO widgets (id, name) VALUES (1, 'gizmo')"); err != nil {
		t.Fatalf("Exec insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var name string
	if err := db.QueryRow("SELECT name FROM widgets WHERE id = 1").Scan(&name); err != nil {
		t.Fatalf("QueryRow after commit: %v", err)
	}
	if name != "gizmo" {
		t.Errorf("name = %q, want gizmo", name)
	}
}

func TestEmbeddedRelational_Savepoint_RollbackTo(t *testing.T) {
	db, _ := openRelationalTestDB(t, "reltx3")

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO widgets (id, name) VALUES (1, 'a')"); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := tx.Exec("SAVEPOINT sp1"); err != nil {
		t.Fatalf("SAVEPOINT: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO widgets (id, name) VALUES (2, 'b')"); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if _, err := tx.Exec("ROLLBACK TO SAVEPOINT sp1"); err != nil {
		t.Fatalf("ROLLBACK TO SAVEPOINT: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM widgets").Scan(&n); err != nil {
		t.Fatalf("QueryRow count: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1 (the insert after the savepoint must have been undone)", n)
	}
}

// TestEmbeddedRelational_ConcurrentUpdate_OneGets1213 verifies that of two
// concurrent transactions racing to update the same row, exactly one
// commits successfully and the other fails with MySQL error 1213
// (ER_LOCK_DEADLOCK) / SQLSTATE 40001 -- the code database/sql clients and
// GORM's retry middleware recognize as "retry the transaction".
func TestEmbeddedRelational_ConcurrentUpdate_OneGets1213(t *testing.T) {
	db, reg := openRelationalTestDB(t, "reltx4")
	other := openRelationalTestDBWithRegistry(t, "reltx4", reg)

	if _, err := db.Exec("CREATE TABLE counters (id BIGINT UNSIGNED PRIMARY KEY, n INT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO counters (id, n) VALUES (1, 0)"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	txA, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin A: %v", err)
	}
	txB, err := other.Begin()
	if err != nil {
		t.Fatalf("Begin B: %v", err)
	}

	if _, err := txA.Exec("UPDATE counters SET n = 1 WHERE id = 1"); err != nil {
		t.Fatalf("update A: %v", err)
	}
	if _, err := txB.Exec("UPDATE counters SET n = 2 WHERE id = 1"); err != nil {
		t.Fatalf("update B: %v", err)
	}

	if err := txA.Commit(); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	err = txB.Commit()
	if err == nil {
		t.Fatal("expected transaction B's commit to fail with a conflict")
	}
	var stateErr *SQLStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error = %v (%T), want *SQLStateError", err, err)
	}
	if stateErr.Number != 1213 {
		t.Errorf("error number = %d, want 1213 (ER_LOCK_DEADLOCK)", stateErr.Number)
	}
	if stateErr.SQLState != "40001" {
		t.Errorf("SQLSTATE = %q, want 40001", stateErr.SQLState)
	}

	var n int
	if err := db.QueryRow("SELECT n FROM counters WHERE id = 1").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1 (only the winning transaction's write must be durable)", n)
	}
}

// TestEmbeddedRelational_LegacyKVTable_InsideBegin_Rejected verifies the
// legacy `kv` table -- which has no WriteSet of its own to buffer into --
// refuses a write attempted inside an explicit transaction rather than
// silently applying it outside that transaction's boundary.
func TestEmbeddedRelational_LegacyKVTable_InsideBegin_Rejected(t *testing.T) {
	db, _ := openRelationalTestDB(t, "reltx5")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = tx.Exec("INSERT INTO `kv` (`key`, value) VALUES ('k1', 'v1')")
	if err == nil {
		t.Fatal("expected an error writing to the legacy kv table inside BEGIN")
	}
	_ = tx.Rollback()
}
