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

// volume_boot_degraded_cli_test.go proves the owner's 2026-10-06 ruling's
// second half end to end, against the real qrdbd/qrdb binaries: under
// --allow-degraded-volume-boot, a node with one healthy pool and one
// pool that failed to open SERVES the tenant on the healthy pool and
// REFUSES the tenant on the failed one with a typed, classified "volume
// unavailable" error -- on REST and gRPC alike -- never a not-found or a
// generic internal error. See docs/volume-pools/{architecture,engineering,
// cli,user,api}.md.
package integration_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeTwoPoolTenantConfig writes a daemon YAML config with two volume
// pools, "fast" (fastDir) and "broken" (brokenDir), both entitled to
// prefix/volume isolation tenants, and optionally
// storage.allow_degraded_volume_boot set.
func writeTwoPoolTenantConfig(t *testing.T, fastDir, brokenDir string, allowDegraded bool) string {
	t.Helper()
	degraded := ""
	if allowDegraded {
		degraded = "  allow_degraded_volume_boot: true\n"
	}
	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: none
storage:
  volume_pools:
    - name: fast
      path: %s
    - name: broken
      path: %s
%stenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - volume
    allow_volume_storage: true
    allow_system_managed_barrier: true
    allowed_volume_pools:
      - fast
      - broken
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
`, fastDir, brokenDir, degraded)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeTwoPoolTenantConfig: %v", err)
	}
	return cfgPath
}

// TestQrdbdStart_DegradedBoot_ServesGoodPoolRefusesBadPool: a tenant is
// created on each of two pools while both are healthy. The node is then
// restarted with the "broken" pool's mount gone and
// --allow-degraded-volume-boot set: the "good" tenant (on "fast") keeps
// serving exactly as before, and the "bad" tenant (on "broken") is
// refused with a typed, classified "volume unavailable" error -- proven
// through the qrdb CLI over both REST and gRPC -- never a not-found or a
// bare internal error.
func TestQrdbdStart_DegradedBoot_ServesGoodPoolRefusesBadPool(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	ca := generateTestCA(t)
	nodeCert, nodeKey := ca.generateNodeCert(t)

	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	fastDir := t.TempDir()
	brokenDirRound1 := t.TempDir()
	brokenDirRound2 := filepath.Join(t.TempDir(), "gone")

	startArgs := func(cfgPath string, extra ...string) []string {
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
		return append(args, extra...)
	}

	// --- Round 1: both pools healthy; create pools, tenants, data ---------
	cfg1 := writeTwoPoolTenantConfig(t, fastDir, brokenDirRound1, false)
	cmd1 := startNode(t, bin, startArgs(cfg1), "[degraded-boot-r1] ")

	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)
	adminCertPath, adminKeyPath := ca.boundAdminCert(t, dataDir)
	sysConnArgs := restConnArgs(ca, adminCertPath, adminKeyPath, adminPort)

	createVolumePoolCLI(t, cli, sysConnArgs, "fast")
	createVolumePoolCLI(t, cli, sysConnArgs, "broken")

	stamp := time.Now().UnixNano()
	goodID := fmt.Sprintf("good-%d", stamp)
	badID := fmt.Sprintf("bad-%d", stamp)

	adminCertPaths := make(map[string]string, 2)
	adminKeyPaths := make(map[string]string, 2)
	for _, tc := range []struct{ id, pool string }{{goodID, "fast"}, {badID, "broken"}} {
		adminArgs := adminCSRArgsForDataRootTenant(t, cli, sysConnArgs, tc.id+"-admin")
		adminCertDir := t.TempDir()
		out, err := runQRDB(t, cli, sysConnArgs, append([]string{"tenant", "create",
			"--id", tc.id,
			"--name", tc.id,
			"--isolation-type", "volume",
			"--barrier-auto-unseal",
			"--pool", tc.pool,
			"--out", adminCertDir,
		}, adminArgs...)...)
		if err != nil {
			t.Fatalf("tenant create %s on pool %s: %v\noutput: %s", tc.id, tc.pool, err, out)
		}
		adminCertPaths[tc.id] = filepath.Join(adminCertDir, "admin-cert.pem")
		adminKeyPaths[tc.id] = filepath.Join(filepath.Dir(adminArgs[3]), "key.pem")
	}

	goodConnArgs := provisionTenantCLIIdentity(t, ca, adminPort, adminCertPaths[goodID], adminKeyPaths[goodID], goodID)
	badConnArgs := provisionTenantCLIIdentity(t, ca, adminPort, adminCertPaths[badID], adminKeyPaths[badID], badID)

	// Every tenant's default database ("" / dbReg.DefaultName()) always
	// exists without a separate create step (rest_registry.go's
	// databaseExists short-circuits true for it) -- using it keeps this
	// test about the pool-unavailable refusal, not about a named
	// database's own catalog lookup.
	for _, tc := range []struct {
		id       string
		connArgs []string
	}{{goodID, goodConnArgs}, {badID, badConnArgs}} {
		value := "value-for-" + tc.id
		if out, err := runQRDB(t, cli, tc.connArgs, "kv", "put", "k", value); err != nil {
			t.Fatalf("kv put for %s: %v\noutput: %s", tc.id, err, out)
		}
	}

	// Stop round 1 cleanly before restarting against the same data-dir and
	// ports.
	if err := cmd1.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal round 1: %v", err)
	}
	_ = awaitNodeExit(t, cmd1, nodeStopTimeout)

	// --- Round 2: "broken" pool's mount is gone; degraded boot opts in -----
	cfg2 := writeTwoPoolTenantConfig(t, fastDir, brokenDirRound2, true)
	startNode(t, bin, startArgs(cfg2, "--allow-degraded-volume-boot"), "[degraded-boot-r2] ")

	// A fresh process boots sealed again even under strategy "none": the
	// barrier was already initialized in round 1 (persisted), so round 2
	// needs only the unseal half of the ceremony, not another initialize.
	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	unsealOnlyBarrier(t, ca, adminPort)
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	// The good tenant, on the still-healthy "fast" pool, keeps serving
	// exactly as before.
	out, err := runQRDB(t, cli, goodConnArgs, "kv", "get", "k")
	if err != nil {
		t.Fatalf("kv get on the good tenant after degraded boot: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "value-for-"+goodID {
		t.Fatalf("kv get on the good tenant = %q, want %q", strings.TrimSpace(out), "value-for-"+goodID)
	}

	// The bad tenant, on the pool that failed to open, is refused -- over
	// REST (badConnArgs already targets https://, i.e. REST).
	restOut, restErr := runQRDB(t, cli, badConnArgs, "kv", "get", "k")
	if restErr == nil {
		t.Fatalf("kv get on the bad tenant over REST succeeded; want a typed refusal. output: %s", restOut)
	}
	assertVolumeUnavailableNotNotFound(t, "REST", restOut)

	// The same request over gRPC: swap only the --server scheme/port,
	// keeping the same CA/cert/key/timeout flags (cmd/qrdb/root.go's
	// --transport selection follows the --server scheme).
	grpcConnArgs := append([]string{"--server", fmt.Sprintf("grpcs://127.0.0.1:%d", grpcPort)}, badConnArgs[2:]...)
	grpcOut, grpcErr := runQRDB(t, cli, grpcConnArgs, "kv", "get", "k")
	if grpcErr == nil {
		t.Fatalf("kv get on the bad tenant over gRPC succeeded; want a typed refusal. output: %s", grpcOut)
	}
	assertVolumeUnavailableNotNotFound(t, "gRPC", grpcOut)

	// The degradation is visible in the node's own status/health surface,
	// not just in a failed data request: "broken" reports unhealthy in
	// volume-pool get, and the bad tenant carries a pool_unavailable
	// refusal in tenant get.
	poolOut, err := runQRDB(t, cli, sysConnArgs, "--output", "json", "volume-pool", "get", "broken")
	if err != nil {
		t.Fatalf("volume-pool get broken: %v\noutput: %s", err, poolOut)
	}
	if !strings.Contains(strings.ToLower(poolOut), "unavailable") {
		t.Fatalf("volume-pool get broken does not report it unavailable: %s", poolOut)
	}

	// The wire response itself carries the refusal (proves the server side
	// independent of the CLI's own JSON rendering, which has its own SDK
	// decode step per transport).
	rawResp, rawErr := sysClient.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s", adminPort, badID))
	if rawErr != nil {
		t.Fatalf("raw GET /v1/tenants/%s: %v", badID, rawErr)
	}
	rawBody, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	if !strings.Contains(string(rawBody), `"pool_unavailable"`) {
		t.Fatalf("raw REST GET /v1/tenants/%s (status %d) does not carry a pool_unavailable refusal: %s", badID, rawResp.StatusCode, rawBody)
	}

	// The qrdb CLI (REST transport, --output json) must show the same
	// refusal, not drop it in its own wire-to-SDK decode step.
	tenantOut, err := runQRDB(t, cli, sysConnArgs, "--output", "json", "tenant", "get", badID)
	if err != nil {
		t.Fatalf("tenant get %s: %v\noutput: %s", badID, err, tenantOut)
	}
	if !strings.Contains(tenantOut, "pool_unavailable") {
		t.Fatalf("tenant get %s does not carry a pool_unavailable refusal: %s", badID, tenantOut)
	}

	// The same, over gRPC: the client's own wire-to-SDK decode must not
	// drop the refusal either (sdk/go/transport/grpc shares tenantconv.
	// ToTenantInfo with the Unix transport).
	sysGRPCConnArgs := append([]string{"--server", fmt.Sprintf("grpcs://127.0.0.1:%d", grpcPort)}, sysConnArgs[2:]...)
	tenantGRPCOut, err := runQRDB(t, cli, sysGRPCConnArgs, "--output", "json", "tenant", "get", badID)
	if err != nil {
		t.Fatalf("tenant get %s over gRPC: %v\noutput: %s", badID, err, tenantGRPCOut)
	}
	if !strings.Contains(tenantGRPCOut, "pool_unavailable") {
		t.Fatalf("tenant get %s over gRPC does not carry a pool_unavailable refusal: %s", badID, tenantGRPCOut)
	}
}

// assertVolumeUnavailableNotNotFound asserts the qrdb CLI's output for a
// refused request names unavailability, not a not-found or a bare,
// unclassified internal error -- the owner's explicit requirement that a
// request for a database on a skipped pool must not look like the
// database simply doesn't exist.
func assertVolumeUnavailableNotNotFound(t *testing.T, proto, out string) {
	t.Helper()
	lower := strings.ToLower(out)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "404") {
		t.Fatalf("[%s] a tenant refused for pool unavailability must not look not-found: %s", proto, out)
	}
	if !strings.Contains(lower, "unavailable") {
		t.Fatalf("[%s] output does not name the refusal as unavailable: %s", proto, out)
	}
}
