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

//go:build benchmark

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	// defaultPayloadSize is the value size in bytes for benchmark KV writes.
	defaultPayloadSize = 128

	// batchSize is the number of entries in batch PUT benchmarks.
	batchSize = 10000

	// scanSize is the number of entries seeded for prefix scan benchmarks.
	scanSize = 10000

	// concurrency is the number of goroutines for concurrent benchmarks.
	concurrency = 8
)

// benchSM selects the QRDB state machine backend ("memory" or "pebble").
var benchSM = envOr("QRDB_BENCH_SM", "pebble")

// benchProtocol selects the HTTP protocol for benchmark clients ("h2" or "h3").
var benchProtocol = envOr("QRDB_BENCH_PROTOCOL", "h2")

// benchCA holds the CA certificate and key for benchmark TLS certificates.
type benchCA struct {
	certPEM  []byte
	certPath string
	pool     *x509.CertPool
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
}

// generateBenchCA creates an ephemeral ECDSA P-256 CA for benchmarks.
func generateBenchCA(b *testing.B) *benchCA {
	b.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("generateBenchCA: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		b.Fatalf("generateBenchCA: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-bench"},
			CommonName:   "bench-ca",
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
		b.Fatalf("generateBenchCA: create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		b.Fatalf("generateBenchCA: parse cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	dir, err := os.MkdirTemp("", "qrdb-bench-ca-*")
	if err != nil {
		b.Fatalf("generateBenchCA: MkdirTemp: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })

	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		b.Fatalf("generateBenchCA: write: %v", err)
	}

	return &benchCA{
		certPEM:  certPEM,
		certPath: caFile,
		pool:     pool,
		cert:     cert,
		key:      key,
	}
}

// generateNodeCert creates a leaf certificate signed by this CA.
func (ca *benchCA) generateNodeCert(b *testing.B) (certPath, keyPath string) {
	b.Helper()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("generateNodeCert: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		b.Fatalf("generateNodeCert: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-bench"},
			CommonName:   "bench-node",
		},
		DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
			net.IPv4zero,
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(2 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		b.Fatalf("generateNodeCert: create cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		b.Fatalf("generateNodeCert: marshal key: %v", err)
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir, err := os.MkdirTemp("", "qrdb-bench-node-*")
	if err != nil {
		b.Fatalf("generateNodeCert: MkdirTemp: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	if err := os.WriteFile(certPath, leafCertPEM, 0600); err != nil {
		b.Fatalf("generateNodeCert: write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, leafKeyPEM, 0600); err != nil {
		b.Fatalf("generateNodeCert: write key: %v", err)
	}

	return certPath, keyPath
}

// QRDBCluster manages QRDB processes for benchmarking.
type QRDBCluster struct {
	binary     string
	procs      []*exec.Cmd
	adminPorts []int
	leaderPort int
	dataDir    string
	caPool     *x509.CertPool
}

var (
	buildOnce   sync.Once
	builtBinary string
	buildErr    error
)

// buildBinary returns the path to the qrdb CLI binary. Inside Docker the
// binary is pre-built at /usr/local/bin/qrdb; on the host it is compiled
// once into a temporary directory.
//
// Note: os.MkdirTemp is used instead of b.TempDir() because sync.Once
// captures the first caller's b. If b.TempDir() were used, the directory
// would be cleaned up when that first benchmark finishes, causing all
// subsequent benchmarks to fail with "no such file or directory".
func buildBinary(b *testing.B) string {
	b.Helper()
	buildOnce.Do(func() {
		// In Docker the binary is pre-installed.
		if path := "/usr/local/bin/qrdb"; fileExists(path) {
			builtBinary = path
			return
		}
		dir, err := os.MkdirTemp("", "qrdb-bench-*")
		if err != nil {
			buildErr = fmt.Errorf("MkdirTemp: %v", err)
			return
		}
		builtBinary = filepath.Join(dir, "qrdb")
		cmd := exec.Command("go", "build", "-o", builtBinary, ".")
		cmd.Dir = qrdbRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build failed: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		b.Fatalf("buildBinary: %v", buildErr)
	}
	return builtBinary
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// qrdbRoot returns the contrib/qrdb module root by walking up from
// the current working directory to find the admin.go file that lives
// at the module root.
func qrdbRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "admin.go")); err == nil {
			return dir
		}
		// Check for contrib/qrdb relative to a potential repo root.
		candidate := filepath.Join(dir, "contrib", "qrdb")
		if _, err := os.Stat(filepath.Join(candidate, "admin.go")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback: relative path from this benchmark directory.
	return "../../../../.."
}

// allocateUDPPorts returns n free UDP ports by briefly binding and releasing.
func allocateUDPPorts(n int) ([]int, error) {
	ports := make([]int, n)
	for i := range n {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		ports[i] = pc.LocalAddr().(*net.UDPAddr).Port
		pc.Close()
	}
	return ports, nil
}

// allocateTCPPorts returns n free TCP ports by briefly binding and releasing.
func allocateTCPPorts(n int) ([]int, error) {
	ports := make([]int, n)
	for i := range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		ports[i] = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
	}
	return ports, nil
}

// buildPeersFlag builds the --peers flag value from raft ports.
// Format: "1=127.0.0.1:port1,2=127.0.0.1:port2,..."
func buildPeersFlag(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = fmt.Sprintf("%d=127.0.0.1:%d", i+1, port)
	}
	return strings.Join(parts, ",")
}

// SetupQRDBCluster builds the binary, starts N QRDB processes, and waits
// for leader election. Returns the cluster or fatals the benchmark.
func SetupQRDBCluster(b *testing.B, nodes int) *QRDBCluster {
	b.Helper()
	binary := buildBinary(b)
	ca := generateBenchCA(b)

	raftPorts, err := allocateUDPPorts(nodes)
	if err != nil {
		b.Fatalf("allocateUDPPorts: %v", err)
	}
	adminPorts, err := allocateTCPPorts(nodes)
	if err != nil {
		b.Fatalf("allocateTCPPorts: %v", err)
	}

	dataDir, err := os.MkdirTemp("", "qrdb-bench-data-*")
	if err != nil {
		b.Fatalf("MkdirTemp: %v", err)
	}

	peers := buildPeersFlag(raftPorts)

	procs := make([]*exec.Cmd, nodes)
	for i := range nodes {
		certPath, keyPath := ca.generateNodeCert(b)
		nodeDataDir := filepath.Join(dataDir, fmt.Sprintf("node%d", i+1))
		args := []string{
			"start",
			"--discovery", "static",
			"--peers", peers,
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--data-dir", nodeDataDir,
			"--sm", benchSM,
			"--log-level", "warn",
			"--tls-ca-file", ca.certPath,
			"--tls-cert-file", certPath,
			"--tls-key-file", keyPath,
		}
		cmd := exec.Command(binary, args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			b.Fatalf("start node %d: %v", i+1, err)
		}
		procs[i] = cmd
	}

	b.Cleanup(func() {
		for _, cmd := range procs {
			if cmd.Process != nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		}
		os.RemoveAll(dataDir)
	})

	// Wait for all admin servers to be healthy.
	client := caHTTPClient(ca.pool, 2*time.Second)
	for _, port := range adminPorts {
		waitForAdminHealthy(b, client, port, 30*time.Second)
	}

	// Wait for leader election.
	leaderPort := waitForLeader(b, client, adminPorts, 30*time.Second)

	// Warmup write to ensure ReadIndex pipeline is operational (PhD 6.4).
	warmupWrite(b, client, leaderPort, 10*time.Second)

	return &QRDBCluster{
		binary:     binary,
		procs:      procs,
		adminPorts: adminPorts,
		leaderPort: leaderPort,
		dataDir:    dataDir,
		caPool:     ca.pool,
	}
}

// caHTTPClient returns an HTTP/2+TLS client that verifies server
// certificates using the given CA certificate pool.
func caHTTPClient(pool *x509.CertPool, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS13,
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   128,
			MaxConnsPerHost:       0, // unlimited
			IdleConnTimeout:       5 * time.Minute,
			DisableKeepAlives:     false,
			WriteBufferSize:       64 << 10, // 64KB
			ReadBufferSize:        64 << 10, // 64KB
			ResponseHeaderTimeout: timeout,
		},
	}
}

// http3Client returns an HTTP/3+QUIC client that verifies server
// certificates using the given CA certificate pool.
func http3Client(pool *x509.CertPool, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS13,
			},
			MaxResponseHeaderBytes: 64 << 10, // 64KB
		},
	}
}

// waitForAdminHealthy polls the admin server's /health endpoint until
// it returns 200 or the timeout is reached.
func waitForAdminHealthy(b *testing.B, client *http.Client, port int, timeout time.Duration) {
	b.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(timeout)
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
	b.Fatalf("timed out waiting for admin at port %d", port)
}

// waitForLeader polls all nodes' /status endpoint until at least one
// reports is_leader:true, and returns that node's admin port.
func waitForLeader(b *testing.B, client *http.Client, ports []int, timeout time.Duration) int {
	b.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, port := range ports {
			url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
			resp, err := client.Get(url)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), `"is_leader":true`) {
				return port
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.Fatalf("timed out waiting for leader")
	return 0
}

// warmupWrite performs a PUT followed by a DELETE to force the leader to
// commit entries at the current term (including its no-op entry). This
// ensures the ReadIndex pipeline is operational before benchmarks issue
// reads. The key is deleted so it does not pollute benchmark assertions.
func warmupWrite(b *testing.B, client *http.Client, port int, timeout time.Duration) {
	b.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/__warmup__", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("1")))
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent {
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
	b.Fatalf("timed out waiting for warmup write on port %d", port)
}

// kvPut performs a PUT request to the QRDB admin API.
func kvPut(client *http.Client, port int, key string, value []byte) error {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(value))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("put %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// kvGet performs a GET request to the QRDB admin API.
func kvGet(client *http.Client, port int, key string) ([]byte, error) {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", key, resp.StatusCode)
	}
	return body, nil
}

// kvDelete performs a DELETE request to the QRDB admin API.
func kvDelete(client *http.Client, port int, key string) error {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// kvList performs a prefix scan using GET /kv?prefix=X and returns the
// raw response body length for throughput measurement.
func kvList(client *http.Client, port int, prefix string) (int, error) {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("list prefix=%s: status %d", prefix, resp.StatusCode)
	}
	return len(body), nil
}

// envOr returns the value of the environment variable named by key,
// or fallback if the variable is not set or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// benchClient returns an HTTP client matching the configured benchProtocol.
// "h3" selects HTTP/3+QUIC; any other value selects HTTP/2+TLS.
// The pool parameter provides the CA certificate pool for TLS verification.
func benchClient(pool *x509.CertPool, timeout time.Duration) *http.Client {
	if benchProtocol == "h3" {
		return http3Client(pool, timeout)
	}
	return caHTTPClient(pool, timeout)
}

// seedPoolSize is the number of keys pre-seeded for read and delete
// benchmarks. Benchmarks cycle through these keys with modulo so that
// b.N can grow arbitrarily without the seeding phase dominating runtime.
const seedPoolSize = 10000

// seedKeys writes seedPoolSize keys concurrently using 8 goroutines.
// The key format is "<prefix><index>" where index is 0..seedPoolSize-1.
func seedKeys(b *testing.B, client *http.Client, port int, prefix string, value []byte) {
	b.Helper()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= seedPoolSize {
					return
				}
				key := fmt.Sprintf("%s%d", prefix, i)
				if err := kvPut(client, port, key, value); err != nil {
					b.Errorf("seed %s: %v", key, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// generatePayload creates a deterministic payload of the given size.
func generatePayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	return payload
}
