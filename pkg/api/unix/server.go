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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Server is a gRPC server that listens on a Unix domain socket. It
// delegates all operations to the same service implementations used
// by the TCP gRPC server, but communicates over a local socket path
// without TLS.
type Server struct {
	kvSvc      service.KVServicer
	adminSvc   service.AdminServicer
	socketPath string
	server     *grpc.Server
	listener   net.Listener
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// NewServer creates a new Unix domain socket gRPC server that listens
// on the given socket path and delegates to the provided service
// implementations.
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

	// Reuse the gRPC package's service registration.
	grpcpkg.RegisterService(s.server, s.kvSvc, s.adminSvc)

	slog.Info("unix gRPC server started", "socket", s.socketPath)

	go func() {
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
