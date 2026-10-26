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

// tenant_so_cli_test.go drives `qrdb tenant create --so-username --so-csr
// --so-pin` through the real client CLI on every transport (go-qrdb#1042): the
// tenant's security officer is enrolled with the so role at create, and it --
// not the tenant's Admin -- grants that role.
package integration_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestTenantSO_CLI_CreateWithSO_PerProtocol creates a tenant with an SO over
// each transport, then proves the SO grants the so role while the Admin is
// refused the same grant (the control).
func TestTenantSO_CLI_CreateWithSO_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startStrictSeparationNode(t)
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantSOCreateOverProtocol(t, node, proto)
		})
	}
}

// tenantSOCSR writes a CSR and its key for username, answering the directory.
func tenantSOCSR(t *testing.T, node *multiProtoNode, proto multiProtocol, username string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := runCLIProto(t, node, proto, "ca", "csr", "--username", username, "--out", dir); err != nil {
		t.Fatalf("[%s] ca csr for %s: %v\noutput: %s", proto.name, username, err, out)
	}
	return dir
}

func testTenantSOCreateOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	tenantID := fmt.Sprintf("tsoc-%s-%d", proto.name, time.Now().UnixNano())
	adminCSR := tenantSOCSR(t, node, proto, tenantID+"-admin")
	soCSR := tenantSOCSR(t, node, proto, tenantID+"-so")
	outDir := t.TempDir()
	// Under strict separation of duties the admin may not be the SO.
	if out, err := runCLIProto(t, node, proto, "tenant", "create",
		"--id", tenantID+"-same", "--name", tenantID+"-same", "--isolation-type", "prefix",
		"--admin-username", tenantID+"-admin", "--admin-csr", filepath.Join(adminCSR, "csr.pem"),
		"--so-username", tenantID+"-admin", "--so-csr", filepath.Join(adminCSR, "csr.pem"), "--so-pin", "so-pin-1234",
		"--out", t.TempDir(),
	); err == nil {
		t.Fatalf("[%s] strict separation: tenant create accepted the admin as its SO: %s", proto.name, out)
	}
	if out, err := runCLIProto(t, node, proto, "tenant", "create",
		"--id", tenantID, "--name", tenantID, "--isolation-type", "prefix",
		"--admin-username", tenantID+"-admin", "--admin-csr", filepath.Join(adminCSR, "csr.pem"),
		"--so-username", tenantID+"-so", "--so-csr", filepath.Join(soCSR, "csr.pem"), "--so-pin", "so-pin-1234",
		"--out", outDir,
	); err != nil {
		t.Fatalf("[%s] tenant create with an SO: %v\noutput: %s", proto.name, err, out)
	}
	adminConn := tenantCLIArgsForProto(node, proto, filepath.Join(outDir, "admin-cert.pem"), filepath.Join(adminCSR, "key.pem"))
	soConn := tenantCLIArgsForProto(node, proto, filepath.Join(outDir, "so-cert.pem"), filepath.Join(soCSR, "key.pem"))

	out, err := runQRDB(t, node.cli, adminConn, "--output", "json", "users", "create", "--without-email-login-key", "--username", tenantID+"-uma")
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var uma userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &uma); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	umaID := fmt.Sprintf("%d", uma.ID)
	soID := role.SystemRoleID(tenantID, role.RoleNameSO)

	// Control: the Admin is refused the so role grant.
	if out, err := runQRDB(t, node.cli, adminConn, "roles", "assign", "--user", umaID, "--role", soID); err == nil {
		t.Fatalf("[%s] the tenant Admin granted the so role: %s", proto.name, out)
	}
	if out, err := runQRDB(t, node.cli, soConn, "roles", "assign", "--user", umaID, "--role", soID); err != nil {
		t.Fatalf("[%s] the tenant SO granting the so role: %v\noutput: %s", proto.name, err, out)
	}
	got, err := runQRDB(t, node.cli, soConn, "--output", "json", "users", "get", umaID)
	if err != nil {
		t.Fatalf("[%s] users get %s: %v\noutput: %s", proto.name, umaID, err, got)
	}
	if !strings.Contains(got, soID) {
		t.Fatalf("[%s] %s lacks the so role after the SO's grant: %s", proto.name, umaID, got)
	}
}

// TestTenantSO_CLI_StandardPostureAdminIsTheSO is the standard posture, over
// the first transport: outside strict separation of duties tenant create
// accepts the admin as the SO, and the admin grants the so role.
func TestTenantSO_CLI_StandardPostureAdminIsTheSO(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)
	proto := multiProtocols[0]
	tenantID := fmt.Sprintf("tsos-%s-%d", proto.name, time.Now().UnixNano())
	adminCSR := tenantSOCSR(t, node, proto, tenantID+"-admin")
	outDir := t.TempDir()
	if out, err := runCLIProto(t, node, proto, "tenant", "create",
		"--id", tenantID, "--name", tenantID, "--isolation-type", "prefix",
		"--admin-username", tenantID+"-admin", "--admin-csr", filepath.Join(adminCSR, "csr.pem"),
		"--so-username", tenantID+"-admin", "--so-csr", filepath.Join(adminCSR, "csr.pem"), "--so-pin", "so-pin-1234",
		"--out", outDir,
	); err != nil {
		t.Fatalf("[%s] tenant create with the admin as its SO: %v\noutput: %s", proto.name, err, out)
	}
	adminConn := tenantCLIArgsForProto(node, proto, filepath.Join(outDir, "admin-cert.pem"), filepath.Join(adminCSR, "key.pem"))

	out, err := runQRDB(t, node.cli, adminConn, "--output", "json", "users", "create", "--without-email-login-key", "--username", tenantID+"-uma")
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var uma userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &uma); jerr != nil {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	umaID := fmt.Sprintf("%d", uma.ID)
	soID := role.SystemRoleID(tenantID, role.RoleNameSO)
	if out, err := runQRDB(t, node.cli, adminConn, "roles", "assign", "--user", umaID, "--role", soID); err != nil {
		t.Fatalf("[%s] the tenant Admin granting the so role in the standard posture: %v\noutput: %s", proto.name, err, out)
	}
	got, err := runQRDB(t, node.cli, adminConn, "--output", "json", "users", "get", umaID)
	if err != nil {
		t.Fatalf("[%s] users get %s: %v\noutput: %s", proto.name, umaID, err, got)
	}
	if !strings.Contains(got, soID) {
		t.Fatalf("[%s] %s lacks the so role after the Admin's grant: %s", proto.name, umaID, got)
	}
}

// TestTenantSO_CLI_PartialSORefused_PerProtocol proves a partial SO -- a
// username with no PIN -- is refused on every transport and creates no tenant.
func TestTenantSO_CLI_PartialSORefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("tsop-%s-%d", proto.name, time.Now().UnixNano())
			adminCSR := tenantSOCSR(t, node, proto, tenantID+"-admin")
			soCSR := tenantSOCSR(t, node, proto, tenantID+"-so")
			if out, err := runCLIProto(t, node, proto, "tenant", "create",
				"--id", tenantID, "--name", tenantID, "--isolation-type", "prefix",
				"--admin-username", tenantID+"-admin", "--admin-csr", filepath.Join(adminCSR, "csr.pem"),
				"--so-username", tenantID+"-so", "--so-csr", filepath.Join(soCSR, "csr.pem"),
			); err == nil {
				t.Fatalf("[%s] tenant create with an SO lacking its PIN succeeded: %s", proto.name, out)
			}
			if out, err := runCLIProto(t, node, proto, "tenant", "get", tenantID); err == nil {
				t.Fatalf("[%s] the refused create left tenant %s behind: %s", proto.name, tenantID, out)
			}
		})
	}
}
