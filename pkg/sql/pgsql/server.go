// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"

	wire "github.com/jeroenrinzema/psql-wire"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// defaultDBName is the default database name used when none is configured.
const defaultDBName = "qrdb"

// Server implements a PostgreSQL wire protocol frontend for QRDB. It uses
// psql-wire for PG wire protocol framing and routes queries through
// go-mysql-server for SQL execution against the QRDB KV service layer.
type Server struct {
	addr     string
	listener net.Listener
	wireSrv  *wire.Server
	kvSvc    service.KVServicer
	adminSvc service.AdminServicer

	tlsConfig *tls.Config
	authMW    *authmw.AuthMiddleware
	dbName    string
	logger    *slog.Logger
	cancel    context.CancelFunc
	dbManager *database.Manager
	done      chan struct{} // closed when Serve goroutine exits
}

// ServerOption configures the Server.
type ServerOption func(*Server)

// WithTLS configures TLS for the PostgreSQL wire protocol server.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tlsConfig = cfg
	}
}

// WithAuthMiddleware configures authentication middleware for the server.
// When set, connections are authenticated via mTLS through the auth
// middleware chain.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) {
		s.authMW = mw
	}
}

// WithDatabaseName overrides the default database name ("qrdb").
func WithDatabaseName(name string) ServerOption {
	return func(s *Server) {
		s.dbName = name
	}
}

// WithDatabaseManager configures the server with a database.Manager for
// multi-database support. When set, the server uses
// provider.NewManagerProvider instead of provider.NewProvider, enabling
// CREATE DATABASE / DROP DATABASE operations.
func WithDatabaseManager(manager *database.Manager) ServerOption {
	return func(s *Server) {
		s.dbManager = manager
	}
}

// WithLogger configures a custom logger for the server.
func WithLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// NewServer creates a new PostgreSQL wire protocol server backed by the
// given KV and admin service interfaces. The server listens on the given
// address. Options can be used to configure TLS, authentication, database
// name, and logging.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		addr:     addr,
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		dbName:   defaultDBName,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start initializes the SQL engine, creates the psql-wire server, opens
// a TCP listener, and begins serving connections in a background goroutine.
func (s *Server) Start() error {
	// Build the SQL engine pipeline. When a database manager is
	// configured, use the manager-backed provider for multi-database
	// support; otherwise fall back to the legacy single-database provider.
	var dbProvider *provider.Provider
	if s.dbManager != nil {
		dbProvider = provider.NewManagerProvider(s.dbManager, s.kvSvc,
			provider.WithProtocol(provider.ProtocolPostgreSQL))
	} else {
		dbProvider = provider.NewProvider(s.dbName, s.kvSvc)
	}
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	parseHandler := NewParseHandler(sqlEngine, translator, s.dbName)

	// Build psql-wire server options.
	wireOpts := []wire.OptionFn{
		wire.Logger(s.logger),
	}

	// Configure the authenticator when auth middleware is set.
	var authenticator *Authenticator
	if s.authMW != nil {
		authenticator = NewAuthenticator(s.authMW)
	}

	// Configure TLS options.
	if s.tlsConfig != nil {
		tlsCfg := s.tlsConfig.Clone()

		// PgSQL mandates mTLS — all connections must present a client
		// certificate. Override the inherited VerifyClientCertIfGiven
		// with RequireAndVerifyClientCert.
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert

		// Set up per-connection VerifyConnection callback via
		// GetConfigForClient so that each TLS handshake stores its
		// ConnectionState in the Authenticator's sync.Map keyed by
		// the remote address. This is the bridge between psql-wire's
		// TLS handshake and our auth middleware chain.
		if authenticator != nil {
			existingVerify := tlsCfg.VerifyConnection
			tlsCfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				perConn := tlsCfg.Clone()
				perConn.GetConfigForClient = nil // prevent infinite recursion
				remoteAddr := hello.Conn.RemoteAddr()
				perConn.VerifyConnection = func(cs tls.ConnectionState) error {
					if existingVerify != nil {
						if err := existingVerify(cs); err != nil {
							return err
						}
					}
					authenticator.StoreTLSState(remoteAddr, &cs)
					return nil
				}
				return perConn, nil
			}
		}

		wireOpts = append(wireOpts,
			wire.TLSConfig(tlsCfg),
			wire.ClientAuth(tls.RequireAndVerifyClientCert),
		)
	}

	// Add session middleware and close handler for auth.
	if authenticator != nil {
		wireOpts = append(wireOpts,
			wire.SessionMiddleware(sessionHandler(authenticator)),
			wire.CloseConn(closeHandler(authenticator)),
		)
	}

	// Create the psql-wire server.
	wireSrv, err := wire.NewServer(parseHandler.HandleParse, wireOpts...)
	if err != nil {
		return &ServerStartError{Err: err}
	}
	s.wireSrv = wireSrv

	// Open the TCP listener.
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &ServerStartError{Err: err}
	}
	s.listener = listener

	// Serve in a background goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	go s.serve(ctx, listener)

	return nil
}

// serve runs the psql-wire server on the given listener. It logs server
// errors unless the context has been canceled (expected shutdown).
func (s *Server) serve(ctx context.Context, listener net.Listener) {
	if s.done != nil {
		defer close(s.done)
	}

	s.logger.Info("pgsql: server listening",
		slog.String("addr", listener.Addr().String()),
		slog.String("database", s.dbName),
	)

	if err := s.wireSrv.Serve(listener); err != nil {
		select {
		case <-ctx.Done():
			// Expected shutdown; suppress error logging.
		default:
			s.logger.Error("pgsql: server error", slog.Any("error", err))
		}
	}
}

// Stop gracefully shuts down the server. It cancels the context, closes
// the listener to unblock Accept, waits for the Serve goroutine to exit,
// and then calls Close on the wire server (avoiding a data race).
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	// Close listener to unblock the Serve accept loop.
	if s.listener != nil {
		_ = s.listener.Close()
	}
	// Wait for the serve goroutine to exit before calling wireSrv.Close,
	// avoiding the upstream psql-wire race between Serve and Close.
	if s.done != nil {
		<-s.done
	}
	if s.wireSrv != nil {
		_ = s.wireSrv.Close()
	}
}

// Addr returns the address the server is listening on. Returns the
// listener's resolved address if available, otherwise the configured
// address.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// sessionHandler returns a psql-wire SessionHandler that authenticates
// connections via the Authenticator using the remote address from context.
func sessionHandler(authenticator *Authenticator) wire.SessionHandler {
	return func(ctx context.Context) (context.Context, error) {
		remoteAddr := wire.RemoteAddress(ctx)
		if remoteAddr == nil {
			return ctx, nil
		}
		return authenticator.Authenticate(ctx, remoteAddr)
	}
}

// closeHandler returns a psql-wire CloseFn that cleans up TLS state
// for the connection identified by its remote address.
func closeHandler(authenticator *Authenticator) wire.CloseFn {
	return func(ctx context.Context) error {
		remoteAddr := wire.RemoteAddress(ctx)
		if remoteAddr != nil {
			authenticator.ClearTLSState(remoteAddr.String())
		}
		return nil
	}
}
