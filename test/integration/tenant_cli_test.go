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

// tenant_cli_test.go drives the `qrdb tenant` COMMAND TREE (create, list,
// get, update, update-quotas, set-entitlement, delete) through the real
// client CLI binary (cmd/qrdb), against every transport (REST, gRPC, QUIC,
// MCP, Unix) sharing the multi_protocol_helpers_test.go harness. This closes
// the CLI-coverage-audit finding that every one of these subcommands had
// ZERO integration coverage of any kind: `tenant create` was previously
// exercised only single-protocol (https) and only for its --data-root flag
// (tenant_data_root_cli_test.go); list/get/update/update-quotas/
// set-entitlement/delete had no coverage at all.
//
// This file is scoped to the LIFECYCLE command surface and the entitlement
// gate on --isolation-type at create time. Cross-tenant / system-vs-tenant
// data-plane isolation correctness is proven separately in
// tenant_isolation_cli_test.go.
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tenantCLIQuotasJSON mirrors the subset of transport.TenantQuotas this file
// asserts on, decoded from `tenant ... --output json`.
type tenantCLIQuotasJSON struct {
	MaxDatabases    int64 `json:"max_databases"`
	MaxKeys         int64 `json:"max_keys"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxUsers        int64 `json:"max_users"`
	MaxRoles        int64 `json:"max_roles"`
}

// tenantCLIEntitlementsJSON mirrors the subset of transport.TenantEntitlements
// this file asserts on.
type tenantCLIEntitlementsJSON struct {
	AllowedIsolationModes     []string `json:"allowed_isolation_modes,omitempty"`
	AllowDedicatedRaft        bool     `json:"allow_dedicated_raft,omitempty"`
	AllowVolumeStorage        bool     `json:"allow_volume_storage,omitempty"`
	AllowSystemManagedBarrier bool     `json:"allow_system_managed_barrier,omitempty"`
	AllowedBarrierModes       []string `json:"allowed_barrier_modes,omitempty"`
}

// tenantCLIJSON mirrors transport.TenantInfo, decoded from the unwrapped
// JSON `tenant create|get|update|update-quotas|set-entitlement` print
// (cmd/qrdb/tenant.go's printTenant marshals the bare TenantInfo, not its
// enclosing Create/Get/Update*Response envelope).
type tenantCLIJSON struct {
	ID            string                     `json:"id"`
	Name          string                     `json:"name"`
	IsolationType string                     `json:"isolation_type,omitempty"`
	VolumePool    string                     `json:"volume_pool,omitempty"`
	Quotas        *tenantCLIQuotasJSON       `json:"quotas,omitempty"`
	Entitlements  *tenantCLIEntitlementsJSON `json:"entitlements,omitempty"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
}

// tenantListCLIJSON mirrors transport.ListTenantsResponse, the (wrapped)
// shape `tenant list --output json` actually prints.
type tenantListCLIJSON struct {
	Tenants []tenantCLIJSON `json:"tenants"`
}

// tenantAdminCLIJSON mirrors transport.TenantAdminCredential, the admin
// half of `tenant create --output json`'s response envelope.
type tenantAdminCLIJSON struct {
	Username   string `json:"username"`
	CertPEM    string `json:"cert_pem"`
	Thumbprint string `json:"thumbprint"`
}

// tenantCreateCLIJSON mirrors transport.CreateTenantResponse, the (wrapped)
// shape `tenant create --output json` prints: the tenant and its first
// admin, created together as one unit.
type tenantCreateCLIJSON struct {
	Tenant tenantCLIJSON      `json:"tenant"`
	Admin  tenantAdminCLIJSON `json:"admin"`
}

// TestTenant_CLI_Lifecycle_PerProtocol drives the full `qrdb tenant` lifecycle
// -- create, get, list, update, update-quotas, set-entitlement, delete --
// through the CLI binary against every transport sharing one multiProtoNode.
func TestTenant_CLI_Lifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantLifecycleOverProtocol(t, node, proto)
		})
	}
}

// testTenantLifecycleOverProtocol exercises the full tenant lifecycle
// command tree against a single protocol, using a tenant ID namespaced by
// protocol name so the five protocol subtests -- which share one node and
// therefore one tenant-metadata keyspace -- never collide.
func testTenantLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	tenantID := fmt.Sprintf("tlc-%s-%d", proto.name, time.Now().UnixNano())

	// --- create ("prefix" isolation: always permitted, no volume pool
	// or special entitlement needed) --------------------------------------
	adminArgs := adminCSRArgsForTenant(t, node, proto, tenantID+"-admin")
	out, err := runCLIProto(t, node, proto, append([]string{"--output", "json", "tenant", "create",
		"--id", tenantID,
		"--name", "Lifecycle Tenant",
		"--isolation-type", "prefix",
	}, adminArgs...)...)
	if err != nil {
		t.Fatalf("[%s] tenant create: %v\noutput: %s", proto.name, err, out)
	}
	var createResp tenantCreateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &createResp); jerr != nil {
		t.Fatalf("[%s] decode tenant create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	created := createResp.Tenant
	if created.ID != tenantID || created.Name != "Lifecycle Tenant" || created.IsolationType != "prefix" {
		t.Fatalf("[%s] tenant create result = %+v, want id=%s name=%q isolation=prefix", proto.name, created, tenantID, "Lifecycle Tenant")
	}

	// --- get ---------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get: %v\noutput: %s", proto.name, err, out)
	}
	var got tenantCLIJSON
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("[%s] decode tenant get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if got.ID != tenantID || got.Name != "Lifecycle Tenant" {
		t.Fatalf("[%s] tenant get = %+v, want id=%s name=%q", proto.name, got, tenantID, "Lifecycle Tenant")
	}

	// --- list ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "tenant", "list")
	if err != nil {
		t.Fatalf("[%s] tenant list: %v\noutput: %s", proto.name, err, out)
	}
	var listed tenantListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode tenant list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !tenantListContainsID(listed.Tenants, tenantID) {
		t.Fatalf("[%s] tenant list does not contain %s: %+v", proto.name, tenantID, listed.Tenants)
	}

	// --- update (name only; isolation-type held at its current value so
	// this never attempts the unsupported prefix->barrier/volume/raft
	// transition -- UpdateTenant only patches metadata, it performs none of
	// CreateTenant's barrier/volume-pool provisioning side effects) -------
	out, err = runCLIProto(t, node, proto, "--output", "json", "tenant", "update", tenantID,
		"--name", "Renamed Tenant",
		"--isolation-type", "prefix",
	)
	if err != nil {
		t.Fatalf("[%s] tenant update: %v\noutput: %s", proto.name, err, out)
	}
	var updated tenantCLIJSON
	if jerr := json.Unmarshal([]byte(out), &updated); jerr != nil {
		t.Fatalf("[%s] decode tenant update JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if updated.Name != "Renamed Tenant" || updated.IsolationType != "prefix" {
		t.Fatalf("[%s] tenant update = %+v, want name=%q isolation=prefix", proto.name, updated, "Renamed Tenant")
	}

	// --- update-quotas -------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "tenant", "update-quotas", tenantID,
		"--max-databases", "5",
		"--max-keys", "1000",
		"--max-storage-bytes", "1048576",
		"--max-users", "3",
		"--max-roles", "3",
		"--system-barrier-quota", "-1",
	)
	if err != nil {
		t.Fatalf("[%s] tenant update-quotas: %v\noutput: %s", proto.name, err, out)
	}
	var quotaUpdated tenantCLIJSON
	if jerr := json.Unmarshal([]byte(out), &quotaUpdated); jerr != nil {
		t.Fatalf("[%s] decode tenant update-quotas JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if quotaUpdated.Quotas == nil ||
		quotaUpdated.Quotas.MaxDatabases != 5 ||
		quotaUpdated.Quotas.MaxKeys != 1000 ||
		quotaUpdated.Quotas.MaxStorageBytes != 1048576 ||
		quotaUpdated.Quotas.MaxUsers != 3 ||
		quotaUpdated.Quotas.MaxRoles != 3 {
		t.Fatalf("[%s] tenant update-quotas result quotas = %+v, want {5 1000 1048576 3 3}", proto.name, quotaUpdated.Quotas)
	}

	// --- set-entitlement -------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "tenant", "set-entitlement", tenantID,
		"--isolation-modes", "prefix,barrier",
		"--allow-dedicated-raft",
		"--allow-volume-storage",
		"--allow-system-managed-barrier",
		"--allowed-barrier-modes", "none,passphrase",
	)
	if err != nil {
		t.Fatalf("[%s] tenant set-entitlement: %v\noutput: %s", proto.name, err, out)
	}
	var entUpdated tenantCLIJSON
	if jerr := json.Unmarshal([]byte(out), &entUpdated); jerr != nil {
		t.Fatalf("[%s] decode tenant set-entitlement JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if entUpdated.Entitlements == nil ||
		!stringSliceEqualUnordered(entUpdated.Entitlements.AllowedIsolationModes, []string{"prefix", "barrier"}) ||
		!entUpdated.Entitlements.AllowDedicatedRaft ||
		!entUpdated.Entitlements.AllowVolumeStorage ||
		!entUpdated.Entitlements.AllowSystemManagedBarrier ||
		!stringSliceEqualUnordered(entUpdated.Entitlements.AllowedBarrierModes, []string{"none", "passphrase"}) {
		t.Fatalf("[%s] tenant set-entitlement result entitlements = %+v", proto.name, entUpdated.Entitlements)
	}

	// --- delete ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "tenant", "delete", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] tenant delete: expected OK, got: %s", proto.name, out)
	}

	// --- get-after-delete: the tenant must be genuinely gone -------------
	out, err = runCLIProto(t, node, proto, "tenant", "get", tenantID)
	if err == nil {
		t.Fatalf("[%s] tenant get after delete should fail, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Fatalf("[%s] tenant get after delete: expected an error mentioning 'not found', got: %s", proto.name, out)
	}
}

// TestTenant_CLI_IsolationTypeGating_PerProtocol drives `tenant create
// --isolation-type` through every value (prefix, barrier, volume, raft) and
// the --barrier-auto-unseal flag, against every transport, on a node with NO
// tenant.default_entitlements configured and NO volume pool (matching
// startMultiProtocolNode's plain config). This proves the entitlement/
// infrastructure gates dispatch identically on every transport: prefix
// isolation is always permitted; every DEDICATED isolation mode (barrier,
// volume, raft) fails on a plain node, each for the specific reason the
// product actually enforces (verified empirically against a live server,
// not assumed from doc comments alone -- see the per-case notes below).
// TestTenant_CLI_IsolationTypeSuccess_PerProtocol proves the corresponding
// SUCCESS path on a node provisioned with a volume pool and permissive
// entitlements.
func TestTenant_CLI_IsolationTypeGating_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantIsolationTypeGatingOverProtocol(t, node, proto)
		})
	}
}

func testTenantIsolationTypeGatingOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("tig-%s-%d", proto.name, time.Now().UnixNano())

	// prefix: always permitted (the default entitlement).
	prefixID := base + "-prefix"
	prefixAdminArgs := adminCSRArgsForTenant(t, node, proto, prefixID+"-admin")
	out, err := runCLIProto(t, node, proto, append([]string{"--output", "json", "tenant", "create",
		"--id", prefixID, "--name", "Prefix Tenant", "--isolation-type", "prefix"}, prefixAdminArgs...)...)
	if err != nil {
		t.Fatalf("[%s] tenant create (prefix) should succeed on any node, got: %v\noutput: %s", proto.name, err, out)
	}

	// barrier, self-managed (no --barrier-auto-unseal): denied -- NOT for an
	// entitlement reason (registerDedicatedBarrier's self-managed path is
	// indeed entitlement-free, matching the doc comment on
	// pkg/tenant/service.go's IsolationBarrier case), but because
	// registerDedicatedBarrier always calls the registry's *Pooled*
	// constructor (RegisterTenantPooled / RegisterTenantPooledSystemManaged)
	// regardless of key-management mode, and this plain node has no
	// storage.volume_pools configured. Verified empirically against a live
	// server (pkg/seal/errors.go ErrPoolNotConfigured): every DEDICATED
	// isolation mode -- barrier included, not just volume/raft -- requires a
	// configured volume pool at the infrastructure level.
	barrierID := base + "-barrier"
	barrierAdminArgs := adminCSRArgsForTenant(t, node, proto, barrierID+"-admin")
	out, err = runCLIProto(t, node, proto, append([]string{"tenant", "create",
		"--id", barrierID, "--name", "Barrier Tenant", "--isolation-type", "barrier"}, barrierAdminArgs...)...)
	if err == nil {
		t.Fatalf("[%s] tenant create (barrier, self-managed) should be denied without a configured volume pool, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "pool") {
		t.Fatalf("[%s] tenant create (barrier) error should mention the missing volume pool, got: %s", proto.name, out)
	}

	// barrier + --barrier-auto-unseal: denied. This node carries no
	// tenant.default_entitlements config, so AllowSystemManagedBarrier
	// defaults to false (quota.DefaultEntitlements) and CreateTenant's
	// auto-unseal gate (quota.FeatureSystemManagedBarrier) rejects it.
	autoID := base + "-barrier-auto"
	autoAdminArgs := adminCSRArgsForTenant(t, node, proto, autoID+"-admin")
	out, err = runCLIProto(t, node, proto, append([]string{"tenant", "create",
		"--id", autoID, "--name", "Auto Barrier Tenant", "--isolation-type", "barrier", "--barrier-auto-unseal"}, autoAdminArgs...)...)
	if err == nil {
		t.Fatalf("[%s] tenant create (barrier, auto-unseal) should be denied by entitlement, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "entitle") {
		t.Fatalf("[%s] tenant create (barrier, auto-unseal) error should mention entitlement, got: %s", proto.name, out)
	}

	// volume: denied -- AllowedIsolationModes defaults to [prefix] only,
	// and this node has no configured volume pool either.
	volumeID := base + "-volume"
	volumeAdminArgs := adminCSRArgsForTenant(t, node, proto, volumeID+"-admin")
	out, err = runCLIProto(t, node, proto, append([]string{"tenant", "create",
		"--id", volumeID, "--name", "Volume Tenant", "--isolation-type", "volume"}, volumeAdminArgs...)...)
	if err == nil {
		t.Fatalf("[%s] tenant create (volume) should be denied without a configured volume pool/entitlement, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "entitle") {
		t.Fatalf("[%s] tenant create (volume) error should mention entitlement, got: %s", proto.name, out)
	}

	// raft: denied -- same AllowedIsolationModes gate as volume.
	raftID := base + "-raft"
	raftAdminArgs := adminCSRArgsForTenant(t, node, proto, raftID+"-admin")
	out, err = runCLIProto(t, node, proto, append([]string{"tenant", "create",
		"--id", raftID, "--name", "Raft Tenant", "--isolation-type", "raft"}, raftAdminArgs...)...)
	if err == nil {
		t.Fatalf("[%s] tenant create (raft) should be denied without a configured volume pool/entitlement, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "entitle") {
		t.Fatalf("[%s] tenant create (raft) error should mention entitlement, got: %s", proto.name, out)
	}
}

// writeTenantEntitlementConfig writes a daemon YAML config with the seal
// barrier enabled (passphrase strategy), a single-path volume pool, and
// permissive tenant.default_entitlements so every isolation type
// (prefix/barrier/volume/raft) and the --barrier-auto-unseal opt-in are
// actually createable on one node. registerDedicatedBarrier
// (pkg/tenant/service.go) always calls the barrier registry's *Pooled*
// constructor regardless of isolation type or key-management mode, so EVERY
// dedicated isolation mode needs a configured volume pool, not just
// volume/raft -- see TestTenant_CLI_IsolationTypeGating_PerProtocol's
// barrier-denial case for the empirical proof on a pool-less node.
func writeTenantEntitlementConfig(t *testing.T, volumePoolDir string) string {
	t.Helper()
	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: passphrase
storage:
  volume_pools:
    - name: default
      path: %s
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
      - volume
      - raft
    allow_volume_storage: true
    allow_dedicated_raft: true
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
		t.Fatalf("writeTenantEntitlementConfig: %v", err)
	}
	return cfgPath
}

// startTenantEntitlementNode boots a single-node qrdbd exactly like
// startMultiProtocolNode (every transport enabled, TLS, auth, a
// passphrase-strategy system barrier already initialized and unsealed) but
// using writeTenantEntitlementConfig's volume-pool + permissive-entitlement
// config instead of the shared harness's plain config, so every
// --isolation-type value is actually createable. It returns a
// *multiProtoNode so it drops directly into the same
// runCLIProto/cliArgsFor/serverURLFor helpers as the shared harness.
func startTenantEntitlementNode(t *testing.T) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	volumePoolDir := t.TempDir()
	cfgPath := writeTenantEntitlementConfig(t, volumePoolDir)

	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that, so place the socket under a short,
	// unique /tmp path (matches startMultiProtocolNode's identical
	// rationale).
	sockDir, err := os.MkdirTemp("", "qrdb-tent-")
	if err != nil {
		t.Fatalf("startTenantEntitlementNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	const passphrase = "tenant-entitlement-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	args = append(args, ca.tlsFlags(t)...)

	proc := startNode(t, bin, args, "[tenant-entitlement] ")

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	clientCert, clientKey := ca.boundAdminCert(t, dataDir)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		passphrase: passphrase,
		bin:        bin,
		startArgs:  args,
		proc:       proc,
	}

	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	// The replicated catalog has no pool until an explicit API call creates
	// one (docs/volume-pools/architecture.md D2), and registerDedicatedBarrier
	// always resolves through the pooled registration path regardless of
	// isolation type (see this function's own doc comment), so register the
	// node's configured "default" pool before any caller places a tenant on
	// it.
	createVolumePoolCLI(t, cli, n.cliArgsFor(multiProtocols[0]), "default")

	return n
}

// TestTenant_CLI_IsolationTypeSuccess_PerProtocol drives `tenant create
// --isolation-type` through every DEDICATED mode (barrier, volume, raft)
// PLUS the --barrier-auto-unseal opt-in, against every transport, on a node
// provisioned with a volume pool and permissive tenant.default_entitlements
// (startTenantEntitlementNode). This is the SUCCESS-path complement to
// TestTenant_CLI_IsolationTypeGating_PerProtocol's denial proofs, and
// together the two tests exhaustively cover --isolation-type's full value
// set (prefix/barrier/volume/raft) x --barrier-auto-unseal x all 5
// protocols x both the permit and deny outcomes.
func TestTenant_CLI_IsolationTypeSuccess_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantIsolationTypeSuccessOverProtocol(t, node, proto)
		})
	}
}

func testTenantIsolationTypeSuccessOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("tis-%s-%d", proto.name, time.Now().UnixNano())

	cases := []struct {
		isolationType string
		autoUnseal    bool
	}{
		{isolationType: "barrier", autoUnseal: false},
		{isolationType: "barrier", autoUnseal: true},
		{isolationType: "volume", autoUnseal: false},
		{isolationType: "volume", autoUnseal: true},
		{isolationType: "raft", autoUnseal: false},
		{isolationType: "raft", autoUnseal: true},
	}

	for _, tc := range cases {
		id := fmt.Sprintf("%s-%s-auto%v", base, tc.isolationType, tc.autoUnseal)
		adminArgs := adminCSRArgsForTenant(t, node, proto, id+"-admin")
		args := []string{"--output", "json", "tenant", "create",
			"--id", id, "--name", "Entitled Tenant", "--isolation-type", tc.isolationType}
		if tc.autoUnseal {
			args = append(args, "--barrier-auto-unseal")
		}
		args = append(args, adminArgs...)
		out, err := runCLIProto(t, node, proto, args...)
		if err != nil {
			t.Fatalf("[%s] tenant create (isolation=%s auto-unseal=%v) should succeed with a configured volume pool and entitlements, got: %v\noutput: %s",
				proto.name, tc.isolationType, tc.autoUnseal, err, out)
		}
		var createResp tenantCreateCLIJSON
		if jerr := json.Unmarshal([]byte(out), &createResp); jerr != nil {
			t.Fatalf("[%s] decode tenant create (isolation=%s) JSON: %v\noutput: %s", proto.name, tc.isolationType, jerr, out)
		}
		created := createResp.Tenant
		if created.IsolationType != tc.isolationType {
			t.Fatalf("[%s] tenant create (isolation=%s) IsolationType = %q, want %s", proto.name, tc.isolationType, created.IsolationType, tc.isolationType)
		}
		// Every isolation type here has a dedicated directory, and the
		// response names its pool: a barrier tenant's is the one the
		// registry placed its barrier on, which its tombstone keeps so a
		// node that missed an undo can still find the directory.
		if created.VolumePool == "" {
			t.Fatalf("[%s] tenant create (isolation=%s) VolumePool is empty, want a named pool", proto.name, tc.isolationType)
		}
	}
}

// tenantListContainsID reports whether tenants contains an entry with the
// given ID.
func tenantListContainsID(tenants []tenantCLIJSON, id string) bool {
	for _, tn := range tenants {
		if tn.ID == id {
			return true
		}
	}
	return false
}

// stringSliceEqualUnordered reports whether a and b contain the same
// elements irrespective of order (the server is not contractually obligated
// to preserve the caller's --isolation-modes / --allowed-barrier-modes
// ordering).
func stringSliceEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}
