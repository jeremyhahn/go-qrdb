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
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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
)

// testCA holds a CA certificate and key for generating test TLS certificates.
type testCA struct {
	CertPEM  []byte
	CertPath string
	Pool     *x509.CertPool
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
}

// generateTestCA creates an ephemeral ECDSA P-256 CA certificate for
// integration tests. The CA cert is written to a temp file and the path
// is available as testCA.CertPath.
func generateTestCA(t *testing.T) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateTestCA: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generateTestCA: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-test"},
			CommonName:   "test-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("generateTestCA: create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("generateTestCA: parse cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		t.Fatalf("generateTestCA: write: %v", err)
	}

	return &testCA{
		CertPEM:  certPEM,
		CertPath: caFile,
		Pool:     pool,
		cert:     cert,
		key:      key,
	}
}

// generateNodeCert creates a leaf certificate signed by this CA with
// SANs for all local hostnames and interface IPs. The cert and key
// are written to temp files and their paths are returned.
func (ca *testCA) generateNodeCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateNodeCert: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generateNodeCert: serial: %v", err)
	}

	dnsNames := []string{"localhost"}
	ips := []net.IP{
		net.IPv4(127, 0, 0, 1),
		net.IPv6loopback,
		net.IPv4zero,
		net.IPv6zero,
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				ips = append(ips, ipNet.IP)
			}
		}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-test"},
			CommonName:   "test-node",
		},
		DNSNames:    dnsNames,
		IPAddresses: ips,
		NotBefore:   time.Now().Add(-1 * time.Minute),
		NotAfter:    time.Now().Add(1 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("generateNodeCert: create cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("generateNodeCert: marshal key: %v", err)
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certPath, leafCertPEM, 0600); err != nil {
		t.Fatalf("generateNodeCert: write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, leafKeyPEM, 0600); err != nil {
		t.Fatalf("generateNodeCert: write key: %v", err)
	}

	return certPath, keyPath
}

// generateAdminClientCert creates a client-only certificate signed by this
// CA with CN=admin and OU=tenantID. This matches the admin user created by
// bootstrapRolesAndAdmin so the mTLS authenticator recognizes it. The cert
// and key are written to {dataDir}/admin-cert.pem and admin-key.pem.
func (ca *testCA) generateAdminClientCert(t *testing.T, dataDir string) (certPath, keyPath string) {
	t.Helper()

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateAdminClientCert: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generateAdminClientCert: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{"go-quicraft-test"},
			CommonName:         "admin",
			OrganizationalUnit: []string{"default"},
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &clientKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("generateAdminClientCert: create cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("generateAdminClientCert: marshal key: %v", err)
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	certPath = filepath.Join(dataDir, "admin-cert.pem")
	keyPath = filepath.Join(dataDir, "admin-key.pem")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("generateAdminClientCert: mkdir: %v", err)
	}
	if err := os.WriteFile(certPath, clientCertPEM, 0600); err != nil {
		t.Fatalf("generateAdminClientCert: write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, clientKeyPEM, 0600); err != nil {
		t.Fatalf("generateAdminClientCert: write key: %v", err)
	}

	return certPath, keyPath
}

// tlsFlags returns the CLI flags for TLS cert files for a single node.
func (ca *testCA) tlsFlags(t *testing.T) []string {
	t.Helper()
	certPath, keyPath := ca.generateNodeCert(t)
	return []string{
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", certPath,
		"--tls-key-file", keyPath,
	}
}

// caHTTPClient returns an HTTP client that verifies server certificates
// using the given CA certificate pool. This client does NOT present a
// client certificate, so it can only access public endpoints like /health.
func caHTTPClient(pool *x509.CertPool, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
		},
	}
}

// mtlsHTTPClient returns an HTTP client that presents a client certificate
// signed by the given CA with CN=admin and OU=default. This matches the
// admin user created by bootstrapRolesAndAdmin, so the mTLS authenticator
// + RBAC authorizer grants superadmin access to all endpoints.
func mtlsHTTPClient(t *testing.T, ca *testCA, timeout time.Duration) *http.Client {
	t.Helper()

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("mtlsHTTPClient: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("mtlsHTTPClient: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{"go-quicraft-test"},
			CommonName:         "admin",
			OrganizationalUnit: []string{"default"},
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &clientKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("mtlsHTTPClient: create cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("mtlsHTTPClient: marshal key: %v", err)
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatalf("mtlsHTTPClient: X509KeyPair: %v", err)
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      ca.Pool,
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
		},
	}
}

// buildBinary compiles the qrdbd daemon binary into a temporary directory
// and returns its path. The binary is built once per test run.
//
// Note: os.MkdirTemp is used instead of t.TempDir() because sync.Once
// captures the first caller's t. If t.TempDir() were used, the directory
// would be cleaned up when that first test finishes, causing all
// subsequent tests to fail with "no such file or directory".
var (
	buildOnce   sync.Once
	builtBinary string
	buildErr    error
)

func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "qrdbd-cli-test-*")
		if err != nil {
			buildErr = fmt.Errorf("MkdirTemp: %v", err)
			return
		}
		builtBinary = filepath.Join(dir, "qrdbd")
		cmd := exec.Command("go", "build", "-buildvcs=false",
			"-tags", "codec_json raft qrdb_ts qrdb_vector qrdb_mysql qrdb_pgsql",
			"-o", builtBinary, "./cmd/qrdbd")
		cmd.Dir = projectRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build failed: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatalf("buildBinary: %v", buildErr)
	}
	return builtBinary
}

// projectRoot returns the module root directory by walking up from the
// current working directory to find go.mod.
func projectRoot() string {
	dir, _ := os.Getwd()
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

// logWriter is an io.Writer that forwards lines to t.Log with a prefix.
type logWriter struct {
	t      *testing.T
	prefix string
	mu     sync.Mutex
	buf    []byte
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := -1
		for i, b := range w.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		w.buf = w.buf[idx+1:]
		w.t.Logf("%s%s", w.prefix, line)
	}
	return len(p), nil
}

// startNode starts the qrdbd binary with the given arguments and returns
// the exec.Cmd. Stdout and stderr are piped through t.Log with the given prefix.
// The process is killed on test cleanup.
func startNode(t *testing.T, binary string, args []string, prefix string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Stdout = &logWriter{t: t, prefix: prefix}
	cmd.Stderr = &logWriter{t: t, prefix: prefix}
	if err := cmd.Start(); err != nil {
		t.Fatalf("startNode: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd
}

// allocatePorts returns n free UDP ports for node listen addresses.
// All listeners are held open until all ports are determined, preventing
// the OS from reassigning a port within the same batch.
func allocatePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	listeners := make([]net.PacketConn, n)
	for i := range n {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			for j := range i {
				listeners[j].Close()
			}
			t.Fatalf("allocatePorts: %v", err)
		}
		listeners[i] = pc
		ports[i] = pc.LocalAddr().(*net.UDPAddr).Port
	}
	for _, pc := range listeners {
		pc.Close()
	}
	return ports
}

// buildPeersFlag builds the --peers flag value from ports.
// Format: "1=127.0.0.1:port1,2=127.0.0.1:port2,..."
func buildPeersFlag(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = fmt.Sprintf("%d=127.0.0.1:%d", i+1, port)
	}
	return strings.Join(parts, ",")
}

// waitForHealthy polls for the host.id file in the data directory
// to indicate the node has started successfully.
func waitForHealthy(t *testing.T, dataDir string, timeout time.Duration) {
	t.Helper()
	hostIDPath := filepath.Join(dataDir, "nodehost", "host.id")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(hostIDPath); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, hostIDPath)
}

// runCLI runs the binary with args and returns the combined output and error.
// A 30-second timeout prevents tests from hanging if the binary doesn't exit.
func runCLI(t *testing.T, binary string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// allocateTCPPorts returns n free TCP ports for admin server addresses.
// All listeners are held open until all ports are determined, preventing
// the OS from reassigning a port within the same batch.
func allocateTCPPorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	listeners := make([]net.Listener, n)
	for i := range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for j := range i {
				listeners[j].Close()
			}
			t.Fatalf("allocateTCPPorts: %v", err)
		}
		listeners[i] = ln
		ports[i] = ln.Addr().(*net.TCPAddr).Port
	}
	for _, ln := range listeners {
		ln.Close()
	}
	return ports
}

// waitForAdmin polls the admin server's /health endpoint until it returns
// a 200 or the timeout is reached.
func waitForAdmin(t *testing.T, pool *x509.CertPool, port int, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(timeout)
	client := caHTTPClient(pool, 2*time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for admin at port %d", timeout, port)
}

// httpPut sends a PUT request to the admin server's KV endpoint.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpPut(t *testing.T, ca *testCA, port int, key string, value []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(value))
		if err != nil {
			t.Fatalf("httpPut: new request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpPut %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		return
	}
	t.Fatalf("httpPut: %v (after %d attempts)", lastErr, maxRetries)
}

// httpGet sends a GET request to the admin server's KV endpoint and returns the value.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpGet(t *testing.T, ca *testCA, port int, key string) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGet %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("httpGet: read body: %v", err)
		}
		return body
	}
	t.Fatalf("httpGet: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpGetStale sends a GET request with ?stale=true for local state machine
// reads without Raft coordination. Retries up to 3 times on transient errors.
func httpGetStale(t *testing.T, ca *testCA, port int, key string) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s?stale=true", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGetStale %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("httpGetStale: read body: %v", err)
		}
		return body
	}
	t.Fatalf("httpGetStale: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpGetStatus sends a GET request and returns status code (for testing 404 etc).
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpGetStatus(t *testing.T, ca *testCA, port int, key string) int {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		io.ReadAll(resp.Body)
		return resp.StatusCode
	}
	t.Fatalf("httpGetStatus: %v (after %d attempts)", lastErr, maxRetries)
	return 0
}

// httpDelete sends a DELETE request to the admin server's KV endpoint.
// httpDelete sends a DELETE request to the admin server's KV endpoint.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpDelete(t *testing.T, ca *testCA, port int, key string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			t.Fatalf("httpDelete: new request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpDelete %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		return
	}
	t.Fatalf("httpDelete: %v (after %d attempts)", lastErr, maxRetries)
}

// httpList sends a GET request to list KV keys by prefix.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpList(t *testing.T, ca *testCA, port int, prefix string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpList prefix=%s: status %d, body: %s", prefix, resp.StatusCode, body)
		}
		var result struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("httpList: decode: %v", err)
		}
		return result.Keys
	}
	t.Fatalf("httpList: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpStatus fetches the /status endpoint and returns the decoded response.
func httpStatus(t *testing.T, ca *testCA, port int) map[string]interface{} {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpStatus: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("httpStatus: decode: %v", err)
	}
	return result
}

// httpHealth returns the HTTP status code from /health.
func httpHealth(t *testing.T, pool *x509.CertPool, port int) int {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return resp.StatusCode
}

// waitForLeaderViaAdmin polls all nodes' /status endpoint until at least one
// reports is_leader:true for shard 1.
func waitForLeaderViaAdmin(t *testing.T, ca *testCA, ports []int, timeout time.Duration) int {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	deadline := time.Now().Add(timeout)
	var lastDiag string
	for time.Now().Before(deadline) {
		for _, port := range ports {
			status, diag := httpStatusRaw(t, client, port)
			if diag != "" {
				lastDiag = fmt.Sprintf("port %d: %s", port, diag)
			}
			if status != nil {
				if shards, ok := status["shards"].([]interface{}); ok {
					for _, s := range shards {
						shard, ok := s.(map[string]interface{})
						if !ok {
							continue
						}
						if isLeader, ok := shard["is_leader"].(bool); ok && isLeader {
							return port
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for leader via admin (last: %s)", timeout, lastDiag)
	return 0
}

// httpStatusRaw fetches /status without failing the test. Returns nil on error
// and a diagnostic string describing the last failure.
func httpStatusRaw(t *testing.T, client *http.Client, port int) (map[string]interface{}, string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Sprintf("GET error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Sprintf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body))
	}
	var result map[string]interface{}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&result); err != nil {
		return nil, fmt.Sprintf("decode: %v", err)
	}
	return result, ""
}

// httpListTolerant sends a GET request to list KV keys by prefix. Returns nil
// on any error instead of fataling, allowing callers to retry.
func httpListTolerant(t *testing.T, ca *testCA, port int, prefix string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.ReadAll(resp.Body)
		return nil
	}
	var result struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result.Keys
}

// warmupWrite performs a PUT followed by a DELETE to force the leader to commit
// entries at the current term (including its no-op entry). This ensures the
// ReadIndex pipeline is operational before tests issue reads. The key is deleted
// so it doesn't pollute test assertions on key counts.
func warmupWrite(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()
	const warmupKey = "__warmup__"
	deadline := time.Now().Add(timeout)
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	var lastStatus int
	var lastErr error
	var lastBody string
	for time.Now().Before(deadline) {
		url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, warmupKey)
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("1")))
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode
		lastBody = string(body)
		lastErr = nil
		if resp.StatusCode == http.StatusNoContent {
			// Clean up the warmup key.
			delReq, _ := http.NewRequest(http.MethodDelete, url, nil)
			delResp, delErr := client.Do(delReq)
			if delErr == nil {
				delResp.Body.Close()
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("timed out after %v waiting for warmup write on port %d: last error: %v", timeout, port, lastErr)
	}
	t.Fatalf("timed out after %v waiting for warmup write on port %d: last status %d, body: %s", timeout, port, lastStatus, lastBody)
}

// waitForKV polls GET /kv/{key} until the value matches expected or timeout.
func waitForKV(t *testing.T, ca *testCA, port int, key string, expected []byte, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	deadline := time.Now().Add(timeout)
	client := mtlsHTTPClient(t, ca, 2*time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && bytes.Equal(body, expected) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for key %q to equal %q", timeout, key, expected)
}

// ---------------------------------------------------------------------------
// Batch helpers
// ---------------------------------------------------------------------------

// batchOp is the JSON representation of an operation in a batch or transaction.
type batchOp struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	RangeEnd string `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// batchOpResponse is the JSON representation of an operation result.
type batchOpResponse struct {
	Kvs       []kvResult `json:"kvs,omitempty"`
	Count     int64      `json:"count,omitempty"`
	Succeeded bool       `json:"succeeded"`
}

// batchResult is the JSON payload returned by POST /batch.
type batchResult struct {
	Responses []batchOpResponse `json:"responses"`
}

// kvResult is the JSON representation of an MVCC key-value pair.
type kvResult struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// httpBatch sends a POST request to /batch with the given operations and
// returns the batch result. Retries up to 3 times on transient errors.
func httpBatch(t *testing.T, ca *testCA, port int, ops []batchOp) batchResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/batch", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Ops []batchOp `json:"ops"`
	}{Ops: ops}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpBatch: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpBatch: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result batchResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpBatch: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpBatch: %v (after %d attempts)", lastErr, maxRetries)
	return batchResult{}
}

// ---------------------------------------------------------------------------
// Transaction helpers
// ---------------------------------------------------------------------------

// txCompare is the JSON representation of a transaction compare precondition.
type txCompare struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          string `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// txRequest is the JSON payload for POST /tx.
type txRequest struct {
	Compares   []txCompare `json:"compares"`
	SuccessOps []batchOp   `json:"success_ops"`
	FailureOps []batchOp   `json:"failure_ops"`
}

// txResult is the JSON payload returned by POST /tx.
type txResult struct {
	Succeeded bool              `json:"succeeded"`
	Revision  int64             `json:"revision"`
	Responses []batchOpResponse `json:"responses"`
}

// httpTx sends a POST request to /tx with the given transaction request and
// returns the result. Retries up to 3 times on transient errors.
func httpTx(t *testing.T, ca *testCA, port int, req txRequest) txResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/tx", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("httpTx: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTx: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result txResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTx: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpTx: %v (after %d attempts)", lastErr, maxRetries)
	return txResult{}
}

// ---------------------------------------------------------------------------
// MVCC helpers
// ---------------------------------------------------------------------------

// rangeResult is the JSON payload returned by GET /kv/range.
type rangeResult struct {
	Kvs      []kvResult `json:"kvs"`
	Count    int64      `json:"count"`
	Revision int64      `json:"revision,omitempty"`
}

// revisionResult is the JSON payload returned by GET /kv/revision.
type revisionResult struct {
	Revision int64 `json:"revision"`
}

// httpRange sends a GET request to /kv/range with the given start and end
// keys and returns the range result. Retries up to 3 times on transient errors.
func httpRange(t *testing.T, ca *testCA, port int, start, end string) rangeResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/range?start=%s", port, start)
	if end != "" {
		url += "&end=" + end
	}
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpRange: status %d, body: %s", resp.StatusCode, body)
		}
		var result rangeResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpRange: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpRange: %v (after %d attempts)", lastErr, maxRetries)
	return rangeResult{}
}

// httpCompact sends a POST request to /kv/compact with the given revision.
// Retries up to 3 times on transient errors.
func httpCompact(t *testing.T, ca *testCA, port int, revision int64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/compact", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Revision int64 `json:"revision"`
	}{Revision: revision}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpCompact: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpCompact: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpCompact: %v (after %d attempts)", lastErr, maxRetries)
}

// httpCurrentRevision sends a GET request to /kv/revision and returns the
// current MVCC revision. Retries up to 3 times on transient errors.
func httpCurrentRevision(t *testing.T, ca *testCA, port int) int64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/revision", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpCurrentRevision: status %d, body: %s", resp.StatusCode, body)
		}
		var result revisionResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpCurrentRevision: decode: %v", decErr)
		}
		return result.Revision
	}
	t.Fatalf("httpCurrentRevision: %v (after %d attempts)", lastErr, maxRetries)
	return 0
}

// httpGetWithRevision sends a GET request to /kv/{key}?revision=true and
// returns the full MVCC key-value result. Retries up to 3 times on transient
// errors.
func httpGetWithRevision(t *testing.T, ca *testCA, port int, key string) kvResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s?revision=true", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGetWithRevision %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		var result kvResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpGetWithRevision: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpGetWithRevision: %v (after %d attempts)", lastErr, maxRetries)
	return kvResult{}
}

// ---------------------------------------------------------------------------
// Lease helpers
// ---------------------------------------------------------------------------

// leaseResult is the JSON representation of a lease.
type leaseResult struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         []string `json:"keys"`
}

// leasesResult is the JSON payload returned by GET /lease/list.
type leasesResult struct {
	Leases []leaseResult `json:"leases"`
}

// httpLeaseGrant sends a POST request to /lease/grant with the given TTL
// and returns the lease result. Retries up to 3 times on transient errors.
func httpLeaseGrant(t *testing.T, ca *testCA, port int, ttl int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/grant", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		TTL int64 `json:"ttl"`
	}{TTL: ttl}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseGrant: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseGrant: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseGrant: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseGrant: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseRevoke sends a POST request to /lease/revoke with the given
// lease ID. Retries up to 3 times on transient errors.
func httpLeaseRevoke(t *testing.T, ca *testCA, port int, leaseID int64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/revoke", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		LeaseID int64 `json:"lease_id"`
	}{LeaseID: leaseID}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseRevoke: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseRevoke: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpLeaseRevoke: %v (after %d attempts)", lastErr, maxRetries)
}

// httpLeaseKeepAlive sends a POST request to /lease/keepalive with the given
// lease ID and returns the refreshed lease result. Retries up to 3 times on
// transient errors.
func httpLeaseKeepAlive(t *testing.T, ca *testCA, port int, leaseID int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/keepalive", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		LeaseID int64 `json:"lease_id"`
	}{LeaseID: leaseID}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseKeepAlive: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseKeepAlive: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseKeepAlive: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseKeepAlive: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseTTL sends a GET request to /lease/ttl?lease_id=N and returns the
// lease with its remaining TTL. Retries up to 3 times on transient errors.
func httpLeaseTTL(t *testing.T, ca *testCA, port int, leaseID int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/ttl?lease_id=%d", port, leaseID)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseTTL: status %d, body: %s", resp.StatusCode, body)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseTTL: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseTTL: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseList sends a GET request to /lease/list and returns all active
// leases. Retries up to 3 times on transient errors.
func httpLeaseList(t *testing.T, ca *testCA, port int) []leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/list", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseList: status %d, body: %s", resp.StatusCode, body)
		}
		var result leasesResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseList: decode: %v", decErr)
		}
		return result.Leases
	}
	t.Fatalf("httpLeaseList: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Time Series helpers
// ---------------------------------------------------------------------------

// tsDataPoint is the JSON representation of a time-series data point.
type tsDataPoint struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsQueryResult is the JSON payload returned by GET /ts/query.
type tsQueryResult struct {
	Points []tsDataPoint `json:"points"`
}

// tsMetricsResult is the JSON payload returned by GET /ts/metrics.
type tsMetricsResult struct {
	Metrics []string `json:"metrics"`
}

// tsAggResult is the JSON representation of an aggregation result bucket.
type tsAggResult struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
	Count     int64   `json:"count"`
}

// tsAggregateResult is the JSON payload returned by GET /ts/aggregate.
type tsAggregateResult struct {
	Results []tsAggResult `json:"results"`
}

// httpTSWritePoint sends a POST request to /ts/write to store a single
// time-series data point. The timestamp is set to the current time in
// nanoseconds. Retries up to 3 times on transient errors.
func httpTSWritePoint(t *testing.T, ca *testCA, port int, metric string, value float64, tags map[string]string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/write", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Metric    string            `json:"metric"`
		Value     float64           `json:"value"`
		Timestamp int64             `json:"timestamp"`
		Tags      map[string]string `json:"tags,omitempty"`
	}{
		Metric:    metric,
		Value:     value,
		Timestamp: time.Now().UnixNano(),
		Tags:      tags,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpTSWritePoint: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSWritePoint: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpTSWritePoint: %v (after %d attempts)", lastErr, maxRetries)
}

// httpTSQuery sends a GET request to /ts/query with the given metric name
// and time range (nanoseconds) and returns the data points. Retries up to
// 3 times on transient errors.
func httpTSQuery(t *testing.T, ca *testCA, port int, metric string, startNs, endNs int64) []tsDataPoint {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/query?metric=%s&start_ns=%d&end_ns=%d",
		port, metric, startNs, endNs)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSQuery: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsQueryResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSQuery: decode: %v", decErr)
		}
		return result.Points
	}
	t.Fatalf("httpTSQuery: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpTSListMetrics sends a GET request to /ts/metrics and returns the list
// of available metric names. Retries up to 3 times on transient errors.
func httpTSListMetrics(t *testing.T, ca *testCA, port int) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/metrics", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSListMetrics: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsMetricsResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSListMetrics: decode: %v", decErr)
		}
		return result.Metrics
	}
	t.Fatalf("httpTSListMetrics: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpTSDeleteSeries sends a DELETE request to /ts/series/{metric} to remove
// all data for the given metric. Retries up to 3 times on transient errors.
func httpTSDeleteSeries(t *testing.T, ca *testCA, port int, metric string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/series/%s", port, metric)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, reqErr := http.NewRequest(http.MethodDelete, url, nil)
		if reqErr != nil {
			t.Fatalf("httpTSDeleteSeries: new request: %v", reqErr)
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSDeleteSeries: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpTSDeleteSeries: %v (after %d attempts)", lastErr, maxRetries)
}

// httpTSAggregate sends a GET request to /ts/aggregate with the given
// parameters and returns the aggregation results. Retries up to 3 times
// on transient errors.
func httpTSAggregate(t *testing.T, ca *testCA, port int, metric string, startNs, endNs int64, fn string, intervalNs int64) []tsAggResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/aggregate?metric=%s&start_ns=%d&end_ns=%d&function=%s&interval_ns=%d",
		port, metric, startNs, endNs, fn, intervalNs)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSAggregate: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsAggregateResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSAggregate: decode: %v", decErr)
		}
		return result.Results
	}
	t.Fatalf("httpTSAggregate: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Index helpers
// ---------------------------------------------------------------------------

// indexQueryResult is the JSON payload returned by GET /index/query.
type indexQueryResult struct {
	Keys []string `json:"keys"`
}

// httpIndexCreate sends a POST request to /index/create to create a new
// secondary index. Retries up to 3 times on transient errors.
func httpIndexCreate(t *testing.T, ca *testCA, port int, name, fieldName string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/create", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Name      string `json:"name"`
		FieldName string `json:"field_name"`
	}{Name: name, FieldName: fieldName}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpIndexCreate: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexCreate: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpIndexCreate: %v (after %d attempts)", lastErr, maxRetries)
}

// httpIndexDrop sends a POST request to /index/drop to remove a secondary
// index by name. Retries up to 3 times on transient errors.
func httpIndexDrop(t *testing.T, ca *testCA, port int, name string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/drop", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Name string `json:"name"`
	}{Name: name}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpIndexDrop: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexDrop: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpIndexDrop: %v (after %d attempts)", lastErr, maxRetries)
}

// httpIndexQuery sends a GET request to /index/query with the given index
// name and value, and returns the matching entity IDs. Retries up to 3 times
// on transient errors.
func httpIndexQuery(t *testing.T, ca *testCA, port int, indexName, value string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/query?index_name=%s&value=%s",
		port, indexName, value)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexQuery: status %d, body: %s", resp.StatusCode, body)
		}
		var result indexQueryResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpIndexQuery: decode: %v", decErr)
		}
		return result.Keys
	}
	t.Fatalf("httpIndexQuery: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Vector helpers
// ---------------------------------------------------------------------------

// vectorSearchResult is the JSON representation of a vector search result.
type vectorSearchResult struct {
	ID       uint64                 `json:"id"`
	Score    float32                `json:"score"`
	Vector   []float32              `json:"vector,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Distance float32                `json:"distance"`
	Rank     int                    `json:"rank"`
}

// vectorSearchResponse is the JSON payload returned by POST /vector/search/similar.
type vectorSearchResponse struct {
	Results       []vectorSearchResult `json:"results"`
	Timestamp     int64                `json:"timestamp"`
	QueryDuration int64                `json:"query_duration"`
}

// vectorDataResult is the JSON representation of a single stored vector.
type vectorDataResult struct {
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// vectorListNSResponse is the JSON payload returned by GET /vector/namespaces.
type vectorListNSResponse struct {
	Namespaces []vectorNamespaceInfo `json:"namespaces"`
	TotalCount int                   `json:"total_count"`
	Timestamp  int64                 `json:"timestamp"`
}

// vectorNamespaceInfo is the JSON representation of namespace info in lists.
type vectorNamespaceInfo struct {
	Namespace      string            `json:"namespace"`
	Dimension      uint32            `json:"dimension"`
	VectorCount    uint64            `json:"vector_count"`
	DistanceMetric string            `json:"distance_metric"`
	IndexType      string            `json:"index_type"`
	IndexConfig    map[string]string `json:"index_config,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

// httpVectorCreateNS sends a POST request to /vector/namespace to create a
// new vector namespace. Retries up to 3 times on transient errors.
func httpVectorCreateNS(t *testing.T, ca *testCA, port int, namespace string, dimension int, metric string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespace", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace      string `json:"namespace"`
		Dimension      uint32 `json:"dimension"`
		DistanceMetric string `json:"distance_metric"`
	}{
		Namespace:      namespace,
		Dimension:      uint32(dimension),
		DistanceMetric: metric,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorCreateNS: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorCreateNS: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorCreateNS: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorDeleteNS sends a POST request to /vector/namespace/delete to
// remove a vector namespace and all its vectors. Retries up to 3 times on
// transient errors.
func httpVectorDeleteNS(t *testing.T, ca *testCA, port int, namespace string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespace/delete", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string `json:"namespace"`
	}{Namespace: namespace}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorDeleteNS: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorDeleteNS: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorDeleteNS: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorUpsert sends a POST request to /vector/upsert to insert or
// update a vector in the given namespace. Retries up to 3 times on transient
// errors.
func httpVectorUpsert(t *testing.T, ca *testCA, port int, namespace string, id uint64, vector []float32, metadata map[string]interface{}) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/upsert", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string                 `json:"namespace"`
		ID        uint64                 `json:"id"`
		Vector    []float32              `json:"vector"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
	}{
		Namespace: namespace,
		ID:        id,
		Vector:    vector,
		Metadata:  metadata,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorUpsert: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorUpsert: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorUpsert: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorSearch sends a POST request to /vector/search/similar to perform
// a k-nearest-neighbor search and returns the results. Retries up to 3 times
// on transient errors.
func httpVectorSearch(t *testing.T, ca *testCA, port int, namespace string, vector []float32, topK int) []vectorSearchResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/search/similar", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace   string    `json:"namespace"`
		QueryVector []float32 `json:"query_vector"`
		K           int       `json:"k"`
	}{
		Namespace:   namespace,
		QueryVector: vector,
		K:           topK,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorSearch: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorSearch: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result vectorSearchResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorSearch: decode: %v", decErr)
		}
		return result.Results
	}
	t.Fatalf("httpVectorSearch: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpVectorGet sends a GET request to /vector/get?namespace=N&id=I and
// returns the stored vector data. Retries up to 3 times on transient errors.
func httpVectorGet(t *testing.T, ca *testCA, port int, namespace string, id uint64) vectorDataResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/get?namespace=%s&id=%d", port, namespace, id)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorGet: status %d, body: %s", resp.StatusCode, body)
		}
		var result vectorDataResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorGet: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpVectorGet: %v (after %d attempts)", lastErr, maxRetries)
	return vectorDataResult{}
}

// httpVectorDeleteVec sends a POST request to /vector/batch/delete to remove
// vectors by their IDs from the given namespace. Retries up to 3 times on
// transient errors.
func httpVectorDeleteVec(t *testing.T, ca *testCA, port int, namespace string, ids []uint64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/batch/delete", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string   `json:"namespace"`
		IDs       []uint64 `json:"ids"`
	}{
		Namespace: namespace,
		IDs:       ids,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorDeleteVec: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorDeleteVec: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorDeleteVec: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorListNS sends a GET request to /vector/namespaces and returns the
// list of namespace names. Retries up to 3 times on transient errors.
func httpVectorListNS(t *testing.T, ca *testCA, port int) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespaces", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorListNS: status %d, body: %s", resp.StatusCode, body)
		}
		var result vectorListNSResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorListNS: decode: %v", decErr)
		}
		names := make([]string, len(result.Namespaces))
		for i, ns := range result.Namespaces {
			names[i] = ns.Namespace
		}
		return names
	}
	t.Fatalf("httpVectorListNS: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}
