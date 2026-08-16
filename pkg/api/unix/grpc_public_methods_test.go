//go:build codec_json && raft

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

// Interface-plumbing tests for apimodule.GRPCPublicMethodLister: a module
// declaring public gRPC methods must have those declarations reach the SHARED
// AuthMiddleware during Start, exactly as apimodule.MCPToolPermissioner's
// declarations reach the MCP tool registry.

package unix

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// publicMethodModule declares one public gRPC method, exercising
// apimodule.GRPCPublicMethodLister.
type publicMethodModule struct {
	apimodule.BaseModule
	methods []string
}

func (m *publicMethodModule) Name() string { return "public-method-module" }

func (m *publicMethodModule) PublicGRPCMethods() []string { return m.methods }

// alwaysFailAuthenticator never authenticates a caller. It stands in for "no
// credentials presented at all" so a test proves a declared-public method
// truly bypasses authentication, not merely permission lookup.
type alwaysFailAuthenticator struct{}

func (alwaysFailAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (alwaysFailAuthenticator) Name() string { return "always-fail" }

// TestStartRegistersModulePublicGRPCMethodsOnTheSharedAuthMiddleware is the
// interface-plumbing test: a module's PublicGRPCMethods() declaration must
// reach the AuthMiddleware instance the transport actually authenticates and
// authorizes requests through, so the declared method bypasses auth
// end-to-end and every other method is still gated.
func TestStartRegistersModulePublicGRPCMethodsOnTheSharedAuthMiddleware(t *testing.T) {
	const publicMethod = "/quicbaas.v1.HealthService/Check"
	module := &publicMethodModule{methods: []string{publicMethod}}
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithAuthenticator(alwaysFailAuthenticator{}))

	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithServiceModules([]apimodule.ServiceModule{module}),
		WithAuthMiddleware(mw),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	interceptor := mw.UnaryInterceptor()

	// The declared method bypasses authentication entirely: the configured
	// authenticator always fails, so reaching the handler proves the
	// interceptor never called it for this method.
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: publicMethod}
	if _, err := interceptor(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("declared public method: unexpected error: %v", err)
	}
	if !called {
		t.Error("declared public method must reach the handler without authentication")
	}

	// A method the module did NOT declare stays gated: the failing
	// authenticator denies it.
	otherInfo := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/NotDeclared"}
	_, err := interceptor(context.Background(), nil, otherInfo, func(ctx context.Context, req any) (any, error) {
		t.Fatal("a non-declared method must not reach the handler")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected a non-declared method to be denied")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want %v", got, codes.Unauthenticated)
	}
}

// TestStartFailsWhenModuleDeclaresAMalformedPublicGRPCMethod covers the
// fail-loudly validation: a module's typo in a PublicGRPCMethods() entry
// must fail server startup rather than silently leaving the intended RPC
// auth-gated (or, worse, being ignored).
func TestStartFailsWhenModuleDeclaresAMalformedPublicGRPCMethod(t *testing.T) {
	module := &publicMethodModule{methods: []string{"not-a-full-method-name"}}
	mw := authmw.New(allowAllAuthorizer{})

	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithServiceModules([]apimodule.ServiceModule{module}),
		WithAuthMiddleware(mw),
		WithTLS(selfSignedTLSConfig(t)),
	)

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start must fail when a module declares a malformed public gRPC method name")
	}
	var want *authmw.InvalidPublicGRPCMethodError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *authmw.InvalidPublicGRPCMethodError", err, err)
	}
}

// TestStartWithNoAuthMiddlewareIgnoresModulePublicGRPCMethods covers the
// no-op case: a module declaring public gRPC methods when no AuthMiddleware
// is configured at all must not fail startup — there is no middleware to
// register against, matching every other s.authMW-guarded branch in Start.
func TestStartWithNoAuthMiddlewareIgnoresModulePublicGRPCMethods(t *testing.T) {
	module := &publicMethodModule{methods: []string{"/quicbaas.v1.HealthService/Check"}}

	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithServiceModules([]apimodule.ServiceModule{module}),
		WithTLS(selfSignedTLSConfig(t)),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
}

// TestStartWithModuleWithoutTheListerInterfaceIsANoop covers the other half
// of the no-op case: a module that does not implement GRPCPublicMethodLister
// at all must not affect startup or the public-method set.
func TestStartWithModuleWithoutTheListerInterfaceIsANoop(t *testing.T) {
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithAuthenticator(alwaysFailAuthenticator{}))

	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithServiceModules([]apimodule.ServiceModule{&stubModule{}}),
		WithAuthMiddleware(mw),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	interceptor := mw.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/Check"}
	_, err := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("no method should have become public")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected the method to remain gated without a GRPCPublicMethodLister declaration")
	}
}
