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
	"strings"
	"testing"
	"time"
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
