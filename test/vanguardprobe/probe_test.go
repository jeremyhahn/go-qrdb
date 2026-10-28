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

package vanguardprobe

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/vanguard/vanguardgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
)

// blockerGone is the message every probe fails with once vanguard no longer
// has the behaviour that keeps pkg/api/grpcweb.
const blockerGone = ": #1151 step 3, replace pkg/api/grpcweb"

// clientCN is the common name of the client certificate the probes present;
// a peer carrying the blocker's fix exposes it.
const clientCN = "vanguardprobe-client"

// pki is an in-memory CA with one server and one client certificate. Nothing
// is written to disk.
type pki struct {
	pool   *x509.CertPool
	server tls.Certificate
	client tls.Certificate
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	caKey := newKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vanguardprobe-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	leaf := func(serial int64, cn string, usage x509.ExtKeyUsage, ips []net.IP) tls.Certificate {
		key := newKey(t)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create %s: %v", cn, err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	return &pki{
		pool:   pool,
		server: leaf(2, "vanguardprobe-server", x509.ExtKeyUsageServerAuth, []net.IP{net.IPv4(127, 0, 0, 1)}),
		client: leaf(3, clientCN, x509.ExtKeyUsageClientAuth, nil),
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func (p *pki) serverTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.server},
		ClientCAs:    p.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

func (p *pki) clientTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.client},
		RootCAs:      p.pool,
		MinVersion:   tls.VersionTLS13,
	}
}

// healthProbe is the service under test. Check records what its context
// carries, so a probe can read what the transport handed the handler.
type healthProbe struct {
	healthpb.UnimplementedHealthServer
	calls     atomic.Int32
	peerHasCN atomic.Bool
}

func (h *healthProbe) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.calls.Add(1)
	if p, ok := peer.FromContext(ctx); ok {
		if info, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			for _, cert := range info.State.PeerCertificates {
				if cert.Subject.CommonName == clientCN {
					h.peerHasCN.Store(true)
				}
			}
		}
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

// recordingDesc is the generated health ServiceDesc with each unary handler
// wrapped to record the interceptor its caller passes. grpc-go passes the
// server's chained interceptor there; nil means the chain does not run.
func recordingDesc(nilInterceptor *atomic.Int32) *grpc.ServiceDesc {
	desc := healthpb.Health_ServiceDesc
	desc.Methods = make([]grpc.MethodDesc, len(healthpb.Health_ServiceDesc.Methods))
	for i, m := range healthpb.Health_ServiceDesc.Methods {
		handler := m.Handler
		desc.Methods[i] = grpc.MethodDesc{
			MethodName: m.MethodName,
			Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
				if ic == nil {
					nilInterceptor.Add(1)
				}
				return handler(srv, ctx, dec, ic)
			},
		}
	}
	return &desc
}

// countingInterceptor is qrdb's single interceptor chain reduced to a
// counter.
func countingInterceptor(n *atomic.Int32) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		n.Add(1)
		return h(ctx, req)
	}
}

// serveVanguard registers the service through vanguardgrpc onto a connect
// server, mounts it on a loopback HTTPS listener that requires a client
// certificate, and returns a gRPC client connection to it.
func serveVanguard(t *testing.T, p *pki, desc *grpc.ServiceDesc, srv any) *grpc.ClientConn {
	t.Helper()
	server := connect.NewServer()
	vanguardgrpc.NewServiceRegistrar(server).RegisterService(desc, srv)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	return startTLS(t, p, mux)
}

// serveGRPC is the control: the same registration on a grpc.Server, served
// through its ServeHTTP on the same kind of listener.
func serveGRPC(t *testing.T, p *pki, gs *grpc.Server, desc *grpc.ServiceDesc, srv any) *grpc.ClientConn {
	t.Helper()
	gs.RegisterService(desc, srv)
	t.Cleanup(gs.Stop)
	return startTLS(t, p, gs)
}

func startTLS(t *testing.T, p *pki, h http.Handler) *grpc.ClientConn {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.EnableHTTP2 = true
	ts.TLS = p.serverTLS()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	cc, err := grpc.NewClient(ts.Listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(p.clientTLS())))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func check(t *testing.T, cc *grpc.ClientConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("Check status = %v, want SERVING", resp.GetStatus())
	}
}

// TestVanguardSkipsInterceptor: a grpc.Server built with one unary
// interceptor runs it once per call; the same service registered through
// vanguardgrpc receives a nil interceptor, so the chain runs zero times.
func TestVanguardSkipsInterceptor(t *testing.T) {
	p := newPKI(t)

	var ctlRuns, ctlNil atomic.Int32
	ctlSvc := &healthProbe{}
	check(t, serveGRPC(t, p, grpc.NewServer(grpc.UnaryInterceptor(countingInterceptor(&ctlRuns))),
		recordingDesc(&ctlNil), ctlSvc))
	if ctlSvc.calls.Load() != 1 || ctlRuns.Load() != 1 || ctlNil.Load() != 0 {
		t.Fatalf("control: grpc.Server calls=%d interceptor runs=%d nil interceptors=%d, want 1, 1, 0",
			ctlSvc.calls.Load(), ctlRuns.Load(), ctlNil.Load())
	}

	// vanguardgrpc's registrar takes a connect.Server and no grpc.Server, so
	// the server carrying the interceptor has no seam to hand it over: the
	// handler's interceptor argument is the measurement, the count the effect.
	var runs, nilIC atomic.Int32
	gs := grpc.NewServer(grpc.UnaryInterceptor(countingInterceptor(&runs)))
	t.Cleanup(gs.Stop)
	svc := &healthProbe{}
	check(t, serveVanguard(t, p, recordingDesc(&nilIC), svc))
	if svc.calls.Load() != 1 {
		t.Fatalf("vanguard: handler calls = %d, want 1", svc.calls.Load())
	}
	if runs.Load() != 0 || nilIC.Load() != 1 {
		t.Fatalf("vanguard now keeps the interceptor"+blockerGone+
			" (interceptor runs=%d, nil interceptors=%d; v0.5.0 had 0 and 1)", runs.Load(), nilIC.Load())
	}
}

// TestVanguardDropsPeerTLS: behind a listener that requires and verifies a
// client certificate, a grpc.Server handler's peer carries credentials.TLSInfo
// with that certificate; a vanguardgrpc handler's peer does not.
func TestVanguardDropsPeerTLS(t *testing.T) {
	p := newPKI(t)
	var unused atomic.Int32

	ctlSvc := &healthProbe{}
	check(t, serveGRPC(t, p, grpc.NewServer(), recordingDesc(&unused), ctlSvc))
	if !ctlSvc.peerHasCN.Load() {
		t.Fatalf("control: grpc.Server handler peer has no TLSInfo carrying %q", clientCN)
	}

	svc := &healthProbe{}
	check(t, serveVanguard(t, p, recordingDesc(&unused), svc))
	if svc.calls.Load() != 1 {
		t.Fatalf("vanguard: handler calls = %d, want 1", svc.calls.Load())
	}
	if svc.peerHasCN.Load() {
		t.Fatalf("vanguard now carries peer TLS" + blockerGone)
	}
}

// TestPKIClientCertificate guards the probes' own fixture: the client
// certificate is the one whose common name they look for, and it verifies
// against the CA the server trusts.
func TestPKIClientCertificate(t *testing.T) {
	p := newPKI(t)
	cert, err := x509.ParseCertificate(p.client.Certificate[0])
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}
	if cert.Subject.CommonName != clientCN {
		t.Fatalf("client CN = %q, want %q", cert.Subject.CommonName, clientCN)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: p.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("client cert does not verify: %v", err)
	}
	if bytes.Equal(p.client.Certificate[0], p.server.Certificate[0]) {
		t.Fatal("client and server certificates are the same")
	}
}
