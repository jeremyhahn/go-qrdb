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

package sotest

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// SecretProbe is a gRPC server for RunBarrierSecretTLS: it answers Health, so
// the SDK's Connect succeeds, and counts every other RPC that reaches it on
// any service, answering Unimplemented. Like Probe it stands in for a server
// the client cannot trust, because the property under test is what the
// CLIENT sends.
type SecretProbe struct {
	pb.UnimplementedQRDBServiceServer
	reached atomic.Int64
}

// Health reports healthy.
func (p *SecretProbe) Health(context.Context, *emptypb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: "ok"}, nil
}

// Reached is the number of RPCs other than Health that reached the probe.
func (p *SecretProbe) Reached() int {
	return int(p.reached.Load())
}

// Serve serves the probe on lis, over TLS when serverTLS is set, until the
// test ends.
func (p *SecretProbe) Serve(t *testing.T, lis net.Listener, serverTLS *tls.Config) {
	t.Helper()
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if info.FullMethod != pb.QRDBService_Health_FullMethodName {
				p.reached.Add(1)
			}
			return handler(ctx, req)
		}),
		grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
			p.reached.Add(1)
			return status.Error(codes.Unimplemented, "sotest: secret probe")
		}),
	}
	if serverTLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(serverTLS)))
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterQRDBServiceServer(srv, p)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}
