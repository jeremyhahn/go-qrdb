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

// Package grpc provides the gRPC server for the QRDB distributed key-value
// database. It delegates all operations to the service layer through
// KVService and AdminService interfaces, with optional MVCC, Transaction,
// Batch, Watch, Lease, Index, TimeSeries, Vector, and Database services.
package grpc

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Server is the gRPC server for QRDB. It registers the QRDBService with
// recovery and logging interceptors for both unary and streaming RPCs.
type Server struct {
	kvSvc             service.KVServicer
	adminSvc          service.AdminServicer
	mvccSvc           service.MVCCKVServicer
	txSvc             service.TransactionServicer
	batchSvc          service.BatchServicer
	watchSvc          service.WatchServicer
	leaseSvc          lease.LeaseServicer
	lockSvc           service.LockServicer
	indexSvc          index.IndexServicer
	tsSvc             timeseries.TimeSeriesServicer
	vectorSvc         service.VectorServicer
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	databaseSvc       service.DatabaseServicer
	barrierSvc        service.BarrierServicer
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	credentialSvc     service.CredentialServicer
	tenantSvc         tenant.TenantServicer
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	certProv          cert.CertProvider
	collectionSvc     service.CollectionServicer
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	authMW            *authmw.AuthMiddleware
	auditLogger       audit.AuditLogger
	addr              string
	server            *grpc.Server
	listener          net.Listener
	tls               *tls.Config

	// sealedWindowProbe controls the sealed-window interceptors. When
	// set and IsSealed() returns true, every method whose trailing
	// component is not on SealedWindowAllowlist is rejected with
	// codes.Unavailable. When nil the interceptors are not installed.
	sealedWindowProbe sealedWindowProbe
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the gRPC server with TLS credentials.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tls = cfg
	}
}

// WithMVCC configures the gRPC server with an MVCC key-value service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) {
		s.mvccSvc = svc
	}
}

// WithTransaction configures the gRPC server with a transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) {
		s.txSvc = svc
	}
}

// WithBatch configures the gRPC server with a batch service.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) {
		s.batchSvc = svc
	}
}

// WithWatch configures the gRPC server with a watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) {
		s.watchSvc = svc
	}
}

// WithLease configures the gRPC server with a lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(s *Server) {
		s.leaseSvc = svc
	}
}

// WithLock configures the gRPC server with a distributed lock service.
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) {
		s.lockSvc = svc
	}
}

// WithIndex configures the gRPC server with an index service.
func WithIndex(svc index.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithTimeSeries configures the gRPC server with a time-series service.
func WithTimeSeries(svc timeseries.TimeSeriesServicer) ServerOption {
	return func(s *Server) {
		s.tsSvc = svc
	}
}

// WithVector configures the gRPC server with a vector service.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) {
		s.vectorSvc = svc
	}
}

// WithEmbedding configures the gRPC server with an embedding service.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAGService configures the gRPC server with a RAG service.
func WithRAGService(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithDatabaseService configures the gRPC server with a database management service.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) {
		s.databaseSvc = svc
	}
}

// WithBarrierService configures the gRPC server with a barrier management service.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithPINBackend configures the gRPC server with the canonical
// pin.PINBackend used for the optional SO PIN / User PIN compliance-gating
// layer on barrier administrative operations. When nil, PIN fields supplied
// in barrier requests are ignored.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	return func(s *Server) { s.pinBackend = backend }
}

// WithCeremonyService configures the gRPC server with a ceremony management service.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithCredentialService configures the gRPC server with a credential management service.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithAuditLogger configures the gRPC server with an audit logger, which
// enables the AuditService (compliance-grade audit event queries). When nil
// the audit gRPC service is not registered.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithUserService configures the gRPC server with a user management service.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService configures the gRPC server with a role management service.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithCertProvider configures the gRPC server with a certificate provider.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = svc }
}

// WithCollectionService configures the gRPC server with a collection service.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithTenantService configures the gRPC server with a tenant management service.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithBackupService configures the gRPC server with a backup management service.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService configures the gRPC server with a cluster rebalance service.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService configures the gRPC server with an auth factor service
// for custodian enrollment, challenge, and verification.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService configures the gRPC server with the user-side
// auth factor service (M14) used for the 1-to-many user-factor model.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithAuthMiddleware configures the gRPC server with authentication and
// authorization middleware for unary and streaming interceptors.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) { s.authMW = mw }
}

// WithSealedWindowProbe installs the sealed-window unary and streaming
// interceptors backed by the supplied probe. While probe.IsSealed()
// returns true, every method whose trailing component is not on
// SealedWindowAllowlist is rejected with codes.Unavailable + message
// "barrier sealed". Once unsealed the interceptors are passthrough;
// on re-seal they engage again.
//
// The interceptors are appended to the chain after auth so that
// allowlisted methods (notably Barrier* and Ceremony*) remain
// reachable without authentication during the sealed window — the
// Vault model where possession of the unseal credential is itself
// the authorization to drive unseal.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) { s.sealedWindowProbe = probe }
}

// WithKVService configures the gRPC server with a KV service. This is an
// alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService configures the gRPC server with an admin service. This is
// an alternative to passing adminSvc as a positional parameter and is useful
// when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new gRPC server that listens on the given address
// and delegates to the provided service implementations. kvSvc and adminSvc
// may be nil when only non-KV services (barrier, cert, auth) are needed;
// their RPC handlers return codes.Unimplemented when the service is nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Start begins listening and serving gRPC requests. It creates a TCP
// listener, registers the QRDBService, and starts serving in the
// background. The method returns after the listener is bound.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}
	s.listener = ln

	// Build interceptor chains for both unary and streaming RPCs.
	// Auth interceptors are inserted after logging when configured.
	// The sealed-window interceptor sits AFTER auth so non-allowlisted
	// methods are rejected with codes.Unavailable before the handler
	// runs, while allowlisted methods (Barrier*, Ceremony*) flow
	// through unauthenticated during the sealed window — possession of
	// the unseal credential is the authorization (Vault model).
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		recoveryInterceptor,
		loggingInterceptor,
	}
	if s.authMW != nil {
		unaryInterceptors = append(unaryInterceptors, s.authMW.UnaryInterceptor())
	}
	if s.sealedWindowProbe != nil {
		unaryInterceptors = append(unaryInterceptors,
			SealedWindowUnaryInterceptor(s.sealedWindowProbe, nil))
	}
	streamInterceptors := []grpc.StreamServerInterceptor{
		streamRecoveryInterceptor,
		streamLoggingInterceptor,
	}
	if s.authMW != nil {
		streamInterceptors = append(streamInterceptors, s.authMW.StreamInterceptor())
	}
	if s.sealedWindowProbe != nil {
		streamInterceptors = append(streamInterceptors,
			SealedWindowStreamInterceptor(s.sealedWindowProbe, nil))
	}
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	}

	// Add TLS credentials when configured.
	if s.tls != nil {
		creds := credentials.NewTLS(s.tls)
		serverOpts = append(serverOpts, grpc.Creds(creds))
	}

	s.server = grpc.NewServer(serverOpts...)

	// Register the QRDB gRPC service.
	svc := &qrdbServiceServer{
		kvSvc:         s.kvSvc,
		adminSvc:      s.adminSvc,
		mvccSvc:       s.mvccSvc,
		txSvc:         s.txSvc,
		batchSvc:      s.batchSvc,
		watchSvc:      s.watchSvc,
		leaseSvc:      s.leaseSvc,
		lockSvc:       s.lockSvc,
		indexSvc:      s.indexSvc,
		tsSvc:         s.tsSvc,
		vectorSvc:     s.vectorSvc,
		embeddingSvc:  s.embeddingSvc,
		ragSvc:        s.ragSvc,
		databaseSvc:   s.databaseSvc,
		barrierSvc:    s.barrierSvc,
		pinBackend:    s.pinBackend,
		ceremonySvc:   s.ceremonySvc,
		credentialSvc: s.credentialSvc,
		tenantSvc:     s.tenantSvc,
		userSvc:       s.userSvc,
		roleSvc:       s.roleSvc,
		certProv:      s.certProv,
		collectionSvc: s.collectionSvc,
		backupSvc:     s.backupSvc,
		rebalanceSvc:  s.rebalanceSvc,
	}
	pb.RegisterQRDBServiceServer(s.server, svc)

	// Register the auth factor service when configured.
	RegisterAuthFactorService(s.server, s.authFactorSvc)

	// Register the user-side auth factor service (M14) when configured.
	RegisterUserAuthFactorService(s.server, s.userAuthFactorSvc)

	// Register the per-tenant barrier service when a tenant servicer is configured.
	RegisterTenantBarrierService(s.server, s.tenantSvc, s.authMW)

	// Register the audit query service when an audit logger is configured.
	if s.auditLogger != nil {
		RegisterAuditService(s.server, s.auditLogger)
	}

	proto := "gRPC"
	if s.tls != nil {
		proto = "gRPC+TLS"
	}
	slog.Info("gRPC server started", "addr", ln.Addr().String(), "proto", proto)

	go func() {
		if serveErr := s.server.Serve(ln); serveErr != nil {
			slog.Error("gRPC server error", "error", serveErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the gRPC server, allowing in-flight RPCs
// to complete.
func (s *Server) Stop() {
	if s.server == nil {
		return
	}
	s.server.GracefulStop()
	slog.Info("gRPC server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// recoveryInterceptor catches panics in gRPC handlers and returns an
// Internal error instead of crashing the server.
func recoveryInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("grpc: panic recovered",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(ctx, req)
}

// loggingInterceptor logs each gRPC call with method, duration, and any
// error code.
func loggingInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	duration := time.Since(start)

	code := codes.OK
	if err != nil {
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		}
	}

	level := slog.LevelDebug
	if code != codes.OK {
		level = slog.LevelWarn
	}

	slog.Log(ctx, level, "grpc: request",
		"method", info.FullMethod,
		"code", code.String(),
		"duration", duration,
	)

	return resp, err
}

// streamRecoveryInterceptor catches panics in streaming gRPC handlers
// and returns an Internal error instead of crashing the server.
func streamRecoveryInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("grpc: stream panic recovered",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(srv, ss)
}

// streamLoggingInterceptor logs each streaming gRPC call with method,
// duration, and any error code.
func streamLoggingInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	start := time.Now()
	err := handler(srv, ss)
	duration := time.Since(start)

	code := codes.OK
	if err != nil {
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		}
	}

	level := slog.LevelDebug
	if code != codes.OK {
		level = slog.LevelWarn
	}

	slog.Log(ss.Context(), level, "grpc: stream",
		"method", info.FullMethod,
		"code", code.String(),
		"duration", duration,
	)

	return err
}
