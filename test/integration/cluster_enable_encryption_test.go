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

package integration_test

// TestClusterEnableEncryption_SystemScope_RF3 proves Phase D end-to-end across a
// real 3-node cluster: a system barrier that boots UNENCRYPTED (strategy "none")
// is migrated to a passphrase strategy cluster-wide via a single
// POST /v1/barrier/enable-encryption to the leader. The leader writes a
// replicated migration intent with the unlock credential HPKE-sealed per node;
// every node's reconciler opens its own sealed credential and recycles its local
// at-rest stores in place. After each node comes back up keyed-and-sealed and is
// unsealed with the new passphrase, ALL pre-migration data survives on every node
// and the barrier reports the passphrase strategy — proving rolling, per-node,
// zero-data-loss cluster encryption with no secret ever in the Raft log.
//
// Uses real qrdbd daemon processes with SPIFFE-SAN node certificates (required
// for the per-node credential sealing) and per-node volume pools.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeNoneSealClusterConfig writes a daemon config whose system barrier is
// ENABLED but UNENCRYPTED (strategy none) — the migratable source state — with
// the peer admin URL map required so the leader can fetch + authenticate each
// follower's delivery cert when sealing the per-node migration credential.
func writeNoneSealClusterConfig(t *testing.T, volumePoolDir string, peerAdminURLs map[string]string) string {
	t.Helper()
	peerAdminSection := ""
	if len(peerAdminURLs) > 0 {
		lines := "  peer_admin_urls:\n"
		for raftAddr, adminURL := range peerAdminURLs {
			lines += fmt.Sprintf("    - raft_addr: %q\n      admin_url: %q\n", raftAddr, adminURL)
		}
		peerAdminSection = "auth:\n" + lines
	}
	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: none
storage:
  volume_pools:
    - name: default
      paths:
        - %s
      strategy: round_robin
%s`, volumePoolDir, peerAdminSection)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("writeNoneSealClusterConfig: write: %v", err)
	}
	return cfgPath
}

// writeNoneSealRaftClusterConfig writes a daemon config with a none (unencrypted)
// system barrier PLUS the IsolationRaft tenant entitlements and the peer admin URL
// map — the source state for a per-tenant cluster enable-encryption test. A none
// system barrier is required so a self-managed tenant barrier can be initialized
// as none (a passphrase system barrier forces the deferred-passphrase strategy on
// tenant init, which rejects an empty secret).
func writeNoneSealRaftClusterConfig(t *testing.T, volumePoolDir string, peerAdminURLs map[string]string) string {
	t.Helper()
	peerAdminSection := ""
	if len(peerAdminURLs) > 0 {
		lines := "  peer_admin_urls:\n"
		for raftAddr, adminURL := range peerAdminURLs {
			lines += fmt.Sprintf("    - raft_addr: %q\n      admin_url: %q\n", raftAddr, adminURL)
		}
		peerAdminSection = "auth:\n" + lines
	}
	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: none
storage:
  volume_pools:
    - name: default
      paths:
        - %s
      strategy: round_robin
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - raft
    allow_dedicated_raft: true
    allow_system_managed_barrier: true
    allow_dedicated_wal: true
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
%s`, volumePoolDir, peerAdminSection)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("writeNoneSealRaftClusterConfig: write: %v", err)
	}
	return cfgPath
}

// TestClusterEnableEncryption_SystemScope_RF3 is the Phase D cluster integration
// test for the system barrier scope.
func TestClusterEnableEncryption_SystemScope_RF3(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, iraftTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	dataDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		configPaths[i] = writeNoneSealClusterConfig(t, t.TempDir(), peerAdminURLs)
	}

	startNodeR1 := func(i int) {
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
		startNode(t, bin, args, fmt.Sprintf("[cenc-node%d] ", i+1))
	}
	for i := range 3 {
		startNodeR1(i)
	}

	// Sealed-window: initialize the none barrier (empty secret) on all 3 nodes.
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
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	t.Logf("cluster healthy (none barrier), leader on admin port %d", leaderPort)

	// Confirm the source barrier is genuinely unencrypted on every node.
	for i := range 3 {
		if strategy, _, ok := barrierStatusJSON(t, ca, adminPorts[i]); !ok || strategy != "none" {
			t.Fatalf("node %d pre-migration strategy = %q (ok=%v), want none", i+1, strategy, ok)
		}
	}

	// Seed data through the Raft pipeline on the leader; replicated to all nodes.
	seed := map[string]string{"alpha": "one", "bravo": "two", "charlie": "three", "delta": "four"}
	for k, v := range seed {
		httpPut(t, ca, leaderPort, k, []byte(v))
	}
	for k, v := range seed {
		if got := string(httpGet(t, ca, leaderPort, k)); got != v {
			t.Fatalf("pre-migration %q = %q, want %q", k, got, v)
		}
	}

	// Trigger the cluster-wide system enable-encryption on the LEADER. The
	// coordinator writes the replicated intent with per-node sealed credentials.
	const newPassphrase = "cluster-enable-encryption-system-pass"
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)
	body, status := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"strategy": "passphrase", "passphrase": newPassphrase},
	)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("enable-encryption: status %d, body: %s", status, body)
	}
	t.Logf("cluster enable-encryption requested on leader (status %d)", status)

	// Each node's reconciler picks up the intent, opens its sealed credential, and
	// recycles its data plane in process — coming back up keyed-and-SEALED. Wait
	// for every node to reach the sealed state, then unseal it with the new
	// passphrase so it resumes serving.
	var unsealWg sync.WaitGroup
	unsealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer unsealWg.Done()
			waitForBarrierSealedAgain(t, ca, adminPorts[idx], 180*time.Second)
			t.Logf("node %d barrier sealed (migrated); unsealing with new passphrase", idx+1)
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			unsealBody, unsealStatus := iraftAPIPost(t, sysClient,
				fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPorts[idx]),
				map[string]interface{}{"secret": []byte(newPassphrase)},
			)
			if unsealStatus != http.StatusNoContent && unsealStatus != http.StatusConflict {
				t.Errorf("node %d post-migration unseal: status %d, body: %s", idx+1, unsealStatus, unsealBody)
			}
		}(i)
	}
	unsealWg.Wait()

	// Cluster re-forms; data must survive on every node, and the barrier must now
	// report the passphrase strategy.
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 90*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	newLeaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 90*time.Second)

	for i := range 3 {
		strategy, _, ok := barrierStatusJSON(t, ca, adminPorts[i])
		if !ok || strategy != "passphrase" {
			t.Fatalf("node %d post-migration strategy = %q (ok=%v), want passphrase", i+1, strategy, ok)
		}
	}
	for k, v := range seed {
		if got := string(httpGet(t, ca, newLeaderPort, k)); got != v {
			t.Fatalf("post-migration %q = %q, want %q (DATA LOSS)", k, got, v)
		}
	}

	// The keyed cluster still accepts new writes through the full Raft pipeline.
	httpPut(t, ca, newLeaderPort, "epsilon", []byte("five"))
	if got := string(httpGet(t, ca, newLeaderPort, "epsilon")); got != "five" {
		t.Fatalf("post-migration new write epsilon = %q, want five", got)
	}
	t.Log("Phase D system-scope RF=3 cluster enable-encryption: all data survived, barrier keyed cluster-wide")
}

// TestClusterEnableEncryption_TenantScope_RF3 proves Phase D for the TENANT
// barrier scope across a real 3-node cluster: an IsolationRaft tenant with a
// self-managed UNENCRYPTED (none) dedicated barrier and an RF=3 database is
// migrated to a passphrase barrier cluster-wide via a single
// POST /v1/barrier/enable-encryption{tenant_id} to the leader. Every node hosting
// a replica recycles its own shard ONLINE (no reboot — the tenant barrier reopens
// keyed-and-unsealed), the credential is HPKE-sealed per node, and all tenant data
// survives on every replica. The system barrier is untouched.
func TestClusterEnableEncryption_TenantScope_RF3(t *testing.T) {
	const tenantID = "rt1"
	const dbName = "tdb1"
	const tenantPass = "cluster-tenant-passphrase"

	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, iraftTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	dataDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		configPaths[i] = writeNoneSealRaftClusterConfig(t, t.TempDir(), peerAdminURLs)
	}

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
		startNode(t, bin, args, fmt.Sprintf("[tenc-node%d] ", i+1))
	}

	// Bring up the (none) system barrier on all 3 nodes.
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
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)
	t.Logf("cluster healthy, leader on admin port %d", leaderPort)

	// Create a self-managed (auto_unseal=false) IsolationRaft tenant.
	if b, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", leaderPort),
		map[string]interface{}{"id": tenantID, "name": "Raft Tenant", "isolation_type": "raft", "barrier_auto_unseal": false},
	); s != http.StatusCreated && s != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", s, b)
	}

	// The tenant's dedicated barrier must be initialized BEFORE any of its
	// IAM can exist (the tenant's roles and users are encrypted under the
	// tenant DEK; the store resolver fails closed until the barrier is up).
	// The init itself is trust-on-first-init by the tenant's own identity —
	// see pkg/api/rest authorizeTenantBarrierInit.
	tenantCert, tenantKey := ca.generateClientCertForTenant(t, tenantID)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCert, tenantKey, 15*time.Second)

	// Initialize the dedicated barrier as none (unsealed) on the leader.
	if b, s := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/init", leaderPort, tenantID),
		map[string]interface{}{"strategy": "none", "secret": []byte(nil)},
	); s != http.StatusOK && s != http.StatusNoContent && s != http.StatusCreated {
		t.Fatalf("tenant barrier init: status %d, body: %s", s, b)
	}

	// Role + user, created AFTER the barrier init so the records land in the
	// tenant's own encrypted IAM store.
	roleBody, roleStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name": "db-admin", "tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft", "databases:read", "databases:list",
				"kv:put", "kv:get",
				"tenant_barrier:status", "tenant_barrier:init", "tenant_barrier:unseal",
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
		t.Fatalf("decode role id: %v body=%s", err, roleBody)
	}
	if _, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/users", leaderPort),
		map[string]interface{}{"username": "admin", "email": "admin@" + tenantID + ".example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
	); s != http.StatusCreated && s != http.StatusOK {
		t.Fatalf("create user: status %d", s)
	}

	// Create the RF=3 database as the tenant — schedules placement on 3 nodes and
	// delivers the (self-managed) tenant barrier key to each follower.
	if b, s := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{"name": dbName, "engine": "kv", "backend": "pebble", "replicas": 3},
	); s != http.StatusCreated && s != http.StatusOK {
		t.Fatalf("create database: status %d, body: %s", s, b)
	}

	// Unseal the self-managed (none) tenant barrier on each node so its shard can
	// start. The leader is already unsealed (from init); followers receive the key
	// asynchronously (key delivery during create) and come up sealed. Poll each
	// node's status: skip when already unsealed, unseal when sealed, retry until
	// the barrier exists and is unsealed.
	for i := range 3 {
		deadline := time.Now().Add(60 * time.Second)
		for {
			strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPorts[i], tenantID)
			if ok && !sealed {
				break // already unsealed (leader, or a prior pass succeeded)
			}
			if ok && sealed {
				// The unseal endpoint requires non-empty key material (no derived
				// fallback); a none barrier ignores the value, so a dummy non-empty
				// key unseals the follower's imported none barrier.
				_, s := iraftAPIPost(t, tenantClient,
					fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/unseal", adminPorts[i], tenantID),
					map[string]interface{}{"key": []byte("none")},
				)
				if s == http.StatusNoContent || s == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("node %d tenant barrier never reached unsealed (last strategy=%q sealed=%v ok=%v)", i+1, strategy, sealed, ok)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Each node must host its system shard + the tenant's db1 replica (>=2).
	for i := range 3 {
		waitForShardCount(t, sysClient, adminPorts[i], 2, 90*time.Second)
	}

	// Write tenant data through the RF=3 dedicated shard.
	seed := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	for k, v := range seed {
		deadline := time.Now().Add(30 * time.Second)
		var st int
		var perr error
		for time.Now().Before(deadline) {
			st, perr = dbScopedPutTolerant(t, tenantClient, leaderPort, dbName, k, []byte(v))
			if perr == nil && (st == http.StatusOK || st == http.StatusNoContent || st == http.StatusCreated) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if perr != nil || (st != http.StatusOK && st != http.StatusNoContent && st != http.StatusCreated) {
			t.Fatalf("tenant put %q: status %d err %v", k, st, perr)
		}
	}

	// Confirm the tenant barrier is none on every node before migrating.
	for i := range 3 {
		if strategy, _, ok := tenantBarrierStatusJSON(t, tenantClient, adminPorts[i], tenantID); !ok || strategy != "none" {
			t.Fatalf("node %d pre-migration tenant strategy = %q (ok=%v), want none", i+1, strategy, ok)
		}
	}

	// Trigger the cluster-wide TENANT enable-encryption on the leader.
	if b, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"tenant_id": tenantID, "strategy": "passphrase", "passphrase": tenantPass},
	); s != http.StatusOK && s != http.StatusAccepted {
		t.Fatalf("tenant enable-encryption: status %d, body: %s", s, b)
	}
	t.Log("tenant cluster enable-encryption requested on leader")

	// Every node's reconciler recycles its tenant shard ONLINE; the tenant barrier
	// transitions to passphrase and stays UNSEALED (no re-unseal needed).
	for i := range 3 {
		deadline := time.Now().Add(180 * time.Second)
		migrated := false
		for time.Now().Before(deadline) {
			if strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPorts[i], tenantID); ok && strategy == "passphrase" && !sealed {
				migrated = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !migrated {
			t.Fatalf("node %d tenant barrier did not migrate to unsealed passphrase within timeout", i+1)
		}
		t.Logf("node %d tenant barrier migrated to passphrase (online)", i+1)
	}

	// The system barrier is untouched on every node (still none, unsealed).
	for i := range 3 {
		if strategy, sealed, ok := barrierStatusJSON(t, ca, adminPorts[i]); !ok || strategy != "none" || sealed {
			t.Fatalf("node %d system barrier changed by tenant migration: strategy=%q sealed=%v", i+1, strategy, sealed)
		}
	}

	// All tenant data survives the per-shard recycle, readable on the leader.
	newLeaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	for k, v := range seed {
		var data []byte
		var st int
		rd := time.Now().Add(60 * time.Second)
		for time.Now().Before(rd) {
			data, st = dbScopedGetTolerant(t, tenantClient, newLeaderPort, dbName, k)
			if st == http.StatusOK {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if st != http.StatusOK || string(data) != v {
			t.Fatalf("post-migration tenant %q = %q (status %d), want %q (DATA LOSS)", k, string(data), st, v)
		}
	}
	t.Log("Phase D tenant-scope RF=3 cluster enable-encryption: all tenant data survived, tenant barrier keyed on all replicas")
}
