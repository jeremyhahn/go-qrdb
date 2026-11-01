// Copyright 2026 Jeremy Hahn
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

// The grant rule (go-qrdb#1278) over the qrdb CLI on every protocol: a
// grantor may grant only permissions it holds. A tenant Admin, which does
// not hold the crypto-use duty, cannot mint a role carrying keys:sign, widen
// a role to carry it, or bind to itself the built-in User role that carries
// it; it still assigns User to another account, the duty split.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

func TestRoleGrantRule_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRoleGrantRuleOverProtocol(t, node, proto)
		})
	}
}

func testRoleGrantRuleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "grant-" + suffix
	conn := tenantAdminConn(t, node, proto, tenantID)

	// Self-grant: a role carrying keys:sign, which the Admin does not hold.
	signer := "signer-" + suffix
	out, err := runQRDB(t, node.cli, conn, "roles", "create", "--name", signer, "--permissions", "users:read,keys:sign")
	assertGrantRefused(t, proto, "roles create with keys:sign", out, err)
	if names := roleNamesIn(t, node, proto, conn, tenantID); stringSliceContains(names, signer) {
		t.Fatalf("[%s] tenant %s holds %q after a refused create: %v", proto.name, tenantID, signer, names)
	}

	// The Admin holds no data-plane permission, so it mints no data role.
	out, err = runQRDB(t, node.cli, conn, "roles", "create", "--name", "reader-"+suffix, "--permissions", "kv:get")
	assertGrantRefused(t, proto, "roles create with kv:get", out, err)

	// Cross-role: widening a held role to carry keys:sign.
	ops := runRoleCLI(t, node, proto, conn, "create", "--name", "ops-"+suffix, "--permissions", "users:read")
	out, err = runQRDB(t, node.cli, conn, "roles", "update", ops.ID, "--permissions", "users:read,keys:sign")
	assertGrantRefused(t, proto, "roles update adding keys:sign", out, err)
	if got := runRoleCLI(t, node, proto, conn, "get", ops.ID); !stringSliceEqualUnordered(got.Permissions, []string{"users:read"}) {
		t.Fatalf("[%s] role %s after a refused update = %v, want [users:read]", proto.name, ops.ID, got.Permissions)
	}
	runRoleCLI(t, node, proto, conn, "update", ops.ID, "--permissions", "users:read,users:list")

	// Cross-role: binding to itself the built-in User role, which carries
	// the crypto-use duty the Admin lacks.
	userRole := role.SystemRoleID(tenantID, role.RoleNameUser)
	cred, ok := lookupTenantAdminCred(tenantID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, tenantID)
	}
	adminID := awaitTenantAdminID(t, node.restPort, tenantID,
		mtlsHTTPClientFromFiles(t, node.ca, cred.certPath, cred.keyPath, 15*time.Second))
	out, err = runQRDB(t, node.cli, conn, "roles", "assign", "--user", adminID, "--role", userRole)
	assertGrantRefused(t, proto, "roles assign of User to itself", out, err)
	if ids := userRoleIDs(t, proto, node, conn, adminID); stringSliceContains(ids, userRole) {
		t.Fatalf("[%s] the Admin holds User after a refused self-binding: %v", proto.name, ids)
	}

	// The duty split: User, and a held custom role, to another account.
	out, err = runQRDB(t, node.cli, conn, "--output", "json", "users", "create", "--without-email-login-key", "--username", "member-"+suffix)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var member userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &member); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	memberID := fmt.Sprintf("%d", member.ID)
	for _, roleID := range []string{userRole, ops.ID} {
		if out, err := runQRDB(t, node.cli, conn, "roles", "assign", "--user", memberID, "--role", roleID); err != nil || !strings.Contains(out, "OK") {
			t.Fatalf("[%s] roles assign %s to another account: %v\noutput: %s", proto.name, roleID, err, out)
		}
	}
	if ids := userRoleIDs(t, proto, node, conn, memberID); !stringSliceContains(ids, userRole) || !stringSliceContains(ids, ops.ID) {
		t.Fatalf("[%s] account %s roles = %v, want User and %s", proto.name, memberID, ids, ops.ID)
	}
}

// assertGrantRefused fails unless a CLI call was refused as a permission
// denial, the status every transport maps *role.GrantExceedsHolderError to.
func assertGrantRefused(t *testing.T, proto multiProtocol, what, out string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s] %s was granted: %s", proto.name, what, out)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "permission denied") && !strings.Contains(lower, "forbidden") &&
		!strings.Contains(lower, "not permitted") && !strings.Contains(lower, "access denied") &&
		!strings.Contains(lower, "cannot grant") {
		t.Fatalf("[%s] %s must be a permission denial, got: %s", proto.name, what, out)
	}
}

// userRoleIDs is the role_ids of account id, read through conn.
func userRoleIDs(t *testing.T, proto multiProtocol, node *multiProtoNode, conn []string, id string) []string {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, "--output", "json", "users", "get", id)
	if err != nil {
		t.Fatalf("[%s] users get %s: %v\noutput: %s", proto.name, id, err, out)
	}
	var u userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
		t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return u.RoleIDs
}
