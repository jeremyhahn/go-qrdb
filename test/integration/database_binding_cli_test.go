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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
// KV, SQL, an explicit index and time series answer on every transport, and
// sealing that barrier fails every one of them closed on every transport.
// Collections are absent: no built-in tenant role holds collections:*, so
// a tenant identity cannot be granted them (the store test covers them).
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
	// sql query is authorized as a KV range read (kv:range): the ad-hoc SQL
	// surface is the read-only kv table, so the SQL cell reads the KV marker
	// back through SQL.
	perms := append(append([]string{}, dataPlanePermissions...), "tenant_barrier:status", "tenant_barrier:seal",
		"kv:range",
		"index:create", "index:query", "timeseries:write", "timeseries:query")
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", perms)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "sealed-" + proto.name
			kvKey := "barrier-binding-key-" + proto.name
			value := "barrier-binding-value-" + proto.name
			m := barrierBindingMarkersFor(proto.name)
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
			writeBarrierBindingEngines(t, node, conn, proto.name, name, m)
			for _, c := range barrierBindingReads(name, kvKey, value, m) {
				if out, err := runQRDB(t, node.cli, conn, c.args...); err != nil || !strings.Contains(out, c.want) {
					t.Fatalf("[%s] %s = %q, %v; want %q", proto.name, c.engine, out, err, c.want)
				}
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
	// Every engine of every protocol's database is refused on every
	// transport, and no refusal carries the engine's marker.
	for _, via := range multiProtocols {
		conn := tenantCLIArgsForProto(node, via, cert, key)
		for _, owner := range multiProtocols {
			name := "sealed-" + owner.name
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

// barrierBindingMarkers are the plaintext values one protocol's database
// holds on each engine besides KV, so a leak names the engine it came from.
type barrierBindingMarkers struct {
	entityKey, entity, index, metric, ts string
}

func barrierBindingMarkersFor(proto string) barrierBindingMarkers {
	return barrierBindingMarkers{
		entityKey: "barrier-binding-entity-" + proto,
		entity:    "barrier-binding-indexed-" + proto,
		index:     "barrier-binding-idx-" + proto,
		metric:    "barrier.binding." + proto,
		ts:        "barrier-binding-ts-" + proto,
	}
}

// writeBarrierBindingEngines writes database name's explicit index, the KV
// entity it indexes and its time-series point.
func writeBarrierBindingEngines(t *testing.T, node *multiProtoNode, conn []string, proto, name string, m barrierBindingMarkers) {
	t.Helper()
	db := []string{"--database", name}
	run := func(what string, args ...string) string {
		t.Helper()
		out, err := runQRDB(t, node.cli, conn, append(append([]string{}, db...), args...)...)
		if err != nil {
			t.Fatalf("[%s] %s: %v\n%s", proto, what, err, out)
		}
		return out
	}
	run("index create", "index", "create", m.index, "--field", "secret")
	run("kv put (indexed entity)", "kv", "put", m.entityKey, `{"secret":"`+m.entity+`"}`)
	run("ts write", "ts", "write", "--metric", m.metric, "--value", "42.5", "--tag", "host="+m.ts)
}

// barrierBindingRead is one engine's read of database name and the marker it
// must return.
type barrierBindingRead struct {
	engine, want string
	args         []string
}

// barrierBindingReads reads every engine except vector back from database
// name by exact key, ID, index value or metric.
func barrierBindingReads(name, kvKey, value string, m barrierBindingMarkers) []barrierBindingRead {
	db := []string{"--database", name}
	with := func(args ...string) []string { return append(append([]string{}, db...), args...) }
	return []barrierBindingRead{
		{engine: "kv get", want: value, args: with("kv", "get", kvKey)},
		{engine: "sql query", want: value, args: []string{"sql", "query", "--database", name,
			"SELECT * FROM kv WHERE `key` = '" + kvKey + "'"}},
		{engine: "index query", want: m.entityKey, args: with("index", "query", m.index, "--value", m.entity)},
		{engine: "ts query", want: m.ts, args: with("ts", "query", "--metric", m.metric, "--start", "0", "--end", "9223372036854775807")},
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

// TestDatabaseBinding_CLI_InfoReportsTheBinding_PerProtocol drives
// `database info` and `database list` over every transport and asserts the
// read-only binding block: a standalone node binds a file database locally,
// encrypted under the node or tenant scope, and the table output prints the
// same block.
func TestDatabaseBinding_CLI_InfoReportsTheBinding_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "")

	assertBinding := func(t *testing.T, protoName, where string, b *transport.DatabaseBinding) {
		t.Helper()
		if b == nil {
			t.Fatalf("[%s] %s carries no binding block", protoName, where)
		}
		if b.Kind != "local" {
			t.Fatalf("[%s] %s binding kind = %q, want local", protoName, where, b.Kind)
		}
		if b.Encryption != "node" && b.Encryption != "tenant" {
			t.Fatalf("[%s] %s binding encryption = %q, want node or tenant", protoName, where, b.Encryption)
		}
	}

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			name := "bound-" + proto.name
			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "file"); err != nil {
				t.Fatalf("[%s] database create %s: %v\n%s", proto.name, name, err, out)
			}

			out, err := runQRDB(t, node.cli, conn, "--output", "json", "database", "info", name)
			if err != nil {
				t.Fatalf("[%s] database info %s: %v\n%s", proto.name, name, err, out)
			}
			var info transport.DatabaseInfo
			if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
				t.Fatalf("[%s] decode database info JSON: %v\n%s", proto.name, jerr, out)
			}
			assertBinding(t, proto.name, "database info", info.Binding)

			out, err = runQRDB(t, node.cli, conn, "--output", "json", "database", "list")
			if err != nil {
				t.Fatalf("[%s] database list: %v\n%s", proto.name, err, out)
			}
			var list []transport.DatabaseInfo
			if jerr := json.Unmarshal([]byte(out), &list); jerr != nil {
				t.Fatalf("[%s] decode database list JSON: %v\n%s", proto.name, jerr, out)
			}
			found := false
			for i := range list {
				if list[i].Name == name {
					found = true
					assertBinding(t, proto.name, "database list", list[i].Binding)
				}
			}
			if !found {
				t.Fatalf("[%s] database list does not name %s:\n%s", proto.name, name, out)
			}

			out, err = runQRDB(t, node.cli, conn, "database", "info", name)
			if err != nil {
				t.Fatalf("[%s] database info (table) %s: %v\n%s", proto.name, name, err, out)
			}
			if !strings.Contains(out, "Binding:") || !strings.Contains(out, "Kind:       local") {
				t.Fatalf("[%s] table output lacks the binding block:\n%s", proto.name, out)
			}
		})
	}
}

// TestDatabaseBinding_CLI_RaftModeLocalBackendOwnStore_PerProtocol proves a
// raft-mode node materializes a local binding the way a standalone node does:
// a pebble database of the system tenant is served from its own store under
// the node's data directory, `database info` reports a local binding, and
// dropping the database removes the store, over every transport.
func TestDatabaseBinding_CLI_RaftModeLocalBackendOwnStore_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			name := "raftlocal-" + proto.name
			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "pebble"); err != nil {
				t.Fatalf("[%s] database create %s: %v\n%s", proto.name, name, err, out)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", "k1", "v1"); err != nil {
				t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
			}
			out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", "k1")
			if err != nil || !strings.Contains(out, "v1") {
				t.Fatalf("[%s] kv get = %q, %v; want v1", proto.name, out, err)
			}

			out, err = runQRDB(t, node.cli, conn, "--output", "json", "database", "info", name)
			if err != nil {
				t.Fatalf("[%s] database info %s: %v\n%s", proto.name, name, err, out)
			}
			var info transport.DatabaseInfo
			if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
				t.Fatalf("[%s] decode database info JSON: %v\n%s", proto.name, jerr, out)
			}
			if info.Binding == nil || info.Binding.Kind != "local" {
				t.Fatalf("[%s] database info binding = %+v, want kind local\n%s", proto.name, info.Binding, out)
			}

			root := filepath.Join(node.dataDir, "tenants", "system", "databases", name)
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) == 0 {
				t.Fatalf("[%s] the database's own store %s is missing or empty (%d entries, %v)", proto.name, root, len(entries), err)
			}

			if out, err := runQRDB(t, node.cli, conn, "database", "drop", name); err != nil {
				t.Fatalf("[%s] database drop %s: %v\n%s", proto.name, name, err, out)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("[%s] the dropped database's store %s still exists (%v)", proto.name, root, err)
			}
		})
	}
}

// TestDatabaseBinding_CLI_PrefixTenantRaftDatabaseOwnGroup_PerProtocol: on
// a raft-mode node, a backend: raft database of a prefix tenant and of a
// barrier tenant runs its own Raft group (OD-1 A) on every transport: its
// group's store sits under {DataDir}/tenants/<tenant>/databases/<db>, the
// binding block reports kind raft, the replica nodes and the encryption
// scope, drop removes the group's directory, and a recreate is empty with a
// higher generation.
func TestDatabaseBinding_CLI_PrefixTenantRaftDatabaseOwnGroup_PerProtocol(t *testing.T) {
	// A backend: raft database needs the raft isolation entitlement, an
	// auto-unsealed barrier tenant its own, and the raft backend factory a
	// volume pool for the barrier registry.
	node := startMultiProtocolNodeWithConfig(t, "mvcc", "storage:\n  volume_pools:\n    - name: default\n      path: "+strconv.Quote(t.TempDir())+`
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
      - raft
    allow_dedicated_raft: true
    allow_system_managed_barrier: true
`)
	createVolumePoolCLI(t, node.cli, node.cliArgsFor(multiProtocols[0]), "default")

	tenants := []struct {
		id, isolation, encryption string
		autoUnseal                bool
	}{
		{"raftgrp-prefix", "prefix", "node", false},
		{"raftgrp-vault", "barrier", "tenant", true},
	}
	perms := append(append([]string{}, dataPlanePermissions...), "databases:delete")
	for _, tn := range tenants {
		createTenant(t, node, multiProtocols[0], tn.id, "Raft Group "+tn.isolation, tn.isolation, tn.autoUnseal)
		cert, key := provisionTenantIdentity(t, node, tn.id, "db-admin", perms)
		for _, proto := range multiProtocols {
			proto := proto
			tn := tn
			t.Run(tn.isolation+"/"+proto.name, func(t *testing.T) {
				conn := tenantCLIArgsForProto(node, proto, cert, key)
				name := "grp-" + proto.name
				root := filepath.Join(node.dataDir, "tenants", tn.id, "databases", name)
				info := func() transport.DatabaseInfo {
					t.Helper()
					out, err := runQRDB(t, node.cli, conn, "--output", "json", "database", "info", name)
					if err != nil {
						t.Fatalf("[%s] database info %s: %v\n%s", proto.name, name, err, out)
					}
					var di transport.DatabaseInfo
					if jerr := json.Unmarshal([]byte(out), &di); jerr != nil {
						t.Fatalf("[%s] decode database info JSON: %v\n%s", proto.name, jerr, out)
					}
					if di.Binding == nil {
						t.Fatalf("[%s] database info has no binding block\n%s", proto.name, out)
					}
					return di
				}

				if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "raft"); err != nil {
					t.Fatalf("[%s] database create: %v\n%s", proto.name, err, out)
				}
				if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", "k1", "v1"); err != nil {
					t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
				}
				out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", "k1")
				if err != nil || !strings.Contains(out, "v1") {
					t.Fatalf("[%s] kv get = %q, %v; want v1", proto.name, out, err)
				}

				first := info()
				if first.Binding.Kind != "raft" || first.Binding.Encryption != tn.encryption || len(first.Binding.Nodes) == 0 {
					t.Fatalf("[%s] binding = %+v, want kind raft, encryption %s and the replica nodes", proto.name, first.Binding, tn.encryption)
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) == 0 {
					t.Fatalf("[%s] the database's own group %s is missing or empty (%d entries, %v)", proto.name, root, len(entries), err)
				}
				if !strings.HasPrefix(entries[0].Name(), "shard-") {
					t.Fatalf("[%s] %s holds %q, want the group's shard-<N> directory", proto.name, root, entries[0].Name())
				}

				if out, err := runQRDB(t, node.cli, conn, "database", "drop", name); err != nil {
					t.Fatalf("[%s] database drop: %v\n%s", proto.name, err, out)
				}
				// The placement delete hands the group to the shard
				// reconciler, which stops it and purges its directory.
				shardDir := filepath.Join(root, entries[0].Name())
				if !pollUntil(30*time.Second, 50*time.Millisecond, func() bool {
					_, err := os.Stat(shardDir)
					return os.IsNotExist(err)
				}) {
					t.Fatalf("[%s] the dropped group's directory %s still exists", proto.name, shardDir)
				}

				if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "raft"); err != nil {
					t.Fatalf("[%s] database recreate: %v\n%s", proto.name, err, out)
				}
				if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", "k1"); err == nil && strings.Contains(out, "v1") {
					t.Fatalf("[%s] the recreated database serves the dropped key: %q", proto.name, out)
				}
				if again := info(); again.Binding.Generation <= first.Binding.Generation {
					t.Fatalf("[%s] recreate generation %d, want above %d", proto.name, again.Binding.Generation, first.Binding.Generation)
				}
			})
		}
	}
}

// TestDatabaseBinding_CLI_RecreateGivesNewGeneration_PerProtocol drops a
// database and creates it again under the same name over the CLI on every
// transport, in a prefix tenant, and asserts the drop removes the store
// directory, the new database does not serve the dropped one's key, and the
// binding block reports a higher generation for the new database than for the
// one it replaced, in JSON and table output.
func TestDatabaseBinding_CLI_RecreateGivesNewGeneration_PerProtocol(t *testing.T) {
	node := startStandaloneModeNodeWithConfig(t, "pebble", "")

	const tenantID = "regen-acme"
	createTenant(t, node, multiProtocols[0], tenantID, "Regen Acme", "prefix", false)
	perms := append(append([]string{}, dataPlanePermissions...), "databases:delete")
	cert, key := provisionTenantIdentity(t, node, tenantID, "db-admin", perms)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, cert, key)
			name := "regen-" + proto.name
			kvKey := "regen-key-" + proto.name
			value := "regen-value-" + proto.name
			root := filepath.Join(node.dataDir, "tenants", tenantID, "databases", name)

			generation := func(t *testing.T, step string) uint64 {
				t.Helper()
				out, err := runQRDB(t, node.cli, conn, "--output", "json", "database", "info", name)
				if err != nil {
					t.Fatalf("[%s] %s: database info %s: %v\n%s", proto.name, step, name, err, out)
				}
				var info transport.DatabaseInfo
				if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
					t.Fatalf("[%s] %s: decode database info JSON: %v\n%s", proto.name, step, jerr, out)
				}
				if info.Binding == nil {
					t.Fatalf("[%s] %s: database info carries no binding block:\n%s", proto.name, step, out)
				}
				if info.Binding.Generation == 0 {
					t.Fatalf("[%s] %s: binding generation is 0:\n%s", proto.name, step, out)
				}
				return info.Binding.Generation
			}

			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "file"); err != nil {
				t.Fatalf("[%s] database create %s: %v\n%s", proto.name, name, err, out)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", kvKey, value); err != nil {
				t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
			}
			first := generation(t, "first create")
			if _, err := os.Stat(root); err != nil {
				t.Fatalf("[%s] binding root %s: %v", proto.name, root, err)
			}

			if out, err := runQRDB(t, node.cli, conn, "database", "drop", name); err != nil {
				t.Fatalf("[%s] database drop %s: %v\n%s", proto.name, name, err, out)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("[%s] binding root %s survives DROP (stat err %v)", proto.name, root, err)
			}

			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "file"); err != nil {
				t.Fatalf("[%s] database recreate %s: %v\n%s", proto.name, name, err, out)
			}
			second := generation(t, "recreate")
			if second <= first {
				t.Fatalf("[%s] recreated %s has generation %d, want more than %d", proto.name, name, second, first)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", kvKey); err == nil && strings.Contains(out, value) {
				t.Fatalf("[%s] the recreated database serves the dropped database's key: %q", proto.name, out)
			}

			out, err := runQRDB(t, node.cli, conn, "database", "info", name)
			if err != nil {
				t.Fatalf("[%s] database info (table) %s: %v\n%s", proto.name, name, err, out)
			}
			if want := fmt.Sprintf("Generation: %d", second); !strings.Contains(out, want) {
				t.Fatalf("[%s] table output lacks %q:\n%s", proto.name, want, out)
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
	// The four backends provision under a barrier that encrypts nothing; an
	// objstore database under an encrypting barrier is
	// TestDatabaseBinding_CLI_ObjstoreUnderEncryptingBarrier_PerProtocol.
	node := startStandaloneModeNodeUnencrypted(t, "objstore", extra)

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

// TestDatabaseBinding_CLI_ObjstoreJournalRefusedAsDataDir_PerProtocol proves,
// on every transport, that `qrdb database create --data-dir` refuses the
// node's local object store and its commit-journal sibling as a database data
// directory, and that the store tree survives the refusals and keeps serving.
func TestDatabaseBinding_CLI_ObjstoreJournalRefusedAsDataDir_PerProtocol(t *testing.T) {
	// Absolute: the daemon's working directory is the source tree.
	store := filepath.Join(t.TempDir(), "objects")
	journal := store + ".journal"
	extra := "storage:\n  objstore:\n    provider: local\n    settings:\n      path: " + strconv.Quote(store) + "\n" +
		"databases:\n  - name: \"nd-objstore\"\n    backend: objstore\n"
	node := startStandaloneModeNodeUnencrypted(t, "objstore", extra)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			for i, dataDir := range []string{journal, store} {
				name := "claim-" + proto.name + "-" + strconv.Itoa(i)
				out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "file", "--data-dir", dataDir)
				if err == nil || !strings.Contains(out, "reserved path") {
					t.Fatalf("[%s] database create --data-dir %s = %q, %v; want a reserved path refusal", proto.name, dataDir, out, err)
				}
			}
		})
	}

	// The node store is the object-storage-lsm engine (#924): it opens the
	// bucket alone and never creates the journal sibling, which stays
	// reserved because objstore.LocalRoots names it for the journaled engine.
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("node object store tree %s after the refused creates: %v", store, err)
	}
	for _, proto := range multiProtocols {
		conn := node.cliArgsFor(proto)
		key, value := "journal-key-"+proto.name, "journal-value-"+proto.name
		if out, err := runQRDB(t, node.cli, conn, "--database", "nd-objstore", "kv", "put", key, value); err != nil {
			t.Fatalf("[%s] kv put nd-objstore: %v\n%s", proto.name, err, out)
		}
		out, err := runQRDB(t, node.cli, conn, "--database", "nd-objstore", "kv", "get", key)
		if err != nil || !strings.Contains(out, value) {
			t.Fatalf("[%s] kv get nd-objstore = %q, %v; want %q", proto.name, out, err, value)
		}
	}
}

// TestDatabaseBinding_CLI_ObjstoreUnderEncryptingBarrier_PerProtocol: under
// an encrypting system barrier, an objstore database is created on every
// protocol, its store under its binding root written through the barrier,
// and it serves its own writes.
func TestDatabaseBinding_CLI_ObjstoreUnderEncryptingBarrier_PerProtocol(t *testing.T) {
	shared := t.TempDir()
	extra := "storage:\n  objstore:\n    provider: local\n    settings:\n      path: " + strconv.Quote(shared) + "\n"
	node := startStandaloneModeNodeWithConfig(t, "pebble", extra)
	databases := filepath.Join(node.dataDir, "tenants", "system", "databases")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := node.cliArgsFor(proto)
			name := "enc-obj-" + proto.name
			value := "enc-obj-value-" + proto.name
			if out, err := runQRDB(t, node.cli, conn, "database", "create", name, "--backend", "objstore"); err != nil {
				t.Fatalf("[%s] database create %s --backend objstore under an encrypting barrier: %v\n%s", proto.name, name, err, out)
			}
			if _, err := os.Stat(filepath.Join(databases, name, "objstore")); err != nil {
				t.Fatalf("[%s] the objstore database has no store under its binding root (%v)", proto.name, err)
			}
			if out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "put", "k1", value); err != nil {
				t.Fatalf("[%s] kv put: %v\n%s", proto.name, err, out)
			}
			out, err := runQRDB(t, node.cli, conn, "--database", name, "kv", "get", "k1")
			if err != nil || !strings.Contains(out, value) {
				t.Fatalf("[%s] kv get = %q, %v; want %q", proto.name, out, err, value)
			}
		})
	}
}
