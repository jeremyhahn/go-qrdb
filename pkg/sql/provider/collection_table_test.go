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
	"io"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockCollectionService
// ---------------------------------------------------------------------------

type mockCollectionService struct {
	createFn   func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropFn     func(ctx context.Context, req *service.DropCollectionRequest) error
	listColFn  func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getInfoFn  func(ctx context.Context, db, name string) (*service.CollectionInfo, error)
	insertFn   func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getFn      func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateFn   func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteFn   func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocsFn func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return nil, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listColFn != nil {
		return m.listColFn(ctx, req)
	}
	return &service.ListCollectionsResponse{}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, db, name string) (*service.CollectionInfo, error) {
	if m.getInfoFn != nil {
		return m.getInfoFn(ctx, db, name)
	}
	return nil, &service.CollectionNotFoundError{Database: db, Collection: name}
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertFn != nil {
		return m.insertFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 1}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getFn != nil {
		return m.getFn(ctx, req)
	}
	return nil, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocsFn != nil {
		return m.listDocsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{Documents: []service.GetDocumentResponse{}}, nil
}

// ---------------------------------------------------------------------------
// NewCollectionTable tests
// ---------------------------------------------------------------------------

func TestNewCollectionTable(t *testing.T) {
	svc := &mockCollectionService{}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{
		"name":  "VARCHAR",
		"email": "VARCHAR",
	}, nil)

	require.NotNil(t, tbl)
	assert.Equal(t, "users", tbl.Name())
	assert.Equal(t, "users", tbl.String())
}

func TestCollectionTable_Schema(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", map[string]string{
		"name":  "VARCHAR",
		"age":   "DOUBLE",
		"email": "VARCHAR",
	}, nil)

	schema := tbl.Schema(newTestContext())

	// _id + 3 fields = 4 columns
	require.Len(t, schema, 4)
	assert.Equal(t, "_id", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
	// Fields are alphabetically sorted: age, email, name
	assert.Equal(t, "age", schema[1].Name)
	assert.True(t, schema[1].Nullable)
	assert.Equal(t, "email", schema[2].Name)
	assert.Equal(t, "name", schema[3].Name)
}

func TestCollectionTable_Schema_Empty(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "empty", map[string]string{}, nil)

	schema := tbl.Schema(newTestContext())
	require.Len(t, schema, 1) // only _id
	assert.Equal(t, "_id", schema[0].Name)
}

func TestCollectionTable_Collation(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestCollectionTable_Partitions(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("default"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

// ---------------------------------------------------------------------------
// PartitionRows tests
// ---------------------------------------------------------------------------

func TestCollectionTable_PartitionRows_Empty(t *testing.T) {
	svc := &mockCollectionService{
		listDocsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{Documents: []service.GetDocumentResponse{}}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &collectionPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestCollectionTable_PartitionRows_WithData(t *testing.T) {
	svc := &mockCollectionService{
		listDocsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{
				Documents: []service.GetDocumentResponse{
					{ID: 1, Document: map[string]interface{}{"name": "Alice", "age": float64(30)}},
					{ID: 2, Document: map[string]interface{}{"name": "Bob"}},
				},
			}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{
		"name": "VARCHAR",
		"age":  "DOUBLE",
	}, nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &collectionPartition{})
	require.NoError(t, err)

	row1, err := iter.Next(ctx)
	require.NoError(t, err)
	// [_id, age, name] (fields sorted alphabetically)
	assert.Equal(t, uint64(1), row1[0])
	assert.Equal(t, float64(30), row1[1]) // age
	assert.Equal(t, "Alice", row1[2])     // name

	row2, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), row2[0])
	assert.Nil(t, row2[1])          // age is nil (missing)
	assert.Equal(t, "Bob", row2[2]) // name

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestCollectionTable_PartitionRows_ListError(t *testing.T) {
	listErr := &service.CollectionNotFoundError{Database: "mydb", Collection: "users"}
	svc := &mockCollectionService{
		listDocsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, listErr
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", nil, nil)
	ctx := newTestContext()

	_, err := tbl.PartitionRows(ctx, &collectionPartition{})
	assert.Equal(t, listErr, err)
}

// ---------------------------------------------------------------------------
// Inserter tests
// ---------------------------------------------------------------------------

func TestCollectionTable_Insert(t *testing.T) {
	var insertedDoc map[string]interface{}
	svc := &mockCollectionService{
		insertFn: func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			insertedDoc = req.Document
			return &service.InsertDocumentResponse{ID: 42}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{
		"email": "VARCHAR",
		"name":  "VARCHAR",
	}, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	require.NotNil(t, inserter)

	// Row: [_id, email, name] (sorted field order)
	err := inserter.Insert(ctx, sqle.Row{nil, "alice@example.com", "Alice"})
	require.NoError(t, err)

	assert.Equal(t, "alice@example.com", insertedDoc["email"])
	assert.Equal(t, "Alice", insertedDoc["name"])
}

func TestCollectionTable_Insert_Error(t *testing.T) {
	insertErr := &service.CollectionNotFoundError{Database: "mydb", Collection: "users"}
	svc := &mockCollectionService{
		insertFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, insertErr
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{nil, "Alice"})
	assert.Equal(t, insertErr, err)
}

// ---------------------------------------------------------------------------
// Deleter tests
// ---------------------------------------------------------------------------

func TestCollectionTable_Delete(t *testing.T) {
	var deletedID uint64
	svc := &mockCollectionService{
		deleteFn: func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			deletedID = req.ID
			return &service.DeleteDocumentResponse{Deleted: true}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{uint64(42), "Alice"})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), deletedID)
}

func TestCollectionTable_Delete_EmptyRow(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestCollectionTable_Delete_NilID(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{nil})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// Updater tests
// ---------------------------------------------------------------------------

func TestCollectionTable_Update(t *testing.T) {
	var updatedID uint64
	var updatedDoc map[string]interface{}
	svc := &mockCollectionService{
		updateFn: func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			updatedID = req.ID
			updatedDoc = req.Document
			return &service.UpdateDocumentResponse{ID: req.ID}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{uint64(1), "Old"},
		sqle.Row{uint64(1), "New"},
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), updatedID)
	assert.Equal(t, "New", updatedDoc["name"])
}

func TestCollectionTable_Update_KeyChanged(t *testing.T) {
	var deletedID uint64
	var updatedID uint64
	svc := &mockCollectionService{
		deleteFn: func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			deletedID = req.ID
			return &service.DeleteDocumentResponse{Deleted: true}, nil
		},
		updateFn: func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			updatedID = req.ID
			return &service.UpdateDocumentResponse{ID: req.ID}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{uint64(1), "Old"},
		sqle.Row{uint64(2), "New"},
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), deletedID)
	assert.Equal(t, uint64(2), updatedID)
}

func TestCollectionTable_Update_InvalidOldKey(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx, sqle.Row{}, sqle.Row{uint64(1)})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestCollectionTable_Update_InvalidNewKey(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx, sqle.Row{uint64(1)}, sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// Replacer tests
// ---------------------------------------------------------------------------

func TestCollectionTable_Replacer(t *testing.T) {
	svc := &mockCollectionService{
		insertFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return &service.InsertDocumentResponse{ID: 1}, nil
		},
	}
	tbl := NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	replacer := tbl.Replacer(ctx)
	require.NotNil(t, replacer)

	err := replacer.Insert(ctx, sqle.Row{nil, "Alice"})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// collectionWriter lifecycle tests
// ---------------------------------------------------------------------------

func TestCollectionWriter_StatementBegin(t *testing.T) {
	w := &collectionWriter{}
	ctx := newTestContext()
	w.StatementBegin(ctx) // should not panic
}

func TestCollectionWriter_DiscardChanges(t *testing.T) {
	w := &collectionWriter{}
	ctx := newTestContext()
	err := w.DiscardChanges(ctx, nil)
	assert.NoError(t, err)
}

func TestCollectionWriter_StatementComplete(t *testing.T) {
	w := &collectionWriter{}
	ctx := newTestContext()
	err := w.StatementComplete(ctx)
	assert.NoError(t, err)
}

func TestCollectionWriter_Close(t *testing.T) {
	w := &collectionWriter{}
	ctx := newTestContext()
	err := w.Close(ctx)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// extractDocID tests
// ---------------------------------------------------------------------------

func TestExtractDocID_Uint64(t *testing.T) {
	id, err := extractDocID(sqle.Row{uint64(42)})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), id)
}

func TestExtractDocID_Int64(t *testing.T) {
	id, err := extractDocID(sqle.Row{int64(42)})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), id)
}

func TestExtractDocID_Float64(t *testing.T) {
	id, err := extractDocID(sqle.Row{float64(42)})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), id)
}

func TestExtractDocID_String(t *testing.T) {
	id, err := extractDocID(sqle.Row{"42"})
	require.NoError(t, err)
	assert.Equal(t, uint64(42), id)
}

func TestExtractDocID_InvalidString(t *testing.T) {
	_, err := extractDocID(sqle.Row{"notanumber"})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestExtractDocID_Nil(t *testing.T) {
	_, err := extractDocID(sqle.Row{nil})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestExtractDocID_EmptyRow(t *testing.T) {
	_, err := extractDocID(sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestExtractDocID_UnsupportedType(t *testing.T) {
	_, err := extractDocID(sqle.Row{true})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// Schema builder tests
// ---------------------------------------------------------------------------

func TestBuildCollectionSchema_WithFields(t *testing.T) {
	schema, fields := buildCollectionSchema("users", map[string]string{
		"name":   "VARCHAR",
		"age":    "DOUBLE",
		"active": "BOOLEAN",
	})

	assert.Len(t, schema, 4) // _id + 3 fields
	assert.Equal(t, "_id", schema[0].Name)
	assert.Equal(t, []string{"active", "age", "name"}, fields)
}

func TestBuildCollectionSchema_Empty(t *testing.T) {
	schema, fields := buildCollectionSchema("empty", map[string]string{})
	assert.Len(t, schema, 1) // only _id
	assert.Empty(t, fields)
}

func TestBuildCollectionSchema_NilMap(t *testing.T) {
	schema, fields := buildCollectionSchema("nil", nil)
	assert.Len(t, schema, 1) // only _id
	assert.Empty(t, fields)
}

func TestSQLTypeFromString(t *testing.T) {
	tests := []struct {
		input  string
		notNil bool
	}{
		{"VARCHAR", true},
		{"DOUBLE", true},
		{"BOOLEAN", true},
		{"BIGINT", true},
		{"JSON", true},
		{"UNKNOWN", true}, // defaults to VARCHAR
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sqlTypeFromString(tt.input)
			assert.NotNil(t, result)
		})
	}
}

// ---------------------------------------------------------------------------
// coerceToSQL tests
// ---------------------------------------------------------------------------

func TestCoerceToSQL_Passthrough(t *testing.T) {
	assert.Equal(t, "hello", coerceToSQL("hello"))
	assert.Equal(t, float64(42), coerceToSQL(float64(42)))
	assert.Equal(t, true, coerceToSQL(true))
}

func TestCoerceToSQL_Map(t *testing.T) {
	result := coerceToSQL(map[string]interface{}{"a": "b"})
	assert.Equal(t, `{"a":"b"}`, result)
}

func TestCoerceToSQL_Slice(t *testing.T) {
	result := coerceToSQL([]interface{}{1, 2, 3})
	assert.Equal(t, `[1,2,3]`, result)
}

// ---------------------------------------------------------------------------
// collectionPartition tests
// ---------------------------------------------------------------------------

func TestCollectionPartition_Key(t *testing.T) {
	p := &collectionPartition{}
	assert.Equal(t, []byte("default"), p.Key())
}

// ---------------------------------------------------------------------------
// collectionRowIter tests
// ---------------------------------------------------------------------------

func TestCollectionRowIter_Empty(t *testing.T) {
	iter := &collectionRowIter{}
	ctx := newTestContext()
	_, err := iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestCollectionRowIter_IteratesAll(t *testing.T) {
	rows := []sqle.Row{
		{uint64(1), "a"},
		{uint64(2), "b"},
	}
	iter := &collectionRowIter{rows: rows}
	ctx := newTestContext()

	r1, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, rows[0], r1)

	r2, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, rows[1], r2)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestCollectionRowIter_Close(t *testing.T) {
	iter := &collectionRowIter{rows: []sqle.Row{{uint64(1)}}}
	ctx := newTestContext()
	err := iter.Close(ctx)
	require.NoError(t, err)
	assert.Nil(t, iter.rows)
}

// ---------------------------------------------------------------------------
// Interface compliance tests
// ---------------------------------------------------------------------------

func TestCollectionTable_ImplementsInsertableTable(t *testing.T) {
	var _ sqle.InsertableTable = (*CollectionTable)(nil)
}

func TestCollectionTable_ImplementsDeletableTable(t *testing.T) {
	var _ sqle.DeletableTable = (*CollectionTable)(nil)
}

func TestCollectionTable_ImplementsUpdatableTable(t *testing.T) {
	var _ sqle.UpdatableTable = (*CollectionTable)(nil)
}

func TestCollectionTable_ImplementsReplaceableTable(t *testing.T) {
	var _ sqle.ReplaceableTable = (*CollectionTable)(nil)
}

func TestCollectionWriter_ImplementsRowReplacer(t *testing.T) {
	var _ sqle.RowReplacer = (*collectionWriter)(nil)
}

// ---------------------------------------------------------------------------
// rowToDocument tests
// ---------------------------------------------------------------------------

func TestRowToDocument(t *testing.T) {
	w := &collectionWriter{
		fieldOrder: []string{"age", "email", "name"},
	}

	doc := w.rowToDocument(sqle.Row{uint64(1), float64(30), "alice@test.com", "Alice"})
	assert.Equal(t, float64(30), doc["age"])
	assert.Equal(t, "alice@test.com", doc["email"])
	assert.Equal(t, "Alice", doc["name"])
}

func TestRowToDocument_NilValues(t *testing.T) {
	w := &collectionWriter{
		fieldOrder: []string{"age", "name"},
	}

	doc := w.rowToDocument(sqle.Row{uint64(1), nil, "Bob"})
	_, ageExists := doc["age"]
	assert.False(t, ageExists)
	assert.Equal(t, "Bob", doc["name"])
}

func TestRowToDocument_ShortRow(t *testing.T) {
	w := &collectionWriter{
		fieldOrder: []string{"age", "name", "email"},
	}

	doc := w.rowToDocument(sqle.Row{uint64(1), float64(25)})
	assert.Equal(t, float64(25), doc["age"])
	_, nameExists := doc["name"]
	assert.False(t, nameExists)
}
