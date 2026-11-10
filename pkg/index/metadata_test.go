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
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test entity types.
type TestUser struct {
	ID       uint64 `index:"unique"`
	Email    string `index:"unique,case_insensitive"`
	Username string `index:"true"`
	Age      int    `index:"true"`
	Active   bool   `index:"true"`
	Score    float64
}

type TestProduct struct {
	SKU   string  `index:"unique"`
	Name  string  `index:"case_insensitive"`
	Price float64 `index:"true"`
}

type TestInvalidTag struct {
	Field string `index:"invalid_option"`
}

func TestParseIndexTags(t *testing.T) {
	tests := []struct {
		name      string
		input     reflect.Type
		wantCount int
		wantErr   bool
		check     func(t *testing.T, metadata map[string]*IndexMetadata)
	}{
		{
			name:      "valid user struct",
			input:     reflect.TypeOf(TestUser{}),
			wantCount: 5,
			wantErr:   false,
			check: func(t *testing.T, metadata map[string]*IndexMetadata) {
				// Check ID field (unique)
				id, exists := metadata["ID"]
				require.True(t, exists, "ID index should exist")
				assert.Equal(t, "ID", id.FieldName)
				assert.Equal(t, IndexTypeUnique, id.Config.Type)
				assert.True(t, id.Config.CaseSensitive)

				// Check Email field (unique, case insensitive)
				email, exists := metadata["Email"]
				require.True(t, exists, "Email index should exist")
				assert.Equal(t, "Email", email.FieldName)
				assert.Equal(t, IndexTypeUnique, email.Config.Type)
				assert.False(t, email.Config.CaseSensitive)

				// Check Username field (normal)
				username, exists := metadata["Username"]
				require.True(t, exists, "Username index should exist")
				assert.Equal(t, "Username", username.FieldName)
				assert.Equal(t, IndexTypeNormal, username.Config.Type)
				assert.True(t, username.Config.CaseSensitive)

				// Score should not have index
				_, exists = metadata["Score"]
				assert.False(t, exists, "Score should not be indexed")
			},
		},
		{
			name:      "product struct",
			input:     reflect.TypeOf(TestProduct{}),
			wantCount: 3,
			wantErr:   false,
			check: func(t *testing.T, metadata map[string]*IndexMetadata) {
				sku, exists := metadata["SKU"]
				require.True(t, exists)
				assert.Equal(t, IndexTypeUnique, sku.Config.Type)

				name, exists := metadata["Name"]
				require.True(t, exists)
				assert.False(t, name.Config.CaseSensitive)
			},
		},
		{
			name:      "nil type",
			input:     nil,
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "invalid tag option",
			input:     reflect.TypeOf(TestInvalidTag{}),
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := ParseIndexTags(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, metadata, tt.wantCount)

			if tt.check != nil {
				tt.check(t, metadata)
			}
		})
	}
}

func TestParseIndexTag(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		fieldType reflect.Type
		tag       string
		wantType  IndexType
		wantCS    bool
		wantErr   bool
	}{
		{
			name:      "normal index",
			fieldName: "Field1",
			fieldType: reflect.TypeOf(""),
			tag:       "true",
			wantType:  IndexTypeNormal,
			wantCS:    true,
			wantErr:   false,
		},
		{
			name:      "unique index",
			fieldName: "Field2",
			fieldType: reflect.TypeOf(""),
			tag:       "unique",
			wantType:  IndexTypeUnique,
			wantCS:    true,
			wantErr:   false,
		},
		{
			name:      "case insensitive",
			fieldName: "Field3",
			fieldType: reflect.TypeOf(""),
			tag:       "case_insensitive",
			wantType:  IndexTypeNormal,
			wantCS:    false,
			wantErr:   false,
		},
		{
			name:      "unique and case insensitive",
			fieldName: "Field4",
			fieldType: reflect.TypeOf(""),
			tag:       "unique,case_insensitive",
			wantType:  IndexTypeUnique,
			wantCS:    false,
			wantErr:   false,
		},
		{
			name:      "case sensitive",
			fieldName: "Field7",
			fieldType: reflect.TypeOf(""),
			tag:       "case_sensitive",
			wantType:  IndexTypeNormal,
			wantCS:    true,
		},
		{
			name:      "case sensitive short form",
			fieldName: "Field8",
			fieldType: reflect.TypeOf(""),
			tag:       "cs",
			wantType:  IndexTypeNormal,
			wantCS:    true,
		},
		{
			name:      "empty tag",
			fieldName: "Field5",
			fieldType: reflect.TypeOf(""),
			tag:       "",
			wantErr:   true,
		},
		{
			name:      "invalid option",
			fieldName: "Field6",
			fieldType: reflect.TypeOf(""),
			tag:       "invalid",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := parseIndexTag(tt.fieldName, tt.fieldType, tt.tag)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.fieldName, meta.FieldName)
			assert.Equal(t, tt.wantType, meta.Config.Type)
			assert.Equal(t, tt.wantCS, meta.Config.CaseSensitive)
		})
	}
}

func TestGetFieldValue(t *testing.T) {
	user := TestUser{
		ID:       123,
		Email:    "test@example.com",
		Username: "testuser",
		Age:      25,
		Active:   true,
		Score:    98.5,
	}

	tests := []struct {
		name      string
		entity    interface{}
		fieldName string
		want      []byte
		wantErr   bool
	}{
		{
			name:      "string field",
			entity:    user,
			fieldName: "Email",
			want:      []byte("test@example.com"),
			wantErr:   false,
		},
		{
			name:      "uint64 field",
			entity:    user,
			fieldName: "ID",
			want:      encodeUint64(123),
			wantErr:   false,
		},
		{
			name:      "int field",
			entity:    user,
			fieldName: "Age",
			want:      encodeInt64(25),
			wantErr:   false,
		},
		{
			name:      "bool field (true)",
			entity:    user,
			fieldName: "Active",
			want:      []byte{1},
			wantErr:   false,
		},
		{
			name:      "float field",
			entity:    user,
			fieldName: "Score",
			want:      encodeFloat64(98.5),
			wantErr:   false,
		},
		{
			name:      "non-existent field",
			entity:    user,
			fieldName: "NonExistent",
			wantErr:   true,
		},
		{
			name:      "nil entity",
			entity:    nil,
			fieldName: "Email",
			wantErr:   true,
		},
		{
			name:      "pointer to struct",
			entity:    &user,
			fieldName: "Email",
			want:      []byte("test@example.com"),
			wantErr:   false,
		},
		{
			name:      "pointer to non-struct",
			entity:    new(string),
			fieldName: "Field",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetFieldValue(tt.entity, tt.fieldName)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEncodeDecodeUint64(t *testing.T) {
	tests := []uint64{0, 1, 123, 255, 256, 65535, 4294967295, ^uint64(0)}

	for _, val := range tests {
		t.Run("", func(t *testing.T) {
			encoded := encodeUint64(val)
			assert.Len(t, encoded, 8)

			decoded := binary.BigEndian.Uint64(encoded)
			assert.Equal(t, val, decoded)
		})
	}
}

func TestEncodeInt64(t *testing.T) {
	tests := []struct {
		name string
		val  int64
	}{
		{"zero", 0},
		{"positive small", 1},
		{"positive large", 9223372036854775807},
		{"negative small", -1},
		{"negative large", -9223372036854775808},
		{"mixed", 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := encodeInt64(tt.val)
			assert.Len(t, encoded, 8)
		})
	}

	// Test sorting property
	vals := []int64{-100, -1, 0, 1, 100}
	encoded := make([][]byte, len(vals))
	for i, v := range vals {
		encoded[i] = encodeInt64(v)
	}

	// Verify encoded values are sorted
	for i := 0; i < len(encoded)-1; i++ {
		cmp := compareBytesHelper(encoded[i], encoded[i+1])
		assert.True(t, cmp < 0, "encoded values should be sorted")
	}
}

func TestNormalizeFieldValue(t *testing.T) {
	tests := []struct {
		name   string
		value  []byte
		config IndexConfig
		want   []byte
		wantOK bool
	}{
		{
			name:   "case sensitive - no change",
			value:  []byte("TestValue"),
			config: IndexConfig{CaseSensitive: true},
			want:   []byte("TestValue"),
			wantOK: true,
		},
		{
			name:   "case insensitive - lowercase",
			value:  []byte("TestValue"),
			config: IndexConfig{CaseSensitive: false},
			want:   []byte("testvalue"),
			wantOK: true,
		},
		{
			name:   "case insensitive - already lowercase",
			value:  []byte("testvalue"),
			config: IndexConfig{CaseSensitive: false},
			want:   []byte("testvalue"),
			wantOK: true,
		},
		{
			name:   "empty value",
			value:  []byte{},
			config: IndexConfig{CaseSensitive: false},
			want:   []byte{},
			wantOK: true,
		},
		{
			name:   "nil value",
			value:  nil,
			config: IndexConfig{CaseSensitive: false},
			want:   nil,
			wantOK: true,
		},
		{
			name:   "numeric - valid integer",
			value:  []byte("123"),
			config: IndexConfig{ValueType: IndexValueTypeNumeric},
			want:   nil, // encoded bytes checked via length
			wantOK: true,
		},
		{
			name:   "numeric - invalid string",
			value:  []byte("not-a-number"),
			config: IndexConfig{ValueType: IndexValueTypeNumeric},
			want:   nil,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeFieldValue(tt.value, tt.config)
			assert.Equal(t, tt.wantOK, ok)
			if tt.config.ValueType == IndexValueTypeNumeric {
				if ok {
					assert.Len(t, got, 8, "numeric encoded value should be 8 bytes")
				}
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestGetIndexableFields(t *testing.T) {
	tests := []struct {
		name      string
		input     reflect.Type
		wantCount int
		wantErr   bool
	}{
		{
			name:      "user struct",
			input:     reflect.TypeOf(TestUser{}),
			wantCount: 5,
			wantErr:   false,
		},
		{
			name:      "product struct",
			input:     reflect.TypeOf(TestProduct{}),
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "nil type",
			input:     nil,
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, err := GetIndexableFields(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, fields, tt.wantCount)
		})
	}
}

func TestFieldValueToBytes(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"string", "test", false},
		{"int", int(42), false},
		{"int64", int64(42), false},
		{"uint", uint(42), false},
		{"uint64", uint64(42), false},
		{"bool true", true, false},
		{"bool false", false, false},
		{"float64", float64(3.14), false},
		{"[]byte", []byte("test"), false},
		{"unsupported slice", []string{"a", "b"}, true},
		{"unsupported struct", struct{ x int }{x: 1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val := reflect.ValueOf(tt.value)
			result, err := fieldValueToBytes(val)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
}

func TestEncodeFloat64_AllCases(t *testing.T) {
	inf := func() float64 {
		x := 1.0
		y := 0.0
		return x / y
	}

	tests := []struct {
		name  string
		value float64
	}{
		{"positive", 123.456},
		{"negative", -123.456},
		{"zero", 0.0},
		{"max", 1.7976931348623157e+308},
		{"min", -1.7976931348623157e+308},
		{"small_positive", 1e-308},
		{"small_negative", -1e-308},
		{"infinity", inf()},
		{"neg_infinity", -inf()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := encodeFloat64(tt.value)
			assert.NotNil(t, result)
			assert.Len(t, result, 8)
		})
	}
}

func TestFieldValueToBytes_EdgeCases(t *testing.T) {
	var zeroVal reflect.Value
	result, err := fieldValueToBytes(zeroVal)
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestGetFieldValue_EdgeCases(t *testing.T) {
	type TestStruct struct {
		Name string
		Age  int
	}

	entity := TestStruct{Name: "Alice", Age: 30}

	value, err := GetFieldValue(entity, "Name")
	assert.NoError(t, err)
	assert.Equal(t, []byte("Alice"), value)

	value, err = GetFieldValue(entity, "NonExistent")
	assert.Error(t, err)
	assert.Nil(t, value)

	value, err = GetFieldValue(&entity, "Name")
	assert.NoError(t, err)
	assert.Equal(t, []byte("Alice"), value)
}

func TestParseIndexTags_VariousTypes(t *testing.T) {
	type ComplexEntity struct {
		ID      uint64  `index:"unique"`
		Name    string  `index:"true"`
		Email   string  `index:"unique,case_insensitive"`
		Age     int     `index:"true"`
		Score   float64 `index:"true"`
		Active  bool    `index:"true"`
		Ignored string  // No index tag
	}

	metadata, err := ParseIndexTags(reflect.TypeOf(ComplexEntity{}))
	require.NoError(t, err)
	assert.NotNil(t, metadata)

	assert.Contains(t, metadata, "ID")
	assert.Contains(t, metadata, "Name")
	assert.Contains(t, metadata, "Email")
	assert.Contains(t, metadata, "Age")
	assert.Contains(t, metadata, "Score")
	assert.Contains(t, metadata, "Active")
	assert.NotContains(t, metadata, "Ignored")

	assert.Equal(t, IndexTypeUnique, metadata["ID"].Config.Type)
	assert.Equal(t, IndexTypeUnique, metadata["Email"].Config.Type)
	assert.False(t, metadata["Email"].Config.CaseSensitive)
	assert.True(t, metadata["Name"].Config.CaseSensitive)
}

func TestParseIndexTags_EmptyStruct(t *testing.T) {
	type EmptyEntity struct{}

	metadata, err := ParseIndexTags(reflect.TypeOf(EmptyEntity{}))
	assert.NoError(t, err)
	assert.Empty(t, metadata)
}

func TestParseIndexTags_NoIndexFields(t *testing.T) {
	type NoIndexEntity struct {
		Field1 string
		Field2 int
		Field3 bool
	}

	metadata, err := ParseIndexTags(reflect.TypeOf(NoIndexEntity{}))
	assert.NoError(t, err)
	assert.Empty(t, metadata)
}

func TestParseIndexTags_PointerType(t *testing.T) {
	type Entity struct {
		ID   uint64 `index:"unique"`
		Name string `index:"true"`
	}

	metadata, err := ParseIndexTags(reflect.TypeOf(&Entity{}))
	assert.NoError(t, err)
	assert.NotNil(t, metadata)
	assert.Contains(t, metadata, "ID")
	assert.Contains(t, metadata, "Name")
}

func TestIndexType_String(t *testing.T) {
	tests := []struct {
		name       string
		indexType  IndexType
		expectText string
	}{
		{"normal", IndexTypeNormal, "normal"},
		{"unique", IndexTypeUnique, "unique"},
		{"unknown", IndexType(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.indexType.String()
			assert.Equal(t, tt.expectText, result)
		})
	}
}

func TestParseIndexTags_CaseInsensitive(t *testing.T) {
	type Entity struct {
		Email string `index:"case_insensitive"`
		Name  string `index:"CASE_INSENSITIVE"`
	}

	metadata, err := ParseIndexTags(reflect.TypeOf(Entity{}))
	assert.NoError(t, err)

	assert.False(t, metadata["Email"].Config.CaseSensitive)
	assert.False(t, metadata["Name"].Config.CaseSensitive)
}

// Helper function to compare bytes.
func compareBytesHelper(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}

	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}

	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func BenchmarkEncodeUint64(b *testing.B) {
	val := uint64(123456789)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = encodeUint64(val)
	}
}

func BenchmarkGetFieldValue(b *testing.B) {
	user := TestUser{
		ID:       123,
		Email:    "test@example.com",
		Username: "testuser",
		Age:      25,
		Active:   true,
		Score:    98.5,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetFieldValue(user, "Email")
	}
}

func BenchmarkParseIndexTags(b *testing.B) {
	typ := reflect.TypeOf(TestUser{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseIndexTags(typ)
	}
}
