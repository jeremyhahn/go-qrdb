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
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func (f *fakeCatalogStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.injectGetErr != nil {
		err := f.injectGetErr
		f.injectGetErr = nil
		return nil, err
	}

	rec, ok := f.data[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return cloneBytes(rec.value), nil
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

	rec, ok := f.data[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return &KeyValue{
		Key:            []byte(key),
		Value:          cloneBytes(rec.value),
		ModRevision:    rec.modRevision,
		CreateRevision: rec.createRevision,
		Version:        rec.version,
	}, nil
}

func (f *fakeCatalogStore) Execute(_ context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
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
func newTestTableService() (*TableService, *fakeCatalogStore) {
	store := newFakeCatalogStore()
	seq := NewSequenceService(store, store)
	return NewTableService(store, store, store, seq), store
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
	assert.Equal(t, 4, store.executeCallsSnapshot()-before, "3 forced conflicts + 1 successful attempt")
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

	dataKey := tableDataPrefixFor("db", def.TableID) + "1"
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
