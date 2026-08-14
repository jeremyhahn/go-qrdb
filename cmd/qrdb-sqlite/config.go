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

const (
	// modeEmbedded runs QRDB in-process with no separate daemon.
	modeEmbedded = "embedded"

	// modeClient connects to an existing qrdbd over a network transport.
	modeClient = "client"

	// transportREST identifies the HTTP REST transport.
	transportREST = "rest"

	// transportGRPC identifies the gRPC transport.
	transportGRPC = "grpc"

	// transportUnix identifies the Unix-socket (gRPC over UDS) transport.
	transportUnix = "unix"

	// transportMCP identifies the MCP transport.
	transportMCP = "mcp"

	// transportQUIC identifies the QUIC transport.
	transportQUIC = "quic"

	// consistencyLinearizable is the strictest consistency level.
	consistencyLinearizable = "linearizable"

	// consistencySerializable provides serializable isolation.
	consistencySerializable = "serializable"

	// consistencyBoundedStaleness allows bounded staleness reads.
	consistencyBoundedStaleness = "bounded-staleness"

	// consistencyEventual allows eventual consistency reads.
	consistencyEventual = "eventual"

	// writeModeSync commits every write through Raft with full fsync
	// durability before acknowledging. This is qrdb's canonical write-mode
	// vocabulary (shared with the qrdb CLI's --write-mode flag and the
	// vtab qrdb_write_mode PRAGMA the gateway seeds from this setting).
	writeModeSync = "sync"

	// writeModeWALDurable acknowledges after the write is durable in the
	// WAL but before full state-machine fsync.
	writeModeWALDurable = "wal_durable"

	// writeModeBatched coalesces writes for throughput at the cost of
	// per-write durability guarantees.
	writeModeBatched = "batched"

	// defaultMode is the default gateway operating mode.
	defaultMode = modeClient

	// defaultTransport is the default transport for client mode.
	defaultTransport = transportGRPC

	// defaultConsistency is the default consistency level.
	defaultConsistency = consistencyLinearizable

	// defaultWriteMode is the default write mode.
	defaultWriteMode = writeModeSync

	// defaultChunkSize is the default prefetch chunk size in rows.
	defaultChunkSize = 256
)

// validModes is the set of accepted gateway operating modes for O(1) lookup.
var validModes = map[string]struct{}{
	modeEmbedded: {},
	modeClient:   {},
}

// validTransports is the set of accepted transport types for O(1) lookup.
var validTransports = map[string]struct{}{
	transportREST: {},
	transportGRPC: {},
	transportUnix: {},
	transportMCP:  {},
	transportQUIC: {},
}

// validConsistencyLevels is the set of accepted consistency levels for O(1) lookup.
var validConsistencyLevels = map[string]struct{}{
	consistencyLinearizable:     {},
	consistencySerializable:     {},
	consistencyBoundedStaleness: {},
	consistencyEventual:         {},
}

// validWriteModes is the set of accepted write modes for O(1) lookup. It
// mirrors vtab's qrdb_write_mode PRAGMA vocabulary because runShell seeds
// that PRAGMA from this flag.
var validWriteModes = map[string]struct{}{
	writeModeSync:       {},
	writeModeWALDurable: {},
	writeModeBatched:    {},
}

// gatewayConfig holds the fully-resolved configuration for the qrdb-sqlite
// gateway. It is populated from Cobra persistent flags during PersistentPreRunE
// and is shared across all subcommands.
type gatewayConfig struct {
	// mode selects the operating mode: "embedded" or "client".
	mode string

	// server is the qrdbd address for client mode. Supported schemes:
	// unix://, quic://, grpc://, rest://, mcp://.
	// Ignored when mode is "embedded".
	server string

	// configFile is the path to a qrdb YAML configuration file used when
	// mode is "embedded" to initialise the in-process server.
	configFile string

	// database is the QRDB logical database name to expose as SQLite tables.
	database string

	// transport explicitly sets the transport protocol for client mode.
	// When empty the scheme from server is used for auto-detection.
	transport string

	// consistency is the default consistency level for read operations.
	consistency string

	// writeMode is the default write routing strategy.
	writeMode string

	// coalescing enables the coalescing transport wrapper, which batches
	// small concurrent requests to reduce round-trip overhead.
	coalescing bool

	// chunkSize controls how many rows are prefetched per virtual-table
	// scan iteration.
	chunkSize int

	// debug enables verbose debug logging.
	debug bool

	// caFile is the path to the TLS CA certificate used to verify the
	// server. Required (together with the daemon's own TLS bundle) for
	// every remote transport, including unix (grpc-over-UDS), which is
	// "verified TLS 1.3 only" -- identical to gRPC, REST, QUIC, and MCP.
	caFile string

	// certFile is the path to the client TLS certificate presented for
	// mutual TLS authentication.
	certFile string

	// keyFile is the path to the client TLS private key paired with certFile.
	keyFile string
}

// validate checks that all fields contain accepted values and returns a
// *ConfigError for the first violation found.
func (c *gatewayConfig) validate() error {
	if _, ok := validModes[c.mode]; !ok {
		return &ConfigError{
			Field:  "mode",
			Reason: "must be one of: embedded, client",
		}
	}

	if c.mode == modeClient {
		if c.server == "" {
			return &ConfigError{
				Field:  "server",
				Reason: "required when mode is client",
			}
		}
		if c.transport != "" {
			if _, ok := validTransports[c.transport]; !ok {
				return &ConfigError{
					Field:  "transport",
					Reason: "must be one of: rest, grpc, unix, mcp, quic",
				}
			}
		}
	}

	if c.mode == modeEmbedded && c.configFile == "" {
		return &ConfigError{
			Field:  "config",
			Reason: "required when mode is embedded",
		}
	}

	if _, ok := validConsistencyLevels[c.consistency]; !ok {
		return &ConfigError{
			Field:  "consistency",
			Reason: "must be one of: linearizable, serializable, bounded-staleness, eventual",
		}
	}

	if _, ok := validWriteModes[c.writeMode]; !ok {
		return &ConfigError{
			Field:  "write-mode",
			Reason: "must be one of: sync, wal_durable, batched",
		}
	}

	if c.chunkSize <= 0 {
		return &ConfigError{
			Field:  "chunk-size",
			Reason: "must be a positive integer",
		}
	}

	return nil
}
