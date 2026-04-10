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

// Package main provides etcd v3 API benchmark tests for performance comparison
// against QRDB's etcd compatibility layer and a real etcd server.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// defaultPayloadSize is the value size in bytes for benchmark KV writes.
	defaultPayloadSize = 128

	// concurrency is the number of goroutines for concurrent benchmarks.
	concurrency = 8

	// seedPoolSize is the number of keys pre-seeded for read and delete benchmarks.
	// Kept lower than the KV REST benchmarks (10,000) because seeding goes through
	// the gRPC etcd API with per-key consensus, which is slower than direct REST.
	seedPoolSize = 1000

	// defaultEndpoint is the etcd server endpoint used when ETCD_ENDPOINT is unset.
	defaultEndpoint = "127.0.0.1:2379"

	// Default cert paths (mounted from Docker volume).
	defaultCAPath   = "/certs/ca.pem"
	defaultCertPath = "/certs/client.pem"
	defaultKeyPath  = "/certs/client-key.pem"

	// connectTimeout is the maximum time to wait for an etcd connection.
	connectTimeout = 30 * time.Second

	// seedTimeout is the maximum total time allowed for pre-seeding keys.
	// Individual put operations within seeding use a 5s per-op timeout.
	seedTimeout = 60 * time.Second
)

// systemName identifies the system under test in benchmark results.
// Defaults to "qrdb-etcd" but can be overridden to "etcd" when benchmarking
// a real etcd server.
var systemName = envOr("ETCD_BENCH_SYSTEM", "qrdb-etcd")

// benchMode selects the cluster setup strategy:
//   - "external": connect to an already-running server (cert files at /certs/)
//   - "qrdb":     start a QRDB server internally with etcd API enabled
var benchMode = envOr("ETCD_BENCH_MODE", "external")

// clusterNodes is the number of etcd nodes in the benchmark cluster.
// When "3", scenario keys are suffixed with "_3n" to distinguish from 1N results.
var clusterNodes = envOr("ETCD_CLUSTER_NODES", "1")

// EtcdCluster holds an etcd v3 client connected to a server (either real
// etcd or QRDB's etcd API).
type EtcdCluster struct {
	client *clientv3.Client
}

// SetupEtcdCluster returns an EtcdCluster ready for benchmarking.
// The setup strategy is controlled by the ETCD_BENCH_MODE environment variable:
//   - "external" (default): connect to an already-running server
//   - "qrdb": start a QRDB server internally with etcd API enabled
func SetupEtcdCluster(b *testing.B) *EtcdCluster {
	b.Helper()
	if benchMode == "qrdb" {
		return SetupQRDBEtcdCluster(b)
	}
	return setupExternalEtcd(b)
}

// setupExternalEtcd connects to an already-running etcd or QRDB-etcd server.
// The server must be started externally (via docker-compose or similar).
//
// Configuration is driven by environment variables:
//   - ETCD_ENDPOINT:  server address (default: 127.0.0.1:2379)
//   - ETCD_CA_PATH:   CA certificate file path (default: /certs/ca.pem)
//   - ETCD_CERT_PATH: client certificate file path (default: /certs/client.pem)
//   - ETCD_KEY_PATH:  client key file path (default: /certs/client-key.pem)
//   - ETCD_USE_TLS:   whether to enable mTLS (default: "true")
func setupExternalEtcd(b *testing.B) *EtcdCluster {
	b.Helper()

	endpoint := envOr("ETCD_ENDPOINT", defaultEndpoint)
	useTLS := envOr("ETCD_USE_TLS", "true")

	cfg := clientv3.Config{
		DialTimeout: connectTimeout,
	}

	// Disable the etcd client's default WaitForReady(true). Without this,
	// gRPC calls with context.Background() block indefinitely on transient
	// connection issues instead of failing fast. This is the root cause of
	// concurrent benchmark hangs — not a timeout issue.
	cfg.DialOptions = []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	}

	if useTLS == "true" {
		caPath := envOr("ETCD_CA_PATH", defaultCAPath)
		certPath := envOr("ETCD_CERT_PATH", defaultCertPath)
		keyPath := envOr("ETCD_KEY_PATH", defaultKeyPath)

		tlsCfg, err := buildTLSConfig(caPath, certPath, keyPath)
		if err != nil {
			b.Fatalf("setupExternalEtcd: build TLS config: %v", err)
		}

		// Set TLS on the clientv3.Config so the client uses the https://
		// scheme and negotiates TLS correctly. Also pass credentials via
		// DialOptions for consistent behavior with the qrdb-etcd setup.
		cfg.TLS = tlsCfg
		cfg.DialOptions = append(cfg.DialOptions,
			grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		)

		// Prepend https:// scheme so the etcd client resolves the endpoint
		// with TLS transport. Without the scheme, the client defaults to
		// plaintext and the TLS handshake fails silently.
		if endpoint[0] != '/' && !hasScheme(endpoint) {
			endpoint = "https://" + endpoint
		}
	}

	cfg.Endpoints = []string{endpoint}

	client, err := clientv3.New(cfg)
	if err != nil {
		b.Fatalf("setupExternalEtcd: connect to %s: %v", endpoint, err)
	}

	b.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			b.Logf("close etcd client: %v", closeErr)
		}
	})

	return &EtcdCluster{client: client}
}

// hasScheme returns true if the string starts with a URL scheme (e.g., "https://").
func hasScheme(s string) bool {
	for i, c := range s {
		if c == ':' {
			return i > 0
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	return false
}

// buildTLSConfig loads the CA, client cert, and client key from disk and
// returns a *tls.Config for mTLS connections.
func buildTLSConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse CA certificate from %s", caPath)
	}

	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// envOr returns the value of the environment variable named by key,
// or fallback if the variable is not set or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// scenarioKey returns the scenario key, appending "_3n" when running against
// a multi-node cluster.
func scenarioKey(base string) string {
	if clusterNodes == "3" {
		return base + "_3n"
	}
	return base
}

// seedKeys writes seedPoolSize keys concurrently using 8 goroutines.
// The key format is "<prefix><index>" where index is 0..seedPoolSize-1.
// A global deadline ensures seeding never blocks the benchmark suite for
// more than seedTimeout regardless of individual operation latency.
func seedKeys(b *testing.B, client *clientv3.Client, prefix string, value []byte) {
	b.Helper()

	seedCtx, seedCancel := context.WithTimeout(context.Background(), seedTimeout)
	defer seedCancel()

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
				ctx, cancel := context.WithTimeout(seedCtx, 5*time.Second)
				if _, err := client.Put(ctx, key, string(value)); err != nil {
					cancel()
					b.Errorf("seed %s: %v", key, err)
					return
				}
				cancel()
			}
		}()
	}
	wg.Wait()
}

// generatePayload creates a deterministic payload of the given size.
func generatePayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i % 256)
	}
	return p
}
