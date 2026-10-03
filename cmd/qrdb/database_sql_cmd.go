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

package main

import (
	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// addDatabaseUpdateSQLCmd adds the "database update-sql" subcommand to
// parent. Built only under qrdb_sql: without the SQL engine there is no SQL
// concurrency mode for this subcommand to configure. See
// database_sql_cmd_stub.go for the !qrdb_sql counterpart.
func addDatabaseUpdateSQLCmd(parent *cobra.Command) {
	parent.AddCommand(newDatabaseUpdateSQLCmd())
}

// newDatabaseUpdateSQLCmd creates the "database update-sql" subcommand.
func newDatabaseUpdateSQLCmd() *cobra.Command {
	var concurrency string

	cmd := &cobra.Command{
		Use:   "update-sql <name>",
		Short: "Update a database's SQL settings",
		Long: "Update a database's SQL concurrency mode. In pessimistic mode SQL DML takes row " +
			"locks and concurrent writers of a row wait instead of failing at COMMIT; " +
			"SELECT ... FOR UPDATE and FOR SHARE lock in either mode. New transactions " +
			"pick the change up.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Database().UpdateDatabaseSQL(cmd.Context(), transport.UpdateDatabaseSQLRequest{
				Name: args[0],
				SQL:  &transport.DatabaseSQLConfig{Concurrency: concurrency},
			})
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().StringVar(&concurrency, "concurrency", "", "SQL concurrency mode (optimistic, pessimistic)")
	_ = cmd.MarkFlagRequired("concurrency")

	return cmd
}

// addDatabaseCreateSQLFlags registers the --sql-concurrency flag on the
// "database create" command and returns a function that applies it to a
// CreateDatabaseRequest. Built only under qrdb_sql. See
// database_sql_cmd_stub.go for the !qrdb_sql counterpart.
func addDatabaseCreateSQLFlags(cmd *cobra.Command) func(*transport.CreateDatabaseRequest) {
	var sqlConcurrency string

	cmd.Flags().StringVar(&sqlConcurrency, "sql-concurrency", "", "The database's SQL concurrency mode (optimistic, pessimistic; defaults to optimistic)")

	return func(req *transport.CreateDatabaseRequest) {
		if sqlConcurrency != "" {
			req.SQL = &transport.DatabaseSQLConfig{Concurrency: sqlConcurrency}
		}
	}
}
