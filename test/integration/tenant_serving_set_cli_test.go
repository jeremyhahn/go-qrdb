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
