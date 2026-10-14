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
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newIndexCmd returns the index parent command with subcommands for
// secondary index management: create, drop, query, scan, and list.
func newIndexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Secondary index management",
		Long:  "Create, drop, query, scan, and list secondary indexes.",
	}

	cmd.AddCommand(
		newIndexCreateCmd(),
		newIndexDropCmd(),
		newIndexQueryCmd(),
		newIndexScanCmd(),
		newIndexListCmd(),
	)

	return cmd
}

// newIndexCreateCmd returns the index create subcommand.
func newIndexCreateCmd() *cobra.Command {
	var (
		fieldName string
		unique    bool
		sparse    bool
		numeric   bool
	)

	cmd := &cobra.Command{
		Use:   "create <index-name>",
		Short: "Create a new secondary index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config := transport.IndexConfig{
				Unique:  unique,
				Sparse:  sparse,
				Numeric: numeric,
			}
			if err := client.Index().IndexCreate(cmd.Context(), args[0], fieldName, config); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&fieldName, "field", "", "Field name to index")
	_ = cmd.MarkFlagRequired("field")
	cmd.Flags().BoolVar(&unique, "unique", false, "Enforce uniqueness constraint")
	cmd.Flags().BoolVar(&sparse, "sparse", false, "Allow missing indexed field values")
	cmd.Flags().BoolVar(&numeric, "numeric", false,
		"Index values as numbers, so scans run in numeric order (non-numbers and NaN are not indexed)")

	return cmd
}

// newIndexDropCmd returns the index drop subcommand.
func newIndexDropCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drop <index-name>",
		Short: "Remove a secondary index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Index().IndexDrop(cmd.Context(), args[0]); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newIndexQueryCmd returns the index query subcommand.
func newIndexQueryCmd() *cobra.Command {
	var value string

	cmd := &cobra.Command{
		Use:   "query <index-name>",
		Short: "Query an index for exact value matches",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Index().IndexQuery(cmd.Context(), args[0], value)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"KEY"}
				var rows [][]string
				for _, key := range resp.Keys {
					rows = append(rows, []string{key})
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&value, "value", "", "Exact value to search for")
	_ = cmd.MarkFlagRequired("value")

	return cmd
}

// newIndexScanCmd returns the index scan subcommand.
func newIndexScanCmd() *cobra.Command {
	var (
		start string
		end   string
	)

	cmd := &cobra.Command{
		Use:   "scan <index-name>",
		Short: "Scan an index within a value range",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A flag left unset is an OPEN bound (nil); a flag explicitly set,
			// even to "", is a bound (go-qrdb#36).
			var startBound, endBound *string
			if cmd.Flags().Changed("start") {
				startBound = &start
			}
			if cmd.Flags().Changed("end") {
				endBound = &end
			}
			resp, err := client.Index().IndexScan(cmd.Context(), args[0], startBound, endBound)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"VALUE", "KEYS"}
				var rows [][]string
				for _, entry := range resp.Entries {
					rows = append(rows, []string{
						entry.Value,
						strings.Join(entry.Keys, ", "),
					})
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&start, "start", "", "Start of value range (inclusive); omit for an open lower bound")
	cmd.Flags().StringVar(&end, "end", "", "End of value range (exclusive); omit for an open upper bound")

	return cmd
}

// newIndexListCmd returns the index list subcommand.
func newIndexListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all secondary indexes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Index().IndexList(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"NAME"}
				var rows [][]string
				for _, name := range resp.Names {
					rows = append(rows, []string{name})
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}
