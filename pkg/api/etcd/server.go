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

// Package etcd provides a per-database etcd-compatible gRPC server. Each
// database with etcd enabled gets its own Server instance bound to its
// shard's service layer (MVCC, Watch, Lease, Transaction).
package etcd

import (
	"crypto/tls"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Server is a per-database etcd-compatible gRPC server. Each database
// with etcd enabled gets its own Server instance bound to its shard's
// service layer (MVCC, Watch, Lease, Transaction).
type Server struct {
	addr       string
	dbName     string
	grpcServer *grpc.Server
	listener   net.Listener
	logger     *slog.Logger
	opts       serverOptions
}

// serverOptions holds optional dependencies injected via ServerOption.
type serverOptions struct {
	tlsConfig *tls.Config
	clusterID uint64
	memberID  uint64
	raftTerm  uint64
	mvccSvc   service.MVCCKVServicer
	txSvc     service.TransactionServicer
	watchSvc  service.WatchServicer
	leaseSvc  lease.LeaseServicer
	adminSvc  service.AdminServicer

	// authorizer authenticates and authorizes every RPC. Start refuses to
	// serve without it.
	authorizer RequestAuthorizer
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*serverOptions)

// WithTLS configures TLS for the etcd gRPC server.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(o *serverOptions) { o.tlsConfig = cfg }
}

// WithClusterID sets the cluster ID used in response headers.
func WithClusterID(id uint64) ServerOption {
	return func(o *serverOptions) { o.clusterID = id }
}

// WithMemberID sets the member ID used in response headers.
func WithMemberID(id uint64) ServerOption {
	return func(o *serverOptions) { o.memberID = id }
}

// WithRaftTerm sets the Raft term used in response headers.
func WithRaftTerm(term uint64) ServerOption {
	return func(o *serverOptions) { o.raftTerm = term }
}

// WithMVCC configures the MVCC key-value service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(o *serverOptions) { o.mvccSvc = svc }
}

// WithTransaction configures the transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(o *serverOptions) { o.txSvc = svc }
}

// WithWatch configures the watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(o *serverOptions) { o.watchSvc = svc }
}

// WithLease configures the lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(o *serverOptions) { o.leaseSvc = svc }
}

// WithAdmin configures the admin service for cluster membership.
func WithAdmin(svc service.AdminServicer) ServerOption {
	return func(o *serverOptions) { o.adminSvc = svc }
}

// NewServer creates a new etcd-compatible gRPC server for the named
// database. The server does not start until Start() is called.
func NewServer(dbName, addr string, opts ...ServerOption) *Server {
	s := &Server{
		addr:   addr,
		dbName: dbName,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(&s.opts)
	}
	return s
}

// Start creates a TCP listener and starts serving etcd gRPC services. It
// refuses with AuthRequiredError when no TLS configuration or no
// RequestAuthorizer is set: the etcd Auth service is a stub, so either gap
// would serve the database to anyone who can reach the port. It also
// refuses, with *RevisionHistoryRequiredError, when the MVCC service reports
// that its state machine keeps no revision history.
func (s *Server) Start() error {
	if s.opts.tlsConfig == nil || s.opts.authorizer == nil {
		return &AuthRequiredError{Database: s.dbName}
	}
	if !keepsRevisionHistory(s.opts.mvccSvc) {
		return &StartError{Addr: s.addr, Err: &RevisionHistoryRequiredError{}}
	}
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}
	s.listener = lis

	grpcOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(s.opts.tlsConfig)),
		grpc.ChainUnaryInterceptor(s.unaryAuthorize),
		grpc.ChainStreamInterceptor(s.streamAuthorize),
	}

	s.grpcServer = grpc.NewServer(grpcOpts...)

	// Register etcd KV service.
	if s.opts.mvccSvc != nil {
		kvSrv := &kvServer{
			mvccSvc:   s.opts.mvccSvc,
			txSvc:     s.opts.txSvc,
			clusterID: s.opts.clusterID,
			memberID:  s.opts.memberID,
		}
		pb.RegisterKVServer(s.grpcServer, kvSrv)
	}

	// Register etcd Lease service.
	if s.opts.leaseSvc != nil {
		leaseSrv := &leaseServer{
			leaseSvc:  s.opts.leaseSvc,
			clusterID: s.opts.clusterID,
			memberID:  s.opts.memberID,
		}
		pb.RegisterLeaseServer(s.grpcServer, leaseSrv)
	}

	// Register etcd Watch service.
	if s.opts.watchSvc != nil {
		watchSrv := &watchServer{
			watchSvc:  s.opts.watchSvc,
			mvccSvc:   s.opts.mvccSvc,
			clusterID: s.opts.clusterID,
			memberID:  s.opts.memberID,
		}
		pb.RegisterWatchServer(s.grpcServer, watchSrv)
	}

	// Register etcd Auth service (stub - returns auth not enabled).
	pb.RegisterAuthServer(s.grpcServer, &authServer{})

	// Register etcd Cluster service.
	if s.opts.adminSvc != nil {
		clusterSrv := &clusterServer{
			adminSvc:  s.opts.adminSvc,
			clusterID: s.opts.clusterID,
			memberID:  s.opts.memberID,
		}
		pb.RegisterClusterServer(s.grpcServer, clusterSrv)
	}

	// Register etcd Maintenance service.
	maintenanceSrv := &maintenanceServer{
		adminSvc:  s.opts.adminSvc,
		mvccSvc:   s.opts.mvccSvc,
		clusterID: s.opts.clusterID,
		memberID:  s.opts.memberID,
	}
	pb.RegisterMaintenanceServer(s.grpcServer, maintenanceSrv)

	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error("etcd server error", "database", s.dbName, "error", err)
		}
	}()

	s.logger.Info("etcd server started", "database", s.dbName, "addr", s.Addr())
	return nil
}

// Stop gracefully shuts down the etcd gRPC server.
func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	s.logger.Info("etcd server stopped", "database", s.dbName)
}

// Addr returns the listener address, or the configured address if
// the server has not yet started.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}
