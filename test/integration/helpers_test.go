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
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
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
// using the given CA certificate pool.
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
		cmd := exec.Command("go", "build", "-o", builtBinary, "./cmd/qrdbd")
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
func allocatePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	for i := range n {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocatePorts: %v", err)
		}
		addr := pc.LocalAddr().(*net.UDPAddr)
		ports[i] = addr.Port
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
func allocateTCPPorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	for i := range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocateTCPPorts: %v", err)
		}
		addr := ln.Addr().(*net.TCPAddr)
		ports[i] = addr.Port
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
func httpPut(t *testing.T, pool *x509.CertPool, port int, key string, value []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(value))
	if err != nil {
		t.Fatalf("httpPut: new request: %v", err)
	}
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("httpPut: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("httpPut %s: status %d, body: %s", key, resp.StatusCode, body)
	}
}

// httpGet sends a GET request to the admin server's KV endpoint and returns the value.
func httpGet(t *testing.T, pool *x509.CertPool, port int, key string) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
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

// httpGetStatus sends a GET request and returns status code (for testing 404 etc).
func httpGetStatus(t *testing.T, pool *x509.CertPool, port int, key string) int {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpGetStatus: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return resp.StatusCode
}

// httpDelete sends a DELETE request to the admin server's KV endpoint.
func httpDelete(t *testing.T, pool *x509.CertPool, port int, key string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("httpDelete: new request: %v", err)
	}
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("httpDelete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("httpDelete %s: status %d, body: %s", key, resp.StatusCode, body)
	}
}

// httpList sends a GET request to list KV keys by prefix.
func httpList(t *testing.T, pool *x509.CertPool, port int, prefix string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpList: %v", err)
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

// httpStatus fetches the /status endpoint and returns the decoded response.
func httpStatus(t *testing.T, pool *x509.CertPool, port int) map[string]interface{} {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	client := caHTTPClient(pool, 10*time.Second)
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
func waitForLeaderViaAdmin(t *testing.T, pool *x509.CertPool, ports []int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, port := range ports {
			status := httpStatusRaw(pool, port)
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
	t.Fatalf("timed out after %v waiting for leader via admin", timeout)
	return 0
}

// httpStatusRaw fetches /status without failing the test. Returns nil on error.
func httpStatusRaw(pool *x509.CertPool, port int) map[string]interface{} {
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	client := caHTTPClient(pool, 2*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result
}

// httpListTolerant sends a GET request to list KV keys by prefix. Returns nil
// on any error instead of fataling, allowing callers to retry.
func httpListTolerant(pool *x509.CertPool, port int, prefix string) []string {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := caHTTPClient(pool, 5*time.Second)
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
func warmupWrite(t *testing.T, pool *x509.CertPool, port int, timeout time.Duration) {
	t.Helper()
	const warmupKey = "__warmup__"
	deadline := time.Now().Add(timeout)
	client := caHTTPClient(pool, 5*time.Second)
	for time.Now().Before(deadline) {
		url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, warmupKey)
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("1")))
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent {
				// Clean up the warmup key.
				delReq, _ := http.NewRequest(http.MethodDelete, url, nil)
				delResp, delErr := client.Do(delReq)
				if delErr == nil {
					delResp.Body.Close()
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for warmup write on port %d", timeout, port)
}

// waitForKV polls GET /kv/{key} until the value matches expected or timeout.
func waitForKV(t *testing.T, pool *x509.CertPool, port int, key string, expected []byte, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	deadline := time.Now().Add(timeout)
	client := caHTTPClient(pool, 2*time.Second)
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
