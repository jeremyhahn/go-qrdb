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

package entity

import (
	"reflect"
	"time"
)

// timeType is the reflect.Type for time.Time, used for special-case mapping.
var timeType = reflect.TypeOf(time.Time{})

// GoTypeToSQLType maps a Go reflect.Type to a SQL type string.
// For time.Time it returns "DATETIME".
// For map and struct types it returns "JSON".
// For []byte (slice of uint8) it returns "BLOB".
// Falls back to GoKindToSQLType for all other types.
func GoTypeToSQLType(t reflect.Type) string {
	// Dereference pointer types.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t == timeType {
		return "DATETIME"
	}

	switch t.Kind() {
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "BLOB"
		}
		return "JSON"
	case reflect.Map:
		return "JSON"
	case reflect.Struct:
		return "JSON"
	}

	return GoKindToSQLType(t.Kind())
}

// GoKindToSQLType maps a reflect.Kind to a SQL type string.
func GoKindToSQLType(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "VARCHAR"
	case reflect.Int64, reflect.Int, reflect.Int32, reflect.Int16, reflect.Int8:
		return "BIGINT"
	case reflect.Uint64, reflect.Uint, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		return "BIGINT UNSIGNED"
	case reflect.Float64, reflect.Float32:
		return "DOUBLE"
	case reflect.Bool:
		return "BOOLEAN"
	default:
		return "VARCHAR"
	}
}
