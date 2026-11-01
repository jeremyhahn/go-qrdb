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

//go:build qrdb_mysql

package mysql

import (
	"crypto/tls"
	"log/slog"
	"net"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// defaultDBName is the database name used when none is configured.
const defaultDBName = "qrdb"

// Server wraps go-mysql-server to provide a MySQL wire protocol server
// backed by the QRDB key-value store. It delegates SQL operations to the
// service layer through the provider and engine packages.
// sealedWindowProbe is the abstraction the sealed-window connection
// gate needs to know whether the storage barrier is currently sealed.
// Implemented by *seal.StorageBarrier in production; defined as an
// interface so tests can substitute a deterministic fake. Mirrors the
// unexported probe interfaces on the REST / gRPC / MCP transports.
type sealedWindowProbe interface {
	IsSealed() bool
}

type Server struct {
	addr      string
	listener  net.Listener
	sqlEngine *sqle.Engine
	mysqlSrv  *server.Server
	kvSvc     service.KVServicer
	adminSvc  service.AdminServicer
	tlsConfig *tls.Config
	authMW    *authmw.AuthMiddleware
	dbName    string

	// sharedDefaultDB is the configured default SQL database, which exists
	// for every tenant (WithSharedDefaultDatabase). Empty shares nothing.
	sharedDefaultDB string
	logger          *slog.Logger
	dbManager       *database.Manager
	// databaseSvc is the tenant-scoped database lifecycle behind CREATE
	// DATABASE and DROP DATABASE (see WithDatabaseService).
	databaseSvc service.DatabaseServicer
	// sealedProbe gates new connections while the storage barrier is
	// sealed. When nil the gate is disabled (no seal subsystem).
	sealedProbe sealedWindowProbe
	// sealStreams, when non-nil, is the node's cluster-seal stream registry:
	// a cluster seal or cut-off that reaches a connection's tenant closes the
	// connection (see WithSealStreams).
	sealStreams *clusterseal.Streams
	// sessions guards every open connection against a seal; nil when the
	// server has neither a probe nor a stream registry.
	sessions *sealedSessions
	// queryTimeout bounds every statement (WithQueryTimeout); zero is none.
	queryTimeout time.Duration
	// waitTimeout bounds how long a connection may sit idle with no command
	// from the client -- including one holding an open server-side cursor
	// (COM_STMT_FETCH) the client stopped fetching from -- before the
	// server closes it (WithWaitTimeout). Mapped to vitess's
	// ConnReadTimeout; zero is none.
	waitTimeout time.Duration
	// writeTimeout bounds a single write to a stalled client -- one that
	// stopped reading a result set -- before the server closes the
	// connection (WithWriteTimeout). Mapped to vitess's ConnWriteTimeout;
	// zero is none.
	writeTimeout time.Duration
	// registry resolves per-(tenant, database) services -- KV, MVCC,
	// relational tables, and everything else service.Registry exposes --
	// the same transport-neutral seam every other protocol (REST, gRPC,
	// Unix, QUIC, MCP) enforces barrier/quota/isolation and routes
	// IsolationRaft/IsolationVolume tenants through. Nil disables it: the
	// provider then serves only the fixed engine tables, DAO-registered
	// entities, and (if configured) collections, scoped by the session's
	// tenant against the shared kvSvc exactly as before this option
	// existed.
	registry service.Registry
	// collectionSvc, when set, makes collection tables visible over this
	// listener alongside the fixed engine tables and relational tables.
	collectionSvc service.CollectionServicer
	// indexSvc, when set, lets KV, entity, and collection tables served
	// over this listener use secondary index acceleration for WHERE
	// clause evaluation.
	indexSvc service.IndexServicer
	// clientLimit charges every connection and statement to its client
	// address's bucket (WithClientLimiter); nil limits nothing.
	clientLimit *clientlimit.Limiter
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the server with TLS for encrypted MySQL connections.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tlsConfig = cfg
	}
}

// WithAuthMiddleware attaches authentication and authorization middleware
// for mTLS-based session authentication.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) {
		s.authMW = mw
	}
}

// WithDatabaseName configures the database name exposed by the MySQL
// server. Defaults to "qrdb".
func WithDatabaseName(name string) ServerOption {
	return func(s *Server) {
		s.dbName = name
	}
}

// WithSharedDefaultDatabase marks name -- the configured default SQL
// database -- as existing for every tenant, as the service registry treats it
// (server.WithSharedSQLDefaultDatabase). Each tenant session sees its own
// namespace of it; every other database is visible only to its owner.
func WithSharedDefaultDatabase(name string) ServerOption {
	return func(s *Server) {
		s.sharedDefaultDB = name
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

// WithDatabaseService routes CREATE DATABASE and DROP DATABASE through the
// tenant-scoped database service every API transport uses, so a session
// creates a database its own tenant owns, inside its quota, and drops only
// its own tenant's databases. Without it only system-tenant sessions may run
// either statement.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) {
		s.databaseSvc = svc
	}
}

// WithLogger configures a structured logger for the server. Defaults to
// slog.Default().
func WithLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithServiceRegistry configures the server with a service.Registry for
// per-(tenant, database) service resolution. When set, every connection's
// provider.Database additionally resolves user-defined relational tables
// (CREATE TABLE) through reg.TableService, scoped to the session's
// authenticated tenant (see provider.WithRegistry), and every other
// registry-backed resolution (KV, MVCC, ...) enforces the same barrier/
// quota/isolation guarantees every other transport enforces through this
// same registry -- closing the isolation gap where a MySQL wire-protocol
// session would otherwise bypass it entirely.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) {
		s.registry = reg
	}
}

// WithCollectionService configures the server with a CollectionServicer so
// collection tables are visible over the MySQL wire protocol alongside the
// fixed engine tables and (when WithServiceRegistry is also set) relational
// tables.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) {
		s.collectionSvc = svc
	}
}

// WithIndex configures the server with an IndexServicer so KV, entity, and
// collection tables served over the MySQL wire protocol use secondary
// index acceleration for WHERE clause evaluation.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithSealedWindowProbe installs the sealed-window connection gate.
// While the probe reports sealed, every new MySQL connection is refused
// during the handshake with a "barrier sealed" error -- or, when the probe
// is a sealedwindow.StateProbe reporting the seal state unconfirmed, with
// SealStateUnconfirmedError (1105, retryable) -- consistent with
// the sealed-window envelope on the other protocol transports. Once the
// barrier is unsealed the gate is a passthrough; on re-seal it engages
// again for new connections.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedProbe = probe
	}
}

// WithSealStreams subscribes every MySQL connection to the node's
// cluster-seal stream registry when its session is built: a seal of the node,
// of the connection's tenant or of any of its databases, or a cut-off, closes
// the connection. The probe (WithSealedWindowProbe) closes them too, for a
// node-local seal. Closing is what ends a server-side cursor, which vitess
// serves without consulting the statement gate, and a transaction held
// across the seal.
func WithSealStreams(streams *clusterseal.Streams) ServerOption {
	return func(s *Server) { s.sealStreams = streams }
}

// WithQueryTimeout bounds every statement -- query, multi-statement query,
// prepare and execute, and a server-side cursor while it produces rows -- to d; a
// statement past it is cancelled and answered ER_QUERY_TIMEOUT (3024). Zero
// disables it. It is what bounds a statement whose client has gone away: the
// server runs with go-mysql-server's connection watcher off (see Start), so a
// disconnect no longer cancels an in-flight statement before its timeout.
func WithQueryTimeout(d time.Duration) ServerOption {
	return func(s *Server) { s.queryTimeout = d }
}

// WithWaitTimeout bounds how long a connection may sit idle with no command
// from the client, mapped to vitess's ConnReadTimeout. A connection that
// opened a server-side cursor and stopped issuing COM_STMT_FETCH is idle by
// this definition like any other: the listener's read loop is waiting on the
// client's next command either way, so the same bound closes the connection
// and releases its statement's resources (see Start). Zero disables it.
func WithWaitTimeout(d time.Duration) ServerOption {
	return func(s *Server) { s.waitTimeout = d }
}

// WithWriteTimeout bounds a single write to a stalled client -- one that
// stopped reading a result set -- mapped to vitess's ConnWriteTimeout. Zero
// disables it.
func WithWriteTimeout(d time.Duration) ServerOption {
	return func(s *Server) { s.writeTimeout = d }
}

// NewServer creates a new MySQL wire protocol server that delegates SQL
// operations to the provided KV and admin services.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
		dbName:   defaultDBName,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start initializes the SQL engine, binds to the configured address, and
// begins accepting MySQL protocol connections in the background. Returns
// a ServerStartError if the server fails to start.
func (s *Server) Start() error {
	// Build the QRDB database provider and SQL engine. When a database
	// manager is configured, use the manager-backed provider for
	// multi-database support; otherwise fall back to the legacy
	// single-database provider. Either way, the same set of optional
	// service-wiring options (registry, collections, index acceleration)
	// applies: NewProvider takes no options, so the legacy path applies
	// them directly to the constructed *provider.Provider -- the exported
	// option functions are just closures over unexported fields and work
	// identically regardless of which constructor built the Provider.
	var svcOpts []func(*provider.Provider)
	if s.collectionSvc != nil {
		svcOpts = append(svcOpts, provider.WithCollectionService(s.collectionSvc))
	}
	if s.indexSvc != nil {
		svcOpts = append(svcOpts, provider.WithIndexService(s.indexSvc))
	}
	if s.registry != nil {
		svcOpts = append(svcOpts, provider.WithRegistry(s.registry))
	}
	if s.databaseSvc != nil {
		svcOpts = append(svcOpts, provider.WithDatabaseService(s.databaseSvc))
	}
	if s.authMW != nil {
		svcOpts = append(svcOpts, provider.WithLifecycleAuthorizer(s.authMW))
	}

	var prov *provider.Provider
	if s.dbManager != nil {
		managerOpts := append([]func(*provider.Provider){provider.WithProtocol(provider.ProtocolMySQL)}, svcOpts...)
		if s.sharedDefaultDB != "" {
			managerOpts = append(managerOpts, provider.WithSharedDefaultDatabase(s.sharedDefaultDB))
		}
		prov = provider.NewManagerProvider(s.dbManager, s.kvSvc, managerOpts...)
	} else {
		prov = provider.NewProvider(s.dbName, s.kvSvc)
		for _, opt := range svcOpts {
			opt(prov)
		}
	}
	s.sqlEngine = engine.New(engine.Config{Provider: prov})

	// Build the session builder for connection authentication.
	// The guard closes a connection a seal reaches, or whose tenant the auth
	// chain stops admitting: COM_STMT_FETCH is answered inside vitess, so
	// closing the connection is the only thing that ends an open cursor.
	if s.sealedProbe != nil || s.sealStreams != nil || s.authMW != nil {
		var guardOpts []sessionguard.Option
		if s.authMW != nil {
			guardOpts = append(guardOpts, sessionguard.WithAdmission(sessionguard.TenantAdmission(s.authMW)))
		}
		guard := sessionguard.New(s.sealedProbe, s.sealStreams, sessionguard.DefaultProbeInterval, guardOpts...)
		guard.Start()
		s.sessions = &sealedSessions{guard: guard}
	}
	sb := newSessionBuilder(s.authMW, s.sealedProbe)
	sb.sessions = s.sessions
	sb.clientLimit = s.clientLimit

	// Configure the go-mysql-server server.
	cfg := server.Config{
		Protocol:  "tcp",
		Address:   s.addr,
		TLSConfig: s.tlsConfig,
		// go-mysql-server's connection watcher Peeks the socket while a
		// statement runs, to cancel it if the client goes away. A
		// server-side cursor keeps its statement running while vitess's
		// command loop reads the client's COM_STMT_FETCH from the same
		// socket: an unsynchronized concurrent read, a data race. It is off;
		// the query timeout (WithQueryTimeout) bounds every statement
		// instead, including one whose client has disconnected.
		DisableConnectionWatcher: true,
		// ConnReadTimeout/ConnWriteTimeout bound idle time on the socket
		// itself, independent of DisableConnectionWatcher and of
		// queryTimeout: vitess re-arms a read (write) deadline before every
		// Read (Write) on the connection (netutil.ConnWithTimeouts), so a
		// connection that sends no command for waitTimeout -- including one
		// holding an open server-side cursor the client stopped fetching
		// from -- fails its next read and unwinds through the listener's
		// per-connection defer, which discards the cursor and closes the
		// socket. This does not reintroduce the StatusFlags race the
		// connection watcher caused: that was a background goroutine Peeking
		// the socket concurrently with the command loop's own read; this is
		// the command loop's own read, just bounded.
		ConnReadTimeout:  s.waitTimeout,
		ConnWriteTimeout: s.writeTimeout,
	}
	// The sealed-window gate refuses new sessions in the session builder and
	// every statement of an open one here; the query timeout bounds every
	// statement.
	cfg.Options = append(cfg.Options, handlerOptions(s.clientLimit, s.sealedProbe, s.sessions, s.queryTimeout)...)

	// Bind a TCP listener so we can capture the resolved address
	// (important when port 0 is used in tests).
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &ServerStartError{Err: err}
	}
	s.listener = ln
	cfg.Listener = ln

	// Create the go-mysql-server server.
	mysqlSrv, err := server.NewServer(
		cfg,
		s.sqlEngine,
		sql.NewContext,
		sb.Build,
		nil,
	)
	if err != nil {
		if closeErr := ln.Close(); closeErr != nil {
			s.logger.Error("failed to close listener after server creation failure", "error", closeErr)
		}
		return &ServerStartError{Err: err}
	}
	s.mysqlSrv = mysqlSrv

	// server.NewServer above already pushed ConnReadTimeout/ConnWriteTimeout
	// into net_read_timeout/net_write_timeout (its own updateSystemVariables),
	// but it never touches wait_timeout, which would otherwise stay at
	// go-mysql-server's built-in default (28800) regardless of what this
	// listener actually enforces. SELECT @@wait_timeout is the standard way a
	// client discovers its idle budget, so keep it truthful. The system
	// variable's type rejects anything below one second, same as MySQL's own
	// floor, so a sub-second waitTimeout (as used by tests) is left at the
	// default rather than rejected.
	if s.waitTimeout >= time.Second {
		if err := sql.SystemVariables.AssignValues(map[string]interface{}{
			"wait_timeout": int64(s.waitTimeout / time.Second),
		}); err != nil {
			if closeErr := ln.Close(); closeErr != nil {
				s.logger.Error("failed to close listener after sysvar assignment failure", "error", closeErr)
			}
			return &ServerStartError{Err: err}
		}
	}

	s.logger.Info("MySQL server started",
		"addr", ln.Addr().String(),
		"database", s.dbName,
		"tls", s.tlsConfig != nil,
	)

	// server.Start() blocks on Accept(), so run it in a goroutine.
	go func() {
		if startErr := s.mysqlSrv.Start(); startErr != nil {
			s.logger.Error("MySQL server error", "error", startErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the MySQL server by closing the protocol
// listener.
func (s *Server) Stop() {
	if s.sessions != nil {
		s.sessions.guard.Stop()
	}
	if s.mysqlSrv == nil {
		return
	}
	if err := s.mysqlSrv.Close(); err != nil {
		s.logger.Error("MySQL server close error", "error", err)
	}
	s.logger.Info("MySQL server stopped")
}

// Addr returns the listener address. This is useful for tests that bind
// to port 0 to get an ephemeral port.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}
