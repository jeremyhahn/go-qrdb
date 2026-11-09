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

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// strictHomeAuditYAML is a strict-floor cluster's tenant section: barrier
// tenants on the system-managed barrier, every tenant entitled to the
// replicated audit trail, and the volume pool the tenant's home group is
// placed through.
func strictHomeAuditYAML(volumePoolDir string) string {
	return fmt.Sprintf(`storage:
  volume_pools:
    - name: default
      path: %s
tenant:
  isolation_policy: strict
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
    allow_system_managed_barrier: true
    allow_dedicated_wal: true
    allow_replicated_audit: true
    allowed_volume_pools:
      - default
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
`, volumePoolDir)
}

// TestTenant_StrictHome_BarrierAuditTrail_PerProtocol proves, on a
// three-node strict cluster whose every replica runs the audit log instance,
// that a barrier tenant created with no prior chain keeps its replicated
// audit trail in its home group (docs/audit/architecture.md, the tenant's
// audit route):
//
//   - Over every CLI-selectable transport, each served by a different node in
//     turn, the tenant files an audit event and reads its audit instance
//     <tenant>/system/audit: the head grows, the newest leaf is the event, and
//     its inclusion proof verifies against the head.
//   - `qrdb audit verify --replicated` reports the tenant's chain continuous,
//     its head growing with each event.
//   - The chain is whole in the home group: it reaches genesis, holds every
//     sequence number up to its head, and the home log holds every record. A
//     record written to the system shard would leave a gap the home chain
//     cannot fold. (No daemon surface reads a tenant's system-shard segment
//     raw; the exact-key proof that nothing lands there is in-process, in
//     pkg/service's audit route tests.)
func TestTenant_StrictHome_BarrierAuditTrail_PerProtocol(t *testing.T) {
	logs := [3]*nodeLog{{}, {}, {}}
	nodes, _ := startMultiProtocolClusterWithConfig(t, 0, func(i int) multiProtoClusterNodeConfig {
		return multiProtoClusterNodeConfig{yaml: strictHomeAuditYAML(t.TempDir()), log: logs[i]}
	})

	// The route of a tenant with no prior chain is decided at its first
	// audit write: home once every replica runs the audit instance, the
	// system shard before. Each node logs the feature's activation.
	gateDeadline := time.Now().Add(12 * featuregate.DefaultRefreshInterval)
	for i, l := range logs {
		if !pollUntil(time.Until(gateDeadline), 200*time.Millisecond, func() bool {
			for line := range strings.Lines(l.String()) {
				if strings.Contains(line, "feature gate: feature activat") &&
					strings.Contains(line, string(featuregate.VerifiableLogAudit)) {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("node %d: feature gate never activated %s", i+1, featuregate.VerifiableLogAudit)
		}
	}

	creator := nodes[0]
	createVolumePoolCLI(t, creator.cli, creator.cliArgsFor(multiProtocols[0]), "default")
	waitForVolumePoolReady(t, mtlsHTTPClientFromFiles(t, creator.ca, creator.clientCert, creator.clientKey, 15*time.Second),
		creator.restPort, "default", len(nodes), 60*time.Second)

	const tenantID = "strict-audit"
	createTenant(t, creator, multiProtocols[0], tenantID, "Strict Audit", "barrier", true)
	certPath, keyPath := provisionTenantSOIdentity(t, creator, tenantID, "strict-audit-reader",
		[]string{"vlog:head", "vlog:get", "vlog:proof", "audit:verify_replicated", "admin:health"})
	adminCred, ok := lookupTenantAdminCred(tenantID)
	if !ok {
		t.Fatalf("no admin credential registered for %s", tenantID)
	}
	adminClient := mtlsHTTPClientFromFiles(t, creator.ca, adminCred.certPath, adminCred.keyPath, 15*time.Second)
	for _, node := range nodes {
		waitForNodeToTrustClient(t, adminClient, node.restPort, tenantAdminCompletionWindow)
	}

	auditLog := []string{"--database", "system", "--log", "audit"}
	var logSize, chainHead uint64
	for i, proto := range multiProtocols {
		node := nodes[i%len(nodes)]
		t.Run(proto.name, func(t *testing.T) {
			// The tenant admin onboards a user through this node: its create
			// and cert-binding records are the tenant's audit events.
			username := "strict-audit-" + proto.name
			userCert, _ := node.ca.generateClientCertForTenant(t, tenantID)
			createBoundUser(t, adminClient, node.restPort, map[string]any{
				"username":  username,
				"email":     username + "@" + tenantID + ".example.com",
				"tenant_id": tenantID,
				"role_ids":  []string{role.SystemRoleID(tenantID, role.RoleNameOperator)},
			}, userCert)

			conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
			vlogJSON := func(v any, args ...string) error {
				out, err := runQRDB(t, node.cli, conn, append([]string{"--output", "json", "vlog"}, append(args, auditLog...)...)...)
				if err != nil {
					return fmt.Errorf("vlog %s: %w\noutput: %s", args[0], err, out)
				}
				if err := json.Unmarshal([]byte(out), v); err != nil {
					return fmt.Errorf("decode vlog %s JSON: %w\noutput: %s", args[0], err, out)
				}
				return nil
			}

			var head vlogwire.Head
			var headErr error
			if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
				head = vlogwire.Head{}
				headErr = vlogJSON(&head, "head")
				return headErr == nil && head.Size > logSize
			}) {
				t.Fatalf("[%s] vlog head on %s/system/audit = size %d, %v; want it past %d", proto.name, tenantID, head.Size, headErr, logSize)
			}
			logSize = head.Size

			last := strconv.FormatUint(head.Size-1, 10)
			var leaves vlogwire.GetResult
			if err := vlogJSON(&leaves, "get", "--from", last, "--limit", "1"); err != nil {
				t.Fatalf("[%s] %v", proto.name, err)
			}
			if len(leaves.Entries) != 1 || leaves.Entries[0].Expired {
				t.Fatalf("[%s] vlog get on %s/system/audit = %+v, want the newest leaf's body", proto.name, tenantID, leaves.Entries)
			}
			var leafEvent auditEventCLIJSON
			if err := json.Unmarshal(leaves.Entries[0].Body, &leafEvent); err != nil {
				t.Fatalf("[%s] the audit instance's newest leaf is not an audit event: %v", proto.name, err)
			}

			var incl vlogwire.InclusionProof
			if err := vlogJSON(&incl, "prove-inclusion", "--index", last, "--size", strconv.FormatUint(head.Size, 10)); err != nil {
				t.Fatalf("[%s] %v", proto.name, err)
			}
			if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size,
				rfc6962.DefaultHasher.HashLeaf(leaves.Entries[0].Body), incl.Hashes, head.Root); err != nil {
				t.Fatalf("[%s] inclusion proof of %s/system/audit leaf %s does not verify against the head: %v", proto.name, tenantID, last, err)
			}

			out, err := runQRDB(t, node.cli, conn, "--output", "json", "audit", "verify", "--replicated")
			if err != nil {
				t.Fatalf("[%s] audit verify --replicated: %v\noutput: %s", proto.name, err, out)
			}
			var result transport.ChainContinuityResult
			if jerr := json.Unmarshal([]byte(out), &result); jerr != nil || !result.Verified || result.Head <= chainHead {
				t.Fatalf("[%s] audit verify --replicated = %+v, %v; want a continuous chain past head %d\noutput: %s", proto.name, result, jerr, chainHead, out)
			}
			if !result.Genesis || result.Low != 1 || result.Records != int64(result.Head) {
				t.Fatalf("[%s] audit verify --replicated = %+v; want every sequence number from genesis to the head in the home group", proto.name, result)
			}
			chainHead = result.Head

			var homeHead vlogwire.Head
			if err := vlogJSON(&homeHead, "head"); err != nil {
				t.Fatalf("[%s] %v", proto.name, err)
			}
			if homeHead.Size < uint64(result.Records) {
				t.Fatalf("[%s] %s/system/audit holds %d leaves, the chain %d records; records live outside the home group", proto.name, tenantID, homeHead.Size, result.Records)
			}
		})
	}
}
