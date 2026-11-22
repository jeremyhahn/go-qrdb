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

// On a standalone node a prefix tenant's plain database is materialized in
// its own store under its binding root <data-dir>/tenants/<tenant>/
// databases/<name> (docs/databases/architecture.md): its keys are there and
// not in the tenant's default scope on the shared store, and DROP removes
// the root. Every transport serves it from its own host.
func TestDatabaseBinding_CLI_PrefixTenantDatabaseOwnStore_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "")

	const tenantID = "binding-acme"
	createTenant(t, node, multiProtocols[0], tenantID, "Binding Acme", "prefix", false)
	perms := append(append([]string{}, dataPlanePermissions...), "databases:delete")
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", perms)

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

// A barrier tenant's database on a standalone node is materialized in its
// own store under the binding root, written through the tenant's barrier:
// sealing that barrier fails the database's data closed.
func TestDatabaseBinding_CLI_BarrierTenantDatabaseOwnStore_PerProtocol(t *testing.T) {
	// A system-managed (auto-unsealed) tenant barrier needs its entitlement
	// and a volume pool for the barrier registry to keep its keys in.
	node := startStandaloneModeNodeWithConfig(t, "pebble", "storage:\n  volume_pools:\n    - name: default\n      path: "+strconv.Quote(t.TempDir())+`
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
    allow_system_managed_barrier: true
`)

	const tenantID = "binding-vault"
	createVolumePoolCLI(t, node.cli, node.cliArgsFor(multiProtocols[0]), "default")
	createTenant(t, node, multiProtocols[0], tenantID, "Binding Vault", "barrier", true)
	perms := append(append([]string{}, dataPlanePermissions...), "tenant_barrier:status", "tenant_barrier:seal")
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", perms)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "sealed-" + proto.name
			kvKey := "barrier-binding-key-" + proto.name
			value := "barrier-binding-value-" + proto.name
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
		})
	}

	proto := multiProtocols[0]
	conn := tenantCLIArgsForProto(node, proto, cert, key)
	if out, err := runQRDB(t, node.cli, conn, "tenant", "barrier-seal", "--id", tenantID); err != nil {
		t.Fatalf("[%s] tenant barrier-seal: %v\n%s", proto.name, err, out)
	}
	name := "sealed-" + proto.name
	value := "barrier-binding-value-" + proto.name
	out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", "barrier-binding-key-"+proto.name)
	if err == nil || strings.Contains(out, value) {
		t.Fatalf("[%s] kv get after the tenant barrier sealed = %q, %v; want a refusal without the value", proto.name, out, err)
	}
}

// TestDatabaseBinding_CLI_SystemTenantDatabaseOwnStore_PerProtocol drives
// the system tenant's databases on a standalone node: a database at the
// default root lives under <data-dir>/tenants/system/databases/<name>, one
// created with --data-dir lives in that directory, and DROP removes it.
func TestDatabaseBinding_CLI_SystemTenantDatabaseOwnStore_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			assertOwnStore := func(name, root, kvKey, value string) {
				t.Helper()
				if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", kvKey, value); err != nil {
					t.Fatalf("[%s] kv put %s: %v\n%s", proto.name, name, err, out)
				}
				out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", kvKey)
				if err != nil || !strings.Contains(out, value) {
					t.Fatalf("[%s] kv get %s = %q, %v; want %q", proto.name, name, out, err, value)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatalf("[%s] binding root %s: %v (the database was not materialized in its own store)", proto.name, root, err)
				}
				if len(entries) == 0 {
					t.Fatalf("[%s] binding root %s is empty after a write", proto.name, root)
				}
				if out, err := runQRDB(t, node.cli, conn, "kv", "get", kvKey); err == nil && strings.Contains(out, value) {
					t.Fatalf("[%s] the system database serves %s's key: %q", proto.name, name, out)
				}
			}

			sys := "sys-" + proto.name
			if out, err := runQRDB(t, node.cli, conn, "database", "create", sys, "--backend", "file"); err != nil {
				t.Fatalf("[%s] database create %s: %v\n%s", proto.name, sys, err, out)
			}
			assertOwnStore(sys, filepath.Join(node.dataDir, "tenants", "system", "databases", sys),
				"sys-key-"+proto.name, "sys-value-"+proto.name)

			dir := "dir-" + proto.name
			explicit := filepath.Join(t.TempDir(), dir)
			if out, err := runQRDB(t, node.cli, conn, "database", "create", dir, "--backend", "pebble", "--data-dir", explicit); err != nil {
				t.Fatalf("[%s] database create %s: %v\n%s", proto.name, dir, err, out)
			}
			assertOwnStore(dir, explicit, "dir-key-"+proto.name, "dir-value-"+proto.name)
			if out, err := runQRDB(t, node.cli, conn, "database", "drop", dir); err != nil {
				t.Fatalf("[%s] database drop %s: %v\n%s", proto.name, dir, err, out)
			}
			if _, err := os.Stat(explicit); !os.IsNotExist(err) {
				t.Fatalf("[%s] explicit data directory %s survives DROP (stat err %v)", proto.name, explicit, err)
			}
		})
	}
}

// TestDatabaseBinding_CLI_ProvisionedFourBackendsOwnStores_PerProtocol is the
// "Node defaults" acceptance of engineering.md "Binding materialization": a
// standalone node provisioned with four databases on memory, file, pebble
// and objstore puts each database's files where its binding says and
// nowhere else. storage.objstore sets a local path, which places the node's
// own store only: the objstore database lives under its binding root.
func TestDatabaseBinding_CLI_ProvisionedFourBackendsOwnStores_PerProtocol(t *testing.T) {
	shared := t.TempDir()
	backends := []struct{ name, backend string }{
		{"nd-memory", "memory"},
		{"nd-file", "file"},
		{"nd-pebble", "pebble"},
		{"nd-objstore", "objstore"},
	}
	extra := "storage:\n  objstore:\n    provider: local\n    settings:\n      path: " + strconv.Quote(shared) + "\n" +
		"databases:\n"
	for _, b := range backends {
		extra += "  - name: " + strconv.Quote(b.name) + "\n    backend: " + b.backend + "\n"
	}
	node := startStandaloneModeNodeWithConfig(t, "objstore", extra)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			for _, b := range backends {
				key, value := b.name+"-key-"+proto.name, b.name+"-value-"+proto.name
				if out, err := runQRDB(t, node.cli, conn, "--database", b.name, "kv", "put", key, value); err != nil {
					t.Fatalf("[%s] kv put %s: %v\n%s", proto.name, b.name, err, out)
				}
				out, err := runQRDB(t, node.cli, conn, "--database", b.name, "kv", "get", key)
				if err != nil || !strings.Contains(out, value) {
					t.Fatalf("[%s] kv get %s = %q, %v; want %q", proto.name, b.name, out, err, value)
				}
				if info := cliDatabaseInfo(t, node, proto, b.name); string(info.Backend) != b.backend {
					t.Fatalf("[%s] database info %s backend = %q, want %q", proto.name, b.name, info.Backend, b.backend)
				}
			}
		})
	}

	nonEmpty := func(dir string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("%s is missing or empty (%v)", dir, err)
		}
	}
	databases := filepath.Join(node.dataDir, "tenants", "system", "databases")
	for _, name := range []string{"nd-file", "nd-pebble", "nd-objstore"} {
		nonEmpty(filepath.Join(databases, name))
	}
	nonEmpty(filepath.Join(databases, "nd-objstore", "objstore"))
	if _, err := os.Stat(filepath.Join(databases, "nd-memory")); !os.IsNotExist(err) {
		t.Fatalf("a directory exists for nd-memory (%v)", err)
	}
	for _, proto := range multiProtocols {
		conn := node.cliArgsFor(proto)
		for _, b := range backends {
			key, value := b.name+"-key-"+proto.name, b.name+"-value-"+proto.name
			if out, err := runQRDB(t, node.cli, conn, "kv", "get", key); err == nil && strings.Contains(out, value) {
				t.Fatalf("[%s] the system database serves %s's key: %q", proto.name, b.name, out)
			}
		}
	}
}
