//go:build !qrdb_sql

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

package main

import (
	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// addDatabaseUpdateSQLCmd is a no-op without qrdb_sql: there is no SQL
// engine to configure, so "database update-sql" does not exist on this
// binary. See database_sql_cmd.go for the qrdb_sql counterpart.
func addDatabaseUpdateSQLCmd(_ *cobra.Command) {}

// addDatabaseCreateSQLFlags registers no flags without qrdb_sql and returns
// a no-op apply function, so "database create" has no --sql-concurrency
// flag on this binary. See database_sql_cmd.go for the qrdb_sql
// counterpart.
func addDatabaseCreateSQLFlags(_ *cobra.Command) func(*transport.CreateDatabaseRequest) {
	return func(*transport.CreateDatabaseRequest) {}
}
