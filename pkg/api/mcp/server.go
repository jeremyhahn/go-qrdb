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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Server is the MCP server for QRDB. It registers KV, admin, MVCC,
// transaction, batch, watch, lease, index, time-series, vector, user,
// role, and certificate operations as MCP tools and serves them over
// HTTP using the Streamable HTTP transport.
type Server struct {
	kvSvc             service.KVServicer
	adminSvc          service.AdminServicer
	mvccSvc           service.MVCCKVServicer
	txSvc             service.TransactionServicer
	batchSvc          service.BatchServicer
	watchSvc          service.WatchServicer
	leaseSvc          service.LeaseServicer
	lockSvc           service.LockServicer
	indexSvc          service.IndexServicer
	tsSvc             service.TimeSeriesServicer
	vectorSvc         service.VectorServicer
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	certProv          cert.CertProvider
	collectionSvc     service.CollectionServicer
	dbSvc             service.DatabaseServicer
	barrierSvc        service.BarrierServicer
	encMigrator       service.EncryptionMigrator
	sqlSvc            service.SQLServicer
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	credentialSvc     service.CredentialServicer
	tenantSvc         tenant.TenantServicer
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	ceremonyCA        ceremony.CeremonyCA
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	auditLogger       audit.AuditLogger
	// registry, when set, resolves data-plane services per (tenant, database)
	// scope. The tenant comes from the authenticated identity; the database
	// from the optional "database" tool argument. When nil, the singleton
	// service fields are used, preserving single-database and embedded
	// deployments.
	registry  service.Registry
	addr      string
	tlsConfig *tls.Config
	tlsErr    error
	authMW    *authmw.AuthMiddleware
	mcpSrv    *mcpserver.MCPServer
	httpSrv   *http.Server
	listener  net.Listener

	// sealedWindowProbe controls the sealed-window tool-handler middleware.
	// While the probe reports sealed, every tool call not on
	// SealedWindowAllowlist is rejected. Wired identically to the REST,
	// gRPC, and native-QUIC sealed-window gates so operators see the same
	// envelope on every transport.
	sealedWindowProbe sealedWindowProbe
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the server with TLS. The config is cloned and pinned to
// a TLS 1.3 floor: an unset MinVersion is raised to TLS 1.3, and an explicitly
// weaker MinVersion is rejected (surfaced by Start as *WeakTLSVersionError)
// rather than silently negotiating a weaker handshake. This mirrors the gRPC
// and unix transports so the "TLS 1.3 only" posture holds on every transport.
// A nil cfg leaves TLS unconfigured; because the transport is TLS-only, Start
// then fails closed with *TLSNotConfiguredError.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		if cfg == nil {
			return
		}
		clone := cfg.Clone()
		switch {
		case clone.MinVersion == 0:
			clone.MinVersion = tls.VersionTLS13
		case clone.MinVersion < tls.VersionTLS13:
			s.tlsErr = &WeakTLSVersionError{MinVersion: clone.MinVersion}
			return
		}
		s.tlsConfig = clone
	}
}

// WithAuthMiddleware configures authentication and per-tool authorization for
// the MCP server. When set, every tool call is authenticated (mTLS peer
// identity, injected by the HTTP context func) and authorized against the
// tool's required permission (mcpPermissions, DEFAULT-DENY). When nil, the
// server runs unauthenticated — matching the other transports' behavior when
// no middleware is supplied.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) {
		s.authMW = mw
	}
}

// WithMVCC registers MVCC key-value tools on the MCP server.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) {
		s.mvccSvc = svc
	}
}

// WithTransaction registers transaction tools on the MCP server.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) {
		s.txSvc = svc
	}
}

// WithBatch registers batch operation tools on the MCP server.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) {
		s.batchSvc = svc
	}
}

// WithWatch registers watch tools on the MCP server.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) {
		s.watchSvc = svc
	}
}

// WithLease registers lease management tools on the MCP server.
func WithLease(svc service.LeaseServicer) ServerOption {
	return func(s *Server) {
		s.leaseSvc = svc
	}
}

// WithLock registers distributed lock tools on the MCP server.
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) {
		s.lockSvc = svc
	}
}

// WithIndex registers secondary index tools on the MCP server.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithTimeSeries registers time-series tools on the MCP server.
func WithTimeSeries(svc service.TimeSeriesServicer) ServerOption {
	return func(s *Server) {
		s.tsSvc = svc
	}
}

// WithVector registers vector database tools on the MCP server.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) {
		s.vectorSvc = svc
	}
}

// WithEmbedding registers embedding tools on the MCP server.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) {
		s.embeddingSvc = svc
	}
}

// WithRAG registers RAG (Retrieval-Augmented Generation) tools on the MCP
// server. When nil, no RAG tools are registered.
func WithRAG(svc service.RAGServicer) ServerOption {
	return func(s *Server) {
		s.ragSvc = svc
	}
}

// WithUserService registers user management tools on the MCP server.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) {
		s.userSvc = svc
	}
}

// WithRoleService registers role management tools on the MCP server.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) {
		s.roleSvc = svc
	}
}

// WithCertProvider registers certificate management tools on the MCP server.
func WithCertProvider(prov cert.CertProvider) ServerOption {
	return func(s *Server) {
		s.certProv = prov
	}
}

// WithCollectionService registers collection management tools on the MCP server.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) {
		s.collectionSvc = svc
	}
}

// WithDatabaseService registers database management tools on the MCP server.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) {
		s.dbSvc = svc
	}
}

// WithBarrierService registers barrier lifecycle tools on the MCP server.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) {
		s.barrierSvc = svc
	}
}

// WithEncryptionMigrator registers the qrdb_barrier_enable_encryption tool
// on the MCP server, exposing BarrierEnableEncryption the same way it is
// exposed over REST, gRPC, QUIC, and Unix.
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(s *Server) {
		s.encMigrator = m
	}
}

// WithSQLService registers the qrdb_sql_query tool on the MCP server for
// ad-hoc SQL execution against a named database.
func WithSQLService(svc service.SQLServicer) ServerOption {
	return func(s *Server) {
		s.sqlSvc = svc
	}
}

// WithPINBackend attaches the canonical pin.PINBackend used for the
// optional SO PIN / User PIN compliance-gating layer on barrier
// administrative operations.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	return func(s *Server) {
		s.pinBackend = backend
	}
}

// WithCeremonyService registers ceremony lifecycle tools on the MCP server.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) {
		s.ceremonySvc = svc
	}
}

// WithCredentialService registers credential management tools on the MCP server.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) {
		s.credentialSvc = svc
	}
}

// WithCeremonyCA configures the ceremony CA used for ceremony initialization.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(s *Server) {
		s.ceremonyCA = ca
	}
}

// WithTenantService registers tenant management tools on the MCP server.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithBackupService registers backup management tools on the MCP server.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService registers cluster rebalance tools on the MCP server.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService registers auth factor tools on the MCP server.
// When set, the five qrdb_authfactor_* tools are available.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService registers user-side auth factor tools on the
// MCP server (M14). When set, the qrdb_user_authfactor_* tools are
// available.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithAuditLogger registers audit query tools on the MCP server.
// When set, the qrdb_audit_query tool is available.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithSealedWindowProbe installs the sealed-window tool-handler middleware
// on the MCP server. While probe.IsSealed() returns true, every tool call
// not on SealedWindowAllowlist is rejected with a tool-result error, giving
// operators the same sealed-window envelope on MCP as on REST, gRPC, and
// native QUIC.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) { s.sealedWindowProbe = probe }
}

// WithServiceRegistry configures the MCP server with a per-(tenant, database)
// service registry. When set, data-plane tool handlers resolve their servicer
// from the registry using the authenticated tenant and the optional "database"
// tool argument; when nil they use the singleton service fields. This gives the
// MCP transport the same per-database keyspace isolation as the REST, gRPC,
// QUIC, and Unix transports.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) { s.registry = reg }
}

// WithKVService registers KV tools on the MCP server. This is an alternative
// to passing kvSvc as a positional parameter and is useful when constructing
// a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService registers admin tools on the MCP server. This is an
// alternative to passing adminSvc as a positional parameter and is useful
// when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new MCP server that listens on the given address
// and delegates to the provided service implementations. kvSvc and adminSvc
// may be nil when only non-KV services (barrier, cert, auth) are needed;
// their tools are registered only when the respective service is non-nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	// Create the underlying MCP server with tool capabilities. When auth is
	// configured, install the per-tool authorization middleware so EVERY tool
	// call is authorized (default-deny) before its handler runs.
	mcpOpts := []mcpserver.ServerOption{
		mcpserver.WithToolCapabilities(false),
	}
	if s.authMW != nil {
		mcpOpts = append(mcpOpts, mcpserver.WithToolHandlerMiddleware(s.toolAuthzMiddleware()))
	}
	if s.sealedWindowProbe != nil {
		mcpOpts = append(mcpOpts, mcpserver.WithToolHandlerMiddleware(s.sealedWindowMiddleware()))
	}
	if s.registry != nil {
		// Bind the registry and the request's database scope into the per-call
		// context so data-plane tool handlers resolve per (tenant, database).
		mcpOpts = append(mcpOpts, mcpserver.WithToolHandlerMiddleware(s.databaseScopeMiddleware()))
	}
	s.mcpSrv = mcpserver.NewMCPServer(
		"qrdb",
		"1.0.0",
		mcpOpts...,
	)

	// Register KV and admin tools only when the respective service is provided.
	if s.kvSvc != nil {
		s.mcpSrv.AddTools(kvTools(s.kvSvc)...)
	}
	if s.adminSvc != nil {
		s.mcpSrv.AddTools(adminTools(s.adminSvc)...)
	}

	// Register optional feature tools when their services are provided.
	if s.mvccSvc != nil {
		s.mcpSrv.AddTools(mvccTools(s.mvccSvc)...)
	}
	if s.txSvc != nil {
		s.mcpSrv.AddTools(transactionTools(s.txSvc)...)
	}
	if s.batchSvc != nil {
		s.mcpSrv.AddTools(batchTools(s.batchSvc)...)
	}
	if s.watchSvc != nil {
		s.mcpSrv.AddTools(watchTools(s.watchSvc)...)
	}
	if s.leaseSvc != nil {
		s.mcpSrv.AddTools(leaseTools(s.leaseSvc)...)
	}
	if s.lockSvc != nil {
		s.mcpSrv.AddTools(lockTools(s.lockSvc)...)
	}
	if s.indexSvc != nil {
		s.mcpSrv.AddTools(indexTools(s.indexSvc)...)
	}
	if s.tsSvc != nil {
		s.mcpSrv.AddTools(timeSeriesTools(s.tsSvc)...)
	}
	if s.vectorSvc != nil {
		s.mcpSrv.AddTools(vectorTools(s.vectorSvc)...)
	}
	if s.embeddingSvc != nil {
		s.mcpSrv.AddTools(embeddingTools(s.embeddingSvc)...)
	}
	if s.ragSvc != nil {
		s.mcpSrv.AddTools(ragTools(s.ragSvc)...)
	}
	if s.userSvc != nil {
		s.mcpSrv.AddTools(userTools(s.userSvc)...)
	}
	if s.roleSvc != nil && s.userSvc != nil {
		s.mcpSrv.AddTools(roleTools(s.roleSvc, s.userSvc)...)
	}
	if s.certProv != nil {
		s.mcpSrv.AddTools(certTools(s.certProv)...)
	}
	if s.collectionSvc != nil {
		s.mcpSrv.AddTools(collectionTools(s.collectionSvc)...)
	}
	if s.dbSvc != nil {
		s.mcpSrv.AddTools(databaseTools(s.dbSvc)...)
	}
	if s.barrierSvc != nil {
		s.mcpSrv.AddTools(barrierTools(s.barrierSvc, s.pinBackend, s.encMigrator)...)
	}
	if s.ceremonySvc != nil {
		s.mcpSrv.AddTools(ceremonyTools(s.ceremonySvc, s.ceremonyCA)...)
	}
	if s.credentialSvc != nil {
		s.mcpSrv.AddTools(credentialTools(s.credentialSvc)...)
	}
	if s.tenantSvc != nil {
		s.mcpSrv.AddTools(tenantTools(s.tenantSvc)...)
		s.mcpSrv.AddTools(tenantBarrierTools(s.tenantSvc, s.authorizeTenantBarrierOp)...)
	}
	if s.backupSvc != nil {
		s.mcpSrv.AddTools(backupTools(s.backupSvc)...)
	}
	if s.rebalanceSvc != nil {
		s.mcpSrv.AddTools(rebalanceTools(s.rebalanceSvc)...)
	}
	if s.authFactorSvc != nil {
		s.mcpSrv.AddTools(authFactorTools(s.authFactorSvc)...)
	}
	if s.userAuthFactorSvc != nil {
		s.mcpSrv.AddTools(userAuthFactorTools(s.userAuthFactorSvc, s.userSvc)...)
	}
	// Audit query tool.
	// Authorization: requires "audit:query" permission.
	// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
	if s.auditLogger != nil {
		s.mcpSrv.AddTools(auditTools(s.auditLogger)...)
	}
	if s.sqlSvc != nil {
		s.mcpSrv.AddTools(sqlTools(s.sqlSvc)...)
	}

	return s
}

// Start begins listening and serving MCP requests over HTTP. The MCP
// Streamable HTTP handler is mounted at /mcp. When TLS is configured,
// the listener is wrapped with tls.NewListener.
func (s *Server) Start() error {
	// A WithTLS config with an explicitly weak MinVersion was rejected at
	// option-application time; surface it now.
	if s.tlsErr != nil {
		return s.tlsErr
	}
	// TLS-only: fail closed rather than bind a plaintext listener. The
	// orchestrator always supplies a config (operator-provided or
	// auto-provisioned from the node CA); a nil here means a direct caller
	// tried to start the raw server without TLS.
	if s.tlsConfig == nil {
		return &TLSNotConfiguredError{}
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}

	ln = tls.NewListener(ln, s.tlsConfig)
	s.listener = ln

	// Create the Streamable HTTP handler from the MCP server. When auth is
	// configured, install the HTTP context func that authenticates the mTLS
	// peer and injects the resulting identity into the per-request context so
	// the tool-handler authorization middleware can enforce permissions.
	var streamOpts []mcpserver.StreamableHTTPOption
	if s.authMW != nil {
		streamOpts = append(streamOpts, mcpserver.WithHTTPContextFunc(s.authHTTPContext))
	}
	streamHandler := mcpserver.NewStreamableHTTPServer(s.mcpSrv, streamOpts...)

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
