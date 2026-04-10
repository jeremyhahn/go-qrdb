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

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// qrdbd binary build — once per test binary run
// ---------------------------------------------------------------------------

var (
	buildOnce   sync.Once
	builtBinary string
	buildErr    error
)

// buildQRDBd compiles the qrdbd daemon binary into an os.MkdirTemp directory
// and returns its path. The binary is built once per test run.
//
// os.MkdirTemp is used rather than t.TempDir() because sync.Once captures
// the first caller's *testing.T. Using t.TempDir() would cause the directory
// to be removed when that first test finishes, breaking all subsequent tests.
func buildQRDBd() string {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "qrdbd-sqlite-test-*")
		if err != nil {
			buildErr = fmt.Errorf("MkdirTemp: %w", err)
			return
		}
		builtBinary = filepath.Join(dir, "qrdbd")
		cmd := exec.Command("go", "build", "-buildvcs=false",
			"-tags", "codec_json raft qrdb_ts qrdb_vector qrdb_mysql qrdb_pgsql",
			"-o", builtBinary, "./cmd/qrdbd")
		cmd.Dir = projectRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build failed: %w\n%s", err, out)
		}
	})
	return builtBinary
}

// projectRoot walks up from the current working directory to find the
// go.mod that declares module github.com/jeremyhahn/go-qrdb. This works
// whether tests run from within the module or a subdirectory.
func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		modFile := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modFile); err == nil {
			if strings.Contains(string(data), "module github.com/jeremyhahn/go-qrdb\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback: walk up until we find any go.mod (for monorepo layouts).
	dir, _ = os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir
}

// ---------------------------------------------------------------------------
// testCA — ephemeral certificate authority for test TLS
// ---------------------------------------------------------------------------

// testCA holds a CA certificate and key for generating test TLS certificates.
type testCA struct {
	CertPEM  []byte
	CertPath string
	Pool     *x509.CertPool
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
}

// generateTestCA creates an ephemeral ECDSA P-256 CA certificate for
// integration tests. The CA cert is written to a temp file referenced by
// testCA.CertPath.
func generateTestCA(dir string) (*testCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("CA serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"qrdb-sqlite-test"},
			CommonName:   "test-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}

	return &testCA{
		CertPEM:  certPEM,
		CertPath: caFile,
		Pool:     pool,
		cert:     cert,
		key:      key,
	}, nil
}

// generateNodeCert creates a leaf TLS certificate signed by this CA with
// SANs covering localhost, 127.0.0.1, and all local interface addresses.
// Both server auth and client auth extended key usages are included.
// The cert and key are written to dir/node-cert.pem and dir/node-key.pem.
func (ca *testCA) generateNodeCert(dir string) (certPath, keyPath string, err error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate node key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("node serial: %w", err)
	}

	dnsNames := []string{"localhost"}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if hostname, herr := os.Hostname(); herr == nil && hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	if addrs, aerr := net.InterfaceAddrs(); aerr == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				ips = append(ips, ipNet.IP)
			}
		}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"qrdb-sqlite-test"},
			CommonName:   "test-node",
		},
		DNSNames:    dnsNames,
		IPAddresses: ips,
		NotBefore:   time.Now().Add(-1 * time.Minute),
		NotAfter:    time.Now().Add(2 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		return "", "", fmt.Errorf("create node cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal node key: %w", err)
	}

	certPath = filepath.Join(dir, "node-cert.pem")
	keyPath = filepath.Join(dir, "node-key.pem")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0600); err != nil {
		return "", "", fmt.Errorf("write node cert: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return "", "", fmt.Errorf("write node key: %w", err)
	}

	return certPath, keyPath, nil
}

// generateClientCert creates a client mTLS certificate signed by this CA with
// CN=admin and OU=default. This matches the admin user bootstrapped by qrdbd
// so the mTLS authenticator grants superadmin access. The cert and key are
// written to dir/client-cert.pem and dir/client-key.pem.
func (ca *testCA) generateClientCert(dir string) (certPath, keyPath string, err error) {
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate client key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("client serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{"qrdb-sqlite-test"},
			CommonName:         "admin",
			OrganizationalUnit: []string{"default"},
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(2 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &clientKey.PublicKey, ca.key)
	if err != nil {
		return "", "", fmt.Errorf("create client cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal client key: %w", err)
	}

	certPath = filepath.Join(dir, "client-cert.pem")
	keyPath = filepath.Join(dir, "client-key.pem")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0600); err != nil {
		return "", "", fmt.Errorf("write client cert: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return "", "", fmt.Errorf("write client key: %w", err)
	}

	return certPath, keyPath, nil
}

// ---------------------------------------------------------------------------
// testServer — subprocess lifecycle
// ---------------------------------------------------------------------------

// testServer holds the addresses and resources of the shared qrdbd subprocess.
type testServer struct {
	restPort       int
	grpcPort       int
	quicPort       int // 0 when QUIC is disabled
	mcpPort        int
	unixSocket     string
	dataDir        string
	ca             *testCA
	clientCertPath string
	clientKeyPath  string
	cmd            *exec.Cmd
}

// restAddr returns the REST server address.
func (s *testServer) restAddr() string {
	return fmt.Sprintf("https://127.0.0.1:%d", s.restPort)
}

// grpcAddr returns the gRPC server address (host:port, no scheme).
func (s *testServer) grpcAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.grpcPort)
}

// quicAddr returns the QUIC server address (host:port, no scheme).
func (s *testServer) quicAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.quicPort)
}

// mcpAddr returns the MCP server address with https:// scheme required by the SSE transport.
func (s *testServer) mcpAddr() string {
	return fmt.Sprintf("https://127.0.0.1:%d", s.mcpPort)
}

// addrFor returns the protocol-appropriate address string.
func (s *testServer) addrFor(proto protocol) string {
	switch proto {
	case protoREST:
		return s.restAddr()
	case protoGRPC:
		return s.grpcAddr()
	case protoUnix:
		return s.unixSocket
	case protoQUIC:
		return s.quicAddr()
	case protoMCP:
		return s.mcpAddr()
	default:
		return ""
	}
}

// stop sends SIGINT to the qrdbd process and waits for it to exit.
func (s *testServer) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	_ = s.cmd.Wait()
}

// startSharedServer builds qrdbd, generates ephemeral TLS certificates, starts
// a single-node cluster with all protocols enabled, and waits for it to be
// healthy and elected leader. It is called once from TestMain.
func startSharedServer() (*testServer, error) {
	bin := buildQRDBd()
	if buildErr != nil {
		return nil, fmt.Errorf("build qrdbd: %w", buildErr)
	}

	// Create a persistent temp directory for all server files. This must
	// outlive TestMain so os.MkdirTemp is used instead of t.TempDir().
	dir, err := os.MkdirTemp("", "qrdbd-sqlite-server-*")
	if err != nil {
		return nil, fmt.Errorf("MkdirTemp: %w", err)
	}

	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data: %w", err)
	}

	certDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir certs: %w", err)
	}

	// Generate CA + server cert + client (admin) cert.
	ca, err := generateTestCA(certDir)
	if err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}

	nodeCert, nodeKey, err := ca.generateNodeCert(certDir)
	if err != nil {
		return nil, fmt.Errorf("generate node cert: %w", err)
	}

	clientCert, clientKey, err := ca.generateClientCert(certDir)
	if err != nil {
		return nil, fmt.Errorf("generate client cert: %w", err)
	}

	// Allocate free ports for all protocols. Ports are allocated as a batch
	// so the OS does not reassign them between allocation and bind.
	raftPort, err := allocateTCPPort()
	if err != nil {
		return nil, fmt.Errorf("allocate raft port: %w", err)
	}
	restPort, err := allocateTCPPort()
	if err != nil {
		return nil, fmt.Errorf("allocate rest port: %w", err)
	}
	grpcPort, err := allocateTCPPort()
	if err != nil {
		return nil, fmt.Errorf("allocate grpc port: %w", err)
	}
	mcpPort, err := allocateTCPPort()
	if err != nil {
		return nil, fmt.Errorf("allocate mcp port: %w", err)
	}

	unixSocket := filepath.Join(dir, "qrdbd.sock")

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--unix-socket-path", unixSocket,
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCert,
		"--tls-key-file", nodeKey,
		"--enable-rest=true",
		"--enable-grpc=true",
		"--enable-unix=true",
		"--enable-mcp=true",
		"--log-level", "warn",
		"--log-format", "json",
	}

	lw := &stderrLogWriter{prefix: "[qrdbd] "}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lw
	cmd.Stderr = lw

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start qrdbd: %w", err)
	}

	srv := &testServer{
		restPort:       restPort,
		grpcPort:       grpcPort,
		quicPort:       0, // QUIC not enabled; avoids QUIC test failures on hosts without UDP
		mcpPort:        mcpPort,
		unixSocket:     unixSocket,
		dataDir:        dataDir,
		ca:             ca,
		clientCertPath: clientCert,
		clientKeyPath:  clientKey,
		cmd:            cmd,
	}

	// Wait for the REST health endpoint to return 200 and for a leader to
	// be elected before returning. Use a 120-second deadline to accommodate
	// slow CI environments.
	if err := waitForHealthy(srv, 120*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("wait for health: %w", err)
	}

	if err := waitForLeader(srv, 60*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("wait for leader: %w", err)
	}

	return srv, nil
}

// ---------------------------------------------------------------------------
// Port allocation
// ---------------------------------------------------------------------------

// allocateTCPPort finds a free TCP port by binding a listener on :0 and
// immediately closing it. There is a TOCTOU race between the close and the
// server bind, but it is accepted for test infrastructure.
func allocateTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// ---------------------------------------------------------------------------
// Health and leader wait
// ---------------------------------------------------------------------------

// waitForHealthy polls the REST /health endpoint until it returns HTTP 200 or
// the deadline is reached. It uses a CA-only TLS client (no mTLS) since the
// /health endpoint is typically public.
func waitForHealthy(srv *testServer, timeout time.Duration) error {
	url := fmt.Sprintf("https://127.0.0.1:%d/health", srv.restPort)
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    srv.ca.Pool,
				MinVersion: tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
		},
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health check status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("qrdbd not healthy after %v (last: %v)", timeout, lastErr)
}

// waitForLeader polls the REST /status endpoint using mTLS until at least one
// shard reports is_leader:true, or the deadline is reached.
func waitForLeader(srv *testServer, timeout time.Duration) error {
	// Load client cert for mTLS.
	clientCert, err := tls.LoadX509KeyPair(srv.clientCertPath, srv.clientKeyPath)
	if err != nil {
		return fmt.Errorf("load client cert: %w", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      srv.ca.Pool,
				Certificates: []tls.Certificate{clientCert},
				MinVersion:   tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
		},
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/status", srv.restPort)
	deadline := time.Now().Add(timeout)
	var lastDiag string

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastDiag = fmt.Sprintf("GET /status error: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil || resp.StatusCode != http.StatusOK {
			lastDiag = fmt.Sprintf("status %d: %s", resp.StatusCode, string(body))
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var result map[string]interface{}
		if jerr := json.Unmarshal(body, &result); jerr != nil {
			lastDiag = fmt.Sprintf("decode: %v", jerr)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if shards, ok := result["shards"].([]interface{}); ok {
			for _, s := range shards {
				shard, ok := s.(map[string]interface{})
				if !ok {
					continue
				}
				if isLeader, ok := shard["is_leader"].(bool); ok && isLeader {
					return nil
				}
			}
		}
		lastDiag = "no shard reporting is_leader"
		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf("no leader elected after %v (last: %s)", timeout, lastDiag)
}

// ---------------------------------------------------------------------------
// stderrLogWriter — io.Writer that writes to os.Stderr with a prefix
// ---------------------------------------------------------------------------

// stderrLogWriter forwards qrdbd subprocess output to os.Stderr, prefixing
// each line. This keeps server logs visible in verbose test output (-v) without
// requiring a *testing.T, which is unavailable from TestMain cleanup handlers.
type stderrLogWriter struct {
	prefix string
	mu     sync.Mutex
	buf    []byte
}

func (w *stderrLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		w.buf = w.buf[idx+1:]
		fmt.Fprintf(os.Stderr, "%s%s\n", w.prefix, line)
	}
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Test adapters — thin wrappers around the SDK client
//
// Each adapter satisfies the corresponding vtab store interface and delegates
// all operations to the real SDK client, which calls the live qrdbd server
// over the configured transport.
// ---------------------------------------------------------------------------

// testKVAdapter wraps the SDK KVClient to satisfy vtab.KVStore.
type testKVAdapter struct {
	kv *sdk.Client
}

func (a *testKVAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := a.kv.KV().Get(ctx, key)
	if err != nil {
		// KeyNotFoundError is not an error for the vtab layer — return nil.
		if isKeyNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

func (a *testKVAdapter) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.kv.KV().Scan(ctx, prefix)
}

func (a *testKVAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.kv.KV().Put(ctx, key, value)
}

func (a *testKVAdapter) Delete(ctx context.Context, key string) error {
	return a.kv.KV().Delete(ctx, key)
}

// testDatapointAdapter wraps the SDK TimeSeriesClient to satisfy
// vtab.DatapointStore.
type testDatapointAdapter struct {
	ts *sdk.Client
}

func (a *testDatapointAdapter) WriteDatapoint(ctx context.Context, metric string, timestampNs int64, value float64, tagsJSON string) error {
	dp := &sdktransport.TSDataPoint{
		Metric:      metric,
		TimestampNs: timestampNs,
		Value:       value,
		Tags:        parseTSTagsJSON(tagsJSON),
	}
	return a.ts.TimeSeries().TSWrite(ctx, dp)
}

func (a *testDatapointAdapter) QueryDatapoints(ctx context.Context, metric string, startNs, endNs int64) ([]vtab.DatapointRow, error) {
	resp, err := a.ts.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
		Metric:  metric,
		StartNs: startNs,
		EndNs:   endNs,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DatapointRow, 0, len(resp.Points))
	for _, p := range resp.Points {
		rows = append(rows, vtab.DatapointRow{
			Metric:      p.Metric,
			TimestampNs: p.TimestampNs,
			Value:       p.Value,
			Tags:        tsTagsToJSON(p.Tags),
		})
	}
	return rows, nil
}

func (a *testDatapointAdapter) ScanAllDatapoints(ctx context.Context) ([]vtab.DatapointRow, error) {
	resp, err := a.ts.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
		Metric:  "",
		StartNs: 0,
		EndNs:   1<<62 - 1,
	})
	if err != nil {
		// If unsupported (server requires a non-empty metric), return empty so
		// the vtab falls back to per-metric queries from its filter.
		return nil, nil
	}
	rows := make([]vtab.DatapointRow, 0, len(resp.Points))
	for _, p := range resp.Points {
		rows = append(rows, vtab.DatapointRow{
			Metric:      p.Metric,
			TimestampNs: p.TimestampNs,
			Value:       p.Value,
			Tags:        tsTagsToJSON(p.Tags),
		})
	}
	return rows, nil
}

// parseTSTagsJSON converts a JSON-encoded tags string (the vtab format) into
// the SDK's []TSTag slice. The vtab canonical format is a JSON array of
// {"key": "...", "value": "..."} objects. Returns nil for empty or invalid input.
func parseTSTagsJSON(tagsJSON string) []sdktransport.TSTag {
	if tagsJSON == "" || tagsJSON == "[]" || tagsJSON == "null" {
		return nil
	}
	var tags []sdktransport.TSTag
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		// Non-array format (e.g., a JSON object): treat as unparseable, discard.
		return nil
	}
	return tags
}

// tsTagsToJSON serialises a []TSTag slice to the vtab canonical JSON format.
// Returns "[]" for an empty or nil slice to keep the column non-NULL.
func tsTagsToJSON(tags []sdktransport.TSTag) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// testMetricsAdapter wraps the SDK TimeSeriesClient to satisfy vtab.MetricsStore.
type testMetricsAdapter struct {
	ts *sdk.Client
}

func (a *testMetricsAdapter) ListMetrics(ctx context.Context) ([]vtab.MetricInfo, error) {
	resp, err := a.ts.TimeSeries().TSListMetrics(ctx, &sdktransport.TSListMetricsRequest{Prefix: ""})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.MetricInfo, 0, len(resp.Metrics))
	for _, m := range resp.Metrics {
		infos = append(infos, vtab.MetricInfo{Name: m})
	}
	return infos, nil
}

func (a *testMetricsAdapter) GetMetric(ctx context.Context, name string) (*vtab.MetricInfo, error) {
	resp, err := a.ts.TimeSeries().TSListMetrics(ctx, &sdktransport.TSListMetricsRequest{Prefix: name})
	if err != nil {
		return nil, err
	}
	for _, m := range resp.Metrics {
		if m == name {
			return &vtab.MetricInfo{Name: m}, nil
		}
	}
	return nil, nil
}

// testNamespaceAdapter wraps the SDK VectorClient to satisfy vtab.NamespaceStore.
type testNamespaceAdapter struct {
	vec *sdk.Client
}

func (a *testNamespaceAdapter) ListNamespaces(ctx context.Context) ([]vtab.NamespaceInfo, error) {
	resp, err := a.vec.Vector().ListNamespaces(ctx, &sdktransport.ListNamespacesRequest{})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.NamespaceInfo, 0, len(resp.Namespaces))
	for _, ns := range resp.Namespaces {
		infos = append(infos, vtab.NamespaceInfo{
			Name:           ns.Namespace,
			Dimension:      ns.Dimension,
			DistanceMetric: ns.DistanceMetric,
			IndexType:      ns.IndexType,
		})
	}
	return infos, nil
}

func (a *testNamespaceAdapter) GetNamespace(ctx context.Context, name string) (*vtab.NamespaceInfo, error) {
	resp, err := a.vec.Vector().GetNamespaceInfo(ctx, &sdktransport.GetNamespaceInfoRequest{Namespace: name})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if resp.Namespace == "" {
		return nil, nil
	}
	return &vtab.NamespaceInfo{
		Name:           resp.Namespace,
		Dimension:      resp.Dimension,
		DistanceMetric: resp.DistanceMetric,
		IndexType:      resp.IndexType,
	}, nil
}

func (a *testNamespaceAdapter) CreateNamespace(ctx context.Context, name string, dimension uint32, distanceMetric, indexType string) error {
	_, err := a.vec.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
		Namespace:      name,
		Dimension:      dimension,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
	})
	return err
}

func (a *testNamespaceAdapter) DeleteNamespace(ctx context.Context, name string) error {
	_, err := a.vec.Vector().DeleteNamespace(ctx, &sdktransport.DeleteNamespaceRequest{Namespace: name})
	return err
}

// testVectorAdapter wraps the SDK VectorClient to satisfy vtab.VectorStore.
// The vtab layer stores vectors as raw little-endian float32 bytes and metadata
// as a JSON string. The SDK uses []float32 and map[string]interface{}.
type testVectorAdapter struct {
	vec *sdk.Client
}

func (a *testVectorAdapter) UpsertVector(ctx context.Context, namespace string, id uint64, vector []byte, metadata string) error {
	floats := rawBytesToFloat32s(vector)
	metaMap := jsonStringToMap(metadata)
	_, err := a.vec.Vector().UpsertVector(ctx, &sdktransport.UpsertVectorRequest{
		Namespace: namespace,
		ID:        id,
		Vector:    floats,
		Metadata:  metaMap,
	})
	return err
}

func (a *testVectorAdapter) GetVector(ctx context.Context, namespace string, id uint64) (*vtab.VectorRow, error) {
	resp, err := a.vec.Vector().GetVector(ctx, &sdktransport.GetVectorRequest{
		Namespace:     namespace,
		ID:            id,
		IncludeVector: true,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !resp.Found {
		return nil, nil
	}
	return &vtab.VectorRow{
		ID:        resp.ID,
		Namespace: namespace,
		Vector:    float32sToRawBytes(resp.Vector),
		Metadata:  mapToJSONString(resp.Metadata),
	}, nil
}

func (a *testVectorAdapter) ListVectors(ctx context.Context, namespace string) ([]vtab.VectorRow, error) {
	resp, err := a.vec.Vector().ListVectors(ctx, &sdktransport.ListVectorsRequest{
		Namespace:      namespace,
		IncludeVectors: true,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.VectorRow, 0, len(resp.Vectors))
	for _, v := range resp.Vectors {
		rows = append(rows, vtab.VectorRow{
			ID:        v.ID,
			Namespace: namespace,
			Vector:    float32sToRawBytes(v.Vector),
			Metadata:  mapToJSONString(v.Metadata),
		})
	}
	return rows, nil
}

func (a *testVectorAdapter) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	_, err := a.vec.Vector().DeleteVector(ctx, &sdktransport.DeleteVectorRequest{
		Namespace: namespace,
		ID:        id,
	})
	return err
}

// testCollectionAdapter wraps the SDK CollectionClient to satisfy vtab.CollectionStore.
type testCollectionAdapter struct {
	coll *sdk.Client
}

func (a *testCollectionAdapter) GetDocument(ctx context.Context, database, collection string, id uint64) (map[string]any, error) {
	resp, err := a.coll.Collections().GetDocument(ctx, database, collection, id)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Document, nil
}

func (a *testCollectionAdapter) ListDocuments(ctx context.Context, database, collection string) ([]vtab.DocumentRow, error) {
	resp, err := a.coll.Collections().ListDocuments(ctx, sdktransport.ListDocumentsRequest{
		Database:   database,
		Collection: collection,
		PageSize:   10000,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DocumentRow, 0, len(resp.Documents))
	for _, d := range resp.Documents {
		rows = append(rows, vtab.DocumentRow{ID: d.ID, Fields: d.Document})
	}
	return rows, nil
}

func (a *testCollectionAdapter) InsertDocument(ctx context.Context, database, collection string, doc map[string]any) (uint64, error) {
	resp, err := a.coll.Collections().InsertDocument(ctx, sdktransport.InsertDocumentRequest{
		Database:   database,
		Collection: collection,
		Document:   doc,
	})
	if err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (a *testCollectionAdapter) UpdateDocument(ctx context.Context, database, collection string, id uint64, doc map[string]any) error {
	return a.coll.Collections().UpdateDocument(ctx, sdktransport.UpdateDocumentRequest{
		Database:   database,
		Collection: collection,
		ID:         id,
		Document:   doc,
	})
}

func (a *testCollectionAdapter) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	return a.coll.Collections().DeleteDocument(ctx, database, collection, id)
}

// ---------------------------------------------------------------------------
// Type conversion helpers for vector adapter
// ---------------------------------------------------------------------------

// rawBytesToFloat32s converts a little-endian []byte representation of float32
// values back to []float32 for the SDK.
func rawBytesToFloat32s(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	n := len(b) / 4
	floats := make([]float32, n)
	for i := range floats {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		floats[i] = math.Float32frombits(bits)
	}
	return floats
}

// float32sToRawBytes converts []float32 back to the little-endian []byte
// format used by vtab.VectorRow.Vector.
func float32sToRawBytes(floats []float32) []byte {
	if len(floats) == 0 {
		return nil
	}
	b := make([]byte, len(floats)*4)
	for i, f := range floats {
		bits := math.Float32bits(f)
		b[i*4] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}

// jsonStringToMap parses a JSON metadata string into a map. Returns nil when
// the string is empty or malformed.
func jsonStringToMap(s string) map[string]interface{} {
	if s == "" {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// mapToJSONString serialises a metadata map to a JSON string. Returns an
// empty string when m is nil.
func mapToJSONString(m map[string]interface{}) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ---------------------------------------------------------------------------
// Float32 / byte encoding helpers used by vector tests
// ---------------------------------------------------------------------------

// float32sToBytes encodes a []float32 as a little-endian byte slice.
func float32sToBytes(floats []float32) []byte {
	if len(floats) == 0 {
		return nil
	}
	b := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// bytesToFloat32s decodes a little-endian byte slice into []float32.
func bytesToFloat32s(t *testing.T, b []byte) []float32 {
	t.Helper()
	if len(b)%4 != 0 {
		t.Fatalf("bytesToFloat32s: byte length %d is not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = math.Float32frombits(bits)
	}
	return out
}

// ---------------------------------------------------------------------------
// Error classification helpers
// ---------------------------------------------------------------------------

// isKeyNotFound returns true when err indicates a missing key.
func isKeyNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "key not found") ||
		strings.Contains(msg, "KeyNotFound") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "404")
}

// isNotFound returns true when err indicates a resource was not found.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "NotFound") ||
		strings.Contains(msg, "404")
}

// ---------------------------------------------------------------------------
// Unique key helpers
// ---------------------------------------------------------------------------

// uniqueKey generates a protocol-namespaced key to prevent collisions between
// protocol subtests that share a qrdbd instance.
func uniqueKey(proto protocol, prefix string) string {
	return fmt.Sprintf("%s/%s", proto, prefix)
}

// uniqueMetric generates a protocol-namespaced metric name.
func uniqueMetric(proto protocol, name string) string {
	return fmt.Sprintf("%s.%s", proto, name)
}

// uniqueNamespace generates a protocol-namespaced vector namespace name.
func uniqueNamespace(proto protocol, name string) string {
	return fmt.Sprintf("%s-%s", proto, name)
}

// protoDocID generates a protocol-unique document _id to avoid collisions
// between parallel protocol subtests that share the same collection. Each
// protocol gets a separate range: rest=10xx, grpc=20xx, unix=30xx, quic=40xx, mcp=50xx.
func protoDocID(proto protocol, base int64) int64 {
	switch proto {
	case protoREST:
		return 1000000 + base
	case protoGRPC:
		return 2000000 + base
	case protoUnix:
		return 3000000 + base
	case protoQUIC:
		return 4000000 + base
	case protoMCP:
		return 5000000 + base
	default:
		return base
	}
}

// ---------------------------------------------------------------------------
// SQL assertion and query helpers
// ---------------------------------------------------------------------------

// assertRowCount executes query and asserts that exactly expected rows are
// returned.
func assertRowCount(t *testing.T, db *sql.DB, query string, expected int, args ...any) {
	t.Helper()
	rows, err := db.Query(query, args...)
	require.NoError(t, err, "query: %s", query)
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	require.NoError(t, rows.Err(), "rows.Err: %s", query)
	require.Equal(t, expected, n, "row count mismatch for: %s", query)
}

// assertKVValue queries the kv virtual table for key and asserts that the
// value column equals expected.
func assertKVValue(t *testing.T, db *sql.DB, key, expected string) {
	t.Helper()
	var got string
	err := db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got)
	require.NoError(t, err, "SELECT kv key=%q", key)
	require.Equal(t, expected, got)
}

// insertKVBatch inserts count key-value pairs with keys "<prefix>/<i>" and
// values "<prefix>-value-<i>".
func insertKVBatch(t *testing.T, db *sql.DB, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%s/%d", prefix, i)
		val := fmt.Sprintf("%s-value-%d", prefix, i)
		_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, val)
		require.NoError(t, err, "INSERT kv key=%q", key)
	}
}

// scanAllRows executes query and returns every result row as a map from column
// name to raw value.
func scanAllRows(t *testing.T, db *sql.DB, query string, args ...any) []map[string]any {
	t.Helper()
	rows, err := db.Query(query, args...)
	require.NoError(t, err, "query: %s", query)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)

	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}
