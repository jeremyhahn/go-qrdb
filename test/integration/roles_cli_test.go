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

// roles_cli_test.go drives the `qrdb roles` COMMAND TREE (create, list, get,
// get-by-name, update, delete, assign, unassign, permissions,
// resolve-permissions) through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) sharing the
// multi_protocol_helpers_test.go harness. This closes the CLI-coverage-audit
// finding that every one of these subcommands had ZERO integration coverage
// of any kind. The RBAC role store (pkg/role, exposed via
// sdk/go/transport.RoleClient) is qrdb-native across all five transports, so
// every case in this file is a real positive-path assertion, not a
// documented-NotImplemented contract.
package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// roleCLIJSON mirrors transport.Role, decoded from the unwrapped JSON
// `roles create|get|get-by-name|update` print (cmd/qrdb/roles.go's
// printRole marshals the bare Role, not an enclosing envelope).
type roleCLIJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// roleListCLIJSON mirrors transport.ListRolesResponse, the (wrapped) shape
// `roles list --output json` actually prints.
type roleListCLIJSON struct {
	Roles   []roleCLIJSON `json:"roles"`
	Total   int           `json:"total"`
	HasMore bool          `json:"has_more"`
}

// permissionsCLIJSON mirrors transport.ListPermissionsResponse.
type permissionsCLIJSON struct {
	Categories []struct {
		Name        string   `json:"name"`
		Group       string   `json:"group"`
		Permissions []string `json:"permissions"`
	} `json:"categories"`
}

// resolvePermissionsCLIJSON mirrors transport.ResolvePermissionsResponse.
type resolvePermissionsCLIJSON struct {
	Permissions []string `json:"permissions"`
}

// TestRoles_CLI_Lifecycle_PerProtocol drives the full `qrdb roles` lifecycle
// -- create, get, get-by-name, list, update, assign, unassign,
// resolve-permissions, delete -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestRoles_CLI_Lifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRolesLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testRolesLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	roleName := fmt.Sprintf("rlc-%s-%d", proto.name, time.Now().UnixNano())

	// --- create ------------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", roleName,
		"--permissions", "users:read,users:list",
	)
	if err != nil {
		t.Fatalf("[%s] roles create: %v\noutput: %s", proto.name, err, out)
	}
	var created roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil {
		t.Fatalf("[%s] decode roles create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if created.ID == "" || created.Name != roleName ||
		!stringSliceEqualUnordered(created.Permissions, []string{"users:read", "users:list"}) {
		t.Fatalf("[%s] roles create result = %+v, want name=%q permissions=[users:read users:list]", proto.name, created, roleName)
	}
	roleID := created.ID

	// --- get -----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "get", roleID)
	if err != nil {
		t.Fatalf("[%s] roles get: %v\noutput: %s", proto.name, err, out)
	}
	var got roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("[%s] decode roles get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if got.ID != roleID || got.Name != roleName {
		t.Fatalf("[%s] roles get = %+v, want id=%s name=%q", proto.name, got, roleID, roleName)
	}

	// --- get-by-name -----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "get-by-name", roleName)
	if err != nil {
		t.Fatalf("[%s] roles get-by-name: %v\noutput: %s", proto.name, err, out)
	}
	var gotByName roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &gotByName); jerr != nil {
		t.Fatalf("[%s] decode roles get-by-name JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if gotByName.ID != roleID {
		t.Fatalf("[%s] roles get-by-name = %+v, want id=%s", proto.name, gotByName, roleID)
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "list")
	if err != nil {
		t.Fatalf("[%s] roles list: %v\noutput: %s", proto.name, err, out)
	}
	var listed roleListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode roles list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !roleListContainsID(listed.Roles, roleID) {
		t.Fatalf("[%s] roles list does not contain %s: %+v", proto.name, roleID, listed.Roles)
	}

	// --- update ------------------------------------------------------------
	updatedName := roleName + "-renamed"
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "update", roleID,
		"--name", updatedName,
		"--permissions", "users:read,users:list,audit:query",
	)
	if err != nil {
		t.Fatalf("[%s] roles update: %v\noutput: %s", proto.name, err, out)
	}
	var updated roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &updated); jerr != nil {
		t.Fatalf("[%s] decode roles update JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if updated.Name != updatedName ||
		!stringSliceEqualUnordered(updated.Permissions, []string{"users:read", "users:list", "audit:query"}) {
		t.Fatalf("[%s] roles update result = %+v, want name=%q permissions=[users:read users:list audit:query]", proto.name, updated, updatedName)
	}

	// --- permissions (registry listing) -----------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "permissions")
	if err != nil {
		t.Fatalf("[%s] roles permissions: %v\noutput: %s", proto.name, err, out)
	}
	var perms permissionsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &perms); jerr != nil {
		t.Fatalf("[%s] decode roles permissions JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(perms.Categories) == 0 {
		t.Fatalf("[%s] roles permissions returned no categories", proto.name)
	}
	if !permissionsCategoriesContain(perms, "audit:query") {
		t.Fatalf("[%s] roles permissions registry does not contain audit:query: %+v", proto.name, perms)
	}

	// --- resolve-permissions -----------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "roles", "resolve-permissions", roleID)
	if err != nil {
		t.Fatalf("[%s] roles resolve-permissions: %v\noutput: %s", proto.name, err, out)
	}
	var resolved resolvePermissionsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &resolved); jerr != nil {
		t.Fatalf("[%s] decode roles resolve-permissions JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !stringSliceEqualUnordered(resolved.Permissions, []string{"users:read", "users:list", "audit:query"}) {
		t.Fatalf("[%s] roles resolve-permissions = %+v, want [users:read users:list audit:query]", proto.name, resolved.Permissions)
	}

	// --- assign / unassign (exercise via a fresh user) ---------------------
	username := fmt.Sprintf("rlc-user-%s-%d", proto.name, time.Now().UnixNano())
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create", "--without-email-login-key", "--username", username)
	if err != nil {
		t.Fatalf("[%s] users create (for role assign): %v\noutput: %s", proto.name, err, out)
	}
	var user userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &user); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userIDStr := fmt.Sprintf("%d", user.ID)

	out, err = runCLIProto(t, node, proto, "roles", "assign", "--user", userIDStr, "--role", roleID)
	if err != nil {
		t.Fatalf("[%s] roles assign: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] roles assign: expected OK, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "get", userIDStr)
	if err != nil {
		t.Fatalf("[%s] users get (post-assign): %v\noutput: %s", proto.name, err, out)
	}
	var userAfterAssign userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &userAfterAssign); jerr != nil {
		t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !stringSliceContains(userAfterAssign.RoleIDs, roleID) {
		t.Fatalf("[%s] user %s role_ids = %v, want to contain %s after assign", proto.name, userIDStr, userAfterAssign.RoleIDs, roleID)
	}

	out, err = runCLIProto(t, node, proto, "roles", "unassign", "--user", userIDStr, "--role", roleID)
	if err != nil {
		t.Fatalf("[%s] roles unassign: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] roles unassign: expected OK, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "get", userIDStr)
	if err != nil {
		t.Fatalf("[%s] users get (post-unassign): %v\noutput: %s", proto.name, err, out)
	}
	var userAfterUnassign userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &userAfterUnassign); jerr != nil {
		t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if stringSliceContains(userAfterUnassign.RoleIDs, roleID) {
		t.Fatalf("[%s] user %s role_ids = %v, want NOT to contain %s after unassign", proto.name, userIDStr, userAfterUnassign.RoleIDs, roleID)
	}

	// --- delete ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "roles", "delete", roleID)
	if err != nil {
		t.Fatalf("[%s] roles delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] roles delete: expected OK, got: %s", proto.name, out)
	}

	// --- get-after-delete: the role must be genuinely gone ------------------
	out, err = runCLIProto(t, node, proto, "roles", "get", roleID)
	if err == nil {
		t.Fatalf("[%s] roles get after delete should fail, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Fatalf("[%s] roles get after delete: expected an error mentioning 'not found', got: %s", proto.name, out)
	}
}

// TestRoles_CLI_DeleteStripsHolders_PerProtocol drives `roles delete` through
// the real CLI on every transport and asserts, through `users get`, that no
// account still names the deleted role afterwards while every other role it
// held survives.
func TestRoles_CLI_DeleteStripsHolders_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRolesDeleteStripsHoldersOverProtocol(t, node, proto)
		})
	}
}

func testRolesDeleteStripsHoldersOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	stamp := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())

	createRole := func(name string) string {
		out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
			"--name", name, "--permissions", "users:read")
		if err != nil {
			t.Fatalf("[%s] roles create %s: %v\noutput: %s", proto.name, name, err, out)
		}
		var r roleCLIJSON
		if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || r.ID == "" {
			t.Fatalf("[%s] decode roles create JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		return r.ID
	}
	doomed := createRole("rds-doomed-" + stamp)
	kept := createRole("rds-kept-" + stamp)

	userIDs := make([]string, 0, 3)
	for i := range 3 {
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "create",
			"--without-email-login-key", "--username", fmt.Sprintf("rds-user%d-%s", i, stamp))
		if err != nil {
			t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
		}
		var u userCLIJSON
		if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
			t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		id := fmt.Sprintf("%d", u.ID)
		for _, roleID := range []string{doomed, kept} {
			if out, err := runCLIProto(t, node, proto, "roles", "assign", "--user", id, "--role", roleID); err != nil || !strings.Contains(out, "OK") {
				t.Fatalf("[%s] roles assign %s to %s: %v\noutput: %s", proto.name, roleID, id, err, out)
			}
		}
		userIDs = append(userIDs, id)
	}

	out, err := runCLIProto(t, node, proto, "roles", "delete", doomed)
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("[%s] roles delete: %v\noutput: %s", proto.name, err, out)
	}

	for _, id := range userIDs {
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "get", id)
		if err != nil {
			t.Fatalf("[%s] users get %s: %v\noutput: %s", proto.name, id, err, out)
		}
		var u userCLIJSON
		if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
			t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if stringSliceContains(u.RoleIDs, doomed) {
			t.Fatalf("[%s] user %s role_ids = %v, still holds deleted role %s", proto.name, id, u.RoleIDs, doomed)
		}
		if !stringSliceContains(u.RoleIDs, kept) {
			t.Fatalf("[%s] user %s role_ids = %v, lost role %s that was not deleted", proto.name, id, u.RoleIDs, kept)
		}
	}

	out, err = runCLIProto(t, node, proto, "roles", "get", doomed)
	if err == nil || !strings.Contains(strings.ToLower(out), "not found") {
		t.Fatalf("[%s] roles get after delete: err=%v output: %s", proto.name, err, out)
	}
}

// roleListContainsID reports whether roles contains an entry with the given ID.
func roleListContainsID(roles []roleCLIJSON, id string) bool {
	for _, r := range roles {
		if r.ID == id {
			return true
		}
	}
	return false
}

// permissionsCategoriesContain reports whether any category in resp
// contains the given permission string.
func permissionsCategoriesContain(resp permissionsCLIJSON, perm string) bool {
	for _, cat := range resp.Categories {
		for _, p := range cat.Permissions {
			if p == perm {
				return true
			}
		}
	}
	return false
}

// stringSliceContains reports whether s contains needle.
func stringSliceContains(s []string, needle string) bool {
	for _, v := range s {
		if v == needle {
			return true
		}
	}
	return false
}

// rolesReconcileCLIJSON mirrors transport.ReconcileRolesResponse, decoded
// from qrdb roles reconcile --output json.
type rolesReconcileCLIJSON struct {
	TenantID string        `json:"tenant_id"`
	Roles    []interface{} `json:"roles,omitempty"`
}

// TestRoles_CLI_Reconcile_PerProtocol drives qrdb roles reconcile through
// the CLI against every transport: with no --tenant it targets the caller
// own tenant (system, for the harness system-admin identity), already fully
// converged by firstboot; a freshly created prefix tenant has NO built-in
// roles until the first reconcile seeds them (prefix tenants are not
// auto-seeded at tenant create, unlike dedicated-isolation tenants); and a
// second, immediate reconcile of that same tenant reports NO further
// changes, proving convergence -- pkg/role/reconcile.go own contract
// ("a second call therefore performs zero writes and returns an empty
// report"), also pinned directly against the store in
// pkg/role/reconcile_test.go TestReconcileBuiltInRoles_SecondCallWritesNothing.
//
// A decode-into-a-shared-variable bug in an earlier version of this test
// (json.Unmarshal leaves a destination field untouched when the new JSON
// omits the key, so a stale non-empty Roles slice from the first call
// decode survived into the second call supposedly-empty one) made it look
// like reconcile never converged. It does; the test now decodes each
// response into its own fresh variable.
func TestRoles_CLI_Reconcile_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRolesReconcileOverProtocol(t, node, proto)
		})
	}
}

func testRolesReconcileOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "reconcile")
	if err != nil {
		t.Fatalf("[%s] roles reconcile: %v\noutput: %s", proto.name, err, out)
	}
	var systemResp rolesReconcileCLIJSON
	if jerr := json.Unmarshal([]byte(out), &systemResp); jerr != nil {
		t.Fatalf("[%s] decode roles reconcile JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if systemResp.TenantID != "system" {
		t.Fatalf("[%s] roles reconcile with no --tenant = tenant_id %q, want the caller's own tenant 'system'", proto.name, systemResp.TenantID)
	}
	if len(systemResp.Roles) != 0 {
		t.Fatalf("[%s] roles reconcile on a freshly booted node's system tenant reported changes, want none (built-ins already current): %+v", proto.name, systemResp.Roles)
	}

	// A freshly created PREFIX tenant must already have its built-in roles --
	// CreateTenant seeds them in the SAME operation (pkg/tenant/service.go's
	// RoleSeeder) precisely so the tenant admin can act immediately, per
	// docs/identity-rbac/architecture.md ("each tenant seeds its own built-in
	// roles"). Asserted two ways: `roles list` shows them directly, and the
	// FIRST `roles reconcile` call -- which used to be the only thing that
	// created them -- is now already a no-op.
	tenantID := fmt.Sprintf("rr-%s-%d", proto.name, time.Now().UnixNano())
	createTenant(t, node, proto, tenantID, "Reconcile Tenant", "prefix", false)

	// The system tenant's admin may not act on another tenant's roles, so
	// the new tenant's own administrator drives every call that names it.
	cred, ok := lookupTenantAdminCred(tenantID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, tenantID)
	}
	tenantConn := tenantCLIArgsForProto(node, proto, cred.certPath, cred.keyPath)
	// Reconciling a tenant's built-in roles needs roles:create, which the
	// tenant's built-in Admin holds in its own tenant: no other role is
	// assigned. The wait only outlasts the admin account's completion.
	awaitTenantAdminID(t, node.restPort, tenantID,
		mtlsHTTPClientFromFiles(t, node.ca, cred.certPath, cred.keyPath, 15*time.Second))

	out, err = runQRDB(t, node.cli, tenantConn, "--output", "json", "roles", "list", "--tenant-id", tenantID)
	if err != nil {
		t.Fatalf("[%s] roles list --tenant %s immediately after create: %v\noutput: %s", proto.name, tenantID, err, out)
	}
	var listed roleListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode roles list --tenant JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(listed.Roles) == 0 {
		t.Fatalf("[%s] roles list --tenant %s immediately after create returned none, want the built-ins seeded at creation", proto.name, tenantID)
	}
	for _, r := range listed.Roles {
		if r.TenantID != tenantID {
			t.Fatalf("[%s] roles list --tenant %s returned a role scoped to tenant %q: %+v", proto.name, tenantID, r.TenantID, r)
		}
		if r.Name == "" {
			t.Fatalf("[%s] roles list --tenant %s returned a nameless role: %+v", proto.name, tenantID, r)
		}
	}

	out, err = runQRDB(t, node.cli, tenantConn, "--output", "json", "roles", "reconcile", "--tenant", tenantID)
	if err != nil {
		t.Fatalf("[%s] roles reconcile --tenant %s immediately after create: %v\noutput: %s", proto.name, tenantID, err, out)
	}
	var first rolesReconcileCLIJSON
	if jerr := json.Unmarshal([]byte(out), &first); jerr != nil {
		t.Fatalf("[%s] decode roles reconcile --tenant JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if first.TenantID != tenantID {
		t.Fatalf("[%s] roles reconcile --tenant %s = tenant_id %q, want %s", proto.name, tenantID, first.TenantID, tenantID)
	}
	if len(first.Roles) != 0 {
		t.Fatalf("[%s] roles reconcile on a freshly created prefix tenant reported changes, want none -- CreateTenant must have already seeded the built-ins: %+v", proto.name, first.Roles)
	}

	// A second, immediate call must converge to the SAME empty report too:
	// idempotent either way.
	out, err = runQRDB(t, node.cli, tenantConn, "--output", "json", "roles", "reconcile", "--tenant", tenantID)
	if err != nil {
		t.Fatalf("[%s] second roles reconcile --tenant %s: %v\noutput: %s", proto.name, tenantID, err, out)
	}

	var second rolesReconcileCLIJSON
	if jerr := json.Unmarshal([]byte(out), &second); jerr != nil {
		t.Fatalf("[%s] decode second roles reconcile --tenant JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if second.TenantID != tenantID {
		t.Fatalf("[%s] second roles reconcile --tenant %s = tenant_id %q, want %s", proto.name, tenantID, second.TenantID, tenantID)
	}
	if len(second.Roles) != 0 {
		t.Fatalf("[%s] second, immediate roles reconcile reported changes, want none: %+v", proto.name, second.Roles)
	}
}

// TestRolesTenantAdmin_CLI_PerProtocol drives `roles create` and `roles
// update` as a tenant's own built-in Admin holding no other role. The Admin
// creates and updates custom roles in its own tenant; it is refused in
// another tenant and in the system tenant, and the built-in roles stay out
// of its hands.
func TestRolesTenantAdmin_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRolesTenantAdminOverProtocol(t, node, proto)
		})
	}
}

// tenantAdminConn creates a prefix tenant and returns the CLI connection
// arguments of its own built-in Admin, once that account is accepted.
func tenantAdminConn(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) []string {
	t.Helper()
	createTenant(t, node, proto, tenantID, "Role Admin "+tenantID, "prefix", false)
	cred, ok := lookupTenantAdminCred(tenantID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for tenant %s", proto.name, tenantID)
	}
	awaitTenantAdminID(t, node.restPort, tenantID,
		mtlsHTTPClientFromFiles(t, node.ca, cred.certPath, cred.keyPath, 15*time.Second))
	return tenantCLIArgsForProto(node, proto, cred.certPath, cred.keyPath)
}

// runRoleCLI runs a roles subcommand with JSON output and decodes the role it
// prints.
func runRoleCLI(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) roleCLIJSON {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, append([]string{"--output", "json", "roles"}, args...)...)
	if err != nil {
		t.Fatalf("[%s] roles %v: %v\noutput: %s", proto.name, args, err, out)
	}
	var r roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || r.ID == "" {
		t.Fatalf("[%s] decode roles %v JSON: %v\noutput: %s", proto.name, args, jerr, out)
	}
	return r
}

// roleNamesIn lists the role names a connection sees in tenantID.
func roleNamesIn(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, tenantID string) []string {
	t.Helper()
	args := []string{"--output", "json", "roles", "list"}
	if tenantID != "" {
		args = append(args, "--tenant-id", tenantID)
	}
	out, err := runQRDB(t, node.cli, conn, args...)
	if err != nil {
		t.Fatalf("[%s] roles list --tenant-id %q: %v\noutput: %s", proto.name, tenantID, err, out)
	}
	var listed roleListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode roles list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	names := make([]string, len(listed.Roles))
	for i, r := range listed.Roles {
		names[i] = r.Name
	}
	return names
}

func testRolesTenantAdminOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA, tenantB := "tra-"+suffix, "trb-"+suffix
	connA := tenantAdminConn(t, node, proto, tenantA)
	connB := tenantAdminConn(t, node, proto, tenantB)

	// Own tenant: create, with no --tenant-id and naming the tenant, then
	// update the name and the grant.
	created := runRoleCLI(t, node, proto, connA, "create",
		"--name", "app-reader-"+suffix, "--permissions", "kv:get")
	if created.TenantID != tenantA || created.System {
		t.Fatalf("[%s] created role = tenant %q system %v, want tenant %s, not system", proto.name, created.TenantID, created.System, tenantA)
	}
	named := runRoleCLI(t, node, proto, connA, "create",
		"--name", "app-named-"+suffix, "--permissions", "kv:get", "--tenant-id", tenantA)
	if named.TenantID != tenantA {
		t.Fatalf("[%s] role created naming tenant %s landed in %q", proto.name, tenantA, named.TenantID)
	}
	updated := runRoleCLI(t, node, proto, connA, "update", created.ID,
		"--name", "app-writer-"+suffix, "--permissions", "kv:get,kv:put")
	if updated.ID != created.ID || updated.Name != "app-writer-"+suffix ||
		!stringSliceEqualUnordered(updated.Permissions, []string{"kv:get", "kv:put"}) {
		t.Fatalf("[%s] updated role = %+v, want %s renamed app-writer-%s with [kv:get kv:put]", proto.name, updated, created.ID, suffix)
	}
	if names := roleNamesIn(t, node, proto, connA, tenantA); !slices.Contains(names, "app-writer-"+suffix) {
		t.Fatalf("[%s] tenant %s roles lack the updated role: %v", proto.name, tenantA, names)
	}

	// Another tenant: tenant B's own Admin creates a role there; tenant A's
	// Admin can neither create in B nor update B's role.
	victim := runRoleCLI(t, node, proto, connB, "create",
		"--name", "b-reader-"+suffix, "--permissions", "kv:get")
	intruder := "intruder-" + suffix
	assertCrossTenantIAMForbidden(t, node, proto, connA, victim.Name,
		"roles", "create", "--name", intruder, "--permissions", "kv:get", "--tenant-id", tenantB)
	if out, err := runQRDB(t, node.cli, connA, "roles", "update", victim.ID,
		"--name", intruder, "--permissions", "kv:get,kv:put"); err == nil {
		t.Fatalf("[%s] tenant A admin updated tenant B's role %s: %s", proto.name, victim.ID, out)
	} else if strings.Contains(out, victim.Name) {
		t.Fatalf("[%s] refused update leaked tenant B's role name %q: %s", proto.name, victim.Name, out)
	}
	unchanged := runRoleCLI(t, node, proto, connB, "get", victim.ID)
	if unchanged.Name != victim.Name || !stringSliceEqualUnordered(unchanged.Permissions, []string{"kv:get"}) {
		t.Fatalf("[%s] tenant B's role after refused writes = %+v, want %s with [kv:get]", proto.name, unchanged, victim.Name)
	}
	if names := roleNamesIn(t, node, proto, connB, tenantB); slices.Contains(names, intruder) {
		t.Fatalf("[%s] tenant B holds %q after a refused create: %v", proto.name, intruder, names)
	}

	// The system tenant: refused the same way, and nothing lands there.
	assertCrossTenantIAMForbidden(t, node, proto, connA, "",
		"roles", "create", "--name", intruder, "--permissions", "kv:get", "--tenant-id", "system")
	if out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "list"); err != nil {
		t.Fatalf("[%s] system roles list: %v\noutput: %s", proto.name, err, out)
	} else if strings.Contains(out, intruder) {
		t.Fatalf("[%s] the system tenant holds %q after a refused create: %s", proto.name, intruder, out)
	}

	// The built-in roles: the name is reserved, and the record is immutable.
	if out, err := runQRDB(t, node.cli, connA, "roles", "create",
		"--name", role.RoleNameAdmin, "--permissions", "kv:get"); err == nil {
		t.Fatalf("[%s] tenant admin created a role named %q: %s", proto.name, role.RoleNameAdmin, out)
	}
	adminID := role.SystemRoleID(tenantA, role.RoleNameAdmin)
	if out, err := runQRDB(t, node.cli, connA, "roles", "update", adminID,
		"--permissions", "kv:get"); err == nil {
		t.Fatalf("[%s] tenant admin re-permissioned the built-in admin role: %s", proto.name, out)
	}
	builtIn := runRoleCLI(t, node, proto, connA, "get", adminID)
	if builtIn.Name != role.RoleNameAdmin || !builtIn.System ||
		!slices.Contains(builtIn.Permissions, "roles:create") {
		t.Fatalf("[%s] built-in admin role after refused update = %+v", proto.name, builtIn)
	}
}
