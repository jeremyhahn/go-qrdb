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

package apimodule_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// recordedRoute is one registration observed by the fake registrar helpers.
type recordedRoute struct {
	method  string
	pattern string
	perm    permission.Permission
	public  bool
}

// newRecordingRegistrar builds an HTTPRegistrar over a real chi router whose
// registration helpers record what a module declared, which is what the
// server's authorization registry is built from.
func newRecordingRegistrar() (*apimodule.HTTPRegistrar, *[]recordedRoute) {
	var seen []recordedRoute
	r := chi.NewRouter()

	route := func(method, pattern string, perm permission.Permission, _ http.HandlerFunc) {
		seen = append(seen, recordedRoute{method: method, pattern: pattern, perm: perm})
	}
	public := func(method, pattern string, _ http.HandlerFunc) {
		seen = append(seen, recordedRoute{method: method, pattern: pattern, public: true})
	}

	return apimodule.NewHTTPRegistrar(r, route, public), &seen
}

// TestHandleRecordsThePermission covers the authorized registration path. The
// permission is captured at the registration site precisely so the server's
// default-deny startup assertion can see it; a Handle that dropped it would
// leave the route unauthorized rather than merely undocumented.
func TestHandleRecordsThePermission(t *testing.T) {
	reg, seen := newRecordingRegistrar()

	reg.Handle(http.MethodPost, "/v1/things", permission.KVPut, func(http.ResponseWriter, *http.Request) {})

	if len(*seen) != 1 {
		t.Fatalf("recorded %d routes, want 1", len(*seen))
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.pattern != "/v1/things" {
		t.Errorf("recorded %s %s, want POST /v1/things", got.method, got.pattern)
	}
	if got.perm != permission.KVPut {
		t.Errorf("permission = %v, want %v", got.perm, permission.KVPut)
	}
	if got.public {
		t.Error("Handle must not register a public route")
	}
}

// TestHandlePublicRecordsNoPermission covers the unauthenticated path, which
// exists for genuine first-contact surfaces. It must route through the
// server's own public helper rather than the chi router directly, or the
// startup assertion would flag it as an unregistered route.
func TestHandlePublicRecordsNoPermission(t *testing.T) {
	reg, seen := newRecordingRegistrar()

	reg.HandlePublic(http.MethodGet, "/healthz", func(http.ResponseWriter, *http.Request) {})

	if len(*seen) != 1 {
		t.Fatalf("recorded %d routes, want 1", len(*seen))
	}
	got := (*seen)[0]
	if !got.public {
		t.Error("HandlePublic must register through the public helper")
	}
	if got.method != http.MethodGet || got.pattern != "/healthz" {
		t.Errorf("recorded %s %s, want GET /healthz", got.method, got.pattern)
	}
}

// TestRegistrarExposesTheLiveRouter pins that the embedded chi.Router is the
// real one. Modules are documented as able to build subrouters and attach chi
// middleware through it, which only works if it is not a copy.
func TestRegistrarExposesTheLiveRouter(t *testing.T) {
	router := chi.NewRouter()
	reg := apimodule.NewHTTPRegistrar(router, nil, nil)

	if reg.Router == nil {
		t.Fatal("registrar must expose a router")
	}
	reg.Router.Get("/direct", func(http.ResponseWriter, *http.Request) {})

	var found bool
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && route == "/direct" {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("a route added through the embedded router did not reach the underlying router")
	}
}

// nameOnlyModule embeds BaseModule and supplies only Name, which is the
// documented minimum a module must implement.
type nameOnlyModule struct {
	apimodule.BaseModule
}

func (nameOnlyModule) Name() string { return "name-only" }

// TestBaseModuleSatisfiesServiceModule is a compile-and-run check that
// embedding BaseModule plus a Name is genuinely sufficient. If BaseModule ever
// stops covering a hook, this fails to compile -- which is the point.
func TestBaseModuleSatisfiesServiceModule(t *testing.T) {
	var mod apimodule.ServiceModule = nameOnlyModule{}

	if mod.Name() != "name-only" {
		t.Fatalf("Name = %q, want %q", mod.Name(), "name-only")
	}
}

// TestBaseModuleHooksAreHarmlessNoOps covers every default hook. The server
// calls all of them on every module, so a default that panicked or errored
// would break any module that did not override it.
func TestBaseModuleHooksAreHarmlessNoOps(t *testing.T) {
	mod := nameOnlyModule{}
	ctx := context.Background()

	// Each hook receives the zero value the server would pass if the
	// corresponding transport were disabled.
	mod.RegisterGRPC(nil)
	mod.RegisterHTTP(nil)
	mod.RegisterMCP(nil)

	if err := mod.Start(ctx); err != nil {
		t.Errorf("Start returned %v, want nil", err)
	}
	if err := mod.Stop(ctx); err != nil {
		t.Errorf("Stop returned %v, want nil", err)
	}
}

// TestBaseModuleHooksAcceptRealArguments is the counterpart: the no-ops must
// also tolerate genuine transport objects, not merely nil.
func TestBaseModuleHooksAcceptRealArguments(t *testing.T) {
	mod := nameOnlyModule{}
	reg, _ := newRecordingRegistrar()

	var grpcServer grpc.ServiceRegistrar = grpc.NewServer()
	mod.RegisterGRPC(grpcServer)
	mod.RegisterHTTP(reg)
	mod.RegisterMCP(mcpserver.NewMCPServer("test", "0.0.1"))
}

// permissionedModule declares MCP tool permissions, exercising the optional
// extension interface.
type permissionedModule struct {
	apimodule.BaseModule
}

func (permissionedModule) Name() string { return "permissioned" }

func (permissionedModule) MCPToolPermissions() map[string]permission.Permission {
	return map[string]permission.Permission{
		"thing_read":  permission.KVGet,
		"thing_write": permission.KVPut,
	}
}

// TestMCPToolPermissionerIsOptional pins the extension's shape: a module that
// declares tool permissions is detected by type assertion, and one that does
// not is simply absent rather than an error. A tool with no declaration stays
// fail-closed, so the distinction decides whether a module's MCP tools are
// callable at all.
func TestMCPToolPermissionerIsOptional(t *testing.T) {
	var withDecls apimodule.ServiceModule = permissionedModule{}
	var withoutDecls apimodule.ServiceModule = nameOnlyModule{}

	p, ok := withDecls.(apimodule.MCPToolPermissioner)
	if !ok {
		t.Fatal("a module declaring MCPToolPermissions must satisfy MCPToolPermissioner")
	}
	perms := p.MCPToolPermissions()
	if perms["thing_read"] != permission.KVGet {
		t.Errorf("thing_read = %v, want %v", perms["thing_read"], permission.KVGet)
	}
	if perms["thing_write"] != permission.KVPut {
		t.Errorf("thing_write = %v, want %v", perms["thing_write"], permission.KVPut)
	}

	if _, ok := withoutDecls.(apimodule.MCPToolPermissioner); ok {
		t.Error("a module without MCPToolPermissions must not satisfy MCPToolPermissioner")
	}
}

// grpcPublicModule declares public gRPC methods, exercising the optional
// GRPCPublicMethodLister extension interface.
type grpcPublicModule struct {
	apimodule.BaseModule
}

func (grpcPublicModule) Name() string { return "grpc-public" }

func (grpcPublicModule) PublicGRPCMethods() []string {
	return []string{
		"/quicbaas.v1.HealthService/Check",
		"/quicbaas.v1.HealthService/Version",
	}
}

// TestGRPCPublicMethodListerIsOptional pins the extension's shape: a module
// that declares public gRPC methods is detected by type assertion, and one
// that does not is simply absent rather than an error. A gRPC method with no
// declaration stays auth-gated, so the distinction decides whether the RPC is
// reachable without credentials at all.
func TestGRPCPublicMethodListerIsOptional(t *testing.T) {
	var withDecls apimodule.ServiceModule = grpcPublicModule{}
	var withoutDecls apimodule.ServiceModule = nameOnlyModule{}

	l, ok := withDecls.(apimodule.GRPCPublicMethodLister)
	if !ok {
		t.Fatal("a module declaring PublicGRPCMethods must satisfy GRPCPublicMethodLister")
	}
	methods := l.PublicGRPCMethods()
	if len(methods) != 2 {
		t.Fatalf("PublicGRPCMethods returned %d methods, want 2", len(methods))
	}
	if methods[0] != "/quicbaas.v1.HealthService/Check" {
		t.Errorf("methods[0] = %q, want %q", methods[0], "/quicbaas.v1.HealthService/Check")
	}
	if methods[1] != "/quicbaas.v1.HealthService/Version" {
		t.Errorf("methods[1] = %q, want %q", methods[1], "/quicbaas.v1.HealthService/Version")
	}

	if _, ok := withoutDecls.(apimodule.GRPCPublicMethodLister); ok {
		t.Error("a module without PublicGRPCMethods must not satisfy GRPCPublicMethodLister")
	}
}
