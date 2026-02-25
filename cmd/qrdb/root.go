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
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/spf13/cobra"
)

// Package-level state shared across all subcommands. These are set
// during PersistentPreRunE from resolved flags.
var (
	client       *sdk.Client
	outputFormat string
)

// newRootCmd creates the root cobra.Command for the qrdb client CLI.
// It registers global flags and all subcommands.
func newRootCmd() *cobra.Command {
	var (
		serverFlag    string
		transportFlag string
		caFile        string
		certFile      string
		keyFile       string
		timeout       time.Duration
	)

	rootCmd := &cobra.Command{
		Use:   "qrdb",
		Short: "QRDB client -- interact with a qrdbd server",
		Long:  "QRDB client CLI communicates with a qrdbd server using REST, gRPC, Unix socket, or MCP transports.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Skip client construction for commands that do not need it.
			if cmd.Name() == "version" || cmd.Name() == "help" {
				return nil
			}

			// Validate output format.
			format, _ := cmd.Root().PersistentFlags().GetString("output")
			if err := validateOutputFormat(format); err != nil {
				return err
			}
			outputFormat = format

			// Resolve transport type and address.
			transportType, address := resolveTransport(serverFlag, transportFlag)

			// Build and connect the SDK client.
			c, err := buildClient(cmd.Context(), transportType, address, caFile, certFile, keyFile, timeout)
			if err != nil {
				return err
			}
			client = c

			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global persistent flags.
	pflags := rootCmd.PersistentFlags()
	pflags.StringVar(&serverFlag, "server", "", "Server address (default: auto-detect)")
	pflags.StringVar(&transportFlag, "transport", "", "Transport type: rest, grpc, unix, mcp (default: auto)")
	pflags.StringVar(&caFile, "ca-file", "", "TLS CA certificate file")
	pflags.StringVar(&certFile, "cert-file", "", "TLS client certificate file")
	pflags.StringVar(&keyFile, "key-file", "", "TLS client key file")
	pflags.StringVar(&outputFormat, "output", "table", "Output format: table, json, yaml")
	pflags.DurationVar(&timeout, "timeout", 10*time.Second, "Request timeout")

	// Subcommand groups.
	kvCmd := newKVCmd()
	rootCmd.AddCommand(kvCmd)
	rootCmd.AddCommand(newAdminCmd())
	rootCmd.AddCommand(newVersionCmd())

	// Top-level aliases for common KV operations.
	rootCmd.AddCommand(newPutAliasCmd(kvCmd))
	rootCmd.AddCommand(newGetAliasCmd(kvCmd))
	rootCmd.AddCommand(newDelAliasCmd(kvCmd))

	return rootCmd
}

// validOutputFormats is the set of supported output formats for O(1) lookup.
var validOutputFormats = map[string]struct{}{
	"table": {},
	"json":  {},
	"yaml":  {},
}

// validateOutputFormat checks whether the given format is supported.
func validateOutputFormat(format string) error {
	if _, ok := validOutputFormats[format]; !ok {
		return &OutputFormatError{Format: format}
	}
	return nil
}

// newPutAliasCmd creates a top-level "put" command that delegates to "kv put".
func newPutAliasCmd(kvCmd *cobra.Command) *cobra.Command {
	for _, sub := range kvCmd.Commands() {
		if sub.Name() == "put" {
			alias := *sub
			alias.Use = "put <key> <value>"
			alias.Aliases = nil
			return &alias
		}
	}
	return nil
}

// newGetAliasCmd creates a top-level "get" command that delegates to "kv get".
func newGetAliasCmd(kvCmd *cobra.Command) *cobra.Command {
	for _, sub := range kvCmd.Commands() {
		if sub.Name() == "get" {
			alias := *sub
			alias.Use = "get <key>"
			alias.Aliases = nil
			return &alias
		}
	}
	return nil
}

// newDelAliasCmd creates a top-level "del" command that delegates to "kv del".
func newDelAliasCmd(kvCmd *cobra.Command) *cobra.Command {
	for _, sub := range kvCmd.Commands() {
		if sub.Name() == "del" {
			alias := *sub
			alias.Use = "del <key>"
			alias.Aliases = nil
			return &alias
		}
	}
	return nil
}
