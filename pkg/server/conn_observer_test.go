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

package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// connCountingModule is a capability module that keeps per-transport
// connection counts through the ConnObserver hook — the shape a composing
// host's active_connections metric takes.
type connCountingModule struct {
	apimodule.BaseModule
	mu     sync.Mutex
	opened map[apimodule.Transport]int
	closed map[apimodule.Transport]int
}

var _ apimodule.ConnObserver = (*connCountingModule)(nil)

func newConnCountingModule() *connCountingModule {
	return &connCountingModule{
		opened: map[apimodule.Transport]int{},
		closed: map[apimodule.Transport]int{},
	}
}

func (m *connCountingModule) Name() string { return "conn-counting" }

func (m *connCountingModule) ConnOpened(t apimodule.Transport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opened[t]++
}

func (m *connCountingModule) ConnClosed(t apimodule.Transport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed[t]++
}

func (m *connCountingModule) counts(t apimodule.Transport) (opened, closed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opened[t], m.closed[t]
}

// allConnTransports is every listener the hook applies to.
var allConnTransports = []apimodule.Transport{
	apimodule.TransportREST,
	apimodule.TransportHTTP3,
	apimodule.TransportGRPC,
	apimodule.TransportUnix,
	apimodule.TransportQUIC,
	apimodule.TransportMCP,
}

// waitForCounts polls until cond holds for every transport or the deadline
// passes, reporting the counts it last saw.
func waitForCounts(t *testing.T, m *connCountingModule, what string, cond func(opened, closed int) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending := map[apimodule.Transport][2]int{}
		for _, tr := range allConnTransports {
			if o, c := m.counts(tr); !cond(o, c) {
				pending[tr] = [2]int{o, c}
			}
		}
		if len(pending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: transports not satisfied (opened, closed): %v", what, pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestModuleConnObserver_SeesEveryListener is #562's verification against
// the real unified server: a module implementing ConnObserver sees matched
// open and close events on every listener it serves — REST over TCP and over
// HTTP/3, gRPC, unix, native QUIC and MCP — while each carries a real
// request, so the hook is also shown not to disturb TLS, HTTP/2 negotiation
// or gRPC credentials.
func TestModuleConnObserver_SeesEveryListener(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)
	mod := newConnCountingModule()

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithModule(mod), WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			srv.Stop()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tlsOpt := testAutoProvisionClientTLSOption(t)

	// First, on a quiet server: a bare TLS session per TCP listener,
	// handshaken and closed by the client, is reported opened and then closed
	// while the server keeps running — the live half of an active-connection
	// gauge.
	rawTLS := &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"},
	}
	for tr, addr := range map[apimodule.Transport][2]string{
		apimodule.TransportMCP:  {"tcp", srv.MCPAddr()},
		apimodule.TransportREST: {"tcp", srv.RESTAddr()},
		apimodule.TransportGRPC: {"tcp", srv.GRPCAddr()},
		apimodule.TransportUnix: {"unix", srv.UnixAddr()},
	} {
		conn, err := (&tls.Dialer{Config: rawTLS}).DialContext(ctx, addr[0], addr[1])
		if err != nil {
			t.Fatalf("%s: TLS dial: %v", tr, err)
		}
		_ = conn.Close()
		deadline := time.Now().Add(10 * time.Second)
		for {
			o, c := mod.counts(tr)
			if o == 1 && c == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: client closed but server reports opened=%d closed=%d, want 1/1", tr, o, c)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	clients := map[apimodule.Transport]func() (transport.Transport, error){
		apimodule.TransportREST: func() (transport.Transport, error) {
			return resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), tlsOpt)
		},
		apimodule.TransportGRPC: func() (transport.Transport, error) {
			return grpctransport.New(transport.WithAddress(srv.GRPCAddr()), tlsOpt)
		},
		apimodule.TransportUnix: func() (transport.Transport, error) {
			return unixtransport.New(transport.WithAddress(srv.UnixAddr()), tlsOpt)
		},
		apimodule.TransportQUIC: func() (transport.Transport, error) {
			return quictransport.New(transport.WithAddress(srv.QUICAddr()), tlsOpt)
		},
		apimodule.TransportMCP: func() (transport.Transport, error) {
			return mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), tlsOpt)
		},
	}
	for name, dial := range clients {
		tr, err := dial()
		if err != nil {
			t.Fatalf("%s: New: %v", name, err)
		}
		if err := tr.Connect(ctx); err != nil {
			t.Fatalf("%s: Connect: %v", name, err)
		}
		if !tr.Healthy(ctx) {
			t.Fatalf("%s: health round trip failed through an observed listener", name)
		}
		if err := tr.Close(); err != nil {
			t.Fatalf("%s: Close: %v", name, err)
		}
	}

	// HTTP/3 has no SDK transport; dial the REST listener's QUIC socket
	// directly with the h3 client.
	h3 := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+srv.RESTAddr()+"/health", nil)
	if err != nil {
		t.Fatalf("http3 request: %v", err)
	}
	resp, err := h3.RoundTrip(req)
	if err != nil {
		t.Fatalf("http3 round trip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http3 /health = %d, want 200", resp.StatusCode)
	}
	if err := h3.Close(); err != nil {
		t.Fatalf("http3 transport Close: %v", err)
	}

	// Every listener reported the connection it accepted.
	waitForCounts(t, mod, "after dialing", func(opened, _ int) bool { return opened >= 1 })

	// The QUIC-based listeners see an SDK client's CONNECTION_CLOSE at once,
	// so they balance while the server runs. The TCP ones see a keep-alive
	// client's close only when that client closes its pooled socket; those
	// balance at Stop below.
	for _, tr := range []apimodule.Transport{apimodule.TransportQUIC, apimodule.TransportHTTP3} {
		deadline := time.Now().Add(10 * time.Second)
		for {
			o, c := mod.counts(tr)
			if o == c {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: client closed but server still reports opened=%d closed=%d", tr, o, c)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Stop closes whatever the clients left open (idle HTTP keep-alives among
	// them): after it, every open has its close and nothing was reported
	// twice.
	srv.Stop()
	stopped = true
	waitForCounts(t, mod, "after Stop", func(opened, closed int) bool { return opened >= 1 && opened == closed })
}

// TestModuleConnObserver_NotImplementedChangesNothing is the control: a
// server whose modules do not observe connections serves exactly as before,
// through the same listeners, with no observer wrapping.
func TestModuleConnObserver_NotImplementedChangesNothing(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true, mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithModule(&stubModule{name: "plain"}), WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tr, err := mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer tr.Close()
	if !tr.Healthy(ctx) {
		t.Fatal("health round trip failed with a non-observing module")
	}
}
