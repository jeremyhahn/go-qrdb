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
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiProtocol_ConcurrentAccess creates one test environment per
// protocol and then exercises simultaneous cross-protocol reads and writes:
//
//   - Phase 1: each protocol writes a unique set of keys concurrently.
//   - Phase 2: each protocol reads keys written by every other protocol,
//     verifying that data written through any transport is immediately
//     visible through any other transport.
//
// This test validates the shared qrdbd server as the single source of truth
// that all transports converge on.
func TestMultiProtocol_ConcurrentAccess(t *testing.T) {
	protocols := allProtocols()

	// Build one env per protocol. If a protocol is unavailable, t.Skip is
	// called inside newTestEnv so the subtest is skipped rather than failing.
	envs := make(map[protocol]*testEnv, len(protocols))
	for _, proto := range protocols {
		proto := proto
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("skipping %s: panic during env setup: %v", proto, r)
				}
			}()
			envs[proto] = newTestEnv(t, proto)
		}()
	}

	if len(envs) == 0 {
		t.Skip("no protocols available")
	}

	const keysPerProtocol = 10
	ctx := context.Background()

	// Phase 1: write keys concurrently, one goroutine per protocol.
	var writeWg sync.WaitGroup
	writeErrors := make(map[protocol]error, len(envs))
	var writeErrMu sync.Mutex

	for proto, env := range envs {
		proto, env := proto, env
		writeWg.Add(1)
		go func() {
			defer writeWg.Done()
			for i := 0; i < keysPerProtocol; i++ {
				key := fmt.Sprintf("multiproto/%s/%d", proto, i)
				if err := env.client.KV().Put(ctx, key, []byte(string(proto))); err != nil {
					writeErrMu.Lock()
					writeErrors[proto] = fmt.Errorf("Put key=%q: %w", key, err)
					writeErrMu.Unlock()
					return
				}
			}
		}()
	}
	writeWg.Wait()

	for proto, err := range writeErrors {
		require.NoError(t, err, "write phase failed for %s", proto)
	}

	// Phase 2: cross-protocol reads — each protocol reads keys written by all
	// other protocols through the vtab.
	var readWg sync.WaitGroup
	readErrors := make(map[protocol]error, len(envs))
	var readErrMu sync.Mutex

	for readerProto, readerEnv := range envs {
		readerProto, readerEnv := readerProto, readerEnv
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			for writerProto := range envs {
				for i := 0; i < keysPerProtocol; i++ {
					key := fmt.Sprintf("multiproto/%s/%d", writerProto, i)
					rows := scanAllRows(t, readerEnv.db, "SELECT key, value FROM kv WHERE key = ?", key)
					if len(rows) != 1 {
						readErrMu.Lock()
						if readErrors[readerProto] == nil {
							readErrors[readerProto] = fmt.Errorf(
								"reader=%s: key %q written by %s not visible (got %d rows)",
								readerProto, key, writerProto, len(rows))
						}
						readErrMu.Unlock()
						return
					}
					gotVal, _ := rows[0]["value"].([]byte)
					if string(gotVal) != string(writerProto) {
						readErrMu.Lock()
						if readErrors[readerProto] == nil {
							readErrors[readerProto] = fmt.Errorf(
								"reader=%s: key %q: want value %q, got %q",
								readerProto, key, writerProto, gotVal)
						}
						readErrMu.Unlock()
						return
					}
				}
			}
		}()
	}
	readWg.Wait()

	for proto, err := range readErrors {
		assert.NoError(t, err, "read phase failed for %s", proto)
	}
}

// TestMultiProtocol_WriteRestReadAll writes a single key through the REST
// transport vtab and verifies it is immediately visible through every other
// protocol's vtab. This is a targeted cross-protocol visibility check.
func TestMultiProtocol_WriteRestReadAll(t *testing.T) {
	key := "multiproto/rest-origin"
	value := []byte("rest-written")

	// Write through REST.
	restEnv := newTestEnv(t, protoREST)
	_, err := restEnv.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, value)
	require.NoError(t, err, "INSERT via REST vtab")

	// Read through every protocol.
	for _, proto := range allProtocols() {
		if proto == protoREST {
			continue
		}
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			env := newTestEnv(t, proto)
			rows := scanAllRows(t, env.db, "SELECT key, value FROM kv WHERE key = ?", key)
			require.Len(t, rows, 1, "REST-written key must be visible via %s vtab", proto)
			gotVal, _ := rows[0]["value"].([]byte)
			assert.Equal(t, value, gotVal, "value mismatch reading via %s", proto)
		})
	}
}

// TestMultiProtocol_WriteGRPCReadAll writes a single key through the gRPC
// transport vtab and verifies it is immediately visible through every other
// protocol's vtab.
func TestMultiProtocol_WriteGRPCReadAll(t *testing.T) {
	key := "multiproto/grpc-origin"
	value := []byte("grpc-written")

	// Write through gRPC.
	grpcEnv := newTestEnv(t, protoGRPC)
	_, err := grpcEnv.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, value)
	require.NoError(t, err, "INSERT via gRPC vtab")

	// Read through every protocol.
	for _, proto := range allProtocols() {
		if proto == protoGRPC {
			continue
		}
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			env := newTestEnv(t, proto)
			rows := scanAllRows(t, env.db, "SELECT key, value FROM kv WHERE key = ?", key)
			require.Len(t, rows, 1, "gRPC-written key must be visible via %s vtab", proto)
			gotVal, _ := rows[0]["value"].([]byte)
			assert.Equal(t, value, gotVal, "value mismatch reading via %s", proto)
		})
	}
}

// TestMultiProtocol_SDKCrossRead uses the raw SDK client of each protocol to
// read a key written by a different protocol's SDK client, confirming that
// the shared qrdbd server serves as the authoritative single source of truth.
func TestMultiProtocol_SDKCrossRead(t *testing.T) {
	protocols := allProtocols()
	envs := make(map[protocol]*testEnv, len(protocols))
	for _, proto := range protocols {
		envs[proto] = newTestEnv(t, proto)
	}

	ctx := context.Background()
	const n = 5

	// Write n keys via each protocol's SDK.
	for writerProto, env := range envs {
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("multiproto/sdk/%s/%d", writerProto, i)
			require.NoError(t, env.client.KV().Put(ctx, key, []byte(string(writerProto))),
				"SDK Put key=%q via %s", key, writerProto)
		}
	}

	// Each protocol's SDK reads keys written by all other protocols.
	for readerProto, readerEnv := range envs {
		for writerProto := range envs {
			if readerProto == writerProto {
				continue
			}
			for i := 0; i < n; i++ {
				key := fmt.Sprintf("multiproto/sdk/%s/%d", writerProto, i)
				val, err := readerEnv.client.KV().Get(ctx, key)
				require.NoError(t, err,
					"reader=%s: SDK Get key=%q written by %s", readerProto, key, writerProto)
				assert.Equal(t, []byte(string(writerProto)), val,
					"reader=%s: unexpected value for key=%q written by %s",
					readerProto, key, writerProto)
			}
		}
	}
}
