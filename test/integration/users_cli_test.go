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
// update, delete, enable, disable, suspend, unsuspend) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) sharing
// the multi_protocol_helpers_test.go harness. This closes the
// CLI-coverage-audit finding that every one of these subcommands had ZERO
// integration coverage of any kind. The passwordless user store (pkg/user,
// exposed via sdk/go/transport.UserClient) is qrdb-native across all five
// transports, so every case in this file is a real positive-path assertion.
package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
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
	t.Parallel()
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

// TestUsers_CLI_SuspendUnsuspend_PerProtocol drives `users suspend` and
// `users unsuspend` on every transport. The suspended account's own requests
// carry a client certificate enrolled BEFORE the suspension, so the refusal
// proves the lifecycle is checked on every request, not only when a
// credential is issued.
func TestUsers_CLI_SuspendUnsuspend_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testUsersSuspendOverProtocol(t, node, proto)
		})
	}
}

func testUsersSuspendOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("usus-%s-%d", proto.name, time.Now().UnixNano())

	// The account holds users:read, so `users get` on itself is a request it
	// is authorised to make while active.
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

	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create",
		"--username", base,
		"--email", base+"@example.com",
		"--roles", role.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil || created.ID == 0 {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userID := strconv.FormatUint(created.ID, 10)

	// The account's credential: a client certificate naming it, enrolled on
	// the account as the default auth.mtls_binding_policy ("required")
	// demands, issued before any suspension.
	admin := mtlsHTTPClient(t, node.ca, 15*time.Second)
	kp := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(base, tenantid.System))
	bindUserCert(t, admin, node.restPort, created.ID, kp.CertPath)
	userConn := tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath)

	asUser := func() (string, error) {
		return runQRDB(t, node.cli, userConn, "--output", "json", "users", "get", userID)
	}
	lifecycle := func(step string) string {
		t.Helper()
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "get", userID)
		if err != nil {
			t.Fatalf("[%s] %s: users get: %v\noutput: %s", proto.name, step, err, out)
		}
		var got userCLIJSON
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("[%s] %s: decode users get JSON: %v\noutput: %s", proto.name, step, jerr, out)
		}
		return got.Lifecycle
	}
	verb := func(step, name string) {
		t.Helper()
		out, err := runCLIProto(t, node, proto, "users", name, userID)
		if err != nil {
			t.Fatalf("[%s] %s: users %s: %v\noutput: %s", proto.name, step, name, err, out)
		}
		if !strings.Contains(out, "OK") {
			t.Fatalf("[%s] %s: users %s: expected OK, got: %s", proto.name, step, name, out)
		}
	}

	if out, err := asUser(); err != nil {
		t.Fatalf("[%s] request as the active account: %v\noutput: %s", proto.name, err, out)
	}

	// --- unsuspend on an active user: succeeds and changes nothing ---------
	verb("unsuspend-active", "unsuspend")
	if got := lifecycle("unsuspend-active"); got != "active" {
		t.Fatalf("[%s] lifecycle = %q after unsuspend of an active user, want active", proto.name, got)
	}

	// --- suspend: lifecycle moves to suspended; the account is refused -----
	verb("suspend", "suspend")
	if got := lifecycle("suspend"); got != "suspended" {
		t.Fatalf("[%s] lifecycle = %q after suspend, want suspended", proto.name, got)
	}
	requireRefusedPromptly(t, proto, "request as the suspended account with a pre-suspension certificate", asUser)

	// --- suspend on a suspended user: succeeds and changes nothing ---------
	verb("suspend-suspended", "suspend")
	if got := lifecycle("suspend-suspended"); got != "suspended" {
		t.Fatalf("[%s] lifecycle = %q after suspend of a suspended user, want suspended", proto.name, got)
	}
	requireRefusedPromptly(t, proto, "request as the re-suspended account", asUser)

	// --- unsuspend: lifecycle moves back to active; requests succeed -------
	verb("unsuspend", "unsuspend")
	if got := lifecycle("unsuspend"); got != "active" {
		t.Fatalf("[%s] lifecycle = %q after unsuspend, want active", proto.name, got)
	}
	if out, err := asUser(); err != nil {
		t.Fatalf("[%s] request as the unsuspended account: %v\noutput: %s", proto.name, err, out)
	}

	// --- suspend on a disabled user: the typed lifecycle transition error --
	verb("disable", "disable")
	requireRefusedPromptly(t, proto, "request as the disabled account", asUser)
	out, err = runCLIProto(t, node, proto, "users", "suspend", userID)
	if err == nil {
		t.Fatalf("[%s] users suspend of a disabled user should fail, got: %s", proto.name, out)
	}
	if !strings.Contains(out, "cannot transition user "+userID+" from disabled to suspended") {
		t.Fatalf("[%s] users suspend of a disabled user: expected the lifecycle transition error, got: %s", proto.name, out)
	}
	if got := lifecycle("suspend-disabled"); got != "disabled" {
		t.Fatalf("[%s] lifecycle = %q after a refused suspend of a disabled user, want disabled", proto.name, got)
	}

	if out, err := runCLIProto(t, node, proto, "users", "delete", userID); err != nil {
		t.Fatalf("[%s] users delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}

// TestUsers_CLI_CertBinding_PerProtocol drives `qrdb users cert-binding
// add|list|remove` over every CLI transport: under the default
// auth.mtls_binding_policy ("required") a certificate authenticates as the
// account once the verb enrols it, and stops once the verb withdraws it.
func TestUsers_CLI_CertBinding_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testUsersCertBindingOverProtocol(t, node, proto)
		})
	}
}

func testUsersCertBindingOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("ucb-%s-%d", proto.name, time.Now().UnixNano())

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

	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create",
		"--username", base,
		"--email", base+"@example.com",
		"--roles", role.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil || created.ID == 0 {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userID := strconv.FormatUint(created.ID, 10)

	first := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(base, tenantid.System))
	second := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(base, tenantid.System))
	asUser := func(kp *qrdbdharness.KeyPair) (string, error) {
		conn := tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath)
		return runQRDB(t, node.cli, conn, "--output", "json", "users", "get", userID)
	}
	bindings := func(step string, args ...string) []transport.CertBinding {
		t.Helper()
		out, err := runCLIProto(t, node, proto, append([]string{"--output", "json", "users", "cert-binding"}, args...)...)
		if err != nil {
			t.Fatalf("[%s] %s: users cert-binding %v: %v\noutput: %s", proto.name, step, args, err, out)
		}
		var got []transport.CertBinding
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("[%s] %s: decode bindings JSON: %v\noutput: %s", proto.name, step, jerr, out)
		}
		return got
	}
	holds := func(got []transport.CertBinding, thumbprint string) bool {
		for _, b := range got {
			if b.Thumbprint == thumbprint {
				return true
			}
		}
		return false
	}

	// --- before enrolment the required policy refuses the certificate -----
	if out, err := asUser(first); err == nil {
		t.Fatalf("[%s] request with an unenrolled certificate succeeded: %s", proto.name, out)
	}
	if got := bindings("list-empty", "list", userID); len(got) != 0 {
		t.Fatalf("[%s] bindings before enrolment = %+v, want none", proto.name, got)
	}

	// --- add --cert: thumbprint and expiry come from the certificate ------
	thumbprintOf := func(kp *qrdbdharness.KeyPair) string {
		t.Helper()
		tp, terr := user.ComputeSPKIThumbprintFromCertPEM(kp.CertPEM)
		if terr != nil {
			t.Fatalf("[%s] thumbprint of %s: %v", proto.name, kp.CertPath, terr)
		}
		return tp
	}
	firstTP := thumbprintOf(first)
	got := bindings("add-cert", "add", userID, "--cert", first.CertPath)
	if !holds(got, firstTP) {
		t.Fatalf("[%s] add --cert: bindings %+v lack %s", proto.name, got, firstTP)
	}
	if out, err := asUser(first); err != nil {
		t.Fatalf("[%s] request with the enrolled certificate: %v\noutput: %s", proto.name, err, out)
	}

	// --- add by thumbprint, then list shows both ---------------------------
	secondTP := thumbprintOf(second)
	// A base64url thumbprint can begin with '-' (about one in 64 do), so
	// every call that passes one positionally puts it after '--'.
	bindings("add-thumbprint", "add", userID,
		"--not-after", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "--", secondTP)
	got = bindings("list", "list", userID)
	if !holds(got, firstTP) || !holds(got, secondTP) {
		t.Fatalf("[%s] list: bindings %+v, want %s and %s", proto.name, got, firstTP, secondTP)
	}

	// --- an expiry at or before the epoch is refused, not made permanent ---
	expiryOf := func(got []transport.CertBinding, thumbprint string) string {
		for _, b := range got {
			if b.Thumbprint == thumbprint {
				return b.NotAfter
			}
		}
		return ""
	}
	secondExpiry := expiryOf(got, secondTP)
	for _, date := range []string{"1970-01-01T00:00:00Z", "1969-07-20T20:17:40Z"} {
		out, err := runCLIProto(t, node, proto, "users", "cert-binding", "add", userID, "--not-after", date, "--", secondTP)
		if err == nil || !strings.Contains(out, "after the Unix epoch") {
			t.Fatalf("[%s] add --not-after %s: want the epoch refusal, got err=%v output: %s", proto.name, date, err, out)
		}
	}
	if got := expiryOf(bindings("list-after-epoch", "list", userID), secondTP); got == "" || got != secondExpiry {
		t.Fatalf("[%s] refused epoch expiry changed the binding's expiry: %q, want %q", proto.name, got, secondExpiry)
	}

	// --- remove: the withdrawn key stops authenticating, the other keeps ---
	got = bindings("remove", "remove", userID, "--", firstTP)
	if holds(got, firstTP) || !holds(got, secondTP) {
		t.Fatalf("[%s] remove: bindings %+v, want only %s", proto.name, got, secondTP)
	}
	if out, err := asUser(first); err == nil {
		t.Fatalf("[%s] request with the withdrawn certificate succeeded: %s", proto.name, out)
	}
	if out, err := asUser(second); err != nil {
		t.Fatalf("[%s] request with the still-enrolled certificate: %v\noutput: %s", proto.name, err, out)
	}

	// --- remove of a thumbprint the user does not hold is refused ----------
	if out, err := runCLIProto(t, node, proto, "users", "cert-binding", "remove", userID, "--", firstTP); err == nil {
		t.Fatalf("[%s] remove of a withdrawn thumbprint should fail, got: %s", proto.name, out)
	}

	// --- a thumbprint beginning with '-' round-trips after '--' -----------
	const dashTP = "-mfgDIAA4OnmhPXEgQ0d46rNYBLntyNnLUbId3NJ_eg"
	if got := bindings("add-dash", "add", userID, "--", dashTP); !holds(got, dashTP) {
		t.Fatalf("[%s] add -- %s: bindings %+v lack it", proto.name, dashTP, got)
	}
	if got := bindings("list-dash", "list", userID); !holds(got, dashTP) || !holds(got, secondTP) {
		t.Fatalf("[%s] list: bindings %+v, want %s and %s", proto.name, got, dashTP, secondTP)
	}
	if got := bindings("remove-dash", "remove", userID, "--", dashTP); holds(got, dashTP) || !holds(got, secondTP) {
		t.Fatalf("[%s] remove -- %s: bindings %+v, want only %s", proto.name, dashTP, got, secondTP)
	}

	if out, err := runCLIProto(t, node, proto, "users", "delete", userID); err != nil {
		t.Fatalf("[%s] users delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}

// refusalBound is how long a refused request may take on any transport: far
// below the CLI's --timeout, so a client that waits out its bound on a
// refusal, rather than returning it, fails here.
const refusalBound = 5 * time.Second

// requireRefusedPromptly runs a request the server must refuse and asserts,
// on every transport alike, that it fails with the auth chain's typed
// refusal of the account and that it fails at once.
func requireRefusedPromptly(t *testing.T, proto multiProtocol, step string, request func() (string, error)) {
	t.Helper()
	start := time.Now()
	out, err := request()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("[%s] %s: request succeeded, want it refused: %s", proto.name, step, out)
	}
	if !strings.Contains(out, "user cannot authenticate") {
		t.Fatalf("[%s] %s: want the typed refusal of the account, got: %s", proto.name, step, out)
	}
	if elapsed > refusalBound {
		t.Fatalf("[%s] %s: refusal took %v, want it within %v", proto.name, step, elapsed, refusalBound)
	}
}

// TestUsers_CLI_SubjectIsUserID_PerProtocol proves a caller's Subject is the
// immutable user id of the account it resolves to, on every transport: the
// account logs in, is renamed, has its email changed, and every action it
// takes in between is attributed to the same user id. On the network
// transports the CLI acts as the account with its own bound client
// certificate and the audit actor of each action it takes is read back; the
// embedded transport has no CLI, so it drives the login ceremony directly and
// reads the Subject of the identity each login returns.
func TestUsers_CLI_SubjectIsUserID_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testSubjectIsUserIDOverProtocol(t, node, proto)
		})
	}
	t.Run("embedded", testSubjectIsUserIDEmbedded)
}

// testSubjectIsUserIDOverProtocol creates an account through the CLI, acts
// as it, renames it, acts again under a certificate naming the new username,
// changes its email, acts a third time, and asserts the audit trail names
// the same user id as the actor of all three actions.
func testSubjectIsUserIDOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("subj-%s-%d", proto.name, time.Now().UnixNano())

	// The account may create roles, an audited mutation whose event names
	// the acting identity, holding the one permission those roles grant.
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", base+"-role",
		"--permissions", "roles:create,users:read",
	)
	if err != nil {
		t.Fatalf("[%s] roles create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var grant roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &grant); jerr != nil {
		t.Fatalf("[%s] decode roles create JSON (fixture): %v\noutput: %s", proto.name, jerr, out)
	}

	username := base
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create",
		"--username", username,
		"--email", base+"@example.com",
		"--roles", grant.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil || created.ID == 0 {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userID := strconv.FormatUint(created.ID, 10)
	admin := mtlsHTTPClient(t, node.ca, 15*time.Second)

	// certFor issues a client certificate naming the account's current
	// username and enrols it on the account, as the default
	// auth.mtls_binding_policy ("required") demands.
	certFor := func(name string) []string {
		kp := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(name, tenantid.System))
		bindUserCert(t, admin, node.restPort, created.ID, kp.CertPath)
		return tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath)
	}

	// actAs creates a role as the account and returns the audit actor of
	// that create.
	actAs := func(step string, conn []string) string {
		t.Helper()
		out, err := runQRDB(t, node.cli, conn, "--output", "json", "roles", "create",
			"--name", base+"-"+step,
			"--permissions", "users:read",
		)
		if err != nil {
			t.Fatalf("[%s] %s: roles create as the account: %v\noutput: %s", proto.name, step, err, out)
		}
		var acted roleCLIJSON
		if jerr := json.Unmarshal([]byte(out), &acted); jerr != nil || acted.ID == "" {
			t.Fatalf("[%s] %s: decode roles create JSON: %v\noutput: %s", proto.name, step, jerr, out)
		}
		return findAuditEvent(t, node, proto, "--resource", "role:"+acted.ID).Actor
	}

	conn := certFor(username)
	if got := actAs("login", conn); got != userID {
		t.Fatalf("[%s] audit actor after login = %q, want the user id %q", proto.name, got, userID)
	}

	// --- rename: the certificate names the new username; the actor does not
	// move.
	renamed := username + "-renamed"
	if out, err := runCLIProto(t, node, proto, "--output", "json", "users", "update", userID,
		"--username", renamed,
		"--roles", grant.ID,
	); err != nil {
		t.Fatalf("[%s] users update --username: %v\noutput: %s", proto.name, err, out)
	}
	conn = certFor(renamed)
	if got := actAs("renamed", conn); got != userID {
		t.Fatalf("[%s] audit actor after rename = %q, want the user id %q", proto.name, got, userID)
	}

	// --- email change: the same certificate, the same actor.
	if out, err := runCLIProto(t, node, proto, "--output", "json", "users", "update", userID,
		"--username", renamed,
		"--email", base+"-changed@example.com",
		"--roles", grant.ID,
	); err != nil {
		t.Fatalf("[%s] users update --email: %v\noutput: %s", proto.name, err, out)
	}
	if got := actAs("email-changed", conn); got != userID {
		t.Fatalf("[%s] audit actor after email change = %q, want the user id %q", proto.name, got, userID)
	}
}

// testSubjectIsUserIDEmbedded logs an account in through the embedded
// transport's login ceremony, renames it, changes its email, and asserts
// every login yields the same Subject: the account's user id.
func testSubjectIsUserIDEmbedded(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil,
		node.WithSessionIssuer(newMemorySessionIssuer()),
		node.WithLoginFactorVerifier(signedPIVVerifier{}),
	)
	if err != nil {
		t.Fatalf("[embedded] NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	svcNode := n.Services()
	adminRole, err := svcNode.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "subject-embedded-admin",
		TenantID: tenantid.System,
		Permissions: []permission.Permission{
			permission.UsersRead,
			permission.UsersUpdate,
			permission.UserAuthFactorsAdd,
		},
	})
	if err != nil {
		t.Fatalf("[embedded] CreateRole: %v", err)
	}
	adminUser, err := svcNode.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "subject-embedded-admin",
		Email:    "subject-embedded-admin@example.com",
		TenantID: tenantid.System,
		RoleIDs:  []string{adminRole.ID},
	})
	if err != nil {
		t.Fatalf("[embedded] CreateUser (admin): %v", err)
	}
	adminIdentity := &auth.Identity{TenantID: tenantid.System, Roles: []string{adminRole.ID}}
	adminIdentity.BindUser(adminUser.ID, adminUser.Username)
	adminCtx := auth.WithIdentity(ctx, adminIdentity)

	account, err := svcNode.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "subject-embedded",
		Email:    "subject-embedded@example.com",
		TenantID: tenantid.System,
	})
	if err != nil {
		t.Fatalf("[embedded] CreateUser (account): %v", err)
	}
	client := n.Client()
	credential := []byte("subject-embedded-piv")
	if err := client.Factors().Enroll(adminCtx, account.ID, transport.Factor{
		Type:       transport.FactorTypePIV,
		Label:      "piv",
		Credential: credential,
	}); err != nil {
		t.Fatalf("[embedded] enrol factor: %v", err)
	}

	login := func(step string) string {
		t.Helper()
		begin, err := client.Login().BeginLogin(ctx, transport.BeginLoginRequest{
			Type:     transport.FactorTypePIV,
			TenantID: tenantid.System,
		})
		if err != nil {
			t.Fatalf("[embedded] %s: BeginLogin: %v", step, err)
		}
		fin, err := client.Login().FinishLogin(ctx, transport.FinishLoginRequest{
			SessionID: begin.SessionID,
			Type:      transport.FactorTypePIV,
			LookupKey: credential,
			Signature: []byte("proof"),
		})
		if err != nil {
			t.Fatalf("[embedded] %s: FinishLogin: %v", step, err)
		}
		resolved, err := client.Login().ResolveIdentity(ctx, fin.Bearer)
		if err != nil {
			t.Fatalf("[embedded] %s: ResolveIdentity: %v", step, err)
		}
		if resolved.Subject != fin.Identity.Subject {
			t.Fatalf("[embedded] %s: bearer resolves to Subject %q, login returned %q", step, resolved.Subject, fin.Identity.Subject)
		}
		return fin.Identity.Subject
	}

	want := strconv.FormatUint(account.ID, 10)
	if got := login("login"); got != want {
		t.Fatalf("[embedded] Subject after login = %q, want the user id %q", got, want)
	}
	if _, err := client.Users().UpdateUser(adminCtx, &transport.UpdateUserRequest{
		ID:       account.ID,
		Username: "subject-embedded-renamed",
	}); err != nil {
		t.Fatalf("[embedded] rename: %v", err)
	}
	if got := login("renamed"); got != want {
		t.Fatalf("[embedded] Subject after rename = %q, want the user id %q", got, want)
	}
	if _, err := client.Users().UpdateUser(adminCtx, &transport.UpdateUserRequest{
		ID:    account.ID,
		Email: "subject-embedded-changed@example.com",
	}); err != nil {
		t.Fatalf("[embedded] email change: %v", err)
	}
	if got := login("email-changed"); got != want {
		t.Fatalf("[embedded] Subject after email change = %q, want the user id %q", got, want)
	}
}

// memorySessionIssuer is the session store a composer injects through
// node.WithSessionIssuer (qrdb owns none): it mints a random bearer per
// login and resolves it to the identity the ceremony produced.
type memorySessionIssuer struct {
	mu       sync.Mutex
	sessions map[string]*auth.Identity
}

var _ embedded.SessionIssuer = (*memorySessionIssuer)(nil)

func newMemorySessionIssuer() *memorySessionIssuer {
	return &memorySessionIssuer{sessions: make(map[string]*auth.Identity)}
}

func (s *memorySessionIssuer) Issue(_ context.Context, identity *auth.Identity) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	bearer := hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[bearer] = identity
	return bearer, time.Now().Add(time.Hour), nil
}

func (s *memorySessionIssuer) Resolve(_ context.Context, bearer string) (*auth.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.sessions[bearer]
	if !ok {
		return nil, &transport.InvalidBearerError{Reason: "unknown bearer"}
	}
	return identity, nil
}

func (s *memorySessionIssuer) Revoke(_ context.Context, bearer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, bearer)
	return nil
}

// signedPIVVerifier stands in for the hardware PIV verifier go-xkms injects
// through node.WithLoginFactorVerifier: it refuses a revoked factor and an
// empty signature, and accepts any other proof, which is all the Subject
// assertions above depend on.
type signedPIVVerifier struct{}

var _ embedded.FactorVerifier = signedPIVVerifier{}

func (signedPIVVerifier) Type() transport.FactorType { return transport.FactorTypePIV }

func (signedPIVVerifier) Verify(_ context.Context, factor *auth.AuthFactor, ch transport.VerifyChallenge) error {
	if factor == nil || factor.RevokedAt != nil {
		return &auth.InvalidFactorError{Reason: "revoked or nil factor"}
	}
	if len(ch.Signature) == 0 {
		return &auth.InvalidFactorError{Reason: "empty signature"}
	}
	return nil
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
