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

// Package quic provides a native binary QUIC transport server for QRDB.
// It uses raw QUIC streams with a binary wire protocol for maximum
// throughput, bypassing HTTP and gRPC framing overhead. All service
// operations are dispatched via a map-based handler registry.
package quic

import (
	"context"
	"crypto/tls"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
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

// methodHandler is the function signature for QUIC method dispatch.
// It receives the request context and raw protobuf payload, returning
// the serialized response payload or an error.
type methodHandler func(ctx context.Context, payload []byte) ([]byte, error)

// Server is the native QUIC transport server for QRDB. It listens for
// QUIC connections, accepts bidirectional streams, and dispatches
// requests to the service layer using a binary wire protocol.
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
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	certProv          cert.CertProvider
	collectionSvc     service.CollectionServicer
	databaseSvc       service.DatabaseServicer
	barrierSvc        service.BarrierServicer
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	tenantSvc         tenant.TenantServicer
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	credentialSvc     service.CredentialServicer
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	authMW            *authmw.AuthMiddleware
	auditLogger       audit.AuditLogger

	// registry, when set, resolves data-plane services per (tenant, database)
	// scope. The tenant comes from the authenticated identity; the database
	// from the request frame's database field. When nil, the singleton service
	// fields are used, preserving single-database and embedded deployments.
	registry service.Registry

	addr      string
	tlsConfig *tls.Config
	listener  *quicgo.Listener
	handlers  map[string]methodHandler
	closed    atomic.Bool
	wg        sync.WaitGroup
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the QUIC server with TLS credentials.
// TLS is mandatory for QUIC; this option allows overriding the default.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) { s.tlsConfig = cfg }
}

// WithMVCC configures the QUIC server with an MVCC key-value service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) { s.mvccSvc = svc }
}

// WithTransaction configures the QUIC server with a transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) { s.txSvc = svc }
}

// WithBatch configures the QUIC server with a batch service.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) { s.batchSvc = svc }
}

// WithWatch configures the QUIC server with a watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) { s.watchSvc = svc }
}

// WithLease configures the QUIC server with a lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(s *Server) { s.leaseSvc = svc }
}

// WithLock configures the QUIC server with a distributed lock service.
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) { s.lockSvc = svc }
}

// WithIndex configures the QUIC server with an index service.
func WithIndex(svc index.IndexServicer) ServerOption {
	return func(s *Server) { s.indexSvc = svc }
}

// WithTimeSeries configures the QUIC server with a time-series service.
func WithTimeSeries(svc timeseries.TimeSeriesServicer) ServerOption {
	return func(s *Server) { s.tsSvc = svc }
}

// WithVector configures the QUIC server with a vector service.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) { s.vectorSvc = svc }
}

// WithUserService configures the QUIC server with a user management service.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService configures the QUIC server with a role management service.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithCertProvider configures the QUIC server with a certificate provider.
func WithCertProvider(prov cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = prov }
}

// WithCollectionService configures the QUIC server with a collection service.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithDatabaseService configures the QUIC server with a database management service.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) { s.databaseSvc = svc }
}

// WithBarrierService configures the QUIC server with a barrier management service.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithPINBackend configures the QUIC server with the canonical
// pin.PINBackend used for the optional SO PIN / User PIN compliance-gating
// layer on barrier administrative operations.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	return func(s *Server) { s.pinBackend = backend }
}

// WithCeremonyService configures the QUIC server with a ceremony management service.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithTenantService configures the QUIC server with a tenant management service.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithBackupService configures the QUIC server with a backup management service.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService configures the QUIC server with a cluster rebalance service.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithEmbedding configures the QUIC server with a text embedding service.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAGService configures the QUIC server with a RAG service.
func WithRAGService(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithCredentialService configures the QUIC server with a credential
// management service for credential storage, rotation, and versioning.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithAuthFactorService configures the QUIC server with an auth factor service
// for custodian enrollment, challenge, and verification.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService configures the QUIC server with the user-side
// auth factor service (M14) used for the 1-to-many user-factor model.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithAuditLogger configures the QUIC server with a compliance-grade audit logger.
// When set, the "audit.Query" frame method is registered.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithAuthMiddleware configures the QUIC server with authentication and
// authorization middleware.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) { s.authMW = mw }
}

// WithServiceRegistry configures the QUIC server with a per-(tenant, database)
// service registry. When set, data-plane handlers resolve their servicer from
// the registry using the authenticated tenant and the database field carried
// in the request frame; when nil they use the singleton service fields. This
// gives the native QUIC transport the same per-database keyspace isolation as
// the REST, gRPC, and Unix transports.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) { s.registry = reg }
}

// WithKVService configures the QUIC server with a KV service. This is an
// alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService configures the QUIC server with an admin service. This is
// an alternative to passing adminSvc as a positional parameter and is useful
// when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new native QUIC server that listens on the given
// address and delegates to the provided service implementations. kvSvc and
// adminSvc may be nil when only non-KV services (barrier, cert, auth) are
// needed; their handlers are registered only when the respective service is
// non-nil. The server is not started until Start() is called.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	s.buildHandlers()

	return s
}

// Start begins listening for QUIC connections and serving requests.
// The method returns after the listener is bound; connections are
// served in the background.
func (s *Server) Start() error {
	if s.tlsConfig == nil {
		return &StartError{Addr: s.addr, Err: &tlsRequiredError{}}
	}

	tlsCfg := s.tlsConfig.Clone()
	tlsCfg.NextProtos = append(tlsCfg.NextProtos, "qrdb-quic")

	quicCfg := &quicgo.Config{
		MaxIdleTimeout:        30 * time.Second,
		KeepAlivePeriod:       10 * time.Second,
		MaxIncomingStreams:    1000,
		MaxIncomingUniStreams: 0,
		Allow0RTT:             true,
	}

	ln, err := quicgo.ListenAddr(s.addr, tlsCfg, quicCfg)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}
	s.listener = ln

	slog.Info("QUIC server started", "addr", ln.Addr().String())

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop gracefully shuts down the QUIC server, closing the listener and
// waiting for in-flight connections to drain.
func (s *Server) Stop() {
	if s.listener == nil {
		return
	}
	s.closed.Store(true)
	_ = s.listener.Close()
	s.wg.Wait()
	slog.Info("QUIC server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// acceptLoop accepts new QUIC connections until the listener is closed.
func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept(context.Background())
		if err != nil {
			if s.closed.Load() {
				return
			}
			slog.Error("quic: accept error", "error", err)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection processes streams on a single QUIC connection.
func (s *Server) handleConnection(conn *quicgo.Conn) {
	defer s.wg.Done()

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			if s.closed.Load() {
				return
			}
			// Connection closed by client or network error.
			return
		}

		s.wg.Add(1)
		go s.handleStream(conn, stream)
	}
}

// handleStream reads a request frame from the stream, dispatches to the
// handler, writes the response frame, and closes the stream.
func (s *Server) handleStream(conn *quicgo.Conn, stream *quicgo.Stream) {
	defer s.wg.Done()
	defer func() { _ = stream.Close() }()

	// Recover from panics in handlers.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("quic: panic recovered",
				"panic", r,
				"stack", string(debug.Stack()),
			)
			_ = wire.WriteResponse(stream, 0, wire.StatusError, []byte("internal server error"))
		}
	}()

	method, database, requestID, payload, err := wire.ReadRequest(stream)
	if err != nil {
		slog.Debug("quic: failed to read request", "error", err)
		return
	}

	start := time.Now()

	// Build context with connection state for auth and the database scope for
	// per-(tenant, database) service resolution. The tenant dimension flows
	// separately from the authenticated identity (set during authenticate);
	// only the database is sourced from the request frame. An empty database
	// resolves to the implicit bootstrap database.
	ctx := context.Background()
	ctx = withConnectionState(ctx, conn)
	ctx = tenant.WithDatabase(ctx, database)

	// Auth: authenticate and authorize if middleware is configured.
	if s.authMW != nil {
		ctx, err = s.authenticate(ctx, conn)
		if err != nil {
			_ = wire.WriteResponse(stream, requestID, wire.StatusUnauthenticated, []byte(err.Error()))
			s.logRequest(method, requestID, wire.StatusUnauthenticated, start)
			return
		}

		if authErr := s.authorize(ctx, method); authErr != nil {
			_ = wire.WriteResponse(stream, requestID, wire.StatusPermissionDenied, []byte(authErr.Error()))
			s.logRequest(method, requestID, wire.StatusPermissionDenied, start)
			return
		}
	}

	// Watch is server-streaming: it writes a stream of response frames on this
	// same stream rather than a single unary response, so it bypasses the
	// request/response dispatch table.
	if isWatchMethod(method) {
		s.handleWatchStream(ctx, stream, requestID, method == watchPrefixMethodKey, payload)
		s.logRequest(method, requestID, wire.StatusOK, start)
		return
	}

	// Dispatch to handler.
	handler, ok := s.handlers[method]
	if !ok {
		_ = wire.WriteResponse(stream, requestID, wire.StatusUnimplemented, []byte((&DispatchError{Method: method}).Error()))
		s.logRequest(method, requestID, wire.StatusUnimplemented, start)
		return
	}

	resp, handlerErr := handler(ctx, payload)
	if handlerErr != nil {
		statusCode, errPayload := mapServiceError(handlerErr)
		_ = wire.WriteResponse(stream, requestID, statusCode, errPayload)
		s.logRequest(method, requestID, statusCode, start)
		return
	}

	_ = wire.WriteResponse(stream, requestID, wire.StatusOK, resp)
	s.logRequest(method, requestID, wire.StatusOK, start)
}

// logRequest logs a QUIC request at the appropriate level.
func (s *Server) logRequest(method string, requestID uint64, status uint16, start time.Time) {
	duration := time.Since(start)
	level := slog.LevelDebug
	if status != wire.StatusOK {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "quic: request",
		"method", method,
		"request_id", requestID,
		"status", status,
		"duration", duration,
	)
}

// tlsRequiredError is returned when TLS is not configured for the QUIC server.
type tlsRequiredError struct{}

func (e *tlsRequiredError) Error() string {
	return "quic: TLS is required for QUIC transport"
}
