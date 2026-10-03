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

// addDatabaseUpdateProtocolCmd adds the "database update-protocol"
// subcommand to parent. Built only under qrdb_sql: without the SQL engine
// neither the MySQL nor the PostgreSQL wire protocol exists for this
// subcommand to configure. See database_protocol_cmd_stub.go for the
// !qrdb_sql counterpart.
func addDatabaseUpdateProtocolCmd(parent *cobra.Command) {
	parent.AddCommand(newDatabaseUpdateProtocolCmd())
}

// newDatabaseUpdateProtocolCmd creates the "database update-protocol" subcommand.
func newDatabaseUpdateProtocolCmd() *cobra.Command {
	var (
		mysql      bool
		postgresql bool
	)

	cmd := &cobra.Command{
		Use:   "update-protocol <name>",
		Short: "Update wire protocol configuration",
		Long:  "Update the wire protocol configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			req := transport.UpdateDatabaseProtocolRequest{
				Name:       name,
				MySQL:      mysql,
				PostgreSQL: postgresql,
			}
			info, err := client.Database().UpdateDatabaseProtocol(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().BoolVar(&mysql, "mysql", false, "Enable MySQL wire protocol")
	cmd.Flags().BoolVar(&postgresql, "postgresql", false, "Enable PostgreSQL wire protocol")

	return cmd
}
