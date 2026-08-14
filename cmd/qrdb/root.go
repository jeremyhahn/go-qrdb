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
	"time"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// annotationSkipClient marks a command whose PersistentPreRunE must skip
// SDK client construction (and the server connection it performs). Used by
// deprecation-redirect stubs that only emit a "command moved" error and by
// any other command that must run without a reachable daemon.
const annotationSkipClient = "qrdb/skip-client"

// validLogLevels is the set of supported log levels for O(1) lookup.
var validLogLevels = map[string]struct{}{
	"trace": {},
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

// validTransports is the canonical set of root-level --transport values
// shared by every CLI binary in the stack (qrdb / xkmsctl /
// quicbaasctl / platformctl). O(1) lookup; the values match the keys
// of transportBuilders in config.go plus the "embedded" sentinel which
// requires the desktop/GUI build.
var validTransports = map[string]struct{}{
	transportREST:     {},
	transportGRPC:     {},
	transportQUIC:     {},
	transportMCP:      {},
	transportUnix:     {},
	transportEmbedded: {},
}

// schemePrefixes lists the URL prefixes that, when present on --server,
// override the default --transport value. This lets operators say
// `--server https://host` without having to also supply
// `--transport rest`. Listed longest-first to ensure correct prefix
// matching by the resolver.
var schemePrefixes = []string{
	"https://",
	"grpcs://",
	"quics://",
	"unix://",
	"http://",
	"grpc://",
	"quic://",
	"mcp://",
}

// Package-level state shared across all subcommands. These are set
// during PersistentPreRunE from resolved flags.
var (
	client       *sdk.Client
	outputFormat string
	logLevel     string
	logFile      string
)

// newRootCmd creates the root cobra.Command for the qrdb client CLI.
// It registers global flags and all subcommands.
//
// The canonical persistent flag set documented here is the gold-standard
// surface mirrored by xkmsctl, quicbaasctl, and platformctl. In
// particular:
//
//   - --transport defaults to "unix" (most secure local).
//   - Supported values: rest | grpc | quic | mcp | unix | embedded.
//   - A scheme on --server (e.g. https://, grpcs://, mcp://) overrides
//     the --transport default so URL-style addresses "just work" without
//     forcing operators to also pass --transport.
//   - --output is the persistent output-format flag honored by every
//     read-only subcommand.
func newRootCmd() *cobra.Command {
	var (
		serverFlag    string
		transportFlag string
		caFile        string
		certFile      string
		keyFile       string
		timeout       time.Duration
		databaseFlag  string
	)

	rootCmd := &cobra.Command{
		Use:   "qrdb",
		Short: "QRDB client -- interact with a qrdbd server",
		Long: `QRDB client CLI.

The client talks to a qrdbd server over one of six transports. Operators
select the transport with --transport (default: unix). A URL scheme on
--server overrides the default so https://, grpcs://, mcp://, unix://
addresses work without an explicit --transport.

Supported transports:
  rest      HTTP/JSON REST API (default port 63002)
  grpc      gRPC (default port 63003)
  quic      QUIC (default port 63004)
  mcp       Model Context Protocol over HTTPS
  unix      Unix domain socket (default: /var/run/qrdbd/qrdbd.sock) [DEFAULT]
  embedded  In-process server (desktop/GUI build only)`,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Skip client construction for commands that do not need a
			// server connection: version/help, and any command annotated
			// with annotationSkipClient (e.g. the deprecation-redirect
			// stubs, which must emit their redirect even with no daemon
			// reachable).
			if cmd.Name() == "version" || cmd.Name() == "help" ||
				cmd.Annotations[annotationSkipClient] == "true" {
				return nil
			}

			// Validate output format.
			format, _ := cmd.Root().PersistentFlags().GetString("output")
			if err := validateOutputFormat(format); err != nil {
				return err
			}
			outputFormat = format

			// Validate transport name against the canonical set.
			if err := validateTransport(transportFlag); err != nil {
				return err
			}

			// Apply the "scheme overrides default --transport" rule:
			// when --transport is still at its default value AND
			// --server carries a recognized URL scheme, clear the
			// transport so resolveTransport auto-detects from the
			// scheme. An explicitly-supplied --transport always wins.
			effectiveTransport := transportFlag
			if !cmd.Root().PersistentFlags().Changed("transport") &&
				serverHasScheme(serverFlag) {
				effectiveTransport = ""
			}

			transportType, address := resolveTransport(serverFlag, effectiveTransport)

			// Build and connect the SDK client.
			c, err := buildClient(cmd.Context(), transportType, address, caFile, certFile, keyFile, timeout)
			if err != nil {
				return err
			}

			// Scope data-plane operations to a named database when
			// requested, mirroring the GUI's per-database workspace.
			if databaseFlag != "" {
				c, err = scopeClientToDatabase(c, databaseFlag, transportType)
				if err != nil {
					return err
				}
			}
			client = c

			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global persistent flags.
	pflags := rootCmd.PersistentFlags()
	pflags.StringVar(&serverFlag, "server", "",
		"Server address (default: auto-detect; a URL scheme overrides --transport)")
	pflags.StringVar(&transportFlag, "transport", transportUnix,
		"Transport type: rest | grpc | quic | mcp | unix | embedded")
	pflags.StringVar(&caFile, "ca-file", "", "TLS CA certificate file")
	pflags.StringVar(&certFile, "cert-file", "", "TLS client certificate file")
	pflags.StringVar(&keyFile, "key-file", "", "TLS client key file")
	pflags.StringVar(&outputFormat, "output", "table", "Output format: table | json | yaml")
	pflags.DurationVar(&timeout, "timeout", 10*time.Second, "Request timeout")
	pflags.StringVar(&logLevel, "log-level", "info", "Log level: trace | debug | info | warn | error")
	pflags.StringVar(&logFile, "log-file", "", "Log file path (default: stdout)")
	pflags.StringVar(&databaseFlag, "database", "",
		"Database to scope data-plane commands to (default: the server's default database)")

	// Subcommand groups.
	kvCmd := newKVCmd()
	rootCmd.AddCommand(kvCmd)
	rootCmd.AddCommand(newAdminCmd())
	rootCmd.AddCommand(newTxCmd())
	rootCmd.AddCommand(newBatchCmd())
	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newLeaseCmd())
	rootCmd.AddCommand(newLockCmd())
	if cmd := newTSCmd(); cmd != nil {
		rootCmd.AddCommand(cmd)
	}
	if cmd := newVectorCmd(); cmd != nil {
		rootCmd.AddCommand(cmd)
	}
	rootCmd.AddCommand(newIndexCmd())
	rootCmd.AddCommand(newDatabaseCmd())
	rootCmd.AddCommand(newCollectionCmd())
	rootCmd.AddCommand(newSQLCmd())
	rootCmd.AddCommand(newAuditCmd())
	rootCmd.AddCommand(newUsersCmd())
	rootCmd.AddCommand(newRolesCmd())

	// The tenant command tree is assembled here (rather than inside
	// newTenantCmd itself) so the tenant-scoped Security Officer surface
	// (barrier.go's newTenantBarrierOfficerCmd/newTenantBarrierShamirSealVoteCmd)
	// can be grafted on without editing tenant.go.
	tenantCmd := newTenantCmd()
	tenantCmd.AddCommand(newTenantBarrierOfficerCmd(), newTenantBarrierShamirSealVoteCmd())
	rootCmd.AddCommand(tenantCmd)

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newBootstrapCmd())
	rootCmd.AddCommand(newCACmd())
	rootCmd.AddCommand(newCertsRedirectCmd())
	rootCmd.AddCommand(newCredentialCmd())
	rootCmd.AddCommand(newBarrierCmd())
	rootCmd.AddCommand(newCeremonyCmd())
	rootCmd.AddCommand(newBackupCmd())
	rootCmd.AddCommand(newRebalanceCmd())
	rootCmd.AddCommand(newEmbeddingCmd())
	if cmd := newRAGCmd(); cmd != nil {
		rootCmd.AddCommand(cmd)
	}
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

// validateTransport checks that the supplied --transport value names
// one of the canonical transports. An empty value is accepted as
// "auto" (preserved for backward compatibility with tooling that
// already explicitly clears the flag); the resolver then falls back to
// the auto-detect path.
func validateTransport(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := validTransports[name]; !ok {
		return &InvalidArgError{
			Arg:    "--transport",
			Reason: "must be one of rest, grpc, quic, mcp, unix, embedded",
		}
	}
	return nil
}

// serverHasScheme reports whether the --server value carries a URL
// scheme that the resolver should honor over the default --transport.
func serverHasScheme(server string) bool {
	for _, p := range schemePrefixes {
		if strings.HasPrefix(server, p) {
			return true
		}
	}
	return false
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
