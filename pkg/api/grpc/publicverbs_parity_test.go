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

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs/publicverbstest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// certlessConn dials p's gRPC listener presenting cert, or no certificate
// when cert is nil.
func certlessConn(t *testing.T, p *parityServer, cert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(p.srv.Addr(), grpc.WithTransportCredentials(credentials.NewTLS(p.clientTLS(parityCaller{cert: cert}))))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// callCode invokes fullMethod with an empty request -- authentication runs
// before the request is decoded -- and returns the status code.
func callCode(conn *grpc.ClientConn, fullMethod string, stream *grpc.StreamDesc) codes.Code {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if stream == nil {
		return status.Code(conn.Invoke(ctx, fullMethod, &emptypb.Empty{}, &emptypb.Empty{}))
	}
	s, err := conn.NewStream(ctx, stream, fullMethod)
	if err != nil {
		return status.Code(err)
	}
	_ = s.SendMsg(&emptypb.Empty{})
	_ = s.CloseSend()
	return status.Code(s.RecvMsg(&emptypb.Empty{}))
}

// TestPublicVerbsParity_GRPC fails when the gRPC transport's no-login set
// differs from publicverbs.Table in either direction. With no client
// certificate, every RPC of QRDBService the table names passes
// authentication, and every other RPC -- unary or streaming, every operation
// class -- is refused Unauthenticated.
func TestPublicVerbsParity_GRPC(t *testing.T) {
	p := startParityServer(t, newParityCA(t), enroll(t, map[string]tls.Certificate{}))
	conn := certlessConn(t, p, nil)

	public := make(map[string]struct{}, len(publicverbs.Verbs))
	for _, m := range publicverbs.GRPCFullMethods(pb.QRDBService_ServiceDesc.ServiceName) {
		public[m] = struct{}{}
	}
	service := "/" + pb.QRDBService_ServiceDesc.ServiceName + "/"
	for _, m := range pb.QRDBService_ServiceDesc.Methods {
		full := service + m.MethodName
		t.Run(m.MethodName, func(t *testing.T) {
			code := callCode(conn, full, nil)
			if _, ok := public[full]; ok {
				if code == codes.Unauthenticated {
					t.Fatalf("%s is in publicverbs.Table but refused a caller with no certificate", full)
				}
				return
			}
			if code != codes.Unauthenticated {
				t.Fatalf("%s with no certificate = %v, want Unauthenticated", full, code)
			}
		})
	}
	for _, s := range pb.QRDBService_ServiceDesc.Streams {
		full := service + s.StreamName
		desc := &grpc.StreamDesc{StreamName: s.StreamName, ServerStreams: s.ServerStreams, ClientStreams: s.ClientStreams}
		t.Run(s.StreamName, func(t *testing.T) {
			if code := callCode(conn, full, desc); code != codes.Unauthenticated {
				t.Fatalf("stream %s with no certificate = %v, want Unauthenticated", full, code)
			}
		})
	}
	if code := callCode(conn, service+publicverbs.Table[publicverbs.VerbHealth].GRPC, nil); code != codes.OK {
		t.Fatalf("Health with no certificate = %v, want OK", code)
	}
}

// TestPublicVerbsParity_GRPCHealthAnswer: a caller with no certificate
// gets the admin service's Health answer, every field intact, the same
// fixture the REST, QUIC and Unix parity tests compare against.
func TestPublicVerbsParity_GRPCHealthAnswer(t *testing.T) {
	ca := newParityCA(t)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, 2, pkix.Name{CommonName: "localhost"}, x509.ExtKeyUsageServerAuth)},
		ClientCAs:    ca.pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(auth.NewMTLSAuthenticator()))
	srv := NewServer(newMockKVService(), publicverbstest.Admin{AdminServicer: newMockAdminService()}, "127.0.0.1:0",
		WithTLS(serverTLS), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn := certlessConn(t, &parityServer{srv: srv, pool: ca.pool}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := &pb.HealthResponse{}
	full := "/" + pb.QRDBService_ServiceDesc.ServiceName + "/" + publicverbs.Table[publicverbs.VerbHealth].GRPC
	if err := conn.Invoke(ctx, full, &emptypb.Empty{}, resp); err != nil {
		t.Fatalf("Health with no certificate: %v", err)
	}
	publicverbstest.CheckHealth(t, "gRPC", service.HealthStatus{
		Status: resp.GetStatus(), Leader: resp.GetLeader(), DiagnosticsDropped: resp.GetDiagnosticsDropped(),
	})
}

// TestPublicVerbsParity_GRPCBadCertificateIsHandshakeFailure: a client
// certificate that fails verification ends the handshake. It is never
// downgraded to an anonymous connection, so even the no-login Health fails.
func TestPublicVerbsParity_GRPCBadCertificateIsHandshakeFailure(t *testing.T) {
	p := startParityServer(t, newParityCA(t), enroll(t, map[string]tls.Certificate{}))
	foreign := newParityCA(t).issue(t, 20, pkix.Name{CommonName: "mallory"}, x509.ExtKeyUsageClientAuth)
	conn := certlessConn(t, p, &foreign)

	health := "/" + pb.QRDBService_ServiceDesc.ServiceName + "/" + publicverbs.Table[publicverbs.VerbHealth].GRPC
	if code := callCode(conn, health, nil); code != codes.Unavailable {
		t.Fatalf("Health with an unverifiable certificate = %v, want Unavailable (handshake failure)", code)
	}
}
