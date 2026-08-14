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

// tenant_data_root_cli_test.go drives `qrdb tenant create --data-root` through
// the real client CLI binary (cmd/qrdb) as a subprocess talking REST-over-TLS
// to a live qrdbd node, proving FEATURE B (an operator-pinned dedicated-disk
// DataRoot for IsolationVolume/IsolationRaft tenants) end-to-end: the tenant's
// data is physically placed under the requested path AND encrypted at rest
// under the tenant's own barrier, exactly like a pool-allocated DataRoot.
//
// The IsolationRaft case additionally proves FEATURE C's `local` Raft cluster
// topology is honored end-to-end (CLI -> REST -> service -> manager) for a
// dedicated-raft database created under the pinned-DataRoot tenant.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDataRootTenantConfig writes a daemon YAML config with the seal barrier
// enabled using the "none" (unencrypted, system-shard) strategy, a
// single-path volume pool (required for both IsolationVolume and
// IsolationRaft), and default entitlements permitting prefix/volume/raft
// isolation plus system-managed auto-unseal so a tenant created with
// --barrier-auto-unseal comes up unsealed and ready without a separate
// init/unseal round-trip.
func writeDataRootTenantConfig(t *testing.T, volumePoolDir string) string {
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
      - raft
    allow_volume_storage: true
    allow_dedicated_raft: true
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
		t.Fatalf("writeDataRootTenantConfig: %v", err)
	}
	return cfgPath
}

// startDataRootTenantNode starts a single-node daemon configured with
// writeDataRootTenantConfig and returns the admin port, node data dir, and a
// system-admin CLI connArgs slice ready for tenant/role/user provisioning.
func startDataRootTenantNode(t *testing.T, bin string, logPrefix string) (adminPort int, dataDir string, ca *testCA, sysConnArgs []string) {
	t.Helper()
	ca = generateTestCA(t)
	nodeCert, nodeKey := ca.generateNodeCert(t)

	raftPort := allocatePorts(t, 1)[0]
	adminPort = allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir = t.TempDir()
	volumePoolDir := t.TempDir()
	cfgPath := writeDataRootTenantConfig(t, volumePoolDir)

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
	startNode(t, bin, args, logPrefix)

	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	adminCertPath, adminKeyPath := ca.generateAdminClientCert(t, dataDir)
	sysConnArgs = restConnArgs(ca, adminCertPath, adminKeyPath, adminPort)
	return adminPort, dataDir, ca, sysConnArgs
}

// provisionTenantCLIIdentity grants a tenant's mTLS identity (CN=admin,
// OU=tenantID, see (*testCA).generateClientCertForTenant) the permissions
// needed to create databases and do KV I/O, and returns CLI connArgs bound to
// that identity.
func provisionTenantCLIIdentity(t *testing.T, ca *testCA, adminPort int, sysClient *http.Client, tenantID string) []string {
	t.Helper()

	roleBody, roleStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:read", "databases:list",
				"kv:put", "kv:get",
			},
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role for tenant %s: status %d, body: %s", tenantID, roleStatus, roleBody)
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
		t.Fatalf("create user for tenant %s: status %d", tenantID, uStatus)
	}

	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, tenantID)
	return restConnArgs(ca, tenantCertPath, tenantKeyPath, adminPort)
}

// cliDataRootTenantJSON decodes the subset of the tenant JSON response this
// test needs.
type cliDataRootTenantJSON struct {
	ID            string `json:"id"`
	IsolationType string `json:"isolation_type"`
	DataRoot      string `json:"data_root"`
}

// TestCLI_TenantCreate_DataRoot_Volume_PlacesDataAndEncrypts proves FEATURE B
// for an IsolationVolume tenant: `qrdb tenant create --data-root <dir>`
// places the tenant's barrier AND its data under the operator-supplied path
// (never a pool-allocated one), and the live data plane physically encrypts
// it -- a plaintext value written through KV never appears on disk under that
// directory.
func TestCLI_TenantCreate_DataRoot_Volume_PlacesDataAndEncrypts(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	adminPort, dataDir, ca, sysConnArgs := startDataRootTenantNode(t, bin, "[tenant-dataroot-volume] ")
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	tenantID := fmt.Sprintf("vol-dr-%d", time.Now().UnixNano())
	explicitDataRoot := filepath.Join(t.TempDir(), "dedicated-disk", tenantID)

	out, err := runQRDB(t, cli, sysConnArgs, "--output", "json", "tenant", "create",
		"--id", tenantID,
		"--name", "Volume DataRoot Tenant",
		"--isolation-type", "volume",
		"--barrier-auto-unseal",
		"--data-root", explicitDataRoot,
	)
	if err != nil {
		t.Fatalf("tenant create --data-root: %v\noutput: %s", err, out)
	}
	var tenantResp cliDataRootTenantJSON
	if decErr := json.Unmarshal([]byte(out), &tenantResp); decErr != nil {
		t.Fatalf("decode tenant create JSON: %v\noutput: %s", decErr, out)
	}
	if tenantResp.DataRoot != explicitDataRoot {
		t.Fatalf("tenant DataRoot = %q, want the operator-supplied path %q (must NOT be pool-allocated)", tenantResp.DataRoot, explicitDataRoot)
	}

	// Proof (a): the tenant's barrier is physically placed under the
	// requested path, not the volume pool.
	if _, statErr := os.Stat(filepath.Join(explicitDataRoot, "barrier")); statErr != nil {
		t.Fatalf("barrier subdirectory not found under explicit DataRoot %q: %v", explicitDataRoot, statErr)
	}
	if strings.Contains(dataDir, explicitDataRoot) {
		t.Fatalf("sanity: explicit DataRoot must be independent of the node's own --data-dir")
	}

	tenantConnArgs := provisionTenantCLIIdentity(t, ca, adminPort, sysClient, tenantID)

	dbName := "appdata"
	out, err = runQRDB(t, cli, tenantConnArgs, "database", "create", dbName, "--engine", "kv", "--backend", "pebble")
	if err != nil {
		t.Fatalf("tenant database create: %v\noutput: %s", err, out)
	}

	key, value := "greeting", "hello-volume-dataroot-plaintext-marker"
	out, err = runQRDB(t, cli, tenantConnArgs, "--database", dbName, "kv", "put", key, value)
	if err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}
	out, err = runQRDB(t, cli, tenantConnArgs, "--database", dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("kv get: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != value {
		t.Fatalf("kv get %s = %q, want %q", key, strings.TrimSpace(out), value)
	}

	// Proof (b): the plaintext value never appears anywhere under the
	// operator-pinned DataRoot -- physical encryption at rest, not inert
	// bookkeeping.
	assertPlaintextAbsentInDir(t, explicitDataRoot, []byte(value))
}

// TestCLI_TenantCreate_DataRoot_Raft_PlacesDataAndEncrypts proves FEATURE B
// for an IsolationRaft tenant (each database gets its own dedicated Raft
// shard rooted under the tenant's pinned DataRoot) AND FEATURE C's `local`
// Raft cluster-mode topology, requested explicitly via
// `qrdb database create --cluster-mode local`, is honored end-to-end.
func TestCLI_TenantCreate_DataRoot_Raft_PlacesDataAndEncrypts(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	adminPort, _, ca, sysConnArgs := startDataRootTenantNode(t, bin, "[tenant-dataroot-raft] ")
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	tenantID := fmt.Sprintf("raft-dr-%d", time.Now().UnixNano())
	explicitDataRoot := filepath.Join(t.TempDir(), "dedicated-disk", tenantID)

	out, err := runQRDB(t, cli, sysConnArgs, "--output", "json", "tenant", "create",
		"--id", tenantID,
		"--name", "Raft DataRoot Tenant",
		"--isolation-type", "raft",
		"--barrier-auto-unseal",
		"--data-root", explicitDataRoot,
	)
	if err != nil {
		t.Fatalf("tenant create --data-root: %v\noutput: %s", err, out)
	}
	var tenantResp cliDataRootTenantJSON
	if decErr := json.Unmarshal([]byte(out), &tenantResp); decErr != nil {
		t.Fatalf("decode tenant create JSON: %v\noutput: %s", decErr, out)
	}
	if tenantResp.DataRoot != explicitDataRoot {
		t.Fatalf("tenant DataRoot = %q, want the operator-supplied path %q (must NOT be pool-allocated)", tenantResp.DataRoot, explicitDataRoot)
	}
	if _, statErr := os.Stat(filepath.Join(explicitDataRoot, "barrier")); statErr != nil {
		t.Fatalf("barrier subdirectory not found under explicit DataRoot %q: %v", explicitDataRoot, statErr)
	}

	tenantConnArgs := provisionTenantCLIIdentity(t, ca, adminPort, sysClient, tenantID)

	dbName := "orders"
	out, err = runQRDB(t, cli, tenantConnArgs, "--output", "json", "database", "create", dbName,
		"--backend", "raft",
		"--cluster-mode", "local",
	)
	if err != nil {
		t.Fatalf("tenant database create --backend raft --cluster-mode local: %v\noutput: %s", err, out)
	}
	var dbInfo struct {
		DataDir string `json:"dataDir"`
		Raft    struct {
			ClusterMode string `json:"clusterMode"`
		} `json:"raft"`
	}
	if decErr := json.Unmarshal([]byte(out), &dbInfo); decErr != nil {
		t.Fatalf("decode database create JSON: %v\noutput: %s", decErr, out)
	}
	// FEATURE C proof: the explicitly requested "local" topology is honored
	// and persisted, not silently overwritten by the server-side default.
	if dbInfo.Raft.ClusterMode != "local" {
		t.Fatalf("database Raft.ClusterMode = %q, want %q (explicit --cluster-mode local must be honored)", dbInfo.Raft.ClusterMode, "local")
	}
	// FEATURE B proof: the dedicated shard's state-machine data is rooted
	// under the tenant's pinned DataRoot, not a pool-allocated path.
	if !strings.HasPrefix(dbInfo.DataDir, explicitDataRoot) {
		t.Fatalf("database DataDir = %q, want it rooted under the pinned tenant DataRoot %q", dbInfo.DataDir, explicitDataRoot)
	}

	key, value := "greeting", "hello-raft-dataroot-plaintext-marker"
	out, err = runQRDB(t, cli, tenantConnArgs, "--database", dbName, "kv", "put", key, value)
	if err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}
	out, err = runQRDB(t, cli, tenantConnArgs, "--database", dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("kv get: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != value {
		t.Fatalf("kv get %s = %q, want %q", key, strings.TrimSpace(out), value)
	}

	// Proof: the dedicated raft shard's on-disk state is encrypted under the
	// tenant barrier -- the plaintext value never appears anywhere under the
	// pinned DataRoot.
	assertPlaintextAbsentInDir(t, explicitDataRoot, []byte(value))
}
