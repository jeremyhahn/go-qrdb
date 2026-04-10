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
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// OIDMapping maps go-mysql-server type names to PostgreSQL OIDs.
// Used when converting result schemas from GMS format to PG wire format.
var OIDMapping = map[string]uint32{
	"TEXT":      pgtype.TextOID,
	"VARCHAR":   pgtype.TextOID,
	"BLOB":      pgtype.TextOID,
	"VARBINARY": pgtype.TextOID,
	"INT64":     pgtype.Int8OID,
	"BIGINT":    pgtype.Int8OID,
	"INT32":     pgtype.Int4OID,
	"INT":       pgtype.Int4OID,
	"INT16":     pgtype.Int2OID,
	"SMALLINT":  pgtype.Int2OID,
	"FLOAT64":   pgtype.Float8OID,
	"DOUBLE":    pgtype.Float8OID,
	"FLOAT32":   pgtype.Float4OID,
	"BOOLEAN":   pgtype.BoolOID,
	"TIMESTAMP": pgtype.TimestamptzOID,
	"DATETIME":  pgtype.TimestamptzOID,
	"JSON":      pgtype.JSONBOID,
}

// DefaultOID is used when no mapping exists for a GMS type.
const DefaultOID = pgtype.TextOID

// LookupOID returns the PostgreSQL OID for a given GMS type name.
// Returns DefaultOID if no mapping exists. Lookup is case-insensitive.
func LookupOID(gmsType string) uint32 {
	if oid, ok := OIDMapping[strings.ToUpper(gmsType)]; ok {
		return oid
	}
	return DefaultOID
}
