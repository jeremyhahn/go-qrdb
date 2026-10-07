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

// audit_replicated_cluster_test.go proves the cluster-wide replicated audit
// chain (docs/audit/architecture.md#cluster-wide-replicated-trail) end to
// end against a real 3-node Raft cluster, driven through the real qrdbd
// daemon and qrdb client CLI binaries:
//
//   - An operation audited under a tenant entitled to the replicated chain
//     (AllowReplicatedAudit) produces a chain entry readable identically
//     from every node's own admin port, not just the one that handled the
//     write.
//   - The entry, and the chain's hash continuity, survive a full graceful
//     restart of all 3 nodes.
//   - A low --snapshot-entries threshold forces the state machine to take
//     snapshots during the test, so the restarted nodes reload the audit
//     chain table from their own on-disk SNAPSHOT (Chain.Restore on
//     snapshot recovery), not merely by replaying a log that was never
//     compacted.
//   - `qrdb audit verify --replicated` reports Verified: true throughout.
package integration_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeReplicatedAuditSealConfig writes a qrdbd YAML config with the seal
// barrier enabled (unencrypted "none" strategy, so initAndUnsealBarrier's
// empty passphrase applies) and default tenant entitlements that grant
// every tenant AllowReplicatedAudit -- the opt-in this feature's entitlement
// gate requires (docs/audit/architecture.md#opt-in-not-default-on) -- plus
// prefix isolation, which the tenant this test creates uses.
func writeReplicatedAuditSealConfig(t *testing.T) string {
	t.Helper()
	yaml := `seal:
  enabled: true
  strategy: none
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
    allow_replicated_audit: true
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeReplicatedAuditSealConfig: %v", err)
	}
	return cfgPath
}

func TestAuditReplicated_ThreeNodeCluster(t *testing.T) {
	bin := buildBinary(t)
	cliBin := buildCLIBinary(t)
	ca := generateTestCA(t)
	cfgPath := writeReplicatedAuditSealConfig(t)

	const nodeCount = 3
	raftPorts := allocatePorts(t, nodeCount)
	adminPorts := allocateTCPPorts(t, nodeCount)
	grpcPorts := allocateTCPPorts(t, nodeCount)
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, nodeCount)
	dataDirs := make([]string, nodeCount)
	for i := range nodeCount {
		dataDirs[i] = t.TempDir()
	}

	buildArgs := func(i int) []string {
		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			// Low thresholds force at least one real snapshot (and a
			// compacted log past it) during this test, so the restart
			// phase below exercises Chain.Restore from a snapshot, not
			// pure log replay.
			"--snapshot-entries", "10",
			"--compaction-overhead", "5",
		}
		return append(args, nodeTLS[i]...)
	}

	// ---------------------------------------------------------------------
	// Phase 1: start the 3-node cluster.
	// ---------------------------------------------------------------------
	cmds := make([]*exec.Cmd, nodeCount)
	for i := range nodeCount {
		cmds[i] = startNode(t, bin, buildArgs(i), fmt.Sprintf("[node%d-R1] ", i+1))
	}

	var sealWg sync.WaitGroup
	sealWg.Add(nodeCount)
	for i := range nodeCount {
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

	// protocol.OpAuditAppend is gated by featuregate.AuditAppend
	// (docs/audit/architecture.md#status-of-the-replicated-chain):
	// ReplicatedLogger.Emit refuses to propose it until every replica of the
	// shard has advertised support and the cluster feature gate has
	// activated, which polls on featuregate.DefaultRefreshInterval (5s).
	// Waiting a few cycles here avoids a flaky race between "the gate exists"
	// and "the gate has activated" for the very first write after a fresh
	// cluster forms.
	time.Sleep(4 * featuregate.DefaultRefreshInterval)

	// ---------------------------------------------------------------------
	// Phase 2: provision a tenant entitled to the replicated chain, and an
	// identity within it that can both query/verify the chain AND perform
	// the audited operation itself (role.create's audit event is stamped
	// with the AUTHENTICATED CALLER's tenant, pkg/role/audit.go's
	// emitAudit -- so the operation must be performed BY an "acme" identity,
	// not merely ABOUT one, for the event to land in acme's chain segment).
	// ---------------------------------------------------------------------
	const tenantID = "aud-acme"
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	adminKeyPEM, adminCSRPEM, err := certtest.NewCSR(tenantID + "-admin")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	tenantBody, tenantStatus := iraftAPIPost(t, sysClient, fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", leaderPort),
		map[string]interface{}{
			"id":             tenantID,
			"name":           "Audit Acme",
			"isolation_type": "prefix",
			"admin_username": tenantID + "-admin",
			"admin_csr_pem":  string(adminCSRPEM),
		})
	if tenantStatus != http.StatusCreated && tenantStatus != http.StatusOK {
		t.Fatalf("create tenant %s: status %d, body: %s", tenantID, tenantStatus, tenantBody)
	}

	// The system tenant cannot provision records inside another tenant's IAM
	// (owner ruling: ScopeSelf has no system-tenant lift), so the role and
	// user below are created as the tenant's own onboarded admin, which
	// self-assigns its pre-seeded SO role first (RolesCreate is SO-exclusive).
	var createResp struct {
		Admin struct {
			CertPEM string `json:"cert_pem"`
		} `json:"admin"`
	}
	if jsonErr := json.Unmarshal(tenantBody, &createResp); jsonErr != nil || createResp.Admin.CertPEM == "" {
		t.Fatalf("decode tenant create admin cert: %v body=%s", jsonErr, tenantBody)
	}
	adminTLSCert, err := tls.X509KeyPair([]byte(createResp.Admin.CertPEM), adminKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair for the onboarded tenant admin: %v", err)
	}
	tenantAdminClient := qrdbdharness.HTTPSClient(ca.Pool, &adminTLSCert, 15*time.Second)
	selfAssignTenantSO(t, leaderPort, tenantID, tenantAdminClient)

	roleBody, roleStatus := iraftAPIPost(t, tenantAdminClient, fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name":        "acme-auditor",
			"tenant_id":   tenantID,
			"permissions": []string{"audit:query", "audit:verify_replicated"},
		})
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role for tenant %s: status %d, body: %s", tenantID, roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if jsonErr := json.Unmarshal(roleBody, &roleResp); jsonErr != nil || roleResp.ID == "" {
		t.Fatalf("decode role id: %v body=%s", jsonErr, roleBody)
	}

	// generateClientCertForTenant always mints CN=admin (qrdbdharness.WithSubject),
	// so the bound user's username must be "admin" to match: auth.mtls_binding_policy
	// "required" resolves the username from the certificate's CN first and then
	// validates that user's thumbprint binding, rather than resolving straight
	// from the thumbprint alone.
	certPath, keyPath := ca.generateClientCertForTenant(t, tenantID)
	createBoundUser(t, tenantAdminClient, leaderPort,
		map[string]any{"username": "admin", "email": "auditor@acme.example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
		certPath)

	// ---------------------------------------------------------------------
	// Phase 3: the audited operation. Tenant onboarding itself
	// (pkg/service/tenant_onboarding.go) already produced it, back in Phase
	// 2: a "tenant.onboard" EmitGated record (outcome "permitted") followed
	// by a post-hoc "tenant.onboard" EmitBestEffort record (outcome
	// "success"), BOTH filed under TenantID == the tenant being onboarded
	// ("aud-acme") -- unlike pkg/role/user's own audit wiring, which this
	// lifecycle composition does not attach a sink to at all, tenant
	// onboarding's sink is infra.AuditLogger, the one this feature wraps
	// with the replicated child. The success record is what this test
	// follows through the replicated chain.
	// ---------------------------------------------------------------------

	// Pad the committed log past --snapshot-entries so a real snapshot is
	// taken (and, with --compaction-overhead, the log segment it was taken
	// from is eligible for compaction) before the restart phase below.
	for i := range 30 {
		httpPut(t, ca, leaderPort, fmt.Sprintf("pad/%04d", i), []byte("x"))
	}

	connArgsFor := func(port int) []string { return restConnArgs(ca, certPath, keyPath, port) }

	// assertReplicatedChain is the shared assertion: from EVERY node's own
	// admin port, the replicated chain for tenant "aud-acme" contains the
	// tenant-onboarding success event this test produced, and the chain
	// verifies.
	assertReplicatedChain := func(t *testing.T, label string) {
		t.Helper()
		for _, port := range adminPorts {
			port := port
			connArgs := connArgsFor(port)

			var page transport.AuditPage
			var lastOut string
			found := false
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) && !found {
				out, qerr := runQRDB(t, cliBin, connArgs, "--output", "json", "audit", "query",
					"--replicated", "--action", "tenant.onboard")
				lastOut = out
				if qerr == nil {
					if jsonErr := json.Unmarshal([]byte(out), &page); jsonErr == nil {
						for _, ev := range page.Events {
							if ev.Outcome == "success" && ev.TenantID == tenantID {
								found = true
								break
							}
						}
					}
				}
				if !found {
					time.Sleep(200 * time.Millisecond)
				}
			}
			if !found {
				t.Fatalf("[%s] port %d: replicated chain for tenant %s missing the tenant.onboard success entry; last output: %s",
					label, port, tenantID, lastOut)
			}

			vout, verr := runQRDB(t, cliBin, connArgs, "--output", "json", "audit", "verify", "--replicated")
			if verr != nil {
				t.Fatalf("[%s] port %d: audit verify --replicated: %v\noutput: %s", label, port, verr, vout)
			}
			var result transport.ChainContinuityResult
			if jsonErr := json.Unmarshal([]byte(vout), &result); jsonErr != nil {
				t.Fatalf("[%s] port %d: decode verify result: %v\noutput: %s", label, port, jsonErr, vout)
			}
			if !result.Verified {
				t.Fatalf("[%s] port %d: replicated chain did not verify: %+v", label, port, result)
			}
		}
	}

	// ---------------------------------------------------------------------
	// Phase 4: readable and verifying from every node, before restart.
	// ---------------------------------------------------------------------
	assertReplicatedChain(t, "before restart")

	// ---------------------------------------------------------------------
	// Phase 5: graceful full-cluster restart.
	// ---------------------------------------------------------------------
	t.Log("stopping all nodes...")
	for i, cmd := range cmds {
		if sigErr := cmd.Process.Signal(syscall.SIGTERM); sigErr != nil {
			t.Fatalf("signal node %d: %v", i+1, sigErr)
		}
	}
	for i, cmd := range cmds {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			t.Logf("node %d stopped", i+1)
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("node %d: timed out waiting for shutdown", i+1)
		}
	}

	t.Log("restarting all nodes...")
	cmds2 := make([]*exec.Cmd, nodeCount)
	for i := range nodeCount {
		cmd := startNode(t, bin, buildArgs(i), fmt.Sprintf("[node%d-R2] ", i+1))
		cmds2[i] = cmd
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		})
	}

	unsealNodes(t, ca, adminPorts...)
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// ---------------------------------------------------------------------
	// Phase 6: readable and verifying from every node, after restart --
	// proving the chain entry and its hash continuity survived both the
	// restart and whatever mix of snapshot-restore/log-replay rebuilt each
	// node's state machine.
	// ---------------------------------------------------------------------
	assertReplicatedChain(t, "after restart")
}
