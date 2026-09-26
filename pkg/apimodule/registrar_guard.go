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

package apimodule

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
)

// RouteClaim names one REST route a module registered: its HTTP method and
// its chi pattern. Method is AnyMethod for a registration that claims every
// method (chi's Handle / HandleFunc).
type RouteClaim struct {
	Method  string
	Pattern string
}

// AnyMethod is the RouteClaim.Method of a registration that claims every HTTP
// method on its pattern.
const AnyMethod = "*"

// RegistrationClosedError is the panic value of a route registered through an
// HTTPRegistrar after the server finished constructing it (go-qrdb#194).
//
// The server validates a module's routes -- collisions, the default-deny
// assertion -- once, after RegisterHTTP returns. A module that kept the
// registrar, or the router embedded in it, and registered from Start or a
// goroutine would present a surface nobody validated, on a router that is
// already serving. It is a programmer error of the same class as a route
// collision, so it panics the same way.
type RegistrationClosedError struct {
	Method  string
	Pattern string
}

// Error names the late registration.
func (e *RegistrationClosedError) Error() string {
	return "apimodule: REST route " + e.Method + " " + e.Pattern +
		" registered after the server finished route registration; register routes only inside RegisterHTTP"
}

// UnsupportedRouterMethodError is the panic value of a chi.Router method a
// module may not call on the registrar's router (go-qrdb#194): Mount, Route,
// NotFound, MethodNotAllowed and Use.
//
// Each one serves requests the permission registry cannot see. Mount and Route
// overwrite every method's handler at the mount pattern and hide it from chi's
// route listing; a mounted router's NotFound or MethodNotAllowed handler, and
// the router's own, answer every unmatched path beneath it; Use runs
// middleware ahead of routing for every route on the server, built-ins
// included. Any of them lets a module serve a request no registration
// authorized -- probed on the real server as unauthenticated responses under
// /v1/nf/* and at /v1/hidden. Construction therefore fails instead.
//
// Register every route through Handle or HandlePublic with its permission; a
// module that serves a whole subtree registers it as
// Handle(method, "/prefix/*", perm, h).
type UnsupportedRouterMethodError struct {
	Method string
}

// Error names the refused call and the alternative.
func (e *UnsupportedRouterMethodError) Error() string {
	return "apimodule: chi.Router." + e.Method + " is not available to a module: it serves requests " +
		"no permission registration covers; register routes with HTTPRegistrar.Handle or HandlePublic, " +
		"and a subtree as Handle(method, \"/prefix/*\", perm, h)"
}

// routeRecorder is the state one HTTPRegistrar shares with every router it
// hands out: which routes were registered straight on a router rather than
// through Handle / HandlePublic, and whether registration is still open.
type routeRecorder struct {
	closed atomic.Bool

	mu     sync.Mutex
	direct []RouteClaim
}

// admit panics with *RegistrationClosedError once registration is closed.
func (rec *routeRecorder) admit(method, pattern string) {
	if rec.closed.Load() {
		panic(&RegistrationClosedError{Method: method, Pattern: pattern})
	}
}

// recordDirect admits and records a registration made on the embedded router.
func (rec *routeRecorder) recordDirect(method, pattern string) {
	rec.admit(method, pattern)
	rec.mu.Lock()
	rec.direct = append(rec.direct, RouteClaim{Method: method, Pattern: pattern})
	rec.mu.Unlock()
}

// recordingRouter is the chi.Router a module sees through HTTPRegistrar.
//
// Route registrations are forwarded to the live router and RECORDED, so the
// server can refuse one that collides with a route whose permission was
// recorded through Handle / HandlePublic or by a built-in. Without the record
// such a collision is invisible: chi silently replaces the earlier handler,
// the registry still covers the pattern with the earlier registration's
// permission, and the default-deny assertion -- which asks only whether a
// pattern is covered -- passes. With and Group are wrapped so a route on an
// inline router is recorded too.
//
// Mount, Route, NotFound, MethodNotAllowed and Use are REFUSED with
// *UnsupportedRouterMethodError: each serves requests that no route
// registration describes, so no amount of recording could make the registry
// see them.
type recordingRouter struct {
	chi.Router
	rec *routeRecorder
}

var _ chi.Router = (*recordingRouter)(nil)

func (r *recordingRouter) Handle(pattern string, h http.Handler) {
	r.rec.recordDirect(AnyMethod, pattern)
	r.Router.Handle(pattern, h)
}

func (r *recordingRouter) HandleFunc(pattern string, h http.HandlerFunc) {
	r.rec.recordDirect(AnyMethod, pattern)
	r.Router.HandleFunc(pattern, h)
}

func (r *recordingRouter) Method(method, pattern string, h http.Handler) {
	r.rec.recordDirect(method, pattern)
	r.Router.Method(method, pattern, h)
}

func (r *recordingRouter) MethodFunc(method, pattern string, h http.HandlerFunc) {
	r.rec.recordDirect(method, pattern)
	r.Router.MethodFunc(method, pattern, h)
}

func (r *recordingRouter) Connect(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodConnect, pattern, h)
}

func (r *recordingRouter) Delete(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodDelete, pattern, h)
}

func (r *recordingRouter) Get(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodGet, pattern, h)
}

func (r *recordingRouter) Head(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodHead, pattern, h)
}

func (r *recordingRouter) Options(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodOptions, pattern, h)
}

func (r *recordingRouter) Patch(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodPatch, pattern, h)
}

func (r *recordingRouter) Post(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodPost, pattern, h)
}

func (r *recordingRouter) Put(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodPut, pattern, h)
}

func (r *recordingRouter) Trace(pattern string, h http.HandlerFunc) {
	r.MethodFunc(http.MethodTrace, pattern, h)
}

// With returns an inline router that records like its parent. Its middleware
// applies only to the routes registered through it, each of which is recorded
// and must be covered by a permission.
func (r *recordingRouter) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return &recordingRouter{Router: r.Router.With(middlewares...), rec: r.rec}
}

// Group runs fn over an inline router that records like its parent. It
// mirrors chi's own Group, which is With plus a call.
func (r *recordingRouter) Group(fn func(chi.Router)) chi.Router {
	im := r.With()
	if fn != nil {
		fn(im)
	}
	return im
}

// Mount is refused; see UnsupportedRouterMethodError.
func (r *recordingRouter) Mount(string, http.Handler) {
	panic(&UnsupportedRouterMethodError{Method: "Mount"})
}

// Route is refused; see UnsupportedRouterMethodError.
func (r *recordingRouter) Route(string, func(chi.Router)) chi.Router {
	panic(&UnsupportedRouterMethodError{Method: "Route"})
}

// NotFound is refused; see UnsupportedRouterMethodError.
func (r *recordingRouter) NotFound(http.HandlerFunc) {
	panic(&UnsupportedRouterMethodError{Method: "NotFound"})
}

// MethodNotAllowed is refused; see UnsupportedRouterMethodError.
func (r *recordingRouter) MethodNotAllowed(http.HandlerFunc) {
	panic(&UnsupportedRouterMethodError{Method: "MethodNotAllowed"})
}

// Use is refused; see UnsupportedRouterMethodError.
func (r *recordingRouter) Use(...func(http.Handler) http.Handler) {
	panic(&UnsupportedRouterMethodError{Method: "Use"})
}
