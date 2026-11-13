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

package unix

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs/publicverbstest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// startPublicVerbsServer starts a TLS Unix listener that verifies a client
// certificate when one is given, with authentication on and the admin
// service answering Health with publicverbstest.WantHealth. The middleware's
// public-method set is left to srv.Start, so the parity tests fail if Unix
// startup stops registering Health as public.
func startPublicVerbsServer(t *testing.T) (socketPath string, serverCfg *tls.Config) {
	t.Helper()
	mw := authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	socketPath = tempSocketPath(t)
	serverCfg = selfSignedTLSConfig(t)
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	serverCfg.ClientCAs = x509.NewCertPool()
	srv := NewServer(newMockKVService(), publicverbstest.Admin{AdminServicer: &mockAdminService{}}, socketPath,
		WithTLS(serverCfg), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return socketPath, serverCfg
}

// publicVerbsHealth calls Health over socketPath presenting cert, or no
// certificate when cert is nil.
func publicVerbsHealth(t *testing.T, socketPath string, serverCfg *tls.Config, cert *tls.Certificate) (*pb.HealthResponse, error) {
	t.Helper()
	clientCfg := &tls.Config{RootCAs: serverCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	if cert != nil {
		// Present cert whatever CAs the server names; crypto/tls would
		// otherwise withhold one no named CA issued.
		clientCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(grpccredentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return pb.NewQRDBServiceClient(conn).Health(ctx, nil)
}

// TestPublicVerbsParity_UnixHealthAnswer: a caller with no certificate gets
// the admin service's Health answer, every field intact, the same fixture
// the REST, gRPC and QUIC parity tests compare against.
func TestPublicVerbsParity_UnixHealthAnswer(t *testing.T) {
	socketPath, serverCfg := startPublicVerbsServer(t)
	resp, err := publicVerbsHealth(t, socketPath, serverCfg, nil)
	if err != nil {
		t.Fatalf("Health with no certificate: %v", err)
	}
	publicverbstest.CheckHealth(t, "Unix", service.HealthStatus{
		Status: resp.GetStatus(), Leader: resp.GetLeader(), DiagnosticsDropped: resp.GetDiagnosticsDropped(),
	})
}

// TestPublicVerbsParity_UnixBadCertificateIsHandshakeFailure: a client
// certificate that fails verification ends the handshake. It is never
// downgraded to an anonymous connection, so even the no-login Health fails.
func TestPublicVerbsParity_UnixBadCertificateIsHandshakeFailure(t *testing.T) {
	socketPath, serverCfg := startPublicVerbsServer(t)
	foreign := selfSignedTLSConfig(t).Certificates[0]
	_, err := publicVerbsHealth(t, socketPath, serverCfg, &foreign)
	if code := status.Code(err); code != codes.Unavailable {
		t.Fatalf("Health with an unverifiable certificate = %v (%v), want Unavailable (handshake failure)", code, err)
	}
}
