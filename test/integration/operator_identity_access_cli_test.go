//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// operatorIdentityAccessProtocols are the transports whose user and role
// handlers take the tenant qualifier (strict-tenant-isolation phase 5).
var operatorIdentityAccessProtocols = []string{"rest", "grpc", "quic", "unix"}

// TestOperatorIdentityAccess_CLI_PerProtocol is phase 5's red test, driven
// through the real CLI: under operator-access, the platform operator's read
// of an isolated tenant's users succeeds and is recorded in both trails (the
// platform's and the tenant's); under a strict floor the same read is
// refused.
func TestOperatorIdentityAccess_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	open := startTenantEntitlementNode(t)
	strict := startTenantEntitlementNode(t, "isolation_policy: strict")

	for _, proto := range multiProtocols {
		proto := proto
		if !slices.Contains(operatorIdentityAccessProtocols, proto.name) {
			continue
		}
		t.Run(proto.name, func(t *testing.T) {
			// Under operator-access the read succeeds and both trails hold it.
			tenantID := fmt.Sprintf("opacc-%s-%d", proto.name, time.Now().UnixNano())
			adminCert, adminKey := createTenantWithAdminCert(t, open, proto, tenantID, "Operator Access Tenant", "barrier", true, tenantID+"-admin")
			if out, err := runCLIProto(t, open, proto, "users", "list", "--tenant-id", tenantID); err != nil {
				t.Fatalf("[%s] operator users list --tenant-id %s under operator-access: %v\noutput: %s", proto.name, tenantID, err, out)
			}
			if out, err := runCLIProto(t, open, proto, "roles", "list", "--tenant-id", tenantID); err != nil {
				t.Fatalf("[%s] operator roles list --tenant-id %s under operator-access: %v\noutput: %s", proto.name, tenantID, err, out)
			}
			trails := operatorAccessTrails(t, open, proto, tenantID, adminCert, adminKey)
			if !trails[tenantID] {
				t.Fatalf("[%s] the tenant's trail holds no record of the operator's read; trails %v", proto.name, trails)
			}
			if len(trails) < 2 {
				t.Fatalf("[%s] the platform trail holds no record of the operator's read; trails %v", proto.name, trails)
			}

			// Under a strict floor the read is refused.
			strictID := fmt.Sprintf("opstr-%s-%d", proto.name, time.Now().UnixNano())
			createTenantWithAdminCert(t, strict, proto, strictID, "Strict Tenant", "barrier", true, strictID+"-admin")
			out, err := runCLIProto(t, strict, proto, "users", "list", "--tenant-id", strictID)
			if err == nil {
				t.Fatalf("[%s] operator users list of a strict tenant succeeded: %s", proto.name, out)
			}
			if !strings.Contains(out, "strict") {
				t.Fatalf("[%s] refusal does not name the strict policy: %s", proto.name, out)
			}
		})
	}
}

// operatorAccessTrails returns the set of trails (audit tenant ids) holding
// a successful operator identity access record that targets tenantID: the
// platform trail as the operator reads it, and the tenant's trail as the
// tenant's own admin reads it (an operator may not query a tenant's trail).
func operatorAccessTrails(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID, adminCert, adminKey string) map[string]bool {
	t.Helper()
	// The tenant's first admin account is written after the create answers.
	waitForTenantFirstAdmin(t, node, proto, adminCert, adminKey)
	query := []string{"--output", "json", "audit", "query",
		"--action", "auth.operator_identity_access", "--limit", "1000"}
	readers := []struct {
		name string
		run  func(args ...string) (string, error)
	}{
		{"operator", func(args ...string) (string, error) { return runCLIProto(t, node, proto, args...) }},
		{"tenant admin", func(args ...string) (string, error) {
			return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, adminCert, adminKey), args...)...)
		}},
	}
	trails := map[string]bool{}
	for _, r := range readers {
		out, err := r.run(query...)
		if err != nil {
			t.Fatalf("[%s] audit query as the %s: %v\noutput: %s", proto.name, r.name, err, out)
		}
		var page auditPageCLIJSON
		if jerr := json.Unmarshal([]byte(out), &page); jerr != nil {
			t.Fatalf("[%s] decode audit query JSON as the %s: %v\noutput: %s", proto.name, r.name, jerr, out)
		}
		for _, e := range page.Events {
			if e.Attributes["target_tenant"] == tenantID && e.Outcome == "success" {
				trails[e.TenantID] = true
			}
		}
	}
	return trails
}
