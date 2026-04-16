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
// It manages the lifecycle of REST, gRPC, Unix socket, MCP, MySQL,
// PostgreSQL, and etcd servers based on the configuration, starting them
// sequentially with rollback on failure and stopping them gracefully in
// reverse order.
package server

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	unixapi "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// SQLProtocolServer is the interface implemented by MySQL and PostgreSQL
// wire protocol servers. Build-tagged files register start functions that
// create concrete implementations.
type SQLProtocolServer interface {
	Start() error
	Stop()
	Addr() string
}

// EtcdProtocolServer is the interface implemented by the per-database
// etcd wire protocol server. Build-tagged files register start/stop
// functions that create concrete implementations.
type EtcdProtocolServer interface {
	Start() error
	Stop()
	Addr() string
}

// startSQLFn is a function that creates and starts a SQL protocol server.
// Build-tagged files register these via init().
type startSQLFn func(s *Server) error

// stopSQLFn is a function that stops a SQL protocol server.
type stopSQLFn func(s *Server)

// Package-level function variables set by build-tagged init() functions.
// When the build tag is absent, the function remains nil and the protocol
// is silently skipped.
var (
	startMySQLFn startSQLFn
	stopMySQLFn  stopSQLFn
	startPgSQLFn startSQLFn
	stopPgSQLFn  stopSQLFn
)

// startEtcdFn is a function that creates and starts an etcd protocol server
// for a specific database.
type startEtcdFn func(s *Server, dbName string, cfg *database.EtcdConfig) (EtcdProtocolServer, error)

// stopEtcdFn is a function that stops an etcd protocol server.
type stopEtcdFn func(srv EtcdProtocolServer)

var (
	startEtcdServerFn startEtcdFn
	stopEtcdServerFn  stopEtcdFn
)

// Server orchestrates the REST, gRPC, Unix socket, MCP, MySQL,
// PostgreSQL, and etcd API servers for QRDB. It starts and stops servers
// based on the protocol configuration and manages their lifecycles with
// rollback on startup failure.
type Server struct {
	config      *config.Config
	kvSvc       service.KVServicer
	adminSvc    service.AdminServicer
	restSrv     *rest.Server
	grpcSrv     *grpcapi.Server
	quicSrv     *quicapi.Server
	unixSrv     *unixapi.Server
	mcpSrv      *mcpapi.Server
	mysqlSrv    SQLProtocolServer
	pgsqlSrv    SQLProtocolServer
	etcdServers sync.Map // database name (string) -> EtcdProtocolServer
	wg          sync.WaitGroup
	logger      *slog.Logger
	opts        serverOptions
}

// serverOptions holds optional dependencies injected via ServerOption.
type serverOptions struct {
	tlsConfig       *tls.Config
	caCertPool      *x509.CertPool
	gatherer        prometheus.Gatherer
	resolver        rest.LeaderAdminResolver
	host            rest.Host
	kvSvc           service.KVServicer
	adminSvc        service.AdminServicer
	userSvc         user.UserServicer
	roleSvc         role.RoleServicer
	certProv        cert.CertProvider
	authMW          *authmw.AuthMiddleware
	caBootstrapCert []byte
	caBootstrapKey  []byte
	batchSvc        service.BatchServicer
	txSvc           service.TransactionServicer
	mvccSvc         service.MVCCKVServicer
	leaseSvc        service.LeaseServicer
	lockSvc         service.LockServicer
	tsSvc           service.TimeSeriesServicer
	indexSvc        service.IndexServicer
	vectorSvc       service.VectorServicer
	embeddingSvc    service.EmbeddingServicer
	ragSvc          service.RAGServicer
	watchSvc        service.WatchServicer
	databaseSvc     service.DatabaseServicer
	dbManager       *database.Manager
	barrierSvc      service.BarrierServicer
	ceremonySvc     service.CeremonyServicer
	credentialSvc   service.CredentialServicer
	ceremonyCA      ceremony.CeremonyCA
	collectionSvc   service.CollectionServicer
	barrierRegistry *seal.BarrierRegistry
	tenantDEKMgr    dek.TenantDEKManager
	tenantSvc       tenant.TenantServicer
	dbRegistry      *DatabaseServiceRegistry
	webUI           http.Handler
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

// WithUserService configures the user management service for all protocol
// servers that support it.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(o *serverOptions) { o.userSvc = svc }
}

// WithRoleService configures the role management service for all protocol
// servers that support it.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(o *serverOptions) { o.roleSvc = svc }
}

// WithCertProvider configures the certificate provider for all protocol
// servers that support it.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(o *serverOptions) { o.certProv = svc }
}

// WithAuthMiddleware configures authentication and authorization middleware
// for REST and gRPC protocol servers.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(o *serverOptions) { o.authMW = mw }
}

// WithCABootstrap enables the /v1/ca/bootstrap endpoint on the REST
// server for cluster TLS bootstrapping.
func WithCABootstrap(certPEM, keyPEM []byte) ServerOption {
	return func(o *serverOptions) {
		o.caBootstrapCert = certPEM
		o.caBootstrapKey = keyPEM
	}
}

// WithBatch configures the batch service for all protocol servers.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(o *serverOptions) { o.batchSvc = svc }
}

// WithTransaction configures the transaction service for all protocol servers.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(o *serverOptions) { o.txSvc = svc }
}

// WithMVCC configures the MVCC key-value service for all protocol servers.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(o *serverOptions) { o.mvccSvc = svc }
}

// WithLease configures the lease service for all protocol servers.
func WithLease(svc service.LeaseServicer) ServerOption {
	return func(o *serverOptions) { o.leaseSvc = svc }
}

// WithLockService configures the distributed lock service for all protocol
// servers that support it.
func WithLockService(svc service.LockServicer) ServerOption {
	return func(o *serverOptions) { o.lockSvc = svc }
}

// WithTimeSeries configures the time series service for all protocol servers.
func WithTimeSeries(svc service.TimeSeriesServicer) ServerOption {
	return func(o *serverOptions) { o.tsSvc = svc }
}

// WithIndex configures the index service for all protocol servers.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(o *serverOptions) { o.indexSvc = svc }
}

// WithVector configures the vector service for all protocol servers.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(o *serverOptions) { o.vectorSvc = svc }
}

// WithEmbedding configures the embedding service for all protocol servers
// that support text embedding operations.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(o *serverOptions) { o.embeddingSvc = svc }
}

// WithRAG configures the RAG service for all protocol servers that support
// Retrieval-Augmented Generation operations.
func WithRAG(svc service.RAGServicer) ServerOption {
	return func(o *serverOptions) { o.ragSvc = svc }
}

// WithWatch configures the watch service for all protocol servers.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(o *serverOptions) { o.watchSvc = svc }
}

// WithDatabaseService configures the database management service for all
// protocol servers.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(o *serverOptions) { o.databaseSvc = svc }
}

// WithDatabaseManager configures the database manager for SQL wire
// protocol servers (MySQL and PostgreSQL). When set, SQL servers use
// multi-database support via provider.NewManagerProvider.
func WithDatabaseManager(manager *database.Manager) ServerOption {
	return func(o *serverOptions) { o.dbManager = manager }
}

// WithBarrierService configures the barrier service for all protocol servers.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(o *serverOptions) { o.barrierSvc = svc }
}

// WithCeremonyService configures the ceremony service for all protocol servers.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(o *serverOptions) { o.ceremonySvc = svc }
}

// WithCredentialService configures the credential service for all protocol servers.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(o *serverOptions) { o.credentialSvc = svc }
}

// WithCeremonyCA configures the ceremony CA used for ceremony initialization.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(o *serverOptions) { o.ceremonyCA = ca }
}

// WithCollectionService configures the collection service for all protocol servers.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(o *serverOptions) { o.collectionSvc = svc }
}

// WithBarrierRegistry sets the barrier registry for per-tenant encryption
// isolation. When set, the registry is available for runtime tenant barrier
// lifecycle operations (register, unseal, seal, remove).
func WithBarrierRegistry(reg *seal.BarrierRegistry) ServerOption {
	return func(o *serverOptions) {
		o.barrierRegistry = reg
	}
}

// WithTenantDEKManager sets the tenant DEK manager for per-tenant data
// encryption key management. When set, each tenant's data is encrypted
// with an isolated DEK wrapped by the cluster DEK.
func WithTenantDEKManager(mgr dek.TenantDEKManager) ServerOption {
	return func(o *serverOptions) {
		o.tenantDEKMgr = mgr
	}
}

// WithTenantService sets the tenant lifecycle service for multi-tenant
// provisioning. When set, tenants can be created, queried, and deleted
// via the API layer with barrier registry integration.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(o *serverOptions) {
		o.tenantSvc = svc
	}
}

// WithDatabaseServiceRegistry sets the per-database service registry.
// When set, protocol handlers can resolve the correct service set for a
// given database name instead of falling back to the single shared set.
func WithDatabaseServiceRegistry(reg *DatabaseServiceRegistry) ServerOption {
	return func(o *serverOptions) {
		o.dbRegistry = reg
	}
}

// WithKVService configures the KV service for all protocol servers. This is an
// alternative to passing kvSvc as a positional parameter in New and is useful
// when constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(o *serverOptions) { o.kvSvc = svc }
}

// WithAdminService configures the admin service for all protocol servers. This
// is an alternative to passing adminSvc as a positional parameter in New and is
// useful when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(o *serverOptions) { o.adminSvc = svc }
}

// WithWebUI attaches an http.Handler that serves an embedded web UI from the
// REST server. Non-API requests are forwarded to this handler.
func WithWebUI(h http.Handler) ServerOption {
	return func(o *serverOptions) { o.webUI = h }
}

// New creates a new multi-protocol server orchestrator. kvSvc and adminSvc may
// be nil when only non-KV services (barrier, cert, auth) are needed; protocol
// servers propagate their nil-service behaviour to callers as Unimplemented
// responses. The server does not start until Start() is called.
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

	// Option-based kvSvc/adminSvc override the positional parameters, allowing
	// nil to be passed as a positional param when using option functions instead.
	if s.opts.kvSvc != nil {
		s.kvSvc = s.opts.kvSvc
	}
	if s.opts.adminSvc != nil {
		s.adminSvc = s.opts.adminSvc
	}

	return s
}

// Start launches the configured API servers. Servers are started sequentially
// in order: REST, gRPC, QUIC, Unix, MCP, MySQL, PostgreSQL. If any server
// fails to start, all previously started servers are rolled back and the
// error is returned.
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

	if s.config.Protocols.QUIC {
		if err := s.startQUIC(); err != nil {
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.Unix {
		if err := s.startUnix(); err != nil {
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.MCP {
		if err := s.startMCP(); err != nil {
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.MySQL && startMySQLFn != nil {
		if err := startMySQLFn(s); err != nil {
			s.stopMCP()
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.PostgreSQL && startPgSQLFn != nil {
		if err := startPgSQLFn(s); err != nil {
			s.stopMySQL()
			s.stopMCP()
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	return nil
}

// Stop gracefully shuts down all running API servers in reverse startup
// order (etcd, PostgreSQL, MySQL, MCP, Unix, QUIC, gRPC, REST) and waits
// for all background goroutines to complete.
func (s *Server) Stop() {
	s.stopAllEtcd()
	s.stopPgSQL()
	s.stopMySQL()
	s.stopMCP()
	s.stopUnix()
	s.stopQUIC()
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

// QUICAddr returns the native QUIC server's listener address, or empty
// string if QUIC is not running.
func (s *Server) QUICAddr() string {
	if s.quicSrv != nil {
		return s.quicSrv.Addr()
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
	if s.opts.userSvc != nil {
		restOpts = append(restOpts, rest.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		restOpts = append(restOpts, rest.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		restOpts = append(restOpts, rest.WithCertProvider(s.opts.certProv))
	}
	if s.opts.authMW != nil {
		restOpts = append(restOpts, rest.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.batchSvc != nil {
		restOpts = append(restOpts, rest.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		restOpts = append(restOpts, rest.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		restOpts = append(restOpts, rest.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		restOpts = append(restOpts, rest.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		restOpts = append(restOpts, rest.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		restOpts = append(restOpts, rest.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		restOpts = append(restOpts, rest.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		restOpts = append(restOpts, rest.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		restOpts = append(restOpts, rest.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		restOpts = append(restOpts, rest.WithRAG(s.opts.ragSvc))
	}
	if s.opts.watchSvc != nil {
		restOpts = append(restOpts, rest.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		restOpts = append(restOpts, rest.WithDatabaseService(s.opts.databaseSvc))
	}
	if s.opts.barrierSvc != nil {
		restOpts = append(restOpts, rest.WithBarrierService(s.opts.barrierSvc))
	}
	if s.opts.ceremonySvc != nil {
		restOpts = append(restOpts, rest.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		restOpts = append(restOpts, rest.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.ceremonyCA != nil {
		restOpts = append(restOpts, rest.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.collectionSvc != nil {
		restOpts = append(restOpts, rest.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.tenantSvc != nil {
		restOpts = append(restOpts, rest.WithTenantService(s.opts.tenantSvc))
	}
	if len(s.opts.caBootstrapCert) > 0 {
		restOpts = append(restOpts, rest.WithCABootstrap(s.opts.caBootstrapCert, s.opts.caBootstrapKey))
	}
	restOpts = append(restOpts, rest.WithOnEtcdUpdated(func(dbName string, cfg *database.EtcdConfig) error {
		return s.StartEtcd(dbName, cfg)
	}))
	if s.opts.webUI != nil {
		restOpts = append(restOpts, rest.WithWebUI(s.opts.webUI))
	}
	if s.opts.dbRegistry != nil {
		regOpts := []RESTRegistryOption{}
		if s.opts.tenantSvc != nil {
			regOpts = append(regOpts, WithTenantServicer(s.opts.tenantSvc))
		}
		restOpts = append(restOpts, rest.WithServiceRegistry(
			NewRESTServiceRegistry(s.opts.dbRegistry, s.opts.barrierRegistry, "default", regOpts...),
		))
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
	if s.opts.userSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.authMW != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.barrierSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithBarrierService(s.opts.barrierSvc))
	}
	if s.opts.ceremonySvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.collectionSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.mvccSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.txSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.batchSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.watchSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.leaseSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.indexSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.tsSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.vectorSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.databaseSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithDatabaseService(s.opts.databaseSvc))
	}

	s.grpcSrv = grpcapi.NewServer(s.kvSvc, s.adminSvc, addr, grpcOpts...)

	if err := s.grpcSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("gRPC server started", "addr", s.grpcSrv.Addr())
	return nil
}

// startQUIC creates and starts the native QUIC transport server.
func (s *Server) startQUIC() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.QUICPort))

	var quicOpts []quicapi.ServerOption

	if s.opts.tlsConfig != nil {
		quicOpts = append(quicOpts, quicapi.WithTLS(s.opts.tlsConfig))
	}
	if s.opts.userSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		quicOpts = append(quicOpts, quicapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.authMW != nil {
		quicOpts = append(quicOpts, quicapi.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.collectionSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.batchSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.watchSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithDatabaseService(s.opts.databaseSvc))
	}
	if s.opts.barrierSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithBarrierService(s.opts.barrierSvc))
	}
	if s.opts.ceremonySvc != nil {
		quicOpts = append(quicOpts, quicapi.WithCeremonyService(s.opts.ceremonySvc))
	}

	s.quicSrv = quicapi.NewServer(s.kvSvc, s.adminSvc, addr, quicOpts...)

	if err := s.quicSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("QUIC server started", "addr", s.quicSrv.Addr())
	return nil
}

// startUnix creates and starts the Unix domain socket server.
func (s *Server) startUnix() error {
	var unixOpts []unixapi.ServerOption
	if s.opts.userSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		unixOpts = append(unixOpts, unixapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.barrierSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithBarrierService(s.opts.barrierSvc))
	}
	if s.opts.ceremonySvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.collectionSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.mvccSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.txSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.batchSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.watchSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.leaseSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.indexSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.tsSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.vectorSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.databaseSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithDatabaseService(s.opts.databaseSvc))
	}

	s.unixSrv = unixapi.NewServer(s.kvSvc, s.adminSvc, s.config.Unix.SocketPath, unixOpts...)

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
	if s.opts.userSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.barrierSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithBarrierService(s.opts.barrierSvc))
	}
	if s.opts.ceremonySvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.ceremonyCA != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.collectionSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.batchSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithRAG(s.opts.ragSvc))
	}
	if s.opts.watchSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithDatabaseService(s.opts.databaseSvc))
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

// stopQUIC stops the native QUIC server if it was started.
func (s *Server) stopQUIC() {
	if s.quicSrv != nil {
		s.quicSrv.Stop()
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

// MySQLAddr returns the MySQL server's listener address, or empty string
// if MySQL is not running.
func (s *Server) MySQLAddr() string {
	if s.mysqlSrv != nil {
		return s.mysqlSrv.Addr()
	}
	return ""
}

// PgSQLAddr returns the PostgreSQL server's listener address, or empty
// string if PostgreSQL is not running.
func (s *Server) PgSQLAddr() string {
	if s.pgsqlSrv != nil {
		return s.pgsqlSrv.Addr()
	}
	return ""
}

// StartEtcd starts an etcd protocol server for the named database. If
// etcd support is not compiled in, it returns nil silently.
func (s *Server) StartEtcd(dbName string, cfg *database.EtcdConfig) error {
	if startEtcdServerFn == nil {
		return nil
	}
	_, err := startEtcdServerFn(s, dbName, cfg)
	return err
}

// StopEtcd stops the etcd protocol server for the named database if one
// is running.
func (s *Server) StopEtcd(dbName string) {
	if stopEtcdServerFn == nil {
		return
	}
	val, ok := s.etcdServers.LoadAndDelete(dbName)
	if ok {
		stopEtcdServerFn(val.(EtcdProtocolServer))
	}
}

// EtcdAddr returns the etcd server's listener address for the given
// database, or empty string if etcd is not running for that database.
func (s *Server) EtcdAddr(dbName string) string {
	val, ok := s.etcdServers.Load(dbName)
	if !ok {
		return ""
	}
	return val.(EtcdProtocolServer).Addr()
}

// stopMySQL stops the MySQL server if it was started.
func (s *Server) stopMySQL() {
	if stopMySQLFn != nil {
		stopMySQLFn(s)
	}
}

// stopPgSQL stops the PostgreSQL server if it was started.
func (s *Server) stopPgSQL() {
	if stopPgSQLFn != nil {
		stopPgSQLFn(s)
	}
}

// stopAllEtcd stops all running etcd protocol servers.
func (s *Server) stopAllEtcd() {
	if stopEtcdServerFn == nil {
		return
	}
	s.etcdServers.Range(func(key, value any) bool {
		stopEtcdServerFn(value.(EtcdProtocolServer))
		s.etcdServers.Delete(key)
		return true
	})
}
