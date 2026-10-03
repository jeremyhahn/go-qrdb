//go:build qrdb_pgsql

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

import "github.com/spf13/cobra"

// registerPgSQLFlags registers the PostgreSQL wire protocol flags. Built
// only under qrdb_pgsql, so a binary without the tag has no --enable-pgsql
// or --pgsql-port flag at all -- cobra reports "unknown flag" rather than
// accepting a setting this binary can never serve. See
// start_pgsql_flags_stub.go for the !qrdb_pgsql counterpart.
func registerPgSQLFlags(cmd *cobra.Command, f *startFlags) {
	cmd.Flags().BoolVar(&f.enablePgSQL, "enable-pgsql", false, "Enable PostgreSQL wire protocol server")
	cmd.Flags().IntVar(&f.pgsqlPort, "pgsql-port", 5432, "PostgreSQL wire protocol server port")
}

// appendPgSQLListener appends "PgSQL" to protocols. Built only under
// qrdb_pgsql, so --host's help text lists it only on a binary that actually
// binds a PostgreSQL listener to that host. See start_pgsql_flags_stub.go
// for the !qrdb_pgsql counterpart.
func appendPgSQLListener(protocols []string) []string {
	return append(protocols, "PgSQL")
}
