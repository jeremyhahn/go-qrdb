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

// TestIsolationRaft_Scale_Foundation stands up a real 3-node cluster (same
// harness as TestIsolationRaft_EndToEnd, isolation_raft_cluster_test.go:67),
// creates a dedicated-raft ("IsolationRaft") database at RF=1, writes data
// through it, and drives the full quota-gated dynamic elastic Raft scaling
// flow (design §G, "isolation_raft_scale_test.go") end to end through the
// real qrdbd REST surface (`PATCH /v1/databases/{name}/scale`,
// `{"target_replicas": N}` per design §E.5):
//
//	create RF=1 distributed -> write data -> scale to 3 -> poll new nodes
//	-> assert ciphertext -> scale to 1 -> assert demoted+purged -> quota
//	-exceeded (no partial mutation)
//
// Restart-survival (the direct regression test for the join-flag crux this
// feature fixes) is a separate test, TestIsolationRaft_Scale_RestartSurvival,
// so each test's own cluster-bootstrap and SIGTERM/restart cost stays
// independently bounded for CI scheduling.
//
// scaleDatabaseTolerant/iraftAPIPatch below are the REST client helpers both
// tests use to drive PATCH .../scale.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// scaleFoundationTrustDomain is the cluster trust domain used for SPIFFE node
// identities in this file's harness.
const scaleFoundationTrustDomain = "qrdb.scale.cluster"

// scaleFoundationTenantID is the tenant under test.
const scaleFoundationTenantID = "scale-t1"

// scaleFoundationDBName is the dedicated-raft database created by this test.
const scaleFoundationDBName = "scaledb"

// TestIsolationRaft_Scale_Foundation stands up a 3-node cluster, creates a
// dedicated-raft database at RF=1, writes and reads back data, confirms the
// single replica is encrypted at rest, then drives the full scale-up,
// scale-down, and quota-exceeded flow. See the package-level doc comment
// above for the restart-survival counterpart.
func TestIsolationRaft_Scale_Foundation(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, scaleFoundationTrustDomain)
	}

	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		adminURL := fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
		peerAdminURLs[raftAddr] = adminURL
	}

	dataDirs := make([]string, 3)
	volumePoolDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		volumePoolDirs[i] = t.TempDir()
		configPaths[i] = writeIsolationRaftConfig(t, volumePoolDirs[i], peerAdminURLs)
	}

	// ---------------------------------------------------------------------
	// Start 3-node cluster.
	// ---------------------------------------------------------------------
	cmds := make([]*exec.Cmd, 3)
	for i := range 3 {
		args := []string{
			"start",
			"--config", configPaths[i],
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--cluster-trust-domain", scaleFoundationTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		cmd := spawnNode(t, bin, args, &logWriter{t: t, prefix: fmt.Sprintf("[scale-node%d] ", i+1)})
		cmds[i] = cmd
		t.Cleanup(func() {
			killNode(t, cmd)
		})
	}

	const scaleFoundationBarrierPassphrase = "integration-test-scale-barrier-secret"

	var sealWg sync.WaitGroup
	sealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], scaleFoundationBarrierPassphrase)
		}(i)
	}
	sealWg.Wait()

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	t.Logf("cluster healthy, leader on admin port %d", leaderPort)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	createVolumePoolREST(t, sysClient, leaderPort, "default")
	waitForVolumePoolReady(t, sysClient, leaderPort, "default", len(dataDirs), 60*time.Second)

	// ---------------------------------------------------------------------
	// Tenant + role + user, mirroring TestIsolationRaft_EndToEnd.
	// ---------------------------------------------------------------------
	t.Logf("creating tenant %s with IsolationRaft + barrier_auto_unseal", scaleFoundationTenantID)
	adminKeyPEM, tenantBody, tenantStatus := createRaftTenant(t, sysClient, leaderPort, scaleFoundationTenantID, "Scale Test Tenant")
	if tenantStatus != http.StatusCreated && tenantStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tenantStatus, tenantBody)
	}

	// The system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so this authenticates
	// as the tenant's own onboarded admin; its built-in Admin role cannot
	// create a NEW role (RolesCreate is SO-exclusive), so it self-assigns
	// its own pre-seeded SO role first.
	raftAdminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tenantBody)
	selfAssignTenantSO(t, leaderPort, scaleFoundationTenantID, raftAdminClient)
	roleBody, roleStatus := iraftAPIPost(t, raftAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": scaleFoundationTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft",
				"databases:create_raft",
				"databases:read",
				"databases:list",
				"databases:scale",
				"kv:put",
				"kv:get",
			},
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role: status %d, body: %s", roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("create role: could not decode role ID from response: %s", roleBody)
	}

	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, scaleFoundationTenantID)
	createBoundUser(t, raftAdminClient, leaderPort,
		map[string]interface{}{
			"username":  "admin",
			"email":     "admin@" + scaleFoundationTenantID + ".example.com",
			"tenant_id": scaleFoundationTenantID,
			"role_ids":  []string{roleResp.ID},
		}, tenantCertPath)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)

	// ---------------------------------------------------------------------
	// Create the dedicated-raft database at RF=1 -- the baseline the future
	// scale-up assertions (design §G step 3) will scale from.
	// ---------------------------------------------------------------------
	t.Logf("creating database %q as tenant %q (IsolationRaft, RF=1)", scaleFoundationDBName, scaleFoundationTenantID)
	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{
			"name":     scaleFoundationDBName,
			"backend":  "pebble",
			"replicas": 1,
		},
	)
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create database: status %d, body: %s", dbStatus, dbBody)
	}

	var dbInfoData []byte
	var dbInfoStatus int
	pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
		dbInfoData, dbInfoStatus = iraftAPIGetTolerant(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, scaleFoundationDBName))
		return dbInfoStatus == http.StatusOK && len(dbInfoData) > 0
	})
	if dbInfoStatus != http.StatusOK {
		t.Fatalf("GET /v1/databases/%s: status %d, body: %s", scaleFoundationDBName, dbInfoStatus, dbInfoData)
	}
	var dbInfo struct {
		Name     string `json:"name"`
		Backend  string `json:"backend"`
		ShardID  uint64 `json:"shardId"`
		Replicas int    `json:"replicas"`
	}
	if err := json.Unmarshal(dbInfoData, &dbInfo); err != nil {
		t.Fatalf("decode database info: %v\nraw: %s", err, dbInfoData)
	}
	if dbInfo.Replicas != 1 {
		t.Fatalf("expected replicas=1 (RF=1 baseline), got %d\ndb info: %s", dbInfo.Replicas, dbInfoData)
	}
	if dbInfo.Backend != "raft" {
		t.Fatalf("expected backend=raft, got %q\ndb info: %s", dbInfo.Backend, dbInfoData)
	}
	if dbInfo.ShardID == 0 {
		t.Fatalf("expected shardID > 0, got 0\ndb info: %s", dbInfoData)
	}
	t.Logf("RF=1 baseline PASS: database %s backend=%s shardID=%d replicas=%d",
		dbInfo.Name, dbInfo.Backend, dbInfo.ShardID, dbInfo.Replicas)

	// Confirm the dedicated shard actually started (barrier delivered to the
	// sole replica, reconciler attached it) and discover WHICH of the 3 admin
	// ports hosts it: unlike TestIsolationRaft_EndToEnd (RF=3, so every node
	// hosts a replica and any admin port works), an RF=1 placement assigns
	// the sole replica to exactly one node -- possibly not the current
	// system-shard leader (leaderPort). Since a single-replica Raft group is
	// trivially its own leader, hitting that node's admin port directly for
	// reads/writes needs no leader-forwarding.
	t.Log("waiting for dedicated shard to start (ShardCount >= 2) and locating its host node")
	dbHostPort := waitForShardOwnerPort(t, sysClient, adminPorts, 2, 120*time.Second)
	t.Logf("dedicated shard hosted on admin port %d", dbHostPort)

	// ---------------------------------------------------------------------
	// Write and read back data through the RF=1 dedicated shard -- this is
	// the pre-existing data the future scale-up assertion must confirm
	// survives on the newly added replicas.
	// ---------------------------------------------------------------------
	const numKeys = 10
	values := make([]string, numKeys)
	for i := range numKeys {
		values[i] = fmt.Sprintf("scale-val-%d", i)
	}

	t.Log("writing baseline keys to RF=1 dedicated shard")
	writeDeadline := time.Now().Add(120 * time.Second)
	for i := range numKeys {
		key := fmt.Sprintf("scale-data/%04d", i)
		val := []byte(values[i])
		if !pollUntil(time.Until(writeDeadline), 200*time.Millisecond, func() bool {
			status, err := dbScopedPutTolerant(t, tenantClient, dbHostPort, scaleFoundationDBName, key, val)
			return err == nil && status == http.StatusNoContent
		}) {
			t.Fatalf("timed out writing baseline key %q", key)
		}
		if i == 0 {
			writeDeadline = time.Now().Add(30 * time.Second)
		}
	}

	t.Log("reading baseline keys back from RF=1 dedicated shard")
	for i := range numKeys {
		key := fmt.Sprintf("scale-data/%04d", i)
		var got []byte
		var status int
		pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
			got, status = dbScopedGetTolerant(t, tenantClient, dbHostPort, scaleFoundationDBName, key)
			return status == http.StatusOK
		})
		if status != http.StatusOK {
			t.Fatalf("baseline key %q read timed out (last status: %d)", key, status)
		}
		if string(got) != values[i] {
			t.Fatalf("baseline key %q: got %q, want %q", key, got, values[i])
		}
	}
	t.Logf("baseline data PASS: %d keys committed and readable on RF=1 dedicated shard", numKeys)

	// ---------------------------------------------------------------------
	// Ciphertext-at-rest for the (sole, RF=1) replica -- the same proof
	// pattern the future scale-up assertion must repeat against each NEW
	// replica's data dir once design §E lands.
	// ---------------------------------------------------------------------
	t.Log("verifying RF=1 replica data is encrypted at rest (no plaintext value substrings on disk)")
	for i, dir := range dataDirs {
		for _, val := range values {
			assertPlaintextAbsentInDir(t, dir, []byte(val))
		}
		t.Logf("node %d: no plaintext baseline values found on disk under %s", i+1, dir)
	}

	// ---------------------------------------------------------------------
	// Scale-up: 1 -> 3. Requests target dbHostPort, which -- as the sole RF=1
	// replica -- is trivially its own shard leader, matching the pattern
	// dbScopedPutTolerant/dbScopedGetTolerant already use above.
	// ---------------------------------------------------------------------
	t.Log("scaling database up: target_replicas=3")
	var scaleStatus int
	var scaleBody []byte
	pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
		scaleStatus, scaleBody = scaleDatabaseTolerant(t, tenantClient, leaderPort, scaleFoundationDBName, 3)
		return scaleStatus == http.StatusOK
	})
	if scaleStatus != http.StatusOK {
		t.Fatalf("scale to 3 replicas: status %d, body: %s", scaleStatus, scaleBody)
	}

	pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
		dbInfoData, dbInfoStatus = iraftAPIGetTolerant(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, scaleFoundationDBName))
		return dbInfoStatus == http.StatusOK && json.Unmarshal(dbInfoData, &dbInfo) == nil && dbInfo.Replicas == 3
	})
	if dbInfo.Replicas != 3 {
		t.Fatalf("expected replicas=3 after scale-up, got %d\ndb info: %s", dbInfo.Replicas, dbInfoData)
	}
	t.Logf("scale-up PASS: database %s now reports replicas=%d", dbInfo.Name, dbInfo.Replicas)

	// Poll every admin port until 3 distinct nodes host the dedicated shard
	// (ShardCount >= 2 on each), then identify the two NEW replicas (every
	// port other than dbHostPort).
	t.Log("waiting for all 3 nodes to host the dedicated shard")
	newNodeIdx := waitForAllShardOwners(t, sysClient, adminPorts, 2, 120*time.Second)
	var newPorts []int
	for _, idx := range newNodeIdx {
		if adminPorts[idx] != dbHostPort {
			newPorts = append(newPorts, adminPorts[idx])
		}
	}
	if len(newPorts) != 2 {
		t.Fatalf("expected exactly 2 new replica ports, got %v (all owners: %v, dbHostPort=%d)",
			newPorts, newNodeIdx, dbHostPort)
	}
	t.Logf("scale-up PASS: new replicas hosted on admin ports %v", newPorts)

	// Confirm the pre-existing baseline data is visible directly from each
	// NEW replica (not just the original dbHostPort) -- proof the new
	// replicas actually caught up via snapshot, not just an empty shell.
	t.Log("polling new replicas directly for pre-existing baseline data")
	for _, port := range newPorts {
		for i := range numKeys {
			key := fmt.Sprintf("scale-data/%04d", i)
			var got []byte
			var status int
			pollUntil(60*time.Second, 200*time.Millisecond, func() bool {
				got, status = dbScopedGetTolerant(t, tenantClient, port, scaleFoundationDBName, key)
				return status == http.StatusOK
			})
			if status != http.StatusOK {
				t.Fatalf("new replica (port %d) key %q read timed out (last status: %d)", port, key, status)
			}
			if string(got) != values[i] {
				t.Fatalf("new replica (port %d) key %q: got %q, want %q", port, key, got, values[i])
			}
		}
		t.Logf("new replica (port %d) PASS: all %d baseline keys caught up", port, numKeys)
	}

	// Ciphertext-at-rest for each new replica's own on-disk shard directory
	// (under its node's volume-pool DataRoot, {DataRoot}/databases/{db}/...),
	// not merely the whole node dataDir -- the precise proof the design calls
	// for that a dynamically added replica's snapshot-delivered state is
	// encrypted under the tenant barrier, not plaintext.
	t.Log("verifying new replicas' on-disk shard directories are encrypted at rest")
	for _, idx := range newNodeIdx {
		if adminPorts[idx] == dbHostPort {
			continue
		}
		shardDir := findDirEndingIn(t, volumePoolDirs[idx], filepath.Join("databases", scaleFoundationDBName))
		if shardDir == "" {
			t.Fatalf("node %d (port %d): dedicated shard dir for %s not found under %s",
				idx+1, adminPorts[idx], scaleFoundationDBName, volumePoolDirs[idx])
		}
		for _, val := range values {
			assertPlaintextAbsentInDir(t, shardDir, []byte(val))
		}
		t.Logf("node %d (port %d): no plaintext baseline values found under %s", idx+1, adminPorts[idx], shardDir)
	}

	// ---------------------------------------------------------------------
	// Scale-down: 3 -> 1. dbHostPort was the sole founding replica and (per
	// selectReplicasToRemove's leader-last policy) is removed last, so it is
	// expected to remain the host after scaling back to 1.
	// ---------------------------------------------------------------------
	t.Log("scaling database down: target_replicas=1")
	pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
		scaleStatus, scaleBody = scaleDatabaseTolerant(t, tenantClient, leaderPort, scaleFoundationDBName, 1)
		return scaleStatus == http.StatusOK
	})
	if scaleStatus != http.StatusOK {
		t.Fatalf("scale to 1 replica: status %d, body: %s", scaleStatus, scaleBody)
	}

	pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
		dbInfoData, dbInfoStatus = iraftAPIGetTolerant(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, scaleFoundationDBName))
		return dbInfoStatus == http.StatusOK && json.Unmarshal(dbInfoData, &dbInfo) == nil && dbInfo.Replicas == 1
	})
	if dbInfo.Replicas != 1 {
		t.Fatalf("expected replicas=1 after scale-down, got %d\ndb info: %s", dbInfo.Replicas, dbInfoData)
	}
	t.Logf("scale-down PASS: database %s now reports replicas=%d", dbInfo.Name, dbInfo.Replicas)

	// The two demoted nodes' ShardCount must drop back below the
	// dedicated-shard threshold (system shard only), confirming
	// RaftBackendFactory.Close was invoked by the reconciler's demotion path.
	t.Log("waiting for demoted nodes to stop hosting the dedicated shard")
	for _, port := range newPorts {
		waitForShardCountBelow(t, sysClient, port, 2, 60*time.Second)
	}
	t.Logf("scale-down PASS: demoted ports %v no longer host the dedicated shard", newPorts)

	// Purge check: if a demoted node's on-disk shard dir is reclaimed
	// (CloseAndPurge wired into the reconciler's demotion path per this
	// change's prerequisites), it must no longer exist under that node's
	// volume-pool DataRoot.
	t.Log("verifying demoted nodes' on-disk shard directories were purged")
	for _, idx := range newNodeIdx {
		if adminPorts[idx] == dbHostPort {
			continue
		}
		var shardDir string
		pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
			shardDir = findDirEndingIn(t, volumePoolDirs[idx], filepath.Join("databases", scaleFoundationDBName))
			return shardDir == ""
		})
		if shardDir != "" {
			t.Fatalf("node %d (port %d): demoted shard dir %q still present after scale-down (expected purge)",
				idx+1, adminPorts[idx], shardDir)
		}
		t.Logf("node %d (port %d): demoted shard dir purged", idx+1, adminPorts[idx])
	}

	// ---------------------------------------------------------------------
	// Quota-exceeded: lower the tenant's MaxReplicasPerGroup to 2 and confirm
	// a scale request to 3 is rejected (429) with no partial mutation (the
	// database still reports replicas=1, unchanged).
	// ---------------------------------------------------------------------
	t.Log("setting tenant quota MaxReplicasPerGroup=2")
	quotaBody, quotaStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/quotas", leaderPort, scaleFoundationTenantID),
		map[string]interface{}{
			"max_databases":            -1,
			"max_raft_groups":          -1,
			"max_replicas_per_group":   2,
			"max_total_replicas":       -1,
			"max_dedicated_wal_groups": -1,
		},
	)
	if quotaStatus != http.StatusOK {
		t.Fatalf("set tenant quotas: status %d, body: %s", quotaStatus, quotaBody)
	}

	t.Log("attempting scale to 3 replicas against MaxReplicasPerGroup=2 (expect rejection)")
	scaleStatus, scaleBody = scaleDatabaseTolerant(t, tenantClient, leaderPort, scaleFoundationDBName, 3)
	if scaleStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for quota-exceeded scale, got status %d, body: %s",
			scaleStatus, scaleBody)
	}
	t.Log("quota-exceeded PASS: scale request rejected with 429")

	dbInfoData, dbInfoStatus = iraftAPIGet(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, scaleFoundationDBName))
	if dbInfoStatus != http.StatusOK {
		t.Fatalf("GET /v1/databases/%s after rejected scale: status %d, body: %s",
			scaleFoundationDBName, dbInfoStatus, dbInfoData)
	}
	if err := json.Unmarshal(dbInfoData, &dbInfo); err != nil {
		t.Fatalf("decode database info after rejected scale: %v\nraw: %s", err, dbInfoData)
	}
	if dbInfo.Replicas != 1 {
		t.Fatalf("quota-exceeded scale must not partially mutate placement: expected replicas=1, got %d\ndb info: %s",
			dbInfo.Replicas, dbInfoData)
	}
	t.Log("quota-exceeded PASS: no partial mutation (replicas still 1)")

	t.Log("TestIsolationRaft_Scale_Foundation PASS")
}

// waitForAllShardOwners polls GET /admin/topology on every port in ports
// until exactly 3 of them report ShardCount >= wantCount, returning the
// indices (into ports) of every port that reached wantCount. Used once a
// scale-up has been requested, to confirm the dedicated shard has spread to
// every node in the cluster.
func waitForAllShardOwners(t *testing.T, client *http.Client, ports []int, wantCount int, timeout time.Duration) []int {
	t.Helper()

	var lastOwners []int
	if pollUntil(timeout, 300*time.Millisecond, func() bool {
		var owners []int
		for i, port := range ports {
			rawURL := fmt.Sprintf("https://127.0.0.1:%d/admin/topology", port)
			body, status := iraftAPIGetTolerant(t, client, rawURL)
			if status != http.StatusOK {
				continue
			}
			var topo struct {
				ShardCount int `json:"shard_count"`
			}
			if err := json.Unmarshal(body, &topo); err != nil {
				continue
			}
			if topo.ShardCount >= wantCount {
				owners = append(owners, i)
			}
		}
		lastOwners = owners
		return len(owners) >= len(ports)
	}) {
		return lastOwners
	}
	t.Fatalf("timed out after %v waiting for all of %v to reach shard count >= %d (last owners: %v)",
		timeout, ports, wantCount, lastOwners)
	return nil
}

// waitForShardCountBelow polls GET /admin/topology on the given port until
// ShardCount drops below threshold, or the deadline expires. The inverse of
// waitForShardCount -- used to confirm a demoted node's dedicated shard was
// torn down (RaftBackendFactory.Close invoked by the reconciler).
func waitForShardCountBelow(t *testing.T, client *http.Client, port int, threshold int, timeout time.Duration) {
	t.Helper()

	rawURL := fmt.Sprintf("https://127.0.0.1:%d/admin/topology", port)
	var lastCount int
	if pollUntil(timeout, 300*time.Millisecond, func() bool {
		body, status := iraftAPIGetTolerant(t, client, rawURL)
		if status != http.StatusOK {
			return false
		}
		var topo struct {
			ShardCount int `json:"shard_count"`
		}
		if err := json.Unmarshal(body, &topo); err != nil {
			return false
		}
		lastCount = topo.ShardCount
		return topo.ShardCount < threshold
	}) {
		return
	}
	t.Fatalf("timed out after %v waiting for shard count on port %d to drop below %d (last count: %d)",
		timeout, port, threshold, lastCount)
}

// waitForShardOwnerPort polls GET /admin/topology on every port in ports
// until exactly one of them reports ShardCount >= wantCount, returning that
// port. Unlike waitForShardCount (which targets one already-known port), this
// is used when the caller does not yet know which node a low-RF placement
// (e.g. RF=1) landed its replica on. It fails the test if no port reaches
// wantCount before the deadline.
func waitForShardOwnerPort(t *testing.T, client *http.Client, ports []int, wantCount int, timeout time.Duration) int {
	t.Helper()

	lastCounts := make([]int, len(ports))
	owner := 0
	if pollUntil(timeout, 200*time.Millisecond, func() bool {
		for i, port := range ports {
			rawURL := fmt.Sprintf("https://127.0.0.1:%d/admin/topology", port)
			body, status := iraftAPIGetTolerant(t, client, rawURL)
			if status != http.StatusOK {
				continue
			}
			var topo struct {
				ShardCount int `json:"shard_count"`
			}
			if err := json.Unmarshal(body, &topo); err != nil {
				continue
			}
			lastCounts[i] = topo.ShardCount
			if topo.ShardCount >= wantCount {
				owner = port
				return true
			}
		}
		return false
	}) {
		return owner
	}
	t.Fatalf("timed out after %v waiting for any of %v to reach shard count >= %d (last counts: %v)",
		timeout, ports, wantCount, lastCounts)
	return 0
}

// scaleDatabaseTolerant issues PATCH /v1/databases/{name}/scale with body
// {"target_replicas": targetReplicas} using client, targeting the node on
// port. It returns (statusCode, body) without fataling, matching the
// dbScopedPutTolerant/dbScopedGetTolerant convention so a caller can poll
// across a leader-forwarding retry loop.
func scaleDatabaseTolerant(t *testing.T, client *http.Client, port int, dbName string, targetReplicas int) (int, []byte) {
	t.Helper()
	rawURL := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s/scale", port, dbName)
	body, status := iraftAPIPatch(t, client, rawURL, map[string]interface{}{"target_replicas": targetReplicas})
	return status, body
}

// iraftAPIPatch performs a PATCH request using the provided client and
// returns the response body bytes and status code, mirroring iraftAPIPost.
func iraftAPIPatch(t *testing.T, client *http.Client, rawURL string, body interface{}) ([]byte, int) {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("iraftAPIPatch marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPatch, rawURL, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("iraftAPIPatch %s: %v", rawURL, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("iraftAPIPatch %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode
}

// restartSurvivalTrustDomain is the cluster trust domain used for SPIFFE
// node identities in TestIsolationRaft_Scale_RestartSurvival's harness.
const restartSurvivalTrustDomain = "qrdb.scale.restart.cluster"

// restartSurvivalTenantID is the tenant under test.
const restartSurvivalTenantID = "scale-restart-t1"

// restartSurvivalDBName is the dedicated-raft database created by this test.
const restartSurvivalDBName = "scalerestartdb"

// TestIsolationRaft_Scale_RestartSurvival is the direct regression test for
// the join-flag crux this feature fixes (design §D): it creates a
// dedicated-raft database at RF=1, writes data, scales up to 3 (exercising
// the join=true bootstrap path for the two new replicas), waits for the new
// replicas to fully catch up, then SIGTERMs the whole cluster and restarts
// it. It asserts (a) all 3 nodes re-attach the dedicated shard after restart
// and (b) every key -- including the data replicated to the two
// scale-added replicas -- is still readable from all 3 nodes post-restart.
// A join=true restart of an already-caught-up replica would 404/stall
// rather than serve immediately, so a bounded poll for shard re-attachment
// doubles as an implicit proof the reconciler's joining->active promotion
// (§D.3) completed before the restart.
//
// Kept as a separate test (own cluster bootstrap + SIGTERM/restart cost) so
// TestIsolationRaft_Scale_Foundation's own runtime stays independently
// bounded for CI scheduling -- see that test's package-level doc comment.
func TestIsolationRaft_Scale_RestartSurvival(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, restartSurvivalTrustDomain)
	}

	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	dataDirs := make([]string, 3)
	volumePoolDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		volumePoolDirs[i] = t.TempDir()
		configPaths[i] = writeIsolationRaftConfig(t, volumePoolDirs[i], peerAdminURLs)
	}

	startCluster := func(tag string) []*exec.Cmd {
		cmds := make([]*exec.Cmd, 3)
		for i := range 3 {
			args := []string{
				"start",
				"--config", configPaths[i],
				"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
				"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
				"--data-dir", dataDirs[i],
				"--sm", "pebble",
				"--discovery", "static",
				"--peers", peersFlag,
				"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
				"--shard-id", "1",
				"--replica-id", fmt.Sprintf("%d", i+1),
				"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
				"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
				"--cluster-trust-domain", restartSurvivalTrustDomain,
				"--tls-ca-file", ca.CertPath,
				"--tls-cert-file", nodeCertPaths[i],
				"--tls-key-file", nodeKeyPaths[i],
			}
			cmd := spawnNode(t, bin, args, &logWriter{t: t, prefix: fmt.Sprintf("[scale-restart-node%d-%s] ", i+1, tag)})
			cmds[i] = cmd
			t.Cleanup(func() {
				killNode(t, cmd)
			})
		}
		return cmds
	}

	const restartBarrierPassphrase = "integration-test-scale-restart-barrier-secret"

	unsealAll := func(initialize bool) {
		var wg sync.WaitGroup
		wg.Add(3)
		for i := range 3 {
			go func(idx int) {
				defer wg.Done()
				waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
				if initialize {
					initAndUnsealBarrier(t, ca, adminPorts[idx], restartBarrierPassphrase)
					return
				}
				_, st := iraftAPIPost(t, mtlsHTTPClient(t, ca, 10*time.Second),
					fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPorts[idx]),
					map[string]interface{}{"secret": []byte(restartBarrierPassphrase)})
				if st != http.StatusNoContent {
					t.Errorf("unseal node %d: status %d", idx+1, st)
				}
			}(i)
		}
		wg.Wait()
	}

	// ---------------------------------------------------------------------
	// Start + unseal.
	// ---------------------------------------------------------------------
	cmds := startCluster("R1")
	unsealAll(true)
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	createVolumePoolREST(t, sysClient, leaderPort, "default")
	waitForVolumePoolReady(t, sysClient, leaderPort, "default", len(dataDirs), 60*time.Second)

	// ---------------------------------------------------------------------
	// Tenant + role + user.
	// ---------------------------------------------------------------------
	adminKeyPEM, tBody, tStatus := createRaftTenant(t, sysClient, leaderPort, restartSurvivalTenantID, "Scale Restart Test Tenant")
	if tStatus != http.StatusCreated && tStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d", tStatus)
	}

	// The system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so this authenticates
	// as the tenant's own onboarded admin; its built-in Admin role cannot
	// create a NEW role (RolesCreate is SO-exclusive), so it self-assigns
	// its own pre-seeded SO role first.
	raftAdminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tBody)
	selfAssignTenantSO(t, leaderPort, restartSurvivalTenantID, raftAdminClient)
	roleBody, rStatus := iraftAPIPost(t, raftAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name": "db-admin", "tenant_id": restartSurvivalTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft",
				"databases:read", "databases:list", "databases:scale",
				"kv:put", "kv:get",
			},
		})
	if rStatus != http.StatusCreated && rStatus != http.StatusOK {
		t.Fatalf("create role: status %d, body: %s", rStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("decode role ID: %s", roleBody)
	}
	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, restartSurvivalTenantID)
	createBoundUser(t, raftAdminClient, leaderPort,
		map[string]interface{}{
			"username": "admin", "email": "admin@" + restartSurvivalTenantID + ".example.com", "tenant_id": restartSurvivalTenantID,
			"role_ids": []string{roleResp.ID},
		}, tenantCertPath)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)

	// ---------------------------------------------------------------------
	// Create RF=1, write a small key set, then scale up to 3 -- exercising
	// the join=true bootstrap path this feature fixes.
	// ---------------------------------------------------------------------
	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{
			"name": restartSurvivalDBName, "backend": "pebble", "replicas": 1,
		})
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create database: status %d, body: %s", dbStatus, dbBody)
	}

	t.Log("waiting for RF=1 dedicated shard to start and locating its host node")
	dbHostPort := waitForShardOwnerPort(t, sysClient, adminPorts, 2, 120*time.Second)

	const numKeys = 5
	values := make([]string, numKeys)
	for i := range numKeys {
		values[i] = fmt.Sprintf("scale-restart-val-%d", i)
	}
	writeDeadline := time.Now().Add(60 * time.Second)
	for i := range numKeys {
		key := fmt.Sprintf("scale-restart-data/%04d", i)
		if !pollUntil(time.Until(writeDeadline), 200*time.Millisecond, func() bool {
			status, err := dbScopedPutTolerant(t, tenantClient, dbHostPort, restartSurvivalDBName, key, []byte(values[i]))
			return err == nil && status == http.StatusNoContent
		}) {
			t.Fatalf("timed out writing baseline key %q", key)
		}
		if i == 0 {
			writeDeadline = time.Now().Add(30 * time.Second)
		}
	}

	t.Log("scaling database up: target_replicas=3")
	var scaleStatus int
	var scaleBody []byte
	pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
		scaleStatus, scaleBody = scaleDatabaseTolerant(t, tenantClient, leaderPort, restartSurvivalDBName, 3)
		return scaleStatus == http.StatusOK
	})
	if scaleStatus != http.StatusOK {
		t.Fatalf("scale to 3 replicas: status %d, body: %s", scaleStatus, scaleBody)
	}

	// Wait for all 3 nodes to host the dedicated shard, then poll every node
	// (not just dbHostPort) directly for the pre-scale baseline data -- this
	// is the pre-restart "new replicas caught up" precondition the restart
	// itself must not regress.
	t.Log("waiting for all 3 nodes to host the dedicated shard (pre-restart)")
	waitForAllShardOwners(t, sysClient, adminPorts, 2, 120*time.Second)
	for _, port := range adminPorts {
		for i := range numKeys {
			key := fmt.Sprintf("scale-restart-data/%04d", i)
			var got []byte
			var status int
			pollUntil(60*time.Second, 200*time.Millisecond, func() bool {
				got, status = dbScopedGetTolerant(t, tenantClient, port, restartSurvivalDBName, key)
				return status == http.StatusOK
			})
			if status != http.StatusOK {
				t.Fatalf("pre-restart: port %d key %q read timed out (last status: %d)", port, key, status)
			}
			if string(got) != values[i] {
				t.Fatalf("pre-restart: port %d key %q: got %q, want %q", port, key, got, values[i])
			}
		}
	}
	t.Log("pre-restart PASS: all 3 replicas caught up and serving baseline data")

	// ---------------------------------------------------------------------
	// Full-cluster SIGTERM.
	// ---------------------------------------------------------------------
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("signal node %d: %v", i+1, err)
		}
	}
	for _, cmd := range cmds {
		_ = awaitNodeExit(t, cmd, nodeStopTimeout)
	}

	// ---------------------------------------------------------------------
	// Restart + re-unseal + re-attach.
	// ---------------------------------------------------------------------
	startCluster("R2")
	unsealAll(false)
	for i := range 3 {
		waitForHealthy(t, dataDirs[i], 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort2 := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	restartClient := mtlsHTTPClient(t, ca, 15*time.Second)
	for i, port := range adminPorts {
		t.Logf("waiting for shard count >= 2 on restarted node %d", i+1)
		waitForShardCount(t, restartClient, port, 2, 120*time.Second)
	}
	_ = leaderPort2

	// ---------------------------------------------------------------------
	// NO-DATA-LOSS: every key (including data replicated to the two
	// scale-added replicas) is readable from all 3 nodes post-restart. A
	// join=true restart of an already-caught-up replica would 404/stall on
	// reads rather than serve immediately, so this bounded poll doubles as
	// the direct regression proof for the join-flag crux.
	// ---------------------------------------------------------------------
	restartTenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)
	for _, port := range adminPorts {
		for i := range numKeys {
			key := fmt.Sprintf("scale-restart-data/%04d", i)
			var got []byte
			var status int
			pollUntil(60*time.Second, 200*time.Millisecond, func() bool {
				got, status = dbScopedGetTolerant(t, restartTenantClient, port, restartSurvivalDBName, key)
				return status == http.StatusOK
			})
			if status != http.StatusOK {
				t.Fatalf("post-restart: port %d key %q read timed out (last status: %d)", port, key, status)
			}
			if string(got) != values[i] {
				t.Fatalf("post-restart: port %d key %q: got %q, want %q", port, key, got, values[i])
			}
		}
		t.Logf("post-restart PASS: port %d served all %d keys", port, numKeys)
	}

	t.Log("TestIsolationRaft_Scale_RestartSurvival PASS")
}
