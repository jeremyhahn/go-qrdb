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

// public_operations_parity_test.go asserts the public-operation matrix on
// every TCP/UDP transport of one running node: a capability module declares
// its no-login verbs public once -- HandlePublic on REST, PublicGRPCMethods
// for the gRPC-shaped transports -- and a caller with no client certificate
// reaches exactly those over REST, gRPC and native QUIC, while a KV read is
// refused as unauthenticated. The same caller with its certificate reaches
// both.

package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	qrdbpb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// publicOpsService is the gRPC service of publicOpsModule.
const publicOpsService = "testmodule.v1.PublicOpsService"

// publicOpsVerbs are the module's no-login verbs, one per kind the public
// set names: health, version and the first leg of a login.
var publicOpsVerbs = []string{"Health", "Version", "BeginLogin"}

// publicOpsRESTPath is the module's public REST route.
const publicOpsRESTPath = "/v1/public-ops/version"

// publicOpsModule is a capability module whose every operation needs no
// login. calls counts handler invocations across all transports.
type publicOpsModule struct {
	apimodule.BaseModule
	calls *atomic.Int32
}

func (publicOpsModule) Name() string { return "public-ops" }

func (m publicOpsModule) PublicGRPCMethods() []string {
	methods := make([]string, 0, len(publicOpsVerbs))
	for _, verb := range publicOpsVerbs {
		methods = append(methods, "/"+publicOpsService+"/"+verb)
	}
	return methods
}

func (m publicOpsModule) RegisterGRPC(reg grpc.ServiceRegistrar) { reg.RegisterService(m.desc(), nil) }

func (m publicOpsModule) RegisterQUIC(reg grpc.ServiceRegistrar) { reg.RegisterService(m.desc(), nil) }

func (m publicOpsModule) RegisterHTTP(r *apimodule.HTTPRegistrar) {
	r.HandlePublic(http.MethodGet, publicOpsRESTPath, func(w http.ResponseWriter, _ *http.Request) {
		m.calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
}

// desc builds the service with one unary Empty -> Empty method per verb. Its
// handlers run the server's interceptor chain the way generated code does,
// so the gRPC auth interceptor sees every call.
func (m publicOpsModule) desc() *grpc.ServiceDesc {
	methods := make([]grpc.MethodDesc, 0, len(publicOpsVerbs))
	for _, verb := range publicOpsVerbs {
		fullMethod := "/" + publicOpsService + "/" + verb
		methods = append(methods, grpc.MethodDesc{
			MethodName: verb,
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := &emptypb.Empty{}
				if err := dec(in); err != nil {
					return nil, err
				}
				handler := func(context.Context, any) (any, error) {
					m.calls.Add(1)
					return &emptypb.Empty{}, nil
				}
				if interceptor == nil {
					return handler(ctx, in)
				}
				return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}, handler)
			},
		})
	}
	return &grpc.ServiceDesc{ServiceName: publicOpsService, HandlerType: (*any)(nil), Methods: methods}
}

// peerCertIdentity authenticates a connection that presented a client
// certificate and refuses one that presented none. Like a real
// authenticator it returns a fresh Identity per call: the middleware
// finalizes the one it is given, so a shared one is a data race between
// transports.
type peerCertIdentity struct{}

func (peerCertIdentity) Authenticate(ctx context.Context) (*auth.Identity, error) {
	st := auth.TLSStateFromContext(ctx)
	if st == nil || len(st.PeerCertificates) == 0 {
		return nil, auth.ErrNoPeerCertificate
	}
	return &auth.Identity{Subject: "alice", TenantID: "acme"}, nil
}

func (peerCertIdentity) Name() string { return "test-peer-cert" }

// publicOpsNode is a running node serving REST, gRPC and QUIC with mTLS
// authentication that a caller may decline (VerifyClientCertIfGiven), as
// qrdbd does with auth enabled.
type publicOpsNode struct {
	srv       *Server
	calls     *atomic.Int32
	certified *tls.Config
	certless  *tls.Config
}

func startPublicOpsNode(t *testing.T) *publicOpsNode {
	t.Helper()
	serverTLS, clientTLS := selfSignedMTLS(t)
	calls := &atomic.Int32{}
	kv := newMockKVService()
	kv.store["k"] = []byte("v")

	mw := authmw.New(allowAll{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(peerCertIdentity{}))
	srv := New(testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true}, ""), kv, &mockAdminService{},
		WithTLS(serverTLS), WithAuthMiddleware(mw), WithModule(publicOpsModule{calls: calls}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	certless := clientTLS.Clone()
	certless.Certificates = nil
	return &publicOpsNode{srv: srv, calls: calls, certified: clientTLS, certless: certless}
}

// restStatus issues GET path over REST and returns the HTTP status.
func (n *publicOpsNode) restStatus(t *testing.T, cfg *tls.Config, path string) int {
	t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg.Clone()}}
	t.Cleanup(hc.CloseIdleConnections)
	resp, err := hc.Get("https://" + n.srv.RESTAddr() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// grpcCode invokes method over gRPC and returns the status code.
func (n *publicOpsNode) grpcCode(t *testing.T, cfg *tls.Config, method string, in, out proto.Message) codes.Code {
	t.Helper()
	conn, err := grpc.NewClient(n.srv.GRPCAddr(), grpc.WithTransportCredentials(credentials.NewTLS(cfg.Clone())))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return status.Code(conn.Invoke(ctx, method, in, out))
}

// quicStatus sends one frame over native QUIC and returns its wire status.
func (n *publicOpsNode) quicStatus(t *testing.T, cfg *tls.Config, method string, in proto.Message) uint16 {
	t.Helper()
	payload, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	quicTLS := cfg.Clone()
	quicTLS.NextProtos = []string{wire.ProtocolV3}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, n.srv.QUICAddr(), quicTLS, &quic.Config{})
	if err != nil {
		t.Fatalf("quic dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if err := wire.WriteRequest(stream, method, "", 1, payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, st, _, err := wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return st
}

// assertPublicVerbsServed asserts every public verb answers on every
// transport for a caller configured with cfg, and that each call reached the
// module's handler.
func (n *publicOpsNode) assertPublicVerbsServed(t *testing.T, cfg *tls.Config) {
	t.Helper()
	before := n.calls.Load()
	if got := n.restStatus(t, cfg, publicOpsRESTPath); got != http.StatusOK {
		t.Errorf("REST GET %s = %d, want %d", publicOpsRESTPath, got, http.StatusOK)
	}
	for _, verb := range publicOpsVerbs {
		method := "/" + publicOpsService + "/" + verb
		if got := n.grpcCode(t, cfg, method, &emptypb.Empty{}, &emptypb.Empty{}); got != codes.OK {
			t.Errorf("gRPC %s = %v, want OK", method, got)
		}
		if got := n.quicStatus(t, cfg, method, &emptypb.Empty{}); got != wire.StatusOK {
			t.Errorf("QUIC %s = %d, want StatusOK (%d)", method, got, wire.StatusOK)
		}
	}
	if want := int32(1 + 2*len(publicOpsVerbs)); n.calls.Load()-before != want {
		t.Errorf("module handlers ran %d times, want %d", n.calls.Load()-before, want)
	}
}

// TestPublicOperations_CertlessCallerReachesOnlyThePublicSet is the
// unauthenticated row of the matrix: the module's public verbs answer on
// REST, gRPC and QUIC, and a KV read on each answers with that transport's
// typed unauthenticated refusal.
func TestPublicOperations_CertlessCallerReachesOnlyThePublicSet(t *testing.T) {
	n := startPublicOpsNode(t)
	n.assertPublicVerbsServed(t, n.certless)

	if got := n.restStatus(t, n.certless, "/kv/k"); got != http.StatusUnauthorized {
		t.Errorf("REST GET /kv/k = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := n.grpcCode(t, n.certless, qrdbpb.QRDBService_Get_FullMethodName, &qrdbpb.GetRequest{Key: "k"}, &qrdbpb.GetResponse{}); got != codes.Unauthenticated {
		t.Errorf("gRPC Get = %v, want Unauthenticated", got)
	}
	if got := n.quicStatus(t, n.certless, "kv.Get", &qrdbpb.GetRequest{Key: "k"}); got != wire.StatusUnauthenticated {
		t.Errorf("QUIC kv.Get = %d, want StatusUnauthenticated (%d)", got, wire.StatusUnauthenticated)
	}
}

// TestPublicOperations_CertifiedCallerReachesEverything is the authenticated
// row: the same node serves the public verbs and the KV read to a caller
// presenting its client certificate.
func TestPublicOperations_CertifiedCallerReachesEverything(t *testing.T) {
	n := startPublicOpsNode(t)
	n.assertPublicVerbsServed(t, n.certified)

	if got := n.restStatus(t, n.certified, "/kv/k"); got != http.StatusOK {
		t.Errorf("REST GET /kv/k = %d, want %d", got, http.StatusOK)
	}
	if got := n.grpcCode(t, n.certified, qrdbpb.QRDBService_Get_FullMethodName, &qrdbpb.GetRequest{Key: "k"}, &qrdbpb.GetResponse{}); got != codes.OK {
		t.Errorf("gRPC Get = %v, want OK", got)
	}
	if got := n.quicStatus(t, n.certified, "kv.Get", &qrdbpb.GetRequest{Key: "k"}); got != wire.StatusOK {
		t.Errorf("QUIC kv.Get = %d, want StatusOK (%d)", got, wire.StatusOK)
	}
}

// TestPublicOperations_CertlessQUICCallerIsNotRefusedAtHandshake pins the
// listener half: a QUIC connection with no client certificate completes its
// handshake, so the refusal of a non-public operation is the typed
// per-operation answer above, not a transport error.
func TestPublicOperations_CertlessQUICCallerIsNotRefusedAtHandshake(t *testing.T) {
	n := startPublicOpsNode(t)
	quicTLS := n.certless.Clone()
	quicTLS.NextProtos = []string{wire.ProtocolV3}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, n.srv.QUICAddr(), quicTLS, &quic.Config{})
	if err != nil {
		t.Fatalf("certless QUIC handshake: %v", err)
	}
	_ = conn.CloseWithError(0, "")
}
