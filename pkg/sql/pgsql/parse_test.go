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
	"context"
	"errors"
	"io"
	"testing"

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
	stmts, err := h.HandleParse(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_WhitespaceOnly(t *testing.T) {
	h := newTestParseHandler(t)
	stmts, err := h.HandleParse(context.Background(), "   \t\n  ")
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
			stmts, err := h.HandleParse(context.Background(), query)
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
			stmts, err := h.HandleParse(context.Background(), query)
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

	stmts, err := h.HandleParse(context.Background(), "SELECT * FROM kv")
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)
}

func TestHandleParse_InvalidSQL(t *testing.T) {
	h := newTestParseHandler(t)
	_, err := h.HandleParse(context.Background(), "INVALID SYNTAX HERE !@#$")
	require.Error(t, err)

	var queryErr *QueryError
	assert.ErrorAs(t, err, &queryErr)
}

func TestHandleParse_NonExistentTable(t *testing.T) {
	h := newTestParseHandler(t)
	_, err := h.HandleParse(context.Background(), "SELECT * FROM nonexistent")
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

	stmts, err := h.HandleParse(ctx, "SELECT * FROM kv")
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
		columns := schemaToColumns(sql.Schema{})
		assert.Empty(t, columns)
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

		columns := schemaToColumns(schema)
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
		expected any
	}{
		{"nil", nil, nil},
		{"byte slice", []byte("hello"), "hello"},
		{"string", "world", "world"},
		{"int", 42, 42},
		{"float", 3.14, 3.14},
		{"bool", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatValue(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNoopStatement(t *testing.T) {
	h := newTestParseHandler(t)

	t.Run("exact matches", func(t *testing.T) {
		assert.True(t, h.isNoopStatement("BEGIN"))
		assert.True(t, h.isNoopStatement("COMMIT"))
		assert.True(t, h.isNoopStatement("ROLLBACK"))
		assert.True(t, h.isNoopStatement("END"))
		assert.True(t, h.isNoopStatement("BEGIN;"))
		assert.True(t, h.isNoopStatement("COMMIT; "))
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

	rows, err := collectRows(sqlCtx, iter)
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

	rows, err := collectRows(sqlCtx, iter)
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
	result, err := collectRows(sqlCtx, iter)
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
	result, err := collectRows(sqlCtx, iter)
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
	result, err := collectRows(sqlCtx, iter)
	require.NoError(t, err)
	assert.Len(t, result.rows, 1)
	assert.Nil(t, result.rows[0][1])
}

func TestCollectRows_OkResult(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(
		sql.Row{sqltypes.OkResult{RowsAffected: 5}},
	)
	result, err := collectRows(sqlCtx, iter)
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
	stmts, err := h.HandleParse(context.Background(), "INSERT INTO kv (`key`, `value`) VALUES ('test-key', 'test-value')")
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

	stmts, err := h.HandleParse(context.Background(), "DELETE FROM kv WHERE `key` = 'key1'")
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
	stmts, err := h.HandleParse(context.Background(), `SELECT "key" FROM kv`)
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
		bindings, err := parametersToBindings(params)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.NotNil(t, bindings["v1"])
	})

	t.Run("two parameters", func(t *testing.T) {
		params := []wire.Parameter{
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("hello")),
			wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("world")),
		}
		bindings, err := parametersToBindings(params)
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
	bindings, err := parametersToBindings(params)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	_, ok := bindings["v1"].(*sqlparser.NullVal)
	assert.True(t, ok, "nil parameter value should produce *sqlparser.NullVal")
}

func TestParametersToBindings_Empty(t *testing.T) {
	bindings, err := parametersToBindings([]wire.Parameter{})
	require.NoError(t, err)
	assert.Empty(t, bindings)
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
	stmts, err := h.HandleParse(context.Background(), `INSERT INTO kv ("key", "value") VALUES ($1, $2)`)
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
	stmts, err := h.HandleParse(context.Background(), "SELECT * FROM kv")
	require.NoError(t, err)
	require.NotNil(t, stmts)
	assert.Len(t, stmts, 1)

	// Contrast with a parameterized variant: adding $1 should take the
	// deferred path instead, producing a different code path.
	stmtsDeferred, err := h.HandleParse(context.Background(), `SELECT * FROM kv WHERE "key" = $1`)
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
		)
		require.NoError(t, err)
		assert.Equal(t, 1, writer.rowCount)
		assert.Contains(t, writer.completeTag, "SELECT 1")
	})
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
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write row failed")
}

func TestDecodeBytea_HexFormat(t *testing.T) {
	// PostgreSQL bytea hex format: \x followed by hex pairs.
	input := []byte("\\x68656c6c6f20776f726c64")
	got := decodeBytea(input)
	assert.Equal(t, []byte("hello world"), got)
}

func TestDecodeBytea_PlainText(t *testing.T) {
	// Plain text values should pass through unchanged.
	input := []byte("hello world")
	got := decodeBytea(input)
	assert.Equal(t, []byte("hello world"), got)
}

func TestDecodeBytea_Nil(t *testing.T) {
	got := decodeBytea(nil)
	assert.Nil(t, got)
}

func TestDecodeBytea_Empty(t *testing.T) {
	got := decodeBytea([]byte{})
	assert.Equal(t, []byte{}, got)
}

func TestDecodeBytea_InvalidHex(t *testing.T) {
	// If \x prefix is followed by invalid hex, return as-is.
	input := []byte("\\xZZZZ")
	got := decodeBytea(input)
	assert.Equal(t, input, got)
}

func TestDecodeBytea_ShortPrefix(t *testing.T) {
	// A single-byte value should pass through.
	got := decodeBytea([]byte("\\"))
	assert.Equal(t, []byte("\\"), got)
}

func TestParametersToBindings_ByteaHex(t *testing.T) {
	// Verify that bytea hex format parameters are properly decoded
	// before being passed to GMS as binding expressions.
	params := []wire.Parameter{
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("greeting")),
		wire.NewParameter(pgtype.NewMap(), wire.TextFormat, []byte("\\x68656c6c6f")),
	}
	bindings, err := parametersToBindings(params)
	require.NoError(t, err)
	require.Len(t, bindings, 2)

	// First param is plain text.
	v1, ok := bindings["v1"].(*sqlparser.SQLVal)
	require.True(t, ok)
	assert.Equal(t, "greeting", string(v1.Val))

	// Second param should be decoded from bytea hex.
	v2, ok := bindings["v2"].(*sqlparser.SQLVal)
	require.True(t, ok)
	assert.Equal(t, "hello", string(v2.Val))
}
