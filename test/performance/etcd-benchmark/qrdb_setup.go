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
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// qrdbBinary is the path to the pre-built QRDB binary inside the Docker image.
	qrdbBinary = "/usr/local/bin/qrdb"

	// qrdbEtcdDBName is the database name created for the etcd benchmark.
	qrdbEtcdDBName = "etcd-bench"
)

// qrdbBenchCA holds the ephemeral CA certificate and key used to sign all
// QRDB node and client certificates during benchmarking.
type qrdbBenchCA struct {
	certPEM  []byte
	certPath string
	pool     *x509.CertPool
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
}

// generateQRDBBenchCA creates an ephemeral ECDSA P-256 CA for benchmarks.
func generateQRDBBenchCA(b *testing.B) *qrdbBenchCA {
	b.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("generateQRDBBenchCA: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		b.Fatalf("generateQRDBBenchCA: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-qrdb-etcd-bench"},
			CommonName:   "etcd-bench-ca",
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
		b.Fatalf("generateQRDBBenchCA: create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		b.Fatalf("generateQRDBBenchCA: parse cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	dir, err := os.MkdirTemp("", "qrdb-etcd-bench-ca-*")
	if err != nil {
		b.Fatalf("generateQRDBBenchCA: MkdirTemp: %v", err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })

	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		b.Fatalf("generateQRDBBenchCA: write CA file: %v", err)
	}

	return &qrdbBenchCA{
		certPEM:  certPEM,
		certPath: caFile,
		pool:     pool,
		cert:     cert,
		key:      key,
	}
}

// generateNodeCert creates a leaf certificate signed by this CA and writes
// it to a temporary directory. Returns the paths to the cert and key PEM files.
func generateNodeCert(b *testing.B, ca *qrdbBenchCA) (certPath, keyPath string) {
	b.Helper()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("generateNodeCert: generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		b.Fatalf("generateNodeCert: serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"go-qrdb-etcd-bench"},
			CommonName:   "etcd-bench-node",
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

	dir, err := os.MkdirTemp("", "qrdb-etcd-bench-node-*")
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

// allocateFreeTCPPort finds and returns a single free TCP port.
func allocateFreeTCPPort(b *testing.B) int {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("allocateFreeTCPPort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// allocateFreeUDPPort finds and returns a single free UDP port.
func allocateFreeUDPPort(b *testing.B) int {
	b.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("allocateFreeUDPPort: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	return port
}

// mtlsConfig returns a TLS config that presents a client certificate signed
// by this CA, allowing mutual TLS authentication with the QRDB server.
func (ca *qrdbBenchCA) mtlsConfig(b *testing.B) *tls.Config {
	b.Helper()

	certPath, keyPath := generateNodeCert(b, ca)
	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		b.Fatalf("mtlsConfig: load client cert: %v", err)
	}
	return &tls.Config{
		RootCAs:      ca.pool,
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS13,
	}
}

// qrdbHTTPClient returns an HTTP/2+TLS 1.3 client using the provided TLS
// config, which must include a client certificate for mTLS authentication.
func qrdbHTTPClient(tlsCfg *tls.Config, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:     tlsCfg,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     5 * time.Minute,
		},
	}
}

// waitForQRDBHealthy polls GET /health on the admin port until it returns
// HTTP 200 or the timeout expires.
func waitForQRDBHealthy(b *testing.B, tlsCfg *tls.Config, port int, timeout time.Duration) {
	b.Helper()

	client := qrdbHTTPClient(tlsCfg, 2*time.Second)
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)

	var lastErr error
	for time.Now().Before(deadline) {
		<-ticker.C
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
	}

	b.Fatalf("waitForQRDBHealthy: timed out after %v on port %d; last error: %v", timeout, port, lastErr)
}

// waitForQRDBLeader polls GET /status on all admin ports until at least one
// shard reports is_leader:true. Returns the port of the leader node.
func waitForQRDBLeader(b *testing.B, tlsCfg *tls.Config, ports []int, timeout time.Duration) int {
	b.Helper()

	client := qrdbHTTPClient(tlsCfg, 2*time.Second)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		<-ticker.C
		for _, port := range ports {
			url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
			resp, err := client.Get(url)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			var status map[string]interface{}
			if jsonErr := json.Unmarshal(body, &status); jsonErr != nil {
				continue
			}
			shards, ok := status["shards"].([]interface{})
			if !ok {
				continue
			}
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

	b.Fatalf("waitForQRDBLeader: timed out after %v waiting for leader election", timeout)
	return 0
}

// qrdbWarmupWrite performs a PUT to /kv/warmup-key followed by a DELETE to
// ensure the Raft ReadIndex pipeline is fully operational before benchmarks run.
func qrdbWarmupWrite(b *testing.B, tlsCfg *tls.Config, port int, timeout time.Duration) {
	b.Helper()

	client := qrdbHTTPClient(tlsCfg, 5*time.Second)
	putURL := fmt.Sprintf("https://127.0.0.1:%d/kv/warmup-key", port)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		<-ticker.C
		req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("1")))
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			continue
		}

		delReq, err := http.NewRequest(http.MethodDelete, putURL, nil)
		if err != nil {
			return
		}
		delResp, err := client.Do(delReq)
		if err == nil {
			delResp.Body.Close()
		}
		return
	}

	b.Fatalf("qrdbWarmupWrite: timed out after %v waiting for warmup write on port %d", timeout, port)
}

// createDatabaseWithEtcd creates a KV database via the REST API and then
// enables the etcd wire protocol on the given etcd port.
func createDatabaseWithEtcd(b *testing.B, tlsCfg *tls.Config, adminPort int, dbName string, etcdPort int) {
	b.Helper()

	client := qrdbHTTPClient(tlsCfg, 10*time.Second)

	// Step 1: Create the database.
	createBody := map[string]interface{}{
		"name":    dbName,
		"backend": "pebble",
	}
	createJSON, err := json.Marshal(createBody)
	if err != nil {
		b.Fatalf("createDatabaseWithEtcd: marshal create body: %v", err)
	}

	createURL := fmt.Sprintf("https://127.0.0.1:%d/v1/databases", adminPort)
	resp, err := client.Post(createURL, "application/json", bytes.NewReader(createJSON))
	if err != nil {
		b.Fatalf("createDatabaseWithEtcd: POST /v1/databases: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b.Fatalf("createDatabaseWithEtcd: POST status %d, body: %s", resp.StatusCode, body)
	}

	// Step 2: Enable etcd protocol via PATCH.
	etcdBody := map[string]interface{}{
		"enabled":    true,
		"listenAddr": fmt.Sprintf("127.0.0.1:%d", etcdPort),
	}
	etcdJSON, err := json.Marshal(etcdBody)
	if err != nil {
		b.Fatalf("createDatabaseWithEtcd: marshal etcd body: %v", err)
	}

	etcdURL := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s/etcd", adminPort, dbName)
	req, err := http.NewRequest(http.MethodPatch, etcdURL, bytes.NewReader(etcdJSON))
	if err != nil {
		b.Fatalf("createDatabaseWithEtcd: new PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil {
		b.Fatalf("createDatabaseWithEtcd: PATCH /v1/databases/%s/etcd: %v", dbName, err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("createDatabaseWithEtcd: PATCH status %d, body: %s", resp.StatusCode, body)
	}
}

// waitForEtcdReady polls the etcd gRPC port until the server accepts a
// connection and responds to a Status call, or the timeout expires.
func waitForEtcdReady(b *testing.B, tlsCfg *tls.Config, port int, timeout time.Duration) {
	b.Helper()

	endpoint := fmt.Sprintf("https://127.0.0.1:%d", port)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		<-ticker.C

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{endpoint},
			DialTimeout: 2 * time.Second,
			TLS:         tlsCfg,
			DialOptions: []grpc.DialOption{
				grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
			},
		})
		if err != nil {
			cancel()
			continue
		}

		_, err = cli.Status(ctx, endpoint)
		cancel()
		cli.Close()
		if err == nil {
			return
		}
	}

	b.Fatalf("waitForEtcdReady: timed out after %v waiting for etcd gRPC on port %d", timeout, port)
}

// SetupQRDBEtcdCluster starts a single-node QRDB process with the etcd API
// enabled, waits for it to become healthy and elect a leader, creates a KV
// database with etcd protocol enabled, and returns an EtcdCluster with a
// connected etcd v3 client.
//
// The pre-built binary at /usr/local/bin/qrdb is used (expected inside Docker).
// All TLS certificates are ephemeral and scoped to the benchmark run.
func SetupQRDBEtcdCluster(b *testing.B) *EtcdCluster {
	b.Helper()

	ca := generateQRDBBenchCA(b)
	certPath, keyPath := generateNodeCert(b, ca)

	raftPort := allocateFreeUDPPort(b)
	adminPort := allocateFreeTCPPort(b)
	grpcPort := allocateFreeTCPPort(b)
	etcdPort := allocateFreeTCPPort(b)

	dataDir, err := os.MkdirTemp("", "qrdb-etcd-bench-data-*")
	if err != nil {
		b.Fatalf("SetupQRDBEtcdCluster: MkdirTemp: %v", err)
	}

	args := []string{
		"start",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--replica-id", "1",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--auth-enabled=false",
		"--log-level", "warn",
		"--tls-ca-file", ca.certPath,
		"--tls-cert-file", certPath,
		"--tls-key-file", keyPath,
	}

	cmd := exec.Command(qrdbBinary, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		os.RemoveAll(dataDir)
		b.Fatalf("SetupQRDBEtcdCluster: start qrdb: %v", err)
	}

	// Monitor for early exit in a background goroutine.
	earlyExit := make(chan error, 1)
	go func() {
		earlyExit <- cmd.Wait()
	}()

	// Give the server a moment to start or crash.
	select {
	case waitErr := <-earlyExit:
		os.RemoveAll(dataDir)
		b.Fatalf("SetupQRDBEtcdCluster: qrdb exited immediately: %v", waitErr)
	case <-time.After(3 * time.Second):
		// Process still running, continue.
	}

	b.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		os.RemoveAll(dataDir)
	})

	tlsCfg := ca.mtlsConfig(b)

	waitForQRDBHealthy(b, tlsCfg, adminPort, 30*time.Second)
	waitForQRDBLeader(b, tlsCfg, []int{adminPort}, 30*time.Second)
	qrdbWarmupWrite(b, tlsCfg, adminPort, 10*time.Second)

	createDatabaseWithEtcd(b, tlsCfg, adminPort, qrdbEtcdDBName, etcdPort)
	waitForEtcdReady(b, tlsCfg, etcdPort, 30*time.Second)

	endpoint := fmt.Sprintf("https://127.0.0.1:%d", etcdPort)

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: connectTimeout,
		TLS:         tlsCfg,
		DialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
			grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
		},
	})
	if err != nil {
		b.Fatalf("SetupQRDBEtcdCluster: connect etcd client: %v", err)
	}

	b.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			b.Logf("close qrdb etcd client: %v", closeErr)
		}
	})

	return &EtcdCluster{client: client}
}
