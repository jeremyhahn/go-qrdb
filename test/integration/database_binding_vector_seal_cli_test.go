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
	"strconv"
	"strings"
	"testing"
)

// TestDatabaseBinding_CLI_BarrierTenantSealFailsVectorSearch_PerProtocol pins
// that a barrier tenant's database, whose vector index is warm from a search,
// refuses that search on every transport once the tenant barrier is sealed,
// and the refusal carries none of the upserted vector IDs.
func TestDatabaseBinding_CLI_BarrierTenantSealFailsVectorSearch_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "storage:\n  volume_pools:\n    - name: default\n      path: "+strconv.Quote(t.TempDir())+`
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
    allow_system_managed_barrier: true
`)

	const tenantID = "binding-vector-vault"
	createVolumePoolCLI(t, node.cli, node.cliArgsFor(multiProtocols[0]), "default")
	createTenant(t, node, multiProtocols[0], tenantID, "Binding Vector Vault", "barrier", true)
	perms := append(append([]string{}, dataPlanePermissions...), "tenant_barrier:status", "tenant_barrier:seal",
		"vector:namespace_create", "vector:upsert", "vector:search_similar")
	cert, key := provisionTenantIdentity(t, node, tenantID, "vector-admin", perms)

	// vectorIDFor is a vector ID no other output of the CLI carries.
	vectorIDFor := func(i int) string { return strconv.Itoa(917301 + i) }
	search := func(name string) []string {
		return []string{"--output", "json", "--database", name, "vector", "search",
			"--namespace", "sealed-ns", "--vector", "1,0", "--k", "1"}
	}

	for i, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "vsealed-" + proto.name
			id := vectorIDFor(i)
			steps := [][]string{
				{"database", "create", name, "--backend", "file"},
				{"--database", name, "vector", "namespace", "create",
					"--namespace", "sealed-ns", "--dimension", "2", "--distance-metric", "cosine", "--index-type", "hnsw"},
				{"--database", name, "vector", "upsert", "--namespace", "sealed-ns", "--id", id, "--vector", "1,0"},
			}
			for _, args := range steps {
				if out, err := runQRDB(t, node.cli, conn, args...); err != nil {
					t.Fatalf("[%s] %s: %v\n%s", proto.name, strings.Join(args, " "), err, out)
				}
			}
			// The warming search: the database's in-memory index now holds id.
			if out, err := runQRDB(t, node.cli, conn, search(name)...); err != nil || !strings.Contains(out, id) {
				t.Fatalf("[%s] vector search = %q, %v; want id %s", proto.name, out, err, id)
			}
		})
	}

	conn := tenantCLIArgsForProto(node, multiProtocols[0], cert, key)
	if out, err := runQRDB(t, node.cli, conn, "tenant", "barrier-seal", "--id", tenantID); err != nil {
		t.Fatalf("[%s] tenant barrier-seal: %v\n%s", multiProtocols[0].name, err, out)
	}
	// Every protocol's warm database is refused on every transport, and no
	// refusal carries an upserted ID.
	for _, via := range multiProtocols {
		conn := tenantCLIArgsForProto(node, via, cert, key)
		for i, owner := range multiProtocols {
			name := "vsealed-" + owner.name
			out, err := runQRDB(t, node.cli, conn, search(name)...)
			if err == nil || strings.Contains(out, vectorIDFor(i)) {
				t.Errorf("[%s] vector search on %s after the tenant barrier sealed = %q, %v; want a refusal without id %s",
					via.name, name, out, err, vectorIDFor(i))
			}
		}
	}
}
