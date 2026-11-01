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

package grpc

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
)

// ClientLimitUnaryInterceptor admits every unary call through l, keyed by
// the call's client address, before any other interceptor runs (go-qrdb#1197).
// A refused call fails with codes.ResourceExhausted and a RetryInfo detail.
// A call l already charged at the HTTP boundary (clientlimit.Limiter.HTTPOnce,
// a gRPC-Web call) is not charged again. On a nil Limiter it passes every
// call through.
func ClientLimitUnaryInterceptor(l *clientlimit.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := admitClient(ctx, l); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// ClientLimitStreamInterceptor is ClientLimitUnaryInterceptor for streams:
// opening a stream is one request.
func ClientLimitStreamInterceptor(l *clientlimit.Limiter) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := admitClient(ss.Context(), l); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func admitClient(ctx context.Context, l *clientlimit.Limiter) error {
	if l == nil || l.Admitted(ctx) {
		return nil
	}
	var addr net.Addr
	if p, ok := peer.FromContext(ctx); ok {
		addr = p.Addr
	}
	md, _ := metadata.FromIncomingContext(ctx)
	return mapServiceError(l.Admit(addr, md.Get))
}
