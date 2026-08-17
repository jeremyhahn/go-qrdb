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
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// RESTRouteKind classifies how a registered REST route is authorized.
type RESTRouteKind int

const (
	// RESTRoutePermission routes require the caller to hold the registered
	// permission (or, for the permission.IdentityGated sentinel, an
	// authenticated identity; the handler enforces the real binding check).
	RESTRoutePermission RESTRouteKind = iota

	// RESTRoutePublic routes are reachable without authentication. They must
	// ALSO appear in RESTMiddleware's pre-authentication exact-path allowlist;
	// the registry marker exists so the route-registration site declares the
	// exemption explicitly and the server's default-deny startup assertion can
	// account for the route.
	RESTRoutePublic

	// RESTRouteAuthenticated routes require an authenticated caller but no
	// specific permission (e.g. /metrics: any authenticated peer may scrape).
	RESTRouteAuthenticated
)

// RESTAuthFallback authenticates a request whose credentials qrdb's own
// authenticators do not understand (a capability module's session tokens).
// The unified server's REST middleware invokes it ONLY when qrdb's
// authenticator chain produced no identity for a request that matched one
// of the fallback owner's routes; a returned identity is finalized and
// authorized by the SAME enrichment and authorizer path as an mTLS
// identity — roles are resolved from the store, never trusted from the
// token. Implementations must fail closed: (nil, error) for anything they
// cannot positively authenticate.
type RESTAuthFallback interface {
	AuthenticateHTTP(r *http.Request) (*auth.Identity, error)
}

// restRouteEntry records the authorization contract of one registered route.
type restRouteEntry struct {
	kind     RESTRouteKind
	perm     permission.Permission
	fallback RESTAuthFallback
}

// RESTRouteRegistry is the single source of truth for REST route
// authorization, populated at route-registration time by the REST server's
// registration helpers. It replaces the old hand-maintained
// restPermissions / restPathParamPermissions tables: every route declares its
// permission (or an explicit public marker) exactly once, at the site where
// the route is registered on the router.
//
// Pattern matching is delegated to a shadow chi.Mux so templated routes
// ("/users/{id}", "/kv/*") resolve with chi's own most-specific-wins trie —
// the same matcher that routes the request — rather than a hand-rolled
// pattern walker.
//
// The registry is populated during server construction and is read-only
// afterwards; lookups are safe for concurrent use once registration is done.
type RESTRouteRegistry struct {
	matcher *chi.Mux
	entries map[string]restRouteEntry // "METHOD PATTERN" -> entry
}

// NewRESTRouteRegistry creates an empty route registry.
func NewRESTRouteRegistry() *RESTRouteRegistry {
	return &RESTRouteRegistry{
		matcher: chi.NewRouter(),
		entries: make(map[string]restRouteEntry),
	}
}

// restRouteNoop is the placeholder handler registered on the shadow matcher;
// the matcher is only ever used for pattern resolution, never for serving.
func restRouteNoop(http.ResponseWriter, *http.Request) {}

// assertNotRegistered panics with a descriptive message when (method,
// pattern) is already registered. Route collisions are construction-time
// programmer errors — two modules (or a module and the built-in surface)
// claiming the same route must fail startup loudly and by name, not
// silently shadow each other. The panic-at-construction posture matches
// the default-deny startup assertion.
func (rr *RESTRouteRegistry) assertNotRegistered(method, pattern string) {
	if _, exists := rr.entries[method+" "+pattern]; exists {
		panic("middleware: duplicate REST route registration: " + method + " " + pattern +
			" is already registered (module/route collision)")
	}
}

// Register declares that (method, pattern) requires perm. The pattern uses
// chi syntax ("/users/{id}", "/kv/*"). Registering the same method+pattern
// twice panics with a descriptive collision message.
func (rr *RESTRouteRegistry) Register(method, pattern string, perm permission.Permission) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[method+" "+pattern] = restRouteEntry{kind: RESTRoutePermission, perm: perm}
}

// RegisterPublic declares that (method, pattern) is public: no authentication
// and no permission. The route must also be on RESTMiddleware's
// pre-authentication allowlist; this marker makes the exemption explicit at
// the registration site for the default-deny startup assertion.
func (rr *RESTRouteRegistry) RegisterPublic(method, pattern string) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[method+" "+pattern] = restRouteEntry{kind: RESTRoutePublic}
}

// RegisterAuthenticated declares that (method, pattern) requires an
// authenticated caller but no specific permission (e.g. /metrics).
func (rr *RESTRouteRegistry) RegisterAuthenticated(method, pattern string) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[method+" "+pattern] = restRouteEntry{kind: RESTRouteAuthenticated}
}

// Lookup resolves the authorization contract for a concrete request
// (method, path). It routes the path through the shadow chi matcher and
// returns the registered entry for the matched pattern. ok is false when no
// registered route matches the method+path.
func (rr *RESTRouteRegistry) Lookup(method, path string) (permission.Permission, RESTRouteKind, bool) {
	if rr == nil {
		return "", 0, false
	}
	rctx := chi.NewRouteContext()
	pattern := rr.matcher.Find(rctx, method, path)
	if pattern == "" {
		return "", 0, false
	}
	e, ok := rr.entries[method+" "+pattern]
	if !ok {
		return "", 0, false
	}
	return e.perm, e.kind, true
}

// restMethodProbeOrder is the canonical order in which methods are probed
// when computing the set of methods registered for a path (e.g. to build a
// 405 Allow header). The order mirrors the Allow strings the legacy
// dispatcher emitted ("GET, PUT, DELETE", "GET, POST", ...).
var restMethodProbeOrder = []string{
	http.MethodGet,
	http.MethodPut,
	http.MethodPost,
	http.MethodDelete,
	http.MethodPatch,
	http.MethodHead,
	http.MethodOptions,
	http.MethodConnect,
	http.MethodTrace,
}

// PathHasRoutes reports whether any registered route matches path under any
// HTTP method. RESTMiddleware uses it for default-deny: a request whose
// method does not match a registered route, but whose path belongs to the
// API surface, is rejected rather than passed through.
func (rr *RESTRouteRegistry) PathHasRoutes(path string) bool {
	if rr == nil {
		return false
	}
	for _, m := range restMethodProbeOrder {
		rctx := chi.NewRouteContext()
		if rr.matcher.Find(rctx, m, path) != "" {
			return true
		}
	}
	return false
}

// Covered reports whether (method, pattern) was registered with a permission
// or an explicit public/authenticated marker. The server's default-deny
// startup assertion walks the serving router and fails construction for any
// route this registry does not cover.
func (rr *RESTRouteRegistry) Covered(method, pattern string) bool {
	if rr == nil {
		return false
	}
	_, ok := rr.entries[method+" "+pattern]
	return ok
}

// SetFallback attaches a module authentication fallback to an
// already-registered route. The unified server calls this for every route a
// fallback-bearing module registers, at registration time. Attaching a
// fallback to an unregistered route is a programmer error and panics, the
// same construction-time posture as a route collision.
func (rr *RESTRouteRegistry) SetFallback(method, pattern string, fb RESTAuthFallback) {
	key := method + " " + pattern
	e, ok := rr.entries[key]
	if !ok {
		panic("middleware: SetFallback on unregistered route: " + key)
	}
	e.fallback = fb
	rr.entries[key] = e
}

// FallbackFor resolves the module authentication fallback for a concrete
// request, or nil when the matched route has none.
func (rr *RESTRouteRegistry) FallbackFor(method, path string) RESTAuthFallback {
	if rr == nil {
		return nil
	}
	rctx := chi.NewRouteContext()
	pattern := rr.matcher.Find(rctx, method, path)
	if pattern == "" {
		return nil
	}
	return rr.entries[method+" "+pattern].fallback
}
