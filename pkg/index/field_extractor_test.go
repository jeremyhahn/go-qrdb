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

package index

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testExtractorEntity struct {
	Name   string `index:"unique"`
	Age    uint64 `index:"true"`
	Active bool   `index:"true"`
	Data   []byte
}

func TestNewFieldExtractor_StringField(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := testExtractorEntity{Name: "alice"}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte("alice"), result)
}

func TestNewFieldExtractor_StringFieldEmpty(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)

	entity := testExtractorEntity{Name: ""}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte(""), result)
}

func TestNewFieldExtractor_Uint64Field(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Age")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := testExtractorEntity{Age: 42}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, encodeUint64(42), result)
}

func TestNewFieldExtractor_Uint64FieldZero(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Age")
	require.NoError(t, err)

	entity := testExtractorEntity{Age: 0}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, encodeUint64(0), result)
}

func TestNewFieldExtractor_BoolFieldTrue(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Active")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := testExtractorEntity{Active: true}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, result)
}

func TestNewFieldExtractor_BoolFieldFalse(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Active")
	require.NoError(t, err)

	entity := testExtractorEntity{Active: false}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte{0}, result)
}

func TestNewFieldExtractor_ByteSliceField(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Data")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := testExtractorEntity{Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte{0xDE, 0xAD, 0xBE, 0xEF}, result)
}

func TestNewFieldExtractor_ByteSliceFieldNil(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Data")
	require.NoError(t, err)

	entity := testExtractorEntity{Data: nil}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestNewFieldExtractor_PointerEntity(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := &testExtractorEntity{Name: "bob"}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte("bob"), result)
}

func TestNewFieldExtractor_PointerType(t *testing.T) {
	// Pass pointer type to NewFieldExtractor (should dereference automatically).
	structType := reflect.TypeOf(&testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)
	require.NotNil(t, extractor)

	entity := testExtractorEntity{Name: "charlie"}
	result, err := extractor(entity)
	require.NoError(t, err)
	assert.Equal(t, []byte("charlie"), result)
}

func TestNewFieldExtractor_NonExistentField(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "NonExistent")
	assert.Error(t, err)
	assert.Nil(t, extractor)
	assert.Contains(t, err.Error(), "field NonExistent not found")
}

func TestNewFieldExtractor_NilStructType(t *testing.T) {
	extractor, err := NewFieldExtractor(nil, "Name")
	assert.Error(t, err)
	assert.Nil(t, extractor)
	assert.Contains(t, err.Error(), "structType cannot be nil")
}

func TestNewFieldExtractor_NonStructType(t *testing.T) {
	extractor, err := NewFieldExtractor(reflect.TypeOf(42), "Name")
	assert.Error(t, err)
	assert.Nil(t, extractor)
	assert.Contains(t, err.Error(), "expected struct type")
}

func TestNewFieldExtractor_NilEntity(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)

	result, err := extractor(nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "entity cannot be nil")
}

func TestNewFieldExtractor_NilPointerEntity(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)

	var entity *testExtractorEntity
	result, err := extractor(entity)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "entity pointer cannot be nil")
}

func TestNewFieldExtractor_NonStructEntity(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(t, err)

	result, err := extractor(42)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestNewFieldExtractors_AllIndexedFields(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})

	extractors, err := NewFieldExtractors(structType)
	require.NoError(t, err)
	require.NotNil(t, extractors)

	// testExtractorEntity has 3 indexed fields: Name, Age, Active.
	// Data has no index tag.
	assert.Len(t, extractors, 3)
	assert.Contains(t, extractors, "Name")
	assert.Contains(t, extractors, "Age")
	assert.Contains(t, extractors, "Active")
	assert.NotContains(t, extractors, "Data")

	// Verify each extractor works correctly.
	entity := testExtractorEntity{
		Name:   "test",
		Age:    99,
		Active: true,
		Data:   []byte{1, 2, 3},
	}

	nameVal, err := extractors["Name"](entity)
	require.NoError(t, err)
	assert.Equal(t, []byte("test"), nameVal)

	ageVal, err := extractors["Age"](entity)
	require.NoError(t, err)
	assert.Equal(t, encodeUint64(99), ageVal)

	activeVal, err := extractors["Active"](entity)
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, activeVal)
}

func TestNewFieldExtractors_NilStructType(t *testing.T) {
	extractors, err := NewFieldExtractors(nil)
	assert.Error(t, err)
	assert.Nil(t, extractors)
	assert.Contains(t, err.Error(), "structType cannot be nil")
}

func TestNewFieldExtractors_NoIndexedFields(t *testing.T) {
	type noIndexEntity struct {
		Field1 string
		Field2 int
	}

	extractors, err := NewFieldExtractors(reflect.TypeOf(noIndexEntity{}))
	require.NoError(t, err)
	assert.Empty(t, extractors)
}

func TestNewFieldExtractors_PointerType(t *testing.T) {
	extractors, err := NewFieldExtractors(reflect.TypeOf(&testExtractorEntity{}))
	require.NoError(t, err)
	assert.Len(t, extractors, 3)
}

func TestFieldExtractor_MatchesGetFieldValue(t *testing.T) {
	// Verify the FieldExtractor produces identical output to GetFieldValue
	// for every field type, confirming the optimization is semantically correct.
	structType := reflect.TypeOf(testExtractorEntity{})
	entity := testExtractorEntity{
		Name:   "equivalence-test",
		Age:    12345,
		Active: true,
		Data:   []byte{0xCA, 0xFE},
	}

	fields := []string{"Name", "Age", "Active", "Data"}
	for _, fieldName := range fields {
		t.Run(fieldName, func(t *testing.T) {
			extractor, err := NewFieldExtractor(structType, fieldName)
			require.NoError(t, err)

			extractorResult, err := extractor(entity)
			require.NoError(t, err)

			getFieldResult, err := GetFieldValue(entity, fieldName)
			require.NoError(t, err)

			assert.Equal(t, getFieldResult, extractorResult,
				"FieldExtractor and GetFieldValue must produce identical bytes for field %s", fieldName)
		})
	}
}

func TestFieldExtractor_MatchesGetFieldValue_PointerEntity(t *testing.T) {
	structType := reflect.TypeOf(testExtractorEntity{})
	entity := &testExtractorEntity{
		Name:   "ptr-test",
		Age:    777,
		Active: false,
		Data:   []byte{0x01},
	}

	fields := []string{"Name", "Age", "Active", "Data"}
	for _, fieldName := range fields {
		t.Run(fieldName, func(t *testing.T) {
			extractor, err := NewFieldExtractor(structType, fieldName)
			require.NoError(t, err)

			extractorResult, err := extractor(entity)
			require.NoError(t, err)

			getFieldResult, err := GetFieldValue(entity, fieldName)
			require.NoError(t, err)

			assert.Equal(t, getFieldResult, extractorResult)
		})
	}
}

func BenchmarkFieldExtractor(b *testing.B) {
	structType := reflect.TypeOf(testExtractorEntity{})
	extractor, err := NewFieldExtractor(structType, "Name")
	require.NoError(b, err)

	entity := testExtractorEntity{Name: "benchmark"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = extractor(entity)
	}
}

func BenchmarkGetFieldValue_Comparison(b *testing.B) {
	entity := testExtractorEntity{Name: "benchmark"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetFieldValue(entity, "Name")
	}
}
