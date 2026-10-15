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

// TestIsolationRaftDedicatedWAL_EndToEnd proves the 4d Option C per-tenant-KEY
// WAL feature end-to-end across a real 3-node cluster, alongside a shared-WAL
// (Route A) database to prove the fallback is unaffected:
//
//   - A dedicated-raft database "wal1" created with dedicatedWal=true gets a
//     per-tenant WAL store under the tenant DataRoot ({DataRoot}/databases/wal1/wal)
//     on EVERY replica node (leader + followers materialized by the reconciler).
//
//   - A dedicated-raft database "shared1" created with dedicatedWal=false keeps
//     using the shared system-barrier WAL: no per-tenant WAL directory is created
//     for it (Route A fallback, byte-identical to before).
//
//   - The dedicated WAL's on-disk bytes are encrypted under the TENANT barrier:
//     the plaintext values written to wal1 never appear in the dedicated WAL
//     files (per-tenant-key isolation at rest).
//
//   - Data written to the dedicated-WAL group replicates to all nodes and
//     survives a full-cluster SIGTERM + restart + barrier-unseal + reconciler
//     re-attach (no data loss for the per-tenant-KEY WAL group).
//
// This test reuses the SPIFFE-SAN node cert / volume-pool / entitlement fixtures
// from the Phase-4c IsolationRaft test (writeIsolationRaftConfig grants the
// dedicated_wal entitlement and an unlimited MaxDedicatedWALGroups quota).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const dwTenantID = "t1"

// TestIsolationRaftDedicatedWAL_EndToEnd is the Phase-3 Stage-B per-tenant-WAL
// end-to-end integration test.
func TestIsolationRaftDedicatedWAL_EndToEnd(t *testing.T) {
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
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, iraftTrustDomain)
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
				"--cluster-trust-domain", iraftTrustDomain,
				"--tls-ca-file", ca.CertPath,
				"--tls-cert-file", nodeCertPaths[i],
				"--tls-key-file", nodeKeyPaths[i],
			}
			cmd := spawnNode(t, bin, args, &logWriter{t: t, prefix: fmt.Sprintf("[node%d-%s] ", i+1, tag)})
			cmds[i] = cmd
			t.Cleanup(func() {
				killNode(t, cmd)
			})
		}
		return cmds
	}

	const barrierPassphrase = "integration-test-barrier-secret"

	unsealAll := func(initialize bool) {
		var wg sync.WaitGroup
		wg.Add(3)
		for i := range 3 {
			go func(idx int) {
				defer wg.Done()
				waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
				if initialize {
					initAndUnsealBarrier(t, ca, adminPorts[idx], barrierPassphrase)
					return
				}
				_, st := iraftAPIPost(t, mtlsHTTPClient(t, ca, 10*time.Second),
					fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPorts[idx]),
					map[string]interface{}{"secret": []byte(barrierPassphrase)})
				if st != http.StatusNoContent {
					t.Errorf("unseal node %d: status %d", idx+1, st)
				}
			}(i)
		}
		wg.Wait()
	}

	// ---------------------------------------------------------------------------
	// Phase 1: start + unseal.
	// ---------------------------------------------------------------------------
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

	// ---------------------------------------------------------------------------
	// Phase 2: tenant + role + user.
	// ---------------------------------------------------------------------------
	adminKeyPEM, tBody, tStatus := createRaftTenant(t, sysClient, leaderPort, dwTenantID, "Test Tenant 1")
	if tStatus != http.StatusCreated && tStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d", tStatus)
	}

	// The system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so this authenticates
	// as the tenant's own onboarded admin; its built-in Admin role cannot
	// create a NEW role (RolesCreate is SO-exclusive), so it self-assigns
	// its own pre-seeded SO role first.
	raftAdminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tBody)
	selfAssignTenantSO(t, leaderPort, dwTenantID, raftAdminClient)
	roleBody, rStatus := iraftAPIPost(t, raftAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name": "db-admin", "tenant_id": dwTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft",
				"databases:read", "databases:list", "kv:put", "kv:get",
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
	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, dwTenantID)
	createBoundUser(t, raftAdminClient, leaderPort,
		map[string]interface{}{
			"username": "admin", "email": "admin@" + dwTenantID + ".example.com", "tenant_id": dwTenantID,
			"role_ids": []string{roleResp.ID},
		}, tenantCertPath)

	// ---------------------------------------------------------------------------
	// Phase 3: create wal1 (dedicatedWal=true) and shared1 (dedicatedWal=false).
	// ---------------------------------------------------------------------------
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)

	createDB := func(name string, dedicated bool) {
		body, status := iraftAPIPost(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
			map[string]interface{}{
				"name": name, "backend": "pebble",
				"replicas": 3, "dedicatedWal": dedicated,
			})
		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("create database %q: status %d, body: %s", name, status, body)
		}
	}
	createDB("wal1", true)
	createDB("shared1", false)

	// Confirm wal1 reports DedicatedWAL=true via the API.
	var wal1Info struct {
		ShardID      uint64 `json:"shardId"`
		DedicatedWAL bool   `json:"dedicatedWal"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, st := iraftAPIGet(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/wal1", leaderPort))
		if st == http.StatusOK && json.Unmarshal(data, &wal1Info) == nil && wal1Info.ShardID != 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !wal1Info.DedicatedWAL {
		t.Fatalf("wal1 must report dedicatedWal=true (shardID=%d)", wal1Info.ShardID)
	}

	// ---------------------------------------------------------------------------
	// Wait for all dedicated shards to start on all 3 nodes: system(1) + wal1 +
	// shared1 = 3 shards per node.
	// ---------------------------------------------------------------------------
	for i, port := range adminPorts {
		t.Logf("waiting for shard count >= 3 on node %d", i+1)
		waitForShardCount(t, sysClient, port, 3, 120*time.Second)
	}

	// ---------------------------------------------------------------------------
	// Phase 4: write data to both databases.
	// ---------------------------------------------------------------------------
	const numKeys = 15
	walValues := make([]string, numKeys)
	sharedValues := make([]string, numKeys)
	for i := range numKeys {
		walValues[i] = fmt.Sprintf("WAL1-SECRET-VALUE-%d", i)
		sharedValues[i] = fmt.Sprintf("shared1-val-%d", i)
	}

	writeAll := func(dbName string, vals []string) {
		wDeadline := time.Now().Add(120 * time.Second)
		for i := range numKeys {
			key := fmt.Sprintf("%s/%04d", dbName, i)
			var ok bool
			for time.Now().Before(wDeadline) {
				st, err := dbScopedPutTolerant(t, tenantClient, leaderPort, dbName, key, []byte(vals[i]))
				if err == nil && st == http.StatusNoContent {
					ok = true
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if !ok {
				t.Fatalf("timed out writing %s key %q", dbName, key)
			}
			if i == 0 {
				wDeadline = time.Now().Add(30 * time.Second)
			}
		}
	}
	writeAll("wal1", walValues)
	writeAll("shared1", sharedValues)

	readBackAll := func(client *http.Client, port int, dbName string, vals []string) {
		for i := range numKeys {
			key := fmt.Sprintf("%s/%04d", dbName, i)
			var got []byte
			var st int
			rDeadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(rDeadline) {
				got, st = dbScopedGetTolerant(t, client, port, dbName, key)
				if st == http.StatusOK {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if st != http.StatusOK {
				t.Fatalf("%s key %q read timed out (status %d)", dbName, key, st)
			}
			if string(got) != vals[i] {
				t.Fatalf("%s key %q = %q, want %q", dbName, key, got, vals[i])
			}
		}
	}
	readBackAll(tenantClient, leaderPort, "wal1", walValues)
	readBackAll(tenantClient, leaderPort, "shared1", sharedValues)

	// ---------------------------------------------------------------------------
	// Assertion (a): the dedicated WAL directory exists on EVERY node under the
	// tenant DataRoot for wal1, and NO such directory exists for shared1.
	// ---------------------------------------------------------------------------
	for i := range 3 {
		walDir := findDirEndingIn(t, volumePoolDirs[i],
			filepath.Join("databases", "wal1", "wal"))
		if walDir == "" {
			t.Fatalf("assertion (a): node %d: dedicated WAL dir for wal1 not found under %s",
				i+1, volumePoolDirs[i])
		}
		empty, err := dirIsEmpty(walDir)
		if err != nil {
			t.Fatalf("assertion (a): node %d: dirIsEmpty(%q): %v -- the directory read failed, so this never established that a WAL was written",
				i+1, walDir, err)
		}
		if empty {
			t.Fatalf("assertion (a): node %d: dedicated WAL dir %q is empty (no WAL written)", i+1, walDir)
		}
		// shared1 must NOT have a per-tenant WAL directory (it uses the shared WAL).
		if sharedWALDir := findDirEndingIn(t, volumePoolDirs[i],
			filepath.Join("databases", "shared1", "wal")); sharedWALDir != "" {
			t.Fatalf("assertion (a): node %d: shared1 must NOT have a dedicated WAL dir, found %q",
				i+1, sharedWALDir)
		}
		t.Logf("assertion (a) node %d PASS: wal1 dedicated WAL at %q; shared1 uses shared WAL", i+1, walDir)
	}

	// ---------------------------------------------------------------------------
	// Assertion (b): per-tenant-key isolation at rest — the plaintext values
	// written to wal1 must NOT appear anywhere in the dedicated WAL files (they
	// are encrypted under the tenant barrier, not the system barrier).
	// ---------------------------------------------------------------------------
	for i := range 3 {
		walDir := findDirEndingIn(t, volumePoolDirs[i], filepath.Join("databases", "wal1", "wal"))
		for _, v := range walValues {
			assertPlaintextAbsentInDir(t, walDir, []byte(v))
		}
		t.Logf("assertion (b) node %d PASS: wal1 plaintext absent from dedicated WAL bytes", i+1)
	}

	// ---------------------------------------------------------------------------
	// Phase 5: full-cluster SIGTERM.
	// ---------------------------------------------------------------------------
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("signal node %d: %v", i+1, err)
		}
	}
	for _, cmd := range cmds {
		_ = awaitNodeExit(t, cmd, nodeStopTimeout)
	}

	// ---------------------------------------------------------------------------
	// Phase 6: restart + re-unseal + re-attach.
	// ---------------------------------------------------------------------------
	startCluster("R2")
	unsealAll(false)
	for i := range 3 {
		waitForHealthy(t, dataDirs[i], 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort2 := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	restartClient := mtlsHTTPClient(t, ca, 15*time.Second)
	for i, port := range adminPorts {
		t.Logf("waiting for shard count >= 3 on restarted node %d", i+1)
		waitForShardCount(t, restartClient, port, 3, 120*time.Second)
	}

	// ---------------------------------------------------------------------------
	// Assertion (c): NO-DATA-LOSS — both the dedicated-WAL group and the shared
	// group survive the restart with all values intact.
	// ---------------------------------------------------------------------------
	restartTenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)
	readBackAll(restartTenantClient, leaderPort2, "wal1", walValues)
	readBackAll(restartTenantClient, leaderPort2, "shared1", sharedValues)
	t.Logf("assertion (c) PASS: wal1 (%d keys) + shared1 (%d keys) survived full-cluster restart", numKeys, numKeys)

	t.Log("TestIsolationRaftDedicatedWAL_EndToEnd PASS")
}

// findDirEndingIn walks root and returns the first directory whose path ends in
// suffix, or "" when none is found.
func findDirEndingIn(t *testing.T, root, suffix string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() && strings.HasSuffix(path, string(os.PathSeparator)+suffix) {
			found = path
		}
		return nil
	})
	return found
}

// dirIsEmpty reports whether dir contains no entries.
func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// assertPlaintextAbsentInDir fails the test if needle appears in the raw bytes
// of any file under dir, proving the dir's contents are encrypted at rest.
func assertPlaintextAbsentInDir(t *testing.T, dir string, needle []byte) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(data, needle) {
			t.Fatalf("plaintext %q found in WAL file %q (not encrypted at rest)", needle, path)
		}
		return nil
	})
}
