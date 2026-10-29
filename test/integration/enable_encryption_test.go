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

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
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
      path: %s
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - volume
    allow_volume_storage: true
    allow_dedicated_wal: true
    allow_system_managed_barrier: true
    allowed_volume_pools:
      - default
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
// server down before the sealed-window listener comes back up. A sealed node
// answers sealed with no strategy; a node the cluster seal holds answers the
// typed sealed refusal, which reads the same.
func barrierStatusJSON(t *testing.T, ca *testCA, port int) (strategy string, sealed, ok bool) {
	t.Helper()
	r := readBarrierStatus(t, ca, port)
	return r.Strategy, r.Sealed, r.Definitive
}

// barrierStatusReading is one GET /v1/barrier/status answer. Definitive is
// true when the node answered with its barrier state -- a 200 with a decodable
// body, or the typed sealed refusal -- and false for a transient answer (a
// transport error, any other status, an undecodable body), which says nothing
// about the barrier. Status, Body and Err keep the raw answer so a failure
// message can show what the node actually said.
type barrierStatusReading struct {
	Strategy   string
	Sealed     bool
	Definitive bool
	Status     int
	Body       string
	Err        error
}

// String renders the reading for a failure message, raw answer included.
func (r barrierStatusReading) String() string {
	if r.Err != nil {
		return fmt.Sprintf("no answer (%v)", r.Err)
	}
	return fmt.Sprintf("strategy=%q sealed=%v definitive=%v (HTTP %d, body: %s)",
		r.Strategy, r.Sealed, r.Definitive, r.Status, r.Body)
}

// readBarrierStatus fetches GET /v1/barrier/status once and classifies the
// answer. A sealed node answers sealed with no strategy; a node the cluster
// seal holds answers the typed sealed refusal, which reads the same.
func readBarrierStatus(t *testing.T, ca *testCA, port int) barrierStatusReading {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", port))
	if err != nil {
		return barrierStatusReading{Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	r := barrierStatusReading{Status: resp.StatusCode, Body: string(raw)}
	if err != nil {
		r.Err = err
		return r
	}
	if qrdbdharness.IsSealedRefusal(resp.StatusCode, raw) {
		r.Sealed, r.Definitive = true, true
		return r
	}
	if resp.StatusCode != http.StatusOK {
		return r
	}
	var body struct {
		Strategy string `json:"strategy"`
		Sealed   bool   `json:"sealed"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return r
	}
	r.Strategy, r.Sealed, r.Definitive = body.Strategy, body.Sealed, true
	return r
}

// waitForDefinitiveBarrierStatus polls the barrier status until the node gives
// a definitive answer and returns it, so the caller asserts on what the
// barrier is rather than on whether one request happened to get through. A
// cluster in the middle of a system recycle has nodes rebooting in process and
// a leader that may have lost quorum; a node still serving can answer
// transiently through that window. At the deadline it fails with the last
// transient answer, HTTP status and body included.
func waitForDefinitiveBarrierStatus(t *testing.T, ca *testCA, port int, timeout time.Duration) barrierStatusReading {
	t.Helper()
	var r barrierStatusReading
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		r = readBarrierStatus(t, ca, port)
		return r.Definitive
	}) {
		t.Fatalf("barrier status on port %d gave no definitive answer within %v; last: %s", port, timeout, r)
	}
	return r
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
	if pollUntil(timeout, 250*time.Millisecond, func() bool {
		_, sealed, ok := barrierStatusJSON(t, ca, port)
		return ok && sealed
	}) {
		return
	}
	t.Fatalf("timed out after %v waiting for the migrated barrier to come back sealed on port %d", timeout, port)
}

// createDatabaseRetryingUnavailable creates a database through
// POST /v1/databases, retrying the server's typed "retry" answers
// (postRetryingRetryable). A create that a consensus step could not finish
// inside the server's own create deadline is rolled back and refused as
// retryable, which on a loaded host is an expected answer rather than a
// failure; any other non-2xx status fails the test.
func createDatabaseRetryingUnavailable(t *testing.T, client *http.Client, port int, body map[string]interface{}) {
	t.Helper()
	respBody, status := postRetryingRetryable(t, client, fmt.Sprintf("https://127.0.0.1:%d/v1/databases", port), body)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create database %v: status %d, body: %s", body["name"], status, respBody)
	}
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
	assertSystemEnableEncryptionKeepsData(t, "--sm", "pebble")
}

// TestEnableEncryption_SystemScope_DiskOverFileStorage is the same daemon
// system-scope migration over a disk state machine whose storage is the file
// engine (`raft.sm_storage: file`). That store sits under the data directory's
// sm/ tree, outside the host's own stores, and is encrypted through the
// barrier's encfs like them; a migration that left it under the old key would
// make every replica start fail with a decryption error.
func TestEnableEncryption_SystemScope_DiskOverFileStorage(t *testing.T) {
	assertSystemEnableEncryptionKeepsData(t, "--sm", "disk", "--sm-storage", "file")
}

// assertSystemEnableEncryptionKeepsData starts a single raft node with a
// strategy-none barrier and the given state machine flags, writes through the
// full Raft pipeline, migrates the system barrier to a passphrase in process,
// unseals with it, and asserts every record survived and new writes succeed.
func assertSystemEnableEncryptionKeepsData(t *testing.T, smArgs ...string) {
	t.Helper()
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
	}
	args = append(args, smArgs...)
	args = append(args,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCert,
		"--tls-key-file", nodeKey,
	)
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
		map[string]interface{}{"strategy": "passphrase", "passphrase": []byte(newPassphrase)},
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
		map[string]interface{}{"secret": []byte(newPassphrase)},
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

	createVolumePoolREST(t, sysClient, adminPort, "default")

	// Create an IsolationVolume tenant with a SELF-MANAGED dedicated barrier
	// (barrier_auto_unseal=false). A system-managed (auto-unseal) barrier uses
	// StrategySystemBarrier — it delegates to the shared system barrier and is
	// NOT independently migratable. A self-managed barrier gets its own dedicated
	// barrier that the tenant admin initializes; initialized with an empty secret
	// it is an unencrypted ("none") dedicated barrier — the per-tenant source
	// state for enable-encryption.
	//
	// Its first admin brings its own certificate: this BYO-TLS node has no
	// cluster CA to sign a CSR with, and the tenant's own CA is behind the
	// barrier that admin has yet to initialize.
	createReq, admin := adminBYOCFieldsForTenant(t, tenantID, tenantID+"-admin")
	createReq["id"], createReq["name"] = tenantID, "Acme"
	createReq["isolation_type"], createReq["barrier_auto_unseal"] = "volume", false
	tBody, tStatus := postRetryingRetryable(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", adminPort), createReq)
	if tStatus != http.StatusCreated && tStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tStatus, tBody)
	}

	// The tenant's dedicated barrier must be initialized BEFORE any of its
	// IAM can exist: a self-managed tenant's roles and users are encrypted
	// under the tenant DEK, so the store resolver fails closed until the
	// barrier is up. The initialization itself is trust-on-first-init by
	// the EXACT certificate the tenant's first admin was onboarded with
	// above, never any other authenticated identity of the tenant (see
	// pkg/api/rest authorizeTenantBarrierInit); every later barrier op
	// requires the RBAC grant.
	adminTLSCert, err := admin.TLSCertificate()
	if err != nil {
		t.Fatalf("key pair for the onboarded admin: %v", err)
	}
	onboardedAdminClient := qrdbdharness.HTTPSClient(ca.Pool, &adminTLSCert, 15*time.Second)

	// Initialize the self-managed dedicated barrier with an empty secret => a
	// none (unencrypted) dedicated barrier, unsealed and ready to serve.
	initTenantBarrierAsFirstAdmin(t, fmt.Sprintf("https://127.0.0.1:%d", adminPort), ca.CertPath,
		admin.CertPEM, admin.KeyPEM, tenantID)

	// The onboarded admin's account carries only the pending-admin
	// completion's own role, not the data-plane permissions the rest of
	// this test needs: the tenant's generic (CN=admin, OU=tenantID)
	// identity, provisioned below with its own role, drives everything
	// from here on.
	tenantCert, tenantKey := ca.generateClientCertForTenant(t, tenantID)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCert, tenantKey, 15*time.Second)

	// Role + user so the tenant cert is authorized to create a database, do
	// KV I/O, and read its own barrier status. Created AFTER the barrier
	// init: the records land in the tenant's own encrypted IAM store. The
	// system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so this
	// authenticates as the tenant's own onboarded admin; its built-in Admin role lets it seat a second account with
	// Admin and Operator (seatTenantProvisioner), which creates the role
	// from what it holds (the grant rule, go-qrdb#1278).
	onboardedAdminClient = seatTenantProvisioner(t, ca, adminPort, tenantID, onboardedAdminClient, false)
	roleBody, roleStatus := iraftAPIPost(t, onboardedAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:create_volume", "databases:read", "databases:list",
				"kv:put", "kv:get",
				"tenant_barrier:status", "tenant_barrier:init", "tenant_barrier:enable_encryption",
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
	createBoundUser(t, onboardedAdminClient, adminPort,
		map[string]interface{}{"username": "admin", "email": "admin@" + tenantID + ".example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
		tenantCert)

	// The tenant's dedicated barrier starts unencrypted (read via the tenant's
	// own client — it owns its barrier).
	if strategy, _, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID); !ok || strategy != "none" {
		t.Fatalf("pre-migration tenant barrier strategy = %q (ok=%v), want none", strategy, ok)
	}

	createDatabaseRetryingUnavailable(t, tenantClient, adminPort,
		map[string]interface{}{"name": dbName, "backend": "pebble", "replicas": 1})

	// Write tenant data through the tenant's volume Host (X-QRDB-Database routes
	// to the dedicated database on the tenant Host).
	seed := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	for k, v := range seed {
		var putStatus int
		var putErr error
		pollUntil(30*time.Second, 300*time.Millisecond, func() bool {
			putStatus, putErr = dbScopedPutTolerant(t, tenantClient, adminPort, dbName, k, []byte(v))
			return putErr == nil && (putStatus == http.StatusOK || putStatus == http.StatusNoContent || putStatus == http.StatusCreated)
		})
		if putErr != nil || (putStatus != http.StatusOK && putStatus != http.StatusNoContent && putStatus != http.StatusCreated) {
			t.Fatalf("tenant put %q: status %d err %v", k, putStatus, putErr)
		}
	}

	// Trigger the per-tenant online migration to a passphrase barrier. A
	// tenant's barrier is migrated under tenant_barrier:enable_encryption
	// for THAT tenant (go-qrdb#791): the system root, outside the tenant, is
	// refused, and the tenant's own identity holding the permission migrates.
	const tenantPass = "tenant-acme-passphrase"
	if b, s := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", adminPort),
		map[string]interface{}{"tenant_id": tenantID, "strategy": "passphrase", "passphrase": []byte(tenantPass)},
	); s != http.StatusForbidden {
		t.Fatalf("system-root tenant enable-encryption: status %d, want 403; body: %s", s, b)
	}
	migBody, migStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", adminPort),
		map[string]interface{}{"tenant_id": tenantID, "strategy": "passphrase", "passphrase": []byte(tenantPass)},
	)
	if migStatus != http.StatusOK {
		t.Fatalf("tenant enable-encryption: status %d, body: %s", migStatus, migBody)
	}

	// The tenant barrier transitions to passphrase and is reopened UNSEALED (the
	// recycle unseals it with the supplied credential). The system barrier and
	// daemon keep serving — no server restart.
	if !pollUntil(120*time.Second, 300*time.Millisecond, func() bool {
		strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID)
		return ok && strategy == "passphrase" && !sealed
	}) {
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
		pollUntil(60*time.Second, 300*time.Millisecond, func() bool {
			data, st = dbScopedGetTolerant(t, tenantClient, adminPort, dbName, k)
			return st == http.StatusOK
		})
		if st != http.StatusOK || string(data) != v {
			t.Fatalf("post-migration tenant %q = %q (status %d), want %q (DATA LOSS)", k, string(data), st, v)
		}
	}
}
