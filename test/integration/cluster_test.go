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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCluster_HealthEndpoint(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "memory")

	for i, port := range adminPorts {
		code := httpHealth(t, ca.Pool, port)
		if code != http.StatusOK {
			t.Errorf("node %d: health=%d, want 200", i+1, code)
		}
	}
}

func TestCluster_StatusEndpoint(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "memory")

	for i, port := range adminPorts {
		status := httpStatus(t, ca, port)
		if status["node_id"] == nil {
			t.Errorf("node %d: status missing node_id", i+1)
		}
		if status["raft_address"] == nil {
			t.Errorf("node %d: status missing raft_address", i+1)
		}
		shards, ok := status["shards"].([]interface{})
		if !ok || len(shards) == 0 {
			t.Errorf("node %d: status missing or empty shards", i+1)
		}
	}
}

func TestCluster_LeaderElection(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "memory")

	// Verify exactly one leader is present.
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	leaderCount := 0
	for _, port := range adminPorts {
		status, _ := httpStatusRaw(t, client, port)
		if status == nil {
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
				leaderCount++
			}
		}
	}
	if leaderCount != 1 {
		t.Fatalf("expected exactly 1 leader, got %d", leaderCount)
	}
}

func TestCluster_SnapshotTriggered(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "memory",
		"--snapshot-entries", "100",
		"--compaction-overhead", "10")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write enough records to trigger at least 1 snapshot.
	for i := range 150 {
		key := fmt.Sprintf("snap/%04d", i)
		val := fmt.Sprintf("value-%d", i)
		httpPut(t, ca, leaderPort, key, []byte(val))
	}

	// Verify all data accessible after snapshot.
	keys := httpList(t, ca, leaderPort, "snap/")
	if len(keys) < 150 {
		t.Fatalf("expected at least 150 keys, got %d", len(keys))
	}

	// Verify specific values at boundaries.
	first := httpGet(t, ca, leaderPort, "snap/0000")
	if !bytes.Equal(first, []byte("value-0")) {
		t.Errorf("snap/0000: got %q, want %q", first, "value-0")
	}
	last := httpGet(t, ca, leaderPort, "snap/0149")
	if !bytes.Equal(last, []byte("value-149")) {
		t.Errorf("snap/0149: got %q, want %q", last, "value-149")
	}

	// Check status shows progress.
	status := httpStatus(t, ca, leaderPort)
	t.Logf("status after 150 writes with snapshots: %v", status)
}

func TestCluster_SnapshotDataIntegrity(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "memory",
		"--snapshot-entries", "50",
		"--compaction-overhead", "5")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write 200 keys in multiple batches to trigger multiple snapshots.
	for batch := range 4 {
		for i := range 50 {
			key := fmt.Sprintf("batch%d/%04d", batch, i)
			httpPut(t, ca, leaderPort, key, []byte(fmt.Sprintf("b%d-v%d", batch, i)))
		}
	}

	// Verify all 200 keys are present across all nodes.
	for _, port := range adminPorts {
		var totalKeys int
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			allKeys := httpListTolerant(t, ca, port, "batch")
			totalKeys = len(allKeys)
			if totalKeys >= 200 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if totalKeys < 200 {
			t.Fatalf("port %d: expected 200 keys, got %d", port, totalKeys)
		}
	}
}

func TestCluster_FullRestart_DataSurvives(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)
	cmds := make([]*exec.Cmd, 3)

	// Generate node certs (reused across restarts).
	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCert(t)
	}

	// Phase 1: Start 3 nodes with pebble SM (durable).
	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}

		cmd := exec.Command(bin, args...)
		cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R1] ", i+1)}
		cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R1] ", i+1)}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start node %d: %v", i+1, err)
		}
		cmds[i] = cmd
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Phase 2: Write 100 KV pairs.
	for i := range 100 {
		key := fmt.Sprintf("persist/%04d", i)
		httpPut(t, ca, leaderPort, key, []byte(fmt.Sprintf("val-%d", i)))
	}

	// Verify all keys are readable on all nodes.
	for _, port := range adminPorts {
		var keys []string
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			keys = httpListTolerant(t, ca, port, "persist/")
			if len(keys) == 100 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(keys) != 100 {
			t.Fatalf("port %d: expected 100 keys, got %d", port, len(keys))
		}
	}

	// Phase 3: SIGTERM all 3 nodes for graceful shutdown.
	t.Log("stopping all nodes...")
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("signal node %d: %v", i+1, err)
		}
	}
	for i, cmd := range cmds {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			t.Logf("node %d stopped", i+1)
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("node %d: timed out waiting for shutdown", i+1)
		}
	}

	// Phase 4: Restart all nodes with same data directories and ports.
	t.Log("restarting all nodes...")
	cmds2 := make([]*exec.Cmd, 3)
	for i := range 3 {
		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}

		cmd := exec.Command(bin, args...)
		cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R2] ", i+1)}
		cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R2] ", i+1)}
		if err := cmd.Start(); err != nil {
			t.Fatalf("restart node %d: %v", i+1, err)
		}
		cmds2[i] = cmd

		// Register cleanup for the restarted process.
		t.Cleanup(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
	}

	// Phase 5: Wait for all nodes to become healthy.
	for i := range 3 {
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort2 := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Phase 6: Verify all 100 keys are readable after restart using stale
	// reads. After a full cluster restart, the QUIC transport may take time
	// to stabilize before Raft consensus is operational. Stale reads go
	// directly to the local state machine (Pebble), verifying that data
	// survived the restart without depending on Raft coordination.
	for _, port := range adminPorts {
		var keys []string
		var lastDiag string
		deadline := time.Now().Add(30 * time.Second)
		client := mtlsHTTPClient(t, ca, 5*time.Second)
		for time.Now().Before(deadline) {
			url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=persist/&stale=true", port)
			resp, err := client.Get(url)
			if err != nil {
				lastDiag = fmt.Sprintf("conn error: %v", err)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				lastDiag = fmt.Sprintf("status %d, body: %s", resp.StatusCode, body)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			var result struct {
				Keys []string `json:"keys"`
			}
			if jsonErr := json.Unmarshal(body, &result); jsonErr == nil {
				keys = result.Keys
			}
			if len(keys) >= 100 {
				break
			}
			lastDiag = fmt.Sprintf("got %d keys", len(keys))
			time.Sleep(200 * time.Millisecond)
		}
		if len(keys) < 100 {
			t.Fatalf("port %d after restart: expected 100 keys, got %d; last diagnostic: %s", port, len(keys), lastDiag)
		}
	}

	// Verify a specific value for data integrity using stale read.
	val := httpGetStale(t, ca, leaderPort2, "persist/0042")
	if !bytes.Equal(val, []byte("val-42")) {
		t.Errorf("persist/0042: got %q, want %q", val, "val-42")
	}

	// Check status is populated.
	status := httpStatus(t, ca, adminPorts[0])
	t.Logf("status after restart: %v", status)

	// Phase 7: Health should be OK on all nodes.
	for _, port := range adminPorts {
		code := httpHealth(t, ca.Pool, port)
		if code != http.StatusOK {
			t.Errorf("port %d: health=%d, want 200", port, code)
		}
	}

	t.Log("full restart with data persistence: PASS")
}

func TestCluster_WriteAfterRestart(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)
	cmds := make([]*exec.Cmd, 3)

	// Generate node certs (reused across restarts).
	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCert(t)
	}

	// Start a 3-node pebble cluster.
	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}

		cmd := exec.Command(bin, args...)
		cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R1] ", i+1)}
		cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R1] ", i+1)}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start node %d: %v", i+1, err)
		}
		cmds[i] = cmd
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Write initial data.
	httpPut(t, ca, leaderPort, "before-restart", []byte("original"))

	// Stop all nodes.
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("signal node %d: %v", i+1, err)
		}
	}
	for i, cmd := range cmds {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("node %d: timed out waiting for shutdown", i+1)
		}
	}

	// Restart.
	for i := range 3 {
		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		cmd := exec.Command(bin, args...)
		cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R2] ", i+1)}
		cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R2] ", i+1)}
		if err := cmd.Start(); err != nil {
			t.Fatalf("restart node %d: %v", i+1, err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
	}

	for i := range 3 {
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort = waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Warmup write to ensure the leader's no-op has been committed and the
	// ReadIndex pipeline is operational.
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	// Verify pre-restart data is intact.
	val := httpGet(t, ca, leaderPort, "before-restart")
	if !bytes.Equal(val, []byte("original")) {
		t.Fatalf("before-restart: got %q, want %q", val, "original")
	}

	// Write new data after restart to prove the cluster is fully operational.
	httpPut(t, ca, leaderPort, "after-restart", []byte("new-data"))
	got := httpGet(t, ca, leaderPort, "after-restart")
	if !bytes.Equal(got, []byte("new-data")) {
		t.Fatalf("after-restart: got %q, want %q", got, "new-data")
	}
}

func TestCluster_PebbleSingleNode(t *testing.T) {
	adminPort, ca := startSingleNode(t, "pebble")

	// Basic CRUD with pebble SM.
	httpPut(t, ca, adminPort, "pebble/key1", []byte("value1"))
	val := httpGet(t, ca, adminPort, "pebble/key1")
	if !bytes.Equal(val, []byte("value1")) {
		t.Fatalf("pebble GET: got %q, want %q", val, "value1")
	}

	httpDelete(t, ca, adminPort, "pebble/key1")
	status := httpGetStatus(t, ca, adminPort, "pebble/key1")
	if status != http.StatusNotFound {
		t.Fatalf("pebble GET after DELETE: got %d, want 404", status)
	}
}

func TestCluster_ConcurrentSingleNode(t *testing.T) {
	adminPort, ca := startSingleNode(t, "concurrent")

	// Basic CRUD with concurrent SM.
	httpPut(t, ca, adminPort, "conc/key1", []byte("value1"))
	val := httpGet(t, ca, adminPort, "conc/key1")
	if !bytes.Equal(val, []byte("value1")) {
		t.Fatalf("concurrent GET: got %q, want %q", val, "value1")
	}

	httpPut(t, ca, adminPort, "conc/key2", []byte("value2"))
	keys := httpList(t, ca, adminPort, "conc/")
	if len(keys) != 2 {
		t.Fatalf("concurrent list: got %d keys, want 2: %v", len(keys), keys)
	}
}
