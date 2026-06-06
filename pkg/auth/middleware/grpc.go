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
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// UnaryInterceptor returns a grpc.UnaryServerInterceptor that performs
// authentication and authorization for each unary RPC. Methods without
// a permission mapping are allowed through without authorization.
func (m *AuthMiddleware) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Inject TLS state from gRPC peer info so the mTLS
		// authenticator can extract peer certificate identity.
		ctx = injectTLSState(ctx)

		// Authenticate the caller.
		ctx, identity, authnErr := m.authenticate(ctx)
		if authnErr != nil {
			return nil, mapGRPCAuthError(authnErr)
		}

		// Look up the required permission for this method. DEFAULT-DENY: an
		// unmapped method that reaches a service handler is rejected rather
		// than treated as public, so a newly-added RPC cannot silently become
		// unauthorized. Only infrastructure services (health/reflection) are
		// exempt.
		perm, ok := LookupGRPC(info.FullMethod)
		if !ok {
			if isPublicGRPCMethod(info.FullMethod) {
				return handler(ctx, req)
			}
			return nil, mapGRPCAuthError(&auth.AuthError{
				Code:    "unauthorized",
				Message: "grpc: method not authorized (no permission mapping): " + info.FullMethod,
			})
		}

		// Authorize the action.
		if authzErr := m.authorize(ctx, identity, perm); authzErr != nil {
			return nil, mapGRPCAuthError(authzErr)
		}

		return handler(ctx, req)
	}
}

// isPublicGRPCMethod reports whether a gRPC full method belongs to an
// infrastructure service (health checking, server reflection) that is
// intentionally reachable without an application permission. Application
// services (QRDBService, TenantBarrierService) are NOT public — every method
// must carry a permission mapping or it is denied.
func isPublicGRPCMethod(fullMethod string) bool {
	switch {
	case strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/"):
		return true
	case strings.HasPrefix(fullMethod, "/grpc.reflection."):
		return true
	default:
		return false
	}
}

// StreamInterceptor returns a grpc.StreamServerInterceptor that performs
// authentication and authorization for each streaming RPC. The enriched
// context with the caller's identity is propagated to the stream via a
// wrapped ServerStream.
func (m *AuthMiddleware) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()

		// Inject TLS state from gRPC peer info so the mTLS
		// authenticator can extract peer certificate identity.
		ctx = injectTLSState(ctx)

		// Authenticate the caller.
		ctx, identity, authnErr := m.authenticate(ctx)
		if authnErr != nil {
			return mapGRPCAuthError(authnErr)
		}

		// Look up the required permission for this stream method. DEFAULT-DENY
		// (see UnaryInterceptor): an unmapped non-infrastructure method is
		// rejected rather than treated as public.
		perm, ok := LookupGRPC(info.FullMethod)
		if !ok {
			if isPublicGRPCMethod(info.FullMethod) {
				return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
			}
			return mapGRPCAuthError(&auth.AuthError{
				Code:    "unauthorized",
				Message: "grpc: method not authorized (no permission mapping): " + info.FullMethod,
			})
		}

		// Authorize the action.
		if authzErr := m.authorize(ctx, identity, perm); authzErr != nil {
			return mapGRPCAuthError(authzErr)
		}

		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

// wrappedStream wraps a grpc.ServerStream to override its Context with
// one that contains the authenticated identity.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the enriched context containing the authenticated identity.
func (w *wrappedStream) Context() context.Context {
	return w.ctx
}

// injectTLSState extracts TLS connection state from the gRPC peer info
// and stores it in the context for the mTLS authenticator. Returns the
// context unchanged if no TLS info is available.
func injectTLSState(ctx context.Context) context.Context {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ctx
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return ctx
	}
	return auth.WithTLSState(ctx, &tlsInfo.State)
}

// mapGRPCAuthError converts authentication/authorization errors to
// appropriate gRPC status codes. AuthError with code "unauthenticated"
// maps to codes.Unauthenticated; "unauthorized" maps to
// codes.PermissionDenied. UserDisabledError maps to
// codes.PermissionDenied. All other errors map to codes.Unauthenticated.
func mapGRPCAuthError(err error) error {
	var authErr *auth.AuthError
	if errors.As(err, &authErr) {
		switch authErr.Code {
		case "unauthenticated", "no_peer_cert", "invalid_cert":
			return status.Error(codes.Unauthenticated, authErr.Error())
		case "unauthorized":
			return status.Error(codes.PermissionDenied, authErr.Error())
		}
	}

	var unavailable *StoreUnavailableError
	if errors.As(err, &unavailable) {
		return status.Error(codes.Unavailable, unavailable.Error())
	}

	var disabled *UserDisabledError
	if errors.As(err, &disabled) {
		return status.Error(codes.PermissionDenied, disabled.Error())
	}

	var chainErr *AuthChainError
	if errors.As(err, &chainErr) {
		return status.Error(codes.Unauthenticated, chainErr.Error())
	}

	return status.Error(codes.Unauthenticated, err.Error())
}
