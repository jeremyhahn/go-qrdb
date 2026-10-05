// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// sm_storage_file_test.go is the run-modes phase-5 acceptance: a raft
// single-node daemon whose state machine runs over the FILE storage
// backend (raft.sm_storage: file with the storage-generic "disk" SM)
// serves standard operations and SURVIVES A RESTART — the durability
// contract the file backend's atomic writes and commit journal exist for.
package integration_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startSMFileNode boots (or reboots, over the same dataDir and ports) a
// single-node raft daemon with --sm disk --sm-storage file, REST only,
// auth disabled — the minimal fixture for the storage-seam acceptance.
func startSMFileNode(t *testing.T, bin string, dataDir string, raftPort, restPort, grpcPort int, ca *testCA) *exec.Cmd {
	t.Helper()
	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled=false",
		"--enable-rest",
		"--sm", "disk",
		"--sm-storage", "file",
	}
	args = append(args, ca.tlsFlags(t)...)
	cmd := startNode(t, bin, args, "[sm-storage=file] ")
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	return cmd
}

// TestRaftSMOverFileStorage_ServesAndSurvivesRestart writes through a
// disk-SM-over-file-storage raft node, kills it, reboots it on the same
// data directory, and requires the data back — proving the sm_storage
// seam end to end (registry factory path, DiskAdapter applied-index
// persistence, and the file backend's durable writes).
func TestRaftSMOverFileStorage_ServesAndSurvivesRestart(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()

	raftPort := allocatePorts(t, 1)[0]
	tcpPorts := allocateTCPPorts(t, 2)
	restPort, grpcPort := tcpPorts[0], tcpPorts[1]

	cliArgs := func(rest int) []string {
		return []string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", rest),
			"--ca-file", ca.CertPath,
			"--timeout", "20s",
		}
	}

	// First life: write.
	cmd := startSMFileNode(t, bin, dataDir, raftPort, restPort, grpcPort, ca)

	putArgs := append(cliArgs(restPort), "kv", "put", "sm-file-survivor", "v-durable")
	deadline := time.Now().Add(60 * time.Second)
	var out string
	var err error
	for time.Now().Before(deadline) {
		out, err = runCLI(t, cli, putArgs...)
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("kv put never succeeded: %v\n%s", err, out)
	}

	// Kill hard (no graceful shutdown): the durability contract must not
	// depend on a clean exit.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first daemon: %v", err)
	}
	_ = cmd.Wait()

	// Second life: same data dir, same ports.
	startSMFileNode(t, bin, dataDir, raftPort, restPort, grpcPort, ca)

	getArgs := append(cliArgs(restPort), "kv", "get", "sm-file-survivor")
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err = runCLI(t, cli, getArgs...)
		if err == nil && strings.Contains(out, "v-durable") {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("value did not survive the restart: err=%v out=%s", err, out)
}

// TestRaftSMOverFileStorage_PrefixKeyPairSurvivesEitherOrder is the
// end-to-end acceptance for go-qrdb#592: a disk-SM-over-file-storage raft
// node used to kill the shard on an ordinary pair of client writes -- a key
// and a key for which it is a '/' prefix, in either order -- because the
// file backend's key-to-path mapping collided the second write with the
// first's own directory. `SM.Apply` returning that storage error is fatal
// to go-quicraft: `lastApplied` stops advancing on the replica and every
// later linearizable read stalls, which is why this runs through the real
// daemon and CLI rather than only at the state-machine level -- it is the
// shard staying responsive afterward that the unit-level tests cannot
// observe. Both orders must leave the shard able to serve a `kv get` for
// EVERY key this test writes, including ones proposed after the pair.
func TestRaftSMOverFileStorage_PrefixKeyPairSurvivesEitherOrder(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()

	raftPort := allocatePorts(t, 1)[0]
	tcpPorts := allocateTCPPorts(t, 2)
	restPort, grpcPort := tcpPorts[0], tcpPorts[1]

	startSMFileNode(t, bin, dataDir, raftPort, restPort, grpcPort, ca)

	cliArgs := func(args ...string) []string {
		base := []string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", restPort),
			"--ca-file", ca.CertPath,
			"--timeout", "20s",
		}
		return append(base, args...)
	}

	put := func(key, value string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		var out string
		var err error
		for time.Now().Before(deadline) {
			out, err = runCLI(t, cli, cliArgs("kv", "put", key, value)...)
			if err == nil {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("kv put %s=%s never succeeded (this is the circuit-breaker trip "+
			"go-qrdb#592 describes if it stalls rather than erroring quickly): %v\n%s", key, value, err, out)
	}
	get := func(key, want string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		var out string
		var err error
		for time.Now().Before(deadline) {
			out, err = runCLI(t, cli, cliArgs("kv", "get", key)...)
			if err == nil && strings.Contains(out, want) {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("kv get %s never returned %q (shard may be stalled): err=%v out=%s", key, want, err, out)
	}

	// Child, then its own '/' prefix -- the exact order the report named.
	put("prefix-a/child", "child-value")
	put("prefix-a", "parent-value")
	get("prefix-a/child", "child-value")
	get("prefix-a", "parent-value")

	// The reverse order, on a second key pair so the two cases cannot mask
	// each other.
	put("prefix-b", "parent-value-2")
	put("prefix-b/child", "child-value-2")
	get("prefix-b", "parent-value-2")
	get("prefix-b/child", "child-value-2")

	// The shard must still apply and serve an entry proposed after both
	// pairs -- the property an actual breaker trip would have cost every
	// later proposal on the shard.
	put("after-the-pairs", "still-alive")
	get("after-the-pairs", "still-alive")
}

// TestRaftSMOverFileStorage_DeepKeyNeverHaltsTheShard is the end-to-end
// acceptance for review probe finding B1 against go-qrdb#592's FIRST fix
// attempt: a key built from thousands of short '/'-delimited segments
// produced an absolute path longer than PATH_MAX, so os.MkdirAll returned
// ENAMETOOLONG from inside SM.Apply — a genuine storage fault from the
// state machine's point of view, which halts the shard on every replica
// exactly as the original prefix-collision bug did. The hashed,
// content-addressed layout that replaced the per-segment one has no
// path-length dependency on the key at all, so a key this deep must apply
// cleanly, and the shard must still serve ordinary requests afterward.
func TestRaftSMOverFileStorage_DeepKeyNeverHaltsTheShard(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()

	raftPort := allocatePorts(t, 1)[0]
	tcpPorts := allocateTCPPorts(t, 2)
	restPort, grpcPort := tcpPorts[0], tcpPorts[1]

	startSMFileNode(t, bin, dataDir, raftPort, restPort, grpcPort, ca)

	cliArgs := func(args ...string) []string {
		base := []string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", restPort),
			"--ca-file", ca.CertPath,
			"--timeout", "20s",
		}
		return append(base, args...)
	}

	put := func(key, value string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		var out string
		var err error
		for time.Now().Before(deadline) {
			out, err = runCLI(t, cli, cliArgs("kv", "put", key, value)...)
			if err == nil {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("kv put (key len %d) never succeeded (this is the circuit-breaker trip "+
			"a storage-layer fault would cause if the shard halted): %v\n%s", len(key), err, out)
	}
	get := func(key, want string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		var out string
		var err error
		for time.Now().Before(deadline) {
			out, err = runCLI(t, cli, cliArgs("kv", "get", key)...)
			if err == nil && strings.Contains(out, want) {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("kv get (key len %d) never returned %q (shard may be stalled): err=%v out=%s", len(key), want, err, out)
	}

	deepKey := strings.Repeat("a/", 2100) + "a"
	put(deepKey, "deep-value")
	get(deepKey, "deep-value")

	// The shard must still apply and serve an ordinary request afterward --
	// the property an actual ENAMETOOLONG-triggered halt would have cost
	// every later proposal on the shard.
	put("after-the-deep-key", "still-alive")
	get("after-the-deep-key", "still-alive")
}

// TestRaftSMOverFileStorage_ThreeNodeCluster is the clustered half of the
// phase-5 acceptance: a 3-node raft group whose replicas all apply
// through file-backed SM storage reaches quorum, accepts a write on one
// node, and serves it from the others.
func TestRaftSMOverFileStorage_ThreeNodeCluster(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, len(raftPorts))

	for i := range 3 {
		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", t.TempDir(),
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--auth-enabled=false",
			"--enable-rest",
			"--sm", "disk",
			"--sm-storage", "file",
		}
		args = append(args, nodeTLS[i]...)
		startNode(t, bin, args, fmt.Sprintf("[sm-file node%d] ", i+1))
	}
	for i := range 3 {
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	cliArgs := func(rest int, rest2 ...string) []string {
		base := []string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", rest),
			"--ca-file", ca.CertPath,
			"--timeout", "20s",
		}
		return append(base, rest2...)
	}

	// Write through node 1 (leader forwarding routes to the leader).
	deadline := time.Now().Add(90 * time.Second)
	var out string
	var err error
	for time.Now().Before(deadline) {
		out, err = runCLI(t, cli, cliArgs(adminPorts[0], "kv", "put", "sm-file-cluster", "replicated")...)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("kv put through node1 never succeeded: %v\n%s", err, out)
	}

	// Read through every node.
	for i := range 3 {
		nodeDeadline := time.Now().Add(60 * time.Second)
		for {
			out, err = runCLI(t, cli, cliArgs(adminPorts[i], "kv", "get", "sm-file-cluster")...)
			if err == nil && strings.Contains(out, "replicated") {
				break
			}
			if time.Now().After(nodeDeadline) {
				t.Fatalf("node %d never served the replicated value: err=%v out=%s", i+1, err, out)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}
