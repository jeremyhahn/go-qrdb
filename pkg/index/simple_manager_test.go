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
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSimpleIndexManager(t *testing.T) {
	mgr := NewSimpleIndexManager()
	assert.NotNil(t, mgr)
	assert.NotNil(t, mgr.indexes)
	assert.NotNil(t, mgr.registeredFields)
}

func TestSimpleIndexManager_RegisterEntityType(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type User struct {
		ID    uint64 `index:"unique"`
		Email string `index:"unique"`
		Name  string `index:"true"`
	}

	entityType := reflect.TypeOf(User{})
	err := mgr.RegisterEntityType("User", entityType)
	assert.NoError(t, err)

	assert.Contains(t, mgr.indexes, "ID")
	assert.Contains(t, mgr.indexes, "Email")
	assert.Contains(t, mgr.indexes, "Name")

	assert.Contains(t, mgr.registeredFields, "User")
	assert.True(t, mgr.registeredFields["User"]["ID"])
	assert.True(t, mgr.registeredFields["User"]["Email"])
	assert.True(t, mgr.registeredFields["User"]["Name"])
}

func TestSimpleIndexManager_RegisterEntityType_MultipleTypes(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type User struct {
		ID   uint64 `index:"unique"`
		Name string `index:"true"`
	}

	type Product struct {
		ID  uint64 `index:"unique"`
		SKU string `index:"unique"`
	}

	err := mgr.RegisterEntityType("User", reflect.TypeOf(User{}))
	assert.NoError(t, err)

	err = mgr.RegisterEntityType("Product", reflect.TypeOf(Product{}))
	assert.NoError(t, err)

	assert.Contains(t, mgr.registeredFields, "User")
	assert.Contains(t, mgr.registeredFields, "Product")
}

func TestSimpleIndexManager_RegisterEntityType_InvalidType(t *testing.T) {
	mgr := NewSimpleIndexManager()

	err := mgr.RegisterEntityType("InvalidType", reflect.TypeOf("string"))
	assert.Error(t, err)
}

func TestSimpleIndexManager_RegisterEntityType_NoIndexTags(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type NoIndexEntity struct {
		ID   uint64
		Name string
	}

	err := mgr.RegisterEntityType("NoIndexEntity", reflect.TypeOf(NoIndexEntity{}))
	assert.NoError(t, err)
}

func TestSimpleIndexManager_IndexEntity(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"username":"john","age":30}`)
	err := mgr.IndexEntity([]byte("1"), jsonData)
	require.NoError(t, err)

	assert.NotNil(t, mgr.indexes["username"])
	assert.NotNil(t, mgr.indexes["age"])

	keys, err := mgr.QueryIndex("username", "john")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("1")}, keys)
}

func TestSimpleIndexManager_IndexEntity_NilData(t *testing.T) {
	mgr := NewSimpleIndexManager()
	err := mgr.IndexEntity([]byte("1"), nil)
	assert.NoError(t, err)
}

func TestSimpleIndexManager_IndexEntity_InvalidJSON(t *testing.T) {
	mgr := NewSimpleIndexManager()
	err := mgr.IndexEntity([]byte("1"), []byte("not json"))
	assert.NoError(t, err)
}

func TestSimpleIndexManager_IndexEntityWithType(t *testing.T) {
	mgr := NewSimpleIndexManager()

	err := mgr.RegisterEntityType("TestEntity", reflect.TypeOf(TestEntity{}))
	require.NoError(t, err)

	jsonData := []byte(`{"ID":1,"Name":"alice","Email":"alice@test.com"}`)
	err = mgr.IndexEntityWithType([]byte("1"), "TestEntity", jsonData)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("1")}, keys)

	keys, err = mgr.QueryIndex("Email", "alice@test.com")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("1")}, keys)
}

func TestSimpleIndexManager_IndexEntityWithType_UnregisteredField(t *testing.T) {
	mgr := NewSimpleIndexManager()

	err := mgr.RegisterEntityType("TestEntity", reflect.TypeOf(TestEntity{}))
	require.NoError(t, err)

	jsonData := []byte(`{"Name":"bob","ExtraField":"should_not_index"}`)
	err = mgr.IndexEntityWithType([]byte("2"), "TestEntity", jsonData)
	require.NoError(t, err)

	_, err = mgr.QueryIndex("ExtraField", "should_not_index")
	assert.Error(t, err)
}

func TestSimpleIndexManager_IndexEntityWithType_UnregisteredType(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"ID":1,"Name":"Alice"}`)
	err := mgr.IndexEntityWithType([]byte("1"), "UnregisteredType", jsonData)
	assert.NoError(t, err)
}

func TestSimpleIndexManager_IndexEntityWithType_SelectiveIndexing(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type User struct {
		ID       uint64 `index:"unique"`
		Email    string `index:"true"`
		Password string // No index tag
	}

	err := mgr.RegisterEntityType("User", reflect.TypeOf(User{}))
	require.NoError(t, err)

	jsonData := []byte(`{"ID":1,"Email":"test@example.com","Password":"secret"}`)
	err = mgr.IndexEntityWithType([]byte("1"), "User", jsonData)
	assert.NoError(t, err)

	assert.Contains(t, mgr.indexes, "ID")
	assert.Contains(t, mgr.indexes, "Email")
}

func TestSimpleIndexManager_DeleteEntity(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"username":"charlie","age":35}`)
	err := mgr.IndexEntity([]byte("3"), jsonData)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("username", "charlie")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("3")}, keys)

	err = mgr.DeleteEntity([]byte("3"), jsonData)
	require.NoError(t, err)

	keys, err = mgr.QueryIndex("username", "charlie")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestSimpleIndexManager_DeleteEntity_NilData(t *testing.T) {
	mgr := NewSimpleIndexManager()
	err := mgr.DeleteEntity([]byte("1"), nil)
	assert.NoError(t, err)
}

func TestSimpleIndexManager_DeleteEntity_InvalidJSON(t *testing.T) {
	mgr := NewSimpleIndexManager()
	err := mgr.DeleteEntity([]byte("1"), []byte("not json"))
	assert.NoError(t, err)
}

func TestSimpleIndexManager_DeleteEntity_NonExistentIndex(t *testing.T) {
	mgr := NewSimpleIndexManager()
	jsonData := []byte(`{"nonexistent":"value"}`)
	err := mgr.DeleteEntity([]byte("1"), jsonData)
	assert.NoError(t, err)
}

func TestSimpleIndexManager_QueryIndex_NotFound(t *testing.T) {
	mgr := NewSimpleIndexManager()
	_, err := mgr.QueryIndex("nonexistent", "value")
	assert.Error(t, err)
}

func TestSimpleIndexManager_QueryIndex_Success(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData1 := []byte(`{"status":"active"}`)
	jsonData2 := []byte(`{"status":"active"}`)
	jsonData3 := []byte(`{"status":"inactive"}`)

	err := mgr.IndexEntity([]byte("10"), jsonData1)
	require.NoError(t, err)
	err = mgr.IndexEntity([]byte("11"), jsonData2)
	require.NoError(t, err)
	err = mgr.IndexEntity([]byte("12"), jsonData3)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("status", "active")
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, []byte("10"))
	assert.Contains(t, keys, []byte("11"))
}

func TestSimpleIndexManager_QueryIndex_ConvertError(t *testing.T) {
	mgr := NewSimpleIndexManager()
	require.NoError(t, mgr.RegisterEntityType("TestEntity", reflect.TypeOf(TestEntity{})))

	// A channel cannot be JSON-marshaled into an index value.
	_, err := mgr.QueryIndex("Name", make(chan int))
	assert.Error(t, err)
}

func TestSimpleIndexManager_ScanIndex(t *testing.T) {
	mgr := NewSimpleIndexManager()

	for i := 1; i <= 5; i++ {
		jsonData := []byte(`{"score":` + string(rune('0'+i)) + `}`)
		err := mgr.IndexEntity([]byte(fmt.Sprintf("%d", i)), jsonData)
		require.NoError(t, err)
	}

	results, err := mgr.ScanIndex("score", "2", "4")
	require.NoError(t, err)
	assert.NotNil(t, results)
}

func TestSimpleIndexManager_ScanIndex_NotFound(t *testing.T) {
	mgr := NewSimpleIndexManager()
	_, err := mgr.ScanIndex("nonexistent", "start", "end")
	assert.Error(t, err)
}

func TestSimpleIndexManager_ScanIndex_NilBounds(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"field":"value"}`)
	err := mgr.IndexEntity([]byte("1"), jsonData)
	require.NoError(t, err)

	results, err := mgr.ScanIndex("field", nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, results)
}

func TestSimpleIndexManager_ScanIndex_ConvertError(t *testing.T) {
	mgr := NewSimpleIndexManager()
	require.NoError(t, mgr.RegisterEntityType("TestEntity", reflect.TypeOf(TestEntity{})))

	// A channel cannot be JSON-marshaled into either scan bound.
	_, err := mgr.ScanIndex("Name", make(chan int), nil)
	assert.Error(t, err, "start bound")
	_, err = mgr.ScanIndex("Name", nil, make(chan int))
	assert.Error(t, err, "end bound")
}

func TestSimpleIndexManager_ListIndexes(t *testing.T) {
	mgr := NewSimpleIndexManager()

	indexes := mgr.ListIndexes()
	assert.Empty(t, indexes)

	jsonData := []byte(`{"field1":"value1","field2":"value2","field3":"value3"}`)
	err := mgr.IndexEntity([]byte("1"), jsonData)
	require.NoError(t, err)

	indexes = mgr.ListIndexes()
	assert.Len(t, indexes, 3)
	assert.Contains(t, indexes, "field1")
	assert.Contains(t, indexes, "field2")
	assert.Contains(t, indexes, "field3")
}

func TestSimpleIndexManager_Clear(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"username":"dave"}`)
	err := mgr.IndexEntity([]byte("20"), jsonData)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("username", "dave")
	require.NoError(t, err)
	assert.Len(t, keys, 1)

	err = mgr.Clear()
	require.NoError(t, err)

	keys, err = mgr.QueryIndex("username", "dave")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestSimpleIndexManager_Close(t *testing.T) {
	mgr := NewSimpleIndexManager()

	jsonData := []byte(`{"username":"eve"}`)
	err := mgr.IndexEntity([]byte("30"), jsonData)
	require.NoError(t, err)

	err = mgr.Close()
	require.NoError(t, err)

	assert.Nil(t, mgr.indexes)
}

func TestSimpleIndexManager_Close_Empty(t *testing.T) {
	mgr := NewSimpleIndexManager()

	err := mgr.Close()
	assert.NoError(t, err)
}

func TestSimpleIndexManager_MultipleClears(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type User struct {
		ID   uint64 `index:"unique"`
		Name string `index:"true"`
	}

	err := mgr.RegisterEntityType("User", reflect.TypeOf(User{}))
	require.NoError(t, err)

	err = mgr.Clear()
	assert.NoError(t, err)

	err = mgr.Clear()
	assert.NoError(t, err)
}

func TestSimpleIndexManager_ConcurrentOperations(t *testing.T) {
	mgr := NewSimpleIndexManager()

	type User struct {
		ID   uint64 `index:"unique"`
		Name string `index:"true"`
	}

	err := mgr.RegisterEntityType("User", reflect.TypeOf(User{}))
	require.NoError(t, err)

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			jsonData := []byte(fmt.Sprintf(`{"ID":%d,"Name":"User%d"}`, id, id))
			_ = mgr.IndexEntityWithType([]byte(fmt.Sprintf("%d", id)), "User", jsonData)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	indexes := mgr.ListIndexes()
	assert.NotEmpty(t, indexes)
}

func TestSimpleIndexManager_Concurrent(t *testing.T) {
	mgr := NewSimpleIndexManager()

	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 10; i++ {
			jsonData := []byte(`{"field":"value"}`)
			_ = mgr.IndexEntity([]byte(fmt.Sprintf("%d", i)), jsonData)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			_, _ = mgr.QueryIndex("field", "value")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			_ = mgr.ListIndexes()
		}
		done <- true
	}()

	<-done
	<-done
	<-done
}

func TestValueToBytes_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected []byte
		wantErr  bool
	}{
		{"string", "test", []byte("test"), false},
		{"bytes", []byte("test"), []byte("test"), false},
		{"float64", 123.456, []byte("123.456"), false},
		{"int", 123, []byte("123"), false},
		{"int64", int64(123), []byte("123"), false},
		{"uint64", uint64(123), []byte("123"), false},
		{"bool_true", true, []byte("true"), false},
		{"bool_false", false, []byte("false"), false},
		{"nil", nil, []byte("null"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := valueToBytes(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestValueToBytes_ComplexTypes(t *testing.T) {
	complexValue := map[string]interface{}{
		"key": "value",
		"num": 123,
	}

	result, err := valueToBytes(complexValue)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	var unmarshaled map[string]interface{}
	err = json.Unmarshal(result, &unmarshaled)
	assert.NoError(t, err)
	assert.Equal(t, "value", unmarshaled["key"])
}

func TestValueToBytes_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
	}{
		{"empty_string", ""},
		{"zero_int", 0},
		{"zero_float", 0.0},
		{"negative_int", -123},
		{"negative_float", -123.456},
		{"large_number", int64(9223372036854775807)},
		{"empty_bytes", []byte{}},
		{"complex_map", map[string]interface{}{"nested": map[string]int{"deep": 1}}},
		{"array", []interface{}{1, 2, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := valueToBytes(tt.value)
			assert.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
}
