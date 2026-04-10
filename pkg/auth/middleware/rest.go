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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// RESTMiddleware returns an http.Handler that wraps the given handler
// with authentication and authorization. Requests to routes without a
// permission mapping are passed through without authorization.
//
// The middleware reads the request path and method, authenticates the
// caller (typically via mTLS from the TLS state in context), looks up
// the required permission, and authorizes the action. On failure it
// returns a JSON error response with the appropriate HTTP status code.
func (m *AuthMiddleware) RESTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Inject TLS connection state into context so the mTLS
		// authenticator can extract peer certificate identity.
		if r.TLS != nil {
			ctx = auth.WithTLSState(ctx, r.TLS)
		}

		// Health probes and CA bootstrap are public -- skip authentication
		// entirely. Load balancers, Kubernetes probes, and monitoring systems
		// must reach /health without presenting client certificates. The
		// /v1/ca/bootstrap endpoint is public so joining nodes without TLS
		// credentials can bootstrap their trust store.
		if r.URL.Path == "/health" || r.URL.Path == "/v1/ca/bootstrap" {
			next.ServeHTTP(w, r)
			return
		}

		// Authenticate the caller.
		ctx, identity, authnErr := m.authenticate(ctx)
		if authnErr != nil {
			code := mapHTTPAuthError(authnErr)
			writeJSONError(w, code, authnErr.Error())
			return
		}

		// Determine the canonical path for permission lookup.
		// The REST server routes /kv/{key} requests through a
		// prefix match, so we normalize to /kv for method-based
		// dispatch.
		path := normalizePath(r.URL.Path)

		// Look up the required permission for this route.
		perm, ok := LookupREST(r.Method, path)
		if !ok {
			// No mapping means no authorization required (public route).
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Authorize the action.
		if authzErr := m.authorize(ctx, identity, perm); authzErr != nil {
			code := mapHTTPAuthError(authzErr)
			writeJSONError(w, code, authzErr.Error())
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// normalizePath maps REST request paths to their canonical permission
// lookup form. Paths under /kv/ with a key component are normalized
// to /kv for the method-based KV dispatch.
func normalizePath(path string) string {
	// Exact matches for known sub-paths take priority.
	switch {
	case path == "/kv" || path == "/kv/":
		return "/kv"
	case path == "/kv/scan" || path == "/kv/range" ||
		path == "/kv/lease" || path == "/kv/revision" ||
		path == "/kv/compact":
		return path
	case strings.HasPrefix(path, "/kv/"):
		// /kv/{key} -> /kv (method-dispatched)
		return "/kv"
	}
	return path
}

// mapHTTPAuthError maps auth errors to HTTP status codes.
func mapHTTPAuthError(err error) int {
	var authErr *auth.AuthError
	if errors.As(err, &authErr) {
		switch authErr.Code {
		case "unauthorized":
			return http.StatusForbidden
		case "unauthenticated", "no_peer_cert", "invalid_cert":
			return http.StatusUnauthorized
		}
	}

	var unavailable *StoreUnavailableError
	if errors.As(err, &unavailable) {
		return http.StatusServiceUnavailable
	}

	var disabled *UserDisabledError
	if errors.As(err, &disabled) {
		return http.StatusForbidden
	}

	var chainErr *AuthChainError
	if errors.As(err, &chainErr) {
		return http.StatusUnauthorized
	}

	return http.StatusUnauthorized
}

// restErrorResponse is the JSON payload for auth error responses.
type restErrorResponse struct {
	Error string `json:"error"`
}

// writeJSONError writes a JSON error response with the given HTTP
// status code and message.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(restErrorResponse{Error: message}); err != nil {
		slog.Error("auth/middleware: JSON encode error", "error", err)
	}
}
