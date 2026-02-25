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

// Package main provides TiKV benchmark tests for KV-level performance
// comparison against QRDB. It connects to a running TiKV cluster via
// the rawkv client with mTLS.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tikv/client-go/v2/config"
	"github.com/tikv/client-go/v2/rawkv"
)

const (
	defaultPayloadSize = 128
	batchSize          = 10000
	scanSize           = 10000
	concurrency        = 8

	// defaultPDAddr is the default PD endpoint for the TiKV cluster.
	defaultPDAddr = "127.0.0.1:2379"

	// Default cert paths (mounted from Docker volume).
	defaultCAPath   = "/certs/ca.pem"
	defaultCertPath = "/certs/client.pem"
	defaultKeyPath  = "/certs/client-key.pem"

	// connectTimeout is the maximum time to wait for a TiKV connection.
	connectTimeout = 30 * time.Second
)

// TiKVCluster holds a rawkv client connected to a TiKV cluster.
type TiKVCluster struct {
	client *rawkv.Client
}

// SetupTiKVCluster connects to an already-running TiKV cluster with TLS.
// The cluster must be started externally (via docker-compose).
func SetupTiKVCluster(b *testing.B) *TiKVCluster {
	b.Helper()

	pdAddr := envOr("TIKV_PD_ADDR", defaultPDAddr)
	caPath := envOr("TIKV_CA_PATH", defaultCAPath)
	certPath := envOr("TIKV_CERT_PATH", defaultCertPath)
	keyPath := envOr("TIKV_KEY_PATH", defaultKeyPath)

	sec := config.Security{
		ClusterSSLCA:   caPath,
		ClusterSSLCert: certPath,
		ClusterSSLKey:  keyPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	client, err := rawkv.NewClient(ctx, []string{"https://" + pdAddr}, sec)
	if err != nil {
		b.Fatalf("connect to TiKV at %s: %v", pdAddr, err)
	}

	b.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			b.Logf("close TiKV client: %v", closeErr)
		}
	})

	return &TiKVCluster{client: client}
}

// envOr returns the value of the environment variable named by key,
// or fallback if the variable is not set or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// clusterNodes is the number of TiKV stores in the benchmark cluster.
// When "3", scenario keys are suffixed with "_3n" to distinguish from 1N results.
var clusterNodes = envOr("TIKV_CLUSTER_NODES", "1")

// scenarioKey returns the scenario key, appending "_3n" when running against
// a multi-node cluster.
func scenarioKey(base string) string {
	if clusterNodes == "3" {
		return base + "_3n"
	}
	return base
}

// seedPoolSize is the number of keys pre-seeded for read and delete
// benchmarks. Benchmarks cycle through these keys with modulo so that
// b.N can grow arbitrarily without the seeding phase dominating runtime.
const seedPoolSize = 10000

// seedKeys writes seedPoolSize keys concurrently using 8 goroutines.
func seedKeys(b *testing.B, client *rawkv.Client, prefix string, value []byte) {
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
				key := []byte(fmt.Sprintf("%s%d", prefix, i))
				if err := client.Put(context.Background(), key, value); err != nil {
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
