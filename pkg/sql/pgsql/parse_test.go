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

//go:build qrdb_pgsql

package pgsql

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql"
	sqltypes "github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/jackc/pgx/v5/pgtype"
	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// newTestParseHandler creates a ParseHandler backed by a mock KV service
// for unit testing. It uses the in-memory mock service to avoid any
// external dependencies.
func newTestParseHandler(t *testing.T) *ParseHandler {
	t.Helper()
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	return NewParseHandler(sqlEngine, translator, "qrdb")
}

func TestNewParseHandler(t *testing.T) {
	h := newTestParseHandler(t)
	require.NotNil(t, h)
	assert.NotNil(t, h.sqlEngine)
	assert.NotNil(t, h.translator)
	assert.Equal(t, "qrdb", h.dbName)
}

func TestHandleParse_EmptyQuery(t *testing.T) {
	h := newTestParseHandler(t)
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: ""})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_WhitespaceOnly(t *testing.T) {
	h := newTestParseHandler(t)
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "   \t\n  "})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_TransactionControlNoops(t *testing.T) {
	h := newTestParseHandler(t)
	noops := []string{
		"BEGIN",
		"COMMIT",
		"ROLLBACK",
		"END",
		"BEGIN;",
		"COMMIT;",
		"begin",
		"commit",
	}

	for _, query := range noops {
		t.Run(query, func(t *testing.T) {
			stmts, err := h.HandleParse(context.Background(), wire.Query{Query: query})
			require.NoError(t, err)
			require.NotNil(t, stmts)
			assert.Len(t, stmts, 1)
		})
	}
}

func TestHandleParse_SessionCommandNoops(t *testing.T) {
	h := newTestParseHandler(t)
	noops := []string{
		"SET client_encoding TO 'UTF8'",
		"SHOW search_path",
		"RESET ALL",
		"DISCARD ALL",
		"DEALLOCATE ALL",
		"LISTEN channel1",
		"UNLISTEN *",
	}

	for _, query := range noops {
		t.Run(query, func(t *testing.T) {
			stmts, err := h.HandleParse(context.Background(), wire.Query{Query: query})
			require.NoError(t, err)
			require.NotNil(t, stmts)
			assert.Len(t, stmts, 1)
		})
	}
}

func TestHandleParse_SelectFromKV(t *testing.T) {
	// Set up mock with data.
	kvSvc := &mockKVService{
		scanData: map[string][]byte{
			"key1": []byte("value1"),
			"key2": []byte("value2"),
		},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "SELECT * FROM kv"})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_InvalidSQL(t *testing.T) {
	h := newTestParseHandler(t)
	_, err := h.HandleParse(context.Background(), wire.Query{Query: "INVALID SYNTAX HERE !@#$"})
	require.Error(t, err)

	var queryErr *QueryError
	assert.ErrorAs(t, err, &queryErr)
}

func TestHandleParse_NonExistentTable(t *testing.T) {
	h := newTestParseHandler(t)
	_, err := h.HandleParse(context.Background(), wire.Query{Query: "SELECT * FROM nonexistent"})
	require.Error(t, err)

	var queryErr *QueryError
	assert.ErrorAs(t, err, &queryErr)
}

func TestHandleParse_WithIdentity(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	identity := &auth.Identity{
		Subject:  "test-user",
		TenantID: "tenant-1",
	}
	ctx := context.WithValue(context.Background(), identityContextKey{}, identity)

	stmts, err := h.HandleParse(ctx, wire.Query{Query: "SELECT * FROM kv"})
	require.NoError(t, err)
	require.NotNil(t, stmts)
}

func TestBuildSQLContext(t *testing.T) {
	h := newTestParseHandler(t)

	t.Run("without identity", func(t *testing.T) {
		sqlCtx := h.buildSQLContext(context.Background())
		require.NotNil(t, sqlCtx)
		assert.Equal(t, "qrdb", sqlCtx.GetCurrentDatabase())
	})

	t.Run("with identity", func(t *testing.T) {
		identity := &auth.Identity{
			Subject:  "admin",
			TenantID: "my-tenant",
		}
		ctx := context.WithValue(context.Background(), identityContextKey{}, identity)
		sqlCtx := h.buildSQLContext(ctx)
		require.NotNil(t, sqlCtx)
		assert.Equal(t, "qrdb", sqlCtx.GetCurrentDatabase())

		// Verify the session carries the identity.
		sess, ok := sqlCtx.Session.(*provider.QRDBSession)
		require.True(t, ok)
		assert.Equal(t, "my-tenant", sess.TenantID())
		assert.Equal(t, "admin", sess.Identity().Subject)
	})
}

func TestSchemaToColumns(t *testing.T) {
	t.Run("empty schema", func(t *testing.T) {
		columns := schemaToColumns(sql.NewContext(context.Background()), sql.Schema{})
		assert.Empty(t, columns)
	})

	// go-mysql-server resolves a TEXT column's declared width against the
	// session's result character set, so every TEXT variant narrower than
	// LONGTEXT dereferences the context. PostgreSQL's own `text` translates
	// to LONGTEXT and takes the branch that needs none, but the engine is
	// shared: a table created over the MySQL wire with a plain TEXT column is
	// read back over this one, and a context-free conversion crashed the
	// connection goroutine on it.
	t.Run("a TEXT column resolves its width", func(t *testing.T) {
		sqlCtx := sql.NewContext(context.Background())
		schema := sql.Schema{
			{Name: "body", Type: sqltypes.Text},
			{Name: "note", Type: sqltypes.TinyText},
			{Name: "long", Type: sqltypes.LongText},
		}

		columns := schemaToColumns(sqlCtx, schema)

		require.Len(t, columns, 3)
		for _, column := range columns {
			assert.NotZero(t, column.Oid, "every column must describe a type OID")
		}
	})

	t.Run("non-empty schema", func(t *testing.T) {
		// Create a simple schema matching the KV table.
		kvSvc := &mockKVService{}
		dbProvider := provider.NewProvider("qrdb", kvSvc)
		sqlEngine := engine.New(engine.Config{Provider: dbProvider})
		sqlCtx := sql.NewContext(context.Background())
		sqlCtx.SetCurrentDatabase("qrdb")

		schema, _, _, err := sqlEngine.Query(sqlCtx, "SELECT * FROM kv LIMIT 0")
		require.NoError(t, err)

		columns := schemaToColumns(sqlCtx, schema)
		assert.Len(t, columns, 2)
		assert.Equal(t, "key", columns[0].Name)
		assert.Equal(t, "value", columns[1].Name)
	})
}

func TestBuildCompleteTag(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		rows     int
		expected string
	}{
		{"SELECT with rows", "SELECT * FROM kv", 5, "SELECT 5"},
		{"SELECT zero rows", "SELECT * FROM kv WHERE key = 'x'", 0, "SELECT 0"},
		{"INSERT", "INSERT INTO kv (key, value) VALUES ('k', 'v')", 1, "INSERT 0 1"},
		{"UPDATE", "UPDATE kv SET value = 'new' WHERE key = 'k'", 3, "UPDATE 3"},
		{"DELETE", "DELETE FROM kv WHERE key = 'k'", 2, "DELETE 2"},
		{"unknown command", "CREATE TABLE foo (id INT)", 0, "OK"},
		{"lowercase select", "select * from kv", 10, "SELECT 10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag := buildCompleteTag(tt.query, tt.rows)
			assert.Equal(t, tt.expected, tag)
		})
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		oid      uint32
		expected any
	}{
		{"nil", nil, DefaultOID, nil},
		{"byte slice", []byte("hello"), DefaultOID, "hello"},
		{"string", "world", DefaultOID, "world"},
		{"int", 42, DefaultOID, 42},
		{"float", 3.14, pgtype.Float8OID, 3.14},
		{"bool", true, pgtype.BoolOID, true},

		// A column DESCRIBED as bool must carry a Go bool, whatever
		// go-mysql-server produced for it: pgx refuses to encode anything
		// else under BoolOID and fails the whole statement.
		{"stored tinyint false", int8(0), pgtype.BoolOID, false},
		{"stored tinyint true", int8(1), pgtype.BoolOID, true},
		{"widened comparison", int64(1), pgtype.BoolOID, true},

		// An integer under an integer declaration is left exactly alone --
		// the coercion is keyed on the declaration, not on the Go type.
		{"tinyint under int8 oid", int8(0), pgtype.Int8OID, int8(0)},
		{"int64 under int8 oid", int64(7), pgtype.Int8OID, int64(7)},

		// A byte slice is text-encoded regardless of declaration, which is
		// why BLOB/VARBINARY are declared TextOID in the first place.
		{"byte slice under bool oid", []byte("raw"), pgtype.BoolOID, "raw"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatValue(tt.input, tt.oid)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNoopStatement(t *testing.T) {
	h := newTestParseHandler(t)

	t.Run("transaction control is NOT a no-op", func(t *testing.T) {
		// BEGIN/COMMIT/ROLLBACK/END now route through to the engine (see
		// QRDBSession's sql.TransactionSession implementation) instead of
		// being silently swallowed as no-ops.
		assert.False(t, h.isNoopStatement("BEGIN"))
		assert.False(t, h.isNoopStatement("COMMIT"))
		assert.False(t, h.isNoopStatement("ROLLBACK"))
		assert.False(t, h.isNoopStatement("END"))
		assert.False(t, h.isNoopStatement("BEGIN;"))
		assert.False(t, h.isNoopStatement("COMMIT; "))
	})

	t.Run("prefix matches", func(t *testing.T) {
		assert.True(t, h.isNoopStatement("SET CLIENT_ENCODING TO 'UTF8'"))
		assert.True(t, h.isNoopStatement("SHOW SEARCH_PATH"))
		assert.True(t, h.isNoopStatement("RESET ALL"))
		assert.True(t, h.isNoopStatement("DISCARD ALL"))
		assert.True(t, h.isNoopStatement("DEALLOCATE ALL"))
		assert.True(t, h.isNoopStatement("LISTEN CHANNEL1"))
		assert.True(t, h.isNoopStatement("UNLISTEN *"))
	})

	t.Run("non-noop statements", func(t *testing.T) {
		assert.False(t, h.isNoopStatement("SELECT * FROM KV"))
		assert.False(t, h.isNoopStatement("INSERT INTO KV (KEY, VALUE) VALUES ('K', 'V')"))
		assert.False(t, h.isNoopStatement("UPDATE KV SET VALUE = 'NEW'"))
		assert.False(t, h.isNoopStatement("DELETE FROM KV"))
	})
}

// mockKVService is a minimal KV service mock for testing the parse handler
// without requiring a Raft cluster.
type mockKVService struct {
	scanData  map[string][]byte
	getData   map[string][]byte
	putErr    error
	getErr    error
	deleteErr error
	listKeys  []string
	listErr   error
	scanErr   error
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	if m.scanData != nil {
		m.scanData[key] = value
	}
	return nil
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if m.putErr != nil {
		return false, m.putErr
	}
	if m.scanData == nil {
		return true, nil
	}
	if _, exists := m.scanData[key]; exists {
		return false, nil
	}
	m.scanData[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.scanData[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.scanData, key)
	return true, nil
}

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.getData != nil {
		if v, ok := m.getData[key]; ok {
			return v, nil
		}
	}
	return nil, nil
}

func (m *mockKVService) Delete(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *mockKVService) List(_ context.Context, _ string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listKeys, nil
}

func (m *mockKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	return m.scanData, nil
}

func (m *mockKVService) GetStale(_ context.Context, key string) ([]byte, error) {
	return m.Get(context.Background(), key)
}

func (m *mockKVService) ListStale(_ context.Context, prefix string) ([]string, error) {
	return m.List(context.Background(), prefix)
}

func (m *mockKVService) ScanStale(_ context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(context.Background(), prefix)
}

func (m *mockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	return m.Put(context.Background(), key, value)
}

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	return m.Delete(context.Background(), key)
}

func (m *mockKVService) GetWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(context.Background(), key)
}

func (m *mockKVService) ListWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(context.Background(), prefix)
}

func (m *mockKVService) ScanWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(context.Background(), prefix)
}

func (m *mockKVService) ExistsWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := m.Get(context.Background(), key)
	if err != nil {
		return false, err
	}
	return v != nil, nil
}

func TestEmptyResultFn_Success(t *testing.T) {
	writer := &mockDataWriter{}
	err := emptyResultFn(context.Background(), writer, nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", writer.completeTag)
}

func TestEmptyResultFn_WriterError(t *testing.T) {
	cause := errors.New("complete failed")
	writer := &mockDataWriter{completeErr: cause}
	err := emptyResultFn(context.Background(), writer, nil)
	require.Error(t, err)
	assert.Equal(t, cause, err)
}

func TestCollectRows_IterNextError(t *testing.T) {
	// Create a mock iterator that returns one row successfully, then
	// fails on the second Next() call to exercise the error recovery
	// path at line 192-194 (close + return error).
	iterErr := errors.New("storage read failed")
	iter := &mockRowIter{
		rows: []sql.Row{{"key1", []byte("val1")}},
		err:  iterErr,
	}
	sqlCtx := sql.NewContext(context.Background())

	rows, err := collectRows(sqlCtx, nil, iter)
	require.Error(t, err)
	assert.Equal(t, iterErr, err)
	assert.Nil(t, rows)
	assert.True(t, iter.closed, "iterator should be closed after error")
}

func TestCollectRows_IterNextErrorImmediate(t *testing.T) {
	// Iterator fails on the very first Next() call.
	iterErr := errors.New("iterator init failed")
	iter := &mockRowIter{
		rows: nil,
		err:  iterErr,
	}
	sqlCtx := sql.NewContext(context.Background())

	rows, err := collectRows(sqlCtx, nil, iter)
	require.Error(t, err)
	assert.Equal(t, iterErr, err)
	assert.Nil(t, rows)
	assert.True(t, iter.closed)
}

// mockRowIter implements sql.RowIter for testing collectRows error paths.
// It returns all configured rows in order, then returns the configured
// error (if non-nil) or io.EOF.
type mockRowIter struct {
	rows   []sql.Row
	err    error
	idx    int
	closed bool
}

func (m *mockRowIter) Next(_ *sql.Context) (sql.Row, error) {
	if m.idx < len(m.rows) {
		row := m.rows[m.idx]
		m.idx++
		return row, nil
	}
	if m.err != nil {
		return nil, m.err
	}
	return nil, io.EOF
}

func (m *mockRowIter) Close(_ *sql.Context) error {
	m.closed = true
	return nil
}

func TestCollectRows_Empty(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter()
	result, err := collectRows(sqlCtx, nil, iter)
	require.NoError(t, err)
	assert.False(t, result.isDML)
	assert.Empty(t, result.rows)
}

func TestCollectRows_WithData(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(
		sql.Row{"key1", []byte("value1")},
		sql.Row{"key2", []byte("value2")},
	)
	result, err := collectRows(sqlCtx, nil, iter)
	require.NoError(t, err)
	assert.False(t, result.isDML)
	assert.Len(t, result.rows, 2)
	assert.Equal(t, "key1", result.rows[0][0])
	assert.Equal(t, "value1", result.rows[0][1])
	assert.Equal(t, "key2", result.rows[1][0])
	assert.Equal(t, "value2", result.rows[1][1])
}

func TestCollectRows_NilValues(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(
		sql.Row{"key1", nil},
	)
	result, err := collectRows(sqlCtx, nil, iter)
	require.NoError(t, err)
	assert.Len(t, result.rows, 1)
	assert.Nil(t, result.rows[0][1])
}

func TestCollectRows_OkResult(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(
		sql.Row{sqltypes.OkResult{RowsAffected: 5}},
	)
	result, err := collectRows(sqlCtx, nil, iter)
	require.NoError(t, err)
	assert.True(t, result.isDML)
	assert.Equal(t, uint64(5), result.rowsAffected)
	assert.Empty(t, result.rows)
}

func TestWriteRows_Success(t *testing.T) {
	writer := &mockDataWriter{}
	result := &queryResult{
		rows: [][]any{
			{"key1", "value1"},
			{"key2", "value2"},
		},
	}
	err := writeRows(writer, result, "SELECT * FROM kv")
	require.NoError(t, err)
	assert.Equal(t, 2, writer.rowCount)
	assert.Equal(t, "SELECT 2", writer.completeTag)
}

func TestWriteRows_Empty(t *testing.T) {
	writer := &mockDataWriter{}
	result := &queryResult{}
	err := writeRows(writer, result, "SELECT * FROM kv")
	require.NoError(t, err)
	assert.Equal(t, 0, writer.rowCount)
	assert.Equal(t, "SELECT 0", writer.completeTag)
}

func TestWriteRows_DML(t *testing.T) {
	writer := &mockDataWriter{}
	result := &queryResult{isDML: true, rowsAffected: 3}
	err := writeRows(writer, result, "INSERT INTO kv (key, value) VALUES ($1, $2)")
	require.NoError(t, err)
	assert.Equal(t, 0, writer.rowCount)
	assert.Equal(t, "INSERT 0 3", writer.completeTag)
}

func TestWriteRows_RowError(t *testing.T) {
	cause := errors.New("write failed")
	writer := &mockDataWriter{rowErr: cause}
	result := &queryResult{rows: [][]any{{"key1", "value1"}}}
	err := writeRows(writer, result, "SELECT * FROM kv")
	require.Error(t, err)
	assert.Equal(t, cause, err)
}

func TestHandleParse_InsertAndSelect(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	// Insert a row.
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "INSERT INTO kv (`key`, `value`) VALUES ('test-key', 'test-value')"})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_Delete(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{
			"key1": []byte("value1"),
		},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "DELETE FROM kv WHERE `key` = 'key1'"})
	require.NoError(t, err)
	require.NotNil(t, stmts)
}

func TestHandleParse_PGSyntaxTranslation(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	// PG-style double-quoted identifier should be translated.
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: `SELECT "key" FROM kv`})
	require.NoError(t, err)
	require.NotNil(t, stmts)
}

// mockDataWriter implements a minimal DataWriter for testing writeRows
// and emptyResultFn.
type mockDataWriter struct {
	rowCount    int
	completeTag string
	rowErr      error
	completeErr error
}

func (m *mockDataWriter) Row(_ []any) error {
	if m.rowErr != nil {
		return m.rowErr
	}
	m.rowCount++
	return nil
}

func (m *mockDataWriter) Limit() uint32   { return 0 }
func (m *mockDataWriter) Written() uint32 { return uint32(m.rowCount) }

func (m *mockDataWriter) Empty() error { return nil }

func (m *mockDataWriter) Columns() wire.Columns { return nil }

func (m *mockDataWriter) Complete(desc string) error {
	if m.completeErr != nil {
		return m.completeErr
	}
	m.completeTag = desc
	return nil
}

func (m *mockDataWriter) CopyIn(_ wire.FormatCode) (*wire.CopyReader, error) {
	return nil, nil
}

func (m *mockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return &service.PutEntityResponse{}, nil
}

func (m *mockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return &service.GetEntityResponse{}, nil
}

func (m *mockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return &service.DeleteEntityResponse{}, nil
}

func (m *mockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{}, nil
}

func TestParametersToBindings_Success(t *testing.T) {
	t.Run("single parameter", func(t *testing.T) {
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("hello")),
		}
		bindings, err := parametersToBindings(params, nil)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.NotNil(t, bindings["v1"])
	})

	t.Run("two parameters", func(t *testing.T) {
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("hello")),
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("world")),
		}
		bindings, err := parametersToBindings(params, nil)
		require.NoError(t, err)
		require.Len(t, bindings, 2)
		assert.NotNil(t, bindings["v1"])
		assert.NotNil(t, bindings["v2"])
	})
}

func TestParametersToBindings_NilValue(t *testing.T) {
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, nil),
	}
	bindings, err := parametersToBindings(params, nil)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	_, ok := bindings["v1"].(*sqlparser.NullVal)
	assert.True(t, ok, "nil parameter value should produce *sqlparser.NullVal")
}

// TestParametersToBindings_NilValue_ResolvedOIDIgnored verifies the NULL
// check happens before a resolved OID is ever used to call Scan: a NULL
// wire value must always bind to NullVal, even when the position has a
// real, non-zero declared OID.
func TestParametersToBindings_NilValue_ResolvedOIDIgnored(t *testing.T) {
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, nil),
	}
	bindings, err := parametersToBindings(params, []uint32{pgtype.Int8OID})
	require.NoError(t, err)
	_, ok := bindings["v1"].(*sqlparser.NullVal)
	assert.True(t, ok)
}

func TestParametersToBindings_Empty(t *testing.T) {
	bindings, err := parametersToBindings([]wire.Parameter{}, nil)
	require.NoError(t, err)
	assert.Empty(t, bindings)
}

// TestParametersToBindings_TypeAware covers the resolved-OID decode path
// for every PostgreSQL type paramOIDForColumn can declare. Each value is
// decoded through pgtype's own codec for the given OID and dispatched to
// the matching sqlparser bind expression by paramExpr -- in particular
// bytea, which must round-trip as a hex literal (binary) rather than a
// string literal, the corruption decodeBytea used to work around after
// the fact instead of preventing.
func TestParametersToBindings_TypeAware(t *testing.T) {
	tests := []struct {
		name   string
		oid    uint32
		value  []byte
		assert func(t *testing.T, expr sqlparser.Expr)
	}{
		{
			name:  "int8",
			oid:   pgtype.Int8OID,
			value: []byte("42"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.IntVal, v.Type)
				assert.Equal(t, "42", string(v.Val))
			},
		},
		{
			name:  "float8",
			oid:   pgtype.Float8OID,
			value: []byte("3.14"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.FloatVal, v.Type)
				assert.Equal(t, "3.14", string(v.Val))
			},
		},
		{
			name:  "bool_true",
			oid:   pgtype.BoolOID,
			value: []byte("t"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.IntVal, v.Type)
				assert.Equal(t, "1", string(v.Val))
			},
		},
		{
			name:  "bool_false",
			oid:   pgtype.BoolOID,
			value: []byte("f"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.IntVal, v.Type)
				assert.Equal(t, "0", string(v.Val))
			},
		},
		{
			name:  "text",
			oid:   pgtype.TextOID,
			value: []byte("hello"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.StrVal, v.Type)
				assert.Equal(t, "hello", string(v.Val))
			},
		},
		{
			// Non-UTF-8 binary content, which is exactly what corrupted
			// under the old decodeBytea + string-literal path: a string
			// literal is reinterpreted under the connection charset, but a
			// hex literal is typed as binary and passes through untouched.
			name:  "bytea_non_utf8",
			oid:   pgtype.ByteaOID,
			value: []byte("\\x00ff8041"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.HexVal, v.Type)
				decoded, err := v.HexDecode()
				require.NoError(t, err)
				assert.Equal(t, []byte{0x00, 0xff, 0x80, 0x41}, decoded)
			},
		},
		{
			name:  "timestamptz",
			oid:   pgtype.TimestamptzOID,
			value: []byte("2024-01-02 15:04:05+00"),
			assert: func(t *testing.T, expr sqlparser.Expr) {
				v, ok := expr.(*sqlparser.SQLVal)
				require.True(t, ok)
				assert.Equal(t, sqlparser.StrVal, v.Type)
				assert.Equal(t, "2024-01-02 15:04:05", string(v.Val))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := []wire.Parameter{
				wire.NewParameter(pgtype.NewMap(), wire.TextFormat, tt.value),
			}
			bindings, err := parametersToBindings(params, []uint32{tt.oid})
			require.NoError(t, err)
			tt.assert(t, bindings["v1"])
		})
	}
}

// TestParametersToBindings_UnresolvedOIDFallsBackToStringLiteral verifies
// that a parameter position with no resolved OID (oid 0, or the oids
// slice shorter than the parameter list) falls back to the pre-existing
// string-literal binding, exactly as it did before OID-based type
// awareness was added.
func TestParametersToBindings_UnresolvedOIDFallsBackToStringLiteral(t *testing.T) {
	t.Run("zero OID", func(t *testing.T) {
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("42")),
		}
		bindings, err := parametersToBindings(params, []uint32{0})
		require.NoError(t, err)
		v, ok := bindings["v1"].(*sqlparser.SQLVal)
		require.True(t, ok)
		assert.Equal(t, sqlparser.StrVal, v.Type)
		assert.Equal(t, "42", string(v.Val))
	})

	t.Run("oids shorter than parameters", func(t *testing.T) {
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("a")),
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("b")),
		}
		bindings, err := parametersToBindings(params, []uint32{pgtype.TextOID})
		require.NoError(t, err)
		_, ok := bindings["v2"].(*sqlparser.SQLVal)
		require.True(t, ok)
		assert.Equal(t, sqlparser.StrVal, bindings["v2"].(*sqlparser.SQLVal).Type)
	})
}

// TestParametersToBindings_ScanError verifies a value that cannot be
// decoded under its declared OID produces a typed *ParameterBindingError
// instead of silently falling back or panicking.
func TestParametersToBindings_ScanError(t *testing.T) {
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("not-a-number")),
	}
	bindings, err := parametersToBindings(params, []uint32{pgtype.Int8OID})
	require.Error(t, err)
	assert.Nil(t, bindings)

	var pbe *ParameterBindingError
	require.ErrorAs(t, err, &pbe)
	assert.Equal(t, 0, pbe.Index)
}

func TestParamExpr(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  func(t *testing.T, expr sqlparser.Expr)
	}{
		{"bool true", true, func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "1", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"bool false", false, func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "0", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"int16", int16(7), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "7", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"int32", int32(8), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "8", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"int64", int64(9), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "9", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"float32", float32(1.5), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "1.5", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"float64", float64(2.5), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "2.5", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"string", "hi", func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "hi", string(expr.(*sqlparser.SQLVal).Val))
		}},
		{"bytes", []byte{0xde, 0xad}, func(t *testing.T, expr sqlparser.Expr) {
			decoded, err := expr.(*sqlparser.SQLVal).HexDecode()
			require.NoError(t, err)
			assert.Equal(t, []byte{0xde, 0xad}, decoded)
		}},
		{"time", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), func(t *testing.T, expr sqlparser.Expr) {
			assert.Equal(t, "2024-01-02 03:04:05", string(expr.(*sqlparser.SQLVal).Val))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := paramExpr(tt.value)
			require.NoError(t, err)
			tt.want(t, expr)
		})
	}
}

func TestParamExpr_UnsupportedType(t *testing.T) {
	_, err := paramExpr(struct{}{})
	require.Error(t, err)

	var upte *UnsupportedParameterTypeError
	assert.ErrorAs(t, err, &upte)
}

func TestHandleParse_ParameterizedInsert(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	// A parameterized INSERT with $1/$2 placeholders should take the
	// deferred execution path and return a PreparedStatement without
	// executing immediately.
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: `INSERT INTO kv ("key", "value") VALUES ($1, $2)`})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)

	// Verify the deferred path did not execute the query eagerly:
	// the mock service should still have an empty scanData map.
	assert.Empty(t, kvSvc.scanData, "deferred path should not have executed the INSERT during PARSE")
}

func TestHandleParse_NoParams_EagerExecution(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{
			"eager-key": []byte("eager-val"),
		},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	// A non-parameterized SELECT should execute eagerly during HandleParse.
	// The query contains no $N placeholders, so it takes the eager path
	// and the result is returned successfully with one prepared statement.
	stmts, err := h.HandleParse(context.Background(), wire.Query{Query: "SELECT * FROM kv"})
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)

	// Contrast with a parameterized variant: adding $1 should take the
	// deferred path instead, producing a different code path.
	stmtsDeferred, err := h.HandleParse(context.Background(), wire.Query{Query: `SELECT * FROM kv WHERE "key" = $1`})
	require.NoError(t, err)
	require.NotNil(t, stmtsDeferred)
	assert.Len(t, stmtsDeferred, 1)
}

func TestParameterBindingError(t *testing.T) {
	t.Run("with underlying error", func(t *testing.T) {
		cause := errors.New("bad value")
		pbe := &ParameterBindingError{Index: 0, Err: cause}
		assert.Contains(t, pbe.Error(), "parameter binding failed at index 0")
		assert.Contains(t, pbe.Error(), "bad value")
		assert.Equal(t, cause, pbe.Unwrap())
	})

	t.Run("without underlying error", func(t *testing.T) {
		pbe := &ParameterBindingError{Index: 3, Err: nil}
		assert.Contains(t, pbe.Error(), "parameter binding failed at index 3")
		assert.Nil(t, pbe.Unwrap())
	})

	t.Run("error type assertion", func(t *testing.T) {
		cause := errors.New("conversion failed")
		var err error = &ParameterBindingError{Index: 1, Err: cause}
		var pbe *ParameterBindingError
		assert.ErrorAs(t, err, &pbe)
		assert.Equal(t, 1, pbe.Index)
	})
}

func TestExecuteDeferredCallback_InsertAndSelect(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	t.Run("insert with parameters", func(t *testing.T) {
		writer := &mockDataWriter{}
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("deferred-key")),
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("deferred-val")),
		}

		// Execute the deferred callback for a parameterized INSERT.
		err := h.executeDeferredCallback(
			context.Background(),
			writer,
			params,
			`INSERT INTO kv ("key", "value") VALUES ($1, $2)`,
			"INSERT INTO kv (`key`, `value`) VALUES (?, ?)",
			nil,
			nil,
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, "INSERT 0 1", writer.completeTag)

		// Verify the insert was routed to the mock service.
		assert.NotEmpty(t, kvSvc.scanData, "deferred INSERT should have written to the mock service")
	})

	t.Run("select with parameters", func(t *testing.T) {
		writer := &mockDataWriter{}
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("deferred-key")),
		}

		err := h.executeDeferredCallback(
			context.Background(),
			writer,
			params,
			`SELECT * FROM kv WHERE "key" = $1`,
			"SELECT * FROM kv WHERE `key` = ?",
			nil,
			nil,
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, 1, writer.rowCount)
		assert.Contains(t, writer.completeTag, "SELECT 1")
	})
}

// TestExecuteDeferredCallback_WithParsedStatement verifies the callback
// executes correctly when handed the AST executeDeferred already parsed
// from the translated text, rather than nil -- the "real bindings" path
// that lets QueryWithBindings execute directly against a structured
// statement instead of re-parsing the query string itself.
func TestExecuteDeferredCallback_WithParsedStatement(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	writer := &mockDataWriter{}
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("parsed-key")),
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("parsed-val")),
	}

	translated := "INSERT INTO kv (`key`, `value`) VALUES (?, ?)"
	parsedStmt, err := sqlparser.Parse(translated)
	require.NoError(t, err)

	err = h.executeDeferredCallback(
		context.Background(),
		writer,
		params,
		`INSERT INTO kv ("key", "value") VALUES ($1, $2)`,
		translated,
		parsedStmt,
		nil,
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, "INSERT 0 1", writer.completeTag)
	assert.NotEmpty(t, kvSvc.scanData)
}

func TestExecuteDeferredCallback_QueryError(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	writer := &mockDataWriter{}
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("val")),
	}

	// Use invalid SQL that will fail during QueryWithBindings.
	err := h.executeDeferredCallback(
		context.Background(),
		writer,
		params,
		"SELECT * FROM nonexistent WHERE id = $1",
		"SELECT * FROM nonexistent WHERE id = ?",
		nil,
		nil,
		nil,
	)
	require.Error(t, err)

	var queryErr *QueryError
	assert.ErrorAs(t, err, &queryErr)
}

func TestExecuteDeferredCallback_EmptyParams(t *testing.T) {
	// Test with zero parameters -- exercises the parametersToBindings
	// empty path within the callback.
	kvSvc := &mockKVService{
		scanData: map[string][]byte{
			"all-key": []byte("all-val"),
		},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	writer := &mockDataWriter{}

	// A SELECT with no parameters but called through the deferred path
	// (this wouldn't normally happen, but exercises the zero-params case).
	err := h.executeDeferredCallback(
		context.Background(),
		writer,
		nil,
		"SELECT * FROM kv",
		"SELECT * FROM kv",
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, writer.rowCount)
	assert.Contains(t, writer.completeTag, "SELECT 1")
}

func TestExecuteDeferredCallback_WriteRowError(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{
			"err-key": []byte("err-val"),
		},
	}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	// Writer that fails on Row() to exercise the writeRows error path.
	writer := &mockDataWriter{rowErr: errors.New("write row failed")}
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("err-key")),
	}

	err := h.executeDeferredCallback(
		context.Background(),
		writer,
		params,
		`SELECT * FROM kv WHERE "key" = $1`,
		"SELECT * FROM kv WHERE `key` = ?",
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write row failed")
}

// TestParametersToBindings_UnresolvedByteaBoundAsStringLiteral pins the
// documented, accepted limitation left after removing decodeBytea: a
// binary parameter this package cannot statically resolve a column type
// for (see inferParamOIDs) is bound as a plain string literal, exactly
// like every other unresolved scalar. This is a narrower, more honest
// failure mode than the old heuristic, which guessed a value was bytea
// purely from its wire-text shape (a "\x..." prefix) and could
// misinterpret an ordinary string that happened to start with it.
func TestParametersToBindings_UnresolvedByteaBoundAsStringLiteral(t *testing.T) {
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("\\x68656c6c6f")),
	}
	bindings, err := parametersToBindings(params, nil)
	require.NoError(t, err)

	v, ok := bindings["v1"].(*sqlparser.SQLVal)
	require.True(t, ok)
	assert.Equal(t, sqlparser.StrVal, v.Type)
	assert.Equal(t, "\\x68656c6c6f", string(v.Val))
}

func TestParamOIDForColumn(t *testing.T) {
	tests := []struct {
		name string
		typ  sql.Type
		want uint32
	}{
		{"blob is bytea", sqltypes.Blob, pgtype.ByteaOID},
		{"medium blob is bytea", sqltypes.MediumBlob, pgtype.ByteaOID},
		{"boolean", sqltypes.Boolean, pgtype.BoolOID},
		{"int64", sqltypes.Int64, pgtype.Int8OID},
		{"float64", sqltypes.Float64, pgtype.Float8OID},
		{"decimal", sqltypes.MustCreateDecimalType(10, 2), pgtype.Float8OID},
		{"datetime", sqltypes.Datetime, pgtype.TimestamptzOID},
		{"timestamp", sqltypes.Timestamp, pgtype.TimestamptzOID},
		{"text", sqltypes.Text, pgtype.TextOID},
		{"json has no mapping", sqltypes.JSON, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, paramOIDForColumn(tt.typ))
		})
	}
}

func TestValArgOrdinal(t *testing.T) {
	t.Run("valid ValArg", func(t *testing.T) {
		n, ok := valArgOrdinal(sqlparser.NewValArg([]byte(":v3")))
		require.True(t, ok)
		assert.Equal(t, 3, n)
	})

	t.Run("not a ValArg SQLVal", func(t *testing.T) {
		_, ok := valArgOrdinal(sqlparser.NewStrVal([]byte("literal")))
		assert.False(t, ok)
	})

	t.Run("not a SQLVal at all", func(t *testing.T) {
		_, ok := valArgOrdinal(sqlparser.NewColName("col"))
		assert.False(t, ok)
	})

	t.Run("malformed ordinal", func(t *testing.T) {
		_, ok := valArgOrdinal(sqlparser.NewValArg([]byte(":vNaN")))
		assert.False(t, ok)
	})
}

func TestColumnType(t *testing.T) {
	schema := sql.Schema{
		{Name: "id", Type: sqltypes.Int64},
		{Name: "Name", Type: sqltypes.Text},
	}

	t.Run("exact match", func(t *testing.T) {
		typ, ok := columnType(schema, sqlparser.NewColIdent("id"))
		require.True(t, ok)
		assert.Equal(t, sqltypes.Int64, typ)
	})

	t.Run("case insensitive match", func(t *testing.T) {
		typ, ok := columnType(schema, sqlparser.NewColIdent("name"))
		require.True(t, ok)
		assert.Equal(t, sqltypes.Text, typ)
	})

	t.Run("no match", func(t *testing.T) {
		_, ok := columnType(schema, sqlparser.NewColIdent("missing"))
		assert.False(t, ok)
	})
}

func TestSingleTableName(t *testing.T) {
	t.Run("plain table", func(t *testing.T) {
		expr := &sqlparser.AliasedTableExpr{Expr: sqlparser.TableName{Name: sqlparser.NewTableIdent("kv")}}
		name, ok := singleTableName(expr)
		require.True(t, ok)
		assert.Equal(t, "kv", name)
	})

	t.Run("not an AliasedTableExpr", func(t *testing.T) {
		_, ok := singleTableName(&sqlparser.JoinTableExpr{})
		assert.False(t, ok)
	})

	t.Run("subquery, not a plain table", func(t *testing.T) {
		expr := &sqlparser.AliasedTableExpr{Expr: &sqlparser.Subquery{}}
		_, ok := singleTableName(expr)
		assert.False(t, ok)
	})
}

func TestLookupTable(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	t.Run("existing table", func(t *testing.T) {
		table, ok := lookupTable(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", "kv")
		require.True(t, ok)
		assert.Equal(t, "kv", table.Name())
	})

	t.Run("nonexistent table", func(t *testing.T) {
		_, ok := lookupTable(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", "nonexistent")
		assert.False(t, ok)
	})

	t.Run("nonexistent database", func(t *testing.T) {
		_, ok := lookupTable(sqlCtx, sqlEngine.Analyzer.Catalog, "nonexistent-db", "kv")
		assert.False(t, ok)
	})
}

// TestInferParamOIDs_Insert exercises the shape GORM's Create() generates
// against the real qrdb `kv` table, whose "value" column is a genuine
// GMS BLOB type -- the exact case that used to require decodeBytea's
// after-the-fact guess. It verifies inferParamOIDs statically resolves
// "key" to TextOID and "value" to ByteaOID from the table's real schema.
func TestInferParamOIDs_Insert(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse("INSERT INTO kv (`key`, `value`) VALUES (:v1, :v2)")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
	assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[2])
}

// TestInferParamOIDs_InsertMultiRow verifies every row of a multi-row
// VALUES list maps its positional parameters to the same target column,
// the shape GORM's batched Create() generates.
func TestInferParamOIDs_InsertMultiRow(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse("INSERT INTO kv (`key`, `value`) VALUES (:v1, :v2), (:v3, :v4)")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
	assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[2])
	assert.Equal(t, uint32(pgtype.TextOID), oids[3])
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[4])
}

// TestInferParamOIDs_InsertRowAlias verifies the MySQL 8.0.19+
// "VALUES (...) AS alias" row-alias form -- which vitess represents as
// *sqlparser.AliasedValues rather than a bare sqlparser.Values -- resolves
// the same as a plain VALUES clause.
func TestInferParamOIDs_InsertRowAlias(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse("INSERT INTO kv (`key`, `value`) VALUES (:v1, :v2) AS new_values")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
	assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[2])
}

// TestInferParamOIDs_Update exercises the shape GORM's Save()/Updates()
// generates: a single-table UPDATE SET clause.
func TestInferParamOIDs_Update(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse("UPDATE kv SET `value` = :v1 WHERE `key` = :v2")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
	// Both the SET target and the WHERE-clause comparison resolve.
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[1])
	assert.Equal(t, uint32(pgtype.TextOID), oids[2])
}

// TestInferParamOIDs_UnresolvedCases verifies every case inferParamOIDs
// intentionally leaves unmapped, falling back to the pre-existing
// string-literal binding rather than guessing.
func TestInferParamOIDs_UnresolvedCases(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	t.Run("select WHERE comparison resolves", func(t *testing.T) {
		stmt, err := sqlparser.Parse("SELECT * FROM kv WHERE `key` = :v1")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	})

	t.Run("delete WHERE comparison resolves", func(t *testing.T) {
		stmt, err := sqlparser.Parse("DELETE FROM kv WHERE `key` = :v1")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	})

	t.Run("WHERE IN tuple and BETWEEN resolve per element", func(t *testing.T) {
		stmt, err := sqlparser.Parse("SELECT * FROM kv WHERE `key` IN (:v1, :v2) AND `key` BETWEEN :v3 AND :v4")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Equal(t, map[int]uint32{
			1: pgtype.TextOID, 2: pgtype.TextOID, 3: pgtype.TextOID, 4: pgtype.TextOID,
		}, oids)
	})

	t.Run("WHERE subquery columns are not resolved against outer relations", func(t *testing.T) {
		// The inner `key` belongs to the subquery's own FROM; resolving it
		// against the outer statement could infer a wrong OID, so it is
		// deliberately left unmapped.
		stmt, err := sqlparser.Parse("SELECT * FROM kv WHERE `value` = (SELECT `value` FROM other WHERE `key` = :v1)")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("unknown table", func(t *testing.T) {
		stmt, err := sqlparser.Parse("INSERT INTO nonexistent (`a`) VALUES (:v1)")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("unknown column", func(t *testing.T) {
		stmt, err := sqlparser.Parse("UPDATE kv SET `nonexistent` = :v1 WHERE `key` = 'k'")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("literal value, not a bind parameter", func(t *testing.T) {
		stmt, err := sqlparser.Parse("INSERT INTO kv (`key`, `value`) VALUES ('literal-key', :v1)")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Equal(t, uint32(pgtype.ByteaOID), oids[1])
		assert.Len(t, oids, 1)
	})

	t.Run("multi-table update resolves qualified assignments", func(t *testing.T) {
		// The multi-table form is what translateUpdateFrom produces for
		// PostgreSQL's UPDATE ... FROM; every assignment arrives qualified
		// with its table, and resolving it is what keeps a bound timestamp
		// from falling back to PostgreSQL's zone-suffixed text form, which
		// the engine's DATETIME parser rejects.
		stmt, err := sqlparser.Parse("UPDATE kv, kv AS other SET kv.`value` = :v1, other.`value` = :v2")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Equal(t, uint32(pgtype.ByteaOID), oids[1])
		assert.Equal(t, uint32(pgtype.ByteaOID), oids[2], "alias qualifier must resolve too")
		assert.Len(t, oids, 2)
	})

	t.Run("multi-table update with unresolvable qualifier", func(t *testing.T) {
		stmt, err := sqlparser.Parse("UPDATE kv, kv AS other SET nope.`value` = :v1")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("multi-table update with unqualified assignment", func(t *testing.T) {
		// An unqualified assignment in the multi-table form is ambiguous;
		// it stays unresolved rather than being guessed.
		stmt, err := sqlparser.Parse("UPDATE kv, kv AS other SET `value` = :v1")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("insert ... select has no VALUES tuple", func(t *testing.T) {
		stmt, err := sqlparser.Parse("INSERT INTO kv (`key`, `value`) SELECT `key`, `value` FROM kv")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})

	t.Run("statement kind with no ON CONFLICT/UPDATE handling", func(t *testing.T) {
		stmt, err := sqlparser.Parse("SET @x = 1")
		require.NoError(t, err)
		oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)
		assert.Empty(t, oids)
	})
}

// TestHandleParse_BinaryParameterRoundTripsWithoutCorruption is the
// regression test for the actual bug this phase fixes: a genuine binary
// bytea parameter -- non-UTF-8 bytes a string-literal bind would corrupt
// under the connection charset -- written to a real BLOB column and read
// back byte for byte, driven end to end through inferParamOIDs,
// parametersToBindings, and paramExpr exactly as the deferred BIND/EXECUTE
// callback does.
func TestHandleParse_BinaryParameterRoundTripsWithoutCorruption(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	h := NewParseHandler(sqlEngine, translator, "qrdb")

	trimmed := `INSERT INTO kv ("key", "value") VALUES ($1, $2)`
	translated, err := translator.Translate(trimmed)
	require.NoError(t, err)

	sqlCtx := h.buildSQLContext(context.Background())
	parsedStmt, err := sqlparser.Parse(translated)
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", parsedStmt)
	require.Equal(t, uint32(pgtype.ByteaOID), oids[2])
	paramOIDs := []uint32{oids[1], oids[2]}

	// Non-UTF-8 raw bytes, exactly what a string-literal bind would mangle
	// once GMS reinterprets it under the connection charset. Sent in
	// binary wire format, which is what pgx sends once the server has
	// declared a real (non-unspecified) OID for the parameter.
	rawValue := []byte{0x00, 0xff, 0x80, 0xfe, 0x41, 0x00, 0x9c}
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("bin-key")),
		wire.NewParameter(pgtype.NewMap(), wire.BinaryFormat, rawValue),
	}

	writer := &mockDataWriter{}
	err = h.executeDeferredCallback(context.Background(), writer, params, trimmed, translated, parsedStmt, paramOIDs, nil)
	require.NoError(t, err)

	// The stored key carries a tenant/database namespace prefix applied by
	// the KV table writer, so look up by the "bin-key" suffix rather than
	// an exact key match.
	var stored []byte
	var found bool
	for k, v := range kvSvc.scanData {
		if strings.HasSuffix(k, "bin-key") {
			stored, found = v, true
			break
		}
	}
	require.True(t, found, "expected a stored entry ending in bin-key, got keys %v", kvSvc.scanData)
	assert.Equal(t, rawValue, stored, "binary parameter must round-trip byte for byte")
}

// ---------------------------------------------------------------------------
// rewriteEndAsCommit
// ---------------------------------------------------------------------------

func TestRewriteEndAsCommit(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bare END", "END", "COMMIT"},
		{"lowercase end", "end", "COMMIT"},
		{"END with semicolon", "END;", "COMMIT"},
		{"END WORK", "END WORK", "COMMIT"},
		{"END TRANSACTION", "END TRANSACTION", "COMMIT"},
		{"end work mixed case", "End Work;", "COMMIT"},
		{"not END: SELECT", "SELECT * FROM widgets", "SELECT * FROM widgets"},
		{"not END: END used as identifier prefix", "ENDPOINT", "ENDPOINT"},
		{"BEGIN untouched", "BEGIN", "BEGIN"},
		{"COMMIT untouched", "COMMIT", "COMMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rewriteEndAsCommit(tc.input))
		})
	}
}

// TestStripTruncateIdentityClause pins the parsing of PostgreSQL's TRUNCATE
// ... RESTART IDENTITY / CONTINUE IDENTITY clause, which the embedded
// MySQL-dialect grammar cannot parse and which must be stripped before the
// statement reaches it.
func TestStripTruncateIdentityClause(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantQuery   string
		wantRestart bool
		wantMatched bool
	}{
		{
			name:        "RESTART IDENTITY",
			query:       "TRUNCATE widgets RESTART IDENTITY",
			wantQuery:   "TRUNCATE widgets",
			wantRestart: true,
			wantMatched: true,
		},
		{
			name:        "CONTINUE IDENTITY",
			query:       "TRUNCATE widgets CONTINUE IDENTITY",
			wantQuery:   "TRUNCATE widgets",
			wantRestart: false,
			wantMatched: true,
		},
		{
			name:        "TABLE keyword and trailing semicolon",
			query:       "TRUNCATE TABLE widgets RESTART IDENTITY;",
			wantQuery:   "TRUNCATE TABLE widgets;",
			wantRestart: true,
			wantMatched: true,
		},
		{
			name:        "case insensitive",
			query:       "truncate widgets restart identity",
			wantQuery:   "truncate widgets",
			wantRestart: true,
			wantMatched: true,
		},
		{
			name:        "plain TRUNCATE has no clause to strip",
			query:       "TRUNCATE widgets",
			wantQuery:   "TRUNCATE widgets",
			wantRestart: false,
			wantMatched: false,
		},
		{
			name:        "non-TRUNCATE statement is left alone",
			query:       "SELECT 1",
			wantQuery:   "SELECT 1",
			wantRestart: false,
			wantMatched: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, restart, matched := stripTruncateIdentityClause(tt.query)
			assert.Equal(t, tt.wantQuery, stripped)
			assert.Equal(t, tt.wantRestart, restart)
			assert.Equal(t, tt.wantMatched, matched)
		})
	}
}

// TestHandleParse_TruncateRestartIdentity_SetsSessionFlag proves HandleParse
// records RESTART IDENTITY on the connection session so the provider can act
// on it, and clears the flag again once the statement finishes.
func TestHandleParse_TruncateRestartIdentity_ClausedStrippedBeforeTranslation(t *testing.T) {
	h := newTestParseHandler(t)

	connCtx := context.WithValue(context.Background(), identityContextKey{},
		&auth.Identity{Subject: "test", TenantID: "acme"})
	connCtx, err := newConnectionSessionHandler()(connCtx)
	require.NoError(t, err)

	sess := connectionSessionFromContext(connCtx)
	require.NotNil(t, sess)
	require.False(t, sess.StatementRestartIdentity())

	// RESTART IDENTITY is not grammar the embedded MySQL-dialect parser
	// accepts, so if HandleParse failed to strip it, this statement would
	// fail with a *TranslationError before ever reaching the engine. It
	// must instead fail with a *QueryError from the engine rejecting the
	// (real) missing table, proving the clause was stripped first.
	_, err = h.HandleParse(connCtx, wire.Query{Query: "TRUNCATE missing_table RESTART IDENTITY"})
	require.Error(t, err)
	var queryErr *QueryError
	assert.ErrorAs(t, err, &queryErr)
	var translationErr *TranslationError
	assert.False(t, errors.As(err, &translationErr))

	assert.False(t, sess.StatementRestartIdentity(),
		"the flag must be cleared again once the statement finishes")
}

// TestHandleParse_EndCommitsRealTransaction verifies PostgreSQL's END alias
// reaches go-mysql-server as a real COMMIT rather than being swallowed as a
// no-op (the pre-Phase-8 behavior) or rejected as invalid MySQL syntax.
func TestHandleParse_EndCommitsRealTransaction(t *testing.T) {
	h := newTestParseHandler(t)
	connCtx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)

	_, err = h.HandleParse(connCtx, wire.Query{Query: "BEGIN"})
	require.NoError(t, err)
	_, err = h.HandleParse(connCtx, wire.Query{Query: "END"})
	require.NoError(t, err, "END must be accepted as a real COMMIT, not rejected as invalid syntax")
}

// TestInferParamOIDs_UpsertAssignments covers the SET clause of an
// ON DUPLICATE KEY UPDATE, which gorm.io/driver/postgres writes on every
// upsert as `ON CONFLICT (...) DO UPDATE SET "col" = $N`.
//
// Those parameters were previously left unresolved, so they fell back to a
// plain string literal. That is harmless for a scalar -- go-mysql-server
// coerces the literal during analysis -- but not for a timestamp: pgx sends
// PostgreSQL's wire form, whose zone designator MySQL's DATETIME parser
// rejects outright, so every GORM upsert carrying an updated_at failed with
// "is not a valid value for 'datetime'".
func TestInferParamOIDs_UpsertAssignments(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse(
		"INSERT INTO kv (`key`, `value`) VALUES (:v1, :v2) " +
			"ON DUPLICATE KEY UPDATE `value` = :v3")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)

	assert.Equal(t, uint32(pgtype.TextOID), oids[1], "the VALUES tuple still resolves")
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[2], "the VALUES tuple still resolves")
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[3],
		"the upsert's SET clause assigns to the same table and must resolve too")
}

// TestInferParamOIDs_UpsertAssignmentOfAnUnknownColumn verifies the walk stays
// best-effort: an assignment this resolution cannot place leaves its parameter
// unmapped, falling back to the string-literal binding rather than guessing an
// OID for it.
func TestInferParamOIDs_UpsertAssignmentOfAnUnknownColumn(t *testing.T) {
	kvSvc := &mockKVService{}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	sqlCtx := sql.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase("qrdb")

	stmt, err := sqlparser.Parse(
		"INSERT INTO kv (`key`, `value`) VALUES (:v1, :v2) " +
			"ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)")
	require.NoError(t, err)

	oids := inferParamOIDs(sqlCtx, sqlEngine.Analyzer.Catalog, "qrdb", stmt)

	assert.Equal(t, uint32(pgtype.TextOID), oids[1])
	assert.Equal(t, uint32(pgtype.ByteaOID), oids[2])
	assert.NotContains(t, oids, 3, "a non-parameter assignment contributes no OID")
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return m.List(a0, a1)
}
