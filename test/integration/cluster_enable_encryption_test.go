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
// replicated migration intent that carries no credential, and the owners
// recycle one at a time: each other node, in its turn, waits in
// awaiting-credential until its own credential file appears under
// seal.migration_credentials_dir, then recycles its local at-rest stores in
// place; the leader goes last, with the request's passphrase held in memory.
// After each node comes back up keyed-and-sealed and is unsealed with
// the new passphrase, ALL pre-migration data survives on every node and the
// barrier reports the passphrase strategy — proving rolling, per-node,
// zero-data-loss cluster encryption with no secret in the Raft log and none
// sent between nodes.
//
// Uses real qrdbd daemon processes with SPIFFE-SAN node certificates and
// per-node volume pools.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// writeNoneSealClusterConfig writes a daemon config whose system barrier is
// ENABLED but UNENCRYPTED (strategy none) — the migratable source state — with
// the peer admin URL map and this node's migration credentials directory.
func writeNoneSealClusterConfig(t *testing.T, volumePoolDir, credDir string, peerAdminURLs map[string]string) string {
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
  migration_credentials_dir: %q
storage:
  volume_pools:
    - name: default
      path: %s
%s`, credDir, volumePoolDir, peerAdminSection)

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
func writeNoneSealRaftClusterConfig(t *testing.T, volumePoolDir, credDir string, peerAdminURLs map[string]string) string {
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
  migration_credentials_dir: %q
storage:
  volume_pools:
    - name: default
      path: %s
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - raft
    allow_dedicated_raft: true
    allow_system_managed_barrier: true
    allow_dedicated_wal: true
    allowed_volume_pools:
      - default
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
%s`, credDir, volumePoolDir, peerAdminSection)

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
	c := startNoneSealSystemRF3(t, "cenc")
	ca, adminPorts, dataDirs, credDirs, leaderPort := c.ca, c.adminPorts, c.dataDirs, c.credDirs, c.leaderPort

	// Confirm the source barrier is genuinely unencrypted on every node.
	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 30*time.Second); got.Sealed || got.Strategy != "none" {
			t.Fatalf("node %d pre-migration barrier: %s, want unsealed none", i+1, got)
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

	// The rolling-migration gate is active before the request, so the
	// coordinator writes the owners' steps and recycles them one at a time.
	waitForFeatureActive(t, c.logs, featuregate.RollingMigration, 90*time.Second)

	// Trigger the cluster-wide system enable-encryption on the LEADER. The
	// coordinator writes the replicated intent, which carries no credential;
	// the leader holds the request's passphrase for its own recycle. No node
	// has a credential file yet.
	const newPassphrase = "cluster-enable-encryption-system-pass"
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)
	body, status := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"strategy": "passphrase", "passphrase": []byte(newPassphrase)},
	)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("enable-encryption: status %d, body: %s", status, body)
	}
	t.Logf("cluster enable-encryption requested on leader (status %d)", status)

	// Each owner in its turn: a node with no credential file waits for it,
	// still serving the none barrier, until it gets its own; the node that
	// accepted the request recycles last with the request's credential and
	// never gets one. Each recycled node comes back keyed-and-SEALED and is
	// unsealed with the new passphrase, which completes its turn.
	holderIdx := walkCredentialTurns(t, credentialTurns{
		logs:  c.logs,
		scope: "scope=system",
		migrated: func(i int) bool {
			return readBarrierStatus(t, ca, adminPorts[i]).Sealed
		},
		untouched: func(i int) error {
			if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 30*time.Second); got.Sealed || got.Strategy != "none" {
				return fmt.Errorf("barrier %s, want unsealed none", got)
			}
			return nil
		},
		credit: func(i int) { writeMigrationCredential(t, credDirs[i], "system", newPassphrase) },
		settle: func(i int) { unsealRolling(t, sysClient, adminPorts[i], newPassphrase) },
	}, 180*time.Second)
	t.Logf("node %d recycled last with the request's held credential", holderIdx+1)

	// Cluster re-forms; data must survive on every node, and the barrier must now
	// report the passphrase strategy.
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 90*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	newLeaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 90*time.Second)

	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 30*time.Second); got.Sealed || got.Strategy != "passphrase" {
			t.Fatalf("node %d post-migration barrier: %s, want unsealed passphrase", i+1, got)
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

// TestClusterEnableEncryption_TenantScope_RF3 proves two strict IsolationRaft
// tenants' barriers on a real 3-node cluster, each served by every node
// holding a replica of the tenant's home group, each node with its own key
// and none ever sent between them.
//
// The first tenant's admin initializes a passphrase barrier through ONE node
// with the real CLI, which sends the init to every node the barrier status
// lists, and the home group forms with no key delivered and no node unsealed
// by hand. An RF=3 database then serves the tenant's data, the tenant's own
// seal through one follower reaches every replica, and each replica unseals
// with the tenant's passphrase on that replica with all the data intact.
//
// The second tenant's self-managed UNENCRYPTED (none) barrier and RF=3
// database are migrated to a passphrase barrier cluster-wide via a single
// POST /v1/barrier/enable-encryption{tenant_id} to the leader. Every replica
// recycles its own shard ONLINE in its turn (no reboot — the tenant barrier
// reopens keyed-and-unsealed), with its own credential -- its own file on
// every other node, then the request's on the leader, last -- and all tenant
// data survives on every replica. The system barrier is untouched.
func TestClusterEnableEncryption_TenantScope_RF3(t *testing.T) {
	const tenantID = "rt1"
	const dbName = "tdb1"
	const migTenantID = "rt2"
	const migDBName = "tdb2"
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
	credDirs := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		credDirs[i] = migrationCredentialsDir(t)
		configPaths[i] = writeNoneSealRaftClusterConfig(t, t.TempDir(), credDirs[i], peerAdminURLs)
	}

	logs := make([]*nodeLog, 3)
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
		logs[i] = &nodeLog{}
		logs[i].cmd = startNode(t, bin, args, fmt.Sprintf("[tenc-node%d] ", i+1), logs[i])
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

	createVolumePoolREST(t, sysClient, leaderPort, "default")
	waitForVolumePoolReady(t, sysClient, leaderPort, "default", len(dataDirs), 60*time.Second)

	// The passphrase tenant: its first admin initializes the barrier as
	// passphrase through the leader alone. The CLI sends the init to every
	// node serving the barrier, and the home group forms once they hold their
	// keys, each node then running the system shard and the tenant's home
	// replica.
	cli := buildCLIBinary(t)
	tenantClient := onboardRaftTenantDBAdmin(t, ca, sysClient, leaderPort, adminPorts, tenantID,
		func(admin *qrdbdharness.KeyPair) {
			fanOutPassphraseBarrierInit(t, cli, leaderPort, ca.CertPath,
				admin.CertPEM, admin.KeyPEM, tenantID, tenantPass, raftPorts)
			for i := range 3 {
				waitForShardCount(t, sysClient, adminPorts[i], 2, 90*time.Second)
			}
		})

	// Create the RF=3 database as the tenant. Its replicas start on nodes
	// already holding the tenant's key: nothing is delivered to them. Each
	// node then hosts its system shard, the tenant's home replica and tdb1's.
	seed := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	createSeededRF3Database(t, tenantClient, leaderPort, dbName, seed)
	for i := range 3 {
		waitForShardCount(t, sysClient, adminPorts[i], 3, 90*time.Second)
	}

	// The tenant barrier is passphrase and unsealed on every node.
	for i := range 3 {
		if strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPorts[i], tenantID); !ok || strategy != "passphrase" || sealed {
			t.Fatalf("node %d tenant barrier: strategy=%q sealed=%v ok=%v, want unsealed passphrase", i+1, strategy, sealed, ok)
		}
	}

	// A tenant's barrier is migrated under tenant_barrier:enable_encryption
	// for THAT tenant (go-qrdb#791): the system root, outside the tenant, is
	// refused before anything else is decided.
	if b, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"tenant_id": tenantID, "strategy": "passphrase", "passphrase": []byte(tenantPass)},
	); s != http.StatusForbidden {
		t.Fatalf("system-root tenant enable-encryption: status %d, want 403; body: %s", s, b)
	}

	// The migrated tenant: a self-managed UNENCRYPTED (none) barrier, its
	// first admin's init sent to the leader, from which every other node
	// initializes its own, with an RF=3 database of its own.
	migTenantClient := onboardRaftTenantDBAdmin(t, ca, sysClient, leaderPort, adminPorts, migTenantID,
		func(admin *qrdbdharness.KeyPair) {
			initTenantBarrierAsFirstAdmin(t, fmt.Sprintf("https://127.0.0.1:%d", leaderPort), ca.CertPath,
				admin.CertPEM, admin.KeyPEM, migTenantID)
			for i := range 3 {
				waitForShardCount(t, sysClient, adminPorts[i], 4, 90*time.Second)
			}
		})
	migSeed := map[string]string{"m1": "w1", "m2": "w2", "m3": "w3"}
	createSeededRF3Database(t, migTenantClient, leaderPort, migDBName, migSeed)
	for i := range 3 {
		waitForShardCount(t, sysClient, adminPorts[i], 5, 90*time.Second)
	}
	for i := range 3 {
		var strategy string
		var sealed, ok bool
		if !pollUntil(60*time.Second, 500*time.Millisecond, func() bool {
			strategy, sealed, ok = tenantBarrierStatusJSON(t, migTenantClient, adminPorts[i], migTenantID)
			return ok && strategy == "none" && !sealed
		}) {
			t.Fatalf("node %d pre-migration tenant barrier: strategy=%q sealed=%v ok=%v, want unsealed none", i+1, strategy, sealed, ok)
		}
	}

	// The rolling-migration gate is active before the request, so the
	// coordinator writes the replicas' steps and recycles them one at a time.
	waitForFeatureActive(t, logs, featuregate.RollingMigration, 90*time.Second)

	// Trigger the cluster-wide TENANT enable-encryption on the leader. A
	// tenant's barrier is migrated under tenant_barrier:enable_encryption for
	// THAT tenant (go-qrdb#791): the system root, outside the tenant, is
	// refused, and the tenant's own identity holding the permission migrates.
	if b, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"tenant_id": migTenantID, "strategy": "passphrase", "passphrase": []byte(tenantPass)},
	); s != http.StatusForbidden {
		t.Fatalf("system-root tenant enable-encryption: status %d, want 403; body: %s", s, b)
	}
	if b, s := iraftAPIPost(t, migTenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", leaderPort),
		map[string]interface{}{"tenant_id": migTenantID, "strategy": "passphrase", "passphrase": []byte(tenantPass)},
	); s != http.StatusOK && s != http.StatusAccepted {
		t.Fatalf("tenant enable-encryption: status %d, body: %s", s, b)
	}
	t.Log("tenant cluster enable-encryption requested on leader")

	// Each replica in its turn: a node with no credential file waits for it,
	// its tenant barrier still none, until it gets its own; the node that
	// accepted the request recycles last with the request's credential and
	// never gets one. Every node recycles its tenant shard ONLINE: the tenant
	// barrier transitions to passphrase and stays UNSEALED (no re-unseal).
	tenantHolderIdx := walkCredentialTurns(t, credentialTurns{
		logs:  logs,
		scope: "tenant=" + migTenantID + " ",
		migrated: func(i int) bool {
			strategy, sealed, ok := tenantBarrierStatusJSON(t, migTenantClient, adminPorts[i], migTenantID)
			return ok && strategy == "passphrase" && !sealed
		},
		untouched: func(i int) error {
			if strategy, _, ok := tenantBarrierStatusJSON(t, migTenantClient, adminPorts[i], migTenantID); !ok || strategy != "none" {
				return fmt.Errorf("tenant barrier strategy %q (ok=%v), want none", strategy, ok)
			}
			return nil
		},
		credit: func(i int) { writeMigrationCredential(t, credDirs[i], "tenant-"+migTenantID, tenantPass) },
		settle: func(i int) { t.Logf("node %d tenant barrier migrated to passphrase (online)", i+1) },
	}, 180*time.Second)
	t.Logf("node %d recycled last with the request's held credential", tenantHolderIdx+1)

	// The system barrier is untouched on every node (still none, unsealed).
	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 30*time.Second); got.Sealed || got.Strategy != "none" {
			t.Fatalf("node %d system barrier changed by tenant migration: %s", i+1, got)
		}
	}

	// All the migrated tenant's data survives the per-shard recycle, readable
	// on the leader.
	newLeaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	assertTenantData(t, migTenantClient, newLeaderPort, migDBName, migSeed, "post-migration")
	t.Log("Phase D tenant-scope RF=3 cluster enable-encryption: all tenant data survived, tenant barrier keyed on all replicas")

	// Cluster sealing (docs/barrier-seal): the tenant's own admin seals its
	// barrier through ONE node, a follower, and the seal reaches every
	// replica. Each replica is then unsealed with the tenant's passphrase on
	// that replica; the first unseal clears the cluster record.
	sealIdx := (adminPortIndex(t, adminPorts, newLeaderPort) + 1) % 3
	if b, s := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/seal", adminPorts[sealIdx], tenantID),
		map[string]interface{}{},
	); s != http.StatusOK && s != http.StatusNoContent {
		t.Fatalf("tenant barrier seal via node %d: status %d, body: %s", sealIdx+1, s, b)
	}
	for i := range 3 {
		if !pollUntil(30*time.Second, 250*time.Millisecond, func() bool {
			_, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPorts[i], tenantID)
			return ok && sealed
		}) {
			t.Fatalf("node %d: the tenant's seal through node %d did not reach it", i+1, sealIdx+1)
		}
		if _, st := dbScopedGetTolerant(t, tenantClient, adminPorts[i], dbName, "k1"); st == http.StatusOK {
			t.Fatalf("node %d served the sealed tenant's data", i+1)
		}
	}
	t.Logf("tenant seal through node %d reached every replica", sealIdx+1)
	for i := range 3 {
		if b, s := iraftAPIPost(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/unseal", adminPorts[i], tenantID),
			map[string]interface{}{"key": []byte(tenantPass)},
		); s != http.StatusOK && s != http.StatusNoContent {
			t.Fatalf("node %d tenant unseal: status %d, body: %s", i+1, s, b)
		}
	}
	finalLeader := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	assertTenantData(t, tenantClient, finalLeader, dbName, seed, "after the tenant unseal")
}

// onboardRaftTenantDBAdmin creates the self-managed (auto_unseal=false)
// IsolationRaft tenant tenantID with its first admin, has initBarrier
// initialize the tenant's barrier as that admin, and returns a client for the
// tenant's generic identity holding a role with the database, KV and tenant
// barrier permissions.
//
// A tenant is created with its first admin, or not at all. The admin brings
// its own certificate: these BYO-TLS nodes have no cluster CA to sign a CSR
// with, and the tenant's own CA is behind the barrier that admin has yet to
// initialize. Each node admits the admin's certificate only once the tenant's
// trust anchor has replicated to it.
func onboardRaftTenantDBAdmin(t *testing.T, ca *testCA, sysClient *http.Client, leaderPort int, adminPorts []int,
	tenantID string, initBarrier func(admin *qrdbdharness.KeyPair),
) *http.Client {
	t.Helper()
	createReq, admin := adminBYOCFieldsForTenant(t, tenantID, tenantID+"-admin")
	createReq["id"], createReq["name"] = tenantID, "Raft Tenant "+tenantID
	createReq["isolation_type"], createReq["barrier_auto_unseal"] = "raft", false
	createBody, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", leaderPort), createReq)
	if s != http.StatusCreated && s != http.StatusOK {
		t.Fatalf("create tenant %s: status %d, body: %s", tenantID, s, createBody)
	}

	// The tenant's dedicated barrier must be initialized BEFORE any of its
	// IAM can exist (the tenant's roles and users are encrypted under the
	// tenant DEK; the store resolver fails closed until the barrier is up).
	// The init itself is trust-on-first-init by the EXACT certificate the
	// tenant's first admin was onboarded with above, never any other
	// authenticated identity of the tenant — see pkg/api/rest
	// authorizeTenantBarrierInit.
	adminTLSCert, err := admin.TLSCertificate()
	if err != nil {
		t.Fatalf("key pair for the onboarded admin of %s: %v", tenantID, err)
	}
	onboardedAdminClient := qrdbdharness.HTTPSClient(ca.Pool, &adminTLSCert, 15*time.Second)
	for _, port := range adminPorts {
		waitForNodeToTrustClient(t, onboardedAdminClient, port, 30*time.Second)
	}
	initBarrier(admin)

	// The onboarded admin's account carries only the pending-admin
	// completion's own role, not the data-plane permissions the rest of
	// the test needs: the tenant's generic (CN=admin, OU=tenantID)
	// identity, provisioned below with its own role, drives everything
	// from here on -- the same two-identity split
	// tenant_barrier_sealed_window_cli_test.go uses for the identical
	// reason.
	tenantCert, tenantKey := ca.generateClientCertForTenant(t, tenantID)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCert, tenantKey, 15*time.Second)

	// Role + user, created AFTER the barrier init so the records land in the
	// tenant's own encrypted IAM store. The system tenant can no longer
	// provision records inside another tenant's IAM at all (owner ruling,
	// 2026-10-01), so this authenticates as the tenant's own onboarded
	// admin; its built-in Admin role cannot create a NEW role (RolesCreate
	// is SO-exclusive), so it self-assigns its own pre-seeded SO role
	// first, exactly like tenant_isolation_cli_test.go's
	// provisionTenantIdentityFor.
	selfAssignTenantSO(t, leaderPort, tenantID, onboardedAdminClient)
	roleBody, roleStatus := iraftAPIPost(t, onboardedAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name": "db-admin", "tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft", "databases:read", "databases:list",
				"kv:put", "kv:get",
				"tenant_barrier:status", "tenant_barrier:init", "tenant_barrier:unseal",
				"tenant_barrier:seal", "tenant_barrier:enable_encryption",
			},
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role in %s: status %d, body: %s", tenantID, roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("decode role id: %v body=%s", err, roleBody)
	}
	createBoundUser(t, onboardedAdminClient, leaderPort,
		map[string]interface{}{"username": "admin", "email": "admin@" + tenantID + ".example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
		tenantCert)
	return tenantClient
}

// createSeededRF3Database creates the RF=3 pebble database dbName as the
// tenant behind tenantClient and writes seed through it.
func createSeededRF3Database(t *testing.T, tenantClient *http.Client, leaderPort int, dbName string, seed map[string]string) {
	t.Helper()
	if b, s := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{"name": dbName, "backend": "pebble", "replicas": 3},
	); s != http.StatusCreated && s != http.StatusOK {
		t.Fatalf("create database %s: status %d, body: %s", dbName, s, b)
	}
	for k, v := range seed {
		var st int
		var perr error
		pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
			st, perr = dbScopedPutTolerant(t, tenantClient, leaderPort, dbName, k, []byte(v))
			return perr == nil && (st == http.StatusOK || st == http.StatusNoContent || st == http.StatusCreated)
		})
		if perr != nil || (st != http.StatusOK && st != http.StatusNoContent && st != http.StatusCreated) {
			t.Fatalf("tenant put %s/%q: status %d err %v", dbName, k, st, perr)
		}
	}
}

// assertTenantData fails the test unless every key of seed reads back its
// value from dbName through the node on port; when names the check.
func assertTenantData(t *testing.T, tenantClient *http.Client, port int, dbName string, seed map[string]string, when string) {
	t.Helper()
	for k, v := range seed {
		var data []byte
		var st int
		pollUntil(60*time.Second, 300*time.Millisecond, func() bool {
			data, st = dbScopedGetTolerant(t, tenantClient, port, dbName, k)
			return st == http.StatusOK
		})
		if st != http.StatusOK || string(data) != v {
			t.Fatalf("%s %s/%q = %q (status %d), want %q (DATA LOSS)", when, dbName, k, string(data), st, v)
		}
	}
}

// fanOutPassphraseBarrierInit initializes tenantID's barrier as passphrase
// with the real CLI, as the first admin, through the node on adminPort, and
// asserts the CLI reports the init accepted by the node on every one of
// raftPorts.
func fanOutPassphraseBarrierInit(t *testing.T, cli string, adminPort int, caPath string, certPEM, keyPEM []byte, tenantID, passphrase string, raftPorts []int) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "admin-cert.pem"), filepath.Join(dir, "admin-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, cli,
		"--server", fmt.Sprintf("https://127.0.0.1:%d", adminPort),
		"--ca-file", caPath, "--cert-file", certPath, "--key-file", keyPath, "--timeout", "60s",
		"tenant", "barrier-init", "--id", tenantID, "--strategy", "passphrase", "--passphrase", passphrase)
	if err != nil {
		t.Fatalf("tenant barrier-init through node on %d: %v\noutput: %s", adminPort, err, out)
	}
	for _, port := range raftPorts {
		if want := fmt.Sprintf("127.0.0.1:%d: OK", port); !strings.Contains(out, want) {
			t.Fatalf("tenant barrier-init output lacks %q:\n%s", want, out)
		}
	}
}

// adminPortIndex returns the index of port in ports.
// noneSealRF3 is a running three-node cluster whose system barrier is
// initialized with the none strategy, each node with its own, still empty,
// migration-credentials directory. Every node serves every transport, so a
// test can drive the qrdb CLI over any of them (nodes, adminCert, adminKey).
type noneSealRF3 struct {
	ca         *testCA
	adminPorts []int
	dataDirs   []string
	credDirs   []string
	leaderPort int
	nodes      []gatedClusterNode
	logs       []*nodeLog
	adminCert  string
	adminKey   string
	// name, bin and args are the cluster's log prefix and each node's binary
	// and arguments, to start a node again.
	name string
	bin  string
	args [][]string
}

// stop stops node i, giving it grace to shut down.
func (c *noneSealRF3) stop(t *testing.T, i int) {
	t.Helper()
	stopNode(t, c.logs[i].cmd, 15*time.Second)
	c.logs[i].cmd = nil
}

// restart starts node i again, after stop, with the arguments it first
// started with, and unseals it if it came back sealed. Its log keeps what it
// logged before the stop.
func (c *noneSealRF3) restart(t *testing.T, i int) {
	t.Helper()
	c.logs[i].cmd = startNode(t, c.bin, c.args[i], fmt.Sprintf("[%s-node%d] ", c.name, i+1), c.logs[i])
	unsealNodes(t, c.ca, c.adminPorts[i])
	waitForHealthy(t, c.dataDirs[i], 60*time.Second)
	waitForAdmin(t, c.ca.Pool, c.adminPorts[i], 90*time.Second)
}

// startNoneSealSystemRF3 starts three nodes on one system shard, initializes
// and unseals the none barrier on each, and returns once a leader has taken a
// write. name prefixes each node's log lines.
func startNoneSealSystemRF3(t *testing.T, name string) *noneSealRF3 {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	mcpPorts := allocateTCPPorts(t, 3)
	quicPorts := allocatePorts(t, 3)
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
	credDirs := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		credDirs[i] = migrationCredentialsDir(t)
		configPaths[i] = writeNoneSealClusterConfig(t, t.TempDir(), credDirs[i], peerAdminURLs)
	}

	nodes := make([]gatedClusterNode, 3)
	logs := make([]*nodeLog, 3)
	argv := make([][]string, 3)
	for i := range 3 {
		sockDir, err := os.MkdirTemp("", "qrdb-rf3-")
		if err != nil {
			t.Fatalf("socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		nodes[i] = gatedClusterNode{admin: adminPorts[i], grpcPort: grpcPorts[i], quicPort: quicPorts[i],
			mcpPort: mcpPorts[i], unixSock: filepath.Join(sockDir, "qrdbd.sock")}
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
			"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--enable-unix", "--unix-socket-path", nodes[i].unixSock,
			"--cluster-trust-domain", iraftTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		argv[i] = args
		logs[i] = &nodeLog{}
		logs[i].cmd = startNode(t, bin, args, fmt.Sprintf("[%s-node%d] ", name, i+1), logs[i])
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
	adminCert, adminKey := ca.boundAdminCert(t, dataDirs[0])
	return &noneSealRF3{ca: ca, adminPorts: adminPorts, dataDirs: dataDirs, credDirs: credDirs, leaderPort: leaderPort,
		nodes: nodes, logs: logs, adminCert: adminCert, adminKey: adminKey, name: name, bin: bin, args: argv}
}

func adminPortIndex(t *testing.T, ports []int, port int) int {
	t.Helper()
	for i, p := range ports {
		if p == port {
			return i
		}
	}
	t.Fatalf("admin port %d is not one of %v", port, ports)
	return -1
}

// migrationCredentialsDir returns a node's seal.migration_credentials_dir as
// an operator makes one: owned by the daemon's user and writable by no one
// else, which the daemon requires.
func migrationCredentialsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("migrationCredentialsDir: %v", err)
	}
	return dir
}

// credentialTurns is what walkCredentialTurns needs to know about one
// migration scope on a cluster whose nodes have no credential file yet.
type credentialTurns struct {
	// logs are the nodes' logs, indexed like every func below.
	logs []*nodeLog
	// scope is the reconciler's scope label in its log lines: "scope=system",
	// or "tenant=<id> " for a tenant.
	scope string
	// migrated reports whether node i has recycled onto the new barrier.
	migrated func(i int) bool
	// untouched returns an error unless node i still serves its old barrier.
	untouched func(i int) error
	// credit gives node i its own credential file.
	credit func(i int)
	// settle completes node i's turn once it has migrated.
	settle func(i int)
}

// walkCredentialTurns walks a rolling migration requested while no node had a
// credential file, one owner per turn. In its turn a node without a credential
// logs that it awaits its own: it must not have migrated, and is given its
// file and waited for. The node that accepted the request holds its credential
// in memory and migrates with no file; any other node doing so, or that node
// before the last turn, fails the test, as do two nodes awaiting at once. It
// returns the index of that node.
func walkCredentialTurns(t *testing.T, w credentialTurns, timeout time.Duration) int {
	t.Helper()
	done := make([]bool, len(w.logs))
	holder := -1
	for turn := 1; turn <= len(w.logs); turn++ {
		idx, awaiting := -1, false
		if !pollUntil(timeout, 250*time.Millisecond, func() bool {
			var waiting []int
			for i, l := range w.logs {
				if done[i] {
					continue
				}
				if awaitingCredentialIn(l.String(), w.scope) {
					waiting = append(waiting, i)
				} else if idx < 0 && w.migrated(i) {
					idx = i
				}
			}
			if len(waiting) > 1 {
				t.Fatalf("turn %d: nodes %v await their credentials at once; owners recycle one at a time", turn, waiting)
			}
			if len(waiting) == 1 {
				idx, awaiting = waiting[0], true
			}
			return idx >= 0
		}) {
			t.Fatalf("turn %d: no node awaited its credential or migrated within %v", turn, timeout)
		}
		if awaiting {
			if err := w.untouched(idx); err != nil {
				t.Fatalf("turn %d: node %d migrated before it had its own credential: %v", turn, idx+1, err)
			}
			w.credit(idx)
			if !pollUntil(timeout, 250*time.Millisecond, func() bool { return w.migrated(idx) }) {
				t.Fatalf("turn %d: node %d did not migrate within %v of getting its credential", turn, idx+1, timeout)
			}
		} else {
			if holder >= 0 || turn != len(w.logs) {
				t.Fatalf("turn %d: node %d migrated with no credential file; only the node holding the request's credential may, and last", turn, idx+1)
			}
			holder = idx
		}
		t.Logf("turn %d: node %d migrated", turn, idx+1)
		w.settle(idx)
		done[idx] = true
	}
	return holder
}

// awaitingCredentialIn reports whether a node log holds the reconciler's line
// that it awaits its own credential for the migration scope labelled scope.
func awaitingCredentialIn(log, scope string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "local encryption recycle awaiting this node's credential") && strings.Contains(line, scope) {
			return true
		}
	}
	return false
}

// writeMigrationCredential writes a node's own enable-encryption credential
// file, mode 0600, as an operator would.
func writeMigrationCredential(t *testing.T, dir, name, credential string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(credential+"\n"), 0o600); err != nil {
		t.Fatalf("writeMigrationCredential: %v", err)
	}
}
