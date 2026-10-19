//go:build integration

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

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// restartTenant is one tenant TestTenantBarrier_ProviderBackedSurvivesRestart_PerProtocol
// initialises before the restart and unseals after it, over one transport.
type restartTenant struct {
	id  string
	cli func(args ...string) (string, error)
}

// TestTenantBarrier_ProviderBackedSurvivesRestart_PerProtocol proves a tenant
// barrier whose strategy the node's key provider resolved comes back after a
// daemon restart. The node's system barrier is sealed with a passphrase, so
// the shell a restarted node registers for a tenant carries only the
// passphrase strategy; each tenant here is initialised with none, which only
// the key provider supplies. After the restart, each tenant's own identity
// unseals its barrier over one transport: the node reads the barrier's signed
// init statement, asks its provider for the strategy that statement names and
// unseals with that strategy alone. The tenant's role and user, written
// through the barrier before the restart, then authorise the same identity
// through RBAC again -- the barrier opens the data it sealed.
func TestTenantBarrier_ProviderBackedSurvivesRestart_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	tenants := make(map[string]restartTenant, len(multiProtocols))
	for _, proto := range multiProtocols {
		tenants[proto.name] = initProviderBackedTenant(t, node, proto)
	}

	node.restart(t, "[tenant-restart] ")

	for _, proto := range multiProtocols {
		tenant := tenants[proto.name]
		t.Run(proto.name, func(t *testing.T) {
			out, err := tenant.cli("tenant", "barrier-status", "--id", tenant.id)
			if err != nil {
				t.Fatalf("[%s] barrier-status after the restart: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "Sealed:      true") || !strings.Contains(out, "Initialized: true") {
				t.Fatalf("[%s] a self-managed barrier must come back sealed and initialized, got: %s", proto.name, out)
			}

			// none ignores the key, but the unseal call requires non-empty
			// key material; "bm9uZQ==" is base64("none").
			if out, err := tenant.cli("tenant", "barrier-unseal", "--id", tenant.id, "--key", "bm9uZQ=="); err != nil {
				t.Fatalf("[%s] barrier-unseal after the restart must resolve the stated strategy through the key provider: %v\noutput: %s", proto.name, err, out)
			}

			out, err = tenant.cli("tenant", "barrier-status", "--id", tenant.id)
			if err != nil {
				t.Fatalf("[%s] barrier-status through RBAC after the unseal: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "Sealed:      false") || !strings.Contains(out, "Strategy:    none") {
				t.Fatalf("[%s] barrier-status after the unseal should report an unsealed none barrier, got: %s", proto.name, out)
			}
		})
	}
}

// initProviderBackedTenant creates a self-managed raft-isolated tenant,
// initialises its barrier with none as its first admin over proto -- a signed
// init statement the node claims -- and provisions a role and user inside the
// tenant that grant the tenant's own identity the barrier operations.
func initProviderBackedTenant(t *testing.T, node *multiProtoNode, proto multiProtocol) restartTenant {
	t.Helper()
	tenantID := fmt.Sprintf("rst-%s-%d", proto.name, time.Now().UnixNano())

	adminCertPath, adminKeyPath := createTenantWithAdminCert(t, node, proto, tenantID, "Restart Tenant", "raft", false, tenantID+"-admin")
	initArgs := append(tenantCLIArgsForProto(node, proto, adminCertPath, adminKeyPath),
		"tenant", "barrier-init", "--id", tenantID, "--strategy", "none")
	if out, err := runCLI(t, node.cli, initArgs...); err != nil {
		t.Fatalf("[%s] barrier-init by the first admin: %v\noutput: %s", proto.name, err, out)
	}

	certPath, keyPath := node.ca.generateClientCertForTenant(t, tenantID)
	provisionTenantIdentityFor(t, node, tenantID, "barrier-admin-"+proto.name, []string{
		"tenant_barrier:status", "tenant_barrier:unseal", "admin:health",
	}, certPath)

	tenant := restartTenant{
		id: tenantID,
		cli: func(args ...string) (string, error) {
			return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, certPath, keyPath), args...)...)
		},
	}
	out, err := tenant.cli("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] barrier-status before the restart: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Sealed:      false") || !strings.Contains(out, "Strategy:    none") {
		t.Fatalf("[%s] barrier-status before the restart should report an unsealed none barrier, got: %s", proto.name, out)
	}
	return tenant
}
