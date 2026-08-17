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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// RESTMiddleware returns an http.Handler that wraps the given handler with
// authentication and authorization. Per-route permissions come from the
// RESTRouteRegistry the REST server populates at route-registration time —
// the single source of truth that replaced the old static permission tables.
//
// The middleware injects TLS state, short-circuits the small
// pre-authentication public allowlist, authenticates the caller (mTLS, or a
// verified on-behalf-of assertion for forwarded writes), resolves the
// matched route's registered permission via the registry's chi matcher, and
// authorizes the action. On failure it returns a JSON error response with
// the appropriate HTTP status code.
//
// DEFAULT-DENY: a request whose path belongs to the registered API surface
// but whose method+path resolves to no registered permission is rejected
// rather than treated as public. Requests outside the registered surface
// (the embedded web UI / static assets served as the router's fallback) pass
// through after authentication.
//
// The middleware never wraps the ResponseWriter, so streaming handlers
// (the SSE /watch endpoint) keep their http.Flusher.
func (m *AuthMiddleware) RESTMiddleware(routes *RESTRouteRegistry, next http.Handler) http.Handler {
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
		// The same routes are registered as RESTRoutePublic in the registry so
		// the server's default-deny startup assertion accounts for them.
		if r.URL.Path == "/health" ||
			r.URL.Path == "/v1/ca/bootstrap" ||
			r.URL.Path == "/v1/config/initialized" ||
			r.URL.Path == "/v1/cluster/keydelivery" ||
			r.URL.Path == "/v1/cluster/nodekey" {
			next.ServeHTTP(w, r)
			return
		}

		// Module-registered PUBLIC routes short-circuit BEFORE
		// authentication, exactly like the built-in allowlist above: a
		// capability module's first-contact surfaces (WebAuthn
		// registration/login ceremonies, OIDC discovery, trust bootstrap)
		// are anonymous BY DECLARATION at the registration site
		// (HandlePublic → RESTRoutePublic), and demanding a peer
		// certificate for them would break every anonymous login ceremony
		// the moment client_auth is enabled. The registry lookup resolves
		// through chi's own trie, so templated public routes match here
		// too. Authenticated/permission routes proceed to authentication
		// below unchanged.
		if _, kind, ok := routes.Lookup(r.Method, r.URL.Path); ok && kind == RESTRoutePublic {
			next.ServeHTTP(w, r.WithContext(ctx))
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
			// qrdb's own authenticators produced no identity. If the matched
			// route belongs to a capability module that registered an
			// authentication fallback (a session-token plane qrdb does not
			// speak — e.g. the JWT pairs xkms's FIDO2/PIV logins mint), give
			// the OWNING module one chance to authenticate the request. The
			// fallback identity then flows through finalizeIdentity — the
			// SAME store-resolved role enrichment and disabled-user check as
			// an mTLS identity — and on to the one authorizer below; the
			// fallback only ever replaces the "who is this" step, never the
			// authorization decision. Routes without a fallback keep the
			// original failure verbatim.
			fb := routes.FallbackFor(r.Method, r.URL.Path)
			if fb == nil {
				code := mapHTTPAuthError(authnErr)
				writeJSONError(w, code, authnErr.Error())
				return
			}
			fbIdentity, fbErr := fb.AuthenticateHTTP(r.WithContext(ctx))
			if fbErr != nil || fbIdentity == nil {
				code := mapHTTPAuthError(authnErr)
				if fbErr != nil {
					code = mapHTTPAuthError(fbErr)
					writeJSONError(w, code, fbErr.Error())
					return
				}
				writeJSONError(w, code, authnErr.Error())
				return
			}
			ctx, identity, authnErr = m.finalizeIdentity(ctx, fbIdentity)
			if authnErr != nil {
				code := mapHTTPAuthError(authnErr)
				writeJSONError(w, code, authnErr.Error())
				return
			}
		}

		// Resolve the route's registered authorization contract. The registry
		// matches with chi's own trie, so templated routes ("/users/{id}") and
		// most-specific-wins resolution come from the router, not a parallel
		// pattern table.
		perm, kind, ok := routes.Lookup(r.Method, r.URL.Path)
		if !ok {
			// DEFAULT-DENY: the path belongs to a registered API route but the
			// method carries no registered permission (e.g. a wrong-method
			// request). Reject rather than fall through to the router.
			if routes.PathHasRoutes(r.URL.Path) {
				code := mapHTTPAuthError(&auth.AuthError{Code: "unauthorized"})
				writeJSONError(w, code, "route not authorized (no permission mapping): "+r.Method+" "+r.URL.Path)
				return
			}
			// Outside the registered API surface: the embedded web UI /
			// static assets served as the router's fallback.
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		switch kind {
		case RESTRoutePublic, RESTRouteAuthenticated:
			// Public routes normally short-circuit above; authenticated-only
			// routes (/metrics) require no specific permission.
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Authorize the action (permission.IdentityGated short-circuits inside
		// authorize: possession of an authenticated identity is the middleware
		// requirement and the handler enforces the real binding check).
		if authzErr := m.authorize(ctx, identity, perm); authzErr != nil {
			code := mapHTTPAuthError(authzErr)
			writeJSONError(w, code, authzErr.Error())
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
