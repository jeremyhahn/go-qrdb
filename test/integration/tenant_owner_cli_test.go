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

// tenant_owner_cli_test.go drives a tenant Admin through the real qrdb CLI
// against a real node on every network transport, before and after the Admin
// role is withdrawn. Each transport's auth chain decides auth.Identity.
// TenantOwner on every authenticated request by reading the role store; no
// wire response carries the flag, so what is observable here is that the
// decision runs and never fails closed for a wired node: the owner is served,
// the demoted caller's next request is still served where its remaining role
// allows it, and an Admin-only verb is then refused as a permission denial,
// not as an undecidable ownership. The flag's value is asserted by the unit
// tests in pkg/auth/middleware and on the embedded login in
// tenant_owner_test.go.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// TestTenantOwner_CLI_DemotionTakesEffectOnTheNextRequest_PerProtocol runs
// the tenant Admin lifecycle over every transport.
func TestTenantOwner_CLI_DemotionTakesEffectOnTheNextRequest_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantOwnerDemotionOverProtocol(t, node, proto)
		})
	}
}

func testTenantOwnerDemotionOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantID := "towner-" + suffix
	const ownerName = "towner-admin"

	firstCert, firstKey := createTenantWithAdminCert(t, node, proto, tenantID, "Tenant Owner", "prefix", false, tenantID+"-admin")
	firstClient := mtlsHTTPClientFromFiles(t, node.ca, firstCert, firstKey, 15*time.Second)
	// Creating the reader role is SO-exclusive; the tenant's first admin
	// takes the tenant's own SO role for this setup step only.
	selfAssignTenantSO(t, node.restPort, tenantID, firstClient)

	roleBody, status := iraftAPIPost(t, firstClient, fmt.Sprintf("https://127.0.0.1:%d/roles", node.restPort),
		map[string]any{"name": "towner-reader", "tenant_id": tenantID, "permissions": []string{"users:read"}})
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("[%s] create reader role: status %d, body: %s", proto.name, status, roleBody)
	}
	var reader struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &reader); err != nil || reader.ID == "" {
		t.Fatalf("[%s] decode reader role id: %v body=%s", proto.name, err, roleBody)
	}

	adminRoleID := role.SystemRoleID(tenantID, role.RoleNameAdmin)
	// The certificate's CN names the account it is bound to.
	kp := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(ownerName, tenantID))
	cert, key := kp.CertPath, kp.KeyPath
	ownerID := createBoundUser(t, firstClient, node.restPort, map[string]any{
		"username":  ownerName,
		"email":     ownerName + "@" + tenantID + ".example.com",
		"tenant_id": tenantID,
		"role_ids":  []string{adminRoleID, reader.ID},
	}, cert)
	id := strconv.FormatUint(ownerID, 10)
	conn := tenantCLIArgsForProto(node, proto, cert, key)
	firstConn := tenantCLIArgsForProto(node, proto, firstCert, firstKey)

	ownerRoles := func(step string) []string {
		t.Helper()
		out, err := runQRDB(t, node.cli, conn, "--output", "json", "users", "get", id)
		if err != nil {
			t.Fatalf("[%s] %s: users get as the caller: %v\noutput: %s", proto.name, step, err, out)
		}
		var u userCLIJSON
		if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
			t.Fatalf("[%s] %s: decode users get JSON: %v\noutput: %s", proto.name, step, jerr, out)
		}
		return u.RoleIDs
	}

	if !stringSliceContains(ownerRoles("as owner"), adminRoleID) {
		t.Fatalf("[%s] the caller must hold %s before demotion", proto.name, adminRoleID)
	}
	if out, err := runQRDB(t, node.cli, conn, "users", "create", "--without-email-login-key",
		"--username", "towner-probe-owner"); err != nil {
		t.Fatalf("[%s] users create as the tenant Admin: %v\noutput: %s", proto.name, err, out)
	}

	if out, err := runQRDB(t, node.cli, firstConn, "roles", "unassign", "--user", id, "--role", adminRoleID); err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("[%s] roles unassign admin: %v\noutput: %s", proto.name, err, out)
	}

	// The very next request is decided again: served under the reader role
	// that remains, with Admin gone from the record.
	if stringSliceContains(ownerRoles("after demotion"), adminRoleID) {
		t.Fatalf("[%s] the caller still holds %s after unassign", proto.name, adminRoleID)
	}
	out, err := runQRDB(t, node.cli, conn, "users", "create", "--without-email-login-key",
		"--username", "towner-probe-demoted")
	if err == nil {
		t.Fatalf("[%s] users create succeeded for the demoted caller: %s", proto.name, out)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "permission denied") && !strings.Contains(lower, "forbidden") &&
		!strings.Contains(lower, "not permitted") && !strings.Contains(lower, "access denied") {
		t.Fatalf("[%s] users create for the demoted caller must be a permission denial, got: %s", proto.name, out)
	}
	if strings.Contains(lower, "tenant ownership") || strings.Contains(lower, "unavailable") {
		t.Fatalf("[%s] the demoted caller's request failed the ownership decision instead of authorization: %s", proto.name, out)
	}
}
