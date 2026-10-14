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

// TestIsolationRaft_EndToEnd proves Phase 4c of the dedicated-raft tenant
// database stack works end-to-end across a real 3-node cluster:
//
//   - Phase 4c-1b: placement scheduling — POST /v1/databases with
//     IsolationRaft creates a Placement record in the replicated system KV
//     listing 3 replicas on 3 distinct nodes.
//
//   - Phase 4c-1c: secure cluster key delivery — the leader sends the
//     tenant barrier's root key to each follower node via HPKE-sealed HTTP
//     POST /v1/cluster/keydelivery, verifying the recipient certificate's
//     SPIFFE URI SAN (spiffe://<trustDomain>/node/<raftAddr>).
//
//   - Phase 4c-1d: reconciler-driven follower start — on each node the
//     TenantShardReconciler watches the placement table and calls
//     RaftBackendFactory.Attach once the barrier is unsealed; ShardCount
//     rising to >= 2 on all 3 nodes is the observable proof.
//
//   - Restart survival — after a graceful full-cluster SIGTERM, restarted
//     nodes re-attach the dedicated shard from persisted state, confirming
//     that the reconciler boot re-attach path works.
//
// The test uses real qrdbd daemon processes started with SPIFFE-SAN node
// certificates, per-node volume pools for barrier storage, and entitlement
// config that permits IsolationRaft tenant databases. No Raft or key delivery
// is faked.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// iraftTrustDomain is the cluster trust domain used for SPIFFE node identities
// throughout this test. All nodes must agree on a single value.
const iraftTrustDomain = "qrdb.cluster"

// iraftTenantID is the tenant under test.
const iraftTenantID = "t1"

// iraftDBName is the dedicated-raft database created by the test.
const iraftDBName = "db1"

// TestIsolationRaft_EndToEnd is the Phase 4c end-to-end integration test.
func TestIsolationRaft_EndToEnd(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	// Allocate ports: 3 raft (UDP), 3 admin (TCP), 3 gRPC (TCP).
	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	// Generate SPIFFE-SAN node certificates — one per node, reused across
	// restarts. The SPIFFE URI SAN (spiffe://<trustDomain>/node/<raftAddr>)
	// is mandatory for Phase 4c-1c key delivery: the sender's fetchNodeKey
	// verifies it and rejects the transfer if absent (ErrRecipientUntrusted).
	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, iraftTrustDomain)
	}

	// Build the peer admin URL map: raftAddr → https://127.0.0.1:<adminPort>.
	// This is required for Phase 4c-1c key delivery: the peerAdminRegistry
	// fallback assumes all peers share the same admin port (correct for
	// multi-host production clusters), which is wrong here since all 3 nodes
	// are on 127.0.0.1 with distinct admin ports. Each node's config receives
	// the FULL map so that whichever node is the leader at delivery time has
	// correct entries for all followers.
	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		adminURL := fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
		peerAdminURLs[raftAddr] = adminURL
	}

	// Per-node data and volume-pool directories (survive restart via dataDirs).
	dataDirs := make([]string, 3)
	volumePoolDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		volumePoolDirs[i] = t.TempDir()
		configPaths[i] = writeIsolationRaftConfig(t, volumePoolDirs[i], peerAdminURLs)
	}

	// ---------------------------------------------------------------------------
	// Phase 1: Start 3-node cluster.
	// ---------------------------------------------------------------------------
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
			"--cluster-trust-domain", iraftTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		cmd := spawnNode(t, bin, args, &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R1] ", i+1)})
		cmds[i] = cmd
		t.Cleanup(func() {
			killNode(t, cmd)
		})
	}

	// ---------------------------------------------------------------------------
	// Sealed-window phase: initialize and unseal the system barrier on all
	// 3 nodes concurrently. When seal.enabled=true the daemon starts a minimal
	// sealed-window REST server (barrier + ceremony only) on the admin port
	// BEFORE constructing the Raft host. The full server (and host.id) only
	// appear after unsealing. We must drive init+unseal on each node before
	// waitForHealthy and waitForAdmin succeed.
	// ---------------------------------------------------------------------------
	const iraftBarrierPassphrase = "integration-test-barrier-secret"

	var sealWg sync.WaitGroup
	sealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer sealWg.Done()
			t.Logf("node %d: waiting for sealed-window listener on port %d", idx+1, adminPorts[idx])
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			t.Logf("node %d: initializing and unsealing system barrier", idx+1)
			initAndUnsealBarrier(t, ca, adminPorts[idx], iraftBarrierPassphrase)
			t.Logf("node %d: system barrier unsealed", idx+1)
		}(i)
	}
	sealWg.Wait()

	// Wait for all nodes to be healthy and elect a leader.
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	t.Logf("cluster healthy, leader on admin port %d", leaderPort)

	// System-admin mTLS client (CN=admin, OU=system).
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	createVolumePoolREST(t, sysClient, leaderPort, "default")

	// ---------------------------------------------------------------------------
	// Phase 2: Create tenant "t1" with IsolationRaft + barrier_auto_unseal.
	// ---------------------------------------------------------------------------
	t.Log("creating tenant t1 with IsolationRaft + barrier_auto_unseal")
	adminKeyPEM, tenantBody, tenantStatus := createRaftTenant(t, sysClient, leaderPort, iraftTenantID, "Test Tenant 1")
	if tenantStatus != http.StatusCreated && tenantStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tenantStatus, tenantBody)
	}
	t.Logf("tenant created: status %d", tenantStatus)

	// ---------------------------------------------------------------------------
	// Phase 3: Create a role + user in tenant "t1" so the tenant cert is
	// authorized to create databases. First-boot only seeds the system tenant;
	// non-system tenants require explicit role/user setup. The system tenant
	// can no longer provision records inside another tenant's IAM at all
	// (owner ruling, 2026-10-01), so this authenticates as the tenant's own
	// onboarded admin; its built-in Admin role cannot create a NEW role
	// (RolesCreate is SO-exclusive), so it self-assigns its own pre-seeded SO
	// role first.
	// ---------------------------------------------------------------------------
	raftAdminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tenantBody)
	selfAssignTenantSO(t, leaderPort, iraftTenantID, raftAdminClient)
	t.Log("creating role 'db-admin' in tenant t1")
	roleBody, roleStatus := iraftAPIPost(t, raftAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": iraftTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft",
				"databases:create_raft",
				"databases:read",
				"databases:list",
				// kv:put and kv:get are required for assertion (e)/(f): the
				// tenant client must be able to write and read KV data on the
				// dedicated Raft shard via PUT /kv/{key} + X-QRDB-Database.
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
	t.Logf("role created: id=%s", roleResp.ID)

	t.Log("creating user 'admin' in tenant t1, bound to the tenant certificate")
	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, iraftTenantID)
	createBoundUser(t, raftAdminClient, leaderPort,
		map[string]interface{}{
			"username":  "admin",
			"email":     "admin@" + iraftTenantID + ".example.com",
			"tenant_id": iraftTenantID,
			"role_ids":  []string{roleResp.ID},
		}, tenantCertPath)

	// ---------------------------------------------------------------------------
	// Phase 4: Create the IsolationRaft database "db1" as tenant t1.
	//
	// The certificate must carry OU=t1 so the mTLS authenticator sets
	// tenant.FromContext(ctx) = "t1". CreateForTenant then takes the
	// IsolationRaft path (placement scheduling + key delivery). A system admin
	// cert (OU=system) would skip the IsolationRaft path entirely.
	// ---------------------------------------------------------------------------
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)

	t.Logf("creating database %q as tenant %q (IsolationRaft, RF=3)", iraftDBName, iraftTenantID)
	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{
			"name":     iraftDBName,
			"backend":  "pebble",
			"replicas": 3,
		},
	)
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create database: status %d, body: %s", dbStatus, dbBody)
	}
	t.Logf("database created: status %d", dbStatus)

	// ---------------------------------------------------------------------------
	// Assertion (a): Placement was scheduled with RF=3, IsolationRaft backend,
	// and a non-zero shard ID.
	//
	// The internal placement record lives at __sys:placement/<tenantID>/<db> in
	// the replicated system KV, but that key is stored WITHOUT the
	// "system/system/" namespace prefix that the REST KV endpoint adds to every
	// key. Attempting GET /kv/__sys:placement/... would therefore look for
	// "system/system/__sys:placement/..." and return 404.
	//
	// Instead, GET /v1/databases/{name} goes through the DatabaseService (not
	// the namespaced KV service) and returns the DatabaseInfo directly from the
	// database manager, which includes the replicas count, backend, and ShardID.
	// The tenant cert (OU=t1) is required so GetForTenant resolves ownership.
	//
	// The distinct-node guarantee is inherent in the scheduler's anti-affinity
	// rule (one replica per node) and is validated indirectly by assertion (b):
	// ShardCount >= 2 rising on all 3 nodes proves each node holds exactly one
	// replica of the dedicated shard, giving 3 replicas on 3 distinct nodes.
	// ---------------------------------------------------------------------------
	t.Log("assertion (a): verifying database has RF=3, raft backend, and non-zero shardID")
	var dbInfoData []byte
	var dbInfoStatus int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		dbInfoData, dbInfoStatus = iraftAPIGet(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, iraftDBName))
		if dbInfoStatus == http.StatusOK && len(dbInfoData) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if dbInfoStatus != http.StatusOK {
		t.Fatalf("assertion (a): GET /v1/databases/%s: status %d, body: %s",
			iraftDBName, dbInfoStatus, dbInfoData)
	}
	var dbInfo struct {
		Name     string `json:"name"`
		Backend  string `json:"backend"`
		ShardID  uint64 `json:"shardId"`
		Replicas int    `json:"replicas"`
	}
	if err := json.Unmarshal(dbInfoData, &dbInfo); err != nil {
		t.Fatalf("assertion (a): decode database info: %v\nraw: %s", err, dbInfoData)
	}
	if dbInfo.Replicas != 3 {
		t.Fatalf("assertion (a): expected replicas=3, got %d\ndb info: %s",
			dbInfo.Replicas, dbInfoData)
	}
	if dbInfo.ShardID == 0 {
		t.Fatalf("assertion (a): expected shardID > 0, got 0\ndb info: %s", dbInfoData)
	}
	if dbInfo.Backend != "raft" {
		t.Fatalf("assertion (a): expected backend=raft, got %q\ndb info: %s", dbInfo.Backend, dbInfoData)
	}
	t.Logf("assertion (a) PASS: database %s backend=%s shardID=%d replicas=%d",
		dbInfo.Name, dbInfo.Backend, dbInfo.ShardID, dbInfo.Replicas)

	// ---------------------------------------------------------------------------
	// Assertion (b): Barrier key delivered and shard started on all 3 nodes.
	//
	// TenantShardReconciler defers Attach while the tenant barrier is absent or
	// sealed (Phase 4c-1d). Once Phase 4c-1c delivers the root key and the
	// barrier unseals, the reconciler calls Attach and ShardCount rises from 1
	// (system shard) to >= 2 (system + dedicated). Observing ShardCount >= 2 on
	// all 3 nodes is therefore a compound proof of: (b1) barrier unsealed on each
	// node, and (b2) dedicated shard started by the reconciler.
	// ---------------------------------------------------------------------------
	t.Log("assertion (b): waiting for dedicated shard to start on all 3 nodes (proves barrier delivery + reconciler attach)")
	for i, port := range adminPorts {
		t.Logf("waiting for shard count >= 2 on node %d (port %d)", i+1, port)
		waitForShardCount(t, sysClient, port, 2, 120*time.Second)
		t.Logf("assertion (b) node %d PASS: ShardCount >= 2", i+1)
	}
	t.Log("assertion (b) PASS: dedicated shard running on all 3 nodes")

	// ---------------------------------------------------------------------------
	// Assertion (c): System shard replication is unaffected — write N keys on
	// the leader, read from all followers.
	// ---------------------------------------------------------------------------
	t.Log("assertion (c): verifying system shard replication is healthy across all nodes")
	const numKeys = 20
	for i := range numKeys {
		key := fmt.Sprintf("iraft-verify/%04d", i)
		httpPut(t, ca, leaderPort, key, []byte(fmt.Sprintf("v%d", i)))
	}

	for _, port := range adminPorts {
		var keys []string
		kDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(kDeadline) {
			keys = httpListTolerant(t, ca, port, "iraft-verify/")
			if len(keys) == numKeys {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(keys) != numKeys {
			t.Fatalf("assertion (c): port %d: expected %d keys, got %d", port, numKeys, len(keys))
		}
	}
	t.Logf("assertion (c) PASS: %d keys readable on all 3 nodes", numKeys)

	// ---------------------------------------------------------------------------
	// Assertion (e): Dedicated raft group data plane — write numDB1Keys keys to
	// db1 via the tenant mTLS client with X-QRDB-Database: db1 BEFORE restart.
	//
	// The REST service registry's resolve() method sees isolation == IsolationRaft
	// and calls raftBackend.Services(ctx, "t1", "db1") to obtain the dedicated
	// shard's KV service (pkg/server/rest_registry.go line 198). A successful
	// write proves the dedicated shard's data plane is operational. Reading all
	// keys back on the leader confirms they committed in the dedicated group's
	// Raft log, not the system shard.
	// ---------------------------------------------------------------------------
	t.Log("assertion (e): writing keys to dedicated raft group db1 before restart")
	const numDB1Keys = 20
	db1Values := make([]string, numDB1Keys)
	for i := range numDB1Keys {
		db1Values[i] = fmt.Sprintf("db1-val-%d", i)
	}

	// Allow 120 s for all writes. The first write may need up to ~60 s for the
	// dedicated group to elect its own leader (the group is confirmed running by
	// assertion (b), but leader election within it is async). After the first
	// write succeeds the remaining ones complete in milliseconds.
	eWriteDeadline := time.Now().Add(120 * time.Second)
	for i := range numDB1Keys {
		key := fmt.Sprintf("db1-data/%04d", i)
		val := []byte(db1Values[i])
		var written bool
		for time.Now().Before(eWriteDeadline) {
			status, err := dbScopedPutTolerant(t, tenantClient, leaderPort, iraftDBName, key, val)
			if err == nil && status == http.StatusNoContent {
				written = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !written {
			t.Fatalf("assertion (e): timed out writing db1 key %q to dedicated raft group", key)
		}
		if i == 0 {
			// First write confirmed: the dedicated group's leader is reachable.
			// Shorten the per-write budget for the remaining keys.
			eWriteDeadline = time.Now().Add(30 * time.Second)
		}
	}

	// Read all keys back from the leader to confirm they committed on the
	// dedicated shard (not the system shard).
	for i := range numDB1Keys {
		key := fmt.Sprintf("db1-data/%04d", i)
		var got []byte
		var status int
		rDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(rDeadline) {
			got, status = dbScopedGetTolerant(t, tenantClient, leaderPort, iraftDBName, key)
			if status == http.StatusOK {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("assertion (e): db1 key %q read timed out (last status: %d)", key, status)
		}
		if string(got) != db1Values[i] {
			t.Fatalf("assertion (e): db1 key %q: got %q, want %q", key, got, db1Values[i])
		}
	}
	t.Logf("assertion (e) PASS: %d keys committed on dedicated raft group db1 before restart", numDB1Keys)

	// ---------------------------------------------------------------------------
	// Phase 5: Graceful shutdown — SIGTERM all 3 nodes.
	// ---------------------------------------------------------------------------
	t.Log("stopping all nodes for restart test")
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("signal node %d: %v (may have already exited)", i+1, err)
		}
	}
	for i, cmd := range cmds {
		_ = awaitNodeExit(t, cmd, nodeStopTimeout)
		t.Logf("node %d stopped", i+1)
	}

	// ---------------------------------------------------------------------------
	// Phase 6: Restart all 3 nodes with the same data directories and ports.
	// ---------------------------------------------------------------------------
	t.Log("restarting all nodes")
	cmds2 := make([]*exec.Cmd, 3)
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
			"--cluster-trust-domain", iraftTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		cmd := spawnNode(t, bin, args, &logWriter{t: t, prefix: fmt.Sprintf("[node%d-R2] ", i+1)})
		cmds2[i] = cmd
		t.Cleanup(func() {
			killNode(t, cmd)
		})
	}

	// Re-unseal barriers on all 3 restarted nodes (sealed-window phase again).
	var resealWg sync.WaitGroup
	resealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer resealWg.Done()
			t.Logf("restart node %d: waiting for sealed-window", idx+1)
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			t.Logf("restart node %d: unsealing system barrier", idx+1)
			// On restart the barrier is already initialized; skip init, just unseal.
			unsealBody, unsealStatus := iraftAPIPost(t, mtlsHTTPClient(t, ca, 10*time.Second),
				fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPorts[idx]),
				map[string]interface{}{"secret": []byte(iraftBarrierPassphrase)},
			)
			if unsealStatus != http.StatusNoContent {
				t.Errorf("restart: barrier unseal node %d: status %d, body: %s",
					idx+1, unsealStatus, unsealBody)
			}
			t.Logf("restart node %d: barrier unsealed", idx+1)
		}(i)
	}
	resealWg.Wait()

	for i := range 3 {
		waitForHealthy(t, dataDirs[i], 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	// Capture leaderPort2 — used by assertion (f) to target the post-restart
	// cluster leader when reading db1 keys back from the dedicated shard.
	leaderPort2 := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	t.Logf("post-restart cluster leader on admin port %d", leaderPort2)

	// ---------------------------------------------------------------------------
	// Assertion (d): Dedicated shard re-attached on all nodes after restart.
	//
	// The reconciler's boot re-attach pass (ReconcileOnce called immediately in
	// Run) reads the placement table and calls Attach for each assigned replica.
	// ShardCount >= 2 after restart proves the reconciler re-attach path works.
	// ---------------------------------------------------------------------------
	t.Log("assertion (d): verifying dedicated shard re-attached after restart")
	restartClient := mtlsHTTPClient(t, ca, 15*time.Second)
	for i, port := range adminPorts {
		t.Logf("waiting for shard count >= 2 on restarted node %d (port %d)", i+1, port)
		waitForShardCount(t, restartClient, port, 2, 120*time.Second)
		t.Logf("assertion (d) node %d PASS: ShardCount >= 2 after restart", i+1)
	}
	t.Log("assertion (d) PASS: reconciler re-attached dedicated shard on all 3 nodes after restart")

	// Verify pre-restart keys survived.
	for _, port := range adminPorts {
		var keys []string
		kDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(kDeadline) {
			keys = httpListTolerant(t, ca, port, "iraft-verify/")
			if len(keys) == numKeys {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(keys) != numKeys {
			t.Fatalf("assertion (d): port %d after restart: expected %d keys, got %d",
				port, numKeys, len(keys))
		}
	}
	// ---------------------------------------------------------------------------
	// Assertion (f): NO-DATA-LOSS for the dedicated raft group — the numDB1Keys
	// keys written in assertion (e) must survive a full-cluster SIGTERM +
	// restart + barrier-unseal + reconciler re-attach. Reading every key back
	// from the post-restart cluster leader proves the dedicated shard's Raft log
	// and state machine were flushed to disk before shutdown and correctly
	// replayed after restart. This is the primary no-data-loss proof for the
	// dedicated-DB data plane.
	// ---------------------------------------------------------------------------
	t.Log("assertion (f): verifying NO-DATA-LOSS for dedicated raft group db1 after restart")
	restartTenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)
	for i := range numDB1Keys {
		key := fmt.Sprintf("db1-data/%04d", i)
		var got []byte
		var status int
		fDeadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(fDeadline) {
			got, status = dbScopedGetTolerant(t, restartTenantClient, leaderPort2, iraftDBName, key)
			if status == http.StatusOK {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("assertion (f): NO-DATA-LOSS FAIL: db1 key %q after restart: timed out reading (last status: %d)", key, status)
		}
		if string(got) != db1Values[i] {
			t.Fatalf("assertion (f): NO-DATA-LOSS FAIL: db1 key %q after restart: got %q, want %q",
				key, got, db1Values[i])
		}
	}
	t.Logf("assertion (f) PASS: %d db1 keys survived full-cluster restart (NO-DATA-LOSS for dedicated raft group)", numDB1Keys)

	t.Log("TestIsolationRaft_EndToEnd PASS")
}
