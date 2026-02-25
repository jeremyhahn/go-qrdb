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
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startSingleNode starts a single-node memory cluster and returns the admin
// port and the CA certificate pool for HTTPS client verification. It blocks
// until the node is healthy, the admin server is accepting requests, and the
// node has been elected leader.
func startSingleNode(t *testing.T, smType string, extraArgs ...string) (int, *x509.CertPool) {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", smType,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)
	args = append(args, extraArgs...)

	startNode(t, bin, args, "[node1] ")
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 10*time.Second)
	waitForLeaderViaAdmin(t, ca.Pool, []int{adminPorts[0]}, 30*time.Second)

	// Warmup write ensures the leader's no-op entry has been committed so
	// the ReadIndex pipeline is operational for read-only tests.
	warmupWrite(t, ca.Pool, adminPorts[0], 15*time.Second)

	return adminPorts[0], ca.Pool
}

// startThreeNodeCluster starts a 3-node cluster and returns the admin ports,
// data directories, and the CA certificate pool for HTTPS client verification.
// It blocks until all nodes are healthy and a leader has been elected.
func startThreeNodeCluster(t *testing.T, smType string, extraArgs ...string) ([]int, []string, *x509.CertPool) {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", smType,
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, ca.tlsFlags(t)...)
		args = append(args, extraArgs...)

		prefix := fmt.Sprintf("[node%d] ", i+1)
		startNode(t, bin, args, prefix)
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 10*time.Second)
	}

	leaderPort := waitForLeaderViaAdmin(t, ca.Pool, adminPorts, 30*time.Second)

	// Warmup write ensures the leader's no-op entry has been committed so
	// the ReadIndex pipeline is operational.
	warmupWrite(t, ca.Pool, leaderPort, 15*time.Second)

	return adminPorts, dataDirs, ca.Pool
}

// ---------------------------------------------------------------------------
// Single-node KV tests
// ---------------------------------------------------------------------------

func TestKV_SingleNode_PutGetDelete(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// PUT a key.
	httpPut(t, pool, adminPort, "greeting", []byte("hello world"))

	// GET the key and verify value.
	val := httpGet(t, pool, adminPort, "greeting")
	if !bytes.Equal(val, []byte("hello world")) {
		t.Fatalf("GET greeting: got %q, want %q", val, "hello world")
	}

	// DELETE the key.
	httpDelete(t, pool, adminPort, "greeting")

	// GET after DELETE should return 404.
	status := httpGetStatus(t, pool, adminPort, "greeting")
	if status != http.StatusNotFound {
		t.Fatalf("GET after DELETE: got %d, want 404", status)
	}
}

func TestKV_SingleNode_Overwrite(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// PUT initial value.
	httpPut(t, pool, adminPort, "mutable", []byte("version-1"))
	val := httpGet(t, pool, adminPort, "mutable")
	if !bytes.Equal(val, []byte("version-1")) {
		t.Fatalf("initial GET: got %q, want %q", val, "version-1")
	}

	// Overwrite with new value.
	httpPut(t, pool, adminPort, "mutable", []byte("version-2"))
	val = httpGet(t, pool, adminPort, "mutable")
	if !bytes.Equal(val, []byte("version-2")) {
		t.Fatalf("overwrite GET: got %q, want %q", val, "version-2")
	}
}

func TestKV_SingleNode_ListKeys(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Put keys with different prefixes.
	httpPut(t, pool, adminPort, "app/key1", []byte("v1"))
	httpPut(t, pool, adminPort, "app/key2", []byte("v2"))
	httpPut(t, pool, adminPort, "db/key1", []byte("v3"))

	// List with prefix should return only matching keys.
	keys := httpList(t, pool, adminPort, "app/")
	if len(keys) != 2 {
		t.Fatalf("list app/: got %d keys, want 2: %v", len(keys), keys)
	}
	if keys[0] != "app/key1" || keys[1] != "app/key2" {
		t.Errorf("list app/: got %v, want [app/key1, app/key2]", keys)
	}
}

func TestKV_SingleNode_ListAll(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	httpPut(t, pool, adminPort, "a", []byte("1"))
	httpPut(t, pool, adminPort, "b", []byte("2"))
	httpPut(t, pool, adminPort, "c", []byte("3"))

	// Empty prefix returns all keys.
	keys := httpList(t, pool, adminPort, "")
	if len(keys) != 3 {
		t.Fatalf("list all: got %d keys, want 3: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_ListEmpty(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// No keys written -- list should return empty slice.
	keys := httpList(t, pool, adminPort, "")
	if len(keys) != 0 {
		t.Fatalf("list empty store: got %d keys, want 0: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_ListNoMatch(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	httpPut(t, pool, adminPort, "x/1", []byte("v"))

	// Prefix with no matching keys.
	keys := httpList(t, pool, adminPort, "z/")
	if len(keys) != 0 {
		t.Fatalf("list z/: got %d keys, want 0: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_GetNotFound(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	status := httpGetStatus(t, pool, adminPort, "nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("GET nonexistent: got %d, want 404", status)
	}
}

func TestKV_SingleNode_DeleteNonExistent(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Deleting a non-existent key should succeed (idempotent).
	httpDelete(t, pool, adminPort, "ghost")
}

func TestKV_SingleNode_LargeValue(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// 1MB random value.
	largeVal := make([]byte, 1<<20)
	if _, err := rand.Read(largeVal); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	httpPut(t, pool, adminPort, "large", largeVal)
	got := httpGet(t, pool, adminPort, "large")
	if !bytes.Equal(got, largeVal) {
		t.Fatalf("large value mismatch: got %d bytes, want %d", len(got), len(largeVal))
	}
}

func TestKV_SingleNode_BinaryValue(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Non-UTF8 binary data including null bytes.
	binVal := []byte{0x00, 0xFF, 0xFE, 0x80, 0x90, 0x00, 0x01}
	httpPut(t, pool, adminPort, "binary", binVal)
	got := httpGet(t, pool, adminPort, "binary")
	if !bytes.Equal(got, binVal) {
		t.Fatalf("binary value mismatch: got %v, want %v", got, binVal)
	}
}

func TestKV_SingleNode_EmptyValue(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	httpPut(t, pool, adminPort, "empty", []byte{})
	got := httpGet(t, pool, adminPort, "empty")
	if len(got) != 0 {
		t.Fatalf("empty value: got %d bytes, want 0", len(got))
	}
}

func TestKV_SingleNode_SpecialCharKey(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Keys containing URL-safe special characters.
	httpPut(t, pool, adminPort, "path/to/deep/key", []byte("deep"))
	got := httpGet(t, pool, adminPort, "path/to/deep/key")
	if !bytes.Equal(got, []byte("deep")) {
		t.Fatalf("deep key: got %q, want %q", got, "deep")
	}
}

func TestKV_SingleNode_ManyKeys(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Write 200 sequential keys.
	const count = 200
	for i := range count {
		key := fmt.Sprintf("seq/%04d", i)
		httpPut(t, pool, adminPort, key, []byte(fmt.Sprintf("v%d", i)))
	}

	// Verify count via list.
	keys := httpList(t, pool, adminPort, "seq/")
	if len(keys) != count {
		t.Fatalf("list seq/: got %d keys, want %d", len(keys), count)
	}

	// Spot-check first and last values.
	first := httpGet(t, pool, adminPort, "seq/0000")
	if !bytes.Equal(first, []byte("v0")) {
		t.Errorf("seq/0000: got %q, want %q", first, "v0")
	}
	last := httpGet(t, pool, adminPort, fmt.Sprintf("seq/%04d", count-1))
	if !bytes.Equal(last, []byte(fmt.Sprintf("v%d", count-1))) {
		t.Errorf("seq/%04d: got %q, want %q", count-1, last, fmt.Sprintf("v%d", count-1))
	}
}

func TestKV_SingleNode_DeleteThenReinsert(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	httpPut(t, pool, adminPort, "cycle", []byte("first"))
	httpDelete(t, pool, adminPort, "cycle")

	// Verify deletion.
	status := httpGetStatus(t, pool, adminPort, "cycle")
	if status != http.StatusNotFound {
		t.Fatalf("after delete: got %d, want 404", status)
	}

	// Re-insert with different value.
	httpPut(t, pool, adminPort, "cycle", []byte("second"))
	got := httpGet(t, pool, adminPort, "cycle")
	if !bytes.Equal(got, []byte("second")) {
		t.Fatalf("after reinsert: got %q, want %q", got, "second")
	}
}

// ---------------------------------------------------------------------------
// Three-node cluster KV tests
// ---------------------------------------------------------------------------

func TestKV_ThreeNode_PutGetFromAnyNode(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	// Write on leader.
	httpPut(t, pool, leaderPort, "cluster-key", []byte("cluster-value"))

	// Read from each node (linearizable reads go through leader).
	for i, port := range adminPorts {
		waitForKV(t, pool, port, "cluster-key", []byte("cluster-value"), 10*time.Second)
		val := httpGet(t, pool, port, "cluster-key")
		if !bytes.Equal(val, []byte("cluster-value")) {
			t.Errorf("node %d: got %q, want %q", i+1, val, "cluster-value")
		}
	}
}

func TestKV_ThreeNode_AllEndpoints(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	// Status endpoint.
	status := httpStatus(t, pool, leaderPort)
	if status["node_id"] == nil {
		t.Error("status missing node_id")
	}

	// Health endpoint.
	code := httpHealth(t, pool, leaderPort)
	if code != http.StatusOK {
		t.Errorf("health: got %d, want 200", code)
	}

	// Put + Get + Delete + List.
	httpPut(t, pool, leaderPort, "test/a", []byte("1"))
	httpPut(t, pool, leaderPort, "test/b", []byte("2"))

	val := httpGet(t, pool, leaderPort, "test/a")
	if !bytes.Equal(val, []byte("1")) {
		t.Errorf("GET test/a: got %q, want %q", val, "1")
	}

	keys := httpList(t, pool, leaderPort, "test/")
	if len(keys) != 2 {
		t.Errorf("list test/: got %d keys, want 2", len(keys))
	}

	httpDelete(t, pool, leaderPort, "test/a")
	s := httpGetStatus(t, pool, leaderPort, "test/a")
	if s != http.StatusNotFound {
		t.Errorf("GET after DELETE: got %d, want 404", s)
	}
}

func TestKV_ThreeNode_ReadAfterWrite(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	// Write then immediately read -- linearizable consistency guarantee.
	for i := range 10 {
		key := fmt.Sprintf("raw/%d", i)
		val := fmt.Sprintf("value-%d", i)
		httpPut(t, pool, leaderPort, key, []byte(val))
		got := httpGet(t, pool, leaderPort, key)
		if string(got) != val {
			t.Fatalf("read-after-write %s: got %q, want %q", key, got, val)
		}
	}
}

func TestKV_ThreeNode_ReplicatedList(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	// Write a batch of keys on the leader.
	const count = 25
	for i := range count {
		key := fmt.Sprintf("rep/%04d", i)
		httpPut(t, pool, leaderPort, key, []byte(fmt.Sprintf("v%d", i)))
	}

	// Verify that all nodes converge on the same key set.
	for _, port := range adminPorts {
		// Poll until replication catches up.
		var keys []string
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			keys = httpList(t, pool, port, "rep/")
			if len(keys) == count {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(keys) != count {
			t.Fatalf("port %d: expected %d keys, got %d", port, count, len(keys))
		}
	}
}

func TestKV_ThreeNode_DeleteReplicates(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	httpPut(t, pool, leaderPort, "del-test", []byte("exists"))

	// Confirm key is readable on all nodes first.
	for _, port := range adminPorts {
		waitForKV(t, pool, port, "del-test", []byte("exists"), 10*time.Second)
	}

	// Delete on leader.
	httpDelete(t, pool, leaderPort, "del-test")

	// Confirm deletion replicates to all nodes.
	for _, port := range adminPorts {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			s := httpGetStatus(t, pool, port, "del-test")
			if s == http.StatusNotFound {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		s := httpGetStatus(t, pool, port, "del-test")
		if s != http.StatusNotFound {
			t.Errorf("port %d: GET after replicated DELETE: got %d, want 404", port, s)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrent load test
// ---------------------------------------------------------------------------

// testStats tracks operation counts and latency during concurrent load tests.
type testStats struct {
	mu               sync.Mutex
	putsAttempted    int64
	putsSucceeded    int64
	putsFailed       int64
	getsAttempted    int64
	getsSucceeded    int64
	getsFailed       int64
	deletesAttempted int64
	deletesSucceeded int64
	deletesFailed    int64
	totalLatency     time.Duration
	minLatency       time.Duration
	maxLatency       time.Duration
}

func (s *testStats) recordPut(ok bool, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putsAttempted++
	if ok {
		s.putsSucceeded++
	} else {
		s.putsFailed++
	}
	s.recordLatencyLocked(d)
}

func (s *testStats) recordGet(ok bool, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getsAttempted++
	if ok {
		s.getsSucceeded++
	} else {
		s.getsFailed++
	}
	s.recordLatencyLocked(d)
}

func (s *testStats) recordLatencyLocked(d time.Duration) {
	s.totalLatency += d
	if s.minLatency == 0 || d < s.minLatency {
		s.minLatency = d
	}
	if d > s.maxLatency {
		s.maxLatency = d
	}
}

func (s *testStats) log(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.putsAttempted + s.getsAttempted + s.deletesAttempted
	t.Logf("Stats: total=%d puts=%d/%d gets=%d/%d deletes=%d/%d",
		total,
		s.putsSucceeded, s.putsAttempted,
		s.getsSucceeded, s.getsAttempted,
		s.deletesSucceeded, s.deletesAttempted)
	if total > 0 {
		t.Logf("Latency: min=%v max=%v avg=%v",
			s.minLatency, s.maxLatency, s.totalLatency/time.Duration(total))
	}
}

func TestKV_ThreeNode_ConcurrentLoad(t *testing.T) {
	adminPorts, _, pool := startThreeNodeCluster(t, "memory")
	leaderPort := waitForLeaderViaAdmin(t, pool, adminPorts, 30*time.Second)

	stats := &testStats{}
	var wg sync.WaitGroup
	var errCount atomic.Int64

	numWorkers := 10
	keysPerWorker := 50

	for w := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := caHTTPClient(pool, 30*time.Second)
			for k := range keysPerWorker {
				key := fmt.Sprintf("worker%d/key%d", workerID, k)
				val := fmt.Sprintf("value-%d-%d", workerID, k)

				// PUT
				start := time.Now()
				url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", leaderPort, key)
				req, reqErr := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte(val)))
				if reqErr != nil {
					errCount.Add(1)
					stats.recordPut(false, time.Since(start))
					continue
				}
				resp, err := client.Do(req)
				putOK := err == nil && resp != nil && resp.StatusCode == http.StatusNoContent
				if resp != nil {
					resp.Body.Close()
				}
				stats.recordPut(putOK, time.Since(start))
				if !putOK {
					errCount.Add(1)
					continue
				}

				// GET
				start = time.Now()
				resp, err = client.Get(url)
				getOK := err == nil && resp != nil && resp.StatusCode == http.StatusOK
				if resp != nil {
					resp.Body.Close()
				}
				stats.recordGet(getOK, time.Since(start))
				if !getOK {
					errCount.Add(1)
				}
			}
		}(w)
	}

	wg.Wait()
	stats.log(t)

	if errCount.Load() > 0 {
		t.Logf("warning: %d errors during concurrent load", errCount.Load())
	}

	// Verify keys are queryable after the load completes.
	totalExpected := numWorkers * keysPerWorker
	keys := httpList(t, pool, leaderPort, "worker")
	t.Logf("total keys after concurrent load: %d (expected %d)", len(keys), totalExpected)

	// Allow tolerance for transient failures under load in Docker environments.
	// The majority of keys must have been written successfully.
	minAcceptable := totalExpected * 80 / 100
	if len(keys) < minAcceptable {
		t.Fatalf("too few keys after load: got %d, minimum acceptable %d", len(keys), minAcceptable)
	}
}

func TestKV_SingleNode_ConcurrentSameKey(t *testing.T) {
	adminPort, pool := startSingleNode(t, "memory")

	// Multiple goroutines writing to the same key concurrently.
	var wg sync.WaitGroup
	const writers = 20
	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			val := fmt.Sprintf("writer-%d", id)
			httpPut(t, pool, adminPort, "contested", []byte(val))
		}(w)
	}
	wg.Wait()

	// The key should exist with one of the written values.
	got := httpGet(t, pool, adminPort, "contested")
	if len(got) == 0 {
		t.Fatal("contested key has empty value after concurrent writes")
	}
}
