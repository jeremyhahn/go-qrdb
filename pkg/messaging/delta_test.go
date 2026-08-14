//go:build !integration

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

package messaging

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stderr",
	})
	require.NoError(t, err)
	return log
}

// testEntity is a simple struct used across tests.
type testEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// testEntityNested has nested struct fields.
type testEntityNested struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Address testAddress `json:"address"`
}

type testAddress struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

// testEntityWithPointer has a pointer field.
type testEntityWithPointer struct {
	ID      string       `json:"id"`
	Address *testAddress `json:"address"`
}

// testEntityWithSlice has a slice field.
type testEntityWithSlice struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// testEntityWithMap has a map field.
type testEntityWithMap struct {
	ID     string            `json:"id"`
	Labels map[string]string `json:"labels"`
}

// testEntityWithTime has a time.Time field.
type testEntityWithTime struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// testEntityWithUnexported has unexported fields that should be skipped.
type testEntityWithUnexported struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	internal string //nolint:unused // used for test
}

// testEntityWithJSONDash has a field with json:"-" tag.
type testEntityWithJSONDash struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"-"`
}

// testEntityWithOmitempty has a field with json omitempty tag.
type testEntityWithOmitempty struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// testEntityWithEmptyJSONTag has a field with empty json tag name.
type testEntityWithEmptyJSONTag struct {
	ID   string `json:",omitempty"`
	Name string `json:"name"`
}

// testEntityWithKey uses Key field instead of ID.
type testEntityWithKey struct {
	Key  string `json:"key"`
	Data string `json:"data"`
}

// testEntityWithEntityID uses EntityID field.
type testEntityWithEntityID struct {
	EntityID string `json:"entity_id"`
	Value    string `json:"value"`
}

// testEntityWithEntityId uses EntityId field.
type testEntityWithEntityId struct {
	EntityId string `json:"entity_id"` //nolint:revive // testing ID extraction
	Value    string `json:"value"`
}

// testEntityWithVersion implements Version() method.
type testEntityWithVersion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ver  uint64 `json:"-"`
}

func (e *testEntityWithVersion) Version() uint64 {
	return e.Ver
}

// testEntityNoID has no ID-like fields.
type testEntityNoID struct {
	Data  string `json:"data"`
	Count int    `json:"count"`
}

// testEntityDeepNested creates deep nesting for max depth tests.
type testDeepA struct {
	B testDeepB `json:"b"`
}
type testDeepB struct {
	C testDeepC `json:"c"`
}
type testDeepC struct {
	Value string `json:"value"`
}

// diffableEntity implements the Diffable interface.
type diffableEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ver  uint64
}

func (d *diffableEntity) Diff(previous interface{}) ([]FieldChange, error) {
	if previous == nil {
		return []FieldChange{
			{Path: "id", Operation: OpSet, Value: d.ID},
			{Path: "name", Operation: OpSet, Value: d.Name},
		}, nil
	}
	old, ok := previous.(*diffableEntity)
	if !ok {
		return nil, errors.New("type assertion failed")
	}
	var changes []FieldChange
	if d.ID != old.ID {
		changes = append(changes, FieldChange{
			Path: "id", Operation: OpSet, Value: d.ID, OldValue: old.ID,
		})
	}
	if d.Name != old.Name {
		changes = append(changes, FieldChange{
			Path: "name", Operation: OpSet, Value: d.Name, OldValue: old.Name,
		})
	}
	return changes, nil
}

func (d *diffableEntity) Apply(_ []FieldChange) error {
	return nil
}

func (d *diffableEntity) Version() uint64 {
	return d.Ver
}

// failingDiffable always returns an error from Diff.
type failingDiffable struct {
	ID  string `json:"id"`
	Ver uint64
}

func (f *failingDiffable) Diff(_ interface{}) ([]FieldChange, error) {
	return nil, errors.New("diff computation failed")
}

func (f *failingDiffable) Apply(_ []FieldChange) error {
	return nil
}

func (f *failingDiffable) Version() uint64 {
	return f.Ver
}

// testEntityWithMapOfStructs has a map value of struct type.
type testEntityWithMapOfStructs struct {
	ID    string                 `json:"id"`
	Items map[string]testAddress `json:"items"`
}

// testEntityWithInterface has interface fields.
type testEntityWithInterface struct {
	ID    string      `json:"id"`
	Value interface{} `json:"value"`
}

// ---------------------------------------------------------------------------
// OpType.String()
// ---------------------------------------------------------------------------

func TestOpType_String_AllConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   OpType
		want string
	}{
		{name: "OpSet", op: OpSet, want: "set"},
		{name: "OpDelete", op: OpDelete, want: "delete"},
		{name: "OpAppend", op: OpAppend, want: "append"},
		{name: "OpIncrement", op: OpIncrement, want: "increment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.op.String())
		})
	}
}

func TestOpType_String_CustomValue(t *testing.T) {
	t.Parallel()
	op := OpType("custom_op")
	assert.Equal(t, "custom_op", op.String())
}

// ---------------------------------------------------------------------------
// DeltaPayload and FieldChange JSON serialization
// ---------------------------------------------------------------------------

func TestDeltaPayload_JSON_RoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Millisecond)
	payload := &DeltaPayload{
		EntityID:    "entity-1",
		EntityType:  "testEntity",
		Version:     2,
		PrevVersion: 1,
		Timestamp:   now,
		Changes: []FieldChange{
			{Path: "name", Operation: OpSet, Value: "new-name", OldValue: "old-name"},
		},
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded DeltaPayload
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, payload.EntityID, decoded.EntityID)
	assert.Equal(t, payload.EntityType, decoded.EntityType)
	assert.Equal(t, payload.Version, decoded.Version)
	assert.Equal(t, payload.PrevVersion, decoded.PrevVersion)
	assert.Equal(t, 1, len(decoded.Changes))
	assert.Equal(t, "name", decoded.Changes[0].Path)
	assert.Equal(t, OpSet, decoded.Changes[0].Operation)
}

func TestFieldChange_JSON_DeleteOmitsValue(t *testing.T) {
	t.Parallel()
	fc := FieldChange{
		Path:      "field",
		Operation: OpDelete,
		OldValue:  "removed",
	}
	data, err := json.Marshal(fc)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "field", decoded["path"])
	assert.Equal(t, "delete", decoded["op"])
	_, hasValue := decoded["value"]
	assert.False(t, hasValue, "delete operations should omit value via omitempty")
}

// ---------------------------------------------------------------------------
// NewDeltaComputer
// ---------------------------------------------------------------------------

func TestNewDeltaComputer_DefaultOptions(t *testing.T) {
	t.Parallel()
	log := newTestLogger(t)
	dc := NewDeltaComputer(log)
	assert.NotNil(t, dc)
	assert.Equal(t, defaultDeltaMaxDepth, dc.maxDepth)
	assert.True(t, dc.ignoreZero)
}

func TestNewDeltaComputer_WithOptions(t *testing.T) {
	t.Parallel()
	log := newTestLogger(t)
	dc := NewDeltaComputer(log,
		WithDeltaMaxDepth(5),
		WithDeltaIgnoreZeroChanges(false),
	)
	assert.Equal(t, 5, dc.maxDepth)
	assert.False(t, dc.ignoreZero)
}

func TestNewDeltaComputer_WithDeltaMaxDepth_IgnoresNonPositive(t *testing.T) {
	t.Parallel()
	log := newTestLogger(t)
	dc := NewDeltaComputer(log, WithDeltaMaxDepth(0))
	assert.Equal(t, defaultDeltaMaxDepth, dc.maxDepth)

	dc2 := NewDeltaComputer(log, WithDeltaMaxDepth(-1))
	assert.Equal(t, defaultDeltaMaxDepth, dc2.maxDepth)
}

func TestNewDeltaComputer_WithDeltaIgnoreZeroChanges_True(t *testing.T) {
	t.Parallel()
	log := newTestLogger(t)
	dc := NewDeltaComputer(log, WithDeltaIgnoreZeroChanges(true))
	assert.True(t, dc.ignoreZero)
}

// ---------------------------------------------------------------------------
// Compute - nil / error inputs
// ---------------------------------------------------------------------------

func TestCompute_NilNewEntity_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.Compute(nil, nil)
	require.Error(t, err)

	var nilErr *DeltaNilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, "new entity", nilErr.Parameter)
}

func TestCompute_NilPointerNewEntity_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var entity *testEntity
	_, err := dc.Compute(nil, entity)
	require.Error(t, err)

	var nilErr *DeltaNilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Contains(t, nilErr.Parameter, "nil pointer")
}

func TestCompute_TypeMismatch_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := &testAddress{City: "NYC"}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var mismatch *DeltaTypeMismatchError
	require.True(t, errors.As(err, &mismatch))
}

func TestCompute_NilOldPointer_TreatedAsNewEntity(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var old *testEntity
	newVal := &testEntity{ID: "1", Name: "alice", Age: 30}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	require.NotNil(t, payload)

	assert.Equal(t, "1", payload.EntityID)
	assert.Equal(t, "testEntity", payload.EntityType)
	assert.GreaterOrEqual(t, len(payload.Changes), 2) // at least id and name
}

// ---------------------------------------------------------------------------
// Compute - new entity (old = nil)
// ---------------------------------------------------------------------------

func TestCompute_NewEntity_AllFieldsAsSet(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntity{ID: "1", Name: "alice", Age: 30}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)
	require.NotNil(t, payload)

	assert.Equal(t, "1", payload.EntityID)
	assert.Equal(t, "testEntity", payload.EntityType)
	assert.Equal(t, uint64(0), payload.PrevVersion)
	assert.False(t, payload.Timestamp.IsZero())

	// All non-zero fields should be set operations
	pathMap := make(map[string]FieldChange)
	for _, c := range payload.Changes {
		pathMap[c.Path] = c
	}

	require.Contains(t, pathMap, "id")
	assert.Equal(t, OpSet, pathMap["id"].Operation)
	assert.Equal(t, "1", pathMap["id"].Value)

	require.Contains(t, pathMap, "name")
	assert.Equal(t, "alice", pathMap["name"].Value)

	require.Contains(t, pathMap, "age")
	assert.Equal(t, 30, pathMap["age"].Value)
}

func TestCompute_NewEntity_IgnoresZeroFields(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	entity := &testEntity{ID: "1", Name: "", Age: 0}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	pathMap := make(map[string]FieldChange)
	for _, c := range payload.Changes {
		pathMap[c.Path] = c
	}

	require.Contains(t, pathMap, "id")
	assert.NotContains(t, pathMap, "name")
	assert.NotContains(t, pathMap, "age")
}

func TestCompute_NewEntity_IncludesZeroFieldsWhenConfigured(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(false))
	entity := &testEntity{ID: "1", Name: "", Age: 0}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}

	assert.True(t, paths["id"])
	assert.True(t, paths["name"])
	assert.True(t, paths["age"])
}

// ---------------------------------------------------------------------------
// Compute - simple struct diffs
// ---------------------------------------------------------------------------

func TestCompute_SimpleStructDiff_DetectsChangedField(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 30}
	newVal := &testEntity{ID: "1", Name: "bob", Age: 30}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
	assert.Equal(t, OpSet, payload.Changes[0].Operation)
	assert.Equal(t, "bob", payload.Changes[0].Value)
	assert.Equal(t, "alice", payload.Changes[0].OldValue)
}

func TestCompute_SimpleStructDiff_NoChanges_EmptyPayload(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 30}
	newVal := &testEntity{ID: "1", Name: "alice", Age: 30}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	assert.Empty(t, payload.Changes)
}

func TestCompute_SimpleStructDiff_MultipleChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 30}
	newVal := &testEntity{ID: "1", Name: "bob", Age: 40}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	assert.Len(t, payload.Changes, 2)

	pathMap := make(map[string]FieldChange)
	for _, c := range payload.Changes {
		pathMap[c.Path] = c
	}
	assert.Contains(t, pathMap, "name")
	assert.Contains(t, pathMap, "age")
}

// ---------------------------------------------------------------------------
// Compute - nested struct diffs
// ---------------------------------------------------------------------------

func TestCompute_NestedStruct_DetectsNestedChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityNested{
		ID: "1", Name: "alice",
		Address: testAddress{City: "NYC", Country: "US"},
	}
	newVal := &testEntityNested{
		ID: "1", Name: "alice",
		Address: testAddress{City: "LA", Country: "US"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "address.city", payload.Changes[0].Path)
	assert.Equal(t, "LA", payload.Changes[0].Value)
	assert.Equal(t, "NYC", payload.Changes[0].OldValue)
}

func TestCompute_NestedStruct_NewEntity_FlattensFields(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityNested{
		ID: "1", Name: "alice",
		Address: testAddress{City: "NYC", Country: "US"},
	}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}

	assert.True(t, paths["id"])
	assert.True(t, paths["name"])
	assert.True(t, paths["address.city"])
	assert.True(t, paths["address.country"])
}

// ---------------------------------------------------------------------------
// Compute - pointer fields
// ---------------------------------------------------------------------------

func TestCompute_PointerField_BothNonNil_DetectsChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "NYC", Country: "US"},
	}
	newVal := &testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "LA", Country: "US"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "address.city", payload.Changes[0].Path)
}

func TestCompute_PointerField_OldNilNewNonNil_DetectsAdd(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithPointer{ID: "1", Address: nil}
	newVal := &testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "NYC", Country: "US"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(payload.Changes), 1)
	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["address.city"])
}

func TestCompute_PointerField_OldNonNilNewNil_DetectsDelete(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "NYC", Country: "US"},
	}
	newVal := &testEntityWithPointer{ID: "1", Address: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(payload.Changes), 1)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "address" && c.Operation == OpDelete {
			found = true
			break
		}
	}
	assert.True(t, found, "expected delete operation for address pointer")
}

func TestCompute_PointerField_BothNil_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithPointer{ID: "1", Address: nil}
	newVal := &testEntityWithPointer{ID: "1", Address: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

// ---------------------------------------------------------------------------
// Compute - pointer to struct in new entity (extractAllFields)
// ---------------------------------------------------------------------------

func TestCompute_NewEntity_PointerToStruct_ExtractsFields(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "NYC", Country: "US"},
	}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}

	assert.True(t, paths["id"])
	assert.True(t, paths["address.city"])
	assert.True(t, paths["address.country"])
}

func TestCompute_NewEntity_NilPointerField_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithPointer{ID: "1", Address: nil}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}

	assert.True(t, paths["id"])
	assert.NotContains(t, paths, "address.city")
}

// ---------------------------------------------------------------------------
// Compute - slices
// ---------------------------------------------------------------------------

func TestCompute_SliceDiff_DetectsChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithSlice{ID: "1", Tags: []string{"a", "b"}}
	newVal := &testEntityWithSlice{ID: "1", Tags: []string{"a", "c"}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "tags", payload.Changes[0].Path)
	assert.Equal(t, OpSet, payload.Changes[0].Operation)
}

func TestCompute_SliceDiff_IdenticalSlices_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithSlice{ID: "1", Tags: []string{"a", "b"}}
	newVal := &testEntityWithSlice{ID: "1", Tags: []string{"a", "b"}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

func TestCompute_SliceDiff_EmptyToNonEmpty(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(false))
	old := &testEntityWithSlice{ID: "1", Tags: nil}
	newVal := &testEntityWithSlice{ID: "1", Tags: []string{"x"}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "tags" {
			found = true
			assert.Equal(t, OpSet, c.Operation)
		}
	}
	assert.True(t, found, "expected change for tags field")
}

func TestCompute_SliceDiff_NonEmptyToNil_DetectsDelete(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithSlice{ID: "1", Tags: []string{"a"}}
	newVal := &testEntityWithSlice{ID: "1", Tags: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "tags" {
			found = true
			assert.Equal(t, OpDelete, c.Operation)
		}
	}
	assert.True(t, found, "expected delete for tags going from non-empty to nil")
}

func TestCompute_SliceDiff_BothNilWithIgnoreZero_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	old := &testEntityWithSlice{ID: "1", Tags: nil}
	newVal := &testEntityWithSlice{ID: "1", Tags: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

// ---------------------------------------------------------------------------
// Compute - maps
// ---------------------------------------------------------------------------

func TestCompute_MapDiff_KeyAdded(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod"},
	}
	newVal := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod", "team": "backend"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "labels.team", payload.Changes[0].Path)
	assert.Equal(t, OpSet, payload.Changes[0].Operation)
	assert.Equal(t, "backend", payload.Changes[0].Value)
}

func TestCompute_MapDiff_KeyDeleted(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod", "team": "backend"},
	}
	newVal := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "labels.team", payload.Changes[0].Path)
	assert.Equal(t, OpDelete, payload.Changes[0].Operation)
	assert.Equal(t, "backend", payload.Changes[0].OldValue)
}

func TestCompute_MapDiff_ValueModified(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod"},
	}
	newVal := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "staging"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "labels.env", payload.Changes[0].Path)
	assert.Equal(t, "staging", payload.Changes[0].Value)
	assert.Equal(t, "prod", payload.Changes[0].OldValue)
}

func TestCompute_MapDiff_BothEmpty_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{ID: "1", Labels: map[string]string{}}
	newVal := &testEntityWithMap{ID: "1", Labels: map[string]string{}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

func TestCompute_MapDiff_NilToNonNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{ID: "1", Labels: nil}
	newVal := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"key": "value"},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(payload.Changes), 1)
	found := false
	for _, c := range payload.Changes {
		if c.Path == "labels.key" && c.Operation == OpSet {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCompute_MapDiff_NonNilToNil_DetectsDeletes(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"a": "1"},
	}
	newVal := &testEntityWithMap{ID: "1", Labels: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "labels.a" && c.Operation == OpDelete {
			found = true
		}
	}
	assert.True(t, found)
}

// ---------------------------------------------------------------------------
// Compute - map with struct values
// ---------------------------------------------------------------------------

func TestCompute_MapOfStructs_NewKeyAdded(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMapOfStructs{
		ID:    "1",
		Items: map[string]testAddress{},
	}
	newVal := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "NYC", Country: "US"},
		},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["items.home.city"])
	assert.True(t, paths["items.home.country"])
}

func TestCompute_MapOfStructs_ValueChanged(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "NYC", Country: "US"},
		},
	}
	newVal := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "LA", Country: "US"},
		},
	}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "items.home.city", payload.Changes[0].Path)
}

// ---------------------------------------------------------------------------
// Compute - new entity with map
// ---------------------------------------------------------------------------

func TestCompute_NewEntity_Map_ExtractedAsWholeValue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithMap{
		ID:     "1",
		Labels: map[string]string{"env": "prod", "team": "backend"},
	}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["id"])
	// Map fields in structs are extracted as whole values, not per-key
	assert.True(t, paths["labels"])
}

func TestCompute_NewEntity_MapOfStructs_ExtractedAsWholeValue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "NYC", Country: "US"},
		},
	}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range payload.Changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["id"])
	// Map fields in structs are extracted as whole values during new entity creation
	assert.True(t, paths["items"])
}

// ---------------------------------------------------------------------------
// Compute - new entity with slice
// ---------------------------------------------------------------------------

func TestCompute_NewEntity_Slice_ExtractsAsSet(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithSlice{ID: "1", Tags: []string{"a", "b"}}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "tags" {
			found = true
			assert.Equal(t, OpSet, c.Operation)
		}
	}
	assert.True(t, found)
}

func TestCompute_NewEntity_NilSlice_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	entity := &testEntityWithSlice{ID: "1", Tags: nil}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)

	for _, c := range payload.Changes {
		assert.NotEqual(t, "tags", c.Path, "nil slice should be skipped when ignoring zero")
	}
}

// ---------------------------------------------------------------------------
// Compute - time.Time (special type)
// ---------------------------------------------------------------------------

func TestCompute_TimeField_DetectsChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	old := &testEntityWithTime{ID: "1", CreatedAt: t1}
	newVal := &testEntityWithTime{ID: "1", CreatedAt: t2}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "created_at", payload.Changes[0].Path)
	assert.Equal(t, OpSet, payload.Changes[0].Operation)
}

func TestCompute_TimeField_Identical_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := &testEntityWithTime{ID: "1", CreatedAt: ts}
	newVal := &testEntityWithTime{ID: "1", CreatedAt: ts}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

func TestCompute_TimeField_ZeroToNonZero_WithIgnoreZero(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	old := &testEntityWithTime{ID: "1", CreatedAt: time.Time{}}
	newVal := &testEntityWithTime{ID: "1", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "created_at" {
			found = true
			assert.Equal(t, OpSet, c.Operation)
		}
	}
	assert.True(t, found)
}

func TestCompute_TimeField_NonZeroToZero_DetectsDelete(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithTime{ID: "1", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	newVal := &testEntityWithTime{ID: "1", CreatedAt: time.Time{}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "created_at" {
			found = true
			assert.Equal(t, OpDelete, c.Operation)
		}
	}
	assert.True(t, found)
}

func TestCompute_TimeField_BothZeroWithIgnoreZero_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	old := &testEntityWithTime{ID: "1", CreatedAt: time.Time{}}
	newVal := &testEntityWithTime{ID: "1", CreatedAt: time.Time{}}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

// ---------------------------------------------------------------------------
// Compute - unexported and json:"-" fields
// ---------------------------------------------------------------------------

func TestCompute_UnexportedFields_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithUnexported{ID: "1", Name: "alice"}
	newVal := &testEntityWithUnexported{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
}

func TestCompute_JSONDashFields_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithJSONDash{ID: "1", Name: "alice", Secret: "s1"}
	newVal := &testEntityWithJSONDash{ID: "1", Name: "bob", Secret: "s2"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
}

// ---------------------------------------------------------------------------
// Compute - JSON tag handling
// ---------------------------------------------------------------------------

func TestCompute_OmitemptyTag_UsesFieldNameBeforeComma(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithOmitempty{ID: "1", Name: "alice"}
	newVal := &testEntityWithOmitempty{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
}

func TestCompute_EmptyJSONTagName_FallsBackToFieldName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithEmptyJSONTag{ID: "a", Name: "alice"}
	newVal := &testEntityWithEmptyJSONTag{ID: "b", Name: "alice"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	// Empty json tag name part falls back to field name "ID"
	assert.Equal(t, "ID", payload.Changes[0].Path)
}

// ---------------------------------------------------------------------------
// Compute - entity ID extraction
// ---------------------------------------------------------------------------

func TestCompute_EntityID_FromIDField(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := &testEntity{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, "1", payload.EntityID)
}

func TestCompute_EntityID_FromKeyField(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithKey{Key: "k1", Data: "d1"}
	newVal := &testEntityWithKey{Key: "k1", Data: "d2"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, "k1", payload.EntityID)
}

func TestCompute_EntityID_FromEntityIDField(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithEntityID{EntityID: "e1", Value: "v1"}
	newVal := &testEntityWithEntityID{EntityID: "e1", Value: "v2"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, "e1", payload.EntityID)
}

func TestCompute_EntityID_FromEntityIdField(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithEntityId{EntityId: "e2", Value: "v1"}
	newVal := &testEntityWithEntityId{EntityId: "e2", Value: "v2"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, "e2", payload.EntityID)
}

func TestCompute_EntityID_NoIDField_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityNoID{Data: "old", Count: 1}
	newVal := &testEntityNoID{Data: "new", Count: 1}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.EntityID)
}

func TestCompute_EntityID_NilEntity_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	id := dc.extractEntityID(nil)
	assert.Empty(t, id)
}

func TestCompute_EntityID_NonStruct_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	strVal := "not a struct"
	id := dc.extractEntityID(&strVal)
	assert.Empty(t, id)
}

// ---------------------------------------------------------------------------
// Compute - entity type extraction
// ---------------------------------------------------------------------------

func TestCompute_EntityType_CorrectTypeName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := &testEntity{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, "testEntity", payload.EntityType)
}

func TestCompute_EntityType_NewEntity_CorrectTypeName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := &testEntityWithKey{Key: "k1", Data: "d1"}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)
	assert.Equal(t, "testEntityWithKey", payload.EntityType)
}

// ---------------------------------------------------------------------------
// Compute - version extraction
// ---------------------------------------------------------------------------

func TestCompute_Version_ExtractsFromVersionMethod(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithVersion{ID: "1", Name: "alice", Ver: 1}
	newVal := &testEntityWithVersion{ID: "1", Name: "bob", Ver: 2}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), payload.Version)
	assert.Equal(t, uint64(1), payload.PrevVersion)
}

func TestCompute_Version_NoVersionMethod_ZeroVersion(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := &testEntity{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), payload.Version)
	assert.Equal(t, uint64(0), payload.PrevVersion)
}

// ---------------------------------------------------------------------------
// Compute - max depth exceeded
// ---------------------------------------------------------------------------

func TestCompute_MaxDepthExceeded_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	old := &testDeepA{B: testDeepB{C: testDeepC{Value: "old"}}}
	newVal := &testDeepA{B: testDeepB{C: testDeepC{Value: "new"}}}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

func TestCompute_MaxDepthExceeded_NewEntity_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	entity := &testDeepA{B: testDeepB{C: testDeepC{Value: "val"}}}
	_, err := dc.Compute(nil, entity)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// Compute - Diffable interface
// ---------------------------------------------------------------------------

func TestCompute_Diffable_UsesCustomDiff(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &diffableEntity{ID: "1", Name: "alice", Ver: 1}
	newVal := &diffableEntity{ID: "1", Name: "bob", Ver: 2}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	assert.Equal(t, uint64(2), payload.Version)
	assert.Equal(t, uint64(1), payload.PrevVersion)
	assert.Equal(t, "1", payload.EntityID)
	assert.Equal(t, "diffableEntity", payload.EntityType)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
	assert.Equal(t, "bob", payload.Changes[0].Value)
	assert.Equal(t, "alice", payload.Changes[0].OldValue)
}

func TestCompute_Diffable_OldNil_NewEntity(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	newVal := &diffableEntity{ID: "2", Name: "charlie", Ver: 1}
	payload, err := dc.Compute(nil, newVal)
	require.NoError(t, err)

	assert.Equal(t, uint64(1), payload.Version)
	assert.Equal(t, uint64(0), payload.PrevVersion)
	assert.Len(t, payload.Changes, 2) // id and name
}

func TestCompute_Diffable_OldNotDiffable_ReturnsDiffError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := "not diffable"
	newVal := &diffableEntity{ID: "1", Name: "alice", Ver: 1}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var diffErr *DeltaDiffError
	require.True(t, errors.As(err, &diffErr))
	assert.Contains(t, diffErr.Cause, "type assertion failed")
}

func TestCompute_Diffable_DiffError_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &diffableEntity{ID: "1", Name: "alice", Ver: 1}
	newVal := &failingDiffable{ID: "1", Ver: 2}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var diffErr *DeltaDiffError
	require.True(t, errors.As(err, &diffErr))
	assert.Contains(t, diffErr.Cause, "diff computation failed")
}

// ---------------------------------------------------------------------------
// Compute - interface fields
// ---------------------------------------------------------------------------

func TestCompute_InterfaceField_BothNil_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithInterface{ID: "1", Value: nil}
	newVal := &testEntityWithInterface{ID: "1", Value: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

func TestCompute_InterfaceField_NilToNonNil_DetectsSet(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithInterface{ID: "1", Value: nil}
	newVal := &testEntityWithInterface{ID: "1", Value: "hello"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "value" {
			found = true
			assert.Equal(t, OpSet, c.Operation)
		}
	}
	assert.True(t, found)
}

func TestCompute_InterfaceField_NonNilToNil_DetectsDelete(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithInterface{ID: "1", Value: "hello"}
	newVal := &testEntityWithInterface{ID: "1", Value: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "value" {
			found = true
			assert.Equal(t, OpDelete, c.Operation)
		}
	}
	assert.True(t, found)
}

// ---------------------------------------------------------------------------
// Compute - zero value behavior
// ---------------------------------------------------------------------------

func TestCompute_PrimitiveZeroToZero_WithIgnoreZero_NoChanges(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	// int 0 to int 0 should not appear
	old := &testEntity{ID: "1", Name: "alice", Age: 0}
	newVal := &testEntity{ID: "1", Name: "alice", Age: 0}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	assert.Empty(t, payload.Changes)
}

func TestCompute_PrimitiveNonZeroToZero_DetectsDelete(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 30}
	newVal := &testEntity{ID: "1", Name: "alice", Age: 0}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "age", payload.Changes[0].Path)
	assert.Equal(t, OpDelete, payload.Changes[0].Operation)
}

func TestCompute_PrimitiveZeroToNonZero_DetectsSet(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 0}
	newVal := &testEntity{ID: "1", Name: "alice", Age: 25}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "age", payload.Changes[0].Path)
	assert.Equal(t, OpSet, payload.Changes[0].Operation)
	assert.Equal(t, 25, payload.Changes[0].Value)
}

// ---------------------------------------------------------------------------
// Compute - non-pointer values
// ---------------------------------------------------------------------------

func TestCompute_NonPointerValues_Works(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := testEntity{ID: "1", Name: "alice"}
	newVal := testEntity{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
}

func TestCompute_NonPointerNewPointerOld_TypeMismatch(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := testAddress{City: "NYC"}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var mismatch *DeltaTypeMismatchError
	require.True(t, errors.As(err, &mismatch))
}

// ---------------------------------------------------------------------------
// ComputeBytes
// ---------------------------------------------------------------------------

func TestComputeBytes_ValidJSON_ComputesDelta(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntity{ID: "1", Name: "alice", Age: 30}
	newVal := &testEntity{ID: "1", Name: "bob", Age: 30}

	oldData, err := json.Marshal(old)
	require.NoError(t, err)
	newData, err := json.Marshal(newVal)
	require.NoError(t, err)

	payload, err := dc.ComputeBytes(oldData, newData, &testEntity{})
	require.NoError(t, err)

	require.Len(t, payload.Changes, 1)
	assert.Equal(t, "name", payload.Changes[0].Path)
}

func TestComputeBytes_NilOldData_TreatedAsNewEntity(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	newVal := &testEntity{ID: "1", Name: "alice", Age: 30}
	newData, err := json.Marshal(newVal)
	require.NoError(t, err)

	payload, err := dc.ComputeBytes(nil, newData, &testEntity{})
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(payload.Changes), 2)
	assert.Equal(t, "1", payload.EntityID)
}

func TestComputeBytes_EmptyNewData_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes([]byte(`{}`), nil, &testEntity{})
	require.Error(t, err)

	var nilErr *DeltaNilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, "new data", nilErr.Parameter)
}

func TestComputeBytes_NilEntityType_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes(nil, []byte(`{}`), nil)
	require.Error(t, err)

	var nilErr *DeltaNilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, "entity type", nilErr.Parameter)
}

func TestComputeBytes_NonPointerEntityType_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes(nil, []byte(`{}`), testEntity{})
	require.Error(t, err)

	var mismatch *DeltaTypeMismatchError
	require.True(t, errors.As(err, &mismatch))
	assert.Equal(t, "entityType", mismatch.Path)
}

func TestComputeBytes_InvalidNewJSON_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes(nil, []byte(`{invalid`), &testEntity{})
	require.Error(t, err)

	var deser *DeltaDeserializationError
	require.True(t, errors.As(err, &deser))
	assert.Contains(t, deser.Cause, "new data")
}

func TestComputeBytes_InvalidOldJSON_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes([]byte(`{invalid`), []byte(`{"id":"1"}`), &testEntity{})
	require.Error(t, err)

	var deser *DeltaDeserializationError
	require.True(t, errors.As(err, &deser))
	assert.Contains(t, deser.Cause, "old data")
}

func TestComputeBytes_EmptyOldData_TreatedAsNewEntity(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	newData := []byte(`{"id":"1","name":"alice","age":25}`)
	payload, err := dc.ComputeBytes([]byte{}, newData, &testEntity{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(payload.Changes), 2)
}

// ---------------------------------------------------------------------------
// Helper methods - buildPath
// ---------------------------------------------------------------------------

func TestBuildPath_EmptyBase_ReturnsSegment(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.Equal(t, "name", dc.buildPath("", "name"))
}

func TestBuildPath_NonEmptyBase_DotSeparated(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.Equal(t, "address.city", dc.buildPath("address", "city"))
}

// ---------------------------------------------------------------------------
// Helper methods - getFieldPath
// ---------------------------------------------------------------------------

func TestGetFieldPath_NoJSONTag_ReturnsFieldName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))

	type noTagEntity struct {
		Field string
	}
	field, _ := reflect.TypeOf(noTagEntity{}).FieldByName("Field")
	assert.Equal(t, "Field", dc.getFieldPath(field))
}

func TestGetFieldPath_WithJSONTag_ReturnsTagName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))

	type tagEntity struct {
		Field string `json:"my_field"`
	}
	field, _ := reflect.TypeOf(tagEntity{}).FieldByName("Field")
	assert.Equal(t, "my_field", dc.getFieldPath(field))
}

// ---------------------------------------------------------------------------
// Helper methods - isZeroValue
// ---------------------------------------------------------------------------

func TestIsZeroValue_InvalidValue_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var v reflect.Value
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_ZeroInt_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(0)
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NonZeroInt_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(42)
	assert.False(t, dc.isZeroValue(v))
}

func TestIsZeroValue_EmptyString_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf("")
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NilSlice_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var s []string
	v := reflect.ValueOf(s)
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NilMap_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var m map[string]string
	v := reflect.ValueOf(m)
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NilPtr_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var p *testEntity
	v := reflect.ValueOf(p)
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_ZeroTimeTime_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(time.Time{})
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NonZeroTimeTime_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(time.Now())
	assert.False(t, dc.isZeroValue(v))
}

func TestIsZeroValue_ZeroStruct_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(testAddress{})
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NonZeroStruct_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(testAddress{City: "NYC"})
	assert.False(t, dc.isZeroValue(v))
}

func TestIsZeroValue_ZeroArray_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var arr [3]int
	v := reflect.ValueOf(arr)
	assert.True(t, dc.isZeroValue(v))
}

func TestIsZeroValue_NonZeroArray_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	arr := [3]int{1, 0, 0}
	v := reflect.ValueOf(arr)
	assert.False(t, dc.isZeroValue(v))
}

// ---------------------------------------------------------------------------
// Helper methods - isSpecialType
// ---------------------------------------------------------------------------

func TestIsSpecialType_TimeTime_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.True(t, dc.isSpecialType(reflect.TypeOf(time.Time{})))
}

func TestIsSpecialType_PointerToTimeTime_ReturnsTrue(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var tp *time.Time
	assert.True(t, dc.isSpecialType(reflect.TypeOf(tp)))
}

func TestIsSpecialType_OtherStruct_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.False(t, dc.isSpecialType(reflect.TypeOf(testAddress{})))
}

func TestIsSpecialType_PrimitiveType_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.False(t, dc.isSpecialType(reflect.TypeOf("")))
}

// ---------------------------------------------------------------------------
// Helper methods - extractValue
// ---------------------------------------------------------------------------

func TestExtractValue_InvalidValue_ReturnsNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var v reflect.Value
	assert.Nil(t, dc.extractValue(v))
}

func TestExtractValue_NilPointer_ReturnsNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var p *string
	v := reflect.ValueOf(p)
	assert.Nil(t, dc.extractValue(v))
}

func TestExtractValue_NonNilPointer_ReturnsElem(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	s := "hello"
	v := reflect.ValueOf(&s)
	assert.Equal(t, "hello", dc.extractValue(v))
}

func TestExtractValue_PrimitiveValue_ReturnsInterface(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(42)
	assert.Equal(t, 42, dc.extractValue(v))
}

// ---------------------------------------------------------------------------
// Helper methods - valueToString
// ---------------------------------------------------------------------------

func TestValueToString_StringKind_ReturnsString(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf("hello")
	assert.Equal(t, "hello", dc.valueToString(v))
}

func TestValueToString_IntKind_ReturnsFormattedString(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	v := reflect.ValueOf(42)
	result := dc.valueToString(v)
	assert.NotEmpty(t, result)
}

// ---------------------------------------------------------------------------
// Helper methods - extractEntityType
// ---------------------------------------------------------------------------

func TestExtractEntityType_StructType_ReturnsName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.Equal(t, "testEntity", dc.extractEntityType(reflect.TypeOf(testEntity{})))
}

func TestExtractEntityType_PointerType_ReturnsElemName(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	assert.Equal(t, "testEntity", dc.extractEntityType(reflect.TypeOf(&testEntity{})))
}

// ---------------------------------------------------------------------------
// Compute - extractAllFields with nil pointer input
// ---------------------------------------------------------------------------

func TestExtractAllFields_NilPointerValue_ReturnsNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var p *testEntity
	v := reflect.ValueOf(p)
	changes, err := dc.extractAllFields(v, "", 0)
	require.NoError(t, err)
	assert.Nil(t, changes)
}

func TestExtractAllFields_DefaultValue_ReturnsSetOperations(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(false))
	v := reflect.ValueOf(42)
	changes, err := dc.extractAllFields(v, "root", 0)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "root", changes[0].Path)
	assert.Equal(t, OpSet, changes[0].Operation)
	assert.Equal(t, 42, changes[0].Value)
}

// ---------------------------------------------------------------------------
// Compute - extractAllFields max depth exceeded
// ---------------------------------------------------------------------------

func TestExtractAllFields_MaxDepthExceeded_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(2))
	v := reflect.ValueOf(testDeepA{B: testDeepB{C: testDeepC{Value: "x"}}})
	_, err := dc.extractAllFields(v, "", 3)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// Compute - computeChanges max depth exceeded
// ---------------------------------------------------------------------------

func TestComputeChanges_MaxDepthExceeded_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	oldVal := reflect.ValueOf(testEntity{ID: "1"})
	newVal := reflect.ValueOf(testEntity{ID: "2"})
	_, err := dc.computeChanges(oldVal, newVal, "root", 2)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// Timestamp is always set
// ---------------------------------------------------------------------------

func TestCompute_Timestamp_AlwaysPopulated(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	before := time.Now()
	old := &testEntity{ID: "1", Name: "alice"}
	newVal := &testEntity{ID: "1", Name: "bob"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	after := time.Now()

	assert.False(t, payload.Timestamp.IsZero())
	assert.True(t, payload.Timestamp.After(before) || payload.Timestamp.Equal(before))
	assert.True(t, payload.Timestamp.Before(after) || payload.Timestamp.Equal(after))
}

func TestCompute_NewEntity_Timestamp_AlwaysPopulated(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	before := time.Now()
	entity := &testEntity{ID: "1", Name: "alice"}
	payload, err := dc.Compute(nil, entity)
	require.NoError(t, err)
	after := time.Now()

	assert.False(t, payload.Timestamp.IsZero())
	assert.True(t, payload.Timestamp.After(before) || payload.Timestamp.Equal(before))
	assert.True(t, payload.Timestamp.Before(after) || payload.Timestamp.Equal(after))
}

// ---------------------------------------------------------------------------
// Edge cases - struct with unexported fields in isZeroValue
// ---------------------------------------------------------------------------

func TestIsZeroValue_StructWithUnexportedFieldsOnly(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	type unexportedOnly struct {
		a int //nolint:unused // testing unexported
		b int //nolint:unused // testing unexported
	}
	v := reflect.ValueOf(unexportedOnly{})
	assert.True(t, dc.isZeroValue(v))
}

// ---------------------------------------------------------------------------
// Edge cases - empty new data ComputeBytes
// ---------------------------------------------------------------------------

func TestComputeBytes_ZeroLengthNewData_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	_, err := dc.ComputeBytes([]byte(`{}`), []byte{}, &testEntity{})
	require.Error(t, err)

	var nilErr *DeltaNilInputError
	require.True(t, errors.As(err, &nilErr))
}

// ---------------------------------------------------------------------------
// Nil interface field comparison
// ---------------------------------------------------------------------------

func TestCompute_InterfaceField_BothNonNil_DetectsChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	old := &testEntityWithInterface{ID: "1", Value: "old"}
	newVal := &testEntityWithInterface{ID: "1", Value: "new"}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "value" {
			found = true
			assert.Equal(t, OpSet, c.Operation)
			assert.Equal(t, "new", c.Value)
			assert.Equal(t, "old", c.OldValue)
		}
	}
	assert.True(t, found)
}

// ---------------------------------------------------------------------------
// extractEntityID - direct pointer handling
// ---------------------------------------------------------------------------

func TestExtractEntityID_NilPointer_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	var p *testEntity
	id := dc.extractEntityID(p)
	assert.Empty(t, id)
}

func TestExtractEntityID_ValidPointer_ReturnsID(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	e := &testEntity{ID: "test-id"}
	id := dc.extractEntityID(e)
	assert.Equal(t, "test-id", id)
}

// ---------------------------------------------------------------------------
// extractAllFields - map, slice, default branches (via pointer field diffs)
// ---------------------------------------------------------------------------

func TestExtractAllFields_MapValue_IteratesEntries(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	m := map[string]string{"env": "prod", "team": "backend"}
	v := reflect.ValueOf(m)
	changes, err := dc.extractAllFields(v, "labels", 0)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range changes {
		paths[c.Path] = true
		assert.Equal(t, OpSet, c.Operation)
	}
	assert.True(t, paths["labels.env"])
	assert.True(t, paths["labels.team"])
}

func TestExtractAllFields_MapOfStructValue_RecursesIntoStructEntries(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	m := map[string]testAddress{
		"home": {City: "NYC", Country: "US"},
	}
	v := reflect.ValueOf(m)
	changes, err := dc.extractAllFields(v, "items", 0)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["items.home.city"])
	assert.True(t, paths["items.home.country"])
}

func TestExtractAllFields_SliceValue_ExtractsAsSet(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	s := []string{"a", "b", "c"}
	v := reflect.ValueOf(s)
	changes, err := dc.extractAllFields(v, "tags", 0)
	require.NoError(t, err)

	require.Len(t, changes, 1)
	assert.Equal(t, "tags", changes[0].Path)
	assert.Equal(t, OpSet, changes[0].Operation)
}

func TestExtractAllFields_NilSlice_ReturnsNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	var s []string
	v := reflect.ValueOf(s)
	changes, err := dc.extractAllFields(v, "tags", 0)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestExtractAllFields_ZeroPrimitive_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))
	v := reflect.ValueOf(0)
	changes, err := dc.extractAllFields(v, "count", 0)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestExtractAllFields_PointerValue_Dereferences(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	addr := &testAddress{City: "NYC", Country: "US"}
	v := reflect.ValueOf(addr)
	changes, err := dc.extractAllFields(v, "address", 0)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["address.city"])
	assert.True(t, paths["address.country"])
}

func TestExtractAllFields_MapMaxDepthExceeded_InNestedStruct(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	m := map[string]testAddress{
		"home": {City: "NYC", Country: "US"},
	}
	v := reflect.ValueOf(m)
	_, err := dc.extractAllFields(v, "items", 1)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// computeNewEntity - version extraction from addressable value
// ---------------------------------------------------------------------------

func TestCompute_NewEntity_WithVersionMethod(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	// ComputeBytes creates addressable values via reflect.New, which enables
	// the Version() method extraction in computeNewEntity
	newData := []byte(`{"id":"1","name":"alice"}`)
	payload, err := dc.ComputeBytes(nil, newData, &testEntityWithVersion{})
	require.NoError(t, err)
	// Version is not in JSON so stays 0, but the path is exercised
	assert.Equal(t, uint64(0), payload.Version)
}

// ---------------------------------------------------------------------------
// compareSlices - both nil with ignoreZero=false
// ---------------------------------------------------------------------------

func TestCompute_SliceDiff_BothNilWithoutIgnoreZero_DetectsSameNil(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(false))
	old := &testEntityWithSlice{ID: "1", Tags: nil}
	newVal := &testEntityWithSlice{ID: "1", Tags: nil}
	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)
	// Both nil slices are DeepEqual, so no change
	assert.Empty(t, payload.Changes)
}

// ---------------------------------------------------------------------------
// compareStructs - nested struct error propagation
// ---------------------------------------------------------------------------

func TestCompareStructs_NestedMaxDepthError_Propagates(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(2))
	old := &testDeepA{B: testDeepB{C: testDeepC{Value: "old"}}}
	newVal := &testDeepA{B: testDeepB{C: testDeepC{Value: "new"}}}
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// compareMaps - error propagation from nested struct diff
// ---------------------------------------------------------------------------

func TestCompareMaps_ExistingKeyNested_ErrorPropagates(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	old := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "NYC", Country: "US"},
		},
	}
	newVal := &testEntityWithMapOfStructs{
		ID: "1",
		Items: map[string]testAddress{
			"home": {City: "LA", Country: "US"},
		},
	}
	// maxDepth=1, depth for map is 1 (from struct level 0 to map comparison),
	// then struct comparison inside map is depth 2 which exceeds max.
	_, err := dc.Compute(old, newVal)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// extractAllFields - struct with nested error propagation
// ---------------------------------------------------------------------------

func TestExtractAllFields_NestedStructMaxDepth_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	entity := testEntityNested{
		ID: "1", Name: "alice",
		Address: testAddress{City: "NYC", Country: "US"},
	}
	v := reflect.ValueOf(entity)
	_, err := dc.extractAllFields(v, "", 1)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// extractAllFields - struct with pointer to struct error propagation
// ---------------------------------------------------------------------------

func TestExtractAllFields_PointerToStructMaxDepth_ReturnsError(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaMaxDepth(1))
	entity := testEntityWithPointer{
		ID:      "1",
		Address: &testAddress{City: "NYC", Country: "US"},
	}
	v := reflect.ValueOf(entity)
	_, err := dc.extractAllFields(v, "", 1)
	require.Error(t, err)

	var maxErr *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &maxErr))
}

// ---------------------------------------------------------------------------
// extractAllFields - struct with json:"-" field
// ---------------------------------------------------------------------------

func TestExtractAllFields_JSONDashField_Skipped(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t))
	entity := testEntityWithJSONDash{ID: "1", Name: "alice", Secret: "s"}
	v := reflect.ValueOf(entity)
	changes, err := dc.extractAllFields(v, "", 0)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, c := range changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["id"])
	assert.True(t, paths["name"])
	assert.NotContains(t, paths, "-")
	assert.NotContains(t, paths, "Secret")
}
