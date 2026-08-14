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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
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

// authorize checks whether the authenticated caller has permission for
// the given QUIC method, resolving the required permission from the
// registration-site methodPerms registry (DEFAULT-DENY for unregistered
// methods).
func (s *Server) authorize(ctx context.Context, method string) error {
	if err := s.authMW.AuthorizeQUIC(ctx, s.methodPerms, method); err != nil {
		return &AuthorizationError{Method: method, Reason: err.Error()}
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
}

// Error returns a human-readable description.
func (e *AuthorizationError) Error() string {
	return "quic: permission denied for " + e.Method + ": " + e.Reason
}
