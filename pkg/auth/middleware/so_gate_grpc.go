// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// SOGateUnaryInterceptor applies gate to every unary RPC whose
// (qrdb.v1.permission) option is in SOPermissions. It must run AFTER the
// AuthMiddleware's UnaryInterceptor, which binds the identity it reads. The
// PIN travels in the SOPINMetadataKey metadata entry and is read only from a
// TLS connection. The gRPC and Unix socket servers both install it.
func SOGateUnaryInterceptor(gate *SOGate) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := enforceSOGateGRPC(ctx, gate, info.FullMethod)
		defer WipeSOFactor(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// SOGateStreamInterceptor is SOGateUnaryInterceptor for streaming RPCs.
func SOGateStreamInterceptor(gate *SOGate) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := enforceSOGateGRPC(ss.Context(), gate, info.FullMethod)
		defer WipeSOFactor(ctx)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

// enforceSOGateGRPC binds the gRPC request's SO factor and enforces the gate
// for fullMethod's permission. A method without a permission mapping never
// reaches here past the auth interceptor, and is not SO-gated.
func enforceSOGateGRPC(ctx context.Context, gate *SOGate, fullMethod string) (context.Context, error) {
	perm, ok := LookupGRPCFromDescriptor(fullMethod)
	if !ok {
		return ctx, nil
	}
	var (
		clientIP string
		isSecure bool
		pin      []byte
	)
	if p, ok := peer.FromContext(ctx); ok {
		if p.Addr != nil {
			clientIP = p.Addr.String()
		}
		_, isSecure = p.AuthInfo.(credentials.TLSInfo)
	}
	if isSecure {
		if vals := metadata.ValueFromIncomingContext(ctx, SOPINMetadataKey); len(vals) > 0 && vals[0] != "" {
			pin = []byte(vals[0])
		}
	}
	ctx = WithSOFactor(ctx, NewSOFactor(ctx, pin, isSecure, clientIP))
	if err := gate.Enforce(ctx, perm, fullMethod, "call"); err != nil {
		return ctx, GRPCError(err, codes.Unavailable)
	}
	return ctx, nil
}
