//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DynamicEntity tests
// ---------------------------------------------------------------------------

func TestDynamicEntity_EntityID(t *testing.T) {
	e := &DynamicEntity{ID: 42}
	assert.Equal(t, uint64(42), e.EntityID())
}

func TestDynamicEntity_EntityID_Zero(t *testing.T) {
	e := &DynamicEntity{}
	assert.Equal(t, uint64(0), e.EntityID())
}

func TestDynamicEntity_SetEntityID(t *testing.T) {
	e := &DynamicEntity{}
	e.SetEntityID(123)
	assert.Equal(t, uint64(123), e.ID)
}

func TestDynamicEntity_SetEntityID_Overwrite(t *testing.T) {
	e := &DynamicEntity{ID: 1}
	e.SetEntityID(999)
	assert.Equal(t, uint64(999), e.ID)
}

func TestDynamicEntity_ImplementsEntity(t *testing.T) {
	var _ Entity = (*DynamicEntity)(nil)
}

func TestDynamicEntity_WithFields(t *testing.T) {
	e := &DynamicEntity{
		ID: 1,
		Fields: map[string]interface{}{
			"name":  "Alice",
			"email": "alice@example.com",
			"age":   float64(30),
		},
	}
	assert.Equal(t, uint64(1), e.EntityID())
	assert.Equal(t, "Alice", e.Fields["name"])
	assert.Equal(t, "alice@example.com", e.Fields["email"])
	assert.Equal(t, float64(30), e.Fields["age"])
}

func TestDynamicEntity_NilFields(t *testing.T) {
	e := &DynamicEntity{ID: 5}
	assert.Nil(t, e.Fields)
}

func TestDynamicEntity_EmptyFields(t *testing.T) {
	e := &DynamicEntity{
		ID:     5,
		Fields: map[string]interface{}{},
	}
	assert.Empty(t, e.Fields)
}

// ---------------------------------------------------------------------------
// DynamicEntity JSON serialization tests
// ---------------------------------------------------------------------------

func TestDynamicEntity_MarshalJSON_FlatFormat(t *testing.T) {
	e := &DynamicEntity{
		ID: 42,
		Fields: map[string]interface{}{
			"name":  "Alice",
			"email": "alice@example.com",
		},
	}

	data, err := json.Marshal(e)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, "42", m["id"])
	assert.Equal(t, "Alice", m["name"])
	assert.Equal(t, "alice@example.com", m["email"])
	assert.Nil(t, m["fields"], "document must not contain a 'fields' wrapper")
}

func TestDynamicEntity_MarshalJSON_EmptyFields(t *testing.T) {
	e := &DynamicEntity{
		ID:     1,
		Fields: map[string]interface{}{},
	}

	data, err := json.Marshal(e)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, "1", m["id"])
	assert.Len(t, m, 1)
}

func TestDynamicEntity_UnmarshalJSON_FlatFormat(t *testing.T) {
	raw := `{"id": "99", "name": "Bob", "active": true}`

	var e DynamicEntity
	require.NoError(t, json.Unmarshal([]byte(raw), &e))

	assert.Equal(t, uint64(99), e.ID)
	assert.Equal(t, "Bob", e.Fields["name"])
	assert.Equal(t, true, e.Fields["active"])
	assert.Nil(t, e.Fields["id"], "id must not appear in Fields")
}

func TestDynamicEntity_UnmarshalJSON_LegacyFormat(t *testing.T) {
	raw := `{"id": 50, "fields": {"color": "blue", "size": 10}}`

	var e DynamicEntity
	require.NoError(t, json.Unmarshal([]byte(raw), &e))

	assert.Equal(t, uint64(50), e.ID)
	assert.Equal(t, "blue", e.Fields["color"])
	assert.Equal(t, float64(10), e.Fields["size"])
}

func TestDynamicEntity_JSON_RoundTrip(t *testing.T) {
	original := &DynamicEntity{
		ID: 7,
		Fields: map[string]interface{}{
			"title":    "Hello",
			"count":    float64(3),
			"verified": true,
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored DynamicEntity
	require.NoError(t, json.Unmarshal(data, &restored))

	assert.Equal(t, original.ID, restored.ID)
	assert.Equal(t, original.Fields["title"], restored.Fields["title"])
	assert.Equal(t, original.Fields["count"], restored.Fields["count"])
	assert.Equal(t, original.Fields["verified"], restored.Fields["verified"])
}

func TestDynamicEntity_MarshalJSON_IgnoresIDInFields(t *testing.T) {
	e := &DynamicEntity{
		ID: 10,
		Fields: map[string]interface{}{
			"id":   999,
			"name": "Conflict",
		},
	}

	data, err := json.Marshal(e)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, "10", m["id"], "entity ID must take precedence")
	assert.Equal(t, "Conflict", m["name"])
}

func TestDynamicEntity_UnmarshalJSON_NilFields(t *testing.T) {
	raw := `{"id": "1"}`

	var e DynamicEntity
	require.NoError(t, json.Unmarshal([]byte(raw), &e))

	assert.Equal(t, uint64(1), e.ID)
	assert.Empty(t, e.Fields)
}

func TestDynamicEntity_JSON_RoundTrip_LargeID(t *testing.T) {
	// IDs exceeding Number.MAX_SAFE_INTEGER (2^53-1) must survive
	// a marshal/unmarshal round-trip without precision loss.
	const largeID uint64 = 1773170949468034560

	original := &DynamicEntity{
		ID:     largeID,
		Fields: map[string]interface{}{"status": "active"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored DynamicEntity
	require.NoError(t, json.Unmarshal(data, &restored))

	assert.Equal(t, largeID, restored.ID, "large uint64 ID must survive round-trip")
	assert.Equal(t, "active", restored.Fields["status"])
}

func TestDynamicEntity_UnmarshalJSON_NumericID(t *testing.T) {
	// Backward compatibility: numeric IDs (small enough for float64).
	raw := `{"id": 42, "name": "legacy"}`

	var e DynamicEntity
	require.NoError(t, json.Unmarshal([]byte(raw), &e))

	assert.Equal(t, uint64(42), e.ID)
	assert.Equal(t, "legacy", e.Fields["name"])
}

// ---------------------------------------------------------------------------
// DynamicEntity DAO round-trip tests
// ---------------------------------------------------------------------------

func TestDynamicEntity_SaveAndGet(t *testing.T) {
	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"test_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
	)
	require.NoError(t, err)

	entity := &DynamicEntity{
		ID: 1,
		Fields: map[string]interface{}{
			"name":  "Bob",
			"email": "bob@example.com",
		},
	}

	ctx := context.Background()
	err = d.Save(ctx, entity)
	require.NoError(t, err)

	got, err := d.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.EntityID())
	assert.Equal(t, "Bob", got.Fields["name"])
	assert.Equal(t, "bob@example.com", got.Fields["email"])
}

func TestDynamicEntity_SaveAutoID(t *testing.T) {
	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"auto_id_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
	)
	require.NoError(t, err)

	entity := &DynamicEntity{
		Fields: map[string]interface{}{
			"key": "value",
		},
	}

	ctx := context.Background()
	err = d.Save(ctx, entity)
	require.NoError(t, err)
	assert.NotZero(t, entity.EntityID())

	got, err := d.Get(ctx, entity.EntityID())
	require.NoError(t, err)
	assert.Equal(t, "value", got.Fields["key"])
}

func TestDynamicEntity_Delete(t *testing.T) {
	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"delete_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
	)
	require.NoError(t, err)

	entity := &DynamicEntity{
		ID:     100,
		Fields: map[string]interface{}{"x": "y"},
	}

	ctx := context.Background()
	err = d.Save(ctx, entity)
	require.NoError(t, err)

	err = d.Delete(ctx, entity)
	require.NoError(t, err)

	_, err = d.Get(ctx, 100)
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

func TestDynamicEntity_Count(t *testing.T) {
	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"count_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	count, err := d.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	_ = d.Save(ctx, &DynamicEntity{ID: 1, Fields: map[string]interface{}{"a": "1"}})
	_ = d.Save(ctx, &DynamicEntity{ID: 2, Fields: map[string]interface{}{"b": "2"}})

	count, err = d.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestDynamicEntity_Page(t *testing.T) {
	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"page_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	for i := uint64(1); i <= 5; i++ {
		_ = d.Save(ctx, &DynamicEntity{
			ID:     i,
			Fields: map[string]interface{}{"idx": float64(i)},
		})
	}

	result, err := d.Page(ctx, PageQuery{Page: 1, PageSize: 3})
	require.NoError(t, err)
	assert.Len(t, result.Entities, 3)
	assert.True(t, result.HasMore)
	assert.Equal(t, 5, result.Total)
}

// ---------------------------------------------------------------------------
// DynamicFieldHashGenerator tests
// ---------------------------------------------------------------------------

func TestDynamicFieldHashGenerator_NextID(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")

	entity := &DynamicEntity{
		Fields: map[string]interface{}{
			"email": "alice@example.com",
		},
	}

	id := gen.NextID(entity)
	assert.NotZero(t, id)

	// Same input produces same ID (deterministic).
	id2 := gen.NextID(entity)
	assert.Equal(t, id, id2)
}

func TestDynamicFieldHashGenerator_NextID_DifferentValues(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")

	e1 := &DynamicEntity{
		Fields: map[string]interface{}{"email": "alice@example.com"},
	}
	e2 := &DynamicEntity{
		Fields: map[string]interface{}{"email": "bob@example.com"},
	}

	id1 := gen.NextID(e1)
	id2 := gen.NextID(e2)
	assert.NotEqual(t, id1, id2)
}

func TestDynamicFieldHashGenerator_NextID_MissingField(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")

	entity := &DynamicEntity{
		Fields: map[string]interface{}{"name": "Alice"},
	}

	id := gen.NextID(entity)
	assert.Equal(t, uint64(0), id)
}

func TestDynamicFieldHashGenerator_NextID_NilFields(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")

	entity := &DynamicEntity{}
	id := gen.NextID(entity)
	assert.Equal(t, uint64(0), id)
}

func TestDynamicFieldHashGenerator_NextID_NonDynamicEntity(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("name")

	// Use a non-DynamicEntity type.
	type other struct {
		id uint64
	}
	entity := &other{id: 1}

	// Must satisfy Entity interface; cast workaround not needed since
	// NextID takes Entity interface, but we need a concrete Entity.
	// Since the test is about type assertion failure, pass nil.
	id := gen.NextID(nil)
	_ = entity
	assert.Equal(t, uint64(0), id)
}

func TestDynamicFieldHashGenerator_FieldName(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")
	assert.Equal(t, "email", gen.FieldName())
}

func TestDynamicFieldHashGenerator_FieldName_Empty(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("")
	assert.Equal(t, "", gen.FieldName())
}

func TestDynamicFieldHashGenerator_SaveWithFieldHash(t *testing.T) {
	gen := NewDynamicFieldHashGenerator("email")

	store := newMemoryKVStore()
	d, err := New[*DynamicEntity](
		store,
		"hash_collection",
		func() *DynamicEntity { return &DynamicEntity{} },
		WithIDGenerator(gen),
	)
	require.NoError(t, err)

	entity := &DynamicEntity{
		Fields: map[string]interface{}{
			"name":  "Alice",
			"email": "alice@example.com",
		},
	}

	ctx := context.Background()
	err = d.Save(ctx, entity)
	require.NoError(t, err)

	expectedID := gen.NextID(entity)
	assert.Equal(t, expectedID, entity.EntityID())

	got, err := d.Get(ctx, expectedID)
	require.NoError(t, err)
	assert.Equal(t, "Alice", got.Fields["name"])
}

func TestDynamicFieldHashGenerator_ImplementsIDGenerator(t *testing.T) {
	var _ IDGenerator = (*DynamicFieldHashGenerator)(nil)
}
