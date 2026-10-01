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

package sdk

import (
	restapi "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
)

// ---------------------------------------------------------------------------
// Embedded qrdb server options
//
// QuicBaaS composes an embedded qrdb server by supplying these functional
// options. Re-exported so the composer never imports pkg/server directly.
// ---------------------------------------------------------------------------

// ServerOption configures an embedded qrdb server at construction time.
type ServerOption = qrdbserver.ServerOption

// WithStrategyRegistry attaches a seal strategy registry to the embedded
// qrdb server.
var WithStrategyRegistry = qrdbserver.WithStrategyRegistry

// WithPINBackend attaches a PIN backend to the embedded qrdb server.
var WithPINBackend = qrdbserver.WithPINBackend

// WithFactorRegistry attaches an auth factor registry to the embedded qrdb
// server.
var WithFactorRegistry = qrdbserver.WithFactorRegistry

// WithCeremonyCA attaches the certificate authority the init ceremony uses to
// issue the TLS serving certificate, sign officer CSRs, and publish the CA
// bundle / SPKI pin. Composers supply go-xkms's CAAdapter — the stack's single
// hardware-backed CA — via sdk/go NewCAAdapter. Without it every CA-dependent
// ceremony verb fails closed with ErrCeremonyCANotInitialized. The argument
// type is the re-exported CeremonyCA (see types_ceremony.go).
var WithCeremonyCA = qrdbserver.WithCeremonyCA

// ---------------------------------------------------------------------------
// Capability modules
//
// A higher layer (e.g. go-xkms) registers its capabilities as a ServiceModule
// via WithModule, instead of standing up a parallel transport stack. The
// module's gRPC/HTTP(REST)/MCP surfaces are exposed by qrdb's own unified
// server, gated by qrdb's authorizer.
// ---------------------------------------------------------------------------

// ServiceModule is a self-contained capability registered into the embedded
// qrdb server via WithModule. See pkg/apimodule for the contract.
type ServiceModule = apimodule.ServiceModule

// BaseModule provides no-op defaults for every ServiceModule hook except Name;
// embed it and override only what applies.
type BaseModule = apimodule.BaseModule

// ModuleHTTPRegistrar is the permission-recording HTTP registration surface the
// unified server passes to ServiceModule.RegisterHTTP. Routes registered via
// Handle declare their permission at the registration site; HandlePublic marks
// genuine first-contact surfaces. It embeds the live chi.Router.
type ModuleHTTPRegistrar = apimodule.HTTPRegistrar

// ModuleMCPToolPermissioner is the optional ServiceModule extension through
// which a module declares its MCP tools' permissions, admitting them into
// the unified server's single MCP authorization path.
type ModuleMCPToolPermissioner = apimodule.MCPToolPermissioner

// ModuleGRPCPublicMethodLister is the optional ServiceModule extension
// through which a module declares which of its gRPC methods (registered via
// RegisterGRPC) must be reachable without authentication, admitting them
// into the unified server's gRPC and unix transports the same way
// HTTPRegistrar.HandlePublic admits unauthenticated REST routes.
type ModuleGRPCPublicMethodLister = apimodule.GRPCPublicMethodLister

// WithModule registers a capability module into the embedded qrdb server. Its
// gRPC/REST/MCP/QUIC surfaces are attached to every transport at startup and
// its Start/Stop lifecycle runs with the server's.
var WithModule = qrdbserver.WithModule

// NewModuleHTTPRegistrar builds the permission-recording registrar the server
// passes to ServiceModule.RegisterHTTP. Exported for module hosts and module
// tests that need a capture registrar without importing pkg/apimodule.
var NewModuleHTTPRegistrar = apimodule.NewHTTPRegistrar

// The REST route-ownership guard (go-qrdb#194). The embedded server refuses,
// at construction, any second claim on a REST route -- a module over a
// built-in or over another module -- and a module route registered directly on
// the router that a permission-recorded route also claims. These are the panic
// values it raises, and the route identity it decides "the same route" by, so
// a host can recover and report them without importing pkg/api/rest.
type (
	// ModuleRouteClaim names one route a module registered directly on the
	// embedded router (ModuleHTTPRegistrar.DirectRoutes).
	ModuleRouteClaim = apimodule.RouteClaim
	// ModuleRegistrationClosedError is the panic of a registration made after
	// the module's RegisterHTTP returned.
	ModuleRegistrationClosedError = apimodule.RegistrationClosedError
	// ModuleUnsupportedRouterMethodError is the panic of a chi.Router method
	// a module may not call on the registrar's router: Mount, Route,
	// NotFound, MethodNotAllowed or Use.
	ModuleUnsupportedRouterMethodError = apimodule.UnsupportedRouterMethodError
	// RESTRouteCollisionError is the panic of a second claim on a REST route.
	RESTRouteCollisionError = restapi.RouteCollisionError
	// RESTDirectRouteCollisionError is the panic of a direct router
	// registration that a permission-recorded registration also claims.
	RESTDirectRouteCollisionError = restapi.DirectRouteCollisionError
)

// RESTRouteKey is the identity of a REST route: method plus pattern with
// path-parameter names erased and regexp constraints kept -- the identity chi
// routes by, and the one the collision guard compares.
var RESTRouteKey = authmw.RESTRouteKey

// WithServerTLS supplies the TLS configuration the embedded server's
// network transports serve with. qrdb transports are TLS-only: without this
// option (and without a CeremonyCA to auto-provision a certificate) the
// server refuses to start a network transport. Module hosts that terminate
// TLS with their own certificate material pass it here.
var WithServerTLS = qrdbserver.WithTLS

// WithRESTListener hands the embedded server a TCP listener the caller already
// bound, which it serves REST on instead of binding server.admin_port. Bind
// ":0" and read the port back to get a kernel-assigned port with no window in
// which another process can take it. The server owns the listener from Start.
var WithRESTListener = qrdbserver.WithRESTListener

// WithRESTPacketConn hands the embedded server the UDP socket to serve HTTP/3
// on; it must sit on the WithRESTListener listener's host and port.
var WithRESTPacketConn = qrdbserver.WithRESTPacketConn

// WithGRPCListener hands the embedded server a TCP listener the caller already
// bound, which it serves gRPC on instead of binding server.grpc_port.
var WithGRPCListener = qrdbserver.WithGRPCListener

// ModuleAuthFallback is the optional ServiceModule extension for capability
// session-token planes: when the unified server's REST middleware finds no
// qrdb identity on a request targeting the module's routes, it invokes the
// owning module's fallback; the returned identity is enriched and
// authorized by the same store-resolved path as an mTLS identity.
type ModuleAuthFallback = apimodule.ModuleAuthFallback
