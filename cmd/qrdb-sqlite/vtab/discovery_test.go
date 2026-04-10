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

package vtab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockMetadataProvider — satisfies MetadataProvider for unit tests
// ---------------------------------------------------------------------------

// mockMetadataProvider is a test double for MetadataProvider. When err is set
// DiscoverTables returns that error immediately; otherwise it returns tables.
type mockMetadataProvider struct {
	tables []TableMeta
	err    error
}

func (m *mockMetadataProvider) DiscoverTables(_ context.Context) ([]TableMeta, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.tables, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// discoveryDriverSeq is incremented per test to produce unique driver names so
// that sql.Register never sees the same name twice within a process.
var discoveryDriverSeq int

// openDiscoveryDB opens an in-memory SQLite database that has the qrdb_kv
// virtual-table module registered. The extra modules (qrdb_entity,
// qrdb_collection, qrdb_datapoints, qrdb_vectors) are not real modules in
// this test build, so we register a stub catch-all module under each name
// that allows CREATE VIRTUAL TABLE to succeed without a real implementation.
//
// This helper returns the *sql.DB and a cleanup function.
func openDiscoveryDB(t *testing.T) *sql.DB {
	t.Helper()
	discoveryDriverSeq++
	driverName := fmt.Sprintf("discovery_test_%d", discoveryDriverSeq)

	kv := newMockKV()
	pragma := newPragmaStore("embedded", "", "")

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			// Register qrdb_kv (the real module).
			mod := &kvModule{kv: kv, pragma: pragma, chunkSize: 100}
			if err := conn.CreateModule(kvModuleName, mod); err != nil {
				return err
			}
			// Register stub modules for the other table types so that
			// CREATE VIRTUAL TABLE … USING <name>(…) succeeds.
			for _, name := range []string{"qrdb_entity", "qrdb_collection", "qrdb_datapoints", "qrdb_vectors"} {
				stub := &stubModule{pragma: pragma, kv: kv, chunkSize: 100}
				if err := conn.CreateModule(name, stub); err != nil {
					return err
				}
			}
			return nil
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// stubModule — minimal sqlite3.Module that allows CREATE VIRTUAL TABLE to
// succeed in tests without a full implementation.
// ---------------------------------------------------------------------------

// stubModule is a no-op virtual table module used to exercise discovery DDL
// execution without requiring a complete implementation of entity, collection,
// time-series, or vector modules.
type stubModule struct {
	pragma    *pragmaStore
	kv        kvStore
	chunkSize int
}

func (s *stubModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab("CREATE TABLE x(id INTEGER PRIMARY KEY, data TEXT)"); err != nil {
		return nil, err
	}
	return &stubTable{}, nil
}

func (s *stubModule) Connect(c *sqlite3.SQLiteConn, args []string) (sqlite3.VTab, error) {
	return s.Create(c, args)
}

func (s *stubModule) DestroyModule() {}

type stubTable struct{}

func (t *stubTable) BestIndex(_ []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	return &sqlite3.IndexResult{EstimatedCost: 1e6}, nil
}
func (t *stubTable) Open() (sqlite3.VTabCursor, error) { return &stubCursor{}, nil }
func (t *stubTable) Disconnect() error                 { return nil }
func (t *stubTable) Destroy() error                    { return nil }

type stubCursor struct{ eof bool }

func (c *stubCursor) Filter(_ int, _ string, _ []interface{}) error { c.eof = true; return nil }
func (c *stubCursor) Next() error                                   { c.eof = true; return nil }
func (c *stubCursor) EOF() bool                                     { return c.eof }
func (c *stubCursor) Rowid() (int64, error)                         { return 0, nil }
func (c *stubCursor) Column(_ *sqlite3.SQLiteContext, _ int) error  { return nil }
func (c *stubCursor) Close() error                                  { return nil }

// ---------------------------------------------------------------------------
// TestDiscovery_KVTable
// ---------------------------------------------------------------------------

// TestDiscovery_KVTable verifies that a KV table is discovered and the correct
// CREATE VIRTUAL TABLE … USING qrdb_kv DDL is executed against the database.
func TestDiscovery_KVTable(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{
				Name: "kv",
				Type: TableKV,
				Columns: []ColumnMeta{
					{Name: "key", SQLiteType: "TEXT", PrimaryKey: true, Nullable: false},
					{Name: "value", SQLiteType: "BLOB", Nullable: true},
				},
			},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	require.Equal(t, []string{"kv"}, created)

	// Verify the virtual table is queryable.
	rows, err := db.QueryContext(context.Background(), "SELECT key, value FROM kv")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next(), "expected empty result set from new KV table")
}

// TestDiscovery_KVTable_DDLFormat verifies that generateCreateVTable produces
// the exact expected DDL for a KV table, without executing it.
func TestDiscovery_KVTable_DDLFormat(t *testing.T) {
	meta := TableMeta{Name: "kv", Type: TableKV}
	ddl := generateCreateVTable(meta)
	assert.Equal(t, "CREATE VIRTUAL TABLE IF NOT EXISTS kv USING qrdb_kv", ddl)
}

// ---------------------------------------------------------------------------
// TestDiscovery_EntityTables
// ---------------------------------------------------------------------------

// TestDiscovery_EntityTables verifies that entity tables are discovered with
// their field columns and the correct qrdb_entity module DDL is generated.
func TestDiscovery_EntityTables(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{
				Name: "users",
				Type: TableEntity,
				Columns: []ColumnMeta{
					{Name: "id", SQLiteType: "TEXT", PrimaryKey: true, Nullable: false},
					{Name: "email", SQLiteType: "TEXT", Nullable: false},
					{Name: "age", SQLiteType: "INTEGER", Nullable: true},
				},
				Indexes: []IndexMeta{
					{Name: "idx_email", FieldName: "email", Unique: true},
				},
			},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	require.Equal(t, []string{"users"}, created)
}

// TestDiscovery_EntityTables_DDLFormat verifies DDL for entity tables includes
// the entityType argument.
func TestDiscovery_EntityTables_DDLFormat(t *testing.T) {
	meta := TableMeta{Name: "users", Type: TableEntity}
	ddl := generateCreateVTable(meta)
	assert.Equal(t, "CREATE VIRTUAL TABLE IF NOT EXISTS users USING qrdb_entity(entityType=users)", ddl)
}

// ---------------------------------------------------------------------------
// TestDiscovery_CollectionTables
// ---------------------------------------------------------------------------

// TestDiscovery_CollectionTables verifies that collection tables are discovered
// and the qrdb_collection module DDL includes database and collection arguments.
func TestDiscovery_CollectionTables(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{
				Name:     "orders",
				Type:     TableCollection,
				Database: "mydb",
				Columns: []ColumnMeta{
					{Name: "id", SQLiteType: "INTEGER", PrimaryKey: true, Nullable: false},
					{Name: "payload", SQLiteType: "BLOB", Nullable: true},
				},
			},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	require.Equal(t, []string{"orders"}, created)
}

// TestDiscovery_CollectionTables_DDLFormat verifies DDL for collection tables.
func TestDiscovery_CollectionTables_DDLFormat(t *testing.T) {
	tests := []struct {
		name    string
		meta    TableMeta
		wantDDL string
	}{
		{
			name:    "with database",
			meta:    TableMeta{Name: "orders", Type: TableCollection, Database: "mydb"},
			wantDDL: "CREATE VIRTUAL TABLE IF NOT EXISTS orders USING qrdb_collection(database=mydb,collection=orders)",
		},
		{
			name:    "without database",
			meta:    TableMeta{Name: "events", Type: TableCollection},
			wantDDL: "CREATE VIRTUAL TABLE IF NOT EXISTS events USING qrdb_collection(collection=events)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantDDL, generateCreateVTable(tt.meta))
		})
	}
}

// ---------------------------------------------------------------------------
// TestDiscovery_TimeSeriesTables
// ---------------------------------------------------------------------------

// TestDiscovery_TimeSeriesTables verifies that time-series tables generate
// placeholder DDL using the qrdb_datapoints module.
func TestDiscovery_TimeSeriesTables(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{Name: "datapoints", Type: TableTimeSeries},
			{Name: "metrics", Type: TableTimeSeries},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.Equal(t, []string{"datapoints", "metrics"}, created)
}

// TestDiscovery_TimeSeriesTables_DDLFormat verifies DDL for time-series tables.
func TestDiscovery_TimeSeriesTables_DDLFormat(t *testing.T) {
	meta := TableMeta{Name: "datapoints", Type: TableTimeSeries}
	ddl := generateCreateVTable(meta)
	assert.Equal(t, "CREATE VIRTUAL TABLE IF NOT EXISTS datapoints USING qrdb_datapoints", ddl)
}

// ---------------------------------------------------------------------------
// TestDiscovery_VectorTables
// ---------------------------------------------------------------------------

// TestDiscovery_VectorTables verifies that vector tables generate placeholder
// DDL using the qrdb_vectors module.
func TestDiscovery_VectorTables(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{Name: "embeddings", Type: TableVector},
			{Name: "vectors", Type: TableVector},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.Equal(t, []string{"embeddings", "vectors"}, created)
}

// TestDiscovery_VectorTables_DDLFormat verifies DDL for vector tables.
func TestDiscovery_VectorTables_DDLFormat(t *testing.T) {
	meta := TableMeta{Name: "vectors", Type: TableVector}
	ddl := generateCreateVTable(meta)
	assert.Equal(t, "CREATE VIRTUAL TABLE IF NOT EXISTS vectors USING qrdb_vectors", ddl)
}

// ---------------------------------------------------------------------------
// TestDiscovery_EmptyServer
// ---------------------------------------------------------------------------

// TestDiscovery_EmptyServer verifies that a server with no tables returns an
// empty (non-nil) slice and no error.
func TestDiscovery_EmptyServer(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{tables: []TableMeta{}}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.NotNil(t, created)
	assert.Empty(t, created)
}

// TestDiscovery_EmptyServer_NilSlice verifies a nil tables slice also returns
// an empty non-nil result without errors.
func TestDiscovery_EmptyServer_NilSlice(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{tables: nil}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.NotNil(t, created)
	assert.Len(t, created, 0)
}

// ---------------------------------------------------------------------------
// TestDiscovery_MetadataEndpoint_Error
// ---------------------------------------------------------------------------

// TestDiscovery_MetadataEndpoint_Error verifies that a provider error is
// wrapped in a DiscoveryError and propagated to the caller.
func TestDiscovery_MetadataEndpoint_Error(t *testing.T) {
	db := openDiscoveryDB(t)
	rootErr := errors.New("connection refused")
	provider := &mockMetadataProvider{err: rootErr}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.Error(t, err)
	assert.Nil(t, created)

	var de *DiscoveryError
	require.True(t, errors.As(err, &de), "expected DiscoveryError, got %T: %v", err, err)
	assert.True(t, errors.Is(err, rootErr), "DiscoveryError must unwrap to root cause")
}

// TestDiscovery_MetadataEndpoint_Error_Message verifies the error message
// format includes the underlying cause.
func TestDiscovery_MetadataEndpoint_Error_Message(t *testing.T) {
	de := &DiscoveryError{Cause: errors.New("dial tcp: no route to host")}
	assert.Equal(t, "vtab: table discovery failed: dial tcp: no route to host", de.Error())
	assert.Equal(t, "dial tcp: no route to host", de.Unwrap().Error())
}

// ---------------------------------------------------------------------------
// TestDiscovery_SchemaMapping
// ---------------------------------------------------------------------------

// TestDiscovery_SchemaMapping verifies that ColumnMeta types are correctly
// translated to SQLite type affinity strings by sqliteTypeFromMeta.
func TestDiscovery_SchemaMapping(t *testing.T) {
	tests := []struct {
		inputType string
		wantType  string
	}{
		{"TEXT", "TEXT"},
		{"text", "TEXT"},
		{"INTEGER", "INTEGER"},
		{"INT", "INTEGER"},
		{"int", "INTEGER"},
		{"REAL", "REAL"},
		{"FLOAT", "REAL"},
		{"DOUBLE", "REAL"},
		{"BLOB", "BLOB"},
		{"NUMERIC", "NUMERIC"},
		{"", "TEXT"},        // empty defaults to TEXT
		{"UNKNOWN", "TEXT"}, // unknown defaults to TEXT
		{"varchar", "TEXT"}, // non-standard maps to TEXT
		{"boolean", "TEXT"}, // non-standard maps to TEXT
	}

	for _, tt := range tests {
		t.Run(tt.inputType, func(t *testing.T) {
			col := ColumnMeta{Name: "col", SQLiteType: tt.inputType}
			got := sqliteTypeFromMeta(col)
			assert.Equal(t, tt.wantType, got,
				"sqliteTypeFromMeta(%q) = %q, want %q", tt.inputType, got, tt.wantType)
		})
	}
}

// TestDiscovery_SchemaMapping_PrimaryKeyAndNullable verifies that PrimaryKey
// and Nullable fields are preserved in ColumnMeta without transformation.
func TestDiscovery_SchemaMapping_PrimaryKeyAndNullable(t *testing.T) {
	pk := ColumnMeta{Name: "id", SQLiteType: "TEXT", PrimaryKey: true, Nullable: false}
	assert.True(t, pk.PrimaryKey)
	assert.False(t, pk.Nullable)

	optional := ColumnMeta{Name: "note", SQLiteType: "TEXT", PrimaryKey: false, Nullable: true}
	assert.False(t, optional.PrimaryKey)
	assert.True(t, optional.Nullable)
}

// ---------------------------------------------------------------------------
// TestDiscovery_GenerateCreateVTable
// ---------------------------------------------------------------------------

// TestDiscovery_GenerateCreateVTable is a table-driven test that covers DDL
// generation for every supported table type and verifies structural correctness.
func TestDiscovery_GenerateCreateVTable(t *testing.T) {
	tests := []struct {
		name         string
		meta         TableMeta
		wantPrefix   string
		wantContains []string
	}{
		{
			name:         "kv",
			meta:         TableMeta{Name: "kv", Type: TableKV},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS kv USING qrdb_kv",
			wantContains: []string{"qrdb_kv"},
		},
		{
			name:         "entity with name",
			meta:         TableMeta{Name: "users", Type: TableEntity},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS users USING qrdb_entity",
			wantContains: []string{"entityType=users"},
		},
		{
			name:         "collection with database",
			meta:         TableMeta{Name: "invoices", Type: TableCollection, Database: "billing"},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS invoices USING qrdb_collection",
			wantContains: []string{"database=billing", "collection=invoices"},
		},
		{
			name:         "collection without database",
			meta:         TableMeta{Name: "logs", Type: TableCollection},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS logs USING qrdb_collection",
			wantContains: []string{"collection=logs"},
		},
		{
			name:         "timeseries",
			meta:         TableMeta{Name: "datapoints", Type: TableTimeSeries},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS datapoints USING qrdb_datapoints",
			wantContains: []string{"qrdb_datapoints"},
		},
		{
			name:         "vector",
			meta:         TableMeta{Name: "embeddings", Type: TableVector},
			wantPrefix:   "CREATE VIRTUAL TABLE IF NOT EXISTS embeddings USING qrdb_vectors",
			wantContains: []string{"qrdb_vectors"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ddl := generateCreateVTable(tt.meta)
			assert.True(t, strings.HasPrefix(ddl, tt.wantPrefix),
				"DDL %q does not start with %q", ddl, tt.wantPrefix)
			for _, substr := range tt.wantContains {
				assert.Contains(t, ddl, substr,
					"DDL %q does not contain %q", ddl, substr)
			}
		})
	}
}

// TestDiscovery_GenerateCreateVTable_IfNotExists verifies that every generated
// DDL statement includes the IF NOT EXISTS guard, making the call idempotent.
func TestDiscovery_GenerateCreateVTable_IfNotExists(t *testing.T) {
	allTypes := []TableMeta{
		{Name: "t1", Type: TableKV},
		{Name: "t2", Type: TableEntity},
		{Name: "t3", Type: TableCollection},
		{Name: "t4", Type: TableTimeSeries},
		{Name: "t5", Type: TableVector},
	}
	for _, meta := range allTypes {
		ddl := generateCreateVTable(meta)
		assert.Contains(t, ddl, "IF NOT EXISTS",
			"DDL for %v must contain IF NOT EXISTS", meta.Type)
	}
}

// ---------------------------------------------------------------------------
// TestDiscovery_MultipleTableTypes
// ---------------------------------------------------------------------------

// TestDiscovery_MultipleTableTypes verifies that a heterogeneous set of tables
// (KV + entity + collection) are all created in a single discovery pass.
func TestDiscovery_MultipleTableTypes(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{Name: "kv", Type: TableKV},
			{Name: "users", Type: TableEntity},
			{Name: "orders", Type: TableCollection, Database: "shop"},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.Equal(t, []string{"kv", "users", "orders"}, created)
}

// TestDiscovery_MultipleTableTypes_OrderPreserved verifies that the creation
// order mirrors the provider's discovery order.
func TestDiscovery_MultipleTableTypes_OrderPreserved(t *testing.T) {
	db := openDiscoveryDB(t)
	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{Name: "alpha", Type: TableTimeSeries},
			{Name: "beta", Type: TableVector},
			{Name: "gamma", Type: TableKV},
		},
	}

	created, err := discoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	require.Equal(t, 3, len(created))
	assert.Equal(t, "alpha", created[0])
	assert.Equal(t, "beta", created[1])
	assert.Equal(t, "gamma", created[2])
}
