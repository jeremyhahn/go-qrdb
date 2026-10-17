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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startSingleNode starts a single-node cluster over the given state machine
// and returns the admin port and the test CA for HTTPS mTLS client
// verification. It blocks until the node is healthy, the admin server is
// accepting requests, and the node has been elected leader.
//
// smType is any registered raft.sm_type: every one serves the guarded batches
// qrdbd's boot-time role/user stores require (go-qrdb#555, go-qrdb#136).
//
// This node's TLS comes from ca.tlsFlags, a bring-your-own leaf certificate
// signed by the test's OWN CA -- the daemon never holds that CA's private
// key, so ceremonyCAFromBundle leaves s.ceremonyCA nil (see
// pkg/api/rest/handlers_ceremony.go). qrdb init supports that directly
// (go-qrdb#548): with no CeremonyCA it does not self-issue a TLS
// certificate, pinning the operator's own configured leaf instead, and the
// bring-your-own-certificate admin-credential path (initAndUnsealBarrier's
// admin_cert_pem) needs no CA at all -- the test's own CA plays the
// operator's CA, present in ca's client-CA trust pool. CSR-sign and
// generate remain refused on a node like this one (there is no CA to sign
// with); nothing here uses them.
func startSingleNode(t *testing.T, smType string, extraArgs ...string) (int, *testCA) {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()
	cfgPath := writeNoneSealConfig(t)

	args := []string{
		"start",
		"--config", cfgPath,
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
	// "qrdb init" is the only system bootstrap (go-qrdb#548): auth defaults
	// to enabled, which now requires the seal subsystem (writeNoneSealConfig
	// above), so the node comes up through the sealed window and needs its
	// barrier brought up and its admin ceremony run before first boot can
	// bind an identity and the full server starts.
	waitForBarrierStatusReachable(t, ca, adminPorts[0], 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPorts[0], "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)

	// Warmup write ensures the leader's no-op entry has been committed so
	// the ReadIndex pipeline is operational for read-only tests.
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	return adminPorts[0], ca
}

// startThreeNodeCluster starts a 3-node cluster and returns the admin ports,
// data directories, and the test CA for HTTPS mTLS client verification. It
// blocks until all nodes are healthy and a leader has been elected.
func startThreeNodeCluster(t *testing.T, smType string, extraArgs ...string) ([]int, []string, *testCA) {
	t.Helper()
	adminPorts, dataDirs, ca, _ := startThreeNodeClusterLogged(t, smType, extraArgs...)
	return adminPorts, dataDirs, ca
}

// startThreeNodeClusterLogged is startThreeNodeCluster that also returns
// each node's captured log output, for assertions on what a node reported.
//
// Each node's system barrier is local, so each one runs its own ceremony
// during its own sealed window, in parallel, exactly as
// admin_cluster_cli_test.go's start3NodeMultiProtocolCluster does.
// initAndUnsealBarrier's admin identity is this CA's ONE shared bootstrap
// admin certificate (testCA.adminIdentity), so calling it once per node
// hands every one of them the identical bring-your-own-certificate: first
// boot on whichever node's write lands first binds it, and the other two
// nodes' writes are idempotent no-ops once raft replicates it. ca.tlsFlags
// and ca.clusterNodeTLS are also bring-your-own (the daemon holds no CA
// private key), which qrdb init supports directly (go-qrdb#548): no TLS
// certificate is self-issued, and BYOC needs no CA at all.
func startThreeNodeClusterLogged(t *testing.T, smType string, extraArgs ...string) ([]int, []string, *testCA, []*nodeLog) {
	t.Helper()
	logs := make([]*nodeLog, 3)
	bin := buildBinary(t)
	ca := generateTestCA(t)
	cfgPath := writeNoneSealConfig(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, len(ports))
	dataDirs := make([]string, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", smType,
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, nodeTLS[i]...)
		args = append(args, extraArgs...)

		prefix := fmt.Sprintf("[node%d] ", i+1)
		logs[i] = &nodeLog{}
		logs[i].cmd = startNode(t, bin, args, prefix, logs[i])
	}

	var sealWg sync.WaitGroup
	sealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], "")
		}(i)
	}
	sealWg.Wait()

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Warmup write ensures the leader's no-op entry has been committed so
	// the ReadIndex pipeline is operational.
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	return adminPorts, dataDirs, ca, logs
}

// nodeLog captures a node's log output for assertions. It is safe for the
// daemon's output goroutine to write while a test reads.
type nodeLog struct {
	mu  sync.Mutex
	buf bytes.Buffer

	// cmd is the node's process, for tests that stop it.
	cmd *exec.Cmd
}

func (l *nodeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// Contains reports whether the node has logged s.
func (l *nodeLog) Contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.buf.String(), s)
}

// String returns everything the node has logged so far.
func (l *nodeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// ---------------------------------------------------------------------------
// Single-node KV tests
// ---------------------------------------------------------------------------

func TestKV_SingleNode_PutGetDelete(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// PUT a key.
	httpPut(t, ca, adminPort, "greeting", []byte("hello world"))

	// GET the key and verify value.
	val := httpGet(t, ca, adminPort, "greeting")
	if !bytes.Equal(val, []byte("hello world")) {
		t.Fatalf("GET greeting: got %q, want %q", val, "hello world")
	}

	// DELETE the key.
	httpDelete(t, ca, adminPort, "greeting")

	// GET after DELETE should return 404.
	status := httpGetStatus(t, ca, adminPort, "greeting")
	if status != http.StatusNotFound {
		t.Fatalf("GET after DELETE: got %d, want 404", status)
	}
}

func TestKV_SingleNode_Overwrite(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// PUT initial value.
	httpPut(t, ca, adminPort, "mutable", []byte("version-1"))
	val := httpGet(t, ca, adminPort, "mutable")
	if !bytes.Equal(val, []byte("version-1")) {
		t.Fatalf("initial GET: got %q, want %q", val, "version-1")
	}

	// Overwrite with new value.
	httpPut(t, ca, adminPort, "mutable", []byte("version-2"))
	val = httpGet(t, ca, adminPort, "mutable")
	if !bytes.Equal(val, []byte("version-2")) {
		t.Fatalf("overwrite GET: got %q, want %q", val, "version-2")
	}
}

func TestKV_SingleNode_ListKeys(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Put keys with different prefixes.
	httpPut(t, ca, adminPort, "app/key1", []byte("v1"))
	httpPut(t, ca, adminPort, "app/key2", []byte("v2"))
	httpPut(t, ca, adminPort, "db/key1", []byte("v3"))

	// List with prefix should return only matching keys.
	keys := httpList(t, ca, adminPort, "app/")
	if len(keys) != 2 {
		t.Fatalf("list app/: got %d keys, want 2: %v", len(keys), keys)
	}
	if keys[0] != "app/key1" || keys[1] != "app/key2" {
		t.Errorf("list app/: got %v, want [app/key1, app/key2]", keys)
	}
}

func TestKV_SingleNode_ListAll(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	httpPut(t, ca, adminPort, "a", []byte("1"))
	httpPut(t, ca, adminPort, "b", []byte("2"))
	httpPut(t, ca, adminPort, "c", []byte("3"))

	// Empty prefix returns all keys.
	keys := httpList(t, ca, adminPort, "")
	if len(keys) != 3 {
		t.Fatalf("list all: got %d keys, want 3: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_ListEmpty(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// No keys written -- list should return empty slice.
	keys := httpList(t, ca, adminPort, "")
	if len(keys) != 0 {
		t.Fatalf("list empty store: got %d keys, want 0: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_ListNoMatch(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	httpPut(t, ca, adminPort, "x/1", []byte("v"))

	// Prefix with no matching keys.
	keys := httpList(t, ca, adminPort, "z/")
	if len(keys) != 0 {
		t.Fatalf("list z/: got %d keys, want 0: %v", len(keys), keys)
	}
}

func TestKV_SingleNode_GetNotFound(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	status := httpGetStatus(t, ca, adminPort, "nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("GET nonexistent: got %d, want 404", status)
	}
}

func TestKV_SingleNode_DeleteNonExistent(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Deleting a non-existent key should succeed (idempotent).
	httpDelete(t, ca, adminPort, "ghost")
}

func TestKV_SingleNode_LargeValue(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// 1MB random value.
	largeVal := make([]byte, 1<<20)
	if _, err := rand.Read(largeVal); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	httpPut(t, ca, adminPort, "large", largeVal)
	got := httpGet(t, ca, adminPort, "large")
	if !bytes.Equal(got, largeVal) {
		t.Fatalf("large value mismatch: got %d bytes, want %d", len(got), len(largeVal))
	}
}

func TestKV_SingleNode_BinaryValue(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Non-UTF8 binary data including null bytes.
	binVal := []byte{0x00, 0xFF, 0xFE, 0x80, 0x90, 0x00, 0x01}
	httpPut(t, ca, adminPort, "binary", binVal)
	got := httpGet(t, ca, adminPort, "binary")
	if !bytes.Equal(got, binVal) {
		t.Fatalf("binary value mismatch: got %v, want %v", got, binVal)
	}
}

func TestKV_SingleNode_EmptyValue(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	httpPut(t, ca, adminPort, "empty", []byte{})
	got := httpGet(t, ca, adminPort, "empty")
	if len(got) != 0 {
		t.Fatalf("empty value: got %d bytes, want 0", len(got))
	}
}

func TestKV_SingleNode_SpecialCharKey(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Keys containing URL-safe special characters.
	httpPut(t, ca, adminPort, "path/to/deep/key", []byte("deep"))
	got := httpGet(t, ca, adminPort, "path/to/deep/key")
	if !bytes.Equal(got, []byte("deep")) {
		t.Fatalf("deep key: got %q, want %q", got, "deep")
	}
}

func TestKV_SingleNode_ManyKeys(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Write 200 sequential keys.
	const count = 200
	for i := range count {
		key := fmt.Sprintf("seq/%04d", i)
		httpPut(t, ca, adminPort, key, []byte(fmt.Sprintf("v%d", i)))
	}

	// Verify count via list.
	keys := httpList(t, ca, adminPort, "seq/")
	if len(keys) != count {
		t.Fatalf("list seq/: got %d keys, want %d", len(keys), count)
	}

	// Spot-check first and last values.
	first := httpGet(t, ca, adminPort, "seq/0000")
	if !bytes.Equal(first, []byte("v0")) {
		t.Errorf("seq/0000: got %q, want %q", first, "v0")
	}
	last := httpGet(t, ca, adminPort, fmt.Sprintf("seq/%04d", count-1))
	if !bytes.Equal(last, []byte(fmt.Sprintf("v%d", count-1))) {
		t.Errorf("seq/%04d: got %q, want %q", count-1, last, fmt.Sprintf("v%d", count-1))
	}
}

func TestKV_SingleNode_DeleteThenReinsert(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	httpPut(t, ca, adminPort, "cycle", []byte("first"))
	httpDelete(t, ca, adminPort, "cycle")

	// Verify deletion.
	status := httpGetStatus(t, ca, adminPort, "cycle")
	if status != http.StatusNotFound {
		t.Fatalf("after delete: got %d, want 404", status)
	}

	// Re-insert with different value.
	httpPut(t, ca, adminPort, "cycle", []byte("second"))
	got := httpGet(t, ca, adminPort, "cycle")
	if !bytes.Equal(got, []byte("second")) {
		t.Fatalf("after reinsert: got %q, want %q", got, "second")
	}
}

// ---------------------------------------------------------------------------
// Three-node cluster KV tests
// ---------------------------------------------------------------------------

func TestKV_ThreeNode_PutGetFromAnyNode(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write on leader.
	httpPut(t, ca, leaderPort, "cluster-key", []byte("cluster-value"))

	// Read from each node (linearizable reads go through leader). A node
	// that knows no leader for the moment answers the read 503 NOT_LEADER,
	// which docs/clustering/engineering.md documents as retryable, so each
	// node's read is waitForKV's bounded retry rather than a single GET.
	for _, port := range adminPorts {
		waitForKV(t, ca, port, "cluster-key", []byte("cluster-value"), 60*time.Second)
	}
}

func TestKV_ThreeNode_AllEndpoints(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Status endpoint.
	status := httpStatus(t, ca, leaderPort)
	if status["node_id"] == nil {
		t.Error("status missing node_id")
	}

	// Health endpoint.
	code := httpHealth(t, ca.Pool, leaderPort)
	if code != http.StatusOK {
		t.Errorf("health: got %d, want 200", code)
	}

	// Put + Get + Delete + List.
	httpPut(t, ca, leaderPort, "test/a", []byte("1"))
	httpPut(t, ca, leaderPort, "test/b", []byte("2"))

	val := httpGet(t, ca, leaderPort, "test/a")
	if !bytes.Equal(val, []byte("1")) {
		t.Errorf("GET test/a: got %q, want %q", val, "1")
	}

	keys := httpList(t, ca, leaderPort, "test/")
	if len(keys) != 2 {
		t.Errorf("list test/: got %d keys, want 2", len(keys))
	}

	httpDelete(t, ca, leaderPort, "test/a")
	s := httpGetStatus(t, ca, leaderPort, "test/a")
	if s != http.StatusNotFound {
		t.Errorf("GET after DELETE: got %d, want 404", s)
	}
}

func TestKV_ThreeNode_ReadAfterWrite(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write then immediately read -- linearizable consistency guarantee.
	for i := range 10 {
		key := fmt.Sprintf("raw/%d", i)
		val := fmt.Sprintf("value-%d", i)
		httpPut(t, ca, leaderPort, key, []byte(val))
		got := httpGet(t, ca, leaderPort, key)
		if string(got) != val {
			t.Fatalf("read-after-write %s: got %q, want %q", key, got, val)
		}
	}
}

func TestKV_ThreeNode_ReplicatedList(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write a batch of keys on the leader.
	const count = 25
	for i := range count {
		key := fmt.Sprintf("rep/%04d", i)
		httpPut(t, ca, leaderPort, key, []byte(fmt.Sprintf("v%d", i)))
	}

	// Verify that all nodes converge on the same key set.
	for _, port := range adminPorts {
		// Poll until replication catches up.
		var keys []string
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			keys = httpList(t, ca, port, "rep/")
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
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	httpPut(t, ca, leaderPort, "del-test", []byte("exists"))

	// Confirm key is readable on all nodes first.
	for _, port := range adminPorts {
		waitForKV(t, ca, port, "del-test", []byte("exists"), 60*time.Second)
	}

	// Delete on leader.
	httpDelete(t, ca, leaderPort, "del-test")

	// Confirm deletion replicates to all nodes.
	for _, port := range adminPorts {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			s := httpGetStatus(t, ca, port, "del-test")
			if s == http.StatusNotFound {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		s := httpGetStatus(t, ca, port, "del-test")
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
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	stats := &testStats{}
	var wg sync.WaitGroup
	var errCount atomic.Int64

	numWorkers := 10
	keysPerWorker := 50

	for w := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := mtlsHTTPClient(t, ca, 10*time.Second)
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
	keys := httpList(t, ca, leaderPort, "worker")
	t.Logf("total keys after concurrent load: %d (expected %d)", len(keys), totalExpected)

	// Allow tolerance for transient failures under load in Docker environments.
	// The majority of keys must have been written successfully.
	minAcceptable := totalExpected * 80 / 100
	if len(keys) < minAcceptable {
		t.Fatalf("too few keys after load: got %d, minimum acceptable %d", len(keys), minAcceptable)
	}
}

func TestKV_SingleNode_ConcurrentSameKey(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Multiple goroutines writing to the same key concurrently.
	var wg sync.WaitGroup
	const writers = 20
	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			val := fmt.Sprintf("writer-%d", id)
			httpPut(t, ca, adminPort, "contested", []byte(val))
		}(w)
	}
	wg.Wait()

	// The key should exist with one of the written values.
	got := httpGet(t, ca, adminPort, "contested")
	if len(got) == 0 {
		t.Fatal("contested key has empty value after concurrent writes")
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestBatch_SingleNode_PutMultiple(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	result := httpBatch(t, ca, adminPort, []batchOp{
		{Type: "put", Key: "batch/k1", Value: "v1"},
		{Type: "put", Key: "batch/k2", Value: "v2"},
		{Type: "put", Key: "batch/k3", Value: "v3"},
	})

	if len(result.Responses) != 3 {
		t.Fatalf("batch responses: got %d, want 3", len(result.Responses))
	}

	// Verify each key individually via GET.
	for i, kv := range []struct{ key, val string }{
		{"batch/k1", "v1"},
		{"batch/k2", "v2"},
		{"batch/k3", "v3"},
	} {
		got := httpGet(t, ca, adminPort, kv.key)
		if string(got) != kv.val {
			t.Errorf("key %d (%s): got %q, want %q", i, kv.key, got, kv.val)
		}
	}
}

func TestBatch_SingleNode_MixedOps(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Seed a key first.
	httpPut(t, ca, adminPort, "batchmix/existing", []byte("original"))

	// Batch: put a new key, get the existing key, delete the existing key.
	result := httpBatch(t, ca, adminPort, []batchOp{
		{Type: "put", Key: "batchmix/new", Value: "new-value"},
		{Type: "get", Key: "batchmix/existing"},
		{Type: "delete", Key: "batchmix/existing"},
	})

	if len(result.Responses) != 3 {
		t.Fatalf("batch responses: got %d, want 3", len(result.Responses))
	}

	// The GET response should contain the existing key data.
	getResp := result.Responses[1]
	if len(getResp.Kvs) == 0 {
		t.Fatal("batch GET response returned no kvs")
	}
	if string(getResp.Kvs[0].Key) != "batchmix/existing" {
		t.Errorf("batch GET key: got %q, want %q", getResp.Kvs[0].Key, "batchmix/existing")
	}

	// Verify the new key exists.
	got := httpGet(t, ca, adminPort, "batchmix/new")
	if string(got) != "new-value" {
		t.Errorf("batchmix/new: got %q, want %q", got, "new-value")
	}

	// Verify the deleted key is gone.
	status := httpGetStatus(t, ca, adminPort, "batchmix/existing")
	if status != http.StatusNotFound {
		t.Errorf("batchmix/existing after delete: got %d, want 404", status)
	}
}

func TestBatch_SingleNode_EmptyBatch(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	result := httpBatch(t, ca, adminPort, []batchOp{})

	if len(result.Responses) != 0 {
		t.Fatalf("empty batch responses: got %d, want 0", len(result.Responses))
	}
}

func TestBatch_SingleNode_RangeOp(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Seed keys.
	httpPut(t, ca, adminPort, "batchrange/a", []byte("1"))
	httpPut(t, ca, adminPort, "batchrange/b", []byte("2"))
	httpPut(t, ca, adminPort, "batchrange/c", []byte("3"))

	// Batch with a range get from "batchrange/a" to "batchrange/d" (exclusive).
	result := httpBatch(t, ca, adminPort, []batchOp{
		{Type: "get", Key: "batchrange/a", RangeEnd: "batchrange/d"},
	})

	if len(result.Responses) != 1 {
		t.Fatalf("batch range responses: got %d, want 1", len(result.Responses))
	}

	rangeResp := result.Responses[0]
	if len(rangeResp.Kvs) != 3 {
		t.Fatalf("batch range kvs: got %d, want 3", len(rangeResp.Kvs))
	}
}

func TestBatch_ThreeNode_Replicated(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "mvcc")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Batch put on leader.
	httpBatch(t, ca, leaderPort, []batchOp{
		{Type: "put", Key: "batchrep/x", Value: "rx"},
		{Type: "put", Key: "batchrep/y", Value: "ry"},
	})

	// Verify keys from each node (including followers).
	for _, port := range adminPorts {
		waitForKV(t, ca, port, "batchrep/x", []byte("rx"), 60*time.Second)
		waitForKV(t, ca, port, "batchrep/y", []byte("ry"), 60*time.Second)
	}
}

// ---------------------------------------------------------------------------
// Transaction tests
// ---------------------------------------------------------------------------

func TestTx_SingleNode_CompareAndSwap(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Seed the key.
	httpPut(t, ca, adminPort, "tx/cas", []byte("initial"))

	// CAS: compare version > 0, put new value on success.
	result := httpTx(t, ca, adminPort, txRequest{
		Compares: []txCompare{
			{Key: "tx/cas", Target: "version", Result: "greater", Version: 0},
		},
		SuccessOps: []batchOp{
			{Type: "put", Key: "tx/cas", Value: "swapped"},
		},
		FailureOps: []batchOp{},
	})

	if !result.Succeeded {
		t.Fatal("CAS transaction should have succeeded")
	}

	// Verify value updated.
	got := httpGet(t, ca, adminPort, "tx/cas")
	if string(got) != "swapped" {
		t.Fatalf("tx/cas after CAS: got %q, want %q", got, "swapped")
	}
}

func TestTx_SingleNode_CompareAndSwap_Fails(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Seed the key.
	httpPut(t, ca, adminPort, "tx/casfail", []byte("original"))

	// CAS with wrong comparison: version > 999 (impossible).
	result := httpTx(t, ca, adminPort, txRequest{
		Compares: []txCompare{
			{Key: "tx/casfail", Target: "version", Result: "greater", Version: 999},
		},
		SuccessOps: []batchOp{
			{Type: "put", Key: "tx/casfail", Value: "should-not-happen"},
		},
		FailureOps: []batchOp{
			{Type: "put", Key: "tx/casfail", Value: "failure-path"},
		},
	})

	if result.Succeeded {
		t.Fatal("CAS transaction should have failed")
	}

	// Verify failure ops executed.
	got := httpGet(t, ca, adminPort, "tx/casfail")
	if string(got) != "failure-path" {
		t.Fatalf("tx/casfail after failed CAS: got %q, want %q", got, "failure-path")
	}
}

func TestTx_SingleNode_MultipleCompares(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Seed two keys.
	httpPut(t, ca, adminPort, "tx/mc1", []byte("a"))
	httpPut(t, ca, adminPort, "tx/mc2", []byte("b"))

	// Both keys exist with version > 0 -- should succeed.
	result := httpTx(t, ca, adminPort, txRequest{
		Compares: []txCompare{
			{Key: "tx/mc1", Target: "version", Result: "greater", Version: 0},
			{Key: "tx/mc2", Target: "version", Result: "greater", Version: 0},
		},
		SuccessOps: []batchOp{
			{Type: "put", Key: "tx/mc-result", Value: "both-matched"},
		},
		FailureOps: []batchOp{},
	})

	if !result.Succeeded {
		t.Fatal("multi-compare transaction should have succeeded")
	}

	got := httpGet(t, ca, adminPort, "tx/mc-result")
	if string(got) != "both-matched" {
		t.Fatalf("tx/mc-result: got %q, want %q", got, "both-matched")
	}
}

func TestTx_SingleNode_PutIfNotExists(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Key does not exist -- version is 0 for non-existent keys.
	// Compare version == 0 to implement create-if-absent.
	result := httpTx(t, ca, adminPort, txRequest{
		Compares: []txCompare{
			{Key: "tx/pine", Target: "version", Result: "equal", Version: 0},
		},
		SuccessOps: []batchOp{
			{Type: "put", Key: "tx/pine", Value: "created"},
		},
		FailureOps: []batchOp{},
	})

	if !result.Succeeded {
		t.Fatal("put-if-not-exists should have succeeded for missing key")
	}

	got := httpGet(t, ca, adminPort, "tx/pine")
	if string(got) != "created" {
		t.Fatalf("tx/pine: got %q, want %q", got, "created")
	}

	// Attempt the same transaction again -- key now exists, compare should fail.
	result2 := httpTx(t, ca, adminPort, txRequest{
		Compares: []txCompare{
			{Key: "tx/pine", Target: "version", Result: "equal", Version: 0},
		},
		SuccessOps: []batchOp{
			{Type: "put", Key: "tx/pine", Value: "should-not-overwrite"},
		},
		FailureOps: []batchOp{},
	})

	if result2.Succeeded {
		t.Fatal("put-if-not-exists should have failed for existing key")
	}

	// Value should remain unchanged.
	got2 := httpGet(t, ca, adminPort, "tx/pine")
	if string(got2) != "created" {
		t.Fatalf("tx/pine after re-attempt: got %q, want %q", got2, "created")
	}
}

// ---------------------------------------------------------------------------
// MVCC tests
// ---------------------------------------------------------------------------

func TestMVCC_SingleNode_Revision(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	rev1 := httpCurrentRevision(t, ca, adminPort)
	httpPut(t, ca, adminPort, "mvcc/r1", []byte("v1"))
	rev2 := httpCurrentRevision(t, ca, adminPort)

	if rev2 <= rev1 {
		t.Fatalf("revision did not increment: %d -> %d", rev1, rev2)
	}
}

func TestMVCC_SingleNode_Range(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	httpPut(t, ca, adminPort, "mvccrange/a", []byte("1"))
	httpPut(t, ca, adminPort, "mvccrange/b", []byte("2"))
	httpPut(t, ca, adminPort, "mvccrange/c", []byte("3"))

	result := httpRange(t, ca, adminPort, "mvccrange/a", "mvccrange/d")

	if len(result.Kvs) != 3 {
		t.Fatalf("range kvs: got %d, want 3", len(result.Kvs))
	}

	// Verify keys are in order.
	expectedKeys := []string{"mvccrange/a", "mvccrange/b", "mvccrange/c"}
	for i, kv := range result.Kvs {
		if string(kv.Key) != expectedKeys[i] {
			t.Errorf("range kv[%d]: got key %q, want %q", i, kv.Key, expectedKeys[i])
		}
	}
}

func TestMVCC_SingleNode_Compact(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	httpPut(t, ca, adminPort, "mvcccompact/a", []byte("v1"))
	httpPut(t, ca, adminPort, "mvcccompact/a", []byte("v2"))
	httpPut(t, ca, adminPort, "mvcccompact/a", []byte("v3"))

	revBeforeCompact := httpCurrentRevision(t, ca, adminPort)

	// Compact to the current revision.
	httpCompact(t, ca, adminPort, revBeforeCompact)

	// Current revision should not change after compaction.
	revAfterCompact := httpCurrentRevision(t, ca, adminPort)
	if revAfterCompact < revBeforeCompact {
		t.Fatalf("revision decreased after compact: %d -> %d", revBeforeCompact, revAfterCompact)
	}

	// The latest value should still be readable.
	got := httpGet(t, ca, adminPort, "mvcccompact/a")
	if string(got) != "v3" {
		t.Fatalf("mvcccompact/a after compact: got %q, want %q", got, "v3")
	}
}

func TestMVCC_ThreeNode_RevisionConsistency(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "mvcc")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	httpPut(t, ca, leaderPort, "mvcccluster/key", []byte("replicated"))

	leaderRev := httpCurrentRevision(t, ca, leaderPort)

	// All nodes should report the same revision (linearizable reads).
	for i, port := range adminPorts {
		deadline := time.Now().Add(15 * time.Second)
		var rev int64
		for time.Now().Before(deadline) {
			rev = httpCurrentRevision(t, ca, port)
			if rev >= leaderRev {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if rev < leaderRev {
			t.Errorf("node %d revision %d < leader revision %d", i+1, rev, leaderRev)
		}
	}
}

// ---------------------------------------------------------------------------
// Lease tests
// ---------------------------------------------------------------------------

func TestLease_SingleNode_GrantAndTTL(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	lease := httpLeaseGrant(t, ca, adminPort, 60)

	if lease.ID == 0 {
		t.Fatal("lease ID should not be 0")
	}
	if lease.TTL != 60 {
		t.Fatalf("TTL: got %d, want 60", lease.TTL)
	}
}

func TestLease_SingleNode_KeepAlive(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	lease := httpLeaseGrant(t, ca, adminPort, 30)

	refreshed := httpLeaseKeepAlive(t, ca, adminPort, lease.ID)

	if refreshed.TTL <= 0 {
		t.Fatalf("keepalive TTL should be positive: got %d", refreshed.TTL)
	}
}

func TestLease_SingleNode_Revoke(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	lease := httpLeaseGrant(t, ca, adminPort, 60)

	// Revoke the lease.
	httpLeaseRevoke(t, ca, adminPort, lease.ID)

	// Verify the lease is no longer listed.
	leases := httpLeaseList(t, ca, adminPort)
	for _, l := range leases {
		if l.ID == lease.ID {
			t.Fatalf("revoked lease %d still present in list", lease.ID)
		}
	}
}

func TestLease_SingleNode_List(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Grant 3 leases.
	ids := make(map[int64]bool)
	for range 3 {
		l := httpLeaseGrant(t, ca, adminPort, 120)
		ids[l.ID] = true
	}

	leases := httpLeaseList(t, ca, adminPort)

	found := 0
	for _, l := range leases {
		if ids[l.ID] {
			found++
		}
	}
	if found != 3 {
		t.Fatalf("lease list: found %d of 3 expected leases", found)
	}
}

func TestLease_SingleNode_Expiry(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Grant a lease with a short TTL.
	lease := httpLeaseGrant(t, ca, adminPort, 2)

	// Wait for expiry.
	time.Sleep(4 * time.Second)

	// Verify the lease is gone from the list.
	leases := httpLeaseList(t, ca, adminPort)
	for _, l := range leases {
		if l.ID == lease.ID {
			t.Fatalf("expired lease %d still present in list", lease.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Time Series tests
// ---------------------------------------------------------------------------

func TestTS_SingleNode_WriteAndQuery(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	now := time.Now()
	startNs := now.Add(-1 * time.Minute).UnixNano()
	endNs := now.Add(2 * time.Minute).UnixNano()

	// Write 3 points.
	httpTSWritePoint(t, ca, adminPort, "ts.cpu.usage", 45.5, map[string]string{"host": "node1"})
	httpTSWritePoint(t, ca, adminPort, "ts.cpu.usage", 62.3, map[string]string{"host": "node1"})
	httpTSWritePoint(t, ca, adminPort, "ts.cpu.usage", 38.7, map[string]string{"host": "node2"})

	points := httpTSQuery(t, ca, adminPort, "ts.cpu.usage", startNs, endNs)

	if len(points) != 3 {
		t.Fatalf("TS query: got %d points, want 3", len(points))
	}
}

func TestTS_SingleNode_Aggregate(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	now := time.Now()
	startNs := now.Add(-1 * time.Minute).UnixNano()
	endNs := now.Add(2 * time.Minute).UnixNano()
	intervalNs := int64(5 * time.Minute) // large interval to get a single bucket

	// Write points for aggregation.
	httpTSWritePoint(t, ca, adminPort, "ts.agg.temp", 10.0, nil)
	httpTSWritePoint(t, ca, adminPort, "ts.agg.temp", 20.0, nil)
	httpTSWritePoint(t, ca, adminPort, "ts.agg.temp", 30.0, nil)

	results := httpTSAggregate(t, ca, adminPort, "ts.agg.temp", startNs, endNs, "avg", intervalNs)

	if len(results) == 0 {
		t.Fatal("TS aggregate: got 0 results")
	}

	// The average of 10, 20, 30 should be 20.
	avg := results[0].Value
	if avg < 19.9 || avg > 20.1 {
		t.Fatalf("TS aggregate avg: got %f, want ~20.0", avg)
	}
}

func TestTS_SingleNode_ListMetrics(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	httpTSWritePoint(t, ca, adminPort, "ts.list.cpu", 10.0, nil)
	httpTSWritePoint(t, ca, adminPort, "ts.list.mem", 20.0, nil)
	httpTSWritePoint(t, ca, adminPort, "ts.list.disk", 30.0, nil)

	metrics := httpTSListMetrics(t, ca, adminPort)

	found := make(map[string]bool)
	for _, m := range metrics {
		found[m] = true
	}

	for _, expected := range []string{"ts.list.cpu", "ts.list.mem", "ts.list.disk"} {
		if !found[expected] {
			t.Errorf("metric %q not found in list: %v", expected, metrics)
		}
	}
}

func TestTS_SingleNode_DeleteSeries(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	now := time.Now()
	startNs := now.Add(-1 * time.Minute).UnixNano()
	endNs := now.Add(2 * time.Minute).UnixNano()

	httpTSWritePoint(t, ca, adminPort, "ts.del.series", 100.0, nil)

	// Verify data exists.
	points := httpTSQuery(t, ca, adminPort, "ts.del.series", startNs, endNs)
	if len(points) == 0 {
		t.Fatal("TS delete: no points found before delete")
	}

	// Delete the series.
	httpTSDeleteSeries(t, ca, adminPort, "ts.del.series")

	// Query should return empty.
	points = httpTSQuery(t, ca, adminPort, "ts.del.series", startNs, endNs)
	if len(points) != 0 {
		t.Fatalf("TS delete: got %d points after delete, want 0", len(points))
	}
}

func TestTS_SingleNode_WriteBatch(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	now := time.Now()
	startNs := now.Add(-1 * time.Minute).UnixNano()
	endNs := now.Add(2 * time.Minute).UnixNano()

	// Write multiple points in quick succession.
	for i := range 5 {
		httpTSWritePoint(t, ca, adminPort, "ts.writebatch.metric",
			float64(i)*10.0, map[string]string{"batch": "true"})
	}

	points := httpTSQuery(t, ca, adminPort, "ts.writebatch.metric", startNs, endNs)

	if len(points) != 5 {
		t.Fatalf("TS write batch: got %d points, want 5", len(points))
	}
}

// ---------------------------------------------------------------------------
// Index tests
// ---------------------------------------------------------------------------

func TestIndex_SingleNode_CreateAndQuery(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Create an index on field "status".
	httpIndexCreate(t, ca, adminPort, "idx-status", "status")

	// Put some JSON values with a "status" field via the KV store.
	httpPut(t, ca, adminPort, "idx/doc1", []byte(`{"status":"active","name":"doc1"}`))
	httpPut(t, ca, adminPort, "idx/doc2", []byte(`{"status":"active","name":"doc2"}`))
	httpPut(t, ca, adminPort, "idx/doc3", []byte(`{"status":"inactive","name":"doc3"}`))

	// Query the index for "active" status.
	keys := httpIndexQuery(t, ca, adminPort, "idx-status", "active")

	if len(keys) < 1 {
		t.Fatalf("index query for 'active': got %d results, want at least 1", len(keys))
	}
}

func TestIndex_SingleNode_Drop(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	httpIndexCreate(t, ca, adminPort, "idx-drop-test", "category")

	// Drop the index.
	httpIndexDrop(t, ca, adminPort, "idx-drop-test")

	// Querying a dropped index should fail gracefully. We use a raw HTTP
	// request to check the status code without fataling.
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	url := fmt.Sprintf("https://127.0.0.1:%d/index/query?index_name=idx-drop-test&value=anything", adminPort)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("index query after drop: %v", err)
	}
	defer resp.Body.Close()

	// Expect a non-200 status (typically 404 or 500 for a missing index).
	if resp.StatusCode == http.StatusOK {
		t.Error("expected error status for querying dropped index, got 200")
	}
}

func TestIndex_SingleNode_Scan(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	httpIndexCreate(t, ca, adminPort, "idx-scan-test", "priority")

	httpPut(t, ca, adminPort, "idxscan/p1", []byte(`{"priority":"a","item":"one"}`))
	httpPut(t, ca, adminPort, "idxscan/p2", []byte(`{"priority":"b","item":"two"}`))
	httpPut(t, ca, adminPort, "idxscan/p3", []byte(`{"priority":"c","item":"three"}`))

	// Scan the index range [a, d).
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	url := fmt.Sprintf("https://127.0.0.1:%d/index/scan?index_name=idx-scan-test&start=a&end=d", adminPort)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("index scan: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("index scan: status %d, body: %s", resp.StatusCode, body)
	}

	var result struct {
		Entries map[string][]string `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("index scan decode: %v", err)
	}

	if len(result.Entries) == 0 {
		t.Fatal("index scan returned 0 entries")
	}
}

// ---------------------------------------------------------------------------
// Auth/Users/Roles tests
// ---------------------------------------------------------------------------

func TestUsers_SingleNode_CreateAndList(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	client := mtlsHTTPClient(t, ca, 10*time.Second)

	// Create a user. The REST wire contract is snake_case; user IDs are
	// uint64 Snowflakes encoded as JSON strings (proto3 convention) to
	// avoid precision loss in JS clients.
	payload := struct {
		Username string   `json:"username"`
		Email    string   `json:"email"`
		TenantID string   `json:"tenant_id"`
		RoleIDs  []string `json:"role_ids"`
	}{
		Username: "testuser1",
		Email:    "testuser1@system.example.com",
		TenantID: "system",
		RoleIDs:  []string{},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("create user marshal: %v", err)
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/users", adminPort)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create user: status %d, body: %s", resp.StatusCode, respBody)
	}

	var createdUser struct {
		ID       uint64 `json:"id,string"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createdUser); err != nil {
		t.Fatalf("create user decode: %v", err)
	}
	if createdUser.Username != "testuser1" {
		t.Errorf("created user name: got %q, want %q", createdUser.Username, "testuser1")
	}

	// List users and verify the created user is present. The list must be
	// scoped to the SAME tenant the user was created in ("system" above) —
	// handleListUsers filters strictly by the tenantID query param. Use a
	// retry loop because the linearizable read may take a brief moment to
	// reflect the freshly committed write on a newly started node.
	listURL := fmt.Sprintf("https://127.0.0.1:%d/users/list?tenantID=system", adminPort)
	deadline := time.Now().Add(10 * time.Second)
	var lastTotal int
	for time.Now().Before(deadline) {
		listResp, err := client.Get(listURL)
		if err != nil {
			t.Fatalf("list users: %v", err)
		}

		if listResp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(listResp.Body)
			listResp.Body.Close()
			t.Fatalf("list users: status %d, body: %s", listResp.StatusCode, respBody)
		}

		var listResult struct {
			Users []struct {
				ID       uint64 `json:"id,string"`
				Username string `json:"username"`
			} `json:"users"`
			Total int `json:"total"`
		}
		if err := json.NewDecoder(listResp.Body).Decode(&listResult); err != nil {
			listResp.Body.Close()
			t.Fatalf("list users decode: %v", err)
		}
		listResp.Body.Close()

		lastTotal = listResult.Total
		for _, u := range listResult.Users {
			if u.Username == "testuser1" {
				return // success
			}
		}

		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("user 'testuser1' not found in list after 10s (last total: %d)", lastTotal)
}

func TestRoles_SingleNode_CreateAndAssign(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	client := mtlsHTTPClient(t, ca, 10*time.Second)

	// Create a role.
	rolePayload := struct {
		Name        string   `json:"name"`
		TenantID    string   `json:"tenant_id"`
		Permissions []string `json:"permissions"`
	}{
		Name:        "testrole",
		TenantID:    "system",
		Permissions: []string{"kv:get", "kv:put"},
	}
	roleBody, err := json.Marshal(rolePayload)
	if err != nil {
		t.Fatalf("create role marshal: %v", err)
	}

	roleURL := fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort)
	roleResp, err := client.Post(roleURL, "application/json", bytes.NewReader(roleBody))
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	defer roleResp.Body.Close()

	if roleResp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(roleResp.Body)
		t.Fatalf("create role: status %d, body: %s", roleResp.StatusCode, respBody)
	}

	var createdRole struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(roleResp.Body).Decode(&createdRole); err != nil {
		t.Fatalf("create role decode: %v", err)
	}

	// Create a user to assign the role to.
	userPayload := struct {
		Username string   `json:"username"`
		Email    string   `json:"email"`
		TenantID string   `json:"tenant_id"`
		RoleIDs  []string `json:"role_ids"`
	}{
		Username: "roleuser",
		Email:    "roleuser@system.example.com",
		TenantID: "system",
		RoleIDs:  []string{},
	}
	userBody, err := json.Marshal(userPayload)
	if err != nil {
		t.Fatalf("create user marshal: %v", err)
	}

	userURL := fmt.Sprintf("https://127.0.0.1:%d/users", adminPort)
	userResp, err := client.Post(userURL, "application/json", bytes.NewReader(userBody))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer userResp.Body.Close()

	if userResp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(userResp.Body)
		t.Fatalf("create user for role assign: status %d, body: %s", userResp.StatusCode, respBody)
	}

	var createdUser struct {
		ID uint64 `json:"id,string"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&createdUser); err != nil {
		t.Fatalf("create user decode: %v", err)
	}

	// Assign the role to the user.
	assignPayload := struct {
		UserID uint64 `json:"user_id,string"`
		RoleID string `json:"role_id"`
	}{
		UserID: createdUser.ID,
		RoleID: createdRole.ID,
	}
	assignBody, err := json.Marshal(assignPayload)
	if err != nil {
		t.Fatalf("assign role marshal: %v", err)
	}

	assignURL := fmt.Sprintf("https://127.0.0.1:%d/roles/assign", adminPort)
	assignResp, err := client.Post(assignURL, "application/json", bytes.NewReader(assignBody))
	if err != nil {
		t.Fatalf("assign role: %v", err)
	}
	defer assignResp.Body.Close()

	if assignResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(assignResp.Body)
		t.Fatalf("assign role: status %d, body: %s", assignResp.StatusCode, respBody)
	}

	// Verify the user now has the role.
	var assignedUser struct {
		RoleIDs []string `json:"role_ids"`
	}
	if err := json.NewDecoder(assignResp.Body).Decode(&assignedUser); err != nil {
		t.Fatalf("assign role decode: %v", err)
	}

	found := false
	for _, rid := range assignedUser.RoleIDs {
		if rid == createdRole.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("role %q not found in user's roleIDs: %v", createdRole.ID, assignedUser.RoleIDs)
	}
}

func TestRoles_SingleNode_ListPermissions(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	client := mtlsHTTPClient(t, ca, 10*time.Second)

	url := fmt.Sprintf("https://127.0.0.1:%d/permissions", adminPort)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list permissions: status %d, body: %s", resp.StatusCode, body)
	}

	var wrapper struct {
		Categories []struct {
			Name        string   `json:"name"`
			Group       string   `json:"group"`
			Permissions []string `json:"permissions"`
		} `json:"categories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		t.Fatalf("list permissions decode: %v", err)
	}

	if len(wrapper.Categories) == 0 {
		t.Fatal("list permissions returned 0 categories")
	}

	// Verify at least one category has permissions.
	hasPerms := false
	for _, cat := range wrapper.Categories {
		if len(cat.Permissions) > 0 {
			hasPerms = true
			break
		}
	}
	if !hasPerms {
		t.Error("no categories contain any permissions")
	}
}
