//go:build codec_json

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

package dao

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// salaryEntity is a test entity with numeric fields for aggregate tests.
type salaryEntity struct {
	ID         uint64  `json:"id"`
	Department string  `json:"department" index:"true"`
	Salary     float64 `json:"salary"`
	Bonus      int     `json:"bonus"`
}

func (e *salaryEntity) EntityID() uint64      { return e.ID }
func (e *salaryEntity) SetEntityID(id uint64) { e.ID = id }

func newSalaryDAO(t *testing.T) GenericDAO[*salaryEntity] {
	t.Helper()
	store := newMemoryKVStore()
	d, err := New[*salaryEntity](
		store,
		"employees",
		func() *salaryEntity { return &salaryEntity{} },
	)
	require.NoError(t, err)
	return d
}

func TestAggFunc_Sum(t *testing.T) {
	af := Sum("Salary")
	assert.Equal(t, AggSum, af.Type)
	assert.Equal(t, "Salary", af.Field)
	assert.Equal(t, "sum_Salary", af.Alias)
}

func TestAggFunc_Avg(t *testing.T) {
	af := Avg("Salary")
	assert.Equal(t, AggAvg, af.Type)
	assert.Equal(t, "Salary", af.Field)
	assert.Equal(t, "avg_Salary", af.Alias)
}

func TestAggFunc_Min(t *testing.T) {
	af := Min("Salary")
	assert.Equal(t, AggMin, af.Type)
	assert.Equal(t, "Salary", af.Field)
	assert.Equal(t, "min_Salary", af.Alias)
}

func TestAggFunc_Max(t *testing.T) {
	af := Max("Salary")
	assert.Equal(t, AggMax, af.Type)
	assert.Equal(t, "Salary", af.Field)
	assert.Equal(t, "max_Salary", af.Alias)
}

func TestAggregate_Sum(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
		{Department: "hr", Salary: 150.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).Aggregate(Sum("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.InDelta(t, 450.0, result.Groups[0].Values["sum_Salary"].(float64), 0.001)
}

func TestAggregate_Avg(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).Aggregate(Avg("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.InDelta(t, 150.0, result.Groups[0].Values["avg_Salary"].(float64), 0.001)
}

func TestAggregate_Min(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 300.0},
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).Aggregate(Min("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.InDelta(t, 100.0, result.Groups[0].Values["min_Salary"].(float64), 0.001)
}

func TestAggregate_Max(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 300.0},
		{Department: "eng", Salary: 200.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).Aggregate(Max("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.InDelta(t, 300.0, result.Groups[0].Values["max_Salary"].(float64), 0.001)
}

func TestAggregate_GroupBy_Sum(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
		{Department: "hr", Salary: 150.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).GroupBy("Department").Aggregate(Sum("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 2)

	byDept := make(map[string]float64)
	for _, g := range result.Groups {
		byDept[g.Key.(string)] = g.Values["sum_Salary"].(float64)
	}
	assert.InDelta(t, 300.0, byDept["eng"], 0.001)
	assert.InDelta(t, 150.0, byDept["hr"], 0.001)
}

func TestAggregate_GroupBy_Min(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
		{Department: "hr", Salary: 500.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).GroupBy("Department").Aggregate(Min("Salary"))
	require.NoError(t, err)

	byDept := make(map[string]float64)
	for _, g := range result.Groups {
		byDept[g.Key.(string)] = g.Values["min_Salary"].(float64)
	}
	assert.InDelta(t, 100.0, byDept["eng"], 0.001)
	assert.InDelta(t, 500.0, byDept["hr"], 0.001)
}

func TestAggregate_GroupBy_Max(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	rows := []*salaryEntity{
		{Department: "eng", Salary: 100.0},
		{Department: "eng", Salary: 200.0},
	}
	for _, r := range rows {
		require.NoError(t, d.Save(ctx, r))
	}

	result, err := d.Find(ctx).GroupBy("Department").Aggregate(Max("Salary"))
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.InDelta(t, 200.0, result.Groups[0].Values["max_Salary"].(float64), 0.001)
}

func TestAggregate_EmptyDataset(t *testing.T) {
	d := newSalaryDAO(t)
	ctx := context.Background()

	result, err := d.Find(ctx).Aggregate(Count())
	require.NoError(t, err)
	// Empty dataset produces no groups — there are no entities to accumulate.
	assert.Empty(t, result.Groups)
}

func TestToFloat64_AllTypes(t *testing.T) {
	tests := []struct {
		val      interface{}
		expected float64
		ok       bool
	}{
		{float64(3.14), 3.14, true},
		{float32(2.5), 2.5, true},
		{int(10), 10, true},
		{int8(8), 8, true},
		{int16(16), 16, true},
		{int32(32), 32, true},
		{int64(64), 64, true},
		{uint(1), 1, true},
		{uint8(2), 2, true},
		{uint16(3), 3, true},
		{uint32(4), 4, true},
		{uint64(5), 5, true},
		{"3.14", 3.14, true},
		{"bad", 0, false},
		{true, 1, true},
		{false, 0, true},
		{nil, 0, false},
		{struct{}{}, 0, false},
	}
	for _, tt := range tests {
		got, ok := toFloat64(tt.val)
		assert.Equal(t, tt.ok, ok, "val=%v", tt.val)
		if tt.ok {
			assert.InDelta(t, tt.expected, got, 0.001, "val=%v", tt.val)
		}
	}
}

// ---------------------------------------------------------------------------
// QueryByCompoundIndex and ScanByCompoundIndex (via compoundTestStore stubs)
// ---------------------------------------------------------------------------

func TestQueryByCompoundIndex_StubReturnsEmpty(t *testing.T) {
	store := newCompoundTestStore()
	d, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	// The stub returns nil keys so loadEntities produces an empty slice.
	entities, err := d.(*genericDAO[*compoundTestMessage]).QueryByCompoundIndex(ctx, "messages.ConversationID_Timestamp", []interface{}{"conv-1", "1000"})
	require.NoError(t, err)
	assert.Empty(t, entities)
}

func TestScanByCompoundIndex_StubReturnsEmpty(t *testing.T) {
	store := newCompoundTestStore()
	d, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entities, err := d.(*genericDAO[*compoundTestMessage]).ScanByCompoundIndex(ctx, "messages.ConversationID_Timestamp", []interface{}{"conv-1"}, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, entities)
}

func TestScanByCompoundIndex_WithRangeBounds(t *testing.T) {
	store := newCompoundTestStore()
	d, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	// Non-nil range bounds exercise the start/end conversion paths.
	entities, err := d.(*genericDAO[*compoundTestMessage]).ScanByCompoundIndex(ctx, "messages.ConversationID_Timestamp", []interface{}{"conv-1"}, "1000", "2000")
	require.NoError(t, err)
	assert.Empty(t, entities)
}

// ---------------------------------------------------------------------------
// loadEntities – ensure keys that do not match the entity prefix are skipped
// ---------------------------------------------------------------------------

func TestLoadEntities_SkipsNonMatchingKeys(t *testing.T) {
	store := newCompoundTestStore()
	d, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	gDAO := d.(*genericDAO[*compoundTestMessage])

	// Keys that don't share the "messages/" prefix must be silently skipped.
	keys := [][]byte{
		[]byte("other/123"),
		[]byte("messages/notanumber"),
	}
	entities, err := gDAO.loadEntities(ctx, keys)
	require.NoError(t, err)
	assert.Empty(t, entities)
}

// ---------------------------------------------------------------------------
// fieldValue — non-struct and invalid-field branches
// ---------------------------------------------------------------------------

func TestFieldValue_NonStruct_ReturnsEmpty(t *testing.T) {
	// A non-struct value (int) should return "".
	got := fieldValue(42, "SomeField")
	assert.Equal(t, "", got, "non-struct entity should return empty string")
}

func TestFieldValue_NilPointer_ReturnsEmpty(t *testing.T) {
	// A nil pointer derefs to an invalid value; Kind() after Elem() on nil ptr
	// is Invalid, not Struct — so the non-struct branch returns "".
	var p *salaryEntity
	got := fieldValue(p, "Salary")
	assert.Equal(t, "", got, "nil pointer entity should return empty string")
}

func TestFieldValue_InvalidFieldName_ReturnsEmpty(t *testing.T) {
	e := &salaryEntity{Salary: 1000.0}
	got := fieldValue(e, "NonExistentField")
	assert.Equal(t, "", got, "invalid field name should return empty string")
}

func TestFieldValue_ValidField_ReturnsString(t *testing.T) {
	e := &salaryEntity{Salary: 250.0}
	got := fieldValue(e, "Salary")
	assert.NotEmpty(t, got, "valid field should return non-empty string")
}

// ---------------------------------------------------------------------------
// extractFieldValue — non-struct and invalid-field branches
// ---------------------------------------------------------------------------

func TestExtractFieldValue_ValidField(t *testing.T) {
	e := &salaryEntity{Bonus: 500}
	got := extractFieldValue(e, "Bonus")
	assert.NotNil(t, got, "valid field should return non-nil")
	assert.Equal(t, 500, got)
}

func TestExtractFieldValue_NonStructEntity_ReturnsNil(t *testing.T) {
	// A non-struct entity returns nil.
	got := extractFieldValue(42, "SomeField")
	assert.Nil(t, got, "non-struct entity should return nil")
}

func TestExtractFieldValue_NilPointer_ReturnsNil(t *testing.T) {
	var p *salaryEntity
	got := extractFieldValue(p, "Salary")
	assert.Nil(t, got, "nil pointer entity should return nil")
}

func TestExtractFieldValue_InvalidField_ReturnsNil(t *testing.T) {
	e := &salaryEntity{Salary: 100.0}
	got := extractFieldValue(e, "NoSuchField")
	assert.Nil(t, got, "invalid field name should return nil")
}
