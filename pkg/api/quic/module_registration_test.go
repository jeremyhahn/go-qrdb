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
// unauthorized -- unless the module declares it public
// (apimodule.GRPCPublicMethodLister), in which case it is served to a caller
// with no client certificate, as on gRPC and unix.

package quic

import (
	"context"
	"crypto/tls"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
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
// built-in kv.Put handler. It flips called and returns a canned
// PutResponse, proving the round trip below actually reached THIS handler
// through the projection, not the server's real KV service.
func putProjectionDesc(called *atomic.Bool) *grpc.ServiceDesc {
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
					called.Store(true)
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
	var called atomic.Bool
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
	if !called.Load() {
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

// widgetVersionMethod is the full method of widgetModule's public verb.
const widgetVersionMethod = "/testmodule.v1.WidgetService/Version"

// widgetModule builds a grpcModule whose WidgetService carries Version, which
// it declares public through apimodule.GRPCPublicMethodLister and which has
// no (qrdb.v1.permission) option, beside the permissioned Put of
// putProjectionDesc. versionCalled flips when Version's handler runs.
func widgetModule(versionCalled, putCalled *atomic.Bool) []apimodule.ServiceModule {
	version := &grpcModule{
		impl:          struct{}{},
		publicMethods: []string{widgetVersionMethod},
		desc: &grpc.ServiceDesc{
			ServiceName: "testmodule.v1.WidgetService",
			HandlerType: (*any)(nil),
			Methods: []grpc.MethodDesc{
				{
					MethodName: "Version",
					Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
						if err := dec(&emptypb.Empty{}); err != nil {
							return nil, err
						}
						versionCalled.Store(true)
						return &emptypb.Empty{}, nil
					},
				},
			},
		},
	}
	put := &grpcModule{desc: putProjectionDesc(putCalled), impl: struct{}{}}
	return []apimodule.ServiceModule{version, put}
}

// TestModuleDeclaredPublicMethodIsRegisteredPublic covers the projection's
// public branch: a method the module declares through
// apimodule.GRPCPublicMethodLister is in the frame dispatch map and is
// registered public -- not permission-mapped -- so the default-deny startup
// assertion passes although the method carries no permission option.
func TestModuleDeclaredPublicMethodIsRegisteredPublic(t *testing.T) {
	var versionCalled, putCalled atomic.Bool
	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg),
		WithServiceModules(widgetModule(&versionCalled, &putCalled)),
	)
	t.Cleanup(srv.Stop)

	if _, ok := srv.handlers[widgetVersionMethod]; !ok {
		t.Fatal("a declared-public module method must be registered in the frame dispatch map")
	}
	if !srv.methodPerms.IsPublic(widgetVersionMethod) {
		t.Fatal("a declared-public module method must be registered public")
	}
	if srv.methodPerms.IsPublic("/qrdb.v1.QRDBService/Put") {
		t.Fatal("a module method the module does not declare public must not be registered public")
	}
}

// TestModuleDeclaredPublicWinsOverPermissionOption pins the gRPC rule on
// QUIC: a method declared public is served public even when its descriptor
// also carries a (qrdb.v1.permission) option, exactly as gRPC's
// RegisterPublicGRPCMethods lets the declaration win.
func TestModuleDeclaredPublicWinsOverPermissionOption(t *testing.T) {
	const putMethod = "/qrdb.v1.QRDBService/Put"
	var called atomic.Bool
	module := &grpcModule{desc: putProjectionDesc(&called), impl: struct{}{}, publicMethods: []string{putMethod}}

	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(bundle.serverCfg),
		WithServiceModules([]apimodule.ServiceModule{module}),
	)
	t.Cleanup(srv.Stop)

	if !srv.methodPerms.IsPublic(putMethod) {
		t.Fatal("the module's public declaration must win over the method's permission option")
	}
	if _, ok := srv.handlers[putMethod]; !ok {
		t.Fatal("the declared-public method must be dispatchable")
	}
}

// certlessMatrixEnv starts an mTLS-authenticating server that, like qrdbd
// with auth enabled, accepts a connection without a client certificate
// (VerifyClientCertIfGiven), and returns a dial env for a client with the
// certificate and one without it.
func certlessMatrixEnv(t *testing.T, opts ...ServerOption) (withCert, withoutCert *integrationEnv) {
	t.Helper()
	bundle := generateMutualTLS(t)
	serverCfg := bundle.serverCfg.Clone()
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	bundle.serverCfg = serverCfg

	mw := authmw.New(
		&noopAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
		authmw.WithMTLSBindingPolicy(auth.MTLSBindingEnrolled),
	)
	withCert = startMutualTLSServer(t, &mockKVService{}, &mockAdminService{}, bundle,
		append([]ServerOption{WithAuthMiddleware(mw)}, opts...)...)

	certless := bundle.clientCfg.Clone()
	certless.Certificates = nil
	withoutCert = &integrationEnv{srv: withCert.srv, clientCfg: certless}
	return withCert, withoutCert
}

// TestIntegration_CertlessCaller_ReachesOnlyPublicMethods is the QUIC half of
// the public-operation matrix: a caller with no client certificate reaches
// the module's declared-public verb, and every other operation -- the
// built-in kv.Put and the module's permissioned Put -- answers
// StatusUnauthenticated, the typed refusal REST and gRPC return.
func TestIntegration_CertlessCaller_ReachesOnlyPublicMethods(t *testing.T) {
	var versionCalled, putCalled atomic.Bool
	_, env := certlessMatrixEnv(t, WithServiceModules(widgetModule(&versionCalled, &putCalled)))
	conn := env.dialServer(t)

	empty, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal Empty: %v", err)
	}
	if _, status, _ := roundTrip(t, conn, widgetVersionMethod, 1, empty); status != wire.StatusOK {
		t.Fatalf("public module method status = %d, want StatusOK (%d)", status, wire.StatusOK)
	}
	if !versionCalled.Load() {
		t.Fatal("the public module method's handler was never invoked")
	}

	put, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	for id, method := range map[uint64]string{2: "kv.Put", 3: "/qrdb.v1.QRDBService/Put"} {
		if _, status, _ := roundTrip(t, conn, method, id, put); status != wire.StatusUnauthenticated {
			t.Errorf("%s status = %d, want StatusUnauthenticated (%d)", method, status, wire.StatusUnauthenticated)
		}
	}
	if putCalled.Load() {
		t.Fatal("a certless caller reached the module's permissioned handler")
	}
}

// TestIntegration_CertifiedCaller_ReachesEveryMethod is the matrix's control:
// on the same server a caller presenting its client certificate reaches the
// public verb and the authenticated ones alike.
func TestIntegration_CertifiedCaller_ReachesEveryMethod(t *testing.T) {
	var versionCalled, putCalled atomic.Bool
	env, _ := certlessMatrixEnv(t, WithServiceModules(widgetModule(&versionCalled, &putCalled)))
	conn := env.dialServer(t)

	empty, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal Empty: %v", err)
	}
	put, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal PutRequest: %v", err)
	}
	frames := []struct {
		id      uint64
		method  string
		payload []byte
	}{
		{1, widgetVersionMethod, empty},
		{2, "kv.Put", put},
		{3, "/qrdb.v1.QRDBService/Put", put},
	}
	for _, f := range frames {
		if _, status, _ := roundTrip(t, conn, f.method, f.id, f.payload); status != wire.StatusOK {
			t.Errorf("%s status = %d, want StatusOK (%d)", f.method, status, wire.StatusOK)
		}
	}
	if !versionCalled.Load() || !putCalled.Load() {
		t.Fatalf("module handlers reached: Version=%v Put=%v, want both", versionCalled.Load(), putCalled.Load())
	}
}
