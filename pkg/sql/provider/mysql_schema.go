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

// MySQLSchemaName is MySQL's reserved system schema. go-mysql-server's plan
// builder resolves it unconditionally while building GRANT, REVOKE, and SHOW
// GRANTS (sql/planbuilder/priv.go), before any of those statements reaches
// the analyzer, so a provider that does not serve it fails every one of them
// with a database-not-found error naming a database the user never mentioned.
const MySQLSchemaName = "mysql"

// mysqlSchema is the empty stand-in served for MySQLSchemaName.
//
// It exists only so that the plan builder's unconditional resolution
// succeeds. qrdb does not implement MySQL's grant tables: privileges live in
// the per-database privilege catalog (service.PrivilegeServicer), and the
// GRANT/REVOKE/SHOW GRANTS plan nodes that would otherwise read this schema
// are rewritten onto that catalog by an analyzer post-analyze rule before
// execution. Serving a schema with no tables is therefore honest rather than
// merely convenient: there is nothing here to read.
type mysqlSchema struct{}

// Compile-time interface assertion.
var _ sqle.Database = mysqlSchema{}

// Name implements sql.Nameable.
func (mysqlSchema) Name() string { return MySQLSchemaName }

// GetTableInsensitive implements sql.Database. The schema is empty, so no
// table ever resolves.
func (mysqlSchema) GetTableInsensitive(_ *sqle.Context, _ string) (sqle.Table, bool, error) {
	return nil, false, nil
}

// GetTableNames implements sql.Database.
func (mysqlSchema) GetTableNames(_ *sqle.Context) ([]string, error) { return nil, nil }
