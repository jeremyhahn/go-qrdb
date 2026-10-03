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

// addProtocolPgSQLFlag registers "database update-protocol"'s --postgresql
// flag. Built only under qrdb_pgsql, so a binary without the tag has no
// --postgresql flag on that subcommand at all -- cobra reports "unknown
// flag" rather than accepting a setting this binary can never serve. See
// database_protocol_pgsql_flag_stub.go for the !qrdb_pgsql counterpart.
func addProtocolPgSQLFlag(cmd *cobra.Command, postgresql *bool) {
	cmd.Flags().BoolVar(postgresql, "postgresql", false, "Enable PostgreSQL wire protocol")
}
