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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

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

	// pattern is the spelling the route was first registered under. The key
	// the entry is filed under is canonical (see RESTRouteKey), so a
	// collision is reported with both spellings.
	pattern string

	// target, when set, extracts the tenant a request names (SetTarget).
	target RESTTargetFunc
}

// RESTRouteKey is the identity of a REST route: its method and its pattern
// with every path-parameter NAME erased. Two patterns that differ only in what
// a parameter is called -- "/a/{id}" and "/a/{deviceID}" -- are the same route
// to chi, which silently replaces the first handler with the second, so they
// must be the same key here or the collision guard cannot see the collision
// (go-qrdb#194). A parameter's regular-expression constraint is KEPT, because
// chi does route on it: "/a/{id:[0-9]+}" and "/a/{id}" are different routes.
func RESTRouteKey(method, pattern string) string {
	return method + " " + canonicalRESTPattern(pattern)
}

// canonicalRESTPattern erases path-parameter names from a chi pattern,
// keeping any regexp constraint: "{id}" becomes "{}", "{id:[0-9]{3}}" becomes
// "{:[0-9]{3}}". Braces are matched by depth, as chi matches them, so a
// quantifier inside a constraint does not end the parameter early. An
// unterminated brace is left as written; chi rejects such a pattern at
// registration.
func canonicalRESTPattern(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			b.WriteByte(pattern[i])
			continue
		}
		end := matchingBrace(pattern, i)
		if end < 0 {
			b.WriteString(pattern[i:])
			break
		}
		param := pattern[i+1 : end]
		b.WriteByte('{')
		if colon := strings.IndexByte(param, ':'); colon >= 0 {
			b.WriteString(param[colon:])
		}
		b.WriteByte('}')
		i = end
	}
	return b.String()
}

// matchingBrace returns the index of the '}' closing the '{' at open, or -1.
func matchingBrace(pattern string, open int) int {
	depth := 0
	for j := open; j < len(pattern); j++ {
		switch pattern[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// RESTRouteCollisionError is the panic value of a REST route registered twice:
// two registrations whose method and pattern are the same route to chi, even
// when the patterns spell a parameter differently. It is a construction-time
// programmer error, the same posture as the default-deny startup assertion:
// the alternative is chi silently serving whichever handler registered last,
// under whichever permission registered first.
type RESTRouteCollisionError struct {
	Method string

	// Pattern is the spelling of the refused registration; Existing is the
	// spelling it collided with.
	Pattern  string
	Existing string
}

// Error names both spellings.
func (e *RESTRouteCollisionError) Error() string {
	return "middleware: duplicate REST route registration: " + e.Method + " " + e.Pattern +
		" is the same route as the already-registered " + e.Method + " " + e.Existing +
		" (module/route collision)"
}

// RESTTargetFunc extracts the Target a REST request names. param resolves
// the matched route's path parameters ("id" for "/users/{id}"). A path or
// query value it cannot parse names the caller's own tenant; the handler
// parses the same value and rejects it itself. A body it cannot read is an
// error — *TargetBodyInvalidError or *TargetBodyTooLargeError — and
// RESTMiddleware refuses the request rather than authorizing it against a
// tenant the body may not name.
type RESTTargetFunc func(r *http.Request, param func(name string) string) (Target, error)

// SetTarget attaches a target extractor to an already-registered permission
// route, so RESTMiddleware authorizes the route's permission against the
// tenant the request names rather than the caller's own (see Target).
// Attaching one to an unregistered route is a programmer error and panics,
// the same construction-time posture as SetFallback.
func (rr *RESTRouteRegistry) SetTarget(method, pattern string, fn RESTTargetFunc) {
	key := RESTRouteKey(method, pattern)
	e, ok := rr.entries[key]
	if !ok {
		panic("middleware: SetTarget on unregistered route: " + method + " " + pattern)
	}
	e.target = fn
	rr.entries[key] = e
}

// targetFor returns the Target r names under its matched route's extractor,
// or the caller's own tenant when the route declares none.
func (rr *RESTRouteRegistry) targetFor(r *http.Request) (Target, error) {
	if rr == nil {
		return SelfTarget(), nil
	}
	rctx := chi.NewRouteContext()
	pattern := rr.matcher.Find(rctx, r.Method, r.URL.Path)
	e, ok := rr.entries[RESTRouteKey(r.Method, pattern)]
	if !ok || e.target == nil {
		return SelfTarget(), nil
	}
	return e.target(r, rctx.URLParam)
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

	// modulePrefixes are the subtrees capability modules' routes claim; see
	// UnderModulePrefix.
	modulePrefixes []string
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
//
// The comparison is on RESTRouteKey, so a renamed path parameter is the
// duplicate it is to chi.
func (rr *RESTRouteRegistry) assertNotRegistered(method, pattern string) {
	if e, exists := rr.entries[RESTRouteKey(method, pattern)]; exists {
		panic(&RESTRouteCollisionError{Method: method, Pattern: pattern, Existing: e.pattern})
	}
}

// Register declares that (method, pattern) requires perm. The pattern uses
// chi syntax ("/users/{id}", "/kv/*"). Registering the same method+pattern
// twice panics with a descriptive collision message.
func (rr *RESTRouteRegistry) Register(method, pattern string, perm permission.Permission) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[RESTRouteKey(method, pattern)] = restRouteEntry{kind: RESTRoutePermission, perm: perm, pattern: pattern}
}

// RegisterPublic declares that (method, pattern) is public: no authentication
// and no permission. The route must also be on RESTMiddleware's
// pre-authentication allowlist; this marker makes the exemption explicit at
// the registration site for the default-deny startup assertion.
func (rr *RESTRouteRegistry) RegisterPublic(method, pattern string) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[RESTRouteKey(method, pattern)] = restRouteEntry{kind: RESTRoutePublic, pattern: pattern}
}

// RegisterAuthenticated declares that (method, pattern) requires an
// authenticated caller but no specific permission (e.g. /metrics).
func (rr *RESTRouteRegistry) RegisterAuthenticated(method, pattern string) {
	rr.assertNotRegistered(method, pattern)
	rr.matcher.Method(method, pattern, http.HandlerFunc(restRouteNoop))
	rr.entries[RESTRouteKey(method, pattern)] = restRouteEntry{kind: RESTRouteAuthenticated, pattern: pattern}
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
	e, ok := rr.entries[RESTRouteKey(method, pattern)]
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
//
// It short-circuits on the first match rather than delegating to
// MethodsForPath, because the common case is a path that is on the API
// surface and the first probe answers it.
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

// MethodsForPath returns every HTTP method registered for path, in
// restMethodProbeOrder, or nil when the path is outside the registered API
// surface.
//
// This is what a 405 response's Allow header is built from, which RFC 9110
// §15.5.6 makes MANDATORY on a 405 rather than a nicety -- so the "if cheap"
// framing in go-qrdb#185 understates it: without this the middleware cannot
// answer 405 correctly at all.
//
// The result is only ever disclosed to a caller that has already
// authenticated: the sole call site sits after the middleware's
// authentication step, so an anonymous caller still gets 401 and learns
// nothing about which verbs a path serves.
func (rr *RESTRouteRegistry) MethodsForPath(path string) []string {
	if rr == nil {
		return nil
	}
	var methods []string
	for _, m := range restMethodProbeOrder {
		rctx := chi.NewRouteContext()
		if rr.matcher.Find(rctx, m, path) != "" {
			methods = append(methods, m)
		}
	}
	return methods
}

// Covered reports whether (method, pattern) was registered with a permission
// or an explicit public/authenticated marker. The server's default-deny
// startup assertion walks the serving router and fails construction for any
// route this registry does not cover.
func (rr *RESTRouteRegistry) Covered(method, pattern string) bool {
	if rr == nil {
		return false
	}
	_, ok := rr.entries[RESTRouteKey(method, pattern)]
	return ok
}

// RegisterModulePrefix records a subtree a capability module's routes claim
// ("/v1/devices/" for "/v1/devices/{id}"). An empty prefix is ignored.
func (rr *RESTRouteRegistry) RegisterModulePrefix(prefix string) {
	if prefix == "" {
		return
	}
	for _, p := range rr.modulePrefixes {
		if p == prefix {
			return
		}
	}
	rr.modulePrefixes = append(rr.modulePrefixes, prefix)
}

// UnderModulePrefix reports whether path lies in a subtree a module claimed.
// RESTMiddleware refuses such a path when no registration matches it, rather
// than handing it to the router's fallback: defence in depth under the
// registrar's refusal of NotFound, MethodNotAllowed, Mount, Route and Use
// (go-qrdb#194), so a module subtree answers only through its registered
// routes whatever else changes around it.
func (rr *RESTRouteRegistry) UnderModulePrefix(path string) bool {
	if rr == nil {
		return false
	}
	for _, p := range rr.modulePrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// SetFallback attaches a module authentication fallback to an
// already-registered route. The unified server calls this for every route a
// fallback-bearing module registers, at registration time. Attaching a
// fallback to an unregistered route is a programmer error and panics, the
// same construction-time posture as a route collision.
func (rr *RESTRouteRegistry) SetFallback(method, pattern string, fb RESTAuthFallback) {
	key := RESTRouteKey(method, pattern)
	e, ok := rr.entries[key]
	if !ok {
		panic("middleware: SetFallback on unregistered route: " + method + " " + pattern)
	}
	e.fallback = fb
	rr.entries[key] = e
}

// FallbackFor resolves the module authentication fallback for a concrete
// request, or nil when none applies.
//
// EXACT MATCH FIRST. A route registered for this method answers with its own
// fallback, whatever that is — including nil. A registered route whose owner
// declared no fallback keeps the original authentication failure; widening
// there would let one module's auth plane establish the identity that a
// DIFFERENT owner's handler is then authorized and dispatched for.
//
// WIDEN TO THE PATH when the method has no registered route at all. A module
// owns the path's auth plane, not one verb of it, so a caller the module can
// authenticate should not be told at 401 that its credentials are the problem
// when the real defect is its verb (go-qrdb#211). This widening is safe
// precisely because of the condition that triggers it: a request whose method
// matched no route cannot reach a handler and cannot reach the authorizer —
// Lookup misses for the same (method, path) a moment later, so the only
// outcomes downstream are the 405 + Allow the caller needed to see, or a 401
// if the fallback also rejects. The identity a widened fallback produces is
// therefore never used to decide anything.
//
// The disclosure order is unchanged: the 405 still sits below authentication,
// so an anonymous caller — and one whose module token the fallback rejects —
// still gets 401 with no Allow header and learns nothing about the path's
// verbs.
func (rr *RESTRouteRegistry) FallbackFor(method, path string) RESTAuthFallback {
	if rr == nil {
		return nil
	}
	rctx := chi.NewRouteContext()
	if pattern := rr.matcher.Find(rctx, method, path); pattern != "" {
		return rr.entries[RESTRouteKey(method, pattern)].fallback
	}
	return rr.pathFallback(path)
}

// pathFallback resolves the single fallback that owns path, probing the same
// restMethodProbeOrder PathHasRoutes and MethodsForPath use rather than adding
// a third probe loop. It returns nil when no registered method on the path
// carries a fallback, and also when two of them carry DIFFERENT ones.
//
// The ambiguous case is real, not hypothetical: entries is keyed per METHOD
// (RESTRouteKey) and the collision guard is keyed the same way, so two
// modules may split the verbs of one path between them and bring one auth
// plane each. Which of them owns a verb that neither registered is not a
// question the registry can answer, so it answers none and the caller keeps
// the 401 it got before — the conservative half of a change whose entire
// benefit is a clearer status code.
func (rr *RESTRouteRegistry) pathFallback(path string) RESTAuthFallback {
	var found RESTAuthFallback
	for _, m := range restMethodProbeOrder {
		rctx := chi.NewRouteContext()
		pattern := rr.matcher.Find(rctx, m, path)
		if pattern == "" {
			continue
		}
		fb := rr.entries[RESTRouteKey(m, pattern)].fallback
		if fb == nil {
			continue
		}
		if found != nil && !sameFallback(found, fb) {
			return nil
		}
		found = fb
	}
	return found
}

// sameFallback reports whether two fallbacks are the same registration.
//
// It cannot simply use ==: comparing two interface values whose dynamic type
// is identical and non-comparable panics at run time (Go spec, comparison
// operators), and RESTAuthFallback is satisfied by whatever a module hands
// over — a struct value with a map field is enough to trigger it. A type it
// cannot prove equal is reported as different, which routes pathFallback to
// its conservative nil rather than to a panic in the middleware.
func sameFallback(a, b RESTAuthFallback) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

// RESTTargetBodyLimit bounds the body RESTBody reads to find a request's
// target. A targeted route's body is a small IAM request; one larger than this
// is refused with *TargetBodyTooLargeError rather than read past it.
const RESTTargetBodyLimit = 1 << 20

// RESTPathUser targets the user whose id is the path parameter name.
func RESTPathUser(name string) RESTTargetFunc {
	return func(_ *http.Request, param func(string) string) (Target, error) {
		id, err := strconv.ParseUint(param(name), 10, 64)
		if err != nil {
			return SelfTarget(), nil
		}
		return UserTarget(id), nil
	}
}

// RESTPathRole targets the role whose id is the path parameter name.
func RESTPathRole(name string) RESTTargetFunc {
	return func(_ *http.Request, param func(string) string) (Target, error) {
		return RoleTarget(param(name)), nil
	}
}

// RESTPathTenant targets the tenant named by the path parameter name.
func RESTPathTenant(name string) RESTTargetFunc {
	return func(_ *http.Request, param func(string) string) (Target, error) {
		return TenantTarget(param(name)), nil
	}
}

// RESTQueryTenant targets the tenant named by the query parameter name.
func RESTQueryTenant(name string) RESTTargetFunc {
	return func(r *http.Request, _ func(string) string) (Target, error) {
		return TenantTarget(r.URL.Query().Get(name)), nil
	}
}

// RESTBody targets what get reads from the request body decoded into T.
//
// T must be the HANDLER'S OWN request type, decoded the way the handler
// decodes it (encoding/json, first value): the route check then authorizes
// exactly the value the handler acts on. Reading a field out of a generic map
// instead is not the same decode — a struct field matches its key
// case-insensitively and the last occurrence wins, so a body carrying
// "user_id" and "USER_ID" would be authorized on one and acted on with the
// other.
//
// The whole body is read, up to RESTTargetBodyLimit, and handed back to the
// handler byte for byte. An empty body names the caller's own tenant, as
// every targeted handler treats it; a body that does not decode is
// *TargetBodyInvalidError and one past the limit *TargetBodyTooLargeError.
func RESTBody[T any](get func(*T) Target) RESTTargetFunc {
	return func(r *http.Request, _ func(string) string) (Target, error) {
		body, err := readTargetBody(r)
		if err != nil {
			return SelfTarget(), err
		}
		var req T
		if derr := json.NewDecoder(bytes.NewReader(body)).Decode(&req); derr != nil {
			if errors.Is(derr, io.EOF) {
				return SelfTarget(), nil
			}
			return SelfTarget(), &TargetBodyInvalidError{Err: derr}
		}
		return get(&req), nil
	}
}

// readTargetBody reads r's whole body under RESTTargetBodyLimit and replaces
// it with the bytes read, so the handler reads exactly what was sent.
func readTargetBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, RESTTargetBodyLimit+1))
	r.Body = restoredBody{Reader: bytes.NewReader(body), Closer: r.Body}
	if err != nil {
		return nil, &TargetBodyInvalidError{Err: err}
	}
	if int64(len(body)) > RESTTargetBodyLimit {
		return nil, &TargetBodyTooLargeError{Limit: RESTTargetBodyLimit}
	}
	return body, nil
}

// restoredBody is a request body replaced by the bytes already read from it;
// closing it closes the original body.
type restoredBody struct {
	io.Reader
	io.Closer
}
