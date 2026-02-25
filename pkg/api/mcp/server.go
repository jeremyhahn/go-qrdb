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

// Package mcp provides a Model Context Protocol server for QRDB.
// It exposes KV and admin operations as MCP tools, enabling AI agent
// interaction with the QRDB cluster.
package mcp

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Server is the MCP server for QRDB. It registers KV and admin operations
// as MCP tools and serves them over HTTP using the Streamable HTTP transport.
type Server struct {
	kvSvc     service.KVServicer
	adminSvc  service.AdminServicer
	addr      string
	tlsConfig *tls.Config
	mcpSrv    *mcpserver.MCPServer
	httpSrv   *http.Server
	listener  net.Listener
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the server with TLS.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tlsConfig = cfg
	}
}

// NewServer creates a new MCP server that listens on the given address
// and delegates to the provided service implementations. All KV and admin
// operations are registered as MCP tools.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	// Create the underlying MCP server with tool capabilities.
	s.mcpSrv = mcpserver.NewMCPServer(
		"qrdb",
		"1.0.0",
		mcpserver.WithToolCapabilities(false),
	)

	// Register all KV and admin tools.
	s.mcpSrv.AddTools(kvTools(kvSvc)...)
	s.mcpSrv.AddTools(adminTools(adminSvc)...)

	return s
}

// Start begins listening and serving MCP requests over HTTP. The MCP
// Streamable HTTP handler is mounted at /mcp. When TLS is configured,
// the listener is wrapped with tls.NewListener.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}

	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
	}
	s.listener = ln

	// Create the Streamable HTTP handler from the MCP server.
	streamHandler := mcpserver.NewStreamableHTTPServer(s.mcpSrv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamHandler)

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	proto := "HTTP"
	if s.tlsConfig != nil {
		proto = "HTTPS"
	}
	slog.Info("MCP server started", "addr", ln.Addr().String(), "proto", proto)

	go func() {
		if serveErr := s.httpSrv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("MCP server error", "error", serveErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the MCP server.
func (s *Server) Stop() {
	if s.httpSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		slog.Error("MCP server shutdown error", "error", err)
	}
	slog.Info("MCP server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// MCPServer returns the underlying mcp-go MCPServer. This is useful for
// testing tool registration and for advanced integrations.
func (s *Server) MCPServer() *mcpserver.MCPServer {
	return s.mcpSrv
}
