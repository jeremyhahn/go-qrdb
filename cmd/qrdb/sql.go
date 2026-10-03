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

// sql.go implements the ad-hoc SQL query surface (`qrdb sql query`). It
// delegates to client.SQL() (sdk/go/sql.go -> transport.SQLClient), the
// same execution core the REST API's POST /v1/sql/query handler and the
// desktop GUI's SQL tab use.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSQLCmd returns the "sql" command group.
func newSQLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sql",
		Short: "Ad-hoc SQL query execution",
		Long:  "Execute ad-hoc SQL queries against a database's KV/collection surface via the go-mysql-server engine.",
	}

	cmd.AddCommand(newSQLQueryCmd())

	return cmd
}

// newSQLQueryCmd returns the "sql query" subcommand.
//
// --database follows the same local per-command scoping convention as
// 'collection create/list/...' (transport.DefaultDatabaseScope), not the
// persistent root --database flag: transport.SQLClient.Query takes the
// target database explicitly in the request, and /v1/sql/query is not one
// of the REST data-plane paths the root --database flag's header-scoping
// covers (see sdk/go/transport/scope.go dataPlanePrefixes). When this local
// flag is left at its default, effectiveDatabaseScope honors the root
// --database flag instead of silently targeting the default database —
// see effectiveDatabaseScope (collection.go) for the full rationale.
func newSQLQueryCmd() *cobra.Command {
	var (
		database string
		file     string
	)

	cmd := &cobra.Command{
		Use:   "query [sql]",
		Short: "Execute an ad-hoc SQL query",
		Long: `Execute an ad-hoc SQL query against a database's KV/collection surface.

The query may be supplied as a positional argument or read from a file
(use --file - to read from stdin). Exactly one of the two forms is
accepted. Output is governed by the persistent --output flag
(table | json | yaml).

Examples:
  qrdb sql query "SELECT * FROM kv"
  qrdb sql query --database mydb "SELECT * FROM kv WHERE key = 'foo'"
  qrdb sql query --file query.sql
  echo "SELECT * FROM kv" | qrdb sql query --file -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := resolveSQLQuery(args, file)
			if err != nil {
				return err
			}

			result, err := client.SQL().Query(cmd.Context(), effectiveDatabaseScope(cmd, database), query)
			if err != nil {
				return err
			}

			return renderSQLResult(result)
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")
	cmd.Flags().StringVar(&file, "file", "", "Read the query from a file (\"-\" for stdin) instead of the positional argument")

	return cmd
}

// resolveSQLQuery enforces the "positional argument XOR --file" input
// contract for 'sql query' and returns the resolved query text.
func resolveSQLQuery(args []string, file string) (string, error) {
	hasArg := len(args) == 1 && args[0] != ""
	hasFile := file != ""
	if hasArg && hasFile {
		return "", &InvalidArgError{
			Arg:    "sql/--file",
			Reason: "exactly one of a positional SQL argument or --file is allowed",
		}
	}
	if !hasArg && !hasFile {
		return "", &MissingArgError{Arg: "sql (positional argument) or --file"}
	}
	if hasArg {
		return args[0], nil
	}
	return readSQLFile(file)
}

// readSQLFile reads the query text from path. The literal path "-" reads
// from stdin.
func readSQLFile(path string) (string, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", &FileReadError{Path: path, Err: err}
	}
	query := strings.TrimSpace(string(data))
	if query == "" {
		return "", &InvalidArgError{Arg: "--file", Reason: "file is empty"}
	}
	return query, nil
}

// renderSQLResult prints a SQLQueryResult in the configured output format.
func renderSQLResult(result *transport.SQLQueryResult) error {
	switch outputFormat {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		rows := make([][]string, 0, len(result.Rows))
		for _, row := range result.Rows {
			cells := make([]string, len(result.Columns))
			for i, col := range result.Columns {
				cells[i] = sqlCellString(row[col])
			}
			rows = append(rows, cells)
		}
		printTable(result.Columns, rows)
		_, _ = fmt.Fprintf(outputWriter, "(%d rows, %s)\n", result.RowCount, result.Duration)
		return nil
	}
}

// sqlCellString renders a single SQL result cell as a display string. A
// nil value (SQL NULL) renders as "NULL"; every other value uses its
// default fmt representation.
func sqlCellString(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprint(v)
}
