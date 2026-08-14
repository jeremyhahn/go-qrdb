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

// Package pgcatalog serves the small slice of PostgreSQL's system catalogue
// that PostgreSQL client libraries need in order to introspect a schema:
// the pg_catalog relations pg_type, pg_class, pg_namespace and
// pg_description, plus the three PostgreSQL-only columns
// information_schema.columns carries in PostgreSQL and not in MySQL
// (udt_name, numeric_precision_radix, identity_increment).
//
// # Why both halves live in one package
//
// They are one feature, not two. gorm.io/driver/postgres reads a table's
// columns with a single statement that joins across the boundary --
//
//	SELECT c.column_name, ..., c.udt_name, ..., 8 * typlen, ...
//	FROM information_schema.columns AS c
//	JOIN pg_type AS pgt ON c.udt_name = pgt.typname
//	LEFT JOIN pg_catalog.pg_description AS pd ON ...
//
// -- so the set of typnames pg_type reports and the set of udt_names
// information_schema.columns reports have to agree exactly. A udt_name with
// no matching pg_type row does not raise an error: the inner join silently
// drops that column from the result, the caller concludes the column does
// not exist, and it re-issues ALTER TABLE ADD COLUMN for a column that is
// already there. Keeping the mapping (see udtNameByDataType) and the
// catalogue it must agree with (see pgTypes) in the same file, checked
// against each other by a test, is what makes that class of drift
// impossible.
//
// # What this package deliberately does not do
//
// It is an inventory of what qrdb has been measured needing, not a
// transcription of PostgreSQL's system catalogue. pg_catalog has several
// hundred relations; four are served here because four are what schema
// introspection touches. Adding a fifth is a decision to be made from a
// measured failure, not in advance.
//
// # Read-only
//
// Every relation served here is structurally read-only: none implements
// sql.InsertableTable, sql.UpdatableTable, sql.DeletableTable or
// sql.ReplaceableTable, so a DML statement against one fails during
// analysis rather than reaching any write path -- the same structural
// guarantee pkg/sql/provider's readOnlyTable provides, obtained here by
// simply never implementing the interfaces. The one write that is
// syntactically reachable, creating or dropping the pg_catalog schema
// itself, is refused by pkg/sql/provider with a typed
// ReservedDatabaseNameError naming it.
package pgcatalog
