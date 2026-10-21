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
	"errors"
	"log/slog"
	"net"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/infoschema"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// defaultDBName is the default database name used when none is configured.
const defaultDBName = "qrdb"

// sealedWindowProbe is the abstraction the sealed-window gates need to
// know whether the storage barrier is currently sealed. Implemented by
// *seal.StorageBarrier in production; defined as an interface so tests
// can substitute a deterministic fake. Mirrors the unexported probe
// interfaces on the REST / gRPC / MCP transports.
type sealedWindowProbe interface {
	IsSealed() bool
}

// Server implements a PostgreSQL wire protocol frontend for QRDB. It uses
// psql-wire for PG wire protocol framing and routes queries through
// go-mysql-server for SQL execution against the QRDB KV service layer.
type Server struct {
	addr      string
	listener  net.Listener
	wireSrv   *wire.Server
	sqlEngine *sqle.Engine
	kvSvc     service.KVServicer
	adminSvc  service.AdminServicer

	tlsConfig *tls.Config
	authMW    *authmw.AuthMiddleware
	dbName    string

	// sharedDefaultDB is the configured default SQL database, which exists
	// for every tenant (WithSharedDefaultDatabase). Empty shares nothing.
	sharedDefaultDB string
	logger          *slog.Logger
	cancel          context.CancelFunc
	dbManager       *database.Manager
	done            chan struct{} // closed when Serve goroutine exits
	// databaseSvc is the tenant-scoped database lifecycle behind CREATE
	// DATABASE and DROP DATABASE (see WithDatabaseService).
	databaseSvc service.DatabaseServicer
	// sealedProbe gates connections and queries while the storage
	// barrier is sealed. When nil the gate is disabled.
	sealedProbe sealedWindowProbe
	// sealStreams, when non-nil, is the node's cluster-seal stream registry
	// every session subscribes to (WithSealStreams).
	sealStreams *clusterseal.Streams
	// guard closes a session's connection when a seal reaches it; nil when
	// the server has neither a probe nor a stream registry.
	guard *sessionguard.Guard
	// queryTimeout bounds every statement (WithQueryTimeout); zero is none.
	queryTimeout time.Duration
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
}

// ServerOption configures the Server.
type ServerOption func(*Server)

// WithSealStreams subscribes every PostgreSQL session to the node's
// cluster-seal stream registry: a seal of the node, of the session's tenant or
// of any of its databases, or a cut-off, closes the connection -- ending any
// transaction held across it, as the MySQL listener does. The probe
// (WithSealedWindowProbe) closes sessions too, for a node-local seal.
func WithSealStreams(streams *clusterseal.Streams) ServerOption {
	return func(s *Server) { s.sealStreams = streams }
}

// WithQueryTimeout bounds every statement -- a simple query, and each
// extended-protocol statement's execution, whether at Parse (no parameters)
// or at each Bind/Execute of its portal -- to d; a statement past it is
// cancelled and answered SQLSTATE 57014 (query_canceled), as PostgreSQL's
// statement_timeout is. Zero disables it.
func WithQueryTimeout(d time.Duration) ServerOption {
	return func(s *Server) { s.queryTimeout = d }
}

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

// WithLogger configures a custom logger for the server.
func WithLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithServiceRegistry configures the server with a service.Registry for
// per-(tenant, database) service resolution. When set, every session's
// provider.Database additionally resolves user-defined relational tables
// (CREATE TABLE) through reg.TableService, scoped to the session's
// authenticated tenant (see provider.WithRegistry), and every other
// registry-backed resolution (KV, MVCC, ...) enforces the same barrier/
// quota/isolation guarantees every other transport enforces through this
// same registry -- closing the isolation gap where a PostgreSQL
// wire-protocol session would otherwise bypass it entirely.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) {
		s.registry = reg
	}
}

// WithCollectionService configures the server with a CollectionServicer so
// collection tables are visible over the PostgreSQL wire protocol alongside
// the fixed engine tables and (when WithServiceRegistry is also set)
// relational tables.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) {
		s.collectionSvc = svc
	}
}

// WithIndex configures the server with an IndexServicer so KV, entity, and
// collection tables served over the PostgreSQL wire protocol use secondary
// index acceleration for WHERE clause evaluation.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithSealedWindowProbe installs the sealed-window gate. While the
// probe reports sealed, new PostgreSQL sessions are refused at startup
// and in-flight sessions have every query rejected, both with SQLSTATE
// 57P03 (cannot_connect_now) and message "barrier sealed" — consistent
// with the sealed-window envelope on the other protocol transports. A
// sealedwindow.StateProbe reporting the seal state unconfirmed is refused
// under the same SQLSTATE with SealStateUnconfirmedError instead: retryable,
// and never reported as sealed.
// Once the barrier is unsealed the gate is a passthrough; on re-seal it
// engages again.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedProbe = probe
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

// buildPgTLSConfig returns the *tls.Config psql-wire serves connections
// with: a clone of base with ClientAuth raised to RequireAndVerifyClientCert
// (PgSQL mandates mTLS -- all connections must present a client
// certificate, overriding whatever VerifyClientCertIfGiven the caller
// inherited), composed with whatever dynamic hook base already carries
// rather than overwriting base.GetConfigForClient outright.
//
// A shared *clienttrust.Pool installs that dynamic hook (via Pool.Wire)
// across every client-facing listener, to widen ClientCAs with every
// tenant's Key B CA certificate on each handshake. Overwriting it the way
// an earlier version of this function did pinned every PG connection to
// whatever ClientCAs base held at Wire time, silently losing Key B over
// this transport (go-qrdb#560 review). Calling the existing hook first and
// reasserting RequireAndVerifyClientCert on its result -- plus, when
// authenticator is non-nil, wrapping VerifyConnection so it also stores
// each handshake's ConnectionState in the Authenticator's sync.Map keyed
// by remote address, the bridge between psql-wire's TLS handshake and our
// auth middleware chain -- is what lets both the static and the dynamic
// (widened) path reach PG the same way every other client-facing
// transport does.
func buildPgTLSConfig(base *tls.Config, authenticator *Authenticator) *tls.Config {
	tlsCfg := base.Clone()
	tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert

	existingDynamic := tlsCfg.GetConfigForClient
	existingVerify := tlsCfg.VerifyConnection
	if existingDynamic == nil && authenticator == nil {
		return tlsCfg
	}
	tlsCfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		dynBase := tlsCfg
		verify := existingVerify
		if existingDynamic != nil {
			dynCfg, err := existingDynamic(hello)
			if err != nil {
				return nil, err
			}
			if dynCfg != nil {
				dynBase = dynCfg
				verify = dynCfg.VerifyConnection
			}
		}
		perConn := dynBase.Clone()
		perConn.GetConfigForClient = nil // prevent infinite recursion
		perConn.ClientAuth = tls.RequireAndVerifyClientCert
		if authenticator != nil {
			remoteAddr := hello.Conn.RemoteAddr()
			perConn.VerifyConnection = func(cs tls.ConnectionState) error {
				if verify != nil {
					if err := verify(cs); err != nil {
						return err
					}
				}
				authenticator.StoreTLSState(remoteAddr, &cs)
				return nil
			}
		} else {
			perConn.VerifyConnection = verify
		}
		return perConn, nil
	}
	return tlsCfg
}

// Start initializes the SQL engine, creates the psql-wire server, opens
// a TCP listener, and begins serving connections in a background goroutine.
func (s *Server) Start() error {
	// Build the SQL engine pipeline. When a database manager is
	// configured, use the manager-backed provider for multi-database
	// support; otherwise fall back to the legacy single-database provider.
	// Either way, the same set of optional service-wiring options
	// (registry, collections, index acceleration) applies: NewProvider
	// takes no options, so the legacy path applies them directly to the
	// constructed *provider.Provider -- the exported option functions are
	// just closures over unexported fields and work identically regardless
	// of which constructor built the Provider.
	//
	// WithProtocol(ProtocolPostgreSQL) is in that set, so it applies on
	// EVERY construction path, not only the database-manager one: it is what
	// makes this a PostgreSQL-dialect provider at all, gating pg_catalog
	// resolution (Provider.servesPGCatalog) and, through Database.postgres
	// and WithPostgreSQLDialect, TRUNCATE's keeping of a table's sequence. A
	// composer that supplies WithServiceRegistry without WithDatabaseManager
	// must not be served MySQL semantics on a PostgreSQL listener.
	svcOpts := []func(*provider.Provider){provider.WithProtocol(provider.ProtocolPostgreSQL)}
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

	var dbProvider *provider.Provider
	if s.dbManager != nil {
		managerOpts := svcOpts
		if s.sharedDefaultDB != "" {
			managerOpts = append(managerOpts, provider.WithSharedDefaultDatabase(s.sharedDefaultDB))
		}
		dbProvider = provider.NewManagerProvider(s.dbManager, s.kvSvc, managerOpts...)
	} else {
		dbProvider = provider.NewProvider(s.dbName, s.kvSvc)
		for _, opt := range svcOpts {
			opt(dbProvider)
		}
	}
	// Add the information_schema views go-mysql-server does not serve, before
	// the engine below builds the catalog that copies them. See
	// pkg/sql/infoschema for why this cannot go through the DatabaseProvider
	// the way pg_catalog does.
	infoschema.Register()

	sqlEngine := engine.New(engine.Config{
		Provider: dbProvider,
		Dialect:  engine.DialectPostgreSQL,
	})
	s.sqlEngine = sqlEngine
	translator := engine.NewTranslator()
	parseHandler := NewParseHandler(sqlEngine, translator, s.dbName)
	parseHandler.sealedProbe = s.sealedProbe
	parseHandler.queryTimeout = s.queryTimeout

	// Build psql-wire server options. The sealed-window session gate is
	// registered FIRST so it runs before the auth session middleware:
	// while the barrier is sealed, connections are refused before any
	// authentication work happens (fail-closed).
	wireOpts := []wire.OptionFn{
		wire.Logger(s.logger),
		wire.GlobalParameters(serverParameters()),
	}
	if s.sealedProbe != nil {
		wireOpts = append(wireOpts, wire.SessionMiddleware(sealedSessionHandler(s.sealedProbe)))
	}

	// Configure the authenticator when auth middleware is set.
	var authenticator *Authenticator
	if s.authMW != nil {
		authenticator = NewAuthenticator(s.authMW)
	}

	// Configure TLS options.
	if s.tlsConfig != nil {
		tlsCfg := buildPgTLSConfig(s.tlsConfig, authenticator)
		wireOpts = append(wireOpts,
			wire.TLSConfig(tlsCfg),
			wire.ClientAuth(tls.RequireAndVerifyClientCert),
		)
	}

	// Add session middleware and close handler for auth.
	if authenticator != nil {
		wireOpts = append(wireOpts,
			wire.SessionMiddleware(sessionHandler(authenticator)),
		)
	}

	// psql-wire runs ONE close handler, after the session middleware, with
	// the connection's context; it ends the connection's SQL session --
	// releasing any row locks an abandoned transaction still holds -- and
	// clears authentication state.
	wireOpts = append(wireOpts, wire.CloseConn(closeHandler(authenticator)))

	// The seal guard registers each session's connection after
	// authentication, so it knows the session's tenant, and closes it when a
	// seal reaches it or the auth chain stops admitting its tenant: a
	// suspended portal is resumed inside psql-wire, so closing the connection
	// is what ends one its tenant can no longer read. The listener tracks
	// every accepted connection so the guard can close one psql-wire hands it
	// only as a context.
	var tracking *trackingListener
	if s.sealedProbe != nil || s.sealStreams != nil || s.authMW != nil {
		var guardOpts []sessionguard.Option
		if s.authMW != nil {
			guardOpts = append(guardOpts, sessionguard.WithAdmission(sessionguard.TenantAdmission(s.authMW)))
		}
		s.guard = sessionguard.New(s.sealedProbe, s.sealStreams, sessionguard.DefaultProbeInterval, guardOpts...)
		tracking = &trackingListener{}
		wireOpts = append(wireOpts, wire.SessionMiddleware(guardSessionHandler(s.guard, tracking)))
	}

	// Register the per-connection QRDBSession handler LAST (after any auth
	// middleware above), so it sees whatever identity authentication
	// established. This is what lets BEGIN/COMMIT/ROLLBACK/SAVEPOINT state
	// persist across the statements of one connection -- see
	// newConnectionSessionHandler's doc comment. Authentication runs once, for
	// the connection; the session re-admits the identity before each
	// statement, so the connection is refused once its tenant is suspended.
	var sessionOpts []provider.SessionOption
	if s.authMW != nil {
		sessionOpts = append(sessionOpts, provider.WithSessionAdmitter(s.authMW))
	}
	wireOpts = append(wireOpts, wire.SessionMiddleware(newConnectionSessionHandler(sessionOpts...)))

	// A later Execute of a suspended portal resumes it inside psql-wire,
	// past every admission above; the portal cache re-admits the session
	// before each resume, closing the window before the guard's next tick.
	wireOpts = append(wireOpts, wire.Portals(newAdmittingPortals))

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
	if tracking != nil {
		tracking.Listener = listener
		listener = tracking
		s.guard.Start()
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
	if s.guard != nil {
		s.guard.Stop()
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

// sealedSessionHandler returns a psql-wire SessionHandler that refuses
// new sessions with SQLSTATE 57P03 (cannot_connect_now) while the
// storage barrier is sealed or the node cannot confirm the cluster seal state.
func sealedSessionHandler(probe sealedWindowProbe) wire.SessionHandler {
	return func(ctx context.Context) (context.Context, error) {
		return ctx, probeWireError(probe, psqlerr.LevelFatal)
	}
}

// standardConformingStrings is the ParameterStatus every PostgreSQL server
// reports at startup to state that a backslash inside a '...' literal is an
// ordinary character rather than an escape introducer. PostgreSQL has
// defaulted it on since 9.1.
//
// Advertising it is required, not decorative. pgx refuses outright to run a
// simple-protocol query against a server that has not reported it -- "simple
// protocol queries must be run with standard_conforming_strings=on" -- and
// gorm.io/driver/postgres forces simple protocol for Migrator.GetRows, which
// backs ColumnTypes and therefore every schema-introspecting migration a GORM
// application performs.
//
// It is also only true because pkg/sql/engine rewrites backslashes inside
// plain string literals before go-mysql-server's MySQL lexer sees them (see
// escapeBackslashesInStringLiterals). Reporting it without that rewrite would
// be a false promise a client acts on: having seen it, pgx escapes an
// interpolated value by doubling quotes alone, so a value ending in a
// backslash would escape its own closing quote and let the remainder of the
// statement be parsed as SQL.
const standardConformingStrings wire.ParameterStatus = "standard_conforming_strings"

// serverParameters returns the ParameterStatus set this server reports beyond
// the ones psql-wire emits for itself (server_encoding, client_encoding,
// server_version, is_superuser, session_authorization).
func serverParameters() wire.Parameters {
	return wire.Parameters{
		standardConformingStrings: "on",
	}
}

// probeWireError is the PG wire refusal for a closed probe at the supplied
// severity, or nil while it serves: see wireRefusal.
func probeWireError(probe sealedWindowProbe, severity psqlerr.Severity) error {
	return wireRefusal(sessionguard.Refusal(probe), severity)
}

// wireRefusal translates a sessionguard refusal into the PG wire error the
// client reads: SQLSTATE 57P03 (cannot_connect_now) at the supplied severity,
// wrapping SealStateUnconfirmedError for a node that cannot confirm the
// cluster seal state and BarrierSealedError for a sealed one. The two are
// kept apart because reporting an unconfirmed node as sealed sends an
// operator to unseal an open barrier. Any other error is returned as it is.
func wireRefusal(err error, severity psqlerr.Severity) error {
	var typed error = &BarrierSealedError{}
	if _, unconfirmed := errors.AsType[*clusterseal.SealStateUnconfirmedError](err); unconfirmed {
		typed = &SealStateUnconfirmedError{}
	} else if _, sealed := errors.AsType[*sessionguard.SealedError](err); !sealed {
		return err
	}
	return psqlerr.WithSeverity(psqlerr.WithCode(typed, codes.CannotConnectNow), severity)
}

// closeHandler returns a psql-wire CloseFn that ends the connection's SQL
// session -- so an open transaction's row locks are released now rather
// than left to expire with their lease -- and, when authentication is on,
// cleans up the TLS state of the connection identified by its remote
// address.
func closeHandler(authenticator *Authenticator) wire.CloseFn {
	return func(ctx context.Context) error {
		releaseGuardSession(ctx)
		if session := connectionSessionFromContext(ctx); session != nil {
			session.SessionEnd()
		}
		if authenticator == nil {
			return nil
		}
		remoteAddr := wire.RemoteAddress(ctx)
		if remoteAddr != nil {
			authenticator.ClearTLSState(remoteAddr.String())
		}
		return nil
	}
}
