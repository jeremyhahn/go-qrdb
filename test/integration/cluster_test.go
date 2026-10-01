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
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCluster_HealthEndpoint(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")

	for i, port := range adminPorts {
		code := httpHealth(t, ca.Pool, port)
		if code != http.StatusOK {
			t.Errorf("node %d: health=%d, want 200", i+1, code)
		}
	}
}

func TestCluster_StatusEndpoint(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")

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
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble")

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

// clusterTestTrustDomain is the SPIFFE trust domain for
// TestCluster_MembershipAdd_ForwardsToLeader's node certificates. It matches
// the qrdbd default (--cluster-trust-domain "qrdb.cluster"), so the flag
// does not need to be passed explicitly.
const clusterTestTrustDomain = "qrdb.cluster"

// writePeerAdminURLConfig writes a minimal qrdbd YAML config containing only
// the auth.peer_admin_urls block, mapping each node's Raft address to its
// admin (REST) URL. The peerAdminRegistry's fallback path assumes every peer
// shares the SAME admin port (true for real multi-host deployments, one
// admin port per host), which does not hold in this single-host,
// distinct-admin-port-per-node test harness -- so tests that exercise
// cross-node leader resolution must supply explicit registrations via this
// config, mirroring writeIsolationRaftConfig's peer_admin_urls block.
func writePeerAdminURLConfig(t *testing.T, peerAdminURLs map[string]string) string {
	t.Helper()

	lines := "  peer_admin_urls:\n"
	for raftAddr, adminURL := range peerAdminURLs {
		lines += fmt.Sprintf("    - raft_addr: %q\n      admin_url: %q\n", raftAddr, adminURL)
	}
	yaml := "auth:\n" + lines

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("writePeerAdminURLConfig: write: %v", err)
	}
	return cfgPath
}

// startThreeNodeClusterSPIFFE is a variant of startThreeNodeCluster (see
// kv_test.go) that issues each node a SPIFFE-SAN certificate instead of a
// plain node certificate, and explicitly registers every peer's admin URL
// (see writePeerAdminURLConfig) instead of relying on the same-admin-port
// fallback. The SPIFFE URI SAN is required for the on-behalf leader-
// forwarding path (pkg/api/rest deriveForwardIdentity / pkg/auth/middleware
// authenticateOnBehalf) to re-authorize a forwarded request as the ORIGINAL
// caller rather than falling back to the forwarding node's own
// (unprivileged) identity.
func startThreeNodeClusterSPIFFE(t *testing.T, smType string) ([]int, *testCA) {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)

	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", ports[i])
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}
	cfgPath := writePeerAdminURLConfig(t, peerAdminURLs)

	// Every certificate is issued before any node starts: each node needs
	// every peer's member identity (#556).
	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", ports[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, clusterTestTrustDomain)
	}
	memberIDs := memberIdentitiesFlag(t, nodeCertPaths)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		raftAddr := fmt.Sprintf("127.0.0.1:%d", ports[i])
		nodeCertPath, nodeKeyPath := nodeCertPaths[i], nodeKeyPaths[i]

		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", raftAddr,
			"--data-dir", dataDir,
			"--sm", smType,
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPath,
			"--tls-key-file", nodeKeyPath,
		}

		prefix := fmt.Sprintf("[node%d] ", i+1)
		startNode(t, bin, args, prefix)
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}

	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	return adminPorts, ca
}

// waitForSettledFollowerView polls port's own /status until it reports a
// non-leader shard whose leader_id is both known (non-zero) and distinct
// from its own replica_id, or the timeout elapses. This guards against
// racing a still-settling election, where a node's cached leader hint can
// transiently point at itself.
func waitForSettledFollowerView(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	deadline := time.Now().Add(timeout)
	var lastDiag string
	for time.Now().Before(deadline) {
		status, diag := httpStatusRaw(t, client, port)
		if diag != "" {
			lastDiag = diag
		}
		if status != nil {
			if shards, ok := status["shards"].([]interface{}); ok {
				for _, s := range shards {
					shard, ok := s.(map[string]interface{})
					if !ok {
						continue
					}
					isLeader, _ := shard["is_leader"].(bool)
					replicaID, _ := shard["replica_id"].(float64)
					leaderID, _ := shard["leader_id"].(float64)
					if !isLeader && leaderID != 0 && leaderID != replicaID {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for port %d to settle on a non-self leader view (last: %s)",
		timeout, port, lastDiag)
}

// TestCluster_MembershipAdd_ForwardsToLeader drives POST /membership/add
// against a FOLLOWER admin port (not the leader). Before the leader-forward
// fix, AdminService.AddMember buried quicraft.NotLeaderError inside an
// opaque *service.MembershipError, so the REST handler had no leader
// routing information to forward with and the request failed with a
// generic 500 on any node that was not the leader. With the fix, the
// follower recognizes the not-leader condition, forwards the request to the
// real leader over the shared forwardToLeader path (re-signing the
// on-behalf-of assertion so the leader re-authorizes as the original
// "admin" caller, not the forwarding node), and the membership change is
// committed there -- so the follower-originated request succeeds end to
// end, exactly as if it had been issued directly to the leader.
func TestCluster_MembershipAdd_ForwardsToLeader(t *testing.T) {
	adminPorts, ca := startThreeNodeClusterSPIFFE(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	var followerPort int
	for _, p := range adminPorts {
		if p != leaderPort {
			followerPort = p
			break
		}
	}
	if followerPort == 0 {
		t.Fatal("no follower admin port found in a 3-node cluster")
	}

	// The election that produced leaderPort may still be settling on the
	// other nodes: a replica that recently lost (or never held) leadership
	// can briefly report a stale/self leader hint until it processes the
	// new leader's first heartbeat. Wait for the follower's OWN /status to
	// report a settled, non-self leader view before issuing the forwarded
	// request, so the test exercises "follower forwards to a genuinely
	// different leader" rather than racing the election.
	waitForSettledFollowerView(t, ca, followerPort, 30*time.Second)

	// Discover the shard ID and the highest existing replica ID from the
	// leader's /peers -- unlike /status (which only reports the LOCAL
	// node's own shard entry), /peers lists every member of the shard, so
	// the computed next replica ID is guaranteed unused cluster-wide.
	peersBefore := httpPeers(t, ca, leaderPort)
	shardsPeersBefore, ok := peersBefore["shards"].([]interface{})
	if !ok || len(shardsPeersBefore) == 0 {
		t.Fatalf("leader /peers missing shards: %v", peersBefore)
	}
	var shardID, maxReplicaID uint64
	for _, raw := range shardsPeersBefore {
		sp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sp["shard_id"].(float64)
		shardID = uint64(sid)
		peerList, ok := sp["peers"].([]interface{})
		if !ok {
			continue
		}
		for _, pRaw := range peerList {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				continue
			}
			rid, _ := p["replica_id"].(float64)
			if rid := uint64(rid); rid > maxReplicaID {
				maxReplicaID = rid
			}
		}
	}
	if maxReplicaID == 0 {
		t.Fatalf("could not determine an existing replica_id from leader /peers: %v", peersBefore)
	}
	newReplicaID := maxReplicaID + 1

	// Issue the add against the FOLLOWER. A 500 here means the
	// leader-forward fix regressed; a 200 means the follower forwarded the
	// request and the leader committed the membership change.
	code, body := httpMembershipAdd(t, ca, followerPort, shardID, newReplicaID, "127.0.0.1:1")
	if code != http.StatusOK {
		t.Fatalf("POST /membership/add to follower (port %d): status %d, body %s, want 200 (forwarded to leader)",
			followerPort, code, body)
	}

	// Confirm the leader actually applied the membership change.
	peers := httpPeers(t, ca, leaderPort)
	shardsPeers, ok := peers["shards"].([]interface{})
	if !ok || len(shardsPeers) == 0 {
		t.Fatalf("leader /peers missing shards: %v", peers)
	}
	found := false
	for _, raw := range shardsPeers {
		sp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sp["shard_id"].(float64)
		if uint64(sid) != shardID {
			continue
		}
		peerList, ok := sp["peers"].([]interface{})
		if !ok {
			continue
		}
		for _, pRaw := range peerList {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				continue
			}
			rid, _ := p["replica_id"].(float64)
			if uint64(rid) == newReplicaID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("leader /peers does not report new replica_id=%d after forwarded add: %v", newReplicaID, peers)
	}
}

// TestCluster_MembershipRemove_ForwardsToLeader mirrors
// TestCluster_MembershipAdd_ForwardsToLeader for the removal path: it first
// adds a throwaway replica directly against the LEADER (an uncontested setup
// step, not itself under test), then issues POST /membership/remove for that
// replica against a FOLLOWER admin port. Before the leader-forward fix this
// failed the same way AddMember did -- quicraft.NotLeaderError buried inside
// *service.MembershipError left the REST handler with no routing information,
// so any non-leader node answered with a generic 500. With the fix in place
// the follower recognizes the not-leader condition, forwards the request
// over the shared forwardToLeader path (re-signing the on-behalf-of
// assertion so the leader re-authorizes as the original "admin" caller), and
// the leader commits the removal -- so the follower-originated remove
// succeeds end to end and the replica disappears from the leader's /peers.
func TestCluster_MembershipRemove_ForwardsToLeader(t *testing.T) {
	adminPorts, ca := startThreeNodeClusterSPIFFE(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	var followerPort int
	for _, p := range adminPorts {
		if p != leaderPort {
			followerPort = p
			break
		}
	}
	if followerPort == 0 {
		t.Fatal("no follower admin port found in a 3-node cluster")
	}

	// See waitForSettledFollowerView's doc comment on
	// TestCluster_MembershipAdd_ForwardsToLeader: wait for the follower's own
	// view of leadership to settle before routing a forwarded request
	// through it.
	waitForSettledFollowerView(t, ca, followerPort, 30*time.Second)

	// Discover the shard ID and the highest existing replica ID from the
	// leader's /peers, exactly as the add-forward test does.
	peersBefore := httpPeers(t, ca, leaderPort)
	shardsPeersBefore, ok := peersBefore["shards"].([]interface{})
	if !ok || len(shardsPeersBefore) == 0 {
		t.Fatalf("leader /peers missing shards: %v", peersBefore)
	}
	var shardID, maxReplicaID uint64
	for _, raw := range shardsPeersBefore {
		sp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sp["shard_id"].(float64)
		shardID = uint64(sid)
		peerList, ok := sp["peers"].([]interface{})
		if !ok {
			continue
		}
		for _, pRaw := range peerList {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				continue
			}
			rid, _ := p["replica_id"].(float64)
			if rid := uint64(rid); rid > maxReplicaID {
				maxReplicaID = rid
			}
		}
	}
	if maxReplicaID == 0 {
		t.Fatalf("could not determine an existing replica_id from leader /peers: %v", peersBefore)
	}
	extraReplicaID := maxReplicaID + 1

	// Setup: add a throwaway replica directly against the LEADER. This is
	// not the behavior under test (that is
	// TestCluster_MembershipAdd_ForwardsToLeader's job) -- it only exists so
	// there is a real, currently-committed replica to remove without
	// touching any of the 3 real cluster members' quorum eligibility.
	addCode, addBody := httpMembershipAdd(t, ca, leaderPort, shardID, extraReplicaID, "127.0.0.1:1")
	if addCode != http.StatusOK {
		t.Fatalf("setup: POST /membership/add to leader (port %d): status %d, body %s, want 200",
			leaderPort, addCode, addBody)
	}
	peersAfterAdd := httpPeers(t, ca, leaderPort)
	if !membershipPeersContainsReplica(peersAfterAdd, shardID, extraReplicaID) {
		t.Fatalf("setup: leader /peers does not report extra replica_id=%d after add: %v", extraReplicaID, peersAfterAdd)
	}

	// Issue the remove against the FOLLOWER. A 500 here means the
	// leader-forward fix regressed for the remove path; a 200 means the
	// follower forwarded the request and the leader committed the removal.
	removeCode, removeBody := httpMembershipRemove(t, ca, followerPort, shardID, extraReplicaID)
	if removeCode != http.StatusOK {
		t.Fatalf("POST /membership/remove to follower (port %d): status %d, body %s, want 200 (forwarded to leader)",
			followerPort, removeCode, removeBody)
	}

	// Confirm the leader actually applied the removal.
	peersAfterRemove := httpPeers(t, ca, leaderPort)
	if membershipPeersContainsReplica(peersAfterRemove, shardID, extraReplicaID) {
		t.Fatalf("leader /peers still reports replica_id=%d after forwarded remove: %v", extraReplicaID, peersAfterRemove)
	}
}

// membershipPeersContainsReplica reports whether the decoded /peers response
// lists replicaID as a peer of shardID.
func membershipPeersContainsReplica(peers map[string]interface{}, shardID, replicaID uint64) bool {
	shardsPeers, ok := peers["shards"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range shardsPeers {
		sp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sp["shard_id"].(float64)
		if uint64(sid) != shardID {
			continue
		}
		peerList, ok := sp["peers"].([]interface{})
		if !ok {
			continue
		}
		for _, pRaw := range peerList {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				continue
			}
			rid, _ := p["replica_id"].(float64)
			if uint64(rid) == replicaID {
				return true
			}
		}
	}
	return false
}

func TestCluster_SnapshotTriggered(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble",
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
	adminPorts, _, ca := startThreeNodeCluster(t, "pebble",
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
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
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
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
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
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
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
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
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

// TestCluster_MVCCSingleNode replaces what was TestCluster_ConcurrentSingleNode:
// "concurrent" has no MVCC/transaction subsystem, so go-qrdb#555's boot-time
// capability probe now refuses it at first boot before any CRUD could run at
// all (see TestCLI_Start_UnsupportedStateMachineFailsClosed in admin_test.go
// for that behavior). Giving "concurrent" a real transaction plane is tracked
// separately as #136; until then, "mvcc" is this matrix's third working SM
// alongside "pebble" above.
func TestCluster_MVCCSingleNode(t *testing.T) {
	adminPort, ca := startSingleNode(t, "mvcc")

	// Basic CRUD with the mvcc SM.
	httpPut(t, ca, adminPort, "mvcc/key1", []byte("value1"))
	val := httpGet(t, ca, adminPort, "mvcc/key1")
	if !bytes.Equal(val, []byte("value1")) {
		t.Fatalf("mvcc GET: got %q, want %q", val, "value1")
	}

	httpPut(t, ca, adminPort, "mvcc/key2", []byte("value2"))
	keys := httpList(t, ca, adminPort, "mvcc/")
	if len(keys) != 2 {
		t.Fatalf("mvcc list: got %d keys, want 2: %v", len(keys), keys)
	}
}
