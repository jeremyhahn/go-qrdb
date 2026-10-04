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
	"crypto/tls"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

// UnaryInterceptor returns a grpc.UnaryServerInterceptor that performs
// authentication and authorization for each unary RPC. The required
// permission is declared once on the RPC's proto descriptor via the
// (qrdb.v1.permission) method option; methods without the option are
// denied unless they belong to an infrastructure service or were declared
// public via RegisterPublicGRPCMethods.
func (m *AuthMiddleware) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// A public method (infrastructure service or module-declared via
		// RegisterPublicGRPCMethods) bypasses authentication AND
		// authorization entirely — this is checked first, before
		// authenticate() runs, so a caller with no credentials at all can
		// reach it. See isPublicGRPCMethod and isRegisteredPublicGRPCMethod.
		if isPublicGRPCMethod(info.FullMethod) || m.isRegisteredPublicGRPCMethod(info.FullMethod) {
			return handler(ctx, req)
		}

		// Inject TLS state from gRPC peer info so the mTLS
		// authenticator can extract peer certificate identity, and the
		// caller's authorization metadata so an embedder's bearer
		// authenticator sees it exactly as it does on REST and MCP.
		ctx = injectTLSState(ctx)
		ctx = injectAuthorization(ctx)

		// Authenticate the caller: the ORIGINAL caller of a request a cluster
		// peer forwarded on its behalf, otherwise whoever presented
		// credentials here.
		var (
			identity *auth.Identity
			authnErr error
		)
		if assertion, sig, forwarded := GRPCOnBehalfAssertion(ctx); forwarded {
			ctx, identity, authnErr = m.authenticateGRPCOnBehalf(ctx, info.FullMethod, req, assertion, sig)
		} else {
			ctx, identity, authnErr = m.authenticate(ctx)
		}
		if authnErr != nil {
			return nil, mapGRPCAuthError(authnErr)
		}

		// Look up the required permission for this method from its proto
		// descriptor's (qrdb.v1.permission) option. DEFAULT-DENY: a method
		// without the option that reaches a service handler is rejected
		// rather than treated as public, so a newly-added RPC cannot
		// silently become unauthorized.
		perm, ok := LookupGRPCFromDescriptor(info.FullMethod)
		if !ok {
			return nil, mapGRPCAuthError(&auth.AuthError{
				Code:    "unauthorized",
				Message: "grpc: method not authorized (no permission mapping): " + info.FullMethod,
			})
		}

		// Authorize the action against the tenant the request names (see
		// Target and RegisterGRPCTargets).
		if authzErr := m.authorizeTarget(ctx, identity, perm, m.grpcTarget(info.FullMethod, req)); authzErr != nil {
			return nil, mapGRPCAuthError(authzErr)
		}

		return handler(ctx, req)
	}
}

// GRPCTargetFunc extracts the Target a unary gRPC request names.
type GRPCTargetFunc func(req any) Target

// RegisterGRPCTargets declares, per full method name, how a request names
// the tenant it acts on, so UnaryInterceptor authorizes the method's
// permission against that tenant rather than the caller's own (see Target).
// A method with no registration acts on the caller's own tenant.
//
// Safe to call more than once and concurrently with in-flight requests, like
// RegisterPublicGRPCMethods: every transport sharing this middleware may
// register the same service's table, and the set only grows, replaced in one
// atomic store. A malformed method name or a nil extractor is refused with
// InvalidGRPCTargetError, so a typo fails startup instead of silently leaving
// the method authorized against the caller's own tenant.
func (m *AuthMiddleware) RegisterGRPCTargets(targets map[string]GRPCTargetFunc) error {
	if len(targets) == 0 {
		return nil
	}
	for method, fn := range targets {
		if !isValidGRPCMethodName(method) || fn == nil {
			return &InvalidGRPCTargetError{Method: method}
		}
	}
	next := make(map[string]GRPCTargetFunc, len(targets))
	if existing := m.grpcTargets.Load(); existing != nil {
		for method, fn := range *existing {
			next[method] = fn
		}
	}
	for method, fn := range targets {
		next[method] = fn
	}
	m.grpcTargets.Store(&next)
	return nil
}

// grpcTarget returns the Target req names under fullMethod's registration,
// or the caller's own tenant when the method has none.
func (m *AuthMiddleware) grpcTarget(fullMethod string, req any) Target {
	targets := m.grpcTargets.Load()
	if targets == nil {
		return SelfTarget()
	}
	fn, ok := (*targets)[fullMethod]
	if !ok {
		return SelfTarget()
	}
	return fn(req)
}

// isPublicGRPCMethod reports whether a gRPC full method belongs to the one
// infrastructure service intentionally reachable without credentials: health
// checking, which reports liveness and nothing else. Application services
// (QRDBService, TenantBarrierService) are NOT public — every method must
// carry a permission mapping, or be declared public by a capability module
// via RegisterPublicGRPCMethods, or it is denied.
//
// Server reflection is deliberately NOT exempt. It answers with the node's
// whole schema -- every service, method and message -- so a composer that
// registers it and wants it anonymous must declare that explicitly through
// RegisterPublicGRPCMethods.
func isPublicGRPCMethod(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/")
}

// RegisterPublicGRPCMethods declares gRPC full method names that must be
// reachable without authentication or authorization, on top of the built-in
// infrastructure services isPublicGRPCMethod already exempts. It is how a
// capability module's apimodule.GRPCPublicMethodLister declaration reaches
// the interceptors: the unified server's gRPC and unix transports call this
// once per qualifying module during Start, before the server accepts
// connections, symmetric with HTTPRegistrar.HandlePublic on REST.
//
// Each method must be a full gRPC method name in the "/package.Service/
// Method" form gRPC hands the interceptors via info.FullMethod; a malformed
// name is rejected with InvalidPublicGRPCMethodError so a typo in a module's
// declaration fails server startup loudly instead of silently leaving the
// intended RPC auth-gated. len(methods) == 0 is a no-op, not an error, so a
// module without any public RPCs can still implement the interface trivially.
//
// Safe to call more than once (e.g. once per transport sharing the same
// AuthMiddleware instance, or with a second module's declarations): the
// method set only grows. Safe to call concurrently with in-flight requests —
// the underlying set is replaced with a new map in one atomic store, so an
// interceptor already serving traffic observes either the pre- or
// post-registration set, never a partially-populated one.
func (m *AuthMiddleware) RegisterPublicGRPCMethods(methods []string) error {
	if len(methods) == 0 {
		return nil
	}
	for _, method := range methods {
		if !isValidGRPCMethodName(method) {
			return &InvalidPublicGRPCMethodError{Method: method}
		}
	}

	next := make(map[string]struct{}, len(methods))
	if existing := m.publicGRPCMethods.Load(); existing != nil {
		for method := range *existing {
			next[method] = struct{}{}
		}
	}
	for _, method := range methods {
		next[method] = struct{}{}
	}
	m.publicGRPCMethods.Store(&next)
	return nil
}

// isValidGRPCMethodName reports whether method has the full gRPC method name
// shape ("/package.Service/Method") that grpc.UnaryServerInfo.FullMethod and
// grpc.StreamServerInfo.FullMethod use: a leading slash, a non-empty service
// component, a separating slash, and a non-empty method component.
func isValidGRPCMethodName(method string) bool {
	if !strings.HasPrefix(method, "/") {
		return false
	}
	rest := strings.TrimPrefix(method, "/")
	idx := strings.LastIndex(rest, "/")
	return idx > 0 && idx < len(rest)-1
}

// isRegisteredPublicGRPCMethod reports whether fullMethod was declared
// public by a capability module via RegisterPublicGRPCMethods.
func (m *AuthMiddleware) isRegisteredPublicGRPCMethod(fullMethod string) bool {
	methods := m.publicGRPCMethods.Load()
	if methods == nil {
		return false
	}
	_, ok := (*methods)[fullMethod]
	return ok
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
		// See UnaryInterceptor: a public method bypasses authentication AND
		// authorization entirely, checked before authenticate() runs.
		if isPublicGRPCMethod(info.FullMethod) || m.isRegisteredPublicGRPCMethod(info.FullMethod) {
			return handler(srv, ss)
		}

		ctx := ss.Context()

		// Inject TLS state from gRPC peer info so the mTLS
		// authenticator can extract peer certificate identity, and the
		// caller's authorization metadata so an embedder's bearer
		// authenticator sees it exactly as it does on REST and MCP.
		ctx = injectTLSState(ctx)
		ctx = injectAuthorization(ctx)

		// Authenticate the caller.
		ctx, identity, authnErr := m.authenticate(ctx)
		if authnErr != nil {
			return mapGRPCAuthError(authnErr)
		}

		// Look up the required permission for this stream method from its
		// proto descriptor's (qrdb.v1.permission) option. DEFAULT-DENY (see
		// UnaryInterceptor): a non-infrastructure, non-declared-public method
		// without the option is rejected rather than treated as public.
		perm, ok := LookupGRPCFromDescriptor(info.FullMethod)
		if !ok {
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

// GRPCOnBehalfAssertion returns the on-behalf assertion and signature a
// forwarded request carries in its metadata. A request carrying only one of
// the two is treated as not forwarded, and authenticates as itself.
func GRPCOnBehalfAssertion(ctx context.Context) (assertion, sig string, ok bool) {
	a := metadata.ValueFromIncomingContext(ctx, forward.MetadataAssertion)
	s := metadata.ValueFromIncomingContext(ctx, forward.MetadataSignature)
	if len(a) == 0 || len(s) == 0 || a[0] == "" || s[0] == "" {
		return "", "", false
	}
	return a[0], s[0], true
}

// authenticateGRPCOnBehalf authenticates a gRPC request a cluster peer
// forwarded on a caller's behalf. The peer must present a verified cluster
// node certificate (onBehalfPeerGate), and the assertion must be bound to
// this method and this request message, serialized deterministically -- the
// form the forwarding node signed.
func (m *AuthMiddleware) authenticateGRPCOnBehalf(ctx context.Context, fullMethod string, req any, assertion, sig string) (context.Context, *auth.Identity, error) {
	var state *tls.ConnectionState
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			state = &tlsInfo.State
		}
	}
	chains, err := auth.ResolveVerifiedChains(ctx, state, m.issuerVerifier)
	if err != nil {
		return ctx, nil, err
	}
	if gerr := m.onBehalfPeerGate(ctx, chains); gerr != nil {
		return ctx, nil, gerr
	}
	peerCert := chains[0][0]
	msg, ok := req.(proto.Message)
	if !ok {
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
	}
	payload, merr := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if merr != nil {
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
	}
	return m.authenticateOnBehalfBinding(ctx, peerCert, assertion, sig, forward.RPCBinding("grpc", fullMethod, payload))
}

// injectAuthorization carries the caller's "authorization" metadata into ctx
// through auth.WithHTTPAuthorization, the carrier the REST and MCP transports
// fill from the Authorization header. An embedder's credential authenticator
// (node.WithAuthenticators) reads auth.HTTPAuthorizationFromContext and so
// works unchanged on gRPC and Unix. gRPC lower-cases metadata keys, so the
// header a client sets as "Authorization" arrives here as "authorization".
//
// The FIRST value is carried, as net/http's Header.Get does for REST, so the
// two transports hand an authenticator the same value for the same request.
// Absent metadata stores nothing.
func injectAuthorization(ctx context.Context) context.Context {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 0 {
		return ctx
	}
	return auth.WithHTTPAuthorization(ctx, values[0])
}

// mapGRPCAuthError converts authentication/authorization errors to gRPC
// status codes for the gRPC auth interceptors.
//
// The per-type reasoning lives once in failure_class.go; this wrapper supplies
// the fallback specific to this call site. codes.Unauthenticated for an
// unrecognised failure, for the same reason mapHTTPAuthError answers 401: the
// interceptors authenticate as well as authorize.
func mapGRPCAuthError(err error) error {
	return GRPCError(err, codes.Unauthenticated)
}
