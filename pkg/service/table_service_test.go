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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// fakeCatalogStore: a self-contained in-memory implementation of
// tableKVService, tableMVCCService, sequenceKVService, and
// TransactionServicer, backed by a single map so a write proposed through
// Execute is immediately visible to Get/GetRevision/List and vice versa --
// mirroring how the catalog metadata key, DDL journal key, and epoch key
// all live in the same underlying MVCC keyspace in production.
//
// It evaluates Compare preconditions the way the real MVCC store machine
// does: a compare against a key that has never been written reads that
// key's ModRevision/CreateRevision/Version as 0, which is what makes
// CompareModRevision==0 read as "must still be absent".
// ---------------------------------------------------------------------------

type fakeCatalogRecord struct {
	value          []byte
	modRevision    int64
	createRevision int64
	version        int64
}

type fakeCatalogStore struct {
	mu   sync.Mutex
	data map[string]*fakeCatalogRecord
	rev  int64

	// forceConflicts, when > 0, makes the next N calls to Execute report a
	// lost compare-and-swap (Succeeded: false) without evaluating or
	// applying anything, simulating a concurrent writer winning the race on
	// each of those attempts. It is decremented on every Execute call while
	// positive, whether or not that call would otherwise have succeeded.
	forceConflicts int

	// executeCalls counts every Execute invocation (forced or real) so
	// tests can assert exactly how many attempts a retry loop made.
	executeCalls int

	// One-shot error injectors: each, when non-nil, is returned by the next
	// call to the corresponding method instead of performing any real work,
	// and is then cleared.
	injectGetErr         error
	injectGetRevisionErr error
	injectListErr        error
	injectScanErr        error
	injectPutIfAbsentErr error
	injectExecuteErr     error

	// raceAfterGetRevision, keyed by key, fires once (then deletes itself)
	// immediately after GetRevision returns that key's observed value --
	// simulating a concurrent writer's CAS landing in the window between a
	// caller's read and its own later Execute, which a fake that only
	// serializes at Execute cannot otherwise reproduce.
	raceAfterGetRevision map[string]func(*fakeCatalogStore)

	// injectRangeErr and injectDeleteErr are one-shot, like the injectors
	// above. rangeLimits records the Limit every Range call asked for and
	// rangeReturned the number of keys each returned, so a test can prove a
	// caller never listed an unbounded page (go-qrdb#547's bounded sweep).
	injectRangeErr  error
	injectDeleteErr error
	rangeLimits     []int64
	rangeReturned   []int
	rangeStarts     []string

	// afterGet, keyed by key, fires once (then deletes itself) after Get
	// has read that key -- a concurrent writer landing between a caller's
	// read and whatever it does next.
	afterGet map[string]func(*fakeCatalogStore)

	// failDeleteAfter and failRangeAfter, when positive, count down on each
	// call and fail the call on which they reach zero -- "let n-1 through,
	// fail the nth".
	failDeleteAfter int
	failRangeAfter  int

	// beforeExecute, when non-nil, runs (without the lock held) at the start
	// of every Execute call that carries compares -- i.e. a catalog
	// compare-and-swap rather than an unconditional index publish.
	beforeExecute func()

	// failReads, keyed by key, fails every Get and GetRevision of that key
	// with its error, for as long as the entry is present: a read that
	// cannot run -- the shard between leaders, a timed-out ReadIndex --
	// however often the caller asks.
	failReads map[string]error
}

func newFakeCatalogStore() *fakeCatalogStore {
	return &fakeCatalogStore{data: make(map[string]*fakeCatalogRecord)}
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

func (f *fakeCatalogStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectPutIfAbsentErr != nil {
		err := f.injectPutIfAbsentErr
		f.injectPutIfAbsentErr = nil
		return false, err
	}

	if _, exists := f.data[key]; exists {
		return false, nil
	}
	f.rev++
	f.data[key] = &fakeCatalogRecord{value: cloneBytes(value), modRevision: f.rev, createRevision: f.rev, version: 1}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (f *fakeCatalogStore) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.data[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current.value, expected) {
		return false, nil
	}
	delete(f.data, key)
	return true, nil
}

// CompareAndSwap is the edit half of the same guarded set: it replaces the
// value only when the stored one still equals expected, and does NOT create an
// absent key. Implemented for real because this store is asserted to
// kvstore.AtomicKVStore at RUN time — a stub would make the assertion succeed
// while the behaviour it promises is absent (go-qrdb#56).
func (f *fakeCatalogStore) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.data[key]
	if !ok || !bytes.Equal(current.value, expected) {
		return false, nil
	}
	valueCopy := make([]byte, len(next))
	copy(valueCopy, next)
	current.value = valueCopy
	return true, nil
}

func (f *fakeCatalogStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectGetErr != nil {
		err := f.injectGetErr
		f.injectGetErr = nil
		return nil, err
	}
	if err, ok := f.failReads[key]; ok {
		return nil, err
	}

	rec, ok := f.data[key]
	var value []byte
	if ok {
		value = cloneBytes(rec.value)
	}
	if hook, hooked := f.afterGet[key]; hooked {
		delete(f.afterGet, key)
		f.mu.Unlock()
		hook(f)
		f.mu.Lock()
	}
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return value, nil
}

func (f *fakeCatalogStore) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectListErr != nil {
		err := f.injectListErr
		f.injectListErr = nil
		return nil, err
	}

	var keys []string
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (f *fakeCatalogStore) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectScanErr != nil {
		err := f.injectScanErr
		f.injectScanErr = nil
		return nil, err
	}

	out := make(map[string][]byte)
	for k, rec := range f.data {
		if strings.HasPrefix(k, prefix) {
			out[k] = cloneBytes(rec.value)
		}
	}
	return out, nil
}

func (f *fakeCatalogStore) GetRevision(_ context.Context, key string) (*KeyValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectGetRevisionErr != nil {
		err := f.injectGetRevisionErr
		f.injectGetRevisionErr = nil
		return nil, err
	}
	if err, ok := f.failReads[key]; ok {
		return nil, err
	}

	rec, ok := f.data[key]
	var result *KeyValue
	if !ok {
		result = nil
	} else {
		result = &KeyValue{
			Key:            []byte(key),
			Value:          cloneBytes(rec.value),
			ModRevision:    rec.modRevision,
			CreateRevision: rec.createRevision,
			Version:        rec.version,
		}
	}
	if race, ok := f.raceAfterGetRevision[key]; ok {
		delete(f.raceAfterGetRevision, key)
		race(f)
	}
	if result == nil {
		return nil, &KeyNotFoundError{Key: key}
	}
	return result, nil
}

// Range returns the keys in [start, end) in sorted order, honoring
// WithLimit and WithKeysOnly the way the MVCC store machine does.
func (f *fakeCatalogStore) Range(_ context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var ro RangeOptions
	for _, o := range opts {
		o(&ro)
	}
	f.rangeLimits = append(f.rangeLimits, ro.Limit)
	f.rangeStarts = append(f.rangeStarts, string(start))
	if f.failRangeAfter > 0 {
		f.failRangeAfter--
		if f.failRangeAfter == 0 {
			return nil, 0, 0, errors.New("fakeCatalogStore: injected range failure")
		}
	}
	if f.injectRangeErr != nil {
		err := f.injectRangeErr
		f.injectRangeErr = nil
		return nil, 0, 0, err
	}

	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		if k >= string(start) && (end == nil || k < string(end)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if ro.Limit > 0 && int64(len(keys)) > ro.Limit {
		keys = keys[:ro.Limit]
	}
	out := make([]*KeyValue, 0, len(keys))
	for _, k := range keys {
		rec := f.data[k]
		kv := &KeyValue{Key: []byte(k), ModRevision: rec.modRevision, CreateRevision: rec.createRevision, Version: rec.version}
		if !ro.KeysOnly {
			kv.Value = cloneBytes(rec.value)
		}
		out = append(out, kv)
	}
	f.rangeReturned = append(f.rangeReturned, len(out))
	return out, int64(len(out)), f.rev, nil
}

// Delete removes key, advancing the store revision like any other write.
func (f *fakeCatalogStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDeleteAfter > 0 {
		f.failDeleteAfter--
		if f.failDeleteAfter == 0 {
			return errors.New("fakeCatalogStore: injected delete failure")
		}
	}
	if f.injectDeleteErr != nil {
		err := f.injectDeleteErr
		f.injectDeleteErr = nil
		return err
	}
	f.rev++
	delete(f.data, key)
	return nil
}

// put writes key directly, advancing the store revision -- the fixture
// equivalent of a committed single-key write.
func (f *fakeCatalogStore) put(key string, value []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	cr, ver := f.rev, int64(1)
	if existing, had := f.data[key]; had {
		cr, ver = existing.createRevision, existing.version+1
	}
	f.data[key] = &fakeCatalogRecord{value: cloneBytes(value), modRevision: f.rev, createRevision: cr, version: ver}
}

// has reports whether key is present.
func (f *fakeCatalogStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

// keysWithPrefix returns every present key under prefix, sorted.
func (f *fakeCatalogStore) keysWithPrefix(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeCatalogStore) Execute(_ context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	if hook := f.beforeExecute; hook != nil && len(compares) > 0 {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.executeCalls++

	if f.injectExecuteErr != nil {
		err := f.injectExecuteErr
		f.injectExecuteErr = nil
		return nil, err
	}

	if f.forceConflicts > 0 {
		f.forceConflicts--
		return &TransactionResponse{Succeeded: false}, nil
	}

	ok := true
	for _, c := range compares {
		if !f.evalCompare(c) {
			ok = false
			break
		}
	}

	ops := successOps
	if !ok {
		ops = failureOps
	}

	f.rev++
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case OpPut:
			cr := f.rev
			ver := int64(1)
			if existing, had := f.data[key]; had {
				cr = existing.createRevision
				ver = existing.version + 1
			}
			f.data[key] = &fakeCatalogRecord{value: cloneBytes(op.Value), modRevision: f.rev, createRevision: cr, version: ver}
		case OpDelete:
			delete(f.data, key)
		}
	}

	return &TransactionResponse{Succeeded: ok, Revision: f.rev}, nil
}

func (f *fakeCatalogStore) evalCompare(c Compare) bool {
	rec, ok := f.data[string(c.Key)]
	var actual int64
	if ok {
		switch c.Target {
		case CompareModRevision:
			actual = rec.modRevision
		case CompareCreateRevision:
			actual = rec.createRevision
		case CompareVersion:
			actual = rec.version
		}
	}
	if c.Result != CompareEqual {
		return false
	}
	switch c.Target {
	case CompareModRevision:
		return actual == c.ModRevision
	case CompareCreateRevision:
		return actual == c.CreateRevision
	case CompareVersion:
		return actual == c.Version
	}
	return false
}

func (f *fakeCatalogStore) ExecuteWithMode(ctx context.Context, compares []Compare, successOps, failureOps []Op, _ writemode.Mode) (*TransactionResponse, error) {
	return f.Execute(ctx, compares, successOps, failureOps)
}

func (f *fakeCatalogStore) ExecuteEntity(_ context.Context, _ *TransactionEntityRequest) (*TransactionEntityResponse, error) {
	return nil, errors.New("fakeCatalogStore: ExecuteEntity not implemented")
}

// executeCallsSnapshot returns the current executeCalls count under lock,
// so tests can measure a delta across a single service call.
func (f *fakeCatalogStore) executeCallsSnapshot() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executeCalls
}

// newTestTableService builds a TableService and the SequenceService it
// delegates TableID allocation to, both backed by the same fakeCatalogStore
// so table creation and its TableID allocation observe a single consistent
// view of state, exactly as they do in production (both are proposed
// through the same Raft group).
// newTestTableService returns a TableService whose reclaim index is
// published into the same fake store as the catalog -- the system store and
// the table's store coincide, as they do for the system tenant's shared
// database. Tests about a separate system store build their own.
func newTestTableService() (*TableService, *fakeCatalogStore) {
	store := newFakeCatalogStore()
	seq := NewSequenceService(store, store)
	return NewTableService(store, store, store, seq,
		WithReclaimIndex(store, StorePlacementShared, "node-1")), store
}

func testColumns() []ColumnDef {
	return []ColumnDef{
		{Name: "id", Type: "BIGINT UNSIGNED", AutoIncrement: true},
		{Name: "email", Type: "VARCHAR(255)"},
		{Name: "created_at", Type: "DATETIME"},
	}
}

// ---------------------------------------------------------------------------
// CreateTable
// ---------------------------------------------------------------------------

func TestTableService_CreateTable_Success(t *testing.T) {
	svc, _ := newTestTableService()

	def, err := svc.CreateTable(context.Background(), &CreateTableRequest{
		Database:   "db",
		Name:       "users",
		Columns:    testColumns(),
		PrimaryKey: []PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	require.NotNil(t, def)
	assert.Equal(t, uint64(1), def.TableID)
	assert.Equal(t, "users", def.Name)
	assert.Equal(t, "db", def.Database)
	assert.Equal(t, uint32(1), def.SchemaVersion)
	assert.Equal(t, "id", def.AutoIncColumn)
	assert.False(t, def.CreatedAt.IsZero())
	assert.Equal(t, def.CreatedAt, def.UpdatedAt)
}

func TestTableService_CreateTable_NoAutoIncrementColumn(t *testing.T) {
	svc, _ := newTestTableService()
	def, err := svc.CreateTable(context.Background(), &CreateTableRequest{
		Database: "db", Name: "t",
		Columns: []ColumnDef{{Name: "id", Type: "BIGINT"}, {Name: "name", Type: "VARCHAR(32)"}},
	})
	require.NoError(t, err)
	assert.Empty(t, def.AutoIncColumn)
}

func TestTableService_CreateTable_TableIDSequenceContiguous(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	first, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "a", Columns: testColumns()})
	require.NoError(t, err)
	second, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "b", Columns: testColumns()})
	require.NoError(t, err)

	assert.Equal(t, uint64(1), first.TableID)
	assert.Equal(t, uint64(2), second.TableID)
}

func TestTableService_CreateTable_AlreadyExists(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	req := &CreateTableRequest{Database: "db", Name: "users", Columns: testColumns()}

	_, err := svc.CreateTable(ctx, req)
	require.NoError(t, err)

	_, err = svc.CreateTable(ctx, req)
	var alreadyExists *TableAlreadyExistsError
	require.ErrorAs(t, err, &alreadyExists)
	assert.Equal(t, "db", alreadyExists.Database)
	assert.Equal(t, "users", alreadyExists.Name)
}

func TestTableService_CreateTable_AlreadyExists_BurnsTableIDGap(t *testing.T) {
	// Documents the "gaps are acceptable, duplicates are not" contract: the
	// TableID sequence is allocated from before the PutIfAbsent that can
	// fail, so a losing create leaves a gap rather than reusing the ID.
	svc, _ := newTestTableService()
	ctx := context.Background()
	req := &CreateTableRequest{Database: "db", Name: "users", Columns: testColumns()}

	first, err := svc.CreateTable(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first.TableID)

	_, err = svc.CreateTable(ctx, req) // fails at PutIfAbsent, burns TableID 2
	require.Error(t, err)

	next, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "other", Columns: testColumns()})
	require.NoError(t, err)
	assert.Equal(t, uint64(3), next.TableID)
}

func TestTableService_CreateTable_Validation(t *testing.T) {
	tests := []struct {
		name    string
		req     *CreateTableRequest
		wantErr func(t *testing.T, err error)
	}{
		{
			name: "empty database",
			req:  &CreateTableRequest{Name: "t", Columns: testColumns()},
			wantErr: func(t *testing.T, err error) {
				var e *TableEmptyDatabaseError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "empty name",
			req:  &CreateTableRequest{Database: "db", Columns: testColumns()},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				require.ErrorAs(t, err, &e)
				assert.Equal(t, "table", e.Kind)
			},
		},
		{
			name: "reserved name prefix",
			req:  &CreateTableRequest{Database: "db", Name: "__reserved", Columns: testColumns()},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				require.ErrorAs(t, err, &e)
			},
		},
		{
			name: "name starts with digit",
			req:  &CreateTableRequest{Database: "db", Name: "1table", Columns: testColumns()},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "name has invalid character",
			req:  &CreateTableRequest{Database: "db", Name: "bad-name", Columns: testColumns()},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "no columns",
			req:  &CreateTableRequest{Database: "db", Name: "t"},
			wantErr: func(t *testing.T, err error) {
				var e *TableNoColumnsError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "invalid column name",
			req: &CreateTableRequest{Database: "db", Name: "t", Columns: []ColumnDef{
				{Name: "bad col", Type: "INT"},
			}},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				require.ErrorAs(t, err, &e)
				assert.Equal(t, "column", e.Kind)
			},
		},
		{
			name: "duplicate column",
			req: &CreateTableRequest{Database: "db", Name: "t", Columns: []ColumnDef{
				{Name: "id", Type: "INT"},
				{Name: "ID", Type: "INT"},
			}},
			wantErr: func(t *testing.T, err error) {
				var e *TableDuplicateColumnError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "unsupported column type",
			req: &CreateTableRequest{Database: "db", Name: "t", Columns: []ColumnDef{
				{Name: "id", Type: "FROBNICATE"},
			}},
			wantErr: func(t *testing.T, err error) {
				var e *TableUnsupportedColumnTypeError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "multiple auto increment columns",
			req: &CreateTableRequest{Database: "db", Name: "t", Columns: []ColumnDef{
				{Name: "id", Type: "BIGINT", AutoIncrement: true},
				{Name: "id2", Type: "BIGINT", AutoIncrement: true},
			}},
			wantErr: func(t *testing.T, err error) {
				var e *TableMultipleAutoIncrementError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "primary key references unknown column",
			req: &CreateTableRequest{
				Database: "db", Name: "t",
				Columns:    []ColumnDef{{Name: "id", Type: "BIGINT"}},
				PrimaryKey: []PKColumn{{Name: "missing"}},
			},
			wantErr: func(t *testing.T, err error) {
				var e *TablePrimaryKeyColumnNotFoundError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "index with no columns",
			req: &CreateTableRequest{
				Database: "db", Name: "t",
				Columns: []ColumnDef{{Name: "id", Type: "BIGINT"}},
				Indexes: []TableIndex{{Name: "idx_x"}},
			},
			wantErr: func(t *testing.T, err error) {
				var e *TableIndexNoColumnsError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "index references unknown column",
			req: &CreateTableRequest{
				Database: "db", Name: "t",
				Columns: []ColumnDef{{Name: "id", Type: "BIGINT"}},
				Indexes: []TableIndex{{Name: "idx_x", Columns: []IndexCol{{Name: "missing"}}}},
			},
			wantErr: func(t *testing.T, err error) {
				var e *TableIndexColumnNotFoundError
				assert.ErrorAs(t, err, &e)
			},
		},
		{
			name: "index with empty name",
			req: &CreateTableRequest{
				Database: "db", Name: "t",
				Columns: []ColumnDef{{Name: "id", Type: "BIGINT"}},
				Indexes: []TableIndex{{Columns: []IndexCol{{Name: "id"}}}},
			},
			wantErr: func(t *testing.T, err error) {
				var e *TableInvalidNameError
				require.ErrorAs(t, err, &e)
				assert.Equal(t, "index", e.Kind)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestTableService()
			_, err := svc.CreateTable(context.Background(), tt.req)
			require.Error(t, err)
			tt.wantErr(t, err)
		})
	}
}

func TestTableService_CreateTable_SequenceAllocationError(t *testing.T) {
	svc, store := newTestTableService()
	store.injectGetRevisionErr = &ReadError{Err: errors.New("boom")}

	_, err := svc.CreateTable(context.Background(), &CreateTableRequest{
		Database: "db", Name: "t", Columns: testColumns(),
	})
	require.Error(t, err)
}

func TestTableService_CreateTable_MarshalMetadataError(t *testing.T) {
	svc, store := newTestTableService()
	store.injectPutIfAbsentErr = errors.New("propose failed")

	_, err := svc.CreateTable(context.Background(), &CreateTableRequest{
		Database: "db", Name: "t", Columns: testColumns(),
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetTable / ListTables
// ---------------------------------------------------------------------------

func TestTableService_GetTable_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.GetTable(context.Background(), "db", "missing")
	var notFound *TableNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

func TestTableService_GetTable_Validation(t *testing.T) {
	svc, _ := newTestTableService()

	_, err := svc.GetTable(context.Background(), "", "t")
	var emptyDB *TableEmptyDatabaseError
	assert.ErrorAs(t, err, &emptyDB)

	_, err = svc.GetTable(context.Background(), "db", "")
	var invalidName *TableInvalidNameError
	assert.ErrorAs(t, err, &invalidName)
}

func TestTableService_GetTable_UnmarshalError(t *testing.T) {
	svc, store := newTestTableService()
	store.data[tableMetadataKey("db", "t")] = &fakeCatalogRecord{value: []byte("not json"), modRevision: 1}

	_, err := svc.GetTable(context.Background(), "db", "t")
	var metaErr *TableMetadataError
	assert.ErrorAs(t, err, &metaErr)
}

// GetReadsThrough asserts GetTable never caches: a direct store mutation
// (as a concurrent writer on another node would perform) is visible on the
// very next call, with no cache to invalidate.
func TestTableService_GetTable_NoCache_ReadsThrough(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, def.TableID, got.TableID)

	// Simulate a concurrent mutation committed by another node/attempt.
	rec := store.data[tableMetadataKey("db", "t")]
	var mutated TableDef
	require.NoError(t, json.Unmarshal(rec.value, &mutated))
	mutated.Comment = "changed out from under us"
	data, err := json.Marshal(&mutated)
	require.NoError(t, err)
	store.mu.Lock()
	rec.value = data
	store.mu.Unlock()

	got2, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, "changed out from under us", got2.Comment)
}

func TestTableService_ListTables(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "a", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "b", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.CreateTable(ctx, &CreateTableRequest{Database: "other", Name: "c", Columns: testColumns()})
	require.NoError(t, err)

	names, err := svc.ListTables(ctx, "db")
	require.NoError(t, err)
	sort.Strings(names)
	assert.Equal(t, []string{"a", "b"}, names)
}

func TestTableService_ListTables_ExcludesEpochAndJournalKeys(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	// Trigger a rewrite-required alter so a DDL journal key exists alongside
	// the metadata and epoch keys under the same scan prefix.
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	names, err := svc.ListTables(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, []string{"t"}, names)
}

func TestTableService_ListTables_EmptyDatabase(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.ListTables(context.Background(), "")
	var e *TableEmptyDatabaseError
	assert.ErrorAs(t, err, &e)
}

func TestTableService_ListTables_ListError(t *testing.T) {
	svc, store := newTestTableService()
	store.injectListErr = errors.New("boom")
	_, err := svc.ListTables(context.Background(), "db")
	var metaErr *TableMetadataError
	assert.ErrorAs(t, err, &metaErr)
}

// ---------------------------------------------------------------------------
// DropTable
// ---------------------------------------------------------------------------

func TestTableService_DropTable_Success(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	epochBefore, _ := store.Get(ctx, tableCatalogEpochKey("db"))

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))

	_, err = svc.GetTable(ctx, "db", "t")
	var notFound *TableNotFoundError
	assert.ErrorAs(t, err, &notFound)

	epochAfter, err := store.Get(ctx, tableCatalogEpochKey("db"))
	require.NoError(t, err)
	assert.NotEqual(t, epochBefore, epochAfter)
}

func TestTableService_DropTable_RemovesJournal(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	_, err = store.Get(ctx, tableDDLJournalKey("db", "t"))
	require.NoError(t, err, "journal should exist before drop")

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))

	_, err = store.Get(ctx, tableDDLJournalKey("db", "t"))
	assert.Error(t, err, "journal should be removed by DropTable")
}

func TestTableService_DropTable_NotFound(t *testing.T) {
	svc, store := newTestTableService()
	err := svc.DropTable(context.Background(), &DropTableRequest{Database: "db", Name: "missing"})
	var notFound *TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, 0, store.executeCalls, "should not attempt a CAS for a table that was never found")
}

func TestTableService_DropTable_Validation(t *testing.T) {
	svc, _ := newTestTableService()
	err := svc.DropTable(context.Background(), &DropTableRequest{Name: "t"})
	var e *TableEmptyDatabaseError
	assert.ErrorAs(t, err, &e)
}

func TestTableService_DropTable_RetryThenSucceed(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	store.forceConflicts = 3 // fewer than catalogCASMaxAttempts

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	// A drop publishes its reclaim-index entry before its own transaction:
	// 3 forced conflicts, then the entry, then the drop itself.
	assert.Equal(t, 5, store.executeCallsSnapshot()-before, "3 forced conflicts + the index entry + the drop")
}

func TestTableService_DropTable_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	store.forceConflicts = 1000 // never lets a CAS through

	err = svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"})
	require.Error(t, err)

	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
	assert.Equal(t, "db", ddlConflict.Database)
	assert.Equal(t, "t", ddlConflict.Name)

	var txnConflict *TransactionConflictError
	require.ErrorAs(t, err, &txnConflict, "TableDDLConflictError must still unwrap to TransactionConflictError")

	assert.Equal(t, catalogCASMaxAttempts, store.executeCallsSnapshot()-before)
}

// ---------------------------------------------------------------------------
// RenameTable
// ---------------------------------------------------------------------------

func TestTableService_RenameTable_Success(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "old_name", Columns: testColumns()})
	require.NoError(t, err)

	require.NoError(t, svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "old_name", To: "new_name"}))

	_, err = svc.GetTable(ctx, "db", "old_name")
	var notFound *TableNotFoundError
	assert.ErrorAs(t, err, &notFound)

	got, err := svc.GetTable(ctx, "db", "new_name")
	require.NoError(t, err)
	assert.Equal(t, "new_name", got.Name)
	assert.Equal(t, def.TableID, got.TableID, "TableID must not change across a rename")
}

// TestTableService_RenameTable_DataKeysUnaffected asserts the load-bearing
// property that makes RENAME TABLE a pure catalog operation: row/index keys
// are composed from the table's immutable TableID (tableDataPrefixFor,
// tableIndexPrefixFor), never from its name, so a rename never touches
// them. A key written under the old name's TableID-derived data prefix must
// still read back, unmoved, after the rename.
func TestTableService_RenameTable_DataKeysUnaffected(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "old_name", Columns: testColumns()})
	require.NoError(t, err)

	dataKey := tableDataPrefixFor("db", def.TableID, 0) + "1"
	_, err = store.PutIfAbsent(ctx, dataKey, []byte("row-1"))
	require.NoError(t, err)

	require.NoError(t, svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "old_name", To: "new_name"}))

	value, err := store.Get(ctx, dataKey)
	require.NoError(t, err, "row data must be unaffected by a rename")
	assert.Equal(t, []byte("row-1"), value)
}

func TestTableService_RenameTable_SameName_NoOp(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	require.NoError(t, svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "t"}))
	assert.Equal(t, before, store.executeCallsSnapshot())
}

func TestTableService_RenameTable_TargetAlreadyExists(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "a", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "b", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "a", To: "b"})
	var alreadyExists *TableAlreadyExistsError
	require.ErrorAs(t, err, &alreadyExists)
	assert.Equal(t, "b", alreadyExists.Name)
}

func TestTableService_RenameTable_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	err := svc.RenameTable(context.Background(), &RenameTableRequest{Database: "db", From: "missing", To: "t"})
	var notFound *TableNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

func TestTableService_RenameTable_InvalidTargetName(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "__reserved"})
	var invalidName *TableInvalidNameError
	assert.ErrorAs(t, err, &invalidName)
}

func TestTableService_RenameTable_RetryThenSucceed(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	store.forceConflicts = 2
	require.NoError(t, svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "t2"}))
	assert.Equal(t, 3, store.executeCallsSnapshot()-before)
}

func TestTableService_RenameTable_UnmarshalError(t *testing.T) {
	svc, store := newTestTableService()
	store.data[tableMetadataKey("db", "t")] = &fakeCatalogRecord{value: []byte("not json"), modRevision: 1}

	err := svc.RenameTable(context.Background(), &RenameTableRequest{Database: "db", From: "t", To: "t2"})
	var metaErr *TableMetadataError
	require.ErrorAs(t, err, &metaErr)
}

func TestTableService_RenameTable_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	store.forceConflicts = 1000
	err = svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "t2"})
	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
}

// ---------------------------------------------------------------------------
// TruncateTable (go-qrdb#547)
// ---------------------------------------------------------------------------

// TestTableService_TruncateTable_FirstTruncateEver verifies a table that has
// never been truncated -- whose generation key does not exist yet --
// truncates successfully rather than erroring on the absent key
// (revisionOrAbsent's whole reason to exist), advancing generation 0 to 1
// and resetting the AUTO_INCREMENT sequence (MySQL semantics, the default).
func TestTableService_TruncateTable_FirstTruncateEver(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	assert.Equal(t, uint64(0), def.Generation)

	// Allocate an AUTO_INCREMENT value so the sequence key exists and is
	// nonzero before truncating -- otherwise the reset branch (which skips
	// proposing a no-op Put when the sequence is already 0) never runs.
	_, _, err = svc.seq.Allocate(ctx, "db", TableAutoIncrementSequenceName(def.TableID), 1)
	require.NoError(t, err)

	got, err := svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.Generation)

	live, err := svc.readGeneration(ctx, "db", def.TableID)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), live)

	seqData, err := store.Get(ctx, tableSequenceKey("db", TableAutoIncrementSequenceName(def.TableID)))
	require.NoError(t, err)
	state := decodeSequenceState(seqData)
	assert.Equal(t, uint64(0), state.counter, "AUTO_INCREMENT must reset to 0")
	assert.Equal(t, uint64(1), state.epoch, "the reset must advance the epoch, fencing blocks reserved before it")
}

// TestTableService_TruncateTable_RowIDSequenceUntouched pins the other half
// of the ruling in docs/sql/engineering.md#tables-with-no-primary-key:
// TruncateTable's resetSequence parameter names only
// TableAutoIncrementSequenceName. The hidden row id sequence a keyless
// table draws from (TableRowIDSequenceName) is a distinct counter that
// TRUNCATE's compare-and-swap never mentions, reset or not -- proven here
// by the key's ModRevision being unchanged, not merely its decoded value.
func TestTableService_TruncateTable_RowIDSequenceUntouched(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	rowIDSeq := TableRowIDSequenceName(def.TableID)
	_, _, err = svc.seq.Allocate(ctx, "db", rowIDSeq, 3)
	require.NoError(t, err)

	before, err := store.GetRevision(ctx, tableSequenceKey("db", rowIDSeq))
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	after, err := store.GetRevision(ctx, tableSequenceKey("db", rowIDSeq))
	require.NoError(t, err)
	assert.Equal(t, before.ModRevision, after.ModRevision, "TRUNCATE must not write the row id sequence key at all")
	state := decodeSequenceState(after.Value)
	assert.Equal(t, uint64(3), state.counter, "the row id counter must survive TRUNCATE unreset")
	assert.Equal(t, uint64(0), state.epoch, "TRUNCATE must never fence row id blocks -- it never resets them")
}

// TestTableService_TruncateTable_PostgreSQLKeepsSequence verifies
// resetSequence=false (PostgreSQL's plain TRUNCATE, without RESTART
// IDENTITY) leaves an already-nonzero sequence untouched.
func TestTableService_TruncateTable_PostgreSQLKeepsSequence(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, _, err = svc.seq.Allocate(ctx, "db", TableAutoIncrementSequenceName(def.TableID), 5)
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", false)
	require.NoError(t, err)

	seqData, err := store.Get(ctx, tableSequenceKey("db", TableAutoIncrementSequenceName(def.TableID)))
	require.NoError(t, err)
	state := decodeSequenceState(seqData)
	assert.Equal(t, uint64(5), state.counter, "PostgreSQL TRUNCATE must not reset the sequence")
	assert.Equal(t, uint64(0), state.epoch, "a TRUNCATE that keeps the sequence must not fence its blocks")
}

// TestTableService_TruncateTable_ZeroSequenceKeepsEpoch verifies a MySQL
// TRUNCATE of a table whose AUTO_INCREMENT counter is already zero writes
// the state SequenceServicer.Reset would leave -- nothing -- to the epoch:
// no block can have been reserved since the epoch began.
func TestTableService_TruncateTable_ZeroSequenceKeepsEpoch(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	seqName := TableAutoIncrementSequenceName(def.TableID)
	_, _, err = svc.seq.Allocate(ctx, "db", seqName, 3)
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	seqData, err := store.Get(ctx, tableSequenceKey("db", seqName))
	require.NoError(t, err)
	assert.Equal(t, sequenceState{counter: 0, epoch: 1}, decodeSequenceState(seqData))
}

// TestTableService_TruncateTable_FencesAllocationsAcrossResets verifies the
// epoch TruncateTable writes is the one SequenceServicer reports: Allocate
// after the TRUNCATE returns 1 under the advanced epoch, and Epoch agrees.
func TestTableService_TruncateTable_FencesAllocationsAcrossResets(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	seqName := TableAutoIncrementSequenceName(def.TableID)
	_, before, err := svc.seq.Allocate(ctx, "db", seqName, 4)
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	epoch, err := svc.seq.Epoch(ctx, "db", seqName)
	require.NoError(t, err)
	assert.Equal(t, before+1, epoch)

	first, after, err := svc.seq.Allocate(ctx, "db", seqName, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)
	assert.Equal(t, epoch, after)
}

// TestTableService_TruncateTable_PublishesReclaimJobNamingTheOldGeneration
// verifies the reclaim job TruncateTable publishes names the OLD
// generation's data prefix and that generation's WHOLE index keyspace (so
// entries under a name the catalog no longer lists go too), and nothing from
// the new generation; and that its index entry carries what the sweeper
// needs to route and settle it.
func TestTableService_TruncateTable_PublishesReclaimJobNamingTheOldGeneration(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")

	def, err := svc.CreateTable(ctx, &CreateTableRequest{
		Database: "db", Name: "t", Columns: testColumns(),
		Indexes: []TableIndex{{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}}},
	})
	require.NoError(t, err)
	metaBefore, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	jobKeys := store.keysWithPrefix(reclaimJobPrefix)
	require.Len(t, jobKeys, 1)
	data, err := store.Get(ctx, jobKeys[0])
	require.NoError(t, err)
	var job ReclaimJob
	require.NoError(t, json.Unmarshal(data, &job))
	assert.Equal(t, "acme", job.TenantID)
	assert.Equal(t, "db", job.Database)
	assert.True(t, strings.HasPrefix(job.ID, "acme:table:db:1:0:"), "job ID %q", job.ID)
	assert.ElementsMatch(t, []string{
		tableDataPrefixFor("db", def.TableID, 0),
		tableIndexGenerationPrefixFor("db", def.TableID, 0),
	}, job.KeyPrefixes)

	entryData, err := store.Get(ctx, reclaimIndexKey(job.ID))
	require.NoError(t, err)
	var entry ReclaimIndexEntry
	require.NoError(t, json.Unmarshal(entryData, &entry))
	assert.Equal(t, ReclaimIndexEntry{
		JobID: job.ID, TenantID: "acme", Database: "db",
		Placement: StorePlacementShared, Origin: "node-1",
		Fences: []ReclaimFence{
			{Key: tableMetadataKey("db", "t"), ModRevision: metaBefore.ModRevision},
			{Key: tableGenerationKey("db", def.TableID), ModRevision: 0},
		},
		CreatedAt: entry.CreatedAt,
	}, entry)
}

// newSplitTableService builds a TableService whose reclaim index lives in a
// SEPARATE system store -- a dedicated store's shape, where the entry and the
// job cannot share one transaction.
func newSplitTableService(placement StorePlacement) (*TableService, *fakeCatalogStore, *fakeCatalogStore) {
	store := newFakeCatalogStore()
	system := newFakeCatalogStore()
	seq := NewSequenceService(store, store)
	return NewTableService(store, store, store, seq, WithReclaimIndex(system, placement, "node-7")), store, system
}

// TestTableService_TruncateTable_IndexEntryIsPublishedBeforeTheJob pins the
// crash-safe order: at the instant the job's transaction runs, the entry
// naming it is already durable in the system store.
func TestTableService_TruncateTable_IndexEntryIsPublishedBeforeTheJob(t *testing.T) {
	svc, store, system := newSplitTableService(StorePlacementNode)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	var entriesAtJobCommit []string
	store.beforeExecute = func() { entriesAtJobCommit = system.keysWithPrefix(reclaimIndexPrefix) }

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	require.Len(t, entriesAtJobCommit, 1, "the entry must exist before the job's transaction runs")
	jobs := store.keysWithPrefix(reclaimJobPrefix)
	require.Len(t, jobs, 1)
	assert.Equal(t, reclaimIndexKey(strings.TrimPrefix(jobs[0], reclaimJobPrefix)), entriesAtJobCommit[0])
	assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix), "the entry lives only in the system store")
}

// TestTableService_TruncateTable_LostAttemptWithdrawsItsOwnEntry proves an
// attempt whose compare-and-swap definitely lost removes the entry it
// published, and the retry that wins leaves exactly one entry -- naming the
// job that actually committed.
func TestTableService_TruncateTable_LostAttemptWithdrawsItsOwnEntry(t *testing.T) {
	svc, store, system := newSplitTableService(StorePlacementNode)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	lost := 0
	store.beforeExecute = func() {
		if lost == 0 {
			lost++
			store.forceConflicts = 1
		}
	}
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	require.Equal(t, 1, lost)

	entries := system.keysWithPrefix(reclaimIndexPrefix)
	jobs := store.keysWithPrefix(reclaimJobPrefix)
	require.Len(t, entries, 1, "the lost attempt's entry must be withdrawn")
	require.Len(t, jobs, 1)
	assert.Equal(t, strings.TrimPrefix(jobs[0], reclaimJobPrefix), strings.TrimPrefix(entries[0], reclaimIndexPrefix))
}

// TestTableService_TruncateTable_UnknownOutcomeLeavesItsEntry proves an
// attempt whose outcome is unknown does NOT withdraw its entry: the job may
// have committed, and only the sweeper's fence check may settle it.
func TestTableService_TruncateTable_UnknownOutcomeLeavesItsEntry(t *testing.T) {
	svc, store, system := newSplitTableService(StorePlacementNode)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	store.beforeExecute = func() { store.injectExecuteErr = errors.New("proposal timed out") }
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.Error(t, err)
	assert.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 1)
}

// TestTableService_TruncateTable_IndexPublishFailureStopsBeforeTheJob proves
// a failed entry publish never lets the job's transaction run at all.
func TestTableService_TruncateTable_IndexPublishFailureStopsBeforeTheJob(t *testing.T) {
	svc, store, system := newSplitTableService(StorePlacementNode)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	system.injectExecuteErr = errors.New("system store unavailable")
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.Error(t, err)
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
	gen, err := svc.readGeneration(ctx, "db", def.TableID)
	require.NoError(t, err)
	assert.Zero(t, gen)
}

// TestTableService_TruncateTable_TwoTenantsSameTableIDNeverCollide is the
// dedicated-store collision: two tenants' stores both allocate TableID 1,
// and their entries share ONE system store.
func TestTableService_TruncateTable_TwoTenantsSameTableIDNeverCollide(t *testing.T) {
	system := newFakeCatalogStore()
	for _, tenantID := range []string{"acme", "globex"} {
		store := newFakeCatalogStore()
		svc := NewTableService(store, store, store, NewSequenceService(store, store),
			WithReclaimIndex(system, StorePlacementNode, "node-7"))
		ctx := tenant.WithTenantID(context.Background(), tenantID)
		def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
		require.NoError(t, err)
		require.Equal(t, uint64(1), def.TableID)
		_, err = svc.TruncateTable(ctx, "db", "t", true)
		require.NoError(t, err)
	}
	assert.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 2)
}

// TestTableService_TruncateTable_RefusesWithoutAReclaimIndex pins the
// fail-closed composition requirement.
func TestTableService_TruncateTable_RefusesWithoutAReclaimIndex(t *testing.T) {
	store := newFakeCatalogStore()
	svc := NewTableService(store, store, store, NewSequenceService(store, store))
	_, err := svc.CreateTable(context.Background(), &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.TruncateTable(context.Background(), "db", "t", true)
	var unconfigured *ReclaimIndexUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.Contains(t, unconfigured.Error(), "WithReclaimIndex")
}

// TestTableService_TruncateTable_SecondTruncateAdvancesAgain verifies
// truncating an already-truncated table advances generation 1 to 2, and
// publishes a SECOND reclaim job naming generation 1 (not 0, already
// reclaimed or at least already a separate job).
func TestTableService_TruncateTable_SecondTruncateAdvancesAgain(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	got, err := svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.Generation)

	keys, err := store.List(ctx, reclaimJobPrefix)
	require.NoError(t, err)
	assert.Len(t, keys, 2, "each TRUNCATE publishes its own reclaim job")

	_ = def
}

// TestTableService_TruncateTable_NotFound verifies truncating a table that
// does not exist reports TableNotFoundError rather than silently succeeding.
func TestTableService_TruncateTable_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.TruncateTable(context.Background(), "db", "missing", true)
	var notFound *TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

// TestTableService_TruncateTable_RejectsWhileRewriteInProgress verifies
// TRUNCATE is refused while an ALTER TABLE DROP/MODIFY COLUMN rewrite is
// mid-flight, the same as RenameTable/AlterTable: letting it proceed would
// race the rewrite's own generation check (relational_rewrite.go's
// checkGenerationCurrent) rather than fail it cleanly up front.
func TestTableService_TruncateTable_RejectsWhileRewriteInProgress(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	var inProgress *TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress)
}

// TestTableService_TruncateTable_RetryExhausted mirrors
// TestTableService_RenameTable_RetryExhausted: a catalog CAS that loses
// every race reports TableDDLConflictError rather than looping forever.
func TestTableService_TruncateTable_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	store.forceConflicts = 1000
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
}

// TestTableService_TruncateTable_InvalidTableRef verifies the same
// validateTableRef guard every other DDL entry point applies.
func TestTableService_TruncateTable_InvalidTableRef(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.TruncateTable(context.Background(), "", "t", true)
	require.Error(t, err)
}

// TestTableService_TruncateTable_ConcurrentAllocateAfterSequenceReadIsRetried
// pins the fix for the sequence-reset CAS gap: a concurrent
// SequenceServicer.Allocate landing between TruncateTable's read of the
// AUTO_INCREMENT counter and its own commit must fail THAT attempt (the
// compare on the counter's ModRevision is now unconditional, even when the
// observed value is already 0) rather than let the allocation survive
// unguarded. The retry then observes the raced-in value and correctly
// resets it, so the counter ends at 0 either way -- what changes is that the
// race is CAUGHT, not that TRUNCATE ultimately fails.
func TestTableService_TruncateTable_ConcurrentAllocateAfterSequenceReadIsRetried(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	seqKey := tableSequenceKey("db", TableAutoIncrementSequenceName(def.TableID))

	raced := false
	store.mu.Lock()
	if store.raceAfterGetRevision == nil {
		store.raceAfterGetRevision = make(map[string]func(*fakeCatalogStore))
	}
	store.raceAfterGetRevision[seqKey] = func(f *fakeCatalogStore) {
		if raced {
			return
		}
		raced = true
		// A concurrent Allocate landing in the window between
		// TruncateTable's read and its own commit: write a nonzero counter
		// directly, bypassing TruncateTable's own transaction entirely.
		f.data[seqKey] = &fakeCatalogRecord{value: encodeSequenceCounter(7), modRevision: f.rev + 1000, version: 1}
	}
	store.mu.Unlock()

	got, err := svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err, "the race must be retried, not surfaced as a failure")
	assert.Equal(t, uint64(1), got.Generation)
	assert.True(t, raced, "the race hook must have fired for this test to prove anything")

	seqData, err := store.Get(ctx, seqKey)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), decodeSequenceCounter(seqData),
		"the counter must end at 0 -- the raced-in allocation must not survive TRUNCATE")
}

// TestTableService_TruncateTable_ConcurrentRenameAfterMetadataReadIsCaught
// pins the fix for TRUNCATE's missing metadata compare: a concurrent RENAME
// landing between TruncateTable's read of the table definition and its own
// commit must fail this attempt (the metadata ModRevision compare), and the
// retry -- which re-reads under the ORIGINAL name TruncateTable was called
// with -- correctly reports TableNotFoundError rather than silently
// committing a generation bump computed from column/index data that is no
// longer the table's.
func TestTableService_TruncateTable_ConcurrentRenameAfterMetadataReadIsCaught(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	metaKey := tableMetadataKey("db", "t")

	raced := false
	store.mu.Lock()
	if store.raceAfterGetRevision == nil {
		store.raceAfterGetRevision = make(map[string]func(*fakeCatalogStore))
	}
	store.raceAfterGetRevision[metaKey] = func(f *fakeCatalogStore) {
		if raced {
			return
		}
		raced = true
		delete(f.data, metaKey)
	}
	store.mu.Unlock()

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	var notFound *TableNotFoundError
	require.ErrorAs(t, err, &notFound, "err = %v", err)
	assert.True(t, raced, "the race hook must have fired for this test to prove anything")
}

// TestTableService_TruncateTable_ConcurrentRewriteJournalAfterCheckIsCaught
// pins the fix that adds an explicit journal-absence compare to TRUNCATE's
// own CAS: a rewrite journal created in the window between TruncateTable's
// absence check and its own commit must fail that attempt, and the retry
// correctly reports TableRewriteInProgressError.
func TestTableService_TruncateTable_ConcurrentRewriteJournalAfterCheckIsCaught(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	journalKey := tableDDLJournalKey("db", "t")

	raced := false
	store.mu.Lock()
	if store.raceAfterGetRevision == nil {
		store.raceAfterGetRevision = make(map[string]func(*fakeCatalogStore))
	}
	store.raceAfterGetRevision[journalKey] = func(f *fakeCatalogStore) {
		if raced {
			return
		}
		raced = true
		journal := &TableDDLJournal{State: DDLStateRewriting}
		data, jerr := json.Marshal(journal)
		require.NoError(t, jerr)
		f.data[journalKey] = &fakeCatalogRecord{value: data, modRevision: f.rev + 1000, version: 1}
	}
	store.mu.Unlock()

	_, err = svc.TruncateTable(ctx, "db", "t", true)
	var inProgress *TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress, "err = %v", err)
	assert.True(t, raced, "the race hook must have fired for this test to prove anything")
}

// ---------------------------------------------------------------------------
// RenameIndex (go-qrdb#547)
// ---------------------------------------------------------------------------

func createIndexedTable(t *testing.T, svc *TableService, ctx context.Context) *TableDef {
	t.Helper()
	def, err := svc.CreateTable(ctx, &CreateTableRequest{
		Database: "db", Name: "t", Columns: testColumns(),
		Indexes: []TableIndex{
			{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
			{Name: "idx_created", Columns: []IndexCol{{Name: "created_at"}}},
		},
	})
	require.NoError(t, err)
	return def
}

// TestTableService_Incarnations_AreAssignedAndNeverReused pins the property
// the index keyspace rests on: CreateTable and CreateIndex assign every index
// an incarnation from the table's own counter, ignore any value the caller
// supplies, and never hand one out twice -- not even to a later index
// created under a dropped or renamed index's name.
func TestTableService_Incarnations_AreAssignedAndNeverReused(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def := createIndexedTable(t, svc, ctx)
	assert.Equal(t, uint64(1), def.Indexes[0].Incarnation)
	assert.Equal(t, uint64(2), def.Indexes[1].Incarnation)
	assert.Equal(t, uint64(2), def.IndexIncarnations)

	require.NoError(t, svc.DropIndex(ctx, "db", "t", "idx_email"))
	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}, Incarnation: 1},
	}))
	require.NoError(t, svc.RenameIndex(ctx, &RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_created", To: "idx_old"}))
	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t", Index: TableIndex{Name: "idx_created", Columns: []IndexCol{{Name: "created_at"}}},
	}))

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	byName := map[string]uint64{}
	for _, idx := range got.Indexes {
		byName[idx.Name] = idx.Incarnation
	}
	assert.Equal(t, map[string]uint64{"idx_old": 2, "idx_email": 3, "idx_created": 4}, byName,
		"a reused name gets a new incarnation; a renamed index keeps its own")
	assert.Equal(t, uint64(4), got.IndexIncarnations)
}

// TestTableService_RenameIndex_IsAPureCatalogChange proves a rename is one
// catalog compare-and-swap that publishes nothing: the entries are keyed by
// incarnation, which the renamed index keeps.
func TestTableService_RenameIndex_IsAPureCatalogChange(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def := createIndexedTable(t, svc, ctx)
	before := store.executeCallsSnapshot()

	require.NoError(t, svc.RenameIndex(ctx, &RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_email", To: "idx_mail"}))
	assert.Equal(t, 1, store.executeCallsSnapshot()-before, "ONE catalog transaction")
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
	assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix))

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	require.Len(t, got.Indexes, 2)
	assert.Equal(t, "idx_mail", got.Indexes[0].Name, "the renamed index keeps its position")
	assert.Equal(t, def.Indexes[0].Incarnation, got.Indexes[0].Incarnation, "and its incarnation")
	assert.Equal(t, def.Indexes[0].Columns, got.Indexes[0].Columns)
}

// TestTableService_DropIndex_PublishesAJobForItsIncarnation proves DROP INDEX
// hands the dropped incarnation's entries to the sweeper, in the catalog's
// own transaction, fenced so the sweeper can settle it -- and deletes none of
// them inline.
func TestTableService_DropIndex_PublishesAJobForItsIncarnation(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def := createIndexedTable(t, svc, ctx)
	entryKey := tableIndexPrefixFor("db", def.TableID, 0, def.Indexes[0].Incarnation) + "k"
	store.put(entryKey, nil)
	metaBefore, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)

	require.NoError(t, svc.DropIndex(ctx, "db", "t", "idx_email"))
	assert.True(t, store.has(entryKey), "entries are reclaimed by the sweeper, never inline")

	jobs := store.keysWithPrefix(reclaimJobPrefix)
	require.Len(t, jobs, 1)
	data, err := store.Get(ctx, jobs[0])
	require.NoError(t, err)
	var job ReclaimJob
	require.NoError(t, json.Unmarshal(data, &job))
	assert.Equal(t, []string{tableIndexPrefixFor("db", def.TableID, 0, def.Indexes[0].Incarnation)}, job.KeyPrefixes)

	entryData, err := store.Get(ctx, reclaimIndexKey(job.ID))
	require.NoError(t, err)
	var entry ReclaimIndexEntry
	require.NoError(t, json.Unmarshal(entryData, &entry))
	assert.Equal(t, []ReclaimFence{
		{Key: tableMetadataKey("db", "t"), ModRevision: metaBefore.ModRevision},
		{Key: tableGenerationKey("db", def.TableID), ModRevision: 0},
	}, entry.Fences)
}

// TestTableService_DropIndex_ConcurrentTruncateIsCaught drives a TRUNCATE
// into the window between DROP INDEX's generation read and its commit: the
// attempt must fail and the retry must name the generation the entries are
// actually under.
func TestTableService_DropIndex_ConcurrentTruncateIsCaught(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def := createIndexedTable(t, svc, ctx)
	genKey := tableGenerationKey("db", def.TableID)

	raced := false
	store.beforeExecute = func() {
		if !raced {
			raced = true
			store.put(genKey, encodeSequenceCounter(1))
		}
	}
	require.NoError(t, svc.DropIndex(ctx, "db", "t", "idx_email"))
	require.True(t, raced)

	jobs := store.keysWithPrefix(reclaimJobPrefix)
	require.Len(t, jobs, 1, "the lost attempt published nothing")
	data, err := store.Get(ctx, jobs[0])
	require.NoError(t, err)
	var job ReclaimJob
	require.NoError(t, json.Unmarshal(data, &job))
	assert.Equal(t, []string{tableIndexPrefixFor("db", def.TableID, 1, def.Indexes[0].Incarnation)}, job.KeyPrefixes)
	assert.Len(t, store.keysWithPrefix(reclaimIndexPrefix), 1, "the lost attempt withdrew its entry")
}

func TestTableService_DropIndex_Rejections(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	createIndexedTable(t, svc, ctx)

	var notFound *TableIndexNotFoundError
	require.ErrorAs(t, svc.DropIndex(ctx, "db", "t", "nope"), &notFound)
	var tableNotFound *TableNotFoundError
	require.ErrorAs(t, svc.DropIndex(ctx, "db", "gone", "idx_email"), &tableNotFound)
	var invalid *TableInvalidNameError
	require.ErrorAs(t, svc.DropIndex(ctx, "db", "t", ""), &invalid)

	store := newFakeCatalogStore()
	bare := NewTableService(store, store, store, NewSequenceService(store, store))
	createIndexedTable(t, bare, context.Background())
	var unconfigured *ReclaimIndexUnconfiguredError
	require.ErrorAs(t, bare.DropIndex(context.Background(), "db", "t", "idx_email"), &unconfigured)
}

// TestTableService_GetTable_ReportsTheRevisionsItRead proves GetTable carries
// the metadata and generation revisions it decoded, which DML fences and
// snapshot checks compare against.
func TestTableService_GetTable_ReportsTheRevisionsItRead(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def := createIndexedTable(t, svc, ctx)
	_, err := svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	meta, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)
	gen, err := store.GetRevision(ctx, tableGenerationKey("db", def.TableID))
	require.NoError(t, err)
	assert.Equal(t, meta.ModRevision, got.MetadataRevision)
	assert.Equal(t, gen.ModRevision, got.GenerationRevision)
	assert.Equal(t, uint64(1), got.Generation)
}

func TestTableService_RenameIndex_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		req      RenameTableIndexRequest
		want     any
		badName  string
		checkBad bool
	}{
		{name: "missing source", req: RenameTableIndexRequest{Database: "db", Table: "t", From: "nope", To: "x"}, want: new(*TableIndexNotFoundError)},
		{name: "target exists", req: RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_email", To: "idx_created"}, want: new(*TableIndexAlreadyExistsError)},
		{name: "missing table", req: RenameTableIndexRequest{Database: "db", Table: "gone", From: "idx_email", To: "x"}, want: new(*TableNotFoundError)},
		{name: "empty target", req: RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_email", To: ""}, want: new(*TableInvalidNameError), checkBad: true},
		{name: "empty source", req: RenameTableIndexRequest{Database: "db", Table: "t", From: "", To: "idx_x"}, want: new(*TableInvalidNameError), checkBad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestTableService()
			ctx := tenant.WithTenantID(context.Background(), "acme")
			createIndexedTable(t, svc, ctx)
			err := svc.RenameIndex(ctx, &tc.req)
			require.Error(t, err)
			require.ErrorAs(t, err, tc.want)
			if tc.checkBad {
				var invalid *TableInvalidNameError
				require.ErrorAs(t, err, &invalid)
				assert.Empty(t, invalid.Name, "the empty name is the one reported")
			}
		})
	}
}

// TestTableService_DropIndex_GenerationReadErrorIsReported proves a failed
// generation read aborts the drop rather than being read as generation 0.
func TestTableService_DropIndex_GenerationReadErrorIsReported(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	createIndexedTable(t, svc, ctx)
	store.mu.Lock()
	store.raceAfterGetRevision = map[string]func(*fakeCatalogStore){
		tableMetadataKey("db", "t"): func(f *fakeCatalogStore) { f.injectGetRevisionErr = errors.New("read failed") },
	}
	store.mu.Unlock()

	require.Error(t, svc.DropIndex(ctx, "db", "t", "idx_email"))
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Len(t, got.Indexes, 2)
}

func TestTableService_RenameIndex_InvalidTableRef(t *testing.T) {
	svc, _ := newTestTableService()
	require.Error(t, svc.RenameIndex(context.Background(), &RenameTableIndexRequest{Table: "t", From: "a", To: "b"}))
}

// ---------------------------------------------------------------------------
// DropTable and the generation counter (go-qrdb#547)
// ---------------------------------------------------------------------------

// TestTableService_DropTable_DeletesTheGenerationCounterAtomically proves the
// counter and the definition naming its TableID go in one transaction.
func TestTableService_DropTable_DeletesTheGenerationCounterAtomically(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	before := store.executeCallsSnapshot()

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	assert.Equal(t, 2, store.executeCallsSnapshot()-before, "the reclaim-index entry, then ONE transaction for the rest")
	assert.False(t, store.has(tableGenerationKey("db", def.TableID)))
	assert.False(t, store.has(tableMetadataKey("db", "t")))
}

// TestTableService_DropTable_ConcurrentTruncateIsCaught drives a TRUNCATE
// between DROP TABLE's reads and its commit: the drop's compare on the
// generation key must fail that attempt, and the retry drops the table at
// the generation it actually has.
func TestTableService_DropTable_ConcurrentTruncateIsCaught(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	genKey := tableGenerationKey("db", def.TableID)

	attempts := 0
	store.beforeExecute = func() {
		attempts++
		if attempts == 1 {
			store.put(genKey, encodeSequenceCounter(1))
		}
	}
	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	assert.Equal(t, 2, attempts, "the first attempt must lose to the TRUNCATE")
	assert.False(t, store.has(genKey))
}

// TestTableService_DropTable_FailedDropKeepsTheGenerationCounter proves a
// drop that does not commit leaves the table exactly as it was -- still at
// its truncated generation, never silently back to generation 0.
func TestTableService_DropTable_FailedDropKeepsTheGenerationCounter(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)

	store.forceConflicts = 1000
	require.Error(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	store.forceConflicts = 0

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.Generation)
	assert.True(t, store.has(tableGenerationKey("db", def.TableID)))
}

func TestTableService_DropTable_CorruptDefinitionIsReported(t *testing.T) {
	svc, store := newTestTableService()
	store.put(tableMetadataKey("db", "t"), []byte("{not json"))
	err := svc.DropTable(context.Background(), &DropTableRequest{Database: "db", Name: "t"})
	var meta *TableMetadataError
	require.ErrorAs(t, err, &meta)
}

// ---------------------------------------------------------------------------
// AlterTable
// ---------------------------------------------------------------------------

func TestTableService_AlterTable_AddColumn(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	before := def.SchemaVersion

	updated, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "nickname", Type: "VARCHAR(64)"}}},
	})
	require.NoError(t, err)
	assert.Len(t, updated.Columns, len(def.Columns)+1)
	assert.Equal(t, before+1, updated.SchemaVersion)

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Len(t, got.Columns, len(def.Columns)+1)
}

func TestTableService_AlterTable_AddColumn_DuplicateAutoIncrement(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "id2", Type: "BIGINT", AutoIncrement: true}}},
	})
	var e *TableMultipleAutoIncrementError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_AddColumn_DuplicateName(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "EMAIL", Type: "VARCHAR(1)"}}},
	})
	var e *TableDuplicateColumnError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_AddColumn_InvalidName(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "bad name", Type: "INT"}}},
	})
	var e *TableInvalidNameError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "column", e.Kind)
}

func TestTableService_AlterTable_AddColumn_UnsupportedType(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "FROBNICATE"}}},
	})
	var e *TableUnsupportedColumnTypeError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_RenameColumn(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	updated, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "email", NewName: "email_address"}},
	})
	require.NoError(t, err)

	found := false
	for _, c := range updated.Columns {
		if c.Name == "email_address" {
			found = true
		}
		assert.NotEqual(t, "email", c.Name)
	}
	assert.True(t, found)
}

func TestTableService_AlterTable_RenameColumn_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "missing", NewName: "x"}},
	})
	var e *TableColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_RenameColumn_DuplicateTarget(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "email", NewName: "ID"}},
	})
	var e *TableDuplicateColumnError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_RenameColumn_InvalidNewName(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "email", NewName: "bad name"}},
	})
	var e *TableInvalidNameError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_DropColumn_RequiresRewrite(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	// The live table is untouched until a (future) rewrite executor commits
	// the journaled target and clears the journal.
	live, err := getRawTableDef(t, svc, "db", "t")
	require.NoError(t, err)
	assert.Len(t, live.Columns, len(def.Columns), "live definition must not change while a rewrite is only journaled")
}

// getRawTableDef bypasses TableService.GetTable's journal-presence guard by
// reading the metadata key directly, so tests can assert the live
// definition is untouched even while a journal makes GetTable itself
// refuse to serve the table.
func getRawTableDef(t *testing.T, svc *TableService, database, name string) (*TableDef, error) {
	t.Helper()
	data, err := svc.kv.Get(context.Background(), tableMetadataKey(database, name))
	if err != nil {
		return nil, err
	}
	var def TableDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, err
	}
	return &def, nil
}

func TestTableService_AlterTable_DropColumn_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "missing"}},
	})
	var e *TableColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_ModifyColumn_RequiresRewrite(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterModifyColumn, Name: "email", Column: ColumnDef{Type: "TEXT"}}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)
}

func TestTableService_AlterTable_ModifyColumn_UnsupportedType(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterModifyColumn, Name: "email", Column: ColumnDef{Type: "FROBNICATE"}}},
	})
	var e *TableUnsupportedColumnTypeError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_ModifyColumn_RenameToDuplicate(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterModifyColumn, Name: "email", Column: ColumnDef{Name: "id", Type: "TEXT"}}},
	})
	var e *TableDuplicateColumnError
	require.ErrorAs(t, err, &e)
}

// TestTableService_AlterTable_RewriteJournal_WriteDetectDelete exercises the
// full journal lifecycle: written by the opening CAS of a rewrite-requiring
// AlterTable, detected by GetTable as TableRewriteInProgressError while
// present, and removable (as a future rewrite executor's closing CAS, or a
// DropTable, would do) to restore normal service.
func TestTableService_AlterTable_RewriteJournal_WriteDetectDelete(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	journalKey := tableDDLJournalKey("db", "t")
	_, err = store.Get(ctx, journalKey)
	require.Error(t, err, "journal must not exist before any rewrite-requiring alter")

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	journalData, err := store.Get(ctx, journalKey)
	require.NoError(t, err, "journal must exist once a rewrite is required")
	var journal TableDDLJournal
	require.NoError(t, json.Unmarshal(journalData, &journal))
	assert.Equal(t, DDLStateRewriting, journal.State)
	require.NotNil(t, journal.Target)
	assert.Len(t, journal.Target.Columns, len(testColumns())-1)

	_, err = svc.GetTable(ctx, "db", "t")
	var inProgress *TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress)

	// Simulate the (not yet implemented) rewrite executor's closing CAS:
	// delete the journal directly.
	store.mu.Lock()
	delete(store.data, journalKey)
	store.mu.Unlock()

	_, err = svc.GetTable(ctx, "db", "t")
	require.NoError(t, err, "table must be servable again once the journal is cleared")
}

// TestTableService_AlterTable_RejectsWhileRewriteInProgress verifies that a
// second DDL statement -- metadata-only or not -- is rejected while a
// rewrite journal is present, so nothing can race the closing CAS a resumed
// rewrite eventually issues.
func TestTableService_AlterTable_RejectsWhileRewriteInProgress(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	// A second, purely metadata-only change must also be rejected.
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "created_at", NewName: "created"}},
	})
	var inProgress *TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress)
}

// TestTableService_AlterTable_DropColumn_RemovesFromPrimaryKeyAndIndexes
// verifies that dropping a column that participates in the primary key or a
// secondary index also removes it from those references (and drops any
// index left with zero columns), so the journaled target never carries a
// dangling column reference.
func TestTableService_AlterTable_DropColumn_RemovesFromPrimaryKeyAndIndexes(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{
		Database: "db", Name: "t",
		Columns:    []ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "email", Type: "VARCHAR(255)"}, {Name: "name", Type: "VARCHAR(255)"}},
		PrimaryKey: []PKColumn{{Name: "id"}, {Name: "email"}},
		Indexes: []TableIndex{
			{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
			{Name: "idx_email_name", Columns: []IndexCol{{Name: "email"}, {Name: "name"}}},
		},
	})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	journal, _, err := svc.RewriteJournal(ctx, "db", "t")
	require.NoError(t, err)
	require.NotNil(t, journal.Target)

	assert.Len(t, journal.Target.PrimaryKey, 1)
	assert.Equal(t, "id", journal.Target.PrimaryKey[0].Name)

	// idx_email referenced only the dropped column and must be gone entirely.
	for _, idx := range journal.Target.Indexes {
		assert.NotEqual(t, "idx_email", idx.Name)
	}
	// idx_email_name survives with only "name" remaining.
	found := false
	for _, idx := range journal.Target.Indexes {
		if idx.Name == "idx_email_name" {
			found = true
			require.Len(t, idx.Columns, 1)
			assert.Equal(t, "name", idx.Columns[0].Name)
		}
	}
	assert.True(t, found, "idx_email_name must survive with its remaining column")
}

// ---------------------------------------------------------------------------
// RewriteJournal / CompleteRewrite
// ---------------------------------------------------------------------------

func TestTableService_RewriteJournal_Success(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	journal, current, err := svc.RewriteJournal(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, DDLStateRewriting, journal.State)
	assert.Len(t, journal.Changes, 1)
	assert.Equal(t, AlterDropColumn, journal.Changes[0].Action)
	assert.Equal(t, "email", journal.Changes[0].Name)
	require.NotNil(t, journal.Target)
	assert.Len(t, journal.Target.Columns, len(testColumns())-1, "target reflects the post-rewrite column list")

	// current is the still-live, pre-rewrite definition -- the one existing
	// rows still physically match.
	assert.Len(t, current.Columns, len(testColumns()), "current must still be the pre-rewrite definition")
}

func TestTableService_RewriteJournal_NoneInProgress(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, _, err = svc.RewriteJournal(ctx, "db", "t")
	var none *TableNoRewriteInProgressError
	require.ErrorAs(t, err, &none)
}

func TestTableService_RewriteJournal_Validation(t *testing.T) {
	svc, _ := newTestTableService()
	_, _, err := svc.RewriteJournal(context.Background(), "", "t")
	var e *TableEmptyDatabaseError
	require.ErrorAs(t, err, &e)
}

func TestTableService_RewriteJournal_UnmarshalJournalError(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	stored, perr := store.PutIfAbsent(ctx, tableDDLJournalKey("db", "t"), []byte("not json"))
	require.NoError(t, perr)
	require.True(t, stored)

	_, _, err = svc.RewriteJournal(ctx, "db", "t")
	var e *TableMetadataError
	require.ErrorAs(t, err, &e)
}

func TestTableService_RewriteJournal_CurrentDefMissing(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	journal := &TableDDLJournal{State: DDLStateRewriting, Target: &TableDef{Name: "t"}}
	data, jerr := json.Marshal(journal)
	require.NoError(t, jerr)
	stored, perr := store.PutIfAbsent(ctx, tableDDLJournalKey("db", "t"), data)
	require.NoError(t, perr)
	require.True(t, stored)

	// Delete the live catalog record out from under the (still-present)
	// journal -- a state that should never occur in production, but
	// RewriteJournal must fail closed rather than panic on a nil current.
	store.mu.Lock()
	delete(store.data, tableMetadataKey("db", "t"))
	store.mu.Unlock()

	_, _, err = svc.RewriteJournal(ctx, "db", "t")
	var e *TableNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestTableService_RewriteJournal_CurrentDefUnmarshalError(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	journal := &TableDDLJournal{State: DDLStateRewriting, Target: &TableDef{Name: "t"}}
	data, jerr := json.Marshal(journal)
	require.NoError(t, jerr)
	stored, perr := store.PutIfAbsent(ctx, tableDDLJournalKey("db", "t"), data)
	require.NoError(t, perr)
	require.True(t, stored)

	store.mu.Lock()
	store.data[tableMetadataKey("db", "t")] = &fakeCatalogRecord{value: []byte("not json"), modRevision: 1, createRevision: 1, version: 1}
	store.mu.Unlock()

	_, _, err = svc.RewriteJournal(ctx, "db", "t")
	var e *TableMetadataError
	require.ErrorAs(t, err, &e)
}

func TestTableService_CompleteRewrite_Success(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	result, err := svc.CompleteRewrite(ctx, "db", "t")
	require.NoError(t, err)
	assert.Len(t, result.Columns, len(testColumns())-1)

	// GetTable now serves the completed definition instead of refusing with
	// TableRewriteInProgressError.
	def, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Len(t, def.Columns, len(testColumns())-1)

	journalData, jerr := store.Get(ctx, tableDDLJournalKey("db", "t"))
	assert.Error(t, jerr, "journal must be deleted")
	assert.Nil(t, journalData)
}

func TestTableService_CompleteRewrite_NoneInProgress(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.CompleteRewrite(ctx, "db", "t")
	var none *TableNoRewriteInProgressError
	require.ErrorAs(t, err, &none)
}

func TestTableService_CompleteRewrite_Validation(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.CompleteRewrite(context.Background(), "db", "")
	var e *TableInvalidNameError
	require.ErrorAs(t, err, &e)
}

// TestTableService_CompleteRewrite_Idempotent verifies that two concurrent
// completions -- e.g. two nodes both resuming the same interrupted rewrite
// -- result in exactly one winner and the other observing
// TableNoRewriteInProgressError rather than corrupting the catalog.
func TestTableService_CompleteRewrite_Idempotent(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "email"}},
	})
	var rewriteRequired *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	_, err = svc.CompleteRewrite(ctx, "db", "t")
	require.NoError(t, err)

	_, err = svc.CompleteRewrite(ctx, "db", "t")
	var none *TableNoRewriteInProgressError
	require.ErrorAs(t, err, &none)
}

func TestTableService_AlterTable_NoChanges(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{Database: "db", Name: "t"})
	var e *TableNoChangesError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_UnsupportedAction(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAction("frobnicate"), Name: "email"}},
	})
	var e *TableUnsupportedAlterActionError
	require.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.AlterTable(context.Background(), &AlterTableRequest{
		Database: "db", Name: "missing",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "INT"}}},
	})
	var e *TableNotFoundError
	assert.ErrorAs(t, err, &e)
}

func TestTableService_AlterTable_StaleExpectedRevision(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes:          []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "INT"}}},
		ExpectedRevision: 99999,
	})
	var stale *TableStaleRevisionError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, int64(99999), stale.Expected)
	assert.Equal(t, before, store.executeCallsSnapshot(), "a stale ExpectedRevision must fail without proposing anything")
}

func TestTableService_AlterTable_MatchingExpectedRevision(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	// Discover the current ModRevision the way a real caller would: read it
	// via the MVCC layer directly (GetTable does not expose it).
	rev, err := serviceTestGetRevision(t, svc, "db", "t")
	require.NoError(t, err)

	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes:          []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "INT"}}},
		ExpectedRevision: rev,
	})
	require.NoError(t, err)
}

func serviceTestGetRevision(t *testing.T, svc *TableService, database, name string) (int64, error) {
	t.Helper()
	kv, err := svc.mvcc.GetRevision(context.Background(), tableMetadataKey(database, name))
	if err != nil {
		return 0, err
	}
	return kv.ModRevision, nil
}

func TestTableService_AlterTable_RetryThenSucceed(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	store.forceConflicts = 4
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "INT"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, 5, store.executeCallsSnapshot()-before)
}

func TestTableService_AlterTable_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	store.forceConflicts = 1000
	_, err = svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "x", Type: "INT"}}},
	})
	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
}

func TestTableService_AlterTable_Validation(t *testing.T) {
	svc, _ := newTestTableService()
	_, err := svc.AlterTable(context.Background(), &AlterTableRequest{Name: "t"})
	var e *TableEmptyDatabaseError
	assert.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// CreateIndex / DropIndex
// ---------------------------------------------------------------------------

func TestTableService_CreateIndex_Success(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
	}))

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	require.Len(t, got.Indexes, 1)
	assert.Equal(t, "idx_email", got.Indexes[0].Name)
}

func TestTableService_CreateIndex_ColumnNotFound(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_x", Columns: []IndexCol{{Name: "missing"}}},
	})
	var e *TableIndexColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestTableService_CreateIndex_AlreadyExists(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	idxReq := &CreateTableIndexRequest{Database: "db", Table: "t", Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}}}
	require.NoError(t, svc.CreateIndex(ctx, idxReq))

	err = svc.CreateIndex(ctx, idxReq)
	var e *TableIndexAlreadyExistsError
	require.ErrorAs(t, err, &e)
}

func TestTableService_CreateIndex_UnmarshalError(t *testing.T) {
	svc, store := newTestTableService()
	store.data[tableMetadataKey("db", "t")] = &fakeCatalogRecord{value: []byte("not json"), modRevision: 1}

	err := svc.CreateIndex(context.Background(), &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_x", Columns: []IndexCol{{Name: "x"}}},
	})
	var metaErr *TableMetadataError
	require.ErrorAs(t, err, &metaErr)
}

func TestTableService_CreateIndex_TableNotFound(t *testing.T) {
	svc, _ := newTestTableService()
	err := svc.CreateIndex(context.Background(), &CreateTableIndexRequest{
		Database: "db", Table: "missing",
		Index: TableIndex{Name: "idx_x", Columns: []IndexCol{{Name: "x"}}},
	})
	var e *TableNotFoundError
	assert.ErrorAs(t, err, &e)
}

func TestTableService_CreateIndex_RetryThenSucceed(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	before := store.executeCallsSnapshot()
	store.forceConflicts = 2
	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
	}))
	assert.Equal(t, 3, store.executeCallsSnapshot()-before)
}

func TestTableService_CreateIndex_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	store.forceConflicts = 1000
	err = svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
	})
	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
}

func TestTableService_DropIndex_Success(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
	}))

	require.NoError(t, svc.DropIndex(ctx, "db", "t", "idx_email"))

	got, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Empty(t, got.Indexes)
}

func TestTableService_DropIndex_NotFound(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.DropIndex(ctx, "db", "t", "missing")
	var e *TableIndexNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestTableService_DropIndex_EmptyName(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.DropIndex(ctx, "db", "t", "")
	var e *TableInvalidNameError
	require.ErrorAs(t, err, &e)
}

func TestTableService_DropIndex_UnmarshalError(t *testing.T) {
	svc, store := newTestTableService()
	store.data[tableMetadataKey("db", "t")] = &fakeCatalogRecord{value: []byte("not json"), modRevision: 1}

	err := svc.DropIndex(context.Background(), "db", "t", "idx_x")
	var metaErr *TableMetadataError
	require.ErrorAs(t, err, &metaErr)
}

func TestTableService_DropIndex_TableNotFound(t *testing.T) {
	svc, _ := newTestTableService()
	err := svc.DropIndex(context.Background(), "db", "missing", "idx_x")
	var e *TableNotFoundError
	assert.ErrorAs(t, err, &e)
}

func TestTableService_DropIndex_RetryExhausted(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	require.NoError(t, svc.CreateIndex(ctx, &CreateTableIndexRequest{
		Database: "db", Table: "t",
		Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
	}))

	store.forceConflicts = 1000
	err = svc.DropIndex(ctx, "db", "t", "idx_email")
	var ddlConflict *TableDDLConflictError
	require.ErrorAs(t, err, &ddlConflict)
}

// ---------------------------------------------------------------------------
// Miscellaneous helpers coverage
// ---------------------------------------------------------------------------

func TestIsSupportedColumnType(t *testing.T) {
	assert.True(t, isSupportedColumnType("VARCHAR(255)"))
	assert.True(t, isSupportedColumnType("bigint unsigned"))
	assert.False(t, isSupportedColumnType(""))
	assert.False(t, isSupportedColumnType("NOPE"))
}

func TestValidateIdentifier(t *testing.T) {
	assert.NoError(t, validateIdentifier("table", "valid_name_1"))
	assert.Error(t, validateIdentifier("table", ""))
	assert.Error(t, validateIdentifier("table", "__reserved"))
	assert.Error(t, validateIdentifier("table", "1abc"))
	assert.Error(t, validateIdentifier("table", "bad name"))
}

// ---------------------------------------------------------------------------
// Key layout (go-qrdb#547): tables created by v0.20.0 through v0.21.0
// ---------------------------------------------------------------------------

// putLegacyTable writes a catalog record shaped exactly as v0.20.0 through
// v0.21.0 wrote one: no layout, no index incarnations, indexes identified
// only by name.
func putLegacyTable(t *testing.T, store *fakeCatalogStore) {
	t.Helper()
	legacy := `{"table_id":7,"name":"users","database":"shop",` +
		`"columns":[{"name":"id","type":"BIGINT UNSIGNED","nullable":false},` +
		`{"name":"email","type":"VARCHAR(255)","nullable":true},{"name":"created_at","type":"DATETIME","nullable":true}],` +
		`"primary_key":[{"name":"id"}],` +
		`"indexes":[{"name":"idx_email","columns":[{"name":"email"}]},{"name":"idx_created","columns":[{"name":"created_at"}]}],` +
		`"schema_version":1}`
	store.put(tableMetadataKey("shop", "users"), []byte(legacy))
}

func requireLayoutUnsupported(t *testing.T, err error) {
	t.Helper()
	var unsupported *TableLayoutUnsupportedError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "users", unsupported.Name)
	assert.Equal(t, uint64(7), unsupported.TableID)
	assert.Equal(t, uint32(0), unsupported.Layout)
	assert.Contains(t, unsupported.Error(), "re-create")
}

// TestTableService_LegacyLayout_IsRefusedNeverRead proves a table whose
// record predates the generation and incarnation layout is refused by every
// operation that would address its keys -- never opened as an empty table,
// and never with its two indexes sharing incarnation 0's keyspace.
func TestTableService_LegacyLayout_IsRefusedNeverRead(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	ops := map[string]func(svc *TableService) error{
		"GetTable": func(svc *TableService) error { _, err := svc.GetTable(ctx, "shop", "users"); return err },
		"TruncateTable": func(svc *TableService) error {
			_, err := svc.TruncateTable(ctx, "shop", "users", true)
			return err
		},
		"CreateIndex": func(svc *TableService) error {
			return svc.CreateIndex(ctx, &CreateTableIndexRequest{Database: "shop", Table: "users",
				Index: TableIndex{Name: "idx_x", Columns: []IndexCol{{Name: "email"}}}})
		},
		"DropIndex": func(svc *TableService) error { return svc.DropIndex(ctx, "shop", "users", "idx_email") },
		"RenameIndex": func(svc *TableService) error {
			return svc.RenameIndex(ctx, &RenameTableIndexRequest{Database: "shop", Table: "users", From: "idx_email", To: "idx_x"})
		},
		"AlterTable": func(svc *TableService) error {
			_, err := svc.AlterTable(ctx, &AlterTableRequest{Database: "shop", Name: "users",
				Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "extra", Type: "INT", Nullable: true}}}})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestTableService()
			putLegacyTable(t, store)
			before := store.executeCallsSnapshot()
			requireLayoutUnsupported(t, op(svc))
			assert.Equal(t, before, store.executeCallsSnapshot(), "a refused operation proposes nothing")
		})
	}
}

// TestTableService_LegacyLayout_CanBeDroppedAndRecreated proves the remedy
// the error names works: the legacy table drops, and the name is then free
// for a table created under the current layout.
func TestTableService_LegacyLayout_CanBeDroppedAndRecreated(t *testing.T) {
	svc, store := newTestTableService()
	putLegacyTable(t, store)
	ctx := context.Background()

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "shop", Name: "users"}))
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "shop", Name: "users", Columns: testColumns()})
	require.NoError(t, err)
	assert.Equal(t, TableLayoutCurrent, def.Layout)
	got, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	assert.Equal(t, TableLayoutCurrent, got.Layout)
}

// TestLegacyTableKeyPrefixes_CoverTheOldLayoutOnly pins the prefixes DROP
// TABLE reclaims for a legacy table: its old rows and index entries, and
// nothing of another table or of the current layout.
func TestLegacyTableKeyPrefixes_CoverTheOldLayoutOnly(t *testing.T) {
	prefixes := LegacyTableKeyPrefixes("shop", 1)
	require.Equal(t, []string{"__tbl:shop:1/", "__idx:shop:1:"}, prefixes)
	covered := func(key string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				return true
			}
		}
		return false
	}
	assert.True(t, covered("__tbl:shop:1/row"))
	assert.True(t, covered("__idx:shop:1:idx_email/entry"))
	assert.False(t, covered(TableDataPrefix("shop", 10, 0)+"row"), "table 10")
	assert.False(t, covered("__tbl:shop:10/row"), "table 10, legacy")
	assert.False(t, covered("__idx:shop:10:idx/entry"), "table 10, legacy")
	assert.False(t, covered(TableDataPrefix("shop", 1, 0)+"row"), "current-layout rows are not legacy")
}

// numberedSQLError is an engine error carrying a MySQL error number, the
// shape the SQL engine renders a refusal in.
type numberedSQLError struct{ n int }

func (e numberedSQLError) Error() string { return "sql error" }
func (e numberedSQLError) Number() int   { return e.n }

// TestIsTableLayoutUnsupported recognizes the refusal in both shapes it
// reaches a transport in -- typed from the catalog, numbered from the SQL
// engine, wrapped or not -- and nothing else.
func TestIsTableLayoutUnsupported(t *testing.T) {
	typed := &TableLayoutUnsupportedError{Database: "shop", Name: "users"}
	assert.True(t, IsTableLayoutUnsupported(typed))
	assert.True(t, IsTableLayoutUnsupported(&ReadError{Err: typed}))
	assert.True(t, IsTableLayoutUnsupported(numberedSQLError{n: TableLayoutUnsupportedSQLCode}))
	assert.True(t, IsTableLayoutUnsupported(&SQLExecutionError{Err: numberedSQLError{n: TableLayoutUnsupportedSQLCode}}))
	assert.False(t, IsTableLayoutUnsupported(numberedSQLError{n: 1105}))
	assert.False(t, IsTableLayoutUnsupported(errors.New("boom")))
	assert.False(t, IsTableLayoutUnsupported(nil))
}
