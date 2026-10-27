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

package server

import (
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
)

// MCPServer is the combined MCP server qrdbd serves: the KV and admin tools
// over the given services, every tool an option adds, and, when the
// sealed-window probe and dispatch options are applied, the sealed-window
// tools behind the same endpoint. Start serves HTTPS on /mcp, Addr reports
// the bound address, and Stop shuts it down.
type MCPServer = mcpapi.Server

// MCPServerOption configures an MCPServer.
type MCPServerOption = mcpapi.ServerOption

// MCPTLSNotConfiguredError is returned by MCPServer.Start when no TLS
// configuration was supplied: the transport is TLS-only and fails closed.
type MCPTLSNotConfiguredError = mcpapi.TLSNotConfiguredError

// NewMCPServer builds the combined MCP server over kvSvc and adminSvc (either
// may be nil), listening on addr unless WithMCPServerListener hands it a
// bound listener. It is the constructor qrdbd's orchestrator calls.
var NewMCPServer = mcpapi.NewServer

// WithMCPServerTLS sets the server's TLS configuration, floored at TLS 1.3.
// Start refuses to serve without one.
var WithMCPServerTLS = mcpapi.WithTLS

// WithMCPServerListener makes Start serve on a plain TCP listener the caller
// bound, instead of binding the address. The server owns the listener from
// Start on.
var WithMCPServerListener = mcpapi.WithListener

// WithMCPServerMaxConnections caps simultaneously open connections
// (DefaultMCPMaxConnections when unset).
var WithMCPServerMaxConnections = mcpapi.WithMaxConnections

// WithMCPServerMaxBodyBytes bounds an authenticated request body.
var WithMCPServerMaxBodyBytes = mcpapi.WithMaxBodyBytes

// WithMCPServerMaxConnsPerPeer caps the concurrent connections one remote IP
// may hold open.
var WithMCPServerMaxConnsPerPeer = mcpapi.WithMaxConnsPerPeer

// WithMCPServerAuthMiddleware authenticates the mTLS peer and authorizes
// every tool call, default-deny. Without it the server runs unauthenticated.
var WithMCPServerAuthMiddleware = mcpapi.WithAuthMiddleware

// WithMCPServerSealedWindowProbe refuses every tool call not on
// MCPSealedWindowAllowlist while the probe reports the barrier sealed.
var WithMCPServerSealedWindowProbe = mcpapi.WithSealedWindowProbe

// WithMCPServerSealedWindowDispatch serves a request that arrives while the
// node barrier is sealed exactly as the boot-time sealed-window listener
// does, with the tools SealedWindowMCPTools builds. Build the probe with
// NewSealedWindowListenerProbe over the bare node barrier.
var WithMCPServerSealedWindowDispatch = mcpapi.WithSealedWindowDispatch
