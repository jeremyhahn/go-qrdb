//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pgcatalog

import (
	"fmt"
	"strconv"
)

// CatalogUnavailableError is returned when a pg_catalog relation is scanned
// before go-mysql-server has handed it the engine catalog through
// AssignCatalog. Every one of these relations is a view over the catalog,
// so without it there is nothing to report -- and reporting zero rows would
// be indistinguishable from a schema that genuinely has no tables, which is
// exactly the silence this package exists to avoid.
type CatalogUnavailableError struct {
	Relation string
}

// Error returns a human-readable description.
func (e *CatalogUnavailableError) Error() string {
	return "pgcatalog: no catalog assigned to relation " + SchemaName + "." + e.Relation
}

// UnknownPartitionError is returned when a pg_catalog relation is asked for
// the rows of a partition that is not its own.
type UnknownPartitionError struct {
	Relation string
	Key      string
}

// Error returns a human-readable description.
func (e *UnknownPartitionError) Error() string {
	return "pgcatalog: relation " + SchemaName + "." + e.Relation +
		" has no partition " + strconv.Quote(e.Key)
}

// RowWidthError is returned when the information_schema.columns rows
// go-mysql-server produced are not the width its own schema declares, which
// would mean the columns this package appends had been written over real
// data. It is a guard against go-mysql-server changing that schema
// underneath us, not a condition any query can provoke.
type RowWidthError struct {
	Want int
	Got  int
}

// Error returns a human-readable description.
func (e *RowWidthError) Error() string {
	return "pgcatalog: information_schema.columns row has " + strconv.Itoa(e.Got) +
		" columns, want " + strconv.Itoa(e.Want)
}

// TypeOIDError is returned by format_type when its first argument converts
// to an unsigned 32-bit integer but does not come back as a type OID.
type TypeOIDError struct {
	Value any
}

// Error returns a human-readable description.
func (e *TypeOIDError) Error() string {
	return "pgcatalog: " + FormatTypeFunction + " cannot read " + fmt.Sprint(e.Value) + " as a type OID"
}
