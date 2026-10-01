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
	"os"
	"strings"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

const (
	// defaultUnixSocket is the well-known Unix socket path for qrdbd.
	defaultUnixSocket = "/var/run/qrdbd/qrdbd.sock"

	// defaultGRPCAddress is the default gRPC server address.
	defaultGRPCAddress = "localhost:63003"

	// defaultQUICAddress is the default QUIC server address.
	defaultQUICAddress = "localhost:63004"

	// transportREST identifies the REST transport type.
	transportREST = "rest"

	// transportGRPC identifies the gRPC transport type.
	transportGRPC = "grpc"

	// transportUnix identifies the Unix socket transport type.
	transportUnix = "unix"

	// transportMCP identifies the MCP transport type.
	transportMCP = "mcp"

	// transportQUIC identifies the QUIC transport type.
	transportQUIC = "quic"

	// transportEmbedded names the in-process transport that links the
	// server side directly. It is only available in the desktop/GUI
	// build; the CLI returns EmbeddedTransportUnavailableError when an
	// operator selects it.
	transportEmbedded = "embedded"
)

// resolveTransport determines the transport type and server address from
// the provided flags. The precedence intentionally honors an explicit
// --transport before any URL scheme on --server; PersistentPreRunE in
// root.go is responsible for clearing the transport argument when the
// flag is still at its default value so URL-style --server addresses
// continue to auto-detect.
//
// Precedence:
//
//  1. If --transport is non-empty, honor it. When --server is empty,
//     fill in a sensible per-transport default address.
//  2. Otherwise auto-detect from any URL scheme on --server.
//  3. Otherwise fall back to the default Unix socket if it exists; the
//     final fallback is gRPC to localhost.
func resolveTransport(serverFlag, transportFlag string) (transportType string, address string) {
	// Explicit transport flag takes priority.
	if transportFlag != "" {
		address = serverFlag
		transportType = transportFlag

		// Provide default addresses when --server is not specified.
		if address == "" {
			switch transportType {
			case transportUnix:
				address = defaultUnixSocket
			case transportGRPC:
				address = defaultGRPCAddress
			case transportQUIC:
				address = defaultQUICAddress
			case transportEmbedded:
				address = ""
			default:
				address = defaultGRPCAddress
			}
		}
		return transportType, address
	}

	// Auto-detect from --server scheme.
	if serverFlag != "" {
		if serverHasScheme(serverFlag) {
			return resolveSchemeTransport(serverFlag)
		}
		// No scheme: default to gRPC for legacy compatibility with
		// operators that supply host:port directly.
		return transportGRPC, serverFlag
	}

	// Auto-detect: check if the default Unix socket exists.
	if _, err := os.Stat(defaultUnixSocket); err == nil {
		return transportUnix, defaultUnixSocket
	}

	// Fallback: gRPC to default address.
	return transportGRPC, defaultGRPCAddress
}

// resolveSchemeTransport maps a URL-scheme-prefixed server value to the
// transport type and stripped/translated address. The caller has
// already verified the value carries one of the known scheme prefixes.
func resolveSchemeTransport(serverFlag string) (transportType string, address string) {
	switch {
	case strings.HasPrefix(serverFlag, "unix://"):
		return transportUnix, strings.TrimPrefix(serverFlag, "unix://")
	case strings.HasPrefix(serverFlag, "https://"), strings.HasPrefix(serverFlag, "http://"):
		return transportREST, serverFlag
	case strings.HasPrefix(serverFlag, "quics://"):
		return transportQUIC, strings.TrimPrefix(serverFlag, "quics://")
	case strings.HasPrefix(serverFlag, "quic://"):
		return transportQUIC, strings.TrimPrefix(serverFlag, "quic://")
	case strings.HasPrefix(serverFlag, "grpcs://"):
		return transportGRPC, strings.TrimPrefix(serverFlag, "grpcs://")
	case strings.HasPrefix(serverFlag, "grpc://"):
		return transportGRPC, strings.TrimPrefix(serverFlag, "grpc://")
	case strings.HasPrefix(serverFlag, "mcp://"):
		// MCP expects a full HTTPS URL with the /mcp endpoint suffix.
		host := strings.TrimPrefix(serverFlag, "mcp://")
		return transportMCP, "https://" + host + "/mcp"
	}
	// Unreachable: caller only invokes this when serverHasScheme is true.
	return transportGRPC, serverFlag
}

// scopeClientToDatabase returns a client whose data-plane operations are
// scoped to the named database, mirroring the GUI's per-database
// workspace. Client.DatabaseScoped refuses rather than falls back when the
// transport cannot scope, so the CLI fails loudly instead of silently
// operating on the default database.
func scopeClientToDatabase(c *sdk.Client, database, transportType string) (*sdk.Client, error) {
	scoped, err := c.DatabaseScoped(database)
	if err != nil {
		return nil, &DatabaseScopeError{Database: database, Transport: transportType, Err: err}
	}
	return scoped, nil
}

// buildClient constructs an SDK client using the resolved transport type
// and address. TLS options are applied when the corresponding flags are
// provided.
func buildClient(ctx context.Context, transportType, address, caFile, certFile, keyFile string, timeout time.Duration) (*sdk.Client, error) {
	if transportType == transportEmbedded {
		return nil, &EmbeddedTransportUnavailableError{}
	}

	var opts []transport.Option

	opts = append(opts, transport.WithAddress(address))

	if timeout > 0 {
		opts = append(opts, transport.WithTimeout(timeout))
	}

	if caFile != "" || certFile != "" || keyFile != "" {
		opts = append(opts, transport.WithTLS(certFile, keyFile, caFile))
	}

	t, err := newTransport(transportType, opts...)
	if err != nil {
		return nil, &TransportError{Transport: transportType, Err: err}
	}

	client, err := sdk.NewClient(t)
	if err != nil {
		return nil, &TransportError{Transport: transportType, Err: err}
	}

	if err := client.Connect(ctx); err != nil {
		return nil, &ConnectError{Address: address, Err: err}
	}

	return client, nil
}

// newTransport creates the appropriate transport implementation based
// on the transport type string.
func newTransport(transportType string, opts ...transport.Option) (transport.Transport, error) {
	handler, ok := transportBuilders[transportType]
	if !ok {
		return nil, &InvalidArgError{Arg: "transport", Reason: "unsupported transport type: " + transportType}
	}
	return handler(opts...)
}

// transportBuilder is a function that constructs a transport from options.
type transportBuilder func(opts ...transport.Option) (transport.Transport, error)

// transportBuilders maps transport type names to their builder functions
// for O(1) dispatch. The "embedded" transport is intentionally absent;
// buildClient short-circuits it with EmbeddedTransportUnavailableError
// because the CLI build does not link the server in-process.
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
