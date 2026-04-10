// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
)

func TestLookupOID_AllMappings(t *testing.T) {
	tests := []struct {
		name     string
		gmsType  string
		expected uint32
	}{
		{"TEXT", "TEXT", pgtype.TextOID},
		{"VARCHAR", "VARCHAR", pgtype.TextOID},
		{"BLOB", "BLOB", pgtype.TextOID},
		{"VARBINARY", "VARBINARY", pgtype.TextOID},
		{"INT64", "INT64", pgtype.Int8OID},
		{"BIGINT", "BIGINT", pgtype.Int8OID},
		{"INT32", "INT32", pgtype.Int4OID},
		{"INT", "INT", pgtype.Int4OID},
		{"INT16", "INT16", pgtype.Int2OID},
		{"SMALLINT", "SMALLINT", pgtype.Int2OID},
		{"FLOAT64", "FLOAT64", pgtype.Float8OID},
		{"DOUBLE", "DOUBLE", pgtype.Float8OID},
		{"FLOAT32", "FLOAT32", pgtype.Float4OID},
		{"BOOLEAN", "BOOLEAN", pgtype.BoolOID},
		{"TIMESTAMP", "TIMESTAMP", pgtype.TimestamptzOID},
		{"DATETIME", "DATETIME", pgtype.TimestamptzOID},
		{"JSON", "JSON", pgtype.JSONBOID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, tt.expected, oid)
		})
	}
}

func TestLookupOID_CaseInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		gmsType  string
		expected uint32
	}{
		{"lowercase text", "text", pgtype.TextOID},
		{"mixed case Text", "Text", pgtype.TextOID},
		{"lowercase varchar", "varchar", pgtype.TextOID},
		{"lowercase int64", "int64", pgtype.Int8OID},
		{"mixed case Boolean", "Boolean", pgtype.BoolOID},
		{"lowercase json", "json", pgtype.JSONBOID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, tt.expected, oid)
		})
	}
}

func TestLookupOID_UnknownType(t *testing.T) {
	tests := []struct {
		name    string
		gmsType string
	}{
		{"GEOMETRY", "GEOMETRY"},
		{"POINT", "POINT"},
		{"UNKNOWN", "UNKNOWN"},
		{"empty string", ""},
		{"BINARY", "BINARY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, uint32(DefaultOID), oid)
		})
	}
}

func TestDefaultOID_IsTextOID(t *testing.T) {
	assert.Equal(t, pgtype.TextOID, DefaultOID)
}

func TestOIDMapping_NotEmpty(t *testing.T) {
	assert.Greater(t, len(OIDMapping), 0)
}

func TestOIDMapping_AllValuesNonZero(t *testing.T) {
	for typeName, oid := range OIDMapping {
		t.Run(typeName, func(t *testing.T) {
			assert.NotZero(t, oid, "OID for %s should not be zero", typeName)
		})
	}
}
