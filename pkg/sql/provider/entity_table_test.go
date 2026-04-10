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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Test entity
// ---------------------------------------------------------------------------

type testEntity struct {
	ID     uint64  `json:"id"`
	Name   string  `json:"name"`
	Email  string  `json:"email"`
	Age    int64   `json:"age"`
	Score  float64 `json:"score"`
	Active bool    `json:"active"`
}

func (e *testEntity) EntityID() uint64      { return e.ID }
func (e *testEntity) SetEntityID(id uint64) { e.ID = id }

// testRegistration builds a Registration for testEntity using the standard
// JSON marshal/unmarshal codec.
func testRegistration() *entity.Registration {
	t := reflect.TypeOf(testEntity{})
	fields := []entity.FieldMeta{
		{
			GoName:       "ID",
			JSONName:     "id",
			GoType:       reflect.TypeOf(uint64(0)),
			GoKind:       reflect.Uint64,
			IsPrimaryKey: true,
			Indexed:      true,
			Unique:       true,
		},
		{
			GoName:   "Name",
			JSONName: "name",
			GoType:   reflect.TypeOf(""),
			GoKind:   reflect.String,
			Indexed:  true,
			Unique:   false,
		},
		{
			GoName:   "Email",
			JSONName: "email",
			GoType:   reflect.TypeOf(""),
			GoKind:   reflect.String,
			Indexed:  true,
			Unique:   true,
		},
		{
			GoName:   "Age",
			JSONName: "age",
			GoType:   reflect.TypeOf(int64(0)),
			GoKind:   reflect.Int64,
		},
		{
			GoName:   "Score",
			JSONName: "score",
			GoType:   reflect.TypeOf(float64(0)),
			GoKind:   reflect.Float64,
		},
		{
			GoName:   "Active",
			JSONName: "active",
			GoType:   reflect.TypeOf(false),
			GoKind:   reflect.Bool,
		},
	}
	return &entity.Registration{
		EntityType:  "users",
		StructType:  t,
		Fields:      fields,
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "testdb",
		CodecType:   "json",
		NewFunc: func() any {
			return &testEntity{}
		},
		MarshalFunc: func(v any) ([]byte, error) {
			return json.Marshal(v)
		},
		UnmarshalFunc: func(data []byte) (any, error) {
			e := &testEntity{}
			if err := json.Unmarshal(data, e); err != nil {
				return nil, err
			}
			return e, nil
		},
	}
}

// marshalTestEntity serializes a testEntity to JSON bytes for test fixtures.
func marshalTestEntity(e *testEntity) []byte {
	data, err := json.Marshal(e)
	if err != nil {
		panic(fmt.Sprintf("marshalTestEntity: %v", err))
	}
	return data
}

// ---------------------------------------------------------------------------
// mockIndexServicer for entity table tests
// ---------------------------------------------------------------------------

// entityMockIndexServicer is a minimal index servicer stub for EntityTable
// tests. It implements index.IndexServicer.
type entityMockIndexServicer struct {
	listFn  func(ctx context.Context) ([]string, error)
	queryFn func(ctx context.Context, indexName string, value []byte) ([][]byte, error)
	scanFn  func(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)
}

func (m *entityMockIndexServicer) CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error {
	return nil
}

func (m *entityMockIndexServicer) DropIndex(ctx context.Context, name string) error {
	return nil
}

func (m *entityMockIndexServicer) Query(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, indexName, value)
	}
	return nil, nil
}

func (m *entityMockIndexServicer) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, indexName, start, end)
	}
	return nil, nil
}

func (m *entityMockIndexServicer) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Constructor and basic method tests
// ---------------------------------------------------------------------------

func TestNewEntityTable(t *testing.T) {
	svc := &mockKVService{}
	reg := testRegistration()

	tbl := NewEntityTable(svc, "tenant1", reg, nil)

	if tbl == nil {
		t.Fatal("expected non-nil EntityTable")
	}
}

func TestEntityTable_Name(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	if got := tbl.Name(); got != "users" {
		t.Errorf("Name() = %q, want %q", got, "users")
	}
}

func TestEntityTable_String(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	if got := tbl.String(); got != "users" {
		t.Errorf("String() = %q, want %q", got, "users")
	}
}

func TestEntityTable_Collation(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	if got := tbl.Collation(); got != sqle.Collation_Default {
		t.Errorf("Collation() = %v, want %v", got, sqle.Collation_Default)
	}
}

// ---------------------------------------------------------------------------
// Schema derivation tests
// ---------------------------------------------------------------------------

func TestEntityTable_Schema_Columns(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	schema := tbl.Schema()

	// _id + 5 non-PK fields = 6 columns
	if len(schema) != 6 {
		t.Fatalf("Schema() len = %d, want 6", len(schema))
	}

	// Column 0: _id primary key
	if schema[0].Name != "_id" {
		t.Errorf("schema[0].Name = %q, want %q", schema[0].Name, "_id")
	}
	if !schema[0].PrimaryKey {
		t.Error("schema[0].PrimaryKey = false, want true")
	}
	if schema[0].Nullable {
		t.Error("schema[0].Nullable = true, want false")
	}

	// Non-PK columns sorted alphabetically: active, age, email, name, score
	wantNames := []string{"active", "age", "email", "name", "score"}
	for i, want := range wantNames {
		col := schema[i+1]
		if col.Name != want {
			t.Errorf("schema[%d].Name = %q, want %q", i+1, col.Name, want)
		}
		if !col.Nullable {
			t.Errorf("schema[%d].Nullable = false, want true", i+1)
		}
	}
}

func TestEntityTable_Schema_Source(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	for _, col := range tbl.Schema() {
		if col.Source != "users" {
			t.Errorf("column %q: Source = %q, want %q", col.Name, col.Source, "users")
		}
	}
}

func TestBuildEntitySchema_FieldOrder(t *testing.T) {
	reg := testRegistration()
	_, fieldOrder := buildEntitySchema(reg)

	want := []string{"active", "age", "email", "name", "score"}
	if len(fieldOrder) != len(want) {
		t.Fatalf("fieldOrder len = %d, want %d", len(fieldOrder), len(want))
	}
	for i, w := range want {
		if fieldOrder[i] != w {
			t.Errorf("fieldOrder[%d] = %q, want %q", i, fieldOrder[i], w)
		}
	}
}

func TestBuildEntitySchema_OnlyPKField(t *testing.T) {
	reg := &entity.Registration{
		EntityType: "minimal",
		StructType: reflect.TypeOf(struct{ ID uint64 }{}),
		Fields: []entity.FieldMeta{
			{
				GoName:       "ID",
				JSONName:     "id",
				GoType:       reflect.TypeOf(uint64(0)),
				GoKind:       reflect.Uint64,
				IsPrimaryKey: true,
			},
		},
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		NewFunc:     func() any { return &struct{ ID uint64 }{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			v := &struct{ ID uint64 }{}
			return v, json.Unmarshal(data, v)
		},
	}

	schema, fieldOrder := buildEntitySchema(reg)

	if len(schema) != 1 {
		t.Errorf("schema len = %d, want 1 (only _id)", len(schema))
	}
	if len(fieldOrder) != 0 {
		t.Errorf("fieldOrder len = %d, want 0", len(fieldOrder))
	}
}

// ---------------------------------------------------------------------------
// Partitions test
// ---------------------------------------------------------------------------

func TestEntityTable_Partitions(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	if err != nil {
		t.Fatalf("Partitions() error = %v", err)
	}

	p, err := iter.Next(ctx)
	if err != nil {
		t.Fatalf("iter.Next() error = %v", err)
	}
	if string(p.Key()) != "default" {
		t.Errorf("partition Key() = %q, want %q", p.Key(), "default")
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF", err)
	}
}

// ---------------------------------------------------------------------------
// Full-scan rows tests
// ---------------------------------------------------------------------------

func TestEntityTable_FullScanRows_Empty(t *testing.T) {
	svc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{}, nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &collectionPartition{})
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("Next() error = %v, want io.EOF", err)
	}
}

func TestEntityTable_FullScanRows_WithData(t *testing.T) {
	alice := &testEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Age: 30, Score: 9.5, Active: true}
	bob := &testEntity{ID: 2, Name: "Bob", Email: "bob@example.com", Age: 25, Score: 7.0, Active: false}

	svc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{
				"tenant1/users/1": marshalTestEntity(alice),
				"tenant1/users/2": marshalTestEntity(bob),
			}, nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &collectionPartition{})
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	// Collect all rows
	var rows []sqle.Row
	for {
		row, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		rows = append(rows, row)
	}

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestEntityTable_FullScanRows_SkipsMalformed(t *testing.T) {
	svc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{
				"tenant1/users/1": []byte("not valid json {{{"),
			}, nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &collectionPartition{})
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("expected io.EOF after skipping malformed entry, got %v", err)
	}
}

func TestEntityTable_FullScanRows_ScanError(t *testing.T) {
	scanErr := fmt.Errorf("storage unavailable")
	svc := &mockKVService{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, scanErr
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	_, err := tbl.PartitionRows(ctx, &collectionPartition{})
	if err == nil {
		t.Fatal("expected error from Scan, got nil")
	}
}

// ---------------------------------------------------------------------------
// Indexed partition rows tests
// ---------------------------------------------------------------------------

func TestEntityTable_IndexedPartitionRows(t *testing.T) {
	alice := &testEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Age: 30, Score: 9.5, Active: true}

	svc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "tenant1/users/1" {
				return marshalTestEntity(alice), nil
			}
			return nil, fmt.Errorf("not found")
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	ip := &indexedPartition{keys: [][]byte{[]byte("users/1")}}
	iter, err := tbl.PartitionRows(ctx, ip)
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	row, err := iter.Next(ctx)
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}

	// row[0] = _id
	if row[0].(uint64) != 1 {
		t.Errorf("row[0] = %v, want 1", row[0])
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF", err)
	}
}

func TestEntityTable_IndexedPartitionRows_SkipsMissingEntities(t *testing.T) {
	svc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return nil, fmt.Errorf("not found: %s", key)
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	ip := &indexedPartition{keys: [][]byte{[]byte("users/99")}}
	iter, err := tbl.PartitionRows(ctx, ip)
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("Next() error = %v, want io.EOF (deleted entity)", err)
	}
}

// ---------------------------------------------------------------------------
// entityToRow tests
// ---------------------------------------------------------------------------

func TestEntityTable_EntityToRow(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	e := &testEntity{
		ID:     42,
		Name:   "Charlie",
		Email:  "charlie@example.com",
		Age:    28,
		Score:  8.8,
		Active: true,
	}

	row := tbl.entityToRow(e)

	// schema order: _id, active, age, email, name, score
	if len(row) != 6 {
		t.Fatalf("row len = %d, want 6", len(row))
	}
	if row[0].(uint64) != 42 {
		t.Errorf("row[0] (_id) = %v, want 42", row[0])
	}
	if row[1].(bool) != true {
		t.Errorf("row[1] (active) = %v, want true", row[1])
	}
	if row[2].(int64) != 28 {
		t.Errorf("row[2] (age) = %v, want 28", row[2])
	}
	if row[3].(string) != "charlie@example.com" {
		t.Errorf("row[3] (email) = %v, want charlie@example.com", row[3])
	}
	if row[4].(string) != "Charlie" {
		t.Errorf("row[4] (name) = %v, want Charlie", row[4])
	}
	if row[5].(float64) != 8.8 {
		t.Errorf("row[5] (score) = %v, want 8.8", row[5])
	}
}

func TestEntityTable_EntityToRow_ZeroValues(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	e := &testEntity{ID: 1}
	row := tbl.entityToRow(e)

	if len(row) != 6 {
		t.Fatalf("row len = %d, want 6", len(row))
	}
	// Zero values should still be present (not nil), except for types where
	// the zero value is the Go zero.
	if row[2].(int64) != 0 {
		t.Errorf("row[2] (age) = %v, want 0", row[2])
	}
	if row[4].(string) != "" {
		t.Errorf("row[4] (name) = %v, want empty string", row[4])
	}
}

// ---------------------------------------------------------------------------
// Insert tests
// ---------------------------------------------------------------------------

func TestEntityTable_Insert(t *testing.T) {
	var putKey string
	var putVal []byte

	svc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			putKey = key
			putVal = value
			return nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	// schema order: _id, active, age, email, name, score
	err := inserter.Insert(ctx, sqle.Row{uint64(10), true, int64(22), "dave@example.com", "Dave", float64(6.5)})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	wantKey := "tenant1/users/10"
	if putKey != wantKey {
		t.Errorf("Put key = %q, want %q", putKey, wantKey)
	}
	if len(putVal) == 0 {
		t.Error("Put value is empty")
	}

	// Verify the marshaled entity round-trips correctly.
	var decoded testEntity
	if err := json.Unmarshal(putVal, &decoded); err != nil {
		t.Fatalf("unmarshal stored value: %v", err)
	}
	if decoded.ID != 10 {
		t.Errorf("decoded.ID = %d, want 10", decoded.ID)
	}
	if decoded.Name != "Dave" {
		t.Errorf("decoded.Name = %q, want %q", decoded.Name, "Dave")
	}
	if decoded.Email != "dave@example.com" {
		t.Errorf("decoded.Email = %q, want %q", decoded.Email, "dave@example.com")
	}
	if decoded.Age != 22 {
		t.Errorf("decoded.Age = %d, want 22", decoded.Age)
	}
	if decoded.Score != 6.5 {
		t.Errorf("decoded.Score = %v, want 6.5", decoded.Score)
	}
	if !decoded.Active {
		t.Error("decoded.Active = false, want true")
	}
}

func TestEntityTable_Insert_InvalidID(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{})
	if err == nil {
		t.Fatal("expected error for empty row, got nil")
	}
}

func TestEntityTable_Insert_NilID(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{nil})
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Errorf("Insert with nil ID: got error %T, want *InvalidKeyError", err)
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestEntityTable_Delete(t *testing.T) {
	var deletedKey string

	svc := &mockKVService{
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	// schema order: _id, active, age, email, name, score
	err := deleter.Delete(ctx, sqle.Row{uint64(7), false, int64(0), "", "", float64(0)})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	wantKey := "tenant1/users/7"
	if deletedKey != wantKey {
		t.Errorf("Delete key = %q, want %q", deletedKey, wantKey)
	}
}

func TestEntityTable_Delete_EmptyRow(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{})
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Errorf("Delete empty row: got error %T, want *InvalidKeyError", err)
	}
}

func TestEntityTable_Delete_NilID(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{nil})
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Errorf("Delete nil ID: got error %T, want *InvalidKeyError", err)
	}
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestEntityTable_Update_SameID(t *testing.T) {
	var putKey string
	var putVal []byte
	var deleteCalled bool

	svc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			putKey = key
			putVal = value
			return nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			deleteCalled = true
			return nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	// schema order: _id, active, age, email, name, score
	err := updater.Update(ctx,
		sqle.Row{uint64(5), false, int64(20), "old@example.com", "Old", float64(1.0)},
		sqle.Row{uint64(5), true, int64(21), "new@example.com", "New", float64(2.0)},
	)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if deleteCalled {
		t.Error("Delete should not be called when ID is unchanged")
	}

	wantKey := "tenant1/users/5"
	if putKey != wantKey {
		t.Errorf("Put key = %q, want %q", putKey, wantKey)
	}

	var decoded testEntity
	if err := json.Unmarshal(putVal, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != "New" {
		t.Errorf("decoded.Name = %q, want New", decoded.Name)
	}
}

func TestEntityTable_Update_IDChanged(t *testing.T) {
	var deletedKey string
	var putKey string

	svc := &mockKVService{
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
		putFn: func(_ context.Context, key string, _ []byte) error {
			putKey = key
			return nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{uint64(3), false, int64(0), "", "Old", float64(0)},
		sqle.Row{uint64(4), false, int64(0), "", "New", float64(0)},
	)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if deletedKey != "tenant1/users/3" {
		t.Errorf("deleted key = %q, want tenant1/users/3", deletedKey)
	}
	if putKey != "tenant1/users/4" {
		t.Errorf("put key = %q, want tenant1/users/4", putKey)
	}
}

func TestEntityTable_Update_InvalidOldID(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx, sqle.Row{}, sqle.Row{uint64(1)})
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Errorf("Update invalid old ID: got error %T, want *InvalidKeyError", err)
	}
}

func TestEntityTable_Update_InvalidNewID(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx, sqle.Row{uint64(1)}, sqle.Row{})
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Errorf("Update invalid new ID: got error %T, want *InvalidKeyError", err)
	}
}

// ---------------------------------------------------------------------------
// Replacer tests
// ---------------------------------------------------------------------------

func TestEntityTable_Replacer(t *testing.T) {
	var putCalled bool
	svc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			putCalled = true
			return nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	replacer := tbl.Replacer(ctx)
	if replacer == nil {
		t.Fatal("Replacer() returned nil")
	}

	err := replacer.Insert(ctx, sqle.Row{uint64(1), false, int64(0), "r@example.com", "Replacer", float64(0)})
	if err != nil {
		t.Fatalf("Replacer.Insert() error = %v", err)
	}
	if !putCalled {
		t.Error("expected Put to be called by Replacer.Insert")
	}
}

// ---------------------------------------------------------------------------
// GetIndexes tests
// ---------------------------------------------------------------------------

func TestEntityTable_GetIndexes(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	if err != nil {
		t.Fatalf("GetIndexes() error = %v", err)
	}

	// Indexed fields: id (unique), name, email (unique) => 3 indexes
	if len(indexes) != 3 {
		t.Fatalf("GetIndexes() len = %d, want 3", len(indexes))
	}

	// Verify all indexes are *qrdbIndex instances.
	for _, idx := range indexes {
		qi, ok := idx.(*qrdbIndex)
		if !ok {
			t.Errorf("index is %T, want *qrdbIndex", idx)
			continue
		}
		if qi.Table() != "users" {
			t.Errorf("index table = %q, want users", qi.Table())
		}
		if qi.Database() != "testdb" {
			t.Errorf("index database = %q, want testdb", qi.Database())
		}
	}
}

func TestEntityTable_GetIndexes_UniqueFlag(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	if err != nil {
		t.Fatalf("GetIndexes() error = %v", err)
	}

	// Find the email index (unique) and name index (non-unique).
	uniqueByColumn := make(map[string]bool)
	for _, idx := range indexes {
		qi := idx.(*qrdbIndex)
		uniqueByColumn[qi.column] = qi.IsUnique()
	}

	if !uniqueByColumn["id"] {
		t.Error("id index should be unique")
	}
	if !uniqueByColumn["email"] {
		t.Error("email index should be unique")
	}
	if uniqueByColumn["name"] {
		t.Error("name index should not be unique")
	}
}

func TestEntityTable_GetIndexes_NoIndexedFields(t *testing.T) {
	reg := &entity.Registration{
		EntityType: "noidx",
		StructType: reflect.TypeOf(testEntity{}),
		Fields: []entity.FieldMeta{
			{
				GoName:       "ID",
				JSONName:     "id",
				GoType:       reflect.TypeOf(uint64(0)),
				GoKind:       reflect.Uint64,
				IsPrimaryKey: true,
				Indexed:      false,
			},
		},
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "testdb",
		NewFunc:     func() any { return &testEntity{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			e := &testEntity{}
			return e, json.Unmarshal(data, e)
		},
	}

	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	if err != nil {
		t.Fatalf("GetIndexes() error = %v", err)
	}
	if len(indexes) != 0 {
		t.Errorf("GetIndexes() len = %d, want 0", len(indexes))
	}
}

// ---------------------------------------------------------------------------
// IndexedAccess and PreciseMatch tests
// ---------------------------------------------------------------------------

func TestEntityTable_IndexedAccess(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)
	ctx := newTestContext()

	result := tbl.IndexedAccess(ctx, sqle.IndexLookup{})
	if result != tbl {
		t.Error("IndexedAccess() should return the table itself")
	}
}

func TestEntityTable_PreciseMatch(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil)

	if !tbl.PreciseMatch() {
		t.Error("PreciseMatch() = false, want true")
	}
}

// ---------------------------------------------------------------------------
// LookupPartitions tests
// ---------------------------------------------------------------------------

func TestEntityTable_LookupPartitions_NilIndexSvc(t *testing.T) {
	reg := testRegistration()
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, nil) // no indexSvc
	ctx := newTestContext()

	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	if err != nil {
		t.Fatalf("LookupPartitions() error = %v", err)
	}

	// Should fall through to a standard collectionPartition.
	p, err := iter.Next(ctx)
	if err != nil {
		t.Fatalf("iter.Next() error = %v", err)
	}
	if string(p.Key()) != "default" {
		t.Errorf("partition Key() = %q, want default", p.Key())
	}
}

func TestEntityTable_LookupPartitions_EmptyLookup(t *testing.T) {
	reg := testRegistration()
	idxSvc := &entityMockIndexServicer{}
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, idxSvc)
	ctx := newTestContext()

	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	if err != nil {
		t.Fatalf("LookupPartitions() error = %v", err)
	}
	if iter == nil {
		t.Fatal("LookupPartitions() returned nil iter")
	}
}

// ---------------------------------------------------------------------------
// entityWriter lifecycle tests
// ---------------------------------------------------------------------------

func TestEntityWriter_StatementBegin(t *testing.T) {
	reg := testRegistration()
	w := &entityWriter{kvSvc: &mockKVService{}, tenantID: "t1", reg: reg}
	ctx := newTestContext()
	w.StatementBegin(ctx) // must not panic
}

func TestEntityWriter_DiscardChanges(t *testing.T) {
	reg := testRegistration()
	w := &entityWriter{kvSvc: &mockKVService{}, tenantID: "t1", reg: reg}
	ctx := newTestContext()
	if err := w.DiscardChanges(ctx, nil); err != nil {
		t.Errorf("DiscardChanges() error = %v, want nil", err)
	}
}

func TestEntityWriter_StatementComplete(t *testing.T) {
	reg := testRegistration()
	w := &entityWriter{kvSvc: &mockKVService{}, tenantID: "t1", reg: reg}
	ctx := newTestContext()
	if err := w.StatementComplete(ctx); err != nil {
		t.Errorf("StatementComplete() error = %v, want nil", err)
	}
}

func TestEntityWriter_Close(t *testing.T) {
	reg := testRegistration()
	w := &entityWriter{kvSvc: &mockKVService{}, tenantID: "t1", reg: reg}
	ctx := newTestContext()
	if err := w.Close(ctx); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// goKindToIndexType tests
// ---------------------------------------------------------------------------

func TestGoKindToIndexType(t *testing.T) {
	tests := []struct {
		kind reflect.Kind
		want string
	}{
		{reflect.String, "string"},
		{reflect.Uint64, "uint64"},
		{reflect.Uint, "uint64"},
		{reflect.Int64, "int64"},
		{reflect.Int, "int64"},
		{reflect.Float64, "float64"},
		{reflect.Bool, "boolean"},
		{reflect.Float32, "string"}, // default
		{reflect.Uint32, "string"},  // default
		{reflect.Struct, "string"},  // default
		{reflect.Ptr, "string"},     // default
	}

	for _, tt := range tests {
		t.Run(tt.kind.String(), func(t *testing.T) {
			got := goKindToIndexType(tt.kind)
			if got != tt.want {
				t.Errorf("goKindToIndexType(%v) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// entitySQLType tests
// ---------------------------------------------------------------------------

func TestEntitySQLType_AllKinds(t *testing.T) {
	tests := []struct {
		name   string
		goType reflect.Type
	}{
		{"string", reflect.TypeOf("")},
		{"int64", reflect.TypeOf(int64(0))},
		{"uint64", reflect.TypeOf(uint64(0))},
		{"float64", reflect.TypeOf(float64(0))},
		{"bool", reflect.TypeOf(false)},
		{"bytes", reflect.TypeOf([]byte{})},
		{"map", reflect.TypeOf(map[string]any{})},
		{"struct", reflect.TypeOf(struct{}{})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := entity.FieldMeta{
				GoType: tt.goType,
				GoKind: tt.goType.Kind(),
			}
			result := entitySQLType(meta)
			if result == nil {
				t.Errorf("entitySQLType(%v) returned nil", tt.goType)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// coerceReflectToSQL tests
// ---------------------------------------------------------------------------

func TestCoerceReflectToSQL_Scalar(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"string", "hello"},
		{"int64", int64(42)},
		{"uint64", uint64(99)},
		{"float64", float64(3.14)},
		{"bool", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fv := reflect.ValueOf(tc.value)
			got := coerceReflectToSQL(fv)
			if got != tc.value {
				t.Errorf("coerceReflectToSQL(%v) = %v, want %v", tc.value, got, tc.value)
			}
		})
	}
}

func TestCoerceReflectToSQL_Map(t *testing.T) {
	m := map[string]any{"key": "val"}
	fv := reflect.ValueOf(m)
	got := coerceReflectToSQL(fv)

	s, ok := got.(string)
	if !ok {
		t.Fatalf("coerceReflectToSQL(map) returned %T, want string", got)
	}
	if s != `{"key":"val"}` {
		t.Errorf("coerceReflectToSQL(map) = %q, want %q", s, `{"key":"val"}`)
	}
}

func TestCoerceReflectToSQL_ByteSlice(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03}
	fv := reflect.ValueOf(b)
	got := coerceReflectToSQL(fv)

	gotBytes, ok := got.([]byte)
	if !ok {
		t.Fatalf("coerceReflectToSQL([]byte) returned %T, want []byte", got)
	}
	if string(gotBytes) != string(b) {
		t.Errorf("coerceReflectToSQL([]byte) = %v, want %v", gotBytes, b)
	}
}

func TestCoerceReflectToSQL_IntSlice(t *testing.T) {
	s := []int{1, 2, 3}
	fv := reflect.ValueOf(s)
	got := coerceReflectToSQL(fv)

	str, ok := got.(string)
	if !ok {
		t.Fatalf("coerceReflectToSQL([]int) returned %T, want string", got)
	}
	if str != "[1,2,3]" {
		t.Errorf("coerceReflectToSQL([]int) = %q, want [1,2,3]", str)
	}
}

// ---------------------------------------------------------------------------
// setFieldFromSQL tests
// ---------------------------------------------------------------------------

func TestSetFieldFromSQL_StringFromBytes(t *testing.T) {
	var s string
	fv := reflect.ValueOf(&s).Elem()
	setFieldFromSQL(fv, []byte("hello"))
	if s != "hello" {
		t.Errorf("s = %q, want hello", s)
	}
}

func TestSetFieldFromSQL_Uint64FromInt64(t *testing.T) {
	var u uint64
	fv := reflect.ValueOf(&u).Elem()
	setFieldFromSQL(fv, int64(42))
	if u != 42 {
		t.Errorf("u = %d, want 42", u)
	}
}

func TestSetFieldFromSQL_Int64FromUint64(t *testing.T) {
	var i int64
	fv := reflect.ValueOf(&i).Elem()
	setFieldFromSQL(fv, uint64(99))
	if i != 99 {
		t.Errorf("i = %d, want 99", i)
	}
}

func TestSetFieldFromSQL_Float64FromInt64(t *testing.T) {
	var f float64
	fv := reflect.ValueOf(&f).Elem()
	setFieldFromSQL(fv, int64(7))
	if f != 7.0 {
		t.Errorf("f = %v, want 7.0", f)
	}
}

func TestSetFieldFromSQL_BoolFromInt8(t *testing.T) {
	var b bool
	fv := reflect.ValueOf(&b).Elem()
	setFieldFromSQL(fv, int8(1))
	if !b {
		t.Error("b = false, want true")
	}
}

func TestSetFieldFromSQL_NilIsNoOp(t *testing.T) {
	s := "original"
	fv := reflect.ValueOf(&s).Elem()
	setFieldFromSQL(fv, nil)
	if s != "original" {
		t.Errorf("s changed to %q after nil set", s)
	}
}

// ---------------------------------------------------------------------------
// Additional setFieldFromSQL coverage
// ---------------------------------------------------------------------------

func TestSetFieldFromSQL_Uint64FromFloat64(t *testing.T) {
	var u uint64
	fv := reflect.ValueOf(&u).Elem()
	setFieldFromSQL(fv, float64(3.14))
	if u != 3 {
		t.Errorf("u = %d, want 3", u)
	}
}

func TestSetFieldFromSQL_Uint64FromString(t *testing.T) {
	var u uint64
	fv := reflect.ValueOf(&u).Elem()
	setFieldFromSQL(fv, "123")
	if u != 123 {
		t.Errorf("u = %d, want 123", u)
	}
}

func TestSetFieldFromSQL_Int64FromFloat64(t *testing.T) {
	var i int64
	fv := reflect.ValueOf(&i).Elem()
	setFieldFromSQL(fv, float64(3.7))
	if i != 3 {
		t.Errorf("i = %d, want 3", i)
	}
}

func TestSetFieldFromSQL_Int64FromString(t *testing.T) {
	var i int64
	fv := reflect.ValueOf(&i).Elem()
	setFieldFromSQL(fv, "456")
	if i != 456 {
		t.Errorf("i = %d, want 456", i)
	}
}

func TestSetFieldFromSQL_Float64FromUint64(t *testing.T) {
	var f float64
	fv := reflect.ValueOf(&f).Elem()
	setFieldFromSQL(fv, uint64(100))
	if f != 100.0 {
		t.Errorf("f = %v, want 100.0", f)
	}
}

func TestSetFieldFromSQL_Float64FromString(t *testing.T) {
	var f float64
	fv := reflect.ValueOf(&f).Elem()
	setFieldFromSQL(fv, "3.14")
	if f != 3.14 {
		t.Errorf("f = %v, want 3.14", f)
	}
}

func TestSetFieldFromSQL_BoolFromInt64(t *testing.T) {
	var b bool
	fv := reflect.ValueOf(&b).Elem()
	setFieldFromSQL(fv, int64(1))
	if !b {
		t.Error("b = false, want true")
	}
}

func TestSetFieldFromSQL_BoolFromInt64_Zero(t *testing.T) {
	b := true
	fv := reflect.ValueOf(&b).Elem()
	setFieldFromSQL(fv, int64(0))
	if b {
		t.Error("b = true, want false")
	}
}

func TestSetFieldFromSQL_BoolFromString(t *testing.T) {
	var b bool
	fv := reflect.ValueOf(&b).Elem()
	setFieldFromSQL(fv, "true")
	if !b {
		t.Error("b = false, want true")
	}
}

func TestSetFieldFromSQL_StringFromFmtSprintf(t *testing.T) {
	var s string
	fv := reflect.ValueOf(&s).Elem()
	// bool is not assignable or convertible to string, so setFieldFromSQL
	// falls through to the fmt.Sprintf default in the String case.
	setFieldFromSQL(fv, true)
	if s != "true" {
		t.Errorf("s = %q, want true", s)
	}
}

func TestSetFieldFromSQL_DirectAssignable(t *testing.T) {
	var s string
	fv := reflect.ValueOf(&s).Elem()
	setFieldFromSQL(fv, "direct")
	if s != "direct" {
		t.Errorf("s = %q, want direct", s)
	}
}

// ---------------------------------------------------------------------------
// Additional coerceReflectToSQL coverage
// ---------------------------------------------------------------------------

func TestCoerceReflectToSQL_InvalidValue(t *testing.T) {
	var fv reflect.Value // zero/invalid Value
	got := coerceReflectToSQL(fv)
	if got != nil {
		t.Errorf("coerceReflectToSQL(invalid) = %v, want nil", got)
	}
}

func TestCoerceReflectToSQL_NilPtr(t *testing.T) {
	var p *testEntity
	fv := reflect.ValueOf(&p).Elem()
	got := coerceReflectToSQL(fv)
	if got != nil {
		t.Errorf("coerceReflectToSQL(nil ptr) = %v, want nil", got)
	}
}

func TestCoerceReflectToSQL_NonNilPtr(t *testing.T) {
	s := "hello"
	fv := reflect.ValueOf(&s)
	got := coerceReflectToSQL(fv)
	if got != "hello" {
		t.Errorf("coerceReflectToSQL(*string) = %v, want hello", got)
	}
}

func TestCoerceReflectToSQL_Struct(t *testing.T) {
	type inner struct {
		X int `json:"x"`
	}
	v := inner{X: 5}
	fv := reflect.ValueOf(v)
	got := coerceReflectToSQL(fv)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("coerceReflectToSQL(struct) returned %T, want string", got)
	}
	if s != `{"x":5}` {
		t.Errorf("coerceReflectToSQL(struct) = %q, want %q", s, `{"x":5}`)
	}
}

// ---------------------------------------------------------------------------
// Additional LookupPartitions coverage
// ---------------------------------------------------------------------------

func TestEntityTable_LookupPartitions_NonQRDBIndex(t *testing.T) {
	reg := testRegistration()
	idxSvc := &entityMockIndexServicer{}
	tbl := NewEntityTable(&mockKVService{}, "tenant1", reg, idxSvc)
	ctx := newTestContext()

	// Use a lookup with a non-qrdbIndex; should fall through to Partitions().
	lookup := sqle.IndexLookup{
		Index: &fakeIndex{},
	}
	iter, err := tbl.LookupPartitions(ctx, lookup)
	if err != nil {
		t.Fatalf("LookupPartitions() error = %v", err)
	}
	p, err := iter.Next(ctx)
	if err != nil {
		t.Fatalf("iter.Next() error = %v", err)
	}
	if string(p.Key()) != "default" {
		t.Errorf("partition Key() = %q, want default", p.Key())
	}
}

// fakeIndex is a non-qrdbIndex for testing the fallback path.
type fakeIndex struct{}

func (f *fakeIndex) ID() string                                         { return "fake" }
func (f *fakeIndex) Database() string                                   { return "db" }
func (f *fakeIndex) Table() string                                      { return "tbl" }
func (f *fakeIndex) Expressions() []string                              { return nil }
func (f *fakeIndex) IsUnique() bool                                     { return false }
func (f *fakeIndex) IsSpatial() bool                                    { return false }
func (f *fakeIndex) IsFullText() bool                                   { return false }
func (f *fakeIndex) IsVector() bool                                     { return false }
func (f *fakeIndex) Comment() string                                    { return "" }
func (f *fakeIndex) IndexType() string                                  { return "" }
func (f *fakeIndex) IsGenerated() bool                                  { return false }
func (f *fakeIndex) ColumnExpressionTypes() []sqle.ColumnExpressionType { return nil }
func (f *fakeIndex) CanSupport(_ *sqle.Context, _ ...sqle.Range) bool   { return false }
func (f *fakeIndex) CanSupportOrderBy(_ sqle.Expression) bool           { return false }
func (f *fakeIndex) PrefixLengths() []uint16                            { return nil }

// ---------------------------------------------------------------------------
// Additional indexedPartitionRows coverage: unmarshal error
// ---------------------------------------------------------------------------

func TestEntityTable_IndexedPartitionRows_SkipsMalformedData(t *testing.T) {
	svc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return []byte("not-valid-json{{{"), nil
		},
	}
	reg := testRegistration()
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	ip := &indexedPartition{keys: [][]byte{[]byte("users/1")}}
	iter, err := tbl.PartitionRows(ctx, ip)
	if err != nil {
		t.Fatalf("PartitionRows() error = %v", err)
	}

	_, err = iter.Next(ctx)
	if err != io.EOF {
		t.Errorf("expected io.EOF after skipping malformed data, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional entitySQLType coverage: time.Time
// ---------------------------------------------------------------------------

func TestEntitySQLType_Datetime(t *testing.T) {
	meta := entity.FieldMeta{
		GoType: reflect.TypeOf((*interface{})(nil)).Elem(), // force default
		GoKind: reflect.Interface,
	}
	// This will hit the default/VARCHAR path
	result := entitySQLType(meta)
	if result == nil {
		t.Error("entitySQLType returned nil for interface type")
	}
}

// ---------------------------------------------------------------------------
// Additional Insert coverage: marshal error
// ---------------------------------------------------------------------------

func TestEntityTable_Insert_MarshalError(t *testing.T) {
	reg := testRegistration()
	reg.MarshalFunc = func(v any) ([]byte, error) {
		return nil, fmt.Errorf("marshal failure")
	}

	svc := &mockKVService{}
	tbl := NewEntityTable(svc, "tenant1", reg, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{uint64(1), false, int64(0), "", "", float64(0)})
	if err == nil {
		t.Fatal("expected error from marshal failure, got nil")
	}
	if _, ok := err.(*InvalidValueError); !ok {
		t.Errorf("expected *InvalidValueError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Interface compliance tests
// ---------------------------------------------------------------------------

func TestEntityTable_ImplementsTable(t *testing.T) {
	var _ sqle.Table = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsInsertableTable(t *testing.T) {
	var _ sqle.InsertableTable = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsDeletableTable(t *testing.T) {
	var _ sqle.DeletableTable = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsUpdatableTable(t *testing.T) {
	var _ sqle.UpdatableTable = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsReplaceableTable(t *testing.T) {
	var _ sqle.ReplaceableTable = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsIndexAddressableTable(t *testing.T) {
	var _ sqle.IndexAddressableTable = (*EntityTable)(nil)
}

func TestEntityTable_ImplementsIndexedTable(t *testing.T) {
	var _ sqle.IndexedTable = (*EntityTable)(nil)
}

func TestEntityWriter_ImplementsRowInserter(t *testing.T) {
	var _ sqle.RowInserter = (*entityWriter)(nil)
}

func TestEntityWriter_ImplementsRowDeleter(t *testing.T) {
	var _ sqle.RowDeleter = (*entityWriter)(nil)
}

func TestEntityWriter_ImplementsRowUpdater(t *testing.T) {
	var _ sqle.RowUpdater = (*entityWriter)(nil)
}

func TestEntityWriter_ImplementsRowReplacer(t *testing.T) {
	var _ sqle.RowReplacer = (*entityWriter)(nil)
}
