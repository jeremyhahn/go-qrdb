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

package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockKV is an in-memory service.KVServicer. Methods the SQL engine does
// not exercise are inherited from the embedded nil interface and panic if
// called, which keeps the mock honest about what the engine touches.
type mockKV struct {
	service.KVServicer
	store map[string][]byte
}

func newMockKV() *mockKV {
	return &mockKV{store: make(map[string][]byte)}
}

func (m *mockKV) Put(_ context.Context, key string, value []byte) error {
	m.store[key] = value
	return nil
}

func (m *mockKV) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKV) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *mockKV) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKV) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

// mockDB implements the Get method of service.DatabaseServicer; all other
// methods are inherited from the embedded nil interface.
type mockDB struct {
	service.DatabaseServicer
	getFn func(name string) (*database.DatabaseInfo, error)
}

func (m *mockDB) Get(name string) (*database.DatabaseInfo, error) {
	return m.getFn(name)
}

func kvDB(name string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Status:  database.StatusOpen,
	}, nil
}

// seed stores a key-value pair under the system tenant prefix, matching
// the prefix the SQL KV table scans with.
func seed(t *testing.T, kv *mockKV, key, value string) {
	t.Helper()
	prefixed := tenant.TenantPrefix(tenant.SystemTenantID) + key
	if err := kv.Put(context.Background(), prefixed, []byte(value)); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Execute: happy paths
// ---------------------------------------------------------------------------

func TestExecute_SelectLiteral(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT 1 AS val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Columns) != 1 || result.Columns[0] != "val" {
		t.Fatalf("columns = %v, want [val]", result.Columns)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
	if result.Rows[0]["val"] != "1" {
		t.Fatalf("rows[0][val] = %v, want %q", result.Rows[0]["val"], "1")
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
}

func TestExecute_SelectSeededKVData(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "greeting", "hello world")
	svc := NewService(kv, &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
	row := result.Rows[0]
	if row["key"] != "greeting" {
		t.Fatalf("row[key] = %v, want %q", row["key"], "greeting")
	}
	// []byte values must be decoded as UTF-8 strings, not base64.
	if row["value"] != "hello world" {
		t.Fatalf("row[value] = %v, want %q", row["value"], "hello world")
	}
}

func TestExecute_EmptyResultSet(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("rowCount = %d, want 0", result.RowCount)
	}
	if result.Rows == nil {
		t.Fatal("rows must be an empty slice, not nil")
	}
}

func TestExecute_NilDatabaseService(t *testing.T) {
	// Legacy single-database mode: no database service, all tables exposed.
	kv := newMockKV()
	seed(t, kv, "k1", "v1")
	svc := NewService(kv, nil)

	result, err := svc.Execute(context.Background(), "qrdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
}

// ---------------------------------------------------------------------------
// Execute: validation and error paths
// ---------------------------------------------------------------------------

func TestExecute_EmptyQuery(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "")
	var eq *service.SQLEmptyQueryError
	if !errors.As(err, &eq) {
		t.Fatalf("expected SQLEmptyQueryError, got %T: %v", err, err)
	}
}

func TestExecute_EmptyDatabase(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "", "SELECT 1")
	var ed *service.SQLEmptyDatabaseError
	if !errors.As(err, &ed) {
		t.Fatalf("expected SQLEmptyDatabaseError, got %T: %v", err, err)
	}
}

func TestExecute_DatabaseNotFound(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	})

	_, err := svc.Execute(context.Background(), "missing", "SELECT 1")
	var nf *database.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected database.NotFoundError, got %T: %v", err, err)
	}
	if nf.Name != "missing" {
		t.Fatalf("name = %q, want %q", nf.Name, "missing")
	}
}

func TestExecute_InvalidSyntax(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "SELECTX BAD SYNTAX")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError, got %T: %v", err, err)
	}
	if ee.Query != "SELECTX BAD SYNTAX" {
		t.Fatalf("query = %q, want original query", ee.Query)
	}
	if ee.Unwrap() == nil {
		t.Fatal("expected wrapped engine error")
	}
}

func TestExecute_UnknownTable(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM nonexistent")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError, got %T: %v", err, err)
	}
}

func TestExecute_EngineScoping(t *testing.T) {
	// A timeseries-engine database must not expose the kv table.
	svc := NewService(newMockKV(), &mockDB{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  database.EngineTimeSeries,
				Backend: database.BackendMemory,
				Status:  database.StatusOpen,
			}, nil
		},
	})

	_, err := svc.Execute(context.Background(), "tsdb", "SELECT * FROM kv")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError for out-of-engine table, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// formatValue
// ---------------------------------------------------------------------------

func TestFormatValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"bytes", []byte("hello"), "hello"},
		{"nil", nil, ""},
		{"int", 42, "42"},
		{"string", "test", "test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(tc.in); got != tc.want {
				t.Fatalf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
