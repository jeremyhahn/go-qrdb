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

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On a standalone node a prefix tenant's plain database is materialized in
// its own store under its binding root <data-dir>/tenants/<tenant>/
// databases/<name> (docs/databases/architecture.md): its keys are there and
// not in the tenant's default scope on the shared store, and DROP removes
// the root. Every transport serves it from its own host.
func TestDatabaseBinding_CLI_PrefixTenantDatabaseOwnStore_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "")

	const tenantID = "binding-acme"
	createTenant(t, node, multiProtocols[0], tenantID, "Binding Acme", "prefix", false)
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", dataPlanePermissions)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "bound-" + proto.name
			kvKey := "binding-key-" + proto.name
			value := "binding-value-" + proto.name
			root := filepath.Join(node.dataDir, "tenants", tenantID, "databases", name)

			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "file"); err != nil {
				t.Fatalf("[%s] database create: %v\n%s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", kvKey, value); err != nil {
				t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
			}
			out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", kvKey)
			if err != nil || !strings.Contains(out, value) {
				t.Fatalf("[%s] kv get = %q, %v; want %q", proto.name, out, err, value)
			}

			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("[%s] binding root %s: %v (the database was not materialized in its own store)", proto.name, root, err)
			}
			if len(entries) == 0 {
				t.Fatalf("[%s] binding root %s is empty after a write", proto.name, root)
			}

			if out, err := runQRDB(t, node.cli, conn, "kv", "get", kvKey); err == nil && strings.Contains(out, value) {
				t.Fatalf("[%s] the tenant's default scope serves the database's key: %q", proto.name, out)
			}

			if out, err := runQRDB(t, node.cli, conn, "database", "drop", name); err != nil {
				t.Fatalf("[%s] database drop: %v\n%s", proto.name, err, out)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("[%s] binding root %s survives DROP (stat err %v)", proto.name, root, err)
			}
		})
	}
}
