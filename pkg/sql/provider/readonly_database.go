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

// readOnlyDatabase wraps a sql.Database so that only the base sql.Database
// surface (Name, GetTableInsensitive, GetTableNames) is reachable through it.
// The embedded field's static type is the sql.Database interface, not the
// wrapped concrete type, so Go's embedding rules do not promote any method
// outside that interface: a type assertion against sql.TableCreator,
// sql.IndexedTableCreator, sql.TableDropper, sql.TableRenamer,
// sql.CollatedDatabase, or sql.ViewDatabase performed by go-mysql-server's
// analyzer against a *readOnlyDatabase always fails, regardless of what the
// wrapped concrete database implements.
//
// This is the database-level counterpart to readOnlyTable, and it exists for
// the same reason: a structural guarantee enforced by the Go type system
// rather than a runtime permission check. readOnlyTable keeps DML
// (INSERT/UPDATE/DELETE/REPLACE) away from a read-only surface;
// readOnlyDatabase keeps DDL (CREATE/ALTER/DROP/RENAME TABLE) away from it.
//
// The DDL half is not hypothetical. The ad-hoc SQL query surface served over
// REST, gRPC, QUIC, Unix, and MCP is authorized as a *read* operation — it
// maps to permission.KVRange — so if Database ever implements sql.TableCreator
// without this wrapper in place, a caller holding only read permission could
// issue CREATE TABLE or DROP TABLE through it. Wrapping at the database level
// closes that path before the capability exists, so it cannot be opened by
// omission later.
type readOnlyDatabase struct {
	sqle.Database
}

// Compile-time interface assertion: readOnlyDatabase satisfies sql.Database
// and nothing more.
var _ sqle.Database = (*readOnlyDatabase)(nil)

// newReadOnlyDatabase wraps db so that DDL-capability interfaces implemented
// by the underlying concrete database are not reachable through the returned
// value. Returns nil when db is nil.
func newReadOnlyDatabase(db sqle.Database) sqle.Database {
	if db == nil {
		return nil
	}
	return &readOnlyDatabase{Database: db}
}
