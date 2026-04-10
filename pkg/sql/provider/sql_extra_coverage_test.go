// Copyright (c) 2026 Jeremy Hahn
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

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// provider.go — WithCollectionService option func (0%)
// ---------------------------------------------------------------------------

// TestWithCollectionService verifies the functional option sets collectionSvc.
func TestWithCollectionService(t *testing.T) {
	svc := &mockKVService{}
	colSvc := &mockCollectionService{}

	p := NewManagerProvider(nil, svc, WithCollectionService(colSvc))
	if p.collectionSvc == nil {
		t.Fatal("expected collectionSvc to be set via WithCollectionService option")
	}
}

// ---------------------------------------------------------------------------
// provider.go — isAllowed with MySQL / PostgreSQL protocols (80%)
// ---------------------------------------------------------------------------

// TestIsAllowed_MySQL verifies that ProtocolMySQL filters by MySQL flag.
func TestIsAllowed_MySQL(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "mysqldb",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{
			MySQL: true,
		},
	})
	require.NoError(t, err)

	// ProtocolMySQL provider should find the MySQL-enabled database.
	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "mysqldb")
	require.NoError(t, err)
	assert.Equal(t, "mysqldb", db.Name())
}

// TestIsAllowed_MySQL_NotAllowed verifies that a non-MySQL-enabled database
// is filtered out when ProtocolMySQL is active.
func TestIsAllowed_MySQL_NotAllowed(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	// Create database with MySQL=false (default).
	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "nomysql",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	_, err = p.Database(ctx, "nomysql")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

// TestIsAllowed_PostgreSQL verifies ProtocolPostgreSQL filter.
func TestIsAllowed_PostgreSQL(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "pgsqldb",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{
			PostgreSQL: true,
		},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "pgsqldb")
	require.NoError(t, err)
	assert.Equal(t, "pgsqldb", db.Name())
}

// ---------------------------------------------------------------------------
// provider.go — AllDatabases with protocol filter (92.3%)
// ---------------------------------------------------------------------------

// TestAllDatabases_WithProtocolFilter verifies AllDatabases applies the
// protocol filter when protocol != ProtocolAny.
func TestAllDatabases_WithProtocolFilter(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	// Create one MySQL-enabled and one non-MySQL database.
	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "mysql1",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{
			MySQL: true,
		},
	})
	require.NoError(t, err)

	_, err = mgr.Create(database.DatabaseConfig{
		Name:    "nomysql2",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	// Only the MySQL-enabled database should be returned.
	require.Len(t, dbs, 1)
	assert.Equal(t, "mysql1", dbs[0].Name())
}

// ---------------------------------------------------------------------------
// provider.go — GetTableInsensitive collection lookup (85.7%)
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_CollectionFound verifies that GetTableInsensitive
// returns a collection table when the collection service finds a match.
func TestGetTableInsensitive_CollectionFound(t *testing.T) {
	colSvc := &mockCollectionService{
		getInfoFn: func(_ context.Context, _, name string) (*service.CollectionInfo, error) {
			return &service.CollectionInfo{
				Name:   name,
				Schema: map[string]string{"field1": "VARCHAR"},
			}, nil
		},
	}
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}, collectionSvc: colSvc}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "mycollection")
	require.NoError(t, err)
	assert.True(t, found)
	assert.NotNil(t, tbl)
	assert.Equal(t, "mycollection", tbl.Name())
}

// ---------------------------------------------------------------------------
// provider.go — GetTableNames with collection service (80%)
// ---------------------------------------------------------------------------

// TestGetTableNames_WithCollectionService verifies collection names are
// appended when a collection service is configured.
func TestGetTableNames_WithCollectionService(t *testing.T) {
	colSvc := &mockCollectionService{
		listColFn: func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return &service.ListCollectionsResponse{
				Collections: []service.CollectionInfo{
					{Name: "orders"},
					{Name: "products"},
				},
			}, nil
		},
	}
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}, collectionSvc: colSvc}
	ctx := newTestContext()

	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)

	// Should contain both static tables and collection names.
	assert.Contains(t, names, "kv")
	assert.Contains(t, names, "orders")
	assert.Contains(t, names, "products")
}

// ---------------------------------------------------------------------------
// kv_table.go — StatementBegin (0%)
// ---------------------------------------------------------------------------

// TestKVWriter_StatementBegin_ExtraCoverage verifies StatementBegin is a no-op.
func TestKVWriter_StatementBegin_ExtraCoverage(t *testing.T) {
	w := &kvWriter{kvSvc: &mockKVService{}, tenantID: "default"}
	ctx := sqle.NewEmptyContext()
	w.StatementBegin(ctx)
}

// ---------------------------------------------------------------------------
// kv_table.go — LookupPartitions with valid qrdbIndex (50%)
// ---------------------------------------------------------------------------

// TestKVTable_LookupPartitions_WithIndex verifies LookupPartitions uses the
// index service when an IndexLookup contains a qrdbIndex.
func TestKVTable_LookupPartitions_WithIndex(t *testing.T) {
	idxSvc := &entityMockIndexServicer{
		queryFn: func(_ context.Context, _ string, value []byte) ([][]byte, error) {
			return [][]byte{value}, nil
		},
	}

	tbl := NewKVTable(&mockKVService{}, "default", idxSvc)
	ctx := newTestContext()

	qIdx := newQRDBIndex("qrdb", "kv", "key", "string", false)
	lookup := sqle.IndexLookup{
		Index: qIdx,
		Ranges: sqle.MySQLRangeCollection{
			sqle.MySQLRange{sqle.ClosedRangeColumnExpr("k1", "k1", types.LongText)},
		},
	}

	iter, err := tbl.LookupPartitions(ctx, lookup)
	require.NoError(t, err)
	require.NotNil(t, iter)
}

// ---------------------------------------------------------------------------
// entity_table.go — LookupPartitions with valid qrdbIndex (50%)
// ---------------------------------------------------------------------------

// TestEntityTable_LookupPartitions_WithIndex verifies LookupPartitions uses
// the index service.
func TestEntityTable_LookupPartitions_WithIndex(t *testing.T) {
	idxSvc := &entityMockIndexServicer{
		queryFn: func(_ context.Context, _ string, value []byte) ([][]byte, error) {
			return [][]byte{value}, nil
		},
	}

	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "default", reg, idxSvc)
	ctx := newTestContext()

	qIdx := newQRDBIndex("testdb", "users", "name", "string", false)
	lookup := sqle.IndexLookup{
		Index: qIdx,
		Ranges: sqle.MySQLRangeCollection{
			sqle.MySQLRange{sqle.ClosedRangeColumnExpr("alice", "alice", types.LongText)},
		},
	}

	iter, err := tbl.LookupPartitions(ctx, lookup)
	require.NoError(t, err)
	require.NotNil(t, iter)
}

// ---------------------------------------------------------------------------
// collection_table.go — LookupPartitions with valid qrdbIndex (50%)
// ---------------------------------------------------------------------------

// TestCollectionTable_LookupPartitions_WithIndex verifies LookupPartitions
// uses the index service for a collection table.
func TestCollectionTable_LookupPartitions_WithIndex(t *testing.T) {
	idxSvc := &entityMockIndexServicer{
		queryFn: func(_ context.Context, _ string, value []byte) ([][]byte, error) {
			return [][]byte{value}, nil
		},
	}

	colSvc := &mockCollectionService{}
	tbl := NewCollectionTable(colSvc, "qrdb", "orders", map[string]string{"name": "VARCHAR"}, idxSvc)
	ctx := newTestContext()

	qIdx := newQRDBIndex("qrdb", "orders", "name", "string", false)
	lookup := sqle.IndexLookup{
		Index: qIdx,
		Ranges: sqle.MySQLRangeCollection{
			sqle.MySQLRange{sqle.ClosedRangeColumnExpr("order1", "order1", types.LongText)},
		},
	}

	iter, err := tbl.LookupPartitions(ctx, lookup)
	require.NoError(t, err)
	require.NotNil(t, iter)
}

// ---------------------------------------------------------------------------
// entity_table.go — entityToRow when metaByJSON lookup fails (78.9%)
// ---------------------------------------------------------------------------

// TestEntityToRow_MissingMetaField verifies that entityToRow sets nil for
// a fieldOrder entry that is not found in metaByJSON.
func TestEntityToRow_MissingMetaField(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "default", reg, nil)

	// Manually inject an extra field into fieldOrder that has no FieldMeta.
	tbl.fieldOrder = append(tbl.fieldOrder, "nonexistent_json_field")

	e := &testEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Age: 30, Score: 9.5, Active: true}
	row := tbl.entityToRow(e)

	// The last column (for "nonexistent_json_field") should be nil.
	require.NotNil(t, row)
	assert.Nil(t, row[len(row)-1])
}

// ---------------------------------------------------------------------------
// entity_table.go — reflectFieldByGoName when field not valid (75%)
// ---------------------------------------------------------------------------

// TestReflectFieldByGoName_InvalidField verifies that reflectFieldByGoName
// returns nil when the field name is not found in the struct.
func TestReflectFieldByGoName_InvalidField(t *testing.T) {
	type MyStruct struct {
		ID uint64
	}
	rv := reflect.ValueOf(MyStruct{ID: 42})
	result := reflectFieldByGoName(rv, "NonExistentField")
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// entity_table.go — coerceReflectToSQL valid pointer branch (88.9%)
// ---------------------------------------------------------------------------

// TestCoerceReflectToSQL_ValidPointer verifies that a non-nil pointer is
// dereferenced and its value returned.
func TestCoerceReflectToSQL_ValidPointer(t *testing.T) {
	s := "hello"
	fv := reflect.ValueOf(&s)
	result := coerceReflectToSQL(fv)
	assert.Equal(t, "hello", result)
}

// TestCoerceReflectToSQL_StringSliceJSON verifies that a []string (non-[]byte
// slice) is JSON-marshaled to a string.
func TestCoerceReflectToSQL_StringSliceJSON(t *testing.T) {
	fv := reflect.ValueOf([]string{"a", "b", "c"})
	result := coerceReflectToSQL(fv)
	assert.Equal(t, `["a","b","c"]`, result)
}

// ---------------------------------------------------------------------------
// entity_table.go — entitySQLType for Blob type (90%)
// ---------------------------------------------------------------------------

// TestEntitySQLType_Blob verifies the Blob type mapping for []byte fields.
func TestEntitySQLType_Blob(t *testing.T) {
	f := entity.FieldMeta{
		GoType: reflect.TypeOf([]byte{}),
	}
	sqlType := entitySQLType(f)
	assert.NotNil(t, sqlType)
}

// ---------------------------------------------------------------------------
// entity_table.go — Insert with marshal failure (89.7%)
// ---------------------------------------------------------------------------

// TestEntityWriter_Insert_MarshalError verifies Insert returns an error when
// the MarshalFunc fails.
func TestEntityWriter_Insert_MarshalError(t *testing.T) {
	marshalErr := errors.New("marshal failed")
	reg := &entity.Registration{
		EntityType:  "failtable",
		StructType:  reflect.TypeOf(testEntity{}),
		Fields:      testRegistration().Fields,
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "testdb",
		CodecType:   "json",
		NewFunc: func() any {
			return &testEntity{}
		},
		MarshalFunc: func(v any) ([]byte, error) {
			return nil, marshalErr
		},
		UnmarshalFunc: func(data []byte) (any, error) {
			e := &testEntity{}
			return e, json.Unmarshal(data, e)
		},
	}

	w := &entityWriter{
		kvSvc:      &mockKVService{},
		tenantID:   "default",
		reg:        reg,
		fieldOrder: []string{"name", "email", "age", "score", "active"},
	}

	ctx := sqle.NewEmptyContext()
	row := sqle.Row{uint64(1), "alice", "alice@example.com", int64(25), float64(8.5), true}
	err := w.Insert(ctx, row)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// range_translator.go — scanRange error path (94.1%)
// ---------------------------------------------------------------------------

// TestTranslateRanges_ScanRangeError_ClosedClosed verifies that scanRange
// errors are propagated as IndexLookupError for ClosedClosed ranges with
// different bounds.
func TestTranslateRanges_ScanRangeError_ClosedClosed(t *testing.T) {
	scanErr := errors.New("scan unavailable")
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return nil, scanErr
		},
	}

	r := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr("aaa", "zzz", types.LongText),
	}
	_, err := translateRanges(context.Background(), svc, "Name", []sqle.Range{r})
	require.Error(t, err)
	var ie *IndexLookupError
	assert.True(t, errors.As(err, &ie))
}
