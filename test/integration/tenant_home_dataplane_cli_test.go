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
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTenantHome_NotSelectableOnTheDataPlane_PerProtocol proves, over every
// CLI transport, that a raft-isolated tenant's home group -- the Raft group
// holding its identity records -- cannot be named as a database. The group
// has a placement like any dedicated-raft database, so a resolver that read
// placements alone would hand a caller with nothing but KV permissions the
// tenant's user and role records, past the user and role services. The
// caller must get exactly the answer a database that does not exist gets, so
// the name is no oracle, while its own databases keep working.
func TestTenantHome_NotSelectableOnTheDataPlane_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testTenantHomeRefusedOverProtocol(t, node, proto)
		})
	}
}

func testTenantHomeRefusedOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "home-dp-" + suffix
	createTenant(t, node, proto, tenantID, "Home Data Plane", "raft", true)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "kv-user", dataPlanePermissions)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

	// The control: the same identity's own dedicated-raft database resolves.
	db := "orders-" + suffix
	if out, err := runQRDB(t, node.cli, conn, "database", "create", db, "--backend", "raft"); err != nil {
		t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, db, err, out)
	}
	const key, value = "greeting", "hello"
	// database create returns before the new group has elected a leader, so
	// the first write can meet the documented retryable 503.
	if out, err := runQRDBRetryingUnavailable(t, node.cli, conn, "--database", db, "kv", "put", key, value); err != nil {
		t.Fatalf("[%s] kv put into %s: %v\noutput: %s", proto.name, db, err, out)
	}

	// refusal runs op against database name, requires it to fail, and returns
	// its output with the name abstracted, so two refusals compare equal only
	// when nothing but the name tells them apart.
	refusal := func(name string, op []string) string {
		t.Helper()
		out, err := runQRDB(t, node.cli, conn, append([]string{"--database", name}, op...)...)
		if err == nil {
			t.Fatalf("[%s] %v against database %q succeeded; the home group must not be selectable\noutput: %s", proto.name, op, name, out)
		}
		return strings.ReplaceAll(out, name, "<database>")
	}
	absent := "absent-" + suffix
	for _, op := range [][]string{
		{"kv", "get", key},
		{"kv", "put", key, "hostile-overwrite"},
	} {
		want := refusal(absent, op)
		if !strings.Contains(strings.ToLower(want), "not found") {
			t.Fatalf("[%s] %v against an unknown database: want a not-found refusal, got: %s", proto.name, op, want)
		}
		for _, home := range []string{"__home", "__HOME"} {
			if got := refusal(home, op); got != want {
				t.Fatalf("[%s] %v against %q answered differently from an unknown database:\n got: %s\nwant: %s", proto.name, op, home, got, want)
			}
		}
	}

	out, err := runQRDB(t, node.cli, conn, "database", "list")
	if err != nil {
		t.Fatalf("[%s] database list: %v\noutput: %s", proto.name, err, out)
	}
	if strings.Contains(strings.ToLower(out), "__home") {
		t.Fatalf("[%s] database list names the home group: %s", proto.name, out)
	}

	// The identity records the refused writes aimed at are intact: the same
	// identity still authenticates and reads its own database, and the
	// tenant's admin still reads its users through the user service.
	out, err = runQRDB(t, node.cli, conn, "--database", db, "kv", "get", key)
	if err != nil || strings.TrimSpace(out) != value {
		t.Fatalf("[%s] kv get %s from %s = %q (err=%v), want %q", proto.name, key, db, out, err, value)
	}
	cred, ok := lookupTenantAdminCred(tenantID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, tenantID)
	}
	adminConn := tenantCLIArgsForProto(node, proto, cred.certPath, cred.keyPath)
	if out, err := runQRDB(t, node.cli, adminConn, "users", "list", "--tenant-id", tenantID); err != nil {
		t.Fatalf("[%s] tenant admin users list: %v\noutput: %s", proto.name, err, out)
	}
}
