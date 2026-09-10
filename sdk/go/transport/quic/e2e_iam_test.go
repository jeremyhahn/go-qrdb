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

package quic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func userJSON(u *userWireResponse) []byte {
	b, _ := json.Marshal(u)
	return b
}

func TestE2E_CreateUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, userJSON(&userWireResponse{
			ID: 1, Username: "alice", TenantID: "t1", Enabled: true,
		})
	})
	resp, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "t1"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if resp.ID != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, userJSON(&userWireResponse{ID: 1, Username: "alice"})
	})
	resp, err := tr.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if resp.Username != "alice" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetUserByUsername_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByUsername", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, userJSON(&userWireResponse{ID: 1, Username: "alice", TenantID: "t1"})
	})
	resp, err := tr.GetUserByUsername(context.Background(), "t1", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if resp.ID != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, userJSON(&userWireResponse{ID: 1, Username: "alice2"})
	})
	resp, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1, Username: "alice2"})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if resp.Username != "alice2" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DeleteUser(context.Background(), 1); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestE2E_ListUsers_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.List", func(_ []byte) (uint16, []byte) {
		out := listUsersWireResponse{
			Users: []*userWireResponse{
				{ID: 1, Username: "alice"},
				{ID: 2, Username: "bob"},
			},
			Total: 2,
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(resp.Users) != 2 || resp.Total != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_EnableUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Enable", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.EnableUser(context.Background(), 1); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
}

func TestE2E_DisableUser_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Disable", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DisableUser(context.Background(), 1); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func roleJSON(r *roleWireResponse) []byte {
	b, _ := json.Marshal(r)
	return b
}

func TestE2E_CreateRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, roleJSON(&roleWireResponse{
			ID: "r1", Name: "admin", TenantID: "t1", Permissions: []string{"kv:read"},
		})
	})
	resp, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{
		Name: "admin", TenantID: "t1", Permissions: []string{"kv:read"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, roleJSON(&roleWireResponse{ID: "r1", Name: "admin"})
	})
	resp, err := tr.GetRole(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if resp.Name != "admin" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, roleJSON(&roleWireResponse{ID: "r1", Name: "renamed"})
	})
	resp, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1", Name: "renamed"})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if resp.Name != "renamed" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DeleteRole(context.Background(), "r1"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}

func TestE2E_ListRoles_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.List", func(_ []byte) (uint16, []byte) {
		out := listRolesWireResponse{
			Roles: []*roleWireResponse{
				{ID: "r1", Name: "admin"},
			},
			Total: 1,
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(resp.Roles) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_AssignRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Assign", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.AssignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
}

func TestE2E_UnassignRole_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Unassign", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.UnassignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
}

func TestE2E_ListPermissions_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.ListPermissions", func(_ []byte) (uint16, []byte) {
		cats := []permissionCategoryWireResponse{
			{Name: "KV", Group: "data", Permissions: []string{"kv:read", "kv:write"}},
		}
		b, _ := json.Marshal(cats)
		return wire.StatusOK, b
	})
	resp, err := tr.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(resp.Categories) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Certs
// ---------------------------------------------------------------------------

func TestE2E_GenerateCert_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Generate", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(certBundleWireResponse{CertPEM: "cert", KeyPEM: "key", Serial: "abc"})
		return wire.StatusOK, b
	})
	resp, err := tr.GenerateCert(context.Background(), &transport.GenerateCertRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("GenerateCert: %v", err)
	}
	if resp.Serial != "abc" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ExportCert_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Export", func(_ []byte) (uint16, []byte) {
		b, _ := json.Marshal(exportCertWireResponse{PKCS12: "base64-pkcs12"})
		return wire.StatusOK, b
	})
	resp, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{
		CertPEM: "cert", KeyPEM: "key", Passphrase: "p",
	})
	if err != nil {
		t.Fatalf("ExportCert: %v", err)
	}
	if resp.PKCS12 == "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_RevokeCert_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Revoke", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte(`{"status":"ok","recordFound":true}`)
	})
	result, err := tr.RevokeCert(context.Background(), "abc")
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if !result.RecordFound {
		t.Error("RecordFound should be true")
	}
}

func TestE2E_ListCerts_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.List", func(_ []byte) (uint16, []byte) {
		out := []certRecordWireResponse{
			{Serial: "abc", Username: "alice", TenantID: "t1"},
		}
		b, _ := json.Marshal(out)
		return wire.StatusOK, b
	})
	resp, err := tr.ListCerts(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(resp.Certs) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

// The QUIC email lookup is stringly typed on both sides — the method name
// "user.GetByEmail" and the JSON keys tenantID/email are handwritten in the
// client and matched by name on the server — so a typo compiles cleanly and
// fails only at runtime. This asserts the wire contract, not just that a call
// returns something (Codex, PR #191).
func TestE2E_GetUserByEmail_WireContract(t *testing.T) {
	srv, tr := startE2EServer(t)

	srv.SetResponder("user.GetByEmail", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, userJSON(&userWireResponse{
			ID: 42, Username: "alice", Email: "alice@example.com", TenantID: "t1", Enabled: true,
		})
	})

	resp, err := tr.GetUserByEmail(context.Background(), "t1", "alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}

	// Read the request back through the harness's recorder, which is guarded by
	// recordMu. Capturing it into a closure variable instead was a genuine data
	// race: the responder runs on the server's per-stream goroutine while these
	// assertions run on the test goroutine, and a QUIC round trip is not a
	// happens-before edge between them. It passed locally because nothing here
	// is ever run under -race — only CI's sdk/go job does that (PR #191).
	rec := srv.LastRequest("user.GetByEmail")
	// The method name reached the server: nothing is recorded otherwise.
	if rec == nil {
		t.Fatal(`no request reached "user.GetByEmail" — the method name does not match the server route`)
	}
	var gotPayload map[string]any
	if uErr := json.Unmarshal(rec.Payload, &gotPayload); uErr != nil {
		t.Fatalf("request payload is not JSON: %v", uErr)
	}
	if gotPayload["email"] != "alice@example.com" {
		t.Fatalf(`payload key "email" wrong or missing: %v`, gotPayload)
	}
	if gotPayload["tenantID"] != "t1" {
		t.Fatalf(`payload key "tenantID" wrong or missing: %v`, gotPayload)
	}
	if resp.ID != 42 || resp.Email != "alice@example.com" {
		t.Fatalf("response not decoded: %+v", resp)
	}
}

func TestE2E_GetUserByEmail_NotFound(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByEmail", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte(`{"error":"not found"}`)
	})
	if _, err := tr.GetUserByEmail(context.Background(), "t1", "nobody@example.com"); err == nil {
		t.Fatal("expected a not-found error")
	}
}
