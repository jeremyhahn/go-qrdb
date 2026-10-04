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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// awaitingAdminCredentialProbe is the abstraction the interceptor needs to
// know whether the node is currently "awaiting admin credential"
// (go-qrdb#548 review item 3). Defined as an interface, exactly like
// sealedWindowProbe, so tests can substitute a deterministic fake.
type awaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// AwaitingAdminCredentialAllowlist is the set of fully-qualified QRDBService
// methods that remain reachable while the node is awaiting admin
// credential. Derived from the canonical transport-neutral verb table so it
// cannot drift from the other transports. The Unix socket transport, which
// serves the identical QRDBService over a different listener, reuses this
// value directly rather than deriving its own.
var AwaitingAdminCredentialAllowlist = sealedwindow.GRPCFullMethodAwaitingAdminCredential(pb.QRDBService_ServiceDesc.ServiceName)

// ChainUnaryServerInterceptors composes interceptors into ONE
// grpc.UnaryServerInterceptor, in the given order -- interceptors[0] runs
// first and its own call to handler invokes interceptors[1], and so on,
// exactly as grpc.ChainUnaryInterceptor composes the ServerOption form. It
// exists as a standalone value, exported for the Unix socket transport to
// reuse, because the awaiting-admin-credential gate must be able to call
// the COMBINED chain directly (when not awaiting) or skip it entirely and
// call the bare RPC handler (when awaiting and the method is allowed) -- a
// choice grpc.ChainUnaryInterceptor does not expose, since it only ever
// hands each interceptor the NEXT one in the chain, never the chain's own
// combined form or its terminus.
func ChainUnaryServerInterceptors(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		chained := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			interceptor, next := interceptors[i], chained
			chained = func(currentCtx context.Context, currentReq any) (any, error) {
				return interceptor(currentCtx, currentReq, info, next)
			}
		}
		return chained(ctx, req)
	}
}

// ChainStreamServerInterceptors is ChainUnaryServerInterceptors' streaming
// sibling.
func ChainStreamServerInterceptors(interceptors ...grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		chained := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			interceptor, next := interceptors[i], chained
			chained = func(currentSrv any, currentSS grpc.ServerStream) error {
				return interceptor(currentSrv, currentSS, info, next)
			}
		}
		return chained(srv, ss)
	}
}

// AwaitingAdminCredentialUnaryInterceptor wraps chain (the server's normal,
// fully composed unary interceptor chain -- auth, SO gate, sealed window)
// with the awaiting-admin-credential gate (go-qrdb#548 review item 3).
// While probe reports awaiting, a method on allowlist is routed DIRECTLY to
// handler, bypassing chain -- and therefore authentication -- entirely,
// since there is no bound identity to authenticate as; every other method
// is refused with codes.Unavailable before chain, or the handler, ever
// runs. Once probe reports false, every call reaches chain unchanged.
//
// Exported for the Unix socket transport, which serves the identical
// QRDBService over a different listener and shares this gate's rule
// rather than reimplementing it.
func AwaitingAdminCredentialUnaryInterceptor(probe awaitingAdminCredentialProbe, allowlist map[string]struct{}, chain grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if probe == nil || !probe.AwaitingAdminCredential() {
			return chain(ctx, req, info, handler)
		}
		if _, ok := allowlist[info.FullMethod]; !ok {
			return nil, authmw.GRPCError(&authmw.AwaitingAdminCredentialError{}, codes.Unavailable)
		}
		return handler(ctx, req)
	}
}

// AwaitingAdminCredentialStreamInterceptor is
// AwaitingAdminCredentialUnaryInterceptor's streaming sibling.
func AwaitingAdminCredentialStreamInterceptor(probe awaitingAdminCredentialProbe, allowlist map[string]struct{}, chain grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if probe == nil || !probe.AwaitingAdminCredential() {
			return chain(srv, ss, info, handler)
		}
		if _, ok := allowlist[info.FullMethod]; !ok {
			return authmw.GRPCError(&authmw.AwaitingAdminCredentialError{}, codes.Unavailable)
		}
		return handler(srv, ss)
	}
}
