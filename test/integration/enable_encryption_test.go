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

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeNoneSealConfig writes a daemon YAML config with the seal barrier ENABLED
// but using the no-encryption ("none") strategy. A node booted with this config
// comes up sealed-window, is initialized with an empty secret (none), and then
// serves with an unencrypted at-rest store — the source state for an
// enable-encryption migration.
func writeNoneSealConfig(t *testing.T) string {
	t.Helper()
	yaml := `seal:
  enabled: true
  strategy: none
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeNoneSealConfig: %v", err)
	}
	return cfgPath
}

// writeNoneSealVolumeConfig writes a daemon YAML config with the seal barrier
// enabled using the "none" (unencrypted) strategy AND a single-path volume pool,
// so an IsolationVolume tenant created against it inherits an UNENCRYPTED
// dedicated barrier — the per-tenant source state for enable-encryption.
func writeNoneSealVolumeConfig(t *testing.T, volumePoolDir string) string {
	t.Helper()
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
      - volume
    allow_volume_storage: true
    allow_dedicated_wal: true
    allow_system_managed_barrier: true
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
`, volumePoolDir)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeNoneSealVolumeConfig: %v", err)
	}
	return cfgPath
}

// tenantBarrierStatusJSON fetches GET /v1/tenants/{id}/barrier/status and
// decodes the strategy + sealed fields, tolerating transient connection errors.
func tenantBarrierStatusJSON(t *testing.T, client *http.Client, port int, tenantID string) (strategy string, sealed, ok bool) {
	t.Helper()
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/status", port, tenantID))
	if err != nil {
		t.Logf("tenant barrier status GET error: %v", err)
		return "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Logf("tenant barrier status GET -> %d: %s", resp.StatusCode, string(b))
		return "", false, false
	}
	var body struct {
		Strategy string `json:"strategy"`
		Sealed   bool   `json:"sealed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, false
	}
	return body.Strategy, body.Sealed, true
}

// barrierStatusJSON fetches GET /v1/barrier/status and decodes the strategy +
// sealed fields. It tolerates transient connection errors (returns ok=false)
// because the daemon's in-process migration recycle briefly tears the data-plane
// server down before the sealed-window listener comes back up.
func barrierStatusJSON(t *testing.T, ca *testCA, port int) (strategy string, sealed, ok bool) {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", port))
	if err != nil {
		return "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, false
	}
	var body struct {
		Strategy string `json:"strategy"`
		Sealed   bool   `json:"sealed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, false
	}
	return body.Strategy, body.Sealed, true
}

// waitForBarrierSealedAgain polls the barrier status until it reports
// sealed==true while reachable, tolerating the connection gap during the
// in-process recycle. A migrated barrier comes back keyed-and-SEALED: the
// pre-migration none barrier was unsealed-and-serving (sealed==false), so the
// sealed==true transition is the signal that the in-process recycle completed
// and brought the new keyed barrier up. A sealed barrier does not expose its
// active strategy (Status().Strategy is set only on unseal), so we cannot wait
// on the strategy here — it is asserted after unseal.
func waitForBarrierSealedAgain(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, sealed, ok := barrierStatusJSON(t, ca, port); ok && sealed {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for the migrated barrier to come back sealed on port %d", timeout, port)
}

// TestEnableEncryption_SystemScope_DataSurvivesInProcessRecycle is the
// end-to-end proof of the daemon SYSTEM-scope enable-encryption feature: a
// single-node daemon boots with an UNENCRYPTED (strategy "none") barrier, has
// data written through the full Raft pipeline, then is migrated to a passphrase
// barrier IN PROCESS (the OS process never restarts). After the recycle the
// barrier comes up keyed-and-sealed; the operator unseals with the new
// passphrase, and every datum written before the migration is intact —
// proving the host's state-machine Pebble + WAL were re-encrypted without loss.
func TestEnableEncryption_SystemScope_DataSurvivesInProcessRecycle(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	nodeCert, nodeKey := ca.generateNodeCert(t)

	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	cfgPath := writeNoneSealConfig(t)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCert,
		"--tls-key-file", nodeKey,
	}
	startNode(t, bin, args, "[enc-mig] ")

	// Sealed-window: initialize the none barrier (empty secret) so the daemon
	// proceeds to full serving with an unencrypted at-rest store.
	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	// Confirm the source barrier is genuinely unencrypted.
	if strategy, _, ok := barrierStatusJSON(t, ca, adminPort); !ok || strategy != "none" {
		t.Fatalf("pre-migration barrier strategy = %q (ok=%v), want none", strategy, ok)
	}

	// Write data through the full Raft pipeline before migrating.
	seed := map[string]string{
		"alpha":   "one",
		"bravo":   "two",
		"charlie": "three",
		"delta":   "four",
	}
	for k, v := range seed {
		httpPut(t, ca, adminPort, k, []byte(v))
	}
	// Verify the seed is readable pre-migration.
	for k, v := range seed {
		if got := string(httpGet(t, ca, adminPort, k)); got != v {
			t.Fatalf("pre-migration %q = %q, want %q", k, got, v)
		}
	}

	// Trigger the in-process system-scope migration to a passphrase barrier.
	const newPassphrase = "enable-encryption-system-pass"
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	body, status := iraftAPIPost(t, client,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", adminPort),
		map[string]interface{}{"strategy": "passphrase", "passphrase": newPassphrase},
	)
	if status != http.StatusOK {
		t.Fatalf("enable-encryption: status %d, body: %s", status, body)
	}

	// The migration runs asynchronously and recycles the data plane in process.
	// The barrier comes back up keyed-and-SEALED (the daemon does not hold the new
	// credential), so wait for the sealed==true transition.
	waitForBarrierSealedAgain(t, ca, adminPort, 120*time.Second)

	// Unseal with the new passphrase via the post-recycle sealed-window listener.
	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	unsealBody, unsealStatus := iraftAPIPost(t, client,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPort),
		map[string]interface{}{"secret": newPassphrase},
	)
	if unsealStatus != http.StatusNoContent && unsealStatus != http.StatusConflict {
		t.Fatalf("post-migration unseal: status %d, body: %s", unsealStatus, unsealBody)
	}

	// Wait for full serving to resume under the keyed barrier.
	waitForHealthy(t, dataDir, 90*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	// Confirm the barrier is now keyed AND unsealed.
	if strategy, sealed, ok := barrierStatusJSON(t, ca, adminPort); !ok || strategy != "passphrase" || sealed {
		t.Fatalf("post-unseal barrier strategy=%q sealed=%v ok=%v, want passphrase/unsealed", strategy, sealed, ok)
	}

	// Every datum written before the migration must survive the re-encryption.
	for k, v := range seed {
		if got := string(httpGet(t, ca, adminPort, k)); got != v {
			t.Fatalf("post-migration %q = %q, want %q (DATA LOSS across re-encryption)", k, got, v)
		}
	}

	// New writes work under the keyed barrier.
	httpPut(t, ca, adminPort, "echo", []byte("five"))
	if got := string(httpGet(t, ca, adminPort, "echo")); got != "five" {
		t.Fatalf("post-migration new write echo = %q, want five", got)
	}
}

// TestEnableEncryption_TenantScope_OnlineRecycleNoServerRestart proves the
// PER-TENANT enable-encryption path over the wire: an IsolationVolume tenant
// with an UNENCRYPTED dedicated barrier is migrated to a passphrase barrier
// ONLINE — only that tenant's Host is recycled; the daemon (and the system
// barrier) keep serving throughout, and the tenant's data survives. Unlike the
// system scope, the tenant barrier is reopened unsealed (no operator unseal).
func TestEnableEncryption_TenantScope_OnlineRecycleNoServerRestart(t *testing.T) {
	const tenantID = "acme"
	const dbName = "appdata"

	bin := buildBinary(t)
	ca := generateTestCA(t)
	nodeCert, nodeKey := ca.generateNodeCert(t)

	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	volumePoolDir := t.TempDir()
	cfgPath := writeNoneSealVolumeConfig(t, volumePoolDir)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCert,
		"--tls-key-file", nodeKey,
	}
	startNode(t, bin, args, "[tenant-mig] ")

	// Bring the (none) system barrier up serving.
	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	// Create an IsolationVolume tenant with a SELF-MANAGED dedicated barrier
	// (barrier_auto_unseal=false). A system-managed (auto-unseal) barrier uses
	// StrategySystemBarrier — it delegates to the shared system barrier and is
	// NOT independently migratable. A self-managed barrier gets its own dedicated
	// barrier that the tenant admin initializes; initialized with an empty secret
	// it is an unencrypted ("none") dedicated barrier — the per-tenant source
	// state for enable-encryption.
	tBody, tStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", adminPort),
		map[string]interface{}{
			"id":                  tenantID,
			"name":                "Acme",
			"isolation_type":      "volume",
			"barrier_auto_unseal": false,
		},
	)
	if tStatus != http.StatusCreated && tStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tStatus, tBody)
	}

	// The tenant's dedicated barrier must be initialized BEFORE any of its
	// IAM can exist: a self-managed tenant's roles and users are encrypted
	// under the tenant DEK, so the store resolver fails closed until the
	// barrier is up. The initialization itself is trust-on-first-init — the
	// tenant's own authenticated identity performs it with no role (see
	// pkg/api/rest authorizeTenantBarrierInit); every later barrier op
	// requires the RBAC grant.
	tenantCert, tenantKey := ca.generateClientCertForTenant(t, tenantID)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCert, tenantKey, 15*time.Second)

	// Initialize the self-managed dedicated barrier with an empty secret => a
	// none (unencrypted) dedicated barrier, unsealed and ready to serve.
	if initBody, initStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/init", adminPort, tenantID),
		map[string]interface{}{"strategy": "none", "secret": ""},
	); initStatus != http.StatusOK && initStatus != http.StatusNoContent && initStatus != http.StatusCreated {
		t.Fatalf("tenant barrier init: status %d, body: %s", initStatus, initBody)
	}

	// Role + user so the tenant cert is authorized to create a database, do
	// KV I/O, and read its own barrier status. Created AFTER the barrier
	// init: the records land in the tenant's own encrypted IAM store.
	roleBody, roleStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:read", "databases:list",
				"kv:put", "kv:get",
				"tenant_barrier:status", "tenant_barrier:init",
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
	if _, uStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/users", adminPort),
		map[string]interface{}{"username": "admin", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
	); uStatus != http.StatusCreated && uStatus != http.StatusOK {
		t.Fatalf("create user: status %d", uStatus)
	}

	// The tenant's dedicated barrier starts unencrypted (read via the tenant's
	// own client — it owns its barrier).
	if strategy, _, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID); !ok || strategy != "none" {
		t.Fatalf("pre-migration tenant barrier strategy = %q (ok=%v), want none", strategy, ok)
	}

	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", adminPort),
		map[string]interface{}{"name": dbName, "engine": "kv", "backend": "pebble", "replicas": 1},
	)
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create tenant database: status %d, body: %s", dbStatus, dbBody)
	}

	// Write tenant data through the tenant's volume Host (X-QRDB-Database routes
	// to the dedicated database on the tenant Host).
	seed := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	for k, v := range seed {
		deadline := time.Now().Add(30 * time.Second)
		var putStatus int
		var putErr error
		for time.Now().Before(deadline) {
			putStatus, putErr = dbScopedPutTolerant(t, tenantClient, adminPort, dbName, k, []byte(v))
			if putErr == nil && (putStatus == http.StatusOK || putStatus == http.StatusNoContent || putStatus == http.StatusCreated) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if putErr != nil || (putStatus != http.StatusOK && putStatus != http.StatusNoContent && putStatus != http.StatusCreated) {
			t.Fatalf("tenant put %q: status %d err %v", k, putStatus, putErr)
		}
	}

	// Trigger the per-tenant online migration to a passphrase barrier.
	const tenantPass = "tenant-acme-passphrase"
	migBody, migStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", adminPort),
		map[string]interface{}{"tenant_id": tenantID, "strategy": "passphrase", "passphrase": tenantPass},
	)
	if migStatus != http.StatusOK {
		t.Fatalf("tenant enable-encryption: status %d, body: %s", migStatus, migBody)
	}

	// The tenant barrier transitions to passphrase and is reopened UNSEALED (the
	// recycle unseals it with the supplied credential). The system barrier and
	// daemon keep serving — no server restart.
	deadline := time.Now().Add(120 * time.Second)
	migrated := false
	for time.Now().Before(deadline) {
		if strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID); ok && strategy == "passphrase" && !sealed {
			migrated = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !migrated {
		t.Fatalf("tenant barrier did not migrate to an unsealed passphrase barrier within timeout")
	}

	// The system barrier is untouched and still serving.
	if strategy, sealed, ok := barrierStatusJSON(t, ca, adminPort); !ok || strategy != "none" || sealed {
		t.Fatalf("system barrier changed by tenant migration: strategy=%q sealed=%v ok=%v", strategy, sealed, ok)
	}

	// Every tenant datum survives the per-tenant recycle. The first read after
	// the migration may briefly race the Host rebuild under the keyed barrier
	// (ErrTenantMigrationInProgress / a transient 5xx); retry until the keyed
	// Host is serving, then assert the value.
	for k, v := range seed {
		var data []byte
		var st int
		rd := time.Now().Add(60 * time.Second)
		for time.Now().Before(rd) {
			data, st = dbScopedGetTolerant(t, tenantClient, adminPort, dbName, k)
			if st == http.StatusOK {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if st != http.StatusOK || string(data) != v {
			t.Fatalf("post-migration tenant %q = %q (status %d), want %q (DATA LOSS)", k, string(data), st, v)
		}
	}
}
