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

// audit_tenant_confinement_cli_test.go proves `qrdb audit query --tenant-id`
// is confined to the caller's own tenant on every transport (go-qrdb#572),
// driven through the real client CLI binary (cmd/qrdb) against a live
// qrdbd node -- the CLI-and-real-daemon counterpart to the per-transport
// unit coverage of tenant.ConfineAuditFilter
// (pkg/api/{rest,grpc,quic,mcp}/..._test.go, sdk/go/transport/embedded).
// A tenant identity naming a foreign tenant is refused before any logger is
// read; naming its own tenant, or omitting --tenant-id entirely, succeeds.
package integration_test

import (
	"testing"
)

func TestAudit_CLI_TenantConfinement_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	const tenantID = "audit-acme"
	const foreignTenantID = "audit-globex"
	createTenant(t, node, multiProtocols[0], tenantID, "Audit Tenant", "prefix", false)

	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "audit-auditor",
		[]string{"audit:query", "admin:health"})

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

			// A foreign tenant_id is refused before any audit logger is read.
			assertCrossTenantIAMForbidden(t, node, proto, conn, "",
				"audit", "query", "--tenant-id", foreignTenantID)

			// Its own tenant_id, and an omitted one (which the server resolves
			// to the caller's own tenant, never every tenant's), both succeed.
			if out, err := runQRDB(t, node.cli, conn, "audit", "query", "--tenant-id", tenantID); err != nil {
				t.Fatalf("[%s] querying its own tenant's audit events: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, conn, "audit", "query"); err != nil {
				t.Fatalf("[%s] querying with no --tenant-id must default to its own tenant: %v\noutput: %s", proto.name, err, out)
			}
		})
	}
}
