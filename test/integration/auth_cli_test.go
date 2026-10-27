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

// auth_cli_test.go drives the `qrdb auth` COMMAND TREE (login, factor
// list/add/remove/delete/rotate/relabel) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) sharing
// the multi_protocol_helpers_test.go harness.
//
// Unlike the hardware factor DRIVERS (FIDO2 / PIV / PKCS#11 / TPM2), which
// per CLAUDE.md's Layer Ownership section live exclusively in go-xkms, the
// user-side N-factor MODEL itself -- the User -> []AuthFactor relationship,
// credential lookup-key resolution, and login RPC -- is qrdb-native (M14,
// service.NewUserAuthFactorService, exposed via
// sdk/go/transport.UserAuthFactorClient on every network transport: see
// cmd/qrdb/auth.go's doc comment and docs/auth-factors/architecture.md). qrdb performs no hardware verification itself (that's xkms's job
// before it calls this RPC), but the mTLS factor type needs no hardware step
// at all: the lookup key is simply SHA-256 of the certificate DER, which
// this test drives end-to-end exactly as a real mTLS-only caller would.
//
// LOGIN-ENRICHMENT (fixed): `auth login`'s response is now enriched with the
// resolved user's Username, TenantID, and Roles on every transport. Each
// server-side Login handler (pkg/api/{rest,grpc,quic,mcp,unix}) resolves the
// factor's user ID through a user servicer (added to the handler struct +
// constructor + server.go call site on grpc/quic/mcp/unix; REST's dead
// `_ = factor.UserID` stub was replaced with a real GetUser call) and fills
// Username/TenantID/Roles from the user record. The assertion below verifies
// the full enriched contract round-trips on all five protocols.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// authFactorRecordCLIJSON mirrors transport.AuthFactorRecord, decoded from
// the unwrapped JSON `auth factor add|relabel` print (cmd/qrdb/auth.go's
// printFactorRecord marshals the bare record).
type authFactorRecordCLIJSON struct {
	ID        string            `json:"id"`
	UserID    uint64            `json:"user_id"`
	Type      string            `json:"type"`
	Label     string            `json:"label,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	RevokedAt *string           `json:"revoked_at,omitempty"`
}

// authFactorListCLIJSON mirrors transport.AuthFactorListResponse.
type authFactorListCLIJSON struct {
	Factors []authFactorRecordCLIJSON `json:"factors"`
	Total   int                       `json:"total"`
}

// authFactorRotateCLIJSON mirrors transport.AuthFactorRotateResponse.
type authFactorRotateCLIJSON struct {
	NewFactor authFactorRecordCLIJSON `json:"new_factor"`
}

// authLoginCLIJSON mirrors transport.AuthFactorLoginResponse.
type authLoginCLIJSON struct {
	UserID   uint64                  `json:"user_id"`
	Username string                  `json:"username"`
	TenantID string                  `json:"tenant_id,omitempty"`
	Roles    []string                `json:"roles,omitempty"`
	Active   bool                    `json:"active"`
	Factor   authFactorRecordCLIJSON `json:"factor"`
}

// TestAuth_CLI_Login_And_Factor_Lifecycle_PerProtocol drives the full `qrdb
// auth` surface -- factor add, factor list, login, factor relabel, factor
// rotate, factor remove -- through the CLI binary against every transport
// sharing one multiProtoNode, using generated mTLS certificates as the
// factor credential (no hardware dependency, exercising the qrdb-native
// lookup-key resolution path end-to-end).
func TestAuth_CLI_Login_And_Factor_Lifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAuthLoginAndFactorLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testAuthLoginAndFactorLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	username := fmt.Sprintf("auc-%s-%d", proto.name, time.Now().UnixNano())

	// --- fixture: a user to enroll factors against. It is created WITH a role
	// so the login-enrichment assertion below can verify the resolved role set
	// round-trips (not just the username). `--roles` takes role IDS, and a
	// built-in role's id is a derived UUID, not its name: the name "auditor"
	// was stored unchecked until role references were validated, after which
	// it was (correctly) refused as a reference to no role. Look the id up the
	// way an operator does.
	fixtureRole := builtInRoleID(t, node, proto, "auditor")
	out, err := runCLIProto(t, node, proto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", username, "--roles", fixtureRole)
	if err != nil {
		t.Fatalf("[%s] users create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var user userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &user); jerr != nil {
		t.Fatalf("[%s] decode users create JSON (fixture): %v\noutput: %s", proto.name, jerr, out)
	}
	userID := fmt.Sprintf("%d", user.ID)

	// Two independently-generated mTLS certificates (distinct keys/serials
	// under the harness CA) so credential lookup keys never collide across
	// add/rotate.
	certA, _ := node.ca.generateAdminClientCert(t, t.TempDir())
	certB, _ := node.ca.generateAdminClientCert(t, t.TempDir())

	// --- factor add (mtls) -------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "add",
		"--user", userID,
		"--type", "mtls",
		"--label", "primary",
		"--cert", certA,
	)
	if err != nil {
		t.Fatalf("[%s] auth factor add: %v\noutput: %s", proto.name, err, out)
	}
	var added authFactorRecordCLIJSON
	if jerr := json.Unmarshal([]byte(out), &added); jerr != nil {
		t.Fatalf("[%s] decode auth factor add JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if added.ID == "" || added.UserID != user.ID || added.Type != "mtls" || added.Label != "primary" {
		t.Fatalf("[%s] auth factor add result = %+v, want user_id=%d type=mtls label=primary", proto.name, added, user.ID)
	}
	factorID := added.ID

	// --- factor list ---------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "list", "--user", userID)
	if err != nil {
		t.Fatalf("[%s] auth factor list: %v\noutput: %s", proto.name, err, out)
	}
	var listed authFactorListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode auth factor list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !authFactorListContainsID(listed.Factors, factorID) {
		t.Fatalf("[%s] auth factor list does not contain %s: %+v", proto.name, factorID, listed.Factors)
	}

	// --- login (resolves certA's SHA-256 back to this user) -----------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "login", "--method", "mtls", "--cert", certA)
	if err != nil {
		t.Fatalf("[%s] auth login: %v\noutput: %s", proto.name, err, out)
	}
	var login authLoginCLIJSON
	if jerr := json.Unmarshal([]byte(out), &login); jerr != nil {
		t.Fatalf("[%s] decode auth login JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	// The login response is now ENRICHED (bug fixed): every server-side Login
	// handler (pkg/api/{rest,grpc,quic,mcp,unix}) resolves the factor's user ID
	// through the user servicer and populates Username, TenantID, and Roles
	// from the user record. Assert the full enriched contract round-trips on
	// every transport.
	if login.UserID != user.ID || !login.Active || login.Factor.ID != factorID {
		t.Fatalf("[%s] auth login result = %+v, want user_id=%d active=true factor.id=%s", proto.name, login, user.ID, factorID)
	}
	if login.Username != username {
		t.Fatalf("[%s] auth login username = %q, want %q (enriched)", proto.name, login.Username, username)
	}
	if login.TenantID == "" || login.TenantID != user.TenantID {
		t.Fatalf("[%s] auth login tenant_id = %q, want %q (enriched)", proto.name, login.TenantID, user.TenantID)
	}
	if !stringSliceContains(login.Roles, fixtureRole) {
		t.Fatalf("[%s] auth login roles = %v, want to contain %q (enriched)", proto.name, login.Roles, fixtureRole)
	}

	// --- factor relabel ----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "relabel", factorID, "--label", "renamed")
	if err != nil {
		t.Fatalf("[%s] auth factor relabel: %v\noutput: %s", proto.name, err, out)
	}
	var relabeled authFactorRecordCLIJSON
	if jerr := json.Unmarshal([]byte(out), &relabeled); jerr != nil {
		t.Fatalf("[%s] decode auth factor relabel JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if relabeled.ID != factorID || relabeled.Label != "renamed" {
		t.Fatalf("[%s] auth factor relabel result = %+v, want id=%s label=renamed", proto.name, relabeled, factorID)
	}

	// --- factor rotate (atomically replaces certA's factor with certB's) ---
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "rotate", factorID,
		"--type", "mtls",
		"--label", "rotated",
		"--cert", certB,
	)
	if err != nil {
		t.Fatalf("[%s] auth factor rotate: %v\noutput: %s", proto.name, err, out)
	}
	var rotated authFactorRotateCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rotated); jerr != nil {
		t.Fatalf("[%s] decode auth factor rotate JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if rotated.NewFactor.ID == "" || rotated.NewFactor.ID == factorID || rotated.NewFactor.Label != "rotated" {
		t.Fatalf("[%s] auth factor rotate result = %+v, want a fresh non-empty id and label=rotated", proto.name, rotated)
	}
	newFactorID := rotated.NewFactor.ID

	// The old factor (certA) must no longer authenticate: rotate revokes it.
	out, err = runCLIProto(t, node, proto, "auth", "login", "--method", "mtls", "--cert", certA)
	if err == nil {
		t.Fatalf("[%s] auth login with the rotated-out certificate should fail, got: %s", proto.name, out)
	}

	// The new factor (certB) authenticates as the same user.
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "login", "--method", "mtls", "--cert", certB)
	if err != nil {
		t.Fatalf("[%s] auth login (post-rotate, new cert): %v\noutput: %s", proto.name, err, out)
	}
	var loginAfterRotate authLoginCLIJSON
	if jerr := json.Unmarshal([]byte(out), &loginAfterRotate); jerr != nil {
		t.Fatalf("[%s] decode auth login JSON (post-rotate): %v\noutput: %s", proto.name, jerr, out)
	}
	if loginAfterRotate.UserID != user.ID || loginAfterRotate.Factor.ID != newFactorID {
		t.Fatalf("[%s] auth login (post-rotate) result = %+v, want user_id=%d factor.id=%s", proto.name, loginAfterRotate, user.ID, newFactorID)
	}

	// --- factor remove (force: this is the user's only active factor) ------
	out, err = runCLIProto(t, node, proto, "auth", "factor", "remove", newFactorID, "--force")
	if err != nil {
		t.Fatalf("[%s] auth factor remove: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] auth factor remove: expected OK, got: %s", proto.name, out)
	}

	// Login with the now-removed factor's certificate must fail.
	out, err = runCLIProto(t, node, proto, "auth", "login", "--method", "mtls", "--cert", certB)
	if err == nil {
		t.Fatalf("[%s] auth login after factor remove should fail, got: %s", proto.name, out)
	}
}

// TestAuthFactorDelete_CLI_PerProtocol drives `qrdb auth factor delete`
// against every transport sharing one multiProtoNode: a factor added and
// then deleted leaves no credential claim behind, so the same certificate
// binds to the same user again, while a revoked factor refuses deletion and
// keeps its certificate claimed.
func TestAuthFactorDelete_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAuthFactorDeleteOverProtocol(t, node, proto)
		})
	}
}

func testAuthFactorDeleteOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	username := fmt.Sprintf("afd-%s-%d", proto.name, time.Now().UnixNano())
	out, err := runCLIProto(t, node, proto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", username)
	if err != nil {
		t.Fatalf("[%s] users create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var user userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &user); jerr != nil {
		t.Fatalf("[%s] decode users create JSON (fixture): %v\noutput: %s", proto.name, jerr, out)
	}
	userID := fmt.Sprintf("%d", user.ID)
	cert, _ := node.ca.generateAdminClientCert(t, t.TempDir())

	add := func() (string, error) {
		return runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "add",
			"--user", userID, "--type", "mtls", "--label", "rollback", "--cert", cert)
	}
	addOK := func(step string) string {
		t.Helper()
		out, err := add()
		if err != nil {
			t.Fatalf("[%s] auth factor add (%s): %v\noutput: %s", proto.name, step, err, out)
		}
		var rec authFactorRecordCLIJSON
		if jerr := json.Unmarshal([]byte(out), &rec); jerr != nil {
			t.Fatalf("[%s] decode auth factor add JSON (%s): %v\noutput: %s", proto.name, step, jerr, out)
		}
		if rec.ID == "" || rec.UserID != user.ID {
			t.Fatalf("[%s] auth factor add (%s) = %+v, want a factor for user %d", proto.name, step, rec, user.ID)
		}
		return rec.ID
	}

	first := addOK("first")
	if out, err := add(); err == nil {
		t.Fatalf("[%s] binding a claimed certificate should fail, got: %s", proto.name, out)
	}

	// --- factor delete: the factor and every index it claimed are gone -----
	out, err = runCLIProto(t, node, proto, "auth", "factor", "delete", first, "--force")
	if err != nil {
		t.Fatalf("[%s] auth factor delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] auth factor delete: expected OK, got: %s", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "--output", "json", "auth", "factor", "list", "--user", userID)
	if err != nil {
		t.Fatalf("[%s] auth factor list: %v\noutput: %s", proto.name, err, out)
	}
	var listed authFactorListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode auth factor list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if authFactorListContainsID(listed.Factors, first) {
		t.Fatalf("[%s] auth factor list still contains deleted %s: %+v", proto.name, first, listed.Factors)
	}

	// The same certificate binds to the same user again, as a new factor.
	second := addOK("rebind after delete")
	if second == first {
		t.Fatalf("[%s] rebind reused the deleted factor id %s", proto.name, first)
	}

	// A deleted factor is not there to delete twice.
	if out, err := runCLIProto(t, node, proto, "auth", "factor", "delete", first, "--force"); err == nil {
		t.Fatalf("[%s] second delete of %s should fail, got: %s", proto.name, first, out)
	}

	// --- revoked: delete refuses, so the revocation cannot be undone -------
	out, err = runCLIProto(t, node, proto, "auth", "factor", "remove", second, "--force")
	if err != nil {
		t.Fatalf("[%s] auth factor remove: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "auth", "factor", "delete", second, "--force"); err == nil {
		t.Fatalf("[%s] delete of revoked %s should fail, got: %s", proto.name, second, out)
	} else if !strings.Contains(strings.ToLower(out+err.Error()), "revoked") {
		t.Fatalf("[%s] delete of revoked %s: want a revoked error, got: %v\noutput: %s", proto.name, second, err, out)
	}
	if out, err := add(); err == nil {
		t.Fatalf("[%s] binding a revoked factor's certificate should fail, got: %s", proto.name, out)
	}
}

// authFactorListContainsID reports whether factors contains an entry with
// the given ID.
func authFactorListContainsID(factors []authFactorRecordCLIJSON, id string) bool {
	for _, f := range factors {
		if f.ID == id {
			return true
		}
	}
	return false
}

// builtInRoleID returns the id of the caller tenant's built-in role named
// name, read through `roles list` on proto.
func builtInRoleID(t *testing.T, node *multiProtoNode, proto multiProtocol, name string) string {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "list")
	if err != nil {
		t.Fatalf("[%s] roles list: %v\noutput: %s", proto.name, err, out)
	}
	var listed roleListCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode roles list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	for _, r := range listed.Roles {
		if r.Name == name && r.System {
			return r.ID
		}
	}
	t.Fatalf("[%s] no built-in role named %q in %+v", proto.name, name, listed.Roles)
	return ""
}
