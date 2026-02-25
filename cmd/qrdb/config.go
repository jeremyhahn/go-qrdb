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
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

const (
	// defaultUnixSocket is the well-known Unix socket path for qrdbd.
	defaultUnixSocket = "/var/run/qrdbd/qrdbd.sock"

	// defaultGRPCAddress is the default gRPC server address.
	defaultGRPCAddress = "localhost:63003"

	// transportREST identifies the REST transport type.
	transportREST = "rest"

	// transportGRPC identifies the gRPC transport type.
	transportGRPC = "grpc"

	// transportUnix identifies the Unix socket transport type.
	transportUnix = "unix"

	// transportMCP identifies the MCP transport type.
	transportMCP = "mcp"
)

// resolveTransport determines the transport type and server address from
// the provided flags. The auto-detection logic:
//  1. If --transport is explicitly set, use it.
//  2. If --server starts with "unix://", use unix transport.
//  3. If --server starts with "http://" or "https://", use rest transport.
//  4. If the default Unix socket exists, use unix transport.
//  5. Default: gRPC transport to localhost:63003.
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
			default:
				address = defaultGRPCAddress
			}
		}
		return transportType, address
	}

	// Auto-detect from --server scheme.
	if serverFlag != "" {
		if strings.HasPrefix(serverFlag, "unix://") {
			return transportUnix, strings.TrimPrefix(serverFlag, "unix://")
		}
		if strings.HasPrefix(serverFlag, "http://") || strings.HasPrefix(serverFlag, "https://") {
			return transportREST, serverFlag
		}
		// No scheme: default to gRPC.
		return transportGRPC, serverFlag
	}

	// Auto-detect: check if the default Unix socket exists.
	if _, err := os.Stat(defaultUnixSocket); err == nil {
		return transportUnix, defaultUnixSocket
	}

	// Fallback: gRPC to default address.
	return transportGRPC, defaultGRPCAddress
}

// buildClient constructs an SDK client using the resolved transport type
// and address. TLS options are applied when the corresponding flags are
// provided.
func buildClient(ctx context.Context, transportType, address, caFile, certFile, keyFile string, timeout time.Duration) (*sdk.Client, error) {
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
}
