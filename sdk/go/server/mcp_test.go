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

package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp/mcphttp"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

// mcpTestTLS returns a server TLS config with a self-signed certificate for
// 127.0.0.1, and a client config that trusts it.
func mcpTestTLS(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	server = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
	}
	client = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	return server, client
}

// TestNewMCPServer_ServesToolsOverListener builds the combined MCP server
// through sdk/go alone, over a real KV service and a caller-bound listener,
// and drives it with an MCP client: tools/list carries the KV tools, and a
// qrdb_put followed by a qrdb_get round-trips the value.
func TestNewMCPServer_ServesToolsOverListener(t *testing.T) {
	serverTLS, clientTLS := mcpTestTLS(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	kv := sdk.NewBackendKVService(sdk.NewStandaloneBackend(sdkstorage.NewMemoryStorage()))

	var srv *sdkserver.MCPServer = sdkserver.NewMCPServer(kv, nil, ln.Addr().String(),
		sdkserver.WithMCPServerTLS(serverTLS),
		sdkserver.WithMCPServerListener(ln),
		sdkserver.WithMCPServerMaxConnections(8),
		sdkserver.WithMCPServerMaxBodyBytes(1<<20),
		sdkserver.WithMCPServerMaxConnsPerPeer(4),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	if srv.Addr() != ln.Addr().String() {
		t.Fatalf("Addr() = %q, want the handed-over listener %q", srv.Addr(), ln.Addr())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := mcpclient.NewClient(mcphttp.New("https://"+srv.Addr()+"/mcp",
		&http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "sdk-server-test", Version: "1"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tools, err := client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	listed := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		listed[tool.Name] = true
	}
	for _, name := range []string{"qrdb_put", "qrdb_get"} {
		if !listed[name] {
			t.Errorf("tools/list lacks %s", name)
		}
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := client.CallTool(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s returned a tool error: %v", name, res.Content)
		}
		return res
	}
	value := base64.StdEncoding.EncodeToString([]byte("over-the-listener"))
	call("qrdb_put", map[string]any{"key": "sdk-mcp-key", "value": value})
	got := call("qrdb_get", map[string]any{"key": "sdk-mcp-key"})
	if len(got.Content) != 1 {
		t.Fatalf("qrdb_get content has %d items, want 1", len(got.Content))
	}
	text, ok := got.Content[0].(mcp.TextContent)
	if !ok || text.Text != value {
		t.Fatalf("qrdb_get = %#v, want text %q", got.Content[0], value)
	}
}

// TestNewMCPServer_RefusesWithoutTLS shows the alias keeps the server's
// fail-closed start: no TLS configuration is the typed refusal, and the
// handed-over listener is released.
func TestNewMCPServer_RefusesWithoutTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := sdkserver.NewMCPServer(nil, nil, addr, sdkserver.WithMCPServerListener(ln))
	err = srv.Start()
	var notConfigured *sdkserver.MCPTLSNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("Start without TLS = %v, want *MCPTLSNotConfiguredError", err)
	}
	// The refused server closed the listener, so its port binds again.
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listener not released after the refusal: %v", err)
	}
	_ = again.Close()
}

// TestMCPServerOptions_ReExported applies the authentication and
// sealed-window options through their aliases: each yields an option the
// constructor accepts.
func TestMCPServerOptions_ReExported(t *testing.T) {
	opts := []sdkserver.MCPServerOption{
		sdkserver.WithMCPServerAuthMiddleware(nil),
		sdkserver.WithMCPServerSealedWindowProbe(&fakeProbe{}),
		sdkserver.WithMCPServerSealedWindowDispatch(nil),
	}
	for i, opt := range opts {
		if opt == nil {
			t.Fatalf("option %d is nil", i)
		}
	}
	if srv := sdkserver.NewMCPServer(nil, nil, "127.0.0.1:0", opts...); srv == nil {
		t.Fatal("NewMCPServer returned nil")
	}
}
