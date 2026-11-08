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

// database_catalog_cli_test.go proves, through the real client CLI on every
// transport, that the replicated database catalog is the record of databases
// across a node restart: what create records comes back, what drop removes
// stays gone, a directory under the data directory that the catalog does not
// record is not a database, and an isolated tenant's database is reopened
// from the record in the tenant's home store.
package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// catalogRestartCase is one transport's databases, created before the
// restart and checked after it.
type catalogRestartCase struct {
	kept     string // a system-tenant database that must survive the restart
	dropped  string // a system-tenant database dropped before the restart
	stray    string // a directory with a database's on-disk shape and no record
	tenantDB string // a database of a raft-isolated tenant
	tenant   []string
}

const (
	catalogKey   = "greeting"
	catalogValue = "hello-catalog"
)

// TestDatabaseCatalog_CLI_SurvivesRestart_PerProtocol creates, writes and
// drops databases on every transport, restarts the node once, and reads
// back what the catalog recorded.
func TestDatabaseCatalog_CLI_SurvivesRestart_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	cases := make(map[string]catalogRestartCase, len(multiProtocols))
	for _, proto := range multiProtocols {
		cases[proto.name] = setupCatalogRestartCase(t, node, proto)
	}

	node.restart(t, "[catalog-restart] ")

	for _, proto := range multiProtocols {
		c := cases[proto.name]
		t.Run(proto.name, func(t *testing.T) {
			assertCatalogAfterRestart(t, node, proto, c)
		})
	}
}

// setupCatalogRestartCase creates, lists, opens and writes the databases of
// one transport's case, drops one of them, and plants the stray directory.
func setupCatalogRestartCase(t *testing.T, node *multiProtoNode, proto multiProtocol) catalogRestartCase {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	c := catalogRestartCase{
		kept:     "cat-kept-" + suffix,
		dropped:  "cat-dropped-" + suffix,
		stray:    "cat-stray-" + suffix,
		tenantDB: "cat-tenant-" + suffix,
	}

	for _, name := range []string{c.kept, c.dropped} {
		if out, err := runCLIProto(t, node, proto, "database", "create", name, "--backend", "pebble"); err != nil {
			t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, name, err, out)
		}
	}
	assertDatabaseListed(t, proto, c.kept, true, func() (string, error) {
		return runCLIProto(t, node, proto, "database", "list")
	})
	if out, err := runCLIProto(t, node, proto, "--database", c.kept, "kv", "put", catalogKey, catalogValue); err != nil {
		t.Fatalf("[%s] kv put into %s: %v\noutput: %s", proto.name, c.kept, err, out)
	}
	assertKVValue(t, proto, c.kept, func() (string, error) {
		return runCLIProto(t, node, proto, "--database", c.kept, "kv", "get", catalogKey)
	})

	if out, err := runCLIProto(t, node, proto, "database", "drop", c.dropped); err != nil {
		t.Fatalf("[%s] database drop %s: %v\noutput: %s", proto.name, c.dropped, err, out)
	}
	assertDatabaseListed(t, proto, c.dropped, false, func() (string, error) {
		return runCLIProto(t, node, proto, "database", "list")
	})

	// A system-tenant database's directory with a data directory in it.
	// Only a catalog record makes a database; a node that scans directories
	// would open this one. (A pre-catalog meta.json in it would refuse the
	// start instead: database.UnmigratedDatabasesError.)
	strayDir := filepath.Join(node.dataDir, "databases", c.stray, "data")
	if err := os.MkdirAll(strayDir, 0o700); err != nil {
		t.Fatalf("[%s] stray database directory: %v", proto.name, err)
	}

	// A raft-isolated tenant keeps its catalog records in its own home
	// store, which the node must reopen before it can open the database.
	tenantID := "cat-iso-" + suffix
	createTenant(t, node, proto, tenantID, "Catalog Restart Tenant", "raft", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "catalog-user", dataPlanePermissions)
	c.tenant = tenantCLIArgsForProto(node, proto, certPath, keyPath)
	if out, err := runQRDB(t, node.cli, c.tenant, "database", "create", c.tenantDB, "--backend", "raft"); err != nil {
		t.Fatalf("[%s] tenant database create %s: %v\noutput: %s", proto.name, c.tenantDB, err, out)
	}
	// database create returns before the new group has elected a leader, so
	// the first write can meet the documented retryable 503.
	if out, err := runQRDBRetryingUnavailable(t, node.cli, c.tenant, "--database", c.tenantDB, "kv", "put", catalogKey, catalogValue); err != nil {
		t.Fatalf("[%s] kv put into tenant database %s: %v\noutput: %s", proto.name, c.tenantDB, err, out)
	}
	return c
}

// assertCatalogAfterRestart checks one transport's case against the
// restarted node.
func assertCatalogAfterRestart(t *testing.T, node *multiProtoNode, proto multiProtocol, c catalogRestartCase) {
	t.Helper()
	systemList := func() (string, error) { return runCLIProto(t, node, proto, "database", "list") }

	// The record create wrote brings the database back, with its settings
	// and its data.
	assertDatabaseListed(t, proto, c.kept, true, systemList)
	if info := cliDatabaseInfo(t, node, proto, c.kept); info.Backend != "pebble" {
		t.Fatalf("[%s] database info %s after the restart: Backend = %q, want pebble", proto.name, c.kept, info.Backend)
	}
	assertKVValue(t, proto, c.kept, func() (string, error) {
		return runCLIProto(t, node, proto, "--database", c.kept, "kv", "get", catalogKey)
	})

	// Drop removed the record, so the restart does not bring it back.
	assertDatabaseListed(t, proto, c.dropped, false, systemList)
	assertDatabaseNotFound(t, node, proto, c.dropped)

	// A directory is not a record.
	assertDatabaseListed(t, proto, c.stray, false, systemList)
	assertDatabaseNotFound(t, node, proto, c.stray)

	// The tenant's database is reopened from the record in its home store.
	tenantList := func() (string, error) {
		return runQRDBRetryingUnavailable(t, node.cli, c.tenant, "database", "list")
	}
	assertDatabaseListed(t, proto, c.tenantDB, true, tenantList)
	assertKVValue(t, proto, c.tenantDB, func() (string, error) {
		return runQRDBRetryingUnavailable(t, node.cli, c.tenant, "--database", c.tenantDB, "kv", "get", catalogKey)
	})
}

// assertDatabaseListed runs list and requires name to be in its output, or,
// when want is false, not in it.
func assertDatabaseListed(t *testing.T, proto multiProtocol, name string, want bool, list func() (string, error)) {
	t.Helper()
	out, err := list()
	if err != nil {
		t.Fatalf("[%s] database list: %v\noutput: %s", proto.name, err, out)
	}
	if got := strings.Contains(out, name); got != want {
		t.Fatalf("[%s] database list names %s = %v, want %v\noutput: %s", proto.name, name, got, want, out)
	}
}

// assertDatabaseNotFound requires `database info` to refuse name as not
// found.
func assertDatabaseNotFound(t *testing.T, node *multiProtoNode, proto multiProtocol, name string) {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "database", "info", name)
	if err == nil {
		t.Fatalf("[%s] database info %s succeeded, want not found\noutput: %s", proto.name, name, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "not found") {
		t.Fatalf("[%s] database info %s: want a not-found refusal, got: %s / %v", proto.name, name, out, err)
	}
}

// assertKVValue runs get and requires it to return catalogValue.
func assertKVValue(t *testing.T, proto multiProtocol, db string, get func() (string, error)) {
	t.Helper()
	out, err := get()
	if err != nil || strings.TrimSpace(out) != catalogValue {
		t.Fatalf("[%s] kv get %s from %s = %q (err=%v), want %q", proto.name, catalogKey, db, out, err, catalogValue)
	}
}
