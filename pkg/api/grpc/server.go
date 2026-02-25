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
// KVService and AdminService interfaces.
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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Server is the gRPC server for QRDB. It registers the QRDBService with
// recovery and logging interceptors.
type Server struct {
	kvSvc    service.KVServicer
	adminSvc service.AdminServicer
	addr     string
	server   *grpc.Server
	listener net.Listener
	tls      *tls.Config
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithTLS configures the gRPC server with TLS credentials.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tls = cfg
	}
}

// NewServer creates a new gRPC server that listens on the given address
// and delegates to the provided service implementations.
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

	// Build server options with interceptor chain.
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			recoveryInterceptor,
			loggingInterceptor,
		),
	}

	// Add TLS credentials when configured.
	if s.tls != nil {
		creds := credentials.NewTLS(s.tls)
		serverOpts = append(serverOpts, grpc.Creds(creds))
	}

	s.server = grpc.NewServer(serverOpts...)

	// Register the QRDB gRPC service.
	svc := &qrdbServiceServer{
		kvSvc:    s.kvSvc,
		adminSvc: s.adminSvc,
	}
	pb.RegisterQRDBServiceServer(s.server, svc)

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
