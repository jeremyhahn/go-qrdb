//go:build qrdb_mysql

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

// registerMySQLFlags registers the MySQL wire protocol flags. Built only
// under qrdb_mysql, so a binary without the tag has no --enable-mysql or
// --mysql-port flag at all -- cobra reports "unknown flag" rather than
// accepting a setting this binary can never serve. See
// start_mysql_flags_stub.go for the !qrdb_mysql counterpart.
func registerMySQLFlags(cmd *cobra.Command, f *startFlags) {
	cmd.Flags().BoolVar(&f.enableMySQL, "enable-mysql", false, "Enable MySQL wire protocol server")
	cmd.Flags().IntVar(&f.mysqlPort, "mysql-port", 3306, "MySQL wire protocol server port")
}
