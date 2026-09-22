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

// users_cli_test.go drives the `qrdb users` COMMAND TREE (create, list, get,
// update, delete, enable, disable) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) sharing
// the multi_protocol_helpers_test.go harness. This closes the
// CLI-coverage-audit finding that every one of these subcommands had ZERO
// integration coverage of any kind. The passwordless user store (pkg/user,
// exposed via sdk/go/transport.UserClient) is qrdb-native across all five
// transports, so every case in this file is a real positive-path assertion.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// userCLIJSON mirrors transport.User, decoded from the unwrapped JSON
// `users create|get|update` print (cmd/qrdb/users.go's printUser marshals
// the bare User, not an enclosing envelope). ID crosses the wire as a JSON
// string (proto3 convention for uint64 Snowflakes) per transport.User's
// `json:"id,string"` tag.
type userCLIJSON struct {
	ID        uint64   `json:"id,string"`
	Username  string   `json:"username"`
	TenantID  string   `json:"tenant_id"`
	RoleIDs   []string `json:"role_ids"`
	Lifecycle string   `json:"lifecycle"`
	System    bool     `json:"system"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// userListCLIJSON mirrors transport.ListUsersResponse, the (wrapped) shape
// `users list --output json` actually prints.
type userListCLIJSON struct {
	Users   []userCLIJSON `json:"users"`
	Total   int           `json:"total"`
	HasMore bool          `json:"has_more"`
}

// TestUsers_CLI_Lifecycle_PerProtocol drives the full `qrdb users` lifecycle
// -- create, get, list, update (username + roles), disable, enable, delete
// -- through the CLI binary against every transport sharing one
// multiProtoNode.
func TestUsers_CLI_Lifecycle_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testUsersLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testUsersLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("ulc-%s-%d", proto.name, time.Now().UnixNano())
	username := base

	// A role to exercise `users create --roles` / `users update --roles`.
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", base+"-role",
		"--permissions", "users:read",
	)
	if err != nil {
		t.Fatalf("[%s] roles create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var role roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &role); jerr != nil {
		t.Fatalf("[%s] decode roles create JSON (fixture): %v\noutput: %s", proto.name, jerr, out)
	}

	// --- create ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", username,
		"--roles", role.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if created.ID == 0 || created.Username != username || created.Lifecycle != "active" ||
		!stringSliceEqualUnordered(created.RoleIDs, []string{role.ID}) {
		t.Fatalf("[%s] users create result = %+v, want username=%q lifecycle=active role_ids=[%s]", proto.name, created, username, role.ID)
	}
	userID := fmt.Sprintf("%d", created.ID)

	// --- get -----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "get", userID)
	if err != nil {
		t.Fatalf("[%s] users get: %v\noutput: %s", proto.name, err, out)
	}
	var got userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("[%s] decode users get JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if got.ID != created.ID || got.Username != username {
		t.Fatalf("[%s] users get = %+v, want id=%d username=%q", proto.name, got, created.ID, username)
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "list")
	if err != nil {
		t.Fatalf("[%s] users list: %v\noutput: %s", proto.name, err, out)
	}
	var listed userListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode users list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !userListContainsID(listed.Users, created.ID) {
		t.Fatalf("[%s] users list does not contain %d: %+v", proto.name, created.ID, listed.Users)
	}

	// --- update (username only; roles held at current value) ---------------
	renamed := username + "-renamed"
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "update", userID,
		"--username", renamed,
		"--roles", role.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users update: %v\noutput: %s", proto.name, err, out)
	}
	var updated userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &updated); jerr != nil {
		t.Fatalf("[%s] decode users update JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if updated.Username != renamed {
		t.Fatalf("[%s] users update result = %+v, want username=%q", proto.name, updated, renamed)
	}

	// --- disable: toggles auth (lifecycle moves to disabled) --------------
	out, err = runCLIProto(t, node, proto, "users", "disable", userID)
	if err != nil {
		t.Fatalf("[%s] users disable: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] users disable: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "get", userID)
	if err != nil {
		t.Fatalf("[%s] users get (post-disable): %v\noutput: %s", proto.name, err, out)
	}
	var afterDisable userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &afterDisable); jerr != nil {
		t.Fatalf("[%s] decode users get JSON (post-disable): %v\noutput: %s", proto.name, jerr, out)
	}
	if afterDisable.Lifecycle != "disabled" {
		t.Fatalf("[%s] user %s lifecycle = %q after disable, want disabled", proto.name, userID, afterDisable.Lifecycle)
	}

	// --- enable: toggles auth back (lifecycle moves to active) -------------
	out, err = runCLIProto(t, node, proto, "users", "enable", userID)
	if err != nil {
		t.Fatalf("[%s] users enable: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] users enable: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "get", userID)
	if err != nil {
		t.Fatalf("[%s] users get (post-enable): %v\noutput: %s", proto.name, err, out)
	}
	var afterEnable userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &afterEnable); jerr != nil {
		t.Fatalf("[%s] decode users get JSON (post-enable): %v\noutput: %s", proto.name, jerr, out)
	}
	if afterEnable.Lifecycle != "active" {
		t.Fatalf("[%s] user %s lifecycle = %q after enable, want active", proto.name, userID, afterEnable.Lifecycle)
	}

	// --- delete ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "users", "delete", userID)
	if err != nil {
		t.Fatalf("[%s] users delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] users delete: expected OK, got: %s", proto.name, out)
	}

	// --- get-after-delete: the user must be genuinely gone ------------------
	out, err = runCLIProto(t, node, proto, "users", "get", userID)
	if err == nil {
		t.Fatalf("[%s] users get after delete should fail, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Fatalf("[%s] users get after delete: expected an error mentioning 'not found', got: %s", proto.name, out)
	}

	// Clean up the fixture role.
	if out, err := runCLIProto(t, node, proto, "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}

// userListContainsID reports whether users contains an entry with the given ID.
func userListContainsID(users []userCLIJSON, id uint64) bool {
	for _, u := range users {
		if u.ID == id {
			return true
		}
	}
	return false
}
