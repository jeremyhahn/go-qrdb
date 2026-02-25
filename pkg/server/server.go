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

// Package server provides the multi-protocol server orchestrator for QRDB.
// It manages the lifecycle of REST, gRPC, Unix socket, and MCP servers
// based on the configuration, starting them sequentially with rollback
// on failure and stopping them gracefully in reverse order.
package server

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	unixapi "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Server orchestrates the REST, gRPC, Unix socket, and MCP API servers
// for QRDB. It starts and stops servers based on the protocol configuration
// and manages their lifecycles with rollback on startup failure.
type Server struct {
	config   *config.Config
	kvSvc    service.KVServicer
	adminSvc service.AdminServicer
	restSrv  *rest.Server
	grpcSrv  *grpcapi.Server
	unixSrv  *unixapi.Server
	mcpSrv   *mcpapi.Server
	wg       sync.WaitGroup
	logger   *slog.Logger
	opts     serverOptions
}

// serverOptions holds optional dependencies injected via ServerOption.
type serverOptions struct {
	tlsConfig  *tls.Config
	caCertPool *x509.CertPool
	gatherer   prometheus.Gatherer
	resolver   rest.LeaderAdminResolver
	host       rest.Host
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*serverOptions)

// WithTLS configures TLS for REST, gRPC, and MCP servers.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(o *serverOptions) {
		o.tlsConfig = cfg
	}
}

// WithGatherer attaches a Prometheus Gatherer for the REST /metrics endpoint.
func WithGatherer(g prometheus.Gatherer) ServerOption {
	return func(o *serverOptions) {
		o.gatherer = g
	}
}

// WithLeaderForwarding enables transparent leader forwarding on the REST
// server for write operations.
func WithLeaderForwarding(resolver rest.LeaderAdminResolver) ServerOption {
	return func(o *serverOptions) {
		o.resolver = resolver
	}
}

// WithCACertPool sets the CA certificate pool for TLS peer verification
// in the REST server's forwarding client.
func WithCACertPool(pool *x509.CertPool) ServerOption {
	return func(o *serverOptions) {
		o.caCertPool = pool
	}
}

// WithHost sets the Host interface for REST leader forwarding headers.
func WithHost(h rest.Host) ServerOption {
	return func(o *serverOptions) {
		o.host = h
	}
}

// New creates a new multi-protocol server orchestrator. The server does not
// start until Start() is called.
func New(cfg *config.Config, kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...ServerOption) *Server {
	s := &Server{
		config:   cfg,
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		logger:   slog.Default(),
	}

	for _, opt := range opts {
		opt(&s.opts)
	}

	return s
}

// Start launches the configured API servers. Servers are started sequentially
// in order: REST, gRPC, Unix, MCP. If any server fails to start, all
// previously started servers are rolled back and the error is returned.
func (s *Server) Start() error {
	if s.config.Protocols.REST {
		if err := s.startREST(); err != nil {
			return err
		}
	}

	if s.config.Protocols.GRPC {
		if err := s.startGRPC(); err != nil {
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.Unix {
		if err := s.startUnix(); err != nil {
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.MCP {
		if err := s.startMCP(); err != nil {
			s.stopUnix()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	return nil
}

// Stop gracefully shuts down all running API servers in reverse startup
// order (MCP, Unix, gRPC, REST) and waits for all background goroutines
// to complete.
func (s *Server) Stop() {
	s.stopMCP()
	s.stopUnix()
	s.stopGRPC()
	s.stopREST()
	s.wg.Wait()
	s.logger.Info("server orchestrator stopped")
}

// RESTAddr returns the REST server's listener address, or empty string
// if REST is not running.
func (s *Server) RESTAddr() string {
	if s.restSrv != nil {
		return s.restSrv.Addr()
	}
	return ""
}

// GRPCAddr returns the gRPC server's listener address, or empty string
// if gRPC is not running.
func (s *Server) GRPCAddr() string {
	if s.grpcSrv != nil {
		return s.grpcSrv.Addr()
	}
	return ""
}

// UnixAddr returns the Unix socket server's listener address, or empty
// string if the Unix server is not running.
func (s *Server) UnixAddr() string {
	if s.unixSrv != nil {
		return s.unixSrv.Addr()
	}
	return ""
}

// MCPAddr returns the MCP server's listener address, or empty string
// if MCP is not running.
func (s *Server) MCPAddr() string {
	if s.mcpSrv != nil {
		return s.mcpSrv.Addr()
	}
	return ""
}

// startREST creates and starts the REST server with configured options.
func (s *Server) startREST() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.AdminPort))

	var restOpts []rest.ServerOption

	if s.opts.tlsConfig != nil {
		restOpts = append(restOpts, rest.WithTLS(s.opts.tlsConfig))
	}
	if s.opts.caCertPool != nil {
		restOpts = append(restOpts, rest.WithCACertPool(s.opts.caCertPool))
	}
	if s.opts.gatherer != nil {
		restOpts = append(restOpts, rest.WithGatherer(s.opts.gatherer))
	}
	if s.opts.resolver != nil {
		restOpts = append(restOpts, rest.WithLeaderForwarding(s.opts.resolver))
	}
	if s.opts.host != nil {
		restOpts = append(restOpts, rest.WithHost(s.opts.host))
	}

	s.restSrv = rest.NewServer(s.kvSvc, s.adminSvc, addr, restOpts...)

	if err := s.restSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("REST server started", "addr", s.restSrv.Addr())
	return nil
}

// startGRPC creates and starts the gRPC server with configured options.
func (s *Server) startGRPC() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.GRPCPort))

	var grpcOpts []grpcapi.ServerOption

	if s.opts.tlsConfig != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTLS(s.opts.tlsConfig))
	}

	s.grpcSrv = grpcapi.NewServer(s.kvSvc, s.adminSvc, addr, grpcOpts...)

	if err := s.grpcSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("gRPC server started", "addr", s.grpcSrv.Addr())
	return nil
}

// startUnix creates and starts the Unix domain socket server.
func (s *Server) startUnix() error {
	s.unixSrv = unixapi.NewServer(s.kvSvc, s.adminSvc, s.config.Unix.SocketPath)

	if err := s.unixSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("Unix socket server started", "path", s.unixSrv.Addr())
	return nil
}

// startMCP creates and starts the MCP server with configured options.
func (s *Server) startMCP() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.MCP.Port))

	var mcpOpts []mcpapi.ServerOption

	if s.opts.tlsConfig != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTLS(s.opts.tlsConfig))
	}

	s.mcpSrv = mcpapi.NewServer(s.kvSvc, s.adminSvc, addr, mcpOpts...)

	if err := s.mcpSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("MCP server started", "addr", s.mcpSrv.Addr())
	return nil
}

// stopREST stops the REST server if it was started.
func (s *Server) stopREST() {
	if s.restSrv != nil {
		s.restSrv.Stop()
	}
}

// stopGRPC stops the gRPC server if it was started.
func (s *Server) stopGRPC() {
	if s.grpcSrv != nil {
		s.grpcSrv.Stop()
	}
}

// stopUnix stops the Unix socket server if it was started.
func (s *Server) stopUnix() {
	if s.unixSrv != nil {
		s.unixSrv.Stop()
	}
}

// stopMCP stops the MCP server if it was started.
func (s *Server) stopMCP() {
	if s.mcpSrv != nil {
		s.mcpSrv.Stop()
	}
}
