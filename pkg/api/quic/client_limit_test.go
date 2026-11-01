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

package quic

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientCertTLS is a TLS pair whose client presents a certificate, so that
// every request reaches the authenticator.
func clientCertTLS(t *testing.T) (serverCfg, clientCfg *tls.Config) {
	t.Helper()
	serverCfg, clientCfg = generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	return serverCfg, clientCfg
}

// roundTripFrom sends one request to addr over a connection whose UDP
// socket is bound to the loopback address local, and returns the status.
func roundTripFrom(t *testing.T, local, addr string, clientCfg *tls.Config, method string) uint16 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(local)})
	if err != nil {
		t.Fatalf("listen %s: %v", local, err)
	}
	tr := &quicgo.Transport{Conn: udp}
	defer func() { _ = tr.Close() }()
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve %s: %v", addr, err)
	}
	conn, err := tr.Dial(ctx, raddr, clientCfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "test done") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := wire.WriteRequest(stream, method, "", 1, nil); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close stream: %v", err)
	}
	_, status, _, err := wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return status
}

// TestClientLimit_QUIC: an address past its burst is refused with
// ResourceExhausted before authentication runs, while another address is
// still authenticated.
func TestClientLimit_QUIC(t *testing.T) {
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: 2}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	authn := &quicDispatchAuthenticator{}
	serverCfg, clientCfg := clientCertTLS(t)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
		WithClientLimiter(l))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)

	for i := 0; i < 2; i++ {
		if st := roundTripFrom(t, "127.0.0.1", srv.Addr(), clientCfg, "kv.Get"); st != wire.StatusUnauthenticated {
			t.Fatalf("request %d: status %s, want UNAUTHENTICATED", i, wire.StatusName(st))
		}
	}
	if st := roundTripFrom(t, "127.0.0.1", srv.Addr(), clientCfg, "kv.Get"); st != wire.StatusResourceExhausted {
		t.Fatalf("over the burst: status %s, want RESOURCE_EXHAUSTED", wire.StatusName(st))
	}
	if n := authn.calls.Load(); n != 2 {
		t.Fatalf("authentications = %d, want 2 (refusals never authenticate)", n)
	}
	if st := roundTripFrom(t, "127.0.0.2", srv.Addr(), clientCfg, "kv.Get"); st != wire.StatusUnauthenticated {
		t.Fatalf("other address: status %s, want UNAUTHENTICATED", wire.StatusName(st))
	}
}

// TestClientLimit_QUICUnlimitedByDefault: without WithClientLimiter every
// request reaches authentication.
func TestClientLimit_QUICUnlimitedByDefault(t *testing.T) {
	authn := &quicDispatchAuthenticator{}
	serverCfg, clientCfg := clientCertTLS(t)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	for i := 0; i < 3; i++ {
		if st := roundTripFrom(t, "127.0.0.1", srv.Addr(), clientCfg, "kv.Get"); st != wire.StatusUnauthenticated {
			t.Fatalf("request %d: status %s, want UNAUTHENTICATED", i, wire.StatusName(st))
		}
	}
	if n := authn.calls.Load(); n != 3 {
		t.Fatalf("authentications = %d, want 3", n)
	}
}
