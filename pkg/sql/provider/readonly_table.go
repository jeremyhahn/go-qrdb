//go:build qrdb_sql

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

package provider

import (
	sqle "github.com/dolthub/go-mysql-server/sql"
)

// readOnlyTable wraps a sql.Table so that only the base read-oriented
// surface (Name, String, Schema, Collation, Partitions, PartitionRows) is
// reachable through it. The embedded field's static type is the sql.Table
// interface, not the wrapped concrete type, so Go's embedding rules do not
// promote any method outside that interface: a type assertion against
// sql.InsertableTable, sql.UpdatableTable, sql.DeletableTable, or
// sql.ReplaceableTable performed by go-mysql-server's analyzer against a
// *readOnlyTable always fails, regardless of what the wrapped concrete
// table implements. This is a structural guarantee enforced by the Go type
// system — not a runtime permission check — so a DML statement (INSERT/
// UPDATE/DELETE/REPLACE) cannot reach a write path through this table even
// if a caller's permission grant were ever misconfigured to allow it.
//
// Index-acceleration interfaces (sql.IndexAddressableTable, sql.IndexedTable)
// are intentionally not passed through: the ad-hoc SQL query surface this
// wrapper protects is a debugging/inspection convenience, not a
// high-throughput path, so trading index push-down for a smaller,
// easier-to-audit read-only boundary is the right default.
type readOnlyTable struct {
	sqle.Table
}

// Compile-time interface assertion: readOnlyTable satisfies sql.Table and
// nothing more.
var _ sqle.Table = (*readOnlyTable)(nil)

// newReadOnlyTable wraps t so that write-capability interfaces implemented
// by the underlying concrete table are not reachable through the returned
// value. Returns nil when t is nil.
func newReadOnlyTable(t sqle.Table) sqle.Table {
	if t == nil {
		return nil
	}
	return &readOnlyTable{Table: t}
}
