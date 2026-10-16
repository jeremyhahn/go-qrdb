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
	"slices"
	"sync"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// httpProtos is the ALPN list a composer's REST listener offers: the list
// the per-connection config of an HTTP-shaped GetConfigForClient hook
// carries (go-qrdb#741).
var httpProtos = []string{"h2", "http/1.1"}

// startHookedServer starts the native QUIC listener on loopback with
// serverCfg and an admin service answering Health, and returns its address.
func startHookedServer(t *testing.T, serverCfg *tls.Config) string {
	t.Helper()
	adminSvc := &mockAdminService{
		healthFn: func(context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "healthy"}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0", WithTLS(serverCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	return srv.Addr()
}

// dialALPN dials addr offering exactly protos.
func dialALPN(addr string, clientCfg *tls.Config, protos ...string) (*quicgo.Conn, error) {
	cfg := clientCfg.Clone()
	cfg.NextProtos = protos
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return quicgo.DialAddr(ctx, addr, cfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
}

// assertServedOnV3 checks conn negotiated wire.ProtocolV3 and that a
// request on it is served.
func assertServedOnV3(t *testing.T, conn *quicgo.Conn) {
	t.Helper()
	if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != wire.ProtocolV3 {
		t.Fatalf("negotiated ALPN = %q, want %q", got, wire.ProtocolV3)
	}
	_, status, _ := roundTrip(t, conn, "admin.Health", 1, nil)
	if status != wire.StatusOK {
		t.Fatalf("admin.Health status = %d, want StatusOK", status)
	}
}

// assertProtos fails when cfg's ALPN list is no longer want: the listener
// wrote its own ALPN into a config it does not own.
func assertProtos(t *testing.T, name string, cfg *tls.Config, want []string) {
	t.Helper()
	if !slices.Equal(cfg.NextProtos, want) {
		t.Fatalf("%s NextProtos = %v after QUIC handshakes, want %v unchanged: the QUIC listener mutated a config another listener serves", name, cfg.NextProtos, want)
	}
}

// TestQUICListener_GetConfigForClientHook_ServesProtocolV3 drives a real
// handshake through a GetConfigForClient hook whose per-connection config
// offers only h2 and http/1.1 -- go-qrdb#741's shape. Before the listener
// reasserted its ALPN on the hook's result, every such handshake failed
// with "tls: no application protocol".
func TestQUICListener_GetConfigForClientHook_ServesProtocolV3(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	perConn := bundle.serverCfg.Clone()
	perConn.NextProtos = slices.Clone(httpProtos)
	serverCfg := bundle.serverCfg.Clone()
	serverCfg.NextProtos = slices.Clone(httpProtos)
	serverCfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return perConn, nil
	}
	addr := startHookedServer(t, serverCfg)

	conn, err := dialALPN(addr, bundle.clientCfg, wire.ProtocolV3)
	if err != nil {
		t.Fatalf("dial offering %q: %v", wire.ProtocolV3, err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test done") })
	assertServedOnV3(t, conn)
	assertProtos(t, "hook's per-connection config", perConn, httpProtos)
	assertProtos(t, "shared config", serverCfg, httpProtos)
}

// TestQUICListener_GetConfigForClientHook_RefusesHTTPOnlyClient checks the
// reassert pins the ALPN rather than widening it: a client offering only
// h2 -- which the hook's per-connection config does list -- is refused.
func TestQUICListener_GetConfigForClientHook_RefusesHTTPOnlyClient(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	perConn := bundle.serverCfg.Clone()
	perConn.NextProtos = slices.Clone(httpProtos)
	serverCfg := bundle.serverCfg.Clone()
	serverCfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return perConn, nil
	}
	addr := startHookedServer(t, serverCfg)

	conn, err := dialALPN(addr, bundle.clientCfg, "h2")
	if err == nil {
		_ = conn.CloseWithError(0, "test done")
		t.Fatalf("a client offering only h2 completed a handshake, negotiating %q", conn.ConnectionState().TLS.NegotiatedProtocol)
	}
	assertProtos(t, "hook's per-connection config", perConn, httpProtos)
}

// TestQUICListener_SharedTrustPoolConfig_LeavesSharedConfigUntouched builds
// the composition an embedded node uses (sdk/go/node's startServer): one
// *tls.Config shared by REST and QUIC, carrying the composer's own
// GetConfigForClient hook, with clienttrust.Pool.Wire composed over it.
// While the pool grants no widening, Wire's hook hands back the composer's
// config itself, so a listener that reasserted its ALPN in place would
// rewrite the ALPN REST serves, racing every concurrent handshake. Run
// under -race: the handshakes run concurrently.
func TestQUICListener_SharedTrustPoolConfig_LeavesSharedConfigUntouched(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	perConn := bundle.serverCfg.Clone()
	perConn.NextProtos = slices.Clone(httpProtos)
	shared := bundle.serverCfg.Clone()
	shared.NextProtos = slices.Clone(httpProtos)
	shared.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return perConn, nil
	}
	clienttrust.New().Wire(shared)
	addr := startHookedServer(t, shared)

	const dials = 4
	conns := make([]*quicgo.Conn, dials)
	errs := make([]error, dials)
	var wg sync.WaitGroup
	for i := range dials {
		wg.Go(func() { conns[i], errs[i] = dialALPN(addr, bundle.clientCfg, wire.ProtocolV3) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("dial %d offering %q: %v", i, wire.ProtocolV3, err)
		}
		t.Cleanup(func() { _ = conns[i].CloseWithError(0, "test done") })
		assertServedOnV3(t, conns[i])
	}
	assertProtos(t, "composer's per-connection config", perConn, httpProtos)
	assertProtos(t, "shared config", shared, httpProtos)
}

// TestQUICListener_SharedTrustPoolConfig_NoComposerHook covers Wire over a
// shared config with no hook of its own: Wire's hook returns the shared
// config itself, which REST serves too.
func TestQUICListener_SharedTrustPoolConfig_NoComposerHook(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	shared := bundle.serverCfg.Clone()
	shared.NextProtos = slices.Clone(httpProtos)
	clienttrust.New().Wire(shared)
	addr := startHookedServer(t, shared)

	conn, err := dialALPN(addr, bundle.clientCfg, wire.ProtocolV3)
	if err != nil {
		t.Fatalf("dial offering %q: %v", wire.ProtocolV3, err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test done") })
	assertServedOnV3(t, conn)
	assertProtos(t, "shared config", shared, httpProtos)
}
