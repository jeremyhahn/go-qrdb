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

// module_registration_test.go covers ServiceModule.RegisterQUIC end to end:
// a capability module registered once (WithServiceModules) must be reachable
// over the native QUIC transport, authorized by the SAME
// (qrdb.v1.permission) proto descriptor option gRPC and unix authorize
// against, with no second permission declaration. It also covers the
// default-deny half: a module method whose descriptor carries no permission
// option must fail server construction loudly rather than become reachable
// unauthorized.

package quic

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
)

// grpcModule is a minimal apimodule.ServiceModule stand-in for a capability
// module whose surface is a single gRPC service, mirroring how a real module
// (go-xkms, go-quicbaas) implements RegisterQUIC: it forwards the registrar
// straight to its ServiceDesc, exactly as RegisterGRPC would.
type grpcModule struct {
	apimodule.BaseModule
	desc *grpc.ServiceDesc
	impl any

	// publicMethods, when non-empty, makes grpcModule satisfy
	// apimodule.GRPCPublicMethodLister, exercising the QUIC projection's
	// public-method carve-out.
	publicMethods []string
}

func (m *grpcModule) Name() string { return "grpc-module" }

func (m *grpcModule) PublicGRPCMethods() []string { return m.publicMethods }

func (m *grpcModule) RegisterQUIC(reg grpc.ServiceRegistrar) {
	reg.RegisterService(m.desc, m.impl)
}

// putProjectionDesc builds a grpc.ServiceDesc that reuses the full gRPC
// method identity of the real qrdb.v1.QRDBService/Put RPC -- purely so its
// (qrdb.v1.permission) proto descriptor option resolves through the global
// proto registry the same way it would for any module's own compiled proto
// package -- but wires a hand-built Handler that has nothing to do with the
// built-in kv.Put handler. It flips *called and returns a canned
// PutResponse, proving the round trip below actually reached THIS handler
// through the projection, not the server's real KV service.
func putProjectionDesc(called *bool) *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "qrdb.v1.QRDBService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Put",
				Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					var req pb.PutRequest
					if err := dec(&req); err != nil {
						return nil, err
					}
					*called = true
					return &pb.PutResponse{}, nil
				},
			},
		},
	}
}

// TestModuleRegisteredOnceReachesQUIC is the mutation-verify test: a module
// registered exactly once, through WithServiceModules -> RegisterQUIC, must
// be dispatchable over a real QUIC connection. Undo the RegisterQUIC hook,
// the registerModules wiring in buildHandlers, or the WithServiceModules
// option and this test fails -- either the build breaks (WithServiceModules
// no longer exists) or the round trip comes back StatusUnimplemented because
// no handler was ever registered for the method.
func TestModuleRegisteredOnceReachesQUIC(t *testing.T) {
	var called bool
	module := &grpcModule{desc: putProjectionDesc(&called), impl: struct{}{}}

	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithServiceModules([]apimodule.ServiceModule{module}),
	)
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{Key: "widget", Value: []byte("value")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	respID, status, respPayload := roundTrip(t, conn, "/qrdb.v1.QRDBService/Put", 99, payload)

	if respID != 99 {
		t.Fatalf("response ID = %d, want 99", respID)
	}
	if status != wire.StatusOK {
		t.Fatalf("status = %d, want StatusOK (%d)", status, wire.StatusOK)
	}
	if !called {
		t.Fatal("the module's projected Handler was never invoked -- the frame did not reach the module")
	}

	var resp pb.PutResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
}

// TestModuleMethodWithoutHandlerIsUnreachable pins the negative control for
// the above: a module method the server was never given (no
// WithServiceModules at all) stays StatusUnimplemented, proving the positive
// result above is really about registration and not a permissive dispatch
// loop.
func TestModuleMethodWithoutHandlerIsUnreachable(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})
	conn := env.dialServer(t)

	payload, err := proto.Marshal(&pb.PutRequest{Key: "widget", Value: []byte("value")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}

	_, status, _ := roundTrip(t, conn, "/qrdb.v1.QRDBService/Put", 1, payload)
	if status != wire.StatusUnimplemented {
		t.Fatalf("status = %d, want StatusUnimplemented (%d)", status, wire.StatusUnimplemented)
	}
}

// TestNewServerPanicsWhenModuleGRPCMethodHasNoPermission covers the
// verification's other half: a module method whose proto descriptor (or, as
// here, a hand-built ServiceDesc with no matching descriptor at all) carries
// no (qrdb.v1.permission) option must fail server construction rather than
// silently become reachable with no authorization check -- QUIC's
// default-deny startup assertion, symmetric with REST's route walk.
func TestNewServerPanicsWhenModuleGRPCMethodHasNoPermission(t *testing.T) {
	module := &grpcModule{
		impl: struct{}{},
		desc: &grpc.ServiceDesc{
			ServiceName: "testmodule.v1.WidgetService",
			HandlerType: (*any)(nil),
			Methods: []grpc.MethodDesc{
				{
					MethodName: "CreateWidget",
					Handler: func(_ any, _ context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
						return nil, nil
					},
				},
			},
		},
	}

	bundle := generateIntegrationTLS(t)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewServer must panic when a module gRPC method has no declared permission")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value %v is not a string", r)
		}
		if !strings.Contains(msg, "/testmodule.v1.WidgetService/CreateWidget") {
			t.Errorf("panic message %q does not name the offending method", msg)
		}
		if !strings.Contains(msg, "without a permission") {
			t.Errorf("panic message %q does not identify the default-deny assertion", msg)
		}
	}()

	_ = NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg),
		WithServiceModules([]apimodule.ServiceModule{module}),
	)
}

// TestModuleStreamingMethodIsNotProjected pins the documented streaming gap:
// a module's streaming RPC (grpc.ServiceDesc.Streams) is not projected onto
// the QUIC frame protocol, so registering only a streaming method must not
// panic and must not register any frame handler. This is what keeps a
// module free to expose streaming operations elsewhere (gRPC, unix) without
// RegisterQUIC becoming a construction-time error.
func TestModuleStreamingMethodIsNotProjected(t *testing.T) {
	module := &grpcModule{
		impl: struct{}{},
		desc: &grpc.ServiceDesc{
			ServiceName: "testmodule.v1.WidgetService",
			HandlerType: (*any)(nil),
			Streams: []grpc.StreamDesc{
				{
					StreamName:    "WatchWidgets",
					ServerStreams: true,
				},
			},
		},
	}

	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg),
		WithServiceModules([]apimodule.ServiceModule{module}),
	)
	t.Cleanup(srv.Stop)

	if _, ok := srv.handlers["/testmodule.v1.WidgetService/WatchWidgets"]; ok {
		t.Fatal("a streaming method must not be registered in the frame dispatch map")
	}
}

// TestModuleDeclaredPublicMethodWithoutPermissionIsSkippedNotPanicked covers
// the projection's public-method carve-out: QUIC has no unauthenticated
// dispatch path (unlike gRPC/unix, which bypass auth entirely for a method
// declared via apimodule.GRPCPublicMethodLister), so a module's public
// method with no (qrdb.v1.permission) proto option must not trip the
// default-deny panic -- it is simply left unreachable over QUIC, logged, so
// the module's OTHER, permissioned methods still project successfully.
func TestModuleDeclaredPublicMethodWithoutPermissionIsSkippedNotPanicked(t *testing.T) {
	const publicMethod = "/testmodule.v1.WidgetService/Version"

	module := &grpcModule{
		impl:          struct{}{},
		publicMethods: []string{publicMethod},
		desc: &grpc.ServiceDesc{
			ServiceName: "testmodule.v1.WidgetService",
			HandlerType: (*any)(nil),
			Methods: []grpc.MethodDesc{
				{
					MethodName: "Version",
					Handler: func(_ any, _ context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
						return nil, nil
					},
				},
			},
		},
	}

	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg),
		WithServiceModules([]apimodule.ServiceModule{module}),
	)
	t.Cleanup(srv.Stop)

	if _, ok := srv.handlers[publicMethod]; ok {
		t.Fatal("a declared-public method with no permission option must not be registered in the frame dispatch map")
	}
	if srv.methodPerms.Covered(publicMethod) {
		t.Fatal("a declared-public method with no permission option must not gain a permission registration either")
	}
}
