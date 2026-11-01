// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//

//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// rolesReconcileAllCLIJSON is `qrdb roles reconcile --all-tenants --output
// json`'s report.
type rolesReconcileAllCLIJSON struct {
	Tenants []rolesReconcileCLIJSON `json:"tenants"`
	Skipped []struct {
		TenantID string `json:"tenant_id"`
		Reason   string `json:"reason"`
	} `json:"skipped"`
	Failed []struct {
		TenantID string `json:"tenant_id"`
		Error    string `json:"error"`
	} `json:"failed"`
}

// TestRoles_CLI_ReconcileAllTenants_PerProtocol drives the all-tenant built-in
// role reconcile through the CLI over every transport: the platform operator
// converges every tenant in one pass, idempotently, while a tenant's own admin
// and another tenant's security officer are refused.
//
// A spawned node offers no surface that writes a built-in role's stored
// permissions -- UpdateRole refuses a system role's grants, and the role
// records live under the reserved "__sys:" keyspace outside any user KV
// namespace -- so the stranded-record case (a built-in still holding a retired
// verb such as certs:generate) is staged against the real role store in
// pkg/role's TestReconcileAllTenants_ConvergesEveryTenantsStrandedBuiltIns.
// Here the node's freshly seeded tenants are already current, so convergence
// is a report naming each live tenant with no change, failure or second-pass
// write.
func TestRoles_CLI_ReconcileAllTenants_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRolesReconcileAllOverProtocol(t, node, proto)
		})
	}
}

func testRolesReconcileAllOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	ownTenant := "rra-" + suffix
	otherTenant := "rro-" + suffix
	createTenant(t, node, proto, ownTenant, "Reconcile All Tenant", "prefix", false)
	createTenant(t, node, proto, otherTenant, "Reconcile All Other", "prefix", false)

	ownCred, ok := lookupTenantAdminCred(ownTenant)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, ownTenant)
	}
	otherCred, ok := lookupTenantAdminCred(otherTenant)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, otherTenant)
	}
	awaitTenantAdminID(t, node.restPort, ownTenant,
		mtlsHTTPClientFromFiles(t, node.ca, ownCred.certPath, ownCred.keyPath, 15*time.Second))
	// The other tenant's admin seats an account holding that tenant's SO
	// role beside Admin, so the refusal below covers a security officer,
	// not only an administrator.
	_, otherSO := seatTenantProvisionerKeyPair(t, node.ca, node.restPort, otherTenant,
		mtlsHTTPClientFromFiles(t, node.ca, otherCred.certPath, otherCred.keyPath, 15*time.Second), true)

	// The platform operator: every live tenant converges, the system tenant
	// and both new tenants included, and nothing fails.
	for pass := 1; pass <= 2; pass++ {
		out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "reconcile", "--all-tenants")
		if err != nil {
			t.Fatalf("[%s] pass %d: roles reconcile --all-tenants: %v\noutput: %s", proto.name, pass, err, out)
		}
		var report rolesReconcileAllCLIJSON
		if jerr := json.Unmarshal([]byte(out), &report); jerr != nil {
			t.Fatalf("[%s] pass %d: decode roles reconcile --all-tenants JSON: %v\noutput: %s", proto.name, pass, jerr, out)
		}
		if len(report.Failed) != 0 {
			t.Fatalf("[%s] pass %d: failed tenants %+v, want none", proto.name, pass, report.Failed)
		}
		seen := map[string]bool{}
		for _, tr := range report.Tenants {
			seen[tr.TenantID] = true
			if len(tr.Roles) != 0 {
				t.Fatalf("[%s] pass %d: tenant %s reported changes %+v, want its seeded built-ins already current",
					proto.name, pass, tr.TenantID, tr.Roles)
			}
		}
		for _, id := range []string{"system", ownTenant, otherTenant} {
			if !seen[id] {
				t.Fatalf("[%s] pass %d: report has no tenant %s: %+v", proto.name, pass, id, report.Tenants)
			}
		}
	}

	// The converged built-ins hold the registry's grants, the retired
	// certs:generate not among them.
	ownConn := tenantCLIArgsForProto(node, proto, ownCred.certPath, ownCred.keyPath)
	for _, name := range []string{role.RoleNameAdmin, role.RoleNameOperator} {
		got := runRoleCLI(t, node, proto, ownConn, "get", role.SystemRoleID(ownTenant, name))
		if !got.System || len(got.Permissions) == 0 || slices.Contains(got.Permissions, "certs:generate") {
			t.Fatalf("[%s] tenant %s built-in %s after reconcile = %+v", proto.name, ownTenant, name, got)
		}
	}

	// A tenant's own administrator holds roles:create in its tenant but not
	// the cross-tenant roles:reconcile_all, so it is refused.
	out, err := runQRDB(t, node.cli, ownConn,
		"--output", "json", "roles", "reconcile", "--all-tenants")
	if err == nil {
		t.Fatalf("[%s] tenant %s admin ran roles reconcile --all-tenants, want refused\noutput: %s", proto.name, ownTenant, out)
	}

	// Another tenant's security officer is refused the same way.
	out, err = runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, otherSO.CertPath, otherSO.KeyPath),
		"--output", "json", "roles", "reconcile", "--all-tenants")
	if err == nil {
		t.Fatalf("[%s] tenant %s SO ran roles reconcile --all-tenants, want refused\noutput: %s", proto.name, otherTenant, out)
	}
}
