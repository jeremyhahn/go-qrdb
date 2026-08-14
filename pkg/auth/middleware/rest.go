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

		// A small set of endpoints is public -- skip authentication entirely.
		// Load balancers and probes must reach /health without a client
		// certificate; /v1/ca/bootstrap lets joining nodes fetch the trust
		// anchor before they have credentials; /v1/config/initialized lets the
		// first-boot setup wizard check provisioning state before any user or
		// certificate exists; the two /v1/cluster/ key-transport routes are the
		// cluster peer plane — authorized by node identity (peer cert SPIFFE SAN)
		// and the placement table inside the handler itself, not by user RBAC.
		//
		// The cluster paths are matched EXACTLY (not by prefix) so that a future
		// handler accidentally mounted under /v1/cluster/ is not silently left
		// unauthenticated; each exact path here is a route that self-authorizes.
		if r.URL.Path == "/health" ||
			r.URL.Path == "/v1/ca/bootstrap" ||
			r.URL.Path == "/v1/config/initialized" ||
			r.URL.Path == "/v1/cluster/keydelivery" ||
			r.URL.Path == "/v1/cluster/nodekey" {
			next.ServeHTTP(w, r)
			return
		}

		// Authenticate the caller. A forwarded write from a cluster peer
		// carries a signed on-behalf-of assertion of the ORIGINAL caller;
		// when present it is verified (peer must be a cluster node, signature
		// and request-binding must hold) and the request is authorized AS THE
		// CALLER with roles re-resolved from this node's own store -- never as
		// the forwarding node, and never trusting asserted roles. When absent,
		// normal mTLS authentication runs unchanged.
		var (
			identity *auth.Identity
			authnErr error
		)
		if hasOnBehalfAssertion(r) {
			ctx, identity, authnErr = m.authenticateOnBehalf(ctx, r)
		} else {
			ctx, identity, authnErr = m.authenticate(ctx)
		}
		if authnErr != nil {
			code := mapHTTPAuthError(authnErr)
			writeJSONError(w, code, authnErr.Error())
			return
		}

		// Determine the canonical path for permission lookup.
		//
		// An EXACT mapping always wins. A sub-route that declares its own
		// permission (POST /kv/restore, /kv/scan, ...) must never be collapsed
		// into the /kv/{key} dispatch: normalization would authorize it under a
		// different permission than the one it declared, or — when /kv has no
		// mapping for that method — reject it outright. Normalization is only
		// the fallback for the genuinely dynamic /kv/{key} form, where the key
		// is arbitrary and cannot be enumerated in the map.
		//
		// This ordering also means a NEW sub-route is protected by simply
		// declaring it in the permission map, with no second registration to
		// keep in sync.
		path := r.URL.Path
		perm, ok := LookupREST(r.Method, path)
		if !ok {
			path = normalizePath(r.URL.Path)
			perm, ok = LookupREST(r.Method, path)
		}

		// DEFAULT-DENY: an unmapped route that lands on the REST API surface is
		// rejected rather than treated as public. Requests outside the API
		// surface (the embedded web UI / static assets served as the router's
		// fallback) and a small authenticated-but-unprivileged allowlist (e.g.
		// /metrics) pass through so the SPA and monitoring keep working.
		if !ok {
			if isRESTAPISurface(path) && !isRESTAuthzPublic(path) {
				code := mapHTTPAuthError(&auth.AuthError{Code: "unauthorized"})
				writeJSONError(w, code, "route not authorized (no permission mapping): "+r.Method+" "+path)
				return
			}
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

// restAPIPrefixes enumerates the path prefixes that make up the REST API
// surface. Under DEFAULT-DENY, a request whose path is under one of these
// prefixes but has no permission mapping is rejected. Paths outside these
// prefixes (the embedded web UI / static assets served as the router fallback,
// favicon, etc.) are not part of the protected API surface and pass through.
var restAPIPrefixes = []string{
	"/kv", "/tx", "/batch", "/watch", "/lease", "/index", "/ts", "/vector",
	"/lock", "/status", "/peers", "/membership", "/snapshot", "/drain",
	"/capabilities", "/users", "/roles", "/permissions", "/certs", "/v1/",
}

// restAuthzPublicPaths are API-surface paths that require authentication but no
// specific permission (e.g. Prometheus metrics scraping by any authenticated
// peer). Pre-authentication-exempt paths (/health, /v1/ca/bootstrap,
// /v1/config/initialized) are handled earlier and never reach here.
var restAuthzPublicPaths = map[string]struct{}{
	"/metrics": {},
}

// isRESTAPISurface reports whether path is part of the protected REST API.
func isRESTAPISurface(path string) bool {
	for _, p := range restAPIPrefixes {
		if path == p || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// isRESTAuthzPublic reports whether an (authenticated) caller may reach path
// without holding a specific permission.
func isRESTAuthzPublic(path string) bool {
	_, ok := restAuthzPublicPaths[path]
	return ok
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
