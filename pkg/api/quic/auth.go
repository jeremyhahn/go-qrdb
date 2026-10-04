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

package quic

import (
	"context"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// withConnectionState injects the QUIC connection's TLS state into the
// context for the auth middleware.
func withConnectionState(ctx context.Context, conn *quicgo.Conn) context.Context {
	tlsState := conn.ConnectionState().TLS
	return auth.WithTLSState(ctx, &tlsState)
}

// authenticate extracts the TLS peer certificate from the QUIC connection
// and authenticates the caller via the auth middleware.
func (s *Server) authenticate(ctx context.Context, conn *quicgo.Conn) (context.Context, error) {
	tlsState := conn.ConnectionState().TLS
	if len(tlsState.PeerCertificates) == 0 {
		return ctx, &AuthenticationError{Reason: "no client certificate provided"}
	}

	ctx = auth.WithTLSState(ctx, &tlsState)

	authCtx, err := s.authMW.AuthenticateQUIC(ctx)
	if err != nil {
		return ctx, &AuthenticationError{Reason: err.Error()}
	}

	return authCtx, nil
}

// authenticateOnBehalf authenticates a request a cluster peer forwarded on a
// caller's behalf: the connection's peer must be a cluster node, and the
// envelope's assertion must be bound to method and payload.
func (s *Server) authenticateOnBehalf(ctx context.Context, conn *quicgo.Conn, method string, payload []byte, env *wire.OnBehalf) (context.Context, error) {
	tlsState := conn.ConnectionState().TLS
	ctx = auth.WithTLSState(ctx, &tlsState)
	authCtx, err := s.authMW.AuthenticateQUICOnBehalf(ctx, method, payload, env.Assertion, env.Signature)
	if err != nil {
		return ctx, &AuthenticationError{Reason: err.Error()}
	}
	return authCtx, nil
}

// authorize checks whether the authenticated caller has permission for
// the given QUIC method, resolving the required permission from the
// registration-site methodPerms registry (DEFAULT-DENY for unregistered
// methods).
//
// payload is the frame's request payload: the permission is authorized
// against the tenant the payload names (see authmw.Target).
func (s *Server) authorize(ctx context.Context, method string, payload []byte) error {
	if err := s.authMW.AuthorizeQUIC(ctx, s.methodPerms, method, payload); err != nil {
		// Err carries the middleware's own typed error. Without it the
		// wrapper ERASED the distinction between "you may not" and "this node
		// could not answer" -- errors.As could see nothing through it, so the
		// frame said permission-denied for every refusal including a failed
		// audit write (go-qrdb#335).
		return &AuthorizationError{Method: method, Reason: err.Error(), Err: err}
	}
	return nil
}

// AuthenticationError is returned when QUIC authentication fails.
type AuthenticationError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *AuthenticationError) Error() string {
	return "quic: authentication failed: " + e.Reason
}

// AuthorizationError is returned when QUIC authorization fails.
type AuthorizationError struct {
	Method     string
	Permission permission.Permission
	Reason     string

	// Err is the underlying middleware error, so errors.As reaches the
	// typed cause and Status can tell a denial apart from a node-side
	// fault. Nil only for a value built by a caller that had no cause.
	Err error
}

// Error returns a human-readable description.
//
// The wording follows the CLASS of the failure, because "permission denied"
// is not merely imprecise for a retryable fault -- it is the sentence that
// sends an operator to fix permissions that were never the problem. A full
// audit disk refusing an unseal is not a permission problem, and this is the
// only channel MCP-style readers of the message have.
func (e *AuthorizationError) Error() string {
	if authmw.ClassifyAuthError(e.Err).Retryable() {
		return "quic: cannot authorize " + e.Method + ": " + e.Reason
	}
	return "quic: permission denied for " + e.Method + ": " + e.Reason
}

// Unwrap returns the underlying middleware error.
func (e *AuthorizationError) Unwrap() error { return e.Err }

// quicStatusByClass maps a classified auth failure onto the QUIC wire status.
//
// A rate-limited caller answers StatusResourceExhausted.
//
// This carried a NAMED PROTOCOL EXCLUSION until go-qrdb#437 (PR #461) landed:
// the wire declared six statuses and none of them meant "quota", so
// AuthFailureRateLimited rode StatusUnavailable -- right instruction (retry),
// coarser reason. #461 added the status, which made the exclusion false, so it
// is removed rather than left standing: an exclusion that outlives its cause
// reads as a decision someone made about the current protocol.
//
// Recorded because the merge that made it stale did not conflict here. The two
// changes touched different files, so nothing forced a reader to revisit this
// clause -- it would have kept mapping quota to Unavailable, correct-looking
// and wrong, with its own comment explaining why.
var quicStatusByClass = map[authmw.AuthFailureClass]uint16{
	authmw.AuthFailureUnauthenticated:  wire.StatusUnauthenticated,
	authmw.AuthFailureUnauthorized:     wire.StatusPermissionDenied,
	authmw.AuthFailureUnavailable:      wire.StatusUnavailable,
	authmw.AuthFailureRateLimited:      wire.StatusResourceExhausted,
	authmw.AuthFailureAuditUnavailable: wire.StatusUnavailable,
}

// authzStatus returns the wire status for a failed authorization gate.
//
// PermissionDenied is the fallback, not StatusError: these gates run after
// authentication, and it keeps the frame identical to what this path sent
// before for anything the middleware does not classify. It mirrors
// writeAuthzErrorJSON on REST and authzError on gRPC, all three over the same
// middleware.ClassifyAuthError.
func authzStatus(err error) uint16 {
	if st, ok := quicStatusByClass[authmw.ClassifyAuthError(err)]; ok {
		return st
	}
	// Not an authorization outcome at all -- the request's own payload could
	// not be read to find the tenant it acts on. The one classification
	// table every transport renders answers it (a bad request), so QUIC says
	// the same as REST and gRPC rather than calling it a denial.
	if errclass.Classify(err) != errclass.Unclassified {
		return ServiceErrorStatus(err)
	}
	return wire.StatusPermissionDenied
}
