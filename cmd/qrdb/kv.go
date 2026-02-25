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
	"errors"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/spf13/cobra"
)

// newKVCmd creates the "kv" command group with all KV subcommands.
func newKVCmd() *cobra.Command {
	kvCmd := &cobra.Command{
		Use:   "kv",
		Short: "Key-value data operations",
		Long:  "Perform key-value data-plane operations: put, get, delete, list, and exists.",
	}

	kvCmd.AddCommand(newKVPutCmd())
	kvCmd.AddCommand(newKVGetCmd())
	kvCmd.AddCommand(newKVDelCmd())
	kvCmd.AddCommand(newKVListCmd())
	kvCmd.AddCommand(newKVExistsCmd())

	return kvCmd
}

// newKVPutCmd creates the "kv put" subcommand.
func newKVPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put <key> <value>",
		Short: "Store a key-value pair",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			if err := client.KV().Put(cmd.Context(), key, []byte(value)); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newKVGetCmd creates the "kv get" subcommand.
func newKVGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Retrieve a value by key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			data, err := client.KV().Get(cmd.Context(), key)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(string(data))
			case "yaml":
				return printYAML(string(data))
			default:
				printValue(string(data))
				return nil
			}
		},
	}
}

// newKVDelCmd creates the "kv del" subcommand.
func newKVDelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "del <key>",
		Short: "Delete a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := client.KV().Delete(cmd.Context(), key); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newKVListCmd creates the "kv list" subcommand.
func newKVListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [prefix]",
		Short: "List keys, optionally filtered by prefix",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) > 0 {
				prefix = args[0]
			}

			keys, err := client.KV().List(cmd.Context(), prefix)
			if err != nil {
				return err
			}

			return printList(keys, outputFormat)
		},
	}
}

// newKVExistsCmd creates the "kv exists" subcommand.
func newKVExistsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exists <key>",
		Short: "Check if a key exists",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			_, err := client.KV().Get(cmd.Context(), key)
			if err != nil {
				var knf *transport.KeyNotFoundError
				if errors.As(err, &knf) {
					printValue("false")
					return nil
				}
				return err
			}
			printValue("true")
			return nil
		},
	}
}
