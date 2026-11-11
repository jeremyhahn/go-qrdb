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

// Coverage-uplift tests for the MCP SDK transport. These tests target
// previously-uncovered code paths:
//   - buildTLSConfig branches (CA load, mTLS cert load, custom config)
//   - HTTP/TLS Connect round trip via mcpapi.Server.Start (httptest pattern)
//   - applyRaftArgs and applyReadArgs helpers
//   - watchPoll happy path with a real WatchService backed by a watch.Hub
//   - Capabilities live JSON-decode path
//   - Ceremony JSON-decode paths via stub-registered MCP tools

package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Self-signed TLS material helper
// ---------------------------------------------------------------------------

// testTLSMaterial holds a self-signed CA cert/key bundle on disk for tests
// that need to exercise the file-based TLS code paths.
type testTLSMaterial struct {
	caFile   string
	certFile string
	keyFile  string
	tlsCfg   *tls.Config
}

// newTestTLSMaterial generates a self-signed certificate, writes its PEM
// encoding to a temporary directory, and returns paths to the resulting
// CA/cert/key files plus a server tls.Config suitable for use with
// httptest.NewUnstartedServer.
func newTestTLSMaterial(t *testing.T) *testTLSMaterial {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	caPath := filepath.Join(dir, "ca.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	return &testTLSMaterial{
		caFile:   caPath,
		certFile: certPath,
		keyFile:  keyPath,
		tlsCfg: &tls.Config{
			Certificates: []tls.Certificate{
				{Certificate: [][]byte{certDER}, PrivateKey: key},
			},
			RootCAs:    pool,
			MinVersion: tls.VersionTLS13,
		},
	}
}

// ---------------------------------------------------------------------------
// buildTLSConfig direct tests
// ---------------------------------------------------------------------------

func TestBuildTLSConfig_CustomConfigOverridesEverything(t *testing.T) {
	custom := &tls.Config{ServerName: "custom"}
	tr := &Transport{config: &transport.Config{TLSConfig: custom}}

	out, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	if out == custom {
		t.Fatal("expected a clone of the custom config, got the caller's pointer")
	}
	if out.ServerName != "custom" {
		t.Fatalf("ServerName = %q, want %q", out.ServerName, "custom")
	}
}

func TestBuildTLSConfig_TLSDisabled(t *testing.T) {
	tr := &Transport{config: &transport.Config{TLSEnabled: false}}

	out, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil TLS config when TLS disabled, got %+v", out)
	}
}

func TestBuildTLSConfig_WithCAFile(t *testing.T) {
	mat := newTestTLSMaterial(t)
	tr := &Transport{config: &transport.Config{
		TLSEnabled: true,
		TLSCAFile:  mat.caFile,
	}}

	out, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	if out == nil || out.RootCAs == nil {
		t.Fatalf("expected RootCAs populated, got %+v", out)
	}
	if out.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS1.3 minimum, got %d", out.MinVersion)
	}
}

func TestBuildTLSConfig_WithMTLSCertAndKey(t *testing.T) {
	mat := newTestTLSMaterial(t)
	tr := &Transport{config: &transport.Config{
		TLSEnabled:  true,
		TLSCAFile:   mat.caFile,
		TLSCertFile: mat.certFile,
		TLSKeyFile:  mat.keyFile,
	}}

	out, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil TLS config")
	}
	// The static Certificates field stays empty: the client certificate
	// is carried via GetClientCertificate instead, so it is presented on
	// every handshake regardless of the server's advertised
	// AcceptableCAs (transport.BuildTLSConfig's own doc comment).
	if len(out.Certificates) != 0 {
		t.Fatalf("expected the static Certificates field to stay empty, got %d entries", len(out.Certificates))
	}
	if out.GetClientCertificate == nil {
		t.Fatal("expected GetClientCertificate to be set")
	}
}

func TestBuildTLSConfig_BadCAFile(t *testing.T) {
	tr := &Transport{config: &transport.Config{
		TLSEnabled: true,
		TLSCAFile:  "/nonexistent/ca.pem",
	}}

	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

func TestBuildTLSConfig_CorruptCAFile(t *testing.T) {
	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badCA, []byte("not a cert"), 0o600); err != nil {
		t.Fatalf("write bad CA: %v", err)
	}

	tr := &Transport{config: &transport.Config{
		TLSEnabled: true,
		TLSCAFile:  badCA,
	}}

	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for malformed CA PEM")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

func TestBuildTLSConfig_BadCertFile(t *testing.T) {
	mat := newTestTLSMaterial(t)

	dir := t.TempDir()
	badCert := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badCert, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("write bad cert: %v", err)
	}

	tr := &Transport{config: &transport.Config{
		TLSEnabled:  true,
		TLSCAFile:   mat.caFile,
		TLSCertFile: badCert,
		TLSKeyFile:  mat.keyFile,
	}}

	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for malformed certificate")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Connect HTTP/TLS round trip
// ---------------------------------------------------------------------------

// TestNew_HTTPRoundTrip exercises the network Connect path (buildTLSConfig +
// NewStreamableHttpClient + Initialize) against a real mcpapi.Server running
// over HTTPS on an ephemeral port. The custom TLS config branch ensures the
// connection succeeds without trusting the system root pool.
func TestNew_HTTPRoundTrip_TLS(t *testing.T) {
	mat := newTestTLSMaterial(t)

	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", mcpapi.WithTLS(mat.tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	// Build the client-side TLS config that trusts our self-signed CA.
	clientTLS := &tls.Config{
		RootCAs:    mat.tlsCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}

	// Resolve the listener address to a localhost URL the MCP HTTP client can
	// reach. The listener may report 127.0.0.1 or ::1; rebuild as localhost
	// so the cert SAN matches.
	addr := srv.Addr()
	_, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		t.Fatalf("split host/port %q: %v", addr, splitErr)
	}
	url := "https://localhost:" + port + "/mcp"

	tr, err := New(
		transport.WithAddress(url),
		transport.WithTLSConfig(clientTLS),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !tr.Healthy(ctx) {
		t.Fatal("expected Healthy=true after Connect over HTTPS")
	}

	// Exercise a real tool call to confirm end-to-end protocol works.
	if err := tr.Put(ctx, "round-trip-key", []byte("ok")); err != nil {
		t.Fatalf("Put over HTTPS: %v", err)
	}
}

// TestConnect_BadTLSCAFile exercises the Connect path's TLS build-error
// branch. With TLS enabled but a missing CA file, buildTLSConfig returns a
// TLSError which Connect must wrap in a ConnectionError.
func TestConnect_BadTLSCAFile(t *testing.T) {
	tr, err := New(
		transport.WithAddress("https://localhost:1/mcp"),
		transport.WithTLS("", "", "/nonexistent/ca.pem"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = tr.Connect(ctx)
	if err == nil {
		_ = tr.Close()
		t.Fatal("expected error when TLS CA file is missing")
	}
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *transport.ConnectionError, got %T: %v", err, err)
	}
}

// TestConnect_NetworkFailure exercises the network branch of Connect when
// the underlying HTTP transport cannot reach the server. This covers the
// Start failure path inside Connect.
func TestConnect_NetworkFailure(t *testing.T) {
	// Reserve a port without listening to ensure the connection refuses.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	tr, err := New(transport.WithAddress("http://" + addr + "/mcp"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = tr.Connect(ctx)
	if err == nil {
		t.Fatal("expected error when target server is not listening")
	}
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *transport.ConnectionError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// applyRaftArgs / applyReadArgs direct unit tests
// ---------------------------------------------------------------------------

func TestApplyRaftArgs_PrefixedFields(t *testing.T) {
	args := map[string]any{}
	cfg := &transport.DatabaseRaftConfig{
		ClusterMode: "multi-raft",
		ReplicaID:   3,
		RaftAddress: "127.0.0.1:63001",
	}

	applyRaftArgs(args, cfg, "raft_")

	if args["raft_cluster_mode"] != "multi-raft" {
		t.Errorf("raft_cluster_mode: %v", args["raft_cluster_mode"])
	}
	// A shard ID is never sent: the cluster issues it at create.
	if v, ok := args["raft_shard_id"]; ok {
		t.Errorf("raft_shard_id sent: %v", v)
	}
	if args["raft_replica_id"].(uint64) != 3 {
		t.Errorf("raft_replica_id: %v", args["raft_replica_id"])
	}
	if args["raft_address"] != "127.0.0.1:63001" {
		t.Errorf("raft_address: %v", args["raft_address"])
	}
}

func TestApplyRaftArgs_UpdateMode(t *testing.T) {
	args := map[string]any{}
	cfg := &transport.DatabaseRaftConfig{
		ClusterMode: "single",
		ReplicaID:   1,
	}

	applyRaftArgs(args, cfg, "")

	if args["cluster_mode"] != "single" {
		t.Errorf("cluster_mode: %v", args["cluster_mode"])
	}
	// A database's shard ID never changes, so an update never sends one.
	if v, ok := args["shard_id"]; ok {
		t.Errorf("shard_id sent: %v", v)
	}
	if _, ok := args["raft_cluster_mode"]; ok {
		t.Error("expected raft_cluster_mode to be absent in update mode")
	}
}

func TestApplyRaftArgs_ZeroValuesOmitted(t *testing.T) {
	args := map[string]any{}
	cfg := &transport.DatabaseRaftConfig{}

	applyRaftArgs(args, cfg, "raft_")

	if len(args) != 0 {
		t.Errorf("expected empty args, got %+v", args)
	}
}

func TestApplyReadArgs_AllOptions(t *testing.T) {
	args := map[string]any{}

	applyReadArgs(args,
		transport.WithConsistency(transport.ConsistencyStrong),
		transport.WithReadTimeout(250),
		transport.WithAllowStale(),
	)

	if args["consistency"] != "strong" {
		t.Errorf("consistency: %v", args["consistency"])
	}
	if args["read_timeout_ms"].(int64) != 250 {
		t.Errorf("read_timeout_ms: %v", args["read_timeout_ms"])
	}
	if args["allow_stale"] != true {
		t.Errorf("allow_stale: %v", args["allow_stale"])
	}
}

func TestApplyReadArgs_Defaults(t *testing.T) {
	args := map[string]any{}

	applyReadArgs(args)

	if len(args) != 0 {
		t.Errorf("expected no args set with defaults, got %+v", args)
	}
}

// ---------------------------------------------------------------------------
// CreateDatabase_WithRaft to drive applyRaftArgs through the live path
// ---------------------------------------------------------------------------

func TestCreateDatabase_WithRaftAndProtocol(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "db-raft",
		Backend: "pebble",
		DataDir: "/tmp/db-raft",
		Mode:    "regional",
		Protocol: transport.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: true,
		},
		Raft: &transport.DatabaseRaftConfig{
			ClusterMode: "multi-raft",
			ReplicaID:   1,
			RaftAddress: "127.0.0.1:63002",
		},
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}

// ---------------------------------------------------------------------------
// Watch happy path with a real WatchService backed by watch.Hub
// ---------------------------------------------------------------------------

// connectWatchInProcess wires up a real watch hub + watch service on a
// fresh in-process MCP server and returns the transport, the hub (so the
// test can publish events), and a cleanup function.
func connectWatchInProcess(t *testing.T) (*Transport, *watch.Hub, func()) {
	t.Helper()

	hub := watch.NewHub()
	watchSvc := service.NewWatchService(hub)

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithWatch(watchSvc),
	)

	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
		hub.Close()
	}

	return tr, hub, cleanup
}

func TestWatch_PollDeliversEvents(t *testing.T) {
	tr, hub, cleanup := connectWatchInProcess(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "watch-key", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Publish an event; this hits the watch hub which the qrdb_watch tool
	// drains and returns to the SDK.
	go func() {
		// Allow the watcher goroutine to register at the server before
		// publishing the event. The hub.Watch call inside the tool handler
		// completes before its bounded collect-window opens, so we yield
		// briefly via a small timer-driven channel send.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hub.NotifyPut([]byte("watch-key"), nil, nil)
			}
		}
	}()

	// Wait for at least one batch with events.
	deadline := time.After(8 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				t.Fatal("watch channel closed before delivering events")
			}
			if len(resp.Events) > 0 {
				cancel()
				// Drain remaining responses until the channel closes.
				for range ch {
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for watch events")
		}
	}
}

func TestWatchPrefix_PollDeliversEvents(t *testing.T) {
	tr, hub, cleanup := connectWatchInProcess(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := tr.WatchPrefix(ctx, "prefix/", transport.WatchOptions{PrevKV: true})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hub.NotifyPut([]byte("prefix/a"), nil, nil)
				hub.NotifyDelete([]byte("prefix/b"), nil, nil)
			}
		}
	}()

	deadline := time.After(8 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				t.Fatal("watch channel closed before delivering events")
			}
			if len(resp.Events) > 0 {
				cancel()
				for range ch {
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for prefix watch events")
		}
	}
}

// TestWatchPoll_ContextCancellation verifies the watch goroutine exits
// cleanly when the caller cancels the context before any events arrive.
// This exercises the ctx.Done path in watchPoll's outer select.
func TestWatchPoll_ContextCancellation(t *testing.T) {
	tr, _, cleanup := connectWatchInProcess(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "never-fired", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Cancel immediately; the watch goroutine must drain and close the
	// channel without blocking.
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // Channel closed as expected.
			}
			// Some events may slip through before cancellation propagates;
			// keep draining until close.
		case <-deadline:
			t.Fatal("watch channel did not close after context cancel")
		}
	}
}

// ---------------------------------------------------------------------------
// Capabilities live JSON-decode path
// ---------------------------------------------------------------------------

func TestCapabilities_FullDecode(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	caps, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
	// The mock admin service reports KV=true, MVCC=true, etc.
	if !caps.KV || !caps.MVCC || !caps.Watch {
		t.Errorf("expected core capabilities reported true, got %+v", caps)
	}
}

// ---------------------------------------------------------------------------
// GetTopology live path
// ---------------------------------------------------------------------------

func TestGetTopology_Success(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	topo, err := tr.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if topo == nil || topo.NodeID == "" {
		t.Fatalf("expected populated topology, got %+v", topo)
	}
	if len(topo.Shards) == 0 {
		t.Fatal("expected at least one shard in topology")
	}
}

// ---------------------------------------------------------------------------
// Stub-tool MCP server for exercising JSON-decode happy paths of methods
// whose real server-side tools do not exist (external officer enroll).
// ---------------------------------------------------------------------------

// stubMCPServer creates a bare MCP server with hand-registered stub tools
// that return predictable JSON text. This lets the SDK exercise the JSON
// decode + response-mapping branches of methods whose canonical server-side
// tools are not yet implemented.
func stubMCPServer(stubs map[string]string) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("qrdb-stub", "1.0.0", mcpserver.WithToolCapabilities(false))
	for name, payload := range stubs {
		text := payload
		srv.AddTool(mcp.NewTool(name), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(text), nil
		})
	}
	return srv
}

func connectStubInProcess(t *testing.T, stubs map[string]string) (*Transport, func()) {
	t.Helper()
	srv := stubMCPServer(stubs)
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// Stub-driven watchPoll branch coverage
// ---------------------------------------------------------------------------

// stubMCPServerWithDynamic creates a server where each tool handler returns
// a different payload on each invocation, drained from a slice. After the
// slice is exhausted, the last entry is reused. This lets tests script a
// sequence of poll responses.
func stubMCPServerWithDynamic(t *testing.T, scripts map[string][]func() (*mcp.CallToolResult, error)) *mcpserver.MCPServer {
	t.Helper()
	srv := mcpserver.NewMCPServer("qrdb-stub-dyn", "1.0.0", mcpserver.WithToolCapabilities(false))
	for name, calls := range scripts {
		callList := calls
		var idx atomicCounter
		srv.AddTool(mcp.NewTool(name), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			i := idx.next()
			if i >= len(callList) {
				i = len(callList) - 1
			}
			return callList[i]()
		})
	}
	return srv
}

// atomicCounter is a tiny monotonic counter used by stubMCPServerWithDynamic
// to step through scripted responses without sharing mutable state across
// goroutines through unsynchronised closures.
type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) next() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.n
	c.n++
	return n
}

// TestWatchPoll_DeliversFullEvent exercises watchPoll's serverWatchResult
// decode plus the inner Kv / PrevKv unmarshal branches by returning a
// canned watch result with fully populated key-value events.
func TestWatchPoll_DeliversFullEvent(t *testing.T) {
	payload := `{
		"events": [
			{"type":"PUT","kv":{"Key":"a2V5","Value":"dmFs","CreateRevision":1,"ModRevision":1,"Version":1},
			 "prev_kv":{"Key":"a2V5","Value":"b2xk","CreateRevision":0,"ModRevision":0,"Version":0}},
			{"type":"DELETE","kv":{"Key":"a2V5","Value":"","CreateRevision":1,"ModRevision":2,"Version":0}}
		],
		"revision": 7,
		"watch_id": 99
	}`

	scripts := map[string][]func() (*mcp.CallToolResult, error){
		"qrdb_watch": {
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(payload), nil
			},
		},
	}

	srv := stubMCPServerWithDynamic(t, scripts)
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.Watch(ctx, "key", transport.WatchOptions{PrevKV: true, StartRevision: 1})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before delivering events")
			}
			if len(resp.Events) >= 2 && resp.Revision == 7 {
				cancel()
				for range ch {
				}
				return
			}
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for stub watch events")
		}
	}
}

// TestWatchPoll_TransientErrorRetries exercises the qrdb_watch error branch
// followed by recovery. The first call returns an error result; the second
// returns a valid (empty) result. The poll loop must retry after the
// transient error.
func TestWatchPoll_TransientErrorRetries(t *testing.T) {
	scripts := map[string][]func() (*mcp.CallToolResult, error){
		"qrdb_watch": {
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultError("temporary"), nil
			},
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(`{"events":[{"type":"PUT","kv":{"Key":"a2V5","Value":"dg=="}}],"revision":3,"watch_id":1}`), nil
			},
		},
	}

	srv := stubMCPServerWithDynamic(t, scripts)
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.Watch(ctx, "key", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before delivering events")
			}
			if len(resp.Events) > 0 {
				cancel()
				for range ch {
				}
				return
			}
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for retry-success watch events")
		}
	}
}

// TestWatchPoll_JSONDecodeError exercises the json.Unmarshal error branch in
// watchPoll by returning malformed JSON, then a valid empty response so the
// loop continues without crashing.
func TestWatchPoll_JSONDecodeError(t *testing.T) {
	scripts := map[string][]func() (*mcp.CallToolResult, error){
		"qrdb_watch": {
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("not-json"), nil
			},
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText(`{"events":[{"type":"PUT","kv":{"Key":"a2V5","Value":"dg=="}}],"revision":11,"watch_id":1}`), nil
			},
		},
	}

	srv := stubMCPServerWithDynamic(t, scripts)
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.Watch(ctx, "key", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before recovery")
			}
			if len(resp.Events) > 0 && resp.Revision == 11 {
				cancel()
				for range ch {
				}
				return
			}
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for post-decode-error events")
		}
	}
}

// TestWatchPoll_ServerCanceledWatch exercises the isWatchCanceledError
// branch by returning a tool error whose message contains "watch canceled".
// The transport must deliver a WatchResponse{Canceled: true} and close
// the channel.
func TestWatchPoll_ServerCanceledWatch(t *testing.T) {
	scripts := map[string][]func() (*mcp.CallToolResult, error){
		"qrdb_watch": {
			func() (*mcp.CallToolResult, error) {
				return mcp.NewToolResultError("watch canceled by server"), nil
			},
		},
	}

	srv := stubMCPServerWithDynamic(t, scripts)
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "key", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	saw := false
	for resp := range ch {
		if resp.Canceled {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected at least one Canceled=true response before channel close")
	}
}
