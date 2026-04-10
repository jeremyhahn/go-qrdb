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

package entity_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
)

func TestGoTypeToSQLType(t *testing.T) {
	type inner struct{ X int }

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			name: "string",
			typ:  reflect.TypeOf(""),
			want: "VARCHAR",
		},
		{
			name: "int64",
			typ:  reflect.TypeOf(int64(0)),
			want: "BIGINT",
		},
		{
			name: "int",
			typ:  reflect.TypeOf(int(0)),
			want: "BIGINT",
		},
		{
			name: "int32",
			typ:  reflect.TypeOf(int32(0)),
			want: "BIGINT",
		},
		{
			name: "int16",
			typ:  reflect.TypeOf(int16(0)),
			want: "BIGINT",
		},
		{
			name: "int8",
			typ:  reflect.TypeOf(int8(0)),
			want: "BIGINT",
		},
		{
			name: "uint64",
			typ:  reflect.TypeOf(uint64(0)),
			want: "BIGINT UNSIGNED",
		},
		{
			name: "uint",
			typ:  reflect.TypeOf(uint(0)),
			want: "BIGINT UNSIGNED",
		},
		{
			name: "uint32",
			typ:  reflect.TypeOf(uint32(0)),
			want: "BIGINT UNSIGNED",
		},
		{
			name: "uint16",
			typ:  reflect.TypeOf(uint16(0)),
			want: "BIGINT UNSIGNED",
		},
		{
			name: "uint8",
			typ:  reflect.TypeOf(uint8(0)),
			want: "BIGINT UNSIGNED",
		},
		{
			name: "float64",
			typ:  reflect.TypeOf(float64(0)),
			want: "DOUBLE",
		},
		{
			name: "float32",
			typ:  reflect.TypeOf(float32(0)),
			want: "DOUBLE",
		},
		{
			name: "bool",
			typ:  reflect.TypeOf(false),
			want: "BOOLEAN",
		},
		{
			name: "byte slice ([]byte)",
			typ:  reflect.TypeOf([]byte{}),
			want: "BLOB",
		},
		{
			name: "time.Time",
			typ:  reflect.TypeOf(time.Time{}),
			want: "DATETIME",
		},
		{
			name: "map[string]interface{}",
			typ:  reflect.TypeOf(map[string]interface{}{}),
			want: "JSON",
		},
		{
			name: "struct (non-time)",
			typ:  reflect.TypeOf(inner{}),
			want: "JSON",
		},
		{
			name: "pointer to time.Time",
			typ:  reflect.TypeOf((*time.Time)(nil)),
			want: "DATETIME",
		},
		{
			name: "pointer to string",
			typ:  reflect.TypeOf((*string)(nil)),
			want: "VARCHAR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := entity.GoTypeToSQLType(tc.typ)
			if got != tc.want {
				t.Errorf("GoTypeToSQLType(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

func TestGoKindToSQLType(t *testing.T) {
	tests := []struct {
		name string
		kind reflect.Kind
		want string
	}{
		{name: "string", kind: reflect.String, want: "VARCHAR"},
		{name: "int64", kind: reflect.Int64, want: "BIGINT"},
		{name: "int", kind: reflect.Int, want: "BIGINT"},
		{name: "int32", kind: reflect.Int32, want: "BIGINT"},
		{name: "int16", kind: reflect.Int16, want: "BIGINT"},
		{name: "int8", kind: reflect.Int8, want: "BIGINT"},
		{name: "uint64", kind: reflect.Uint64, want: "BIGINT UNSIGNED"},
		{name: "uint", kind: reflect.Uint, want: "BIGINT UNSIGNED"},
		{name: "uint32", kind: reflect.Uint32, want: "BIGINT UNSIGNED"},
		{name: "uint16", kind: reflect.Uint16, want: "BIGINT UNSIGNED"},
		{name: "uint8", kind: reflect.Uint8, want: "BIGINT UNSIGNED"},
		{name: "float64", kind: reflect.Float64, want: "DOUBLE"},
		{name: "float32", kind: reflect.Float32, want: "DOUBLE"},
		{name: "bool", kind: reflect.Bool, want: "BOOLEAN"},
		{name: "chan (default)", kind: reflect.Chan, want: "VARCHAR"},
		{name: "func (default)", kind: reflect.Func, want: "VARCHAR"},
		{name: "complex128 (default)", kind: reflect.Complex128, want: "VARCHAR"},
		{name: "uintptr (default)", kind: reflect.Uintptr, want: "VARCHAR"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := entity.GoKindToSQLType(tc.kind)
			if got != tc.want {
				t.Errorf("GoKindToSQLType(%v) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestGoTypeToSQLType_SliceNonByte verifies that a non-byte slice maps to JSON.
func TestGoTypeToSQLType_SliceNonByte(t *testing.T) {
	typ := reflect.TypeOf([]string{})
	got := entity.GoTypeToSQLType(typ)
	if got != "JSON" {
		t.Errorf("GoTypeToSQLType([]string) = %q, want %q", got, "JSON")
	}
}
