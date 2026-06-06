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

// Package unix provides a gRPC server over Unix domain sockets for QRDB.
// It reuses the gRPC service implementation but listens on a local Unix
// socket, providing efficient inter-process communication without TLS.
package unix

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
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

// Server is a gRPC server that listens on a Unix domain socket. It
// delegates all operations to the same service implementations used
// by the TCP gRPC server, but communicates over a local socket path
// without TLS.
type Server struct {
	kvSvc            service.KVServicer
	adminSvc         service.AdminServicer
	socketPath       string
	userSvc          user.UserServicer
	roleSvc          role.RoleServicer
	certProv         cert.CertProvider
	barrierSvc       service.BarrierServicer
	pinBackend       pin.PINBackend
	ceremonySvc      service.CeremonyServicer
	credentialSvc    service.CredentialServicer
	collectionSvc    service.CollectionServicer
	mvccSvc          service.MVCCKVServicer
	txSvc            service.TransactionServicer
	batchSvc         service.BatchServicer
	watchSvc         service.WatchServicer
	leaseSvc         lease.LeaseServicer
	lockSvc          service.LockServicer
	indexSvc         index.IndexServicer
	tsSvc            timeseries.TimeSeriesServicer
	vectorSvc        service.VectorServicer
	embeddingSvc     service.EmbeddingServicer
	ragSvc           service.RAGServicer
	databaseSvc      service.DatabaseServicer
	tenantSvc        tenant.TenantServicer
	backupSvc        service.BackupServicer
	rebalanceSvc     service.RebalanceServicer
	authFactorSvc    service.AuthFactorServicer
	tenantBarrierSvc tenant.TenantServicer
	auditLogger      audit.AuditLogger
	server           *grpc.Server
	listener         net.Listener
	serveWg          sync.WaitGroup
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithUserService configures the Unix socket server with a user management service.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService configures the Unix socket server with a role management service.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithCertProvider configures the Unix socket server with a certificate provider.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = svc }
}

// WithBarrierService configures the Unix socket server with a barrier service.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithPINBackend configures the Unix socket server with the canonical
// pin.PINBackend used for the optional SO PIN / User PIN compliance-gating
// layer on barrier administrative operations.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	return func(s *Server) { s.pinBackend = backend }
}

// WithCeremonyService configures the Unix socket server with a ceremony service.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithCredentialService configures the Unix socket server with a credential service.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithCollectionService configures the Unix socket server with a collection service.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithMVCC configures the Unix socket server with an MVCC service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) { s.mvccSvc = svc }
}

// WithTransaction configures the Unix socket server with a transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) { s.txSvc = svc }
}

// WithBatch configures the Unix socket server with a batch service.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) { s.batchSvc = svc }
}

// WithWatch configures the Unix socket server with a watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) { s.watchSvc = svc }
}

// WithLease configures the Unix socket server with a lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(s *Server) { s.leaseSvc = svc }
}

// WithLock configures the Unix socket server with a distributed lock service.
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) { s.lockSvc = svc }
}

// WithIndex configures the Unix socket server with an index service.
func WithIndex(svc index.IndexServicer) ServerOption {
	return func(s *Server) { s.indexSvc = svc }
}

// WithTimeSeries configures the Unix socket server with a time-series service.
func WithTimeSeries(svc timeseries.TimeSeriesServicer) ServerOption {
	return func(s *Server) { s.tsSvc = svc }
}

// WithVector configures the Unix socket server with a vector service.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) { s.vectorSvc = svc }
}

// WithEmbedding configures the Unix socket server with a text embedding service.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAGService configures the Unix socket server with a RAG service.
func WithRAGService(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithDatabaseService configures the Unix socket server with a database service.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) { s.databaseSvc = svc }
}

// WithTenantService configures the Unix socket server with a tenant management service.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithBackupService configures the Unix socket server with a backup management service.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService configures the Unix socket server with a cluster rebalance service.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService configures the Unix socket server with an auth factor
// service for custodian enrollment, challenge, and verification.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithTenantBarrierService configures the Unix socket server with a tenant
// servicer used to expose the per-tenant barrier RPCs (Init, Unseal, Seal,
// Status). When set, the TenantBarrierService is registered on the gRPC
// server alongside the main QRDBService — same gating pattern as
// WithAuthFactorService and WithAuditLogger.
func WithTenantBarrierService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantBarrierSvc = svc }
}

// WithKVService configures the Unix socket server with a KV service. This is
// an alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService configures the Unix socket server with an admin service.
// This is an alternative to passing adminSvc as a positional parameter and is
// useful when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new Unix domain socket gRPC server that listens on the
// given socket path and delegates to the provided service implementations.
// kvSvc and adminSvc may be nil when only non-KV services (barrier, cert, auth)
// are needed; their RPC handlers return codes.Unimplemented when nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, socketPath string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:      kvSvc,
		adminSvc:   adminSvc,
		socketPath: socketPath,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start creates the parent directory for the socket path, removes any
// stale socket file, binds a Unix domain socket listener, registers the
// QRDB gRPC service, and starts serving in the background. The method
// returns after the listener is bound.
func (s *Server) Start() error {
	// Ensure the parent directory exists.
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &MkdirError{Dir: dir, Err: err}
	}

	// Remove stale socket file from a previous run.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return &StartError{SocketPath: s.socketPath, Err: err}
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return &StartError{SocketPath: s.socketPath, Err: err}
	}
	s.listener = ln

	// Build server options with the same interceptor chain as the TCP
	// gRPC server: recovery first, then logging.
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			recoveryInterceptor,
			loggingInterceptor,
		),
	}

	s.server = grpc.NewServer(serverOpts...)

	// Build register options for optional services.
	var regOpts []grpcpkg.RegisterOption
	if s.userSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterUserService(s.userSvc))
	}
	if s.roleSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRoleService(s.roleSvc))
	}
	if s.certProv != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCertProvider(s.certProv))
	}
	if s.barrierSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBarrierService(s.barrierSvc))
	}
	if s.pinBackend != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterPINBackend(s.pinBackend))
	}
	if s.ceremonySvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCeremonyService(s.ceremonySvc))
	}
	if s.credentialSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCredentialService(s.credentialSvc))
	}
	if s.collectionSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCollectionService(s.collectionSvc))
	}
	if s.mvccSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterMVCC(s.mvccSvc))
	}
	if s.txSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTransaction(s.txSvc))
	}
	if s.batchSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBatch(s.batchSvc))
	}
	if s.watchSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterWatch(s.watchSvc))
	}
	if s.leaseSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterLease(s.leaseSvc))
	}
	if s.lockSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterLockService(s.lockSvc))
	}
	if s.indexSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterIndex(s.indexSvc))
	}
	if s.tsSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTimeSeries(s.tsSvc))
	}
	if s.vectorSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterVector(s.vectorSvc))
	}
	if s.embeddingSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterEmbedding(s.embeddingSvc))
	}
	if s.ragSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRAGService(s.ragSvc))
	}
	if s.databaseSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterDatabaseService(s.databaseSvc))
	}
	if s.tenantSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTenantService(s.tenantSvc))
	}
	if s.backupSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBackupService(s.backupSvc))
	}
	if s.rebalanceSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRebalanceService(s.rebalanceSvc))
	}

	// Reuse the gRPC package's service registration.
	grpcpkg.RegisterService(s.server, s.kvSvc, s.adminSvc, regOpts...)

	// Register AuditService alongside QRDBService when an audit logger is set.
	// Authorization: requires "audit:query" permission.
	// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
	if s.auditLogger != nil {
		grpcpkg.RegisterAuditService(s.server, s.auditLogger)
	}

	// Register AuthFactorService alongside QRDBService when configured.
	if s.authFactorSvc != nil {
		grpcpkg.RegisterAuthFactorService(s.server, s.authFactorSvc)
	}

	// Register TenantBarrierService alongside QRDBService when configured.
	// The same TenantServicer can be passed to both WithTenantService (for
	// CRUD) and WithTenantBarrierService (for the per-tenant barrier
	// lifecycle); they expose disjoint gRPC methods on disjoint service
	// descriptors.
	// The Unix socket is a local-trust transport with no auth middleware, so
	// the tenant-scoped barrier gate is passed nil (no-op) here.
	if s.tenantBarrierSvc != nil {
		grpcpkg.RegisterTenantBarrierService(s.server, s.tenantBarrierSvc, nil)
	}

	slog.Info("unix gRPC server started", "socket", s.socketPath)

	s.serveWg.Add(1)
	go func() {
		defer s.serveWg.Done()
		if serveErr := s.server.Serve(ln); serveErr != nil {
			slog.Error("unix gRPC server error", "error", serveErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the gRPC server, allowing in-flight RPCs
// to complete, and removes the socket file.
func (s *Server) Stop() {
	if s.server == nil {
		return
	}
	s.server.GracefulStop()

	// Wait for the serve goroutine to exit before removing the socket
	// to prevent a race where a subsequent Start recreates the socket
	// file and the exiting goroutine removes it.
	s.serveWg.Wait()

	// Clean up the socket file.
	_ = os.Remove(s.socketPath)

	slog.Info("unix gRPC server stopped", "socket", s.socketPath)
}

// Addr returns the socket path. This satisfies the same convention as
// the TCP gRPC server's Addr method.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.socketPath
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
			slog.Error("unix grpc: panic recovered",
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

	slog.Log(ctx, level, "unix grpc: request",
		"method", info.FullMethod,
		"code", code.String(),
		"duration", duration,
	)

	return resp, err
}
