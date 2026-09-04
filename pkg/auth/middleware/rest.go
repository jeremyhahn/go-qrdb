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
		// Carry the raw Authorization header for embedder-supplied
		// credential authenticators (e.g. an OAuth 2.1 bearer validator
		// registered via wiring.AuthMiddlewareConfig.ExtraAuthenticators);
		// qrdb ships no token-validating authenticator itself.
		ctx = auth.WithHTTPAuthorization(ctx, r.Header.Get("Authorization"))

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
			//
			// When the METHOD matched no route, FallbackFor widens to the
			// path's owning module (go-qrdb#211): a module owns the path's
			// auth plane, not one verb of it, and a caller it can
			// authenticate deserves the 405 below rather than a 401 that
			// blames its credentials for a wrong verb. That widened case
			// provably cannot reach a handler -- Lookup misses the same
			// (method, path) a few lines down -- so the identity it
			// establishes only ever decides 405 versus 401.
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
			// No auth method: this identity came from a capability module's own
			// session-token plane (the JWT pairs xkms's FIDO2/PIV logins mint),
			// a ceremony qrdb does not speak and cannot name. The module that
			// ran it enforces the permitted-method policy there.
			ctx, identity, authnErr = m.finalizeIdentity(ctx, fbIdentity, unknownMethod())
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
			//
			// 405, NOT 403 (go-qrdb#185). Failing closed is right and has not
			// changed -- the handler is still never reached. What changed is
			// the claim the response makes. A 403 "route not authorized"
			// asserts an AUTHORIZATION decision about a route that was never
			// registered, and tells the caller their credentials or grants are
			// at fault when the VERB is. That misdirection has a measured
			// cost: an operator probing with PUT (the API's write verb is
			// POST) read it as a permissions defect and filed
			// go-quicbaas#270 against the frozen route-permission table -- a
			// fix that would have been inert, because that table only
			// decorates routes that exist. The whole investigation was spent
			// disproving a diagnosis this message supplied.
			//
			// The Allow header is mandatory on a 405 (RFC 9110 §15.5.6), and
			// it is safe here: this branch is downstream of authentication, so
			// only an authenticated caller ever sees which verbs a path
			// serves. The authenticated-but-unauthorized case below keeps 403
			// and its wording.
			if allowed := routes.MethodsForPath(r.URL.Path); len(allowed) > 0 {
				allow := strings.Join(allowed, ", ")
				w.Header().Set("Allow", allow)
				writeJSONError(w, http.StatusMethodNotAllowed,
					"method not allowed: "+r.Method+" is not supported for "+
						r.URL.Path+" (supported: "+allow+")")
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
		case "unauthenticated", "no_peer_cert", "invalid_cert", "cert_revoked":
			return http.StatusUnauthorized
		}
	}

	var unavailable *StoreUnavailableError
	if errors.As(err, &unavailable) {
		return http.StatusServiceUnavailable
	}

	// 503, not 401: the certificate may be perfectly good and this node could
	// not read revocation state. Telling a legitimate caller its credential is
	// bad would send it to re-enrol; telling it to retry is the truth.
	var revocationErr *auth.RevocationCheckError
	if errors.As(err, &revocationErr) {
		return http.StatusServiceUnavailable
	}

	var disabled *UserDisabledError
	if errors.As(err, &disabled) {
		return http.StatusForbidden
	}

	// 403, not 401: the credential is valid and the account is fine — this way
	// in is not one the account may use, and retrying it will never work. A 401
	// would tell the client to present credentials again.
	var badMethod *AuthMethodNotPermittedError
	if errors.As(err, &badMethod) {
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
