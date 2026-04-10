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
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
)

// Package-level gateway state shared across all subcommands. These are
// populated during PersistentPreRunE before any subcommand runs.
var (
	// cfg holds the resolved gateway configuration.
	cfg gatewayConfig

	// client is the connected QRDB SDK client for client mode.
	// Nil when running in embedded mode.
	client *sdk.Client
)

// newRootCmd creates the root cobra.Command for qrdb-sqlite. All persistent
// flags are declared here and validated in PersistentPreRunE. The "shell"
// subcommand is the default action when no explicit subcommand is provided.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "qrdb-sqlite",
		Short: "SQLite virtual-table gateway for QRDB",
		Long: `qrdb-sqlite exposes a QRDB server as a set of SQLite virtual tables,
enabling standard SQL tools to query and mutate QRDB data over any
supported transport (gRPC, REST, QUIC, MCP, Unix socket) or as an
embedded in-process instance.`,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Commands that do not interact with QRDB skip connection setup.
			if cmd.Name() == "version" || cmd.Name() == "help" {
				return nil
			}

			// Apply flag values to the shared gateway config.
			pflags := cmd.Root().PersistentFlags()
			cfg.mode, _ = pflags.GetString("mode")
			cfg.server, _ = pflags.GetString("server")
			cfg.configFile, _ = pflags.GetString("config")
			cfg.database, _ = pflags.GetString("database")
			cfg.transport, _ = pflags.GetString("transport")
			cfg.consistency, _ = pflags.GetString("consistency")
			cfg.writeMode, _ = pflags.GetString("write-mode")
			cfg.coalescing, _ = pflags.GetBool("coalescing")
			cfg.chunkSize, _ = pflags.GetInt("chunk-size")
			cfg.debug, _ = pflags.GetBool("debug")

			if err := cfg.validate(); err != nil {
				return err
			}

			if cfg.mode == modeClient {
				c, err := buildClient(cmd.Context(), &cfg)
				if err != nil {
					return err
				}
				client = c
			}

			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Persistent flags available to all subcommands.
	pflags := rootCmd.PersistentFlags()
	pflags.String("mode", defaultMode, "Operating mode: embedded, client")
	pflags.String("server", "", "Server address for client mode (unix://, quic://, grpc://, rest://, mcp://)")
	pflags.String("database", "", "QRDB database name to expose as SQLite tables")
	pflags.String("config", "", "Path to qrdb YAML configuration file (required for embedded mode)")
	pflags.String("transport", defaultTransport, "Transport type for client mode: rest, grpc, unix, mcp, quic")
	pflags.String("consistency", defaultConsistency, "Default consistency level: linearizable, serializable, bounded-staleness, eventual")
	pflags.String("write-mode", defaultWriteMode, "Default write routing: leader, local")
	pflags.Bool("coalescing", false, "Enable coalescing transport to batch concurrent requests")
	pflags.Int("chunk-size", defaultChunkSize, "Prefetch chunk size (rows) for virtual table scans")
	pflags.Bool("debug", false, "Enable debug logging")

	rootCmd.AddCommand(newShellCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}

// transportBuilder is a function that constructs a transport from options.
type transportBuilder func(opts ...transport.Option) (transport.Transport, error)

// transportBuilders maps transport type names to their builder functions
// for O(1) dispatch.
var transportBuilders = map[string]transportBuilder{
	transportREST: func(opts ...transport.Option) (transport.Transport, error) {
		return resttransport.New(opts...)
	},
	transportGRPC: func(opts ...transport.Option) (transport.Transport, error) {
		return grpctransport.New(opts...)
	},
	transportUnix: func(opts ...transport.Option) (transport.Transport, error) {
		return unixtransport.New(opts...)
	},
	transportMCP: func(opts ...transport.Option) (transport.Transport, error) {
		return mcptransport.New(opts...)
	},
	transportQUIC: func(opts ...transport.Option) (transport.Transport, error) {
		return quictransport.New(opts...)
	},
}

// buildClient resolves the transport from c and constructs a connected
// SDK client. Returns a *TransportError or *ConnectionError on failure.
func buildClient(ctx context.Context, c *gatewayConfig) (*sdk.Client, error) {
	transportType, address := resolveTransport(c.server, c.transport)

	var opts []transport.Option
	opts = append(opts, transport.WithAddress(address))

	builder, ok := transportBuilders[transportType]
	if !ok {
		return nil, &TransportError{
			Transport: transportType,
			Err: &ConfigError{
				Field:  "transport",
				Reason: "unsupported transport type: " + transportType,
			},
		}
	}

	t, err := builder(opts...)
	if err != nil {
		return nil, &TransportError{Transport: transportType, Err: err}
	}

	sdkClient, err := sdk.NewClient(t)
	if err != nil {
		return nil, &TransportError{Transport: transportType, Err: err}
	}

	if err := sdkClient.Connect(ctx); err != nil {
		return nil, &ConnectionError{Address: address, Err: err}
	}

	return sdkClient, nil
}

// resolveTransport determines the effective transport type and server address
// from the gateway configuration. Auto-detection logic:
//  1. If transport is explicitly set, use it with the provided server address.
//  2. If server has a recognised scheme prefix, derive transport from that.
//  3. Default: gRPC to the provided server address.
func resolveTransport(server, explicitTransport string) (transportType, address string) {
	if explicitTransport != "" {
		return explicitTransport, server
	}

	switch {
	case strings.HasPrefix(server, "unix://"):
		return transportUnix, strings.TrimPrefix(server, "unix://")
	case strings.HasPrefix(server, "https://"), strings.HasPrefix(server, "http://"):
		return transportREST, server
	case strings.HasPrefix(server, "quics://"):
		return transportQUIC, strings.TrimPrefix(server, "quics://")
	case strings.HasPrefix(server, "quic://"):
		return transportQUIC, strings.TrimPrefix(server, "quic://")
	case strings.HasPrefix(server, "grpcs://"):
		return transportGRPC, strings.TrimPrefix(server, "grpcs://")
	case strings.HasPrefix(server, "grpc://"):
		return transportGRPC, strings.TrimPrefix(server, "grpc://")
	case strings.HasPrefix(server, "mcp://"):
		host := strings.TrimPrefix(server, "mcp://")
		return transportMCP, "https://" + host + "/mcp"
	default:
		return transportGRPC, server
	}
}

// newShellCmd creates the interactive SQLite shell subcommand. It wires the
// QRDB SDK client (built during PersistentPreRunE) to a set of virtual table
// adapters, registers all adapters as SQLite virtual table modules within a
// single custom driver, opens an in-memory SQLite database, runs auto-discovery
// to create virtual tables for every QRDB table in the configured database, and
// then starts the interactive shell REPL.
func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Start an interactive SQLite shell backed by QRDB virtual tables",
		Long: `Opens an interactive SQLite shell with QRDB data exposed as virtual tables.
Each QRDB collection or key-value namespace appears as a queryable table.
Standard SQL SELECT, INSERT, UPDATE, and DELETE statements are supported.`,
		RunE: runShell,
	}
}

// runShell is the RunE implementation for the shell subcommand. It is
// separated from newShellCmd to keep the cobra boilerplate concise and to
// allow the core logic to be tested without constructing a full Command tree.
//
// Execution order:
//  1. Build the shared pragma store and apply flag overrides.
//  2. Fetch table metadata from the server (discovery phase).
//  3. Build store adapters for every discovered table.
//  4. Register a single custom SQLite driver whose ConnectHook installs all
//     virtual table modules — including per-entity and per-collection modules
//     resolved from the discovery results — into every new connection.
//  5. Open an in-memory SQLite database and ping it to confirm the hook runs.
//  6. Issue CREATE VIRTUAL TABLE statements for every discovered table.
//  7. Start the interactive shell REPL.
func runShell(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Build the session pragma store with production defaults, then apply any
	// flag-level overrides. Setters validate input, so errors surface here
	// before anything is opened.
	pragma := vtab.NewPragmaStore(cfg.mode, cfg.server, cfg.database)
	if cfg.consistency != "" {
		if err := pragma.SetConsistency(cfg.consistency); err != nil {
			return &ShellError{Err: err}
		}
	}
	if cfg.writeMode != "" {
		if err := pragma.SetWriteMode(cfg.writeMode); err != nil {
			return &ShellError{Err: err}
		}
	}

	// Fetch table metadata before registering the SQLite driver so that the
	// ConnectHook closure can capture the pre-fetched results. This avoids
	// making network calls from inside the hook, which fires synchronously
	// during sql.Open / db.Ping and must not block on I/O.
	metaProvider := newDiscoveryAdapter(client, cfg.database)
	discoveredTables, err := metaProvider.DiscoverTables(ctx)
	if err != nil {
		return &ShellError{Err: &MetadataError{Err: err}}
	}

	// Build store adapters that wrap the SDK client and inject session options
	// from the pragma store. These are defined in adapters.go.
	kvAdapter := newKVAdapter(client, pragma)
	collectionAdapter := newCollectionAdapter(client)
	datapointAdapter := newDatapointAdapter(client)
	metricsAdapter := newMetricsAdapter(client)
	namespaceAdapter := newNamespaceAdapter(client)
	vectorAdapter := newVectorAdapter(client)
	// entityAdapter is a shared store for all entity types; the entity type
	// is passed per-call so one adapter handles all types.
	entityAdapter := newEntityAdapter(client, pragma)

	// Use a unique driver name per invocation so that multiple shell sessions
	// running in the same process never trigger a duplicate database/sql
	// registration panic.
	driverName := "qrdb_sqlite_" + strconv.FormatInt(time.Now().UnixNano(), 36)

	// Register one custom driver that installs all virtual table modules into
	// every new SQLite connection via a single ConnectHook. All module names
	// must be registered before DiscoverAndRegister issues its DDL so that
	// SQLite can resolve "USING <module>" references.
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			if err := vtab.RegisterKVOnConn(conn, kvAdapter, pragma, cfg.chunkSize); err != nil {
				return err
			}
			if err := vtab.RegisterDatapointModule(conn, datapointAdapter, cfg.chunkSize); err != nil {
				return err
			}
			if err := vtab.RegisterMetricsModule(conn, metricsAdapter, cfg.chunkSize); err != nil {
				return err
			}
			if err := vtab.RegisterNamespaceOnConn(conn, namespaceAdapter, cfg.chunkSize); err != nil {
				return err
			}
			if err := vtab.RegisterVectorOnConn(conn, vectorAdapter, cfg.chunkSize); err != nil {
				return err
			}

			// Register per-entity-type modules using the pre-fetched discovery
			// results. Each entity type needs its own module so SQLite can
			// resolve distinct column schemas.
			for _, meta := range discoveredTables {
				if meta.Type != vtab.TableEntity {
					continue
				}
				if err := vtab.RegisterEntityFromMeta(conn, meta, entityAdapter, cfg.chunkSize); err != nil {
					return err
				}
			}

			// Register per-collection modules. Each collection carries a
			// unique module name ("qrdb_collection_<db>_<name>") so multiple
			// collections in the same database do not conflict.
			for _, meta := range discoveredTables {
				if meta.Type != vtab.TableCollection {
					continue
				}
				if err := vtab.RegisterCollectionFromMeta(conn, meta, collectionAdapter, cfg.chunkSize); err != nil {
					return err
				}
			}

			return nil
		},
	})

	// Open an in-memory SQLite database using the custom driver. ":memory:"
	// ensures nothing is written to disk. Each Open call is independent.
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return &ShellError{Err: err}
	}
	defer db.Close()

	// Ping to force an immediate connection, surfacing any ConnectHook errors
	// (e.g. module registration failures) before the shell REPL starts.
	if err := db.PingContext(ctx); err != nil {
		return &ShellError{Err: err}
	}

	// Issue CREATE VIRTUAL TABLE IF NOT EXISTS statements for every table
	// returned by the metadata provider. The module names referenced here
	// must already be registered via the ConnectHook above.
	if _, err := vtab.DiscoverAndRegister(ctx, db, metaProvider); err != nil {
		return &ShellError{Err: err}
	}

	// Start the interactive REPL. The shell reads SQL from stdin and formats
	// results to stdout. A non-nil return value indicates a genuine I/O error,
	// not a SQL error (those are printed and the REPL continues).
	sh := newShell(db, os.Stdout)
	return sh.run(os.Stdin)
}

// newVersionCmd creates the version subcommand that prints build metadata.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print qrdb-sqlite version information",
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Printf("qrdb-sqlite version %s (commit %s, built %s)\n",
				version, gitCommit, buildDate)
		},
	}
}
