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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A barrier tenant's pebble database stays open in the daemon after its
// writes, so its memtables and block cache hold every value in plaintext.
// Sealing the tenant barrier must still fail every engine's read closed on
// every transport: the open store refuses the read before it touches memory,
// as the file backend does, and no refusal carries the engine's marker.
func TestDatabaseBinding_CLI_BarrierTenantPebbleSealFailsReads_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "storage:\n  volume_pools:\n    - name: default\n      path: "+strconv.Quote(t.TempDir())+`
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
    allow_system_managed_barrier: true
`)

	const tenantID = "binding-pebble-vault"
	createVolumePoolCLI(t, node.cli, node.cliArgsFor(multiProtocols[0]), "default")
	createTenant(t, node, multiProtocols[0], tenantID, "Binding Pebble Vault", "barrier", true)
	perms := append(append([]string{}, dataPlanePermissions...), "tenant_barrier:status", "tenant_barrier:seal",
		"kv:range",
		"index:create", "index:query", "timeseries:write", "timeseries:query")
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", perms)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "pebble-sealed-" + proto.name
			kvKey := "barrier-binding-key-" + proto.name
			value := "barrier-binding-value-" + proto.name
			m := barrierBindingMarkersFor(proto.name)
			root := filepath.Join(node.dataDir, "tenants", tenantID, "databases", name)

			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "pebble"); err != nil {
				t.Fatalf("[%s] database create: %v\n%s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", kvKey, value); err != nil {
				t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
			}
			writeBarrierBindingEngines(t, node, conn, proto.name, name, m)
			// Control: every engine reads its marker back from the open
			// store, so the refusals below are not vacuous.
			for _, c := range barrierBindingReads(name, kvKey, value, m) {
				if out, err := runQRDB(t, node.cli, conn, c.args...); err != nil || !strings.Contains(out, c.want) {
					t.Fatalf("[%s] %s = %q, %v; want %q", proto.name, c.engine, out, err, c.want)
				}
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) == 0 {
				t.Fatalf("[%s] binding root %s is missing or empty (%v): the database is not in its own pebble store", proto.name, root, err)
			}
		})
	}

	conn := tenantCLIArgsForProto(node, multiProtocols[0], cert, key)
	if out, err := runQRDB(t, node.cli, conn, "tenant", "barrier-seal", "--id", tenantID); err != nil {
		t.Fatalf("[%s] tenant barrier-seal: %v\n%s", multiProtocols[0].name, err, out)
	}
	for _, via := range multiProtocols {
		conn := tenantCLIArgsForProto(node, via, cert, key)
		for _, owner := range multiProtocols {
			name := "pebble-sealed-" + owner.name
			kvKey := "barrier-binding-key-" + owner.name
			value := "barrier-binding-value-" + owner.name
			for _, c := range barrierBindingReads(name, kvKey, value, barrierBindingMarkersFor(owner.name)) {
				out, err := runQRDB(t, node.cli, conn, c.args...)
				if err == nil || strings.Contains(out, c.want) {
					t.Errorf("[%s] %s on %s after the tenant barrier sealed = %q, %v; want a refusal without %q",
						via.name, c.engine, name, out, err, c.want)
				}
			}
		}
	}
}
