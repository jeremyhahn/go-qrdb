// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// ---------------------------------------------------------------------------
// The SO PIN travels only over TLS, and only to the configured host.
//
// pinProbe is an MCP server whose qrdb_barrier_rekey tool records the
// X-QRDB-SO-PIN header of the HTTP request that carried the call. It stands
// in for a server the client must not trust -- a plaintext or foreign
// listener -- so the property under test is what the CLIENT sends.
// ---------------------------------------------------------------------------

const soTLSTestPIN = "correct horse battery staple"

type pinCtxKey struct{}

type pinProbe struct {
	mu   sync.Mutex
	pins []string
}

func (p *pinProbe) sawPIN() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pins) > 0
}

func (p *pinProbe) handler() http.Handler {
	srv := mcpserver.NewMCPServer("pin-probe", "1")
	srv.AddTool(mcp.NewTool("qrdb_barrier_rekey"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if pin, _ := ctx.Value(pinCtxKey{}).(string); pin != "" {
			p.mu.Lock()
			p.pins = append(p.pins, pin)
			p.mu.Unlock()
		}
		return mcp.NewToolResultText(`{"threshold":2,"total_shares":3}`), nil
	})
	return mcpserver.NewStreamableHTTPServer(srv, mcpserver.WithHTTPContextFunc(
		func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, pinCtxKey{}, r.Header.Get(transport.SOPINHeader))
		}))
}

func connectMCP(t *testing.T, address string, tlsCfg *tls.Config) *Transport {
	t.Helper()
	opts := []transport.Option{transport.WithAddress(address), transport.WithTimeout(5 * time.Second)}
	if tlsCfg != nil {
		opts = append(opts, transport.WithTLSConfig(tlsCfg))
	}
	tr, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func rekeyWithPIN(t *testing.T, tr *Transport) error {
	t.Helper()
	ctx, release := sotest.BindPIN(t, soTLSTestPIN)
	defer release()
	_, err := tr.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3})
	return err
}

func TestSOPIN_PlaintextConnectionNeverCarriesThePIN(t *testing.T) {
	probe := &pinProbe{}
	srv := httptest.NewServer(probe.handler())
	defer srv.Close()

	err := rekeyWithPIN(t, connectMCP(t, srv.URL+"/mcp", nil))
	if probe.sawPIN() {
		t.Fatal("the SO PIN crossed a plaintext MCP connection")
	}
	var tlsErr *transport.SOPINRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("err = %v, want *transport.SOPINRequiresTLSError", err)
	}
}

func TestSOPIN_RedirectToAnotherHostNeverCarriesThePIN(t *testing.T) {
	probe := &pinProbe{}
	foreign := httptest.NewTLSServer(probe.handler())
	defer foreign.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// #nosec G710 -- the open redirect is the fixture: it sends the client to this test's own second httptest server to prove the SO PIN does not follow.
		http.Redirect(w, r, foreign.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	// One trust store for both: httptest servers share a certificate.
	pool := x509.NewCertPool()
	pool.AddCert(origin.Certificate())
	err := rekeyWithPIN(t, connectMCP(t, origin.URL+"/mcp", &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
	if probe.sawPIN() {
		t.Fatal("the SO PIN followed a redirect to another host")
	}
	var hostErr *transport.SOPINForeignHostError
	if !errors.As(err, &hostErr) {
		t.Fatalf("err = %v, want *transport.SOPINForeignHostError", err)
	}
}

func TestSOPIN_ConfiguredTLSHostCarriesThePIN(t *testing.T) {
	probe := &pinProbe{}
	srv := httptest.NewTLSServer(probe.handler())
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	if err := rekeyWithPIN(t, connectMCP(t, srv.URL+"/mcp", &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})); err != nil {
		t.Fatalf("rekey over TLS: %v", err)
	}
	if !probe.sawPIN() {
		t.Fatal("the configured TLS host did not receive the PIN")
	}
}
