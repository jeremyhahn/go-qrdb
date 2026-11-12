//go:build integration

// tenant_serving_set_cli_test.go drives strict tenant isolation phase 6
// (docs/multi-tenancy/engineering.md) through the real CLI on every
// transport: an isolated tenant's database groups are confined to its
// serving set, the nodes hosting its home group, so a scale-out that needs a
// node outside that set is refused with placement.ServingSetError.
package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTenantServingSet_ScaleOutBeyondServingSetRefused_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("serving-%s-%d", proto.name, time.Now().UnixNano())
			adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Serving Set Tenant", "raft", false, tenantID+"-admin")
			adminCLI := func(args ...string) (string, error) {
				return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, adminCert, adminKey), args...)...)
			}
			if out, err := adminCLI("tenant", "barrier-init", "--id", tenantID, "--passphrase", "serving-set-passphrase"); err != nil {
				t.Fatalf("[%s] tenant barrier-init: %v\noutput: %s", proto.name, err, out)
			}

			dbName := fmt.Sprintf("served-%s", proto.name)
			t.Cleanup(func() { _, _ = adminCLI("database", "drop", dbName) })
			if out, err := adminCLI("database", "create", dbName, "--backend", "raft", "--replicas", "1"); err != nil {
				t.Fatalf("[%s] database create %s --backend raft --replicas 1: %v\noutput: %s", proto.name, dbName, err, out)
			}

			// The tenant's serving set is this node, which already hosts
			// the group's only replica: a second replica needs a node
			// outside the serving set and must be refused as such.
			out, err := adminCLI("database", "scale", dbName, "--replicas", "2")
			if err == nil {
				t.Fatalf("[%s] database scale %s --replicas 2 succeeded, want a serving-set refusal (output: %s)", proto.name, dbName, out)
			}
			if !strings.Contains(strings.ToLower(out+err.Error()), "serving set") {
				t.Fatalf("[%s] database scale %s --replicas 2 error should mention %q, got: %s / %v", proto.name, dbName, "serving set", out, err)
			}
		})
	}
}

// TestTenantServingSet_ThreeNode_CLI_PerProtocol drives an isolated tenant on
// a three-node cluster. The tenant's home group is sized to the cluster when
// the tenant is created (ProvisionTenantHome places it with RF = cluster
// size), so its serving set is all three nodes: a database created through
// node A scales out onto every node of the serving set, and a scale-out
// needing a node beyond it is refused with the typed serving-set error.
func TestTenantServingSet_ThreeNode_CLI_PerProtocol(t *testing.T) {
	nodes := startSelfIssuedCAMultiProtocolCluster(t)
	const passphrase = "serving-set-three-node-passphrase"

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("serving3-%s-%d", proto.name, time.Now().UnixNano())
			adminCert, adminKey := createTenantWithAdminCert(t, nodes[0], proto, tenantID, "Serving Set Three Node", "raft", false, tenantID+"-admin")
			cliOn := func(n *multiProtoNode, args ...string) (string, error) {
				return runCLI(t, n.cli, append(tenantCLIArgsForProto(n, proto, adminCert, adminKey), args...)...)
			}
			// A barrier served by several nodes is initialised over a
			// network transport (the CLI refuses unix for it); init
			// unseals it on every node. It is retried while the cluster's
			// trust state for the new tenant settles.
			initProto := proto
			if proto.name == "unix" {
				initProto = multiProtocols[0]
			}
			initArgs := append(tenantCLIArgsForProto(nodes[0], initProto, adminCert, adminKey),
				"tenant", "barrier-init", "--id", tenantID, "--passphrase", passphrase)
			var initOut string
			var initErr error
			if !pollUntil(3*time.Minute, time.Second, func() bool {
				initOut, initErr = runCLI(t, nodes[0].cli, initArgs...)
				return initErr == nil || strings.Contains(strings.ToLower(initOut), "already initialized")
			}) {
				t.Fatalf("[%s] tenant barrier-init over %s: %v\noutput: %s", proto.name, initProto.name, initErr, initOut)
			}

			dbName := fmt.Sprintf("served3-%s", proto.name)
			t.Cleanup(func() { _, _ = cliOn(nodes[0], "database", "drop", dbName) })
			if out, err := cliOn(nodes[0], "database", "create", dbName, "--backend", "raft", "--replicas", "1"); err != nil {
				t.Fatalf("[%s] database create %s --replicas 1: %v\noutput: %s", proto.name, dbName, err, out)
			}

			// Scale-out within the serving set: every node hosts the home group.
			if out, err := cliOn(nodes[0], "database", "scale", dbName, "--replicas", "3"); err != nil {
				t.Fatalf("[%s] database scale %s --replicas 3 (inside the serving set): %v\noutput: %s", proto.name, dbName, err, out)
			}

			// A fourth replica needs a node outside the serving set.
			out, err := cliOn(nodes[0], "database", "scale", dbName, "--replicas", "4")
			if err == nil {
				t.Fatalf("[%s] database scale %s --replicas 4 succeeded, want a serving-set refusal (output: %s)", proto.name, dbName, out)
			}
			if !strings.Contains(strings.ToLower(out+err.Error()), "serving set") {
				t.Fatalf("[%s] database scale %s --replicas 4 error should mention %q, got: %s / %v", proto.name, dbName, "serving set", out, err)
			}
		})
	}
}
