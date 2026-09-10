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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// User CRUD
// ===========================================================================

func TestRESTCreateUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/users"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != 1 || user.Username != "alice" {
		t.Fatalf("unexpected: %+v", user)
	}
}

func TestRESTCreateUser_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/users"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCreateUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.GetUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.ID != 1 {
		t.Fatalf("ID = %d", user.ID)
	}
}

func TestRESTGetUser_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/users/9999"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.GetUser(context.Background(), 9999)
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got %T", err)
	}
}

func TestRESTGetUser_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.GetUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTGetUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetUser(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetUser_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not-json"))
	})
	_, err := rt.GetUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTGetUserByUsername_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-username/"), func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "tenantID=t1") {
			t.Errorf("expected tenantID in query, got %q", r.URL.RawQuery)
		}
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.GetUserByUsername(context.Background(), "t1", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user.Username != "alice" {
		t.Fatalf("Username = %q", user.Username)
	}
}

func TestRESTGetUserByUsername_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-username/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.GetUserByUsername(context.Background(), "t1", "missing")
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got %T", err)
	}
}

func TestRESTGetUserByUsername_NoTenant(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-username/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	if _, err := rt.GetUserByUsername(context.Background(), "", "alice"); err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
}

func TestRESTGetUserByUsername_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetUserByUsername(context.Background(), "t1", "alice")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTUpdateUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPut, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice2"})
	})
	user, err := rt.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if user.Username != "alice2" {
		t.Fatalf("Username = %q", user.Username)
	}
}

func TestRESTUpdateUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTDeleteUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteUser(context.Background(), 1); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestRESTDeleteUser_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/users/1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	err := rt.DeleteUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTDeleteUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DeleteUser(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListUsers_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListUsersResponse{
			Users: []*transport.User{{ID: 1}},
			Total: 1,
		})
	})
	resp, err := rt.ListUsers(context.Background(), &transport.ListUsersRequest{TenantID: "t1", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d", resp.Total)
	}
}

func TestRESTListUsers_NoParams(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListUsersResponse{})
	})
	if _, err := rt.ListUsers(context.Background(), &transport.ListUsersRequest{}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
}

func TestRESTListUsers_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListUsers(context.Background(), &transport.ListUsersRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTListUsers_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListUsers(context.Background(), &transport.ListUsersRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTEnableUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/users/1/enable"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.EnableUser(context.Background(), 1); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
}

func TestRESTEnableUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.EnableUser(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTDisableUser_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/users/1/disable"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.DisableUser(context.Background(), 1); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
}

func TestRESTDisableUser_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DisableUser(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Role CRUD
// ===========================================================================

func TestRESTCreateRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/roles"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.Role{ID: "r1", Name: "admin"})
	})
	role, err := rt.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "admin"})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.Name != "admin" {
		t.Fatalf("Name = %q", role.Name)
	}
}

func TestRESTCreateRole_ForwardsPreAssignedID(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	const wantID = "deterministic-admin-id"
	var gotID string
	rs.handle(exactPath(http.MethodPost, "/roles"), func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotID = body.ID
		writeJSON(w, &transport.Role{ID: body.ID, Name: body.Name})
	})
	role, err := rt.CreateRole(context.Background(), &transport.CreateRoleRequest{
		ID:   wantID,
		Name: "admin",
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if gotID != wantID {
		t.Errorf("wire body id: got %q, want %q", gotID, wantID)
	}
	if role.ID != wantID {
		t.Errorf("returned role ID: got %q, want %q", role.ID, wantID)
	}
}

func TestRESTCreateRole_EmptyIDOmitted(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	sawIDKey := false
	rs.handle(exactPath(http.MethodPost, "/roles"), func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, sawIDKey = raw["id"]
		writeJSON(w, &transport.Role{ID: "generated-uuid", Name: "admin"})
	})
	role, err := rt.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "admin"})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if sawIDKey {
		t.Error("expected id to be omitted from wire body when empty")
	}
	if role.ID != "generated-uuid" {
		t.Errorf("returned role ID: got %q, want server-generated", role.ID)
	}
}

func TestRESTCreateRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CreateRole(context.Background(), &transport.CreateRoleRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.Role{ID: "r1", Name: "admin"})
	})
	role, err := rt.GetRole(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if role.ID != "r1" {
		t.Fatalf("ID = %q", role.ID)
	}
}

func TestRESTGetRole_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.GetRole(context.Background(), "r1")
	var nfe *transport.RoleNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected RoleNotFoundError, got %T", err)
	}
}

func TestRESTGetRole_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.GetRole(context.Background(), "r1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTGetRole_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	})
	_, err := rt.GetRole(context.Background(), "r1")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTGetRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetRole(context.Background(), "r1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTUpdateRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPut, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.Role{ID: "r1", Name: "admin2"})
	})
	role, err := rt.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1"})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if role.Name != "admin2" {
		t.Fatalf("Name = %q", role.Name)
	}
}

func TestRESTUpdateRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1"})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTDeleteRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteRole(context.Background(), "r1"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
}

func TestRESTDeleteRole_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	err := rt.DeleteRole(context.Background(), "r1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTDeleteRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DeleteRole(context.Background(), "r1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListRoles_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/roles/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListRolesResponse{Roles: []*transport.Role{{ID: "r1"}}, Total: 1})
	})
	resp, err := rt.ListRoles(context.Background(), &transport.ListRolesRequest{TenantID: "t1", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d", resp.Total)
	}
}

func TestRESTListRoles_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/roles/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListRoles(context.Background(), &transport.ListRolesRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTListRoles_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListRoles(context.Background(), &transport.ListRolesRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTAssignRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.AssignRoleRequest
	rs.handle(exactPath(http.MethodPost, "/roles/assign"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.AssignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if captured.UserID != 1 || captured.RoleID != "r1" {
		t.Fatalf("captured = %+v", captured)
	}
}

func TestRESTAssignRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.AssignRole(context.Background(), 1, "r1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTUnassignRole_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/roles/unassign"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.UnassignRole(context.Background(), 1, "r1"); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}
}

func TestRESTUnassignRole_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.UnassignRole(context.Background(), 1, "r1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListPermissions_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/permissions"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListPermissionsResponse{Categories: []*transport.PermissionCategory{{Name: "kv"}}})
	})
	resp, err := rt.ListPermissions(context.Background())
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(resp.Categories) != 1 {
		t.Fatalf("got %d perms", len(resp.Categories))
	}
}

func TestRESTListPermissions_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/permissions"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListPermissions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTListPermissions_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListPermissions(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Cert operations
// ===========================================================================

func TestRESTGenerateCert_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/generate"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CertBundle{Serial: "001", CertPEM: "CERT"})
	})
	bundle, err := rt.GenerateCert(context.Background(), &transport.GenerateCertRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("GenerateCert: %v", err)
	}
	if bundle.Serial != "001" {
		t.Fatalf("Serial = %q", bundle.Serial)
	}
}

func TestRESTGenerateCert_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GenerateCert(context.Background(), &transport.GenerateCertRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTExportCert_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/export"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ExportCertResponse{PKCS12: "BASE64"})
	})
	resp, err := rt.ExportCert(context.Background(), &transport.ExportCertRequest{CertPEM: "CERT", KeyPEM: "KEY", Passphrase: "x"})
	if err != nil {
		t.Fatalf("ExportCert: %v", err)
	}
	if resp.PKCS12 != "BASE64" {
		t.Fatalf("PKCS12Base64 = %q", resp.PKCS12)
	}
}

func TestRESTExportCert_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ExportCert(context.Background(), &transport.ExportCertRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRevokeCert_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/revoke"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "recordFound": true})
	})
	result, err := rt.RevokeCert(context.Background(), "001")
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if !result.RecordFound {
		t.Fatal("RecordFound should be true")
	}
}

func TestRESTRevokeCert_DenyBySerial(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/revoke"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "recordFound": false})
	})
	result, err := rt.RevokeCert(context.Background(), "peer-serial")
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if result.RecordFound {
		t.Fatal("RecordFound should be false for a deny-by-serial revocation")
	}
}

func TestRESTRevokeCert_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RevokeCert(context.Background(), "001")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListCerts_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/certs/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListCertsResponse{Certs: []*transport.CertRecord{{Serial: "001"}}})
	})
	resp, err := rt.ListCerts(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(resp.Certs) != 1 {
		t.Fatalf("got %d certs", len(resp.Certs))
	}
}

func TestRESTListCerts_NoTenant(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/certs/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListCertsResponse{})
	})
	if _, err := rt.ListCerts(context.Background(), ""); err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
}

func TestRESTListCerts_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/certs/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListCerts(context.Background(), "t1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTListCerts_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListCerts(context.Background(), "t1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// AuthFactor methods
// ===========================================================================

func TestRESTEnrollFactor_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/authfactors/pkcs11/enroll"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.EnrollFactorResponse{EnrollmentID: "enr-1"})
	})
	resp, err := rt.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{FactorName: "pkcs11"})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "enr-1" {
		t.Fatalf("EnrollmentID = %q", resp.EnrollmentID)
	}
}

func TestRESTEnrollFactor_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTChallengeFactor_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/authfactors/enr-1/challenge"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ChallengeFactorResponse{ChallengeID: "ch-1"})
	})
	resp, err := rt.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "enr-1"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "ch-1" {
		t.Fatalf("ChallengeID = %q", resp.ChallengeID)
	}
}

func TestRESTChallengeFactor_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTVerifyFactor_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/authfactors/ch-1/verify"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{ChallengeID: "ch-1"}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
}

func TestRESTVerifyFactor_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListFactors_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/authfactors/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListFactorsResponse{Total: 1})
	})
	resp, err := rt.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d", resp.Total)
	}
}

func TestRESTListFactors_NilRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/authfactors/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListFactorsResponse{})
	})
	if _, err := rt.ListFactors(context.Background(), nil); err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
}

func TestRESTListFactors_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRevokeFactor_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/authfactors/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "enr-1"}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
}

func TestRESTRevokeFactor_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Audit
// ===========================================================================

func TestRESTQueryAudit_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/audit/events"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.AuditPage{Total: 1})
	})
	resp, err := rt.QueryAudit(context.Background(), transport.AuditFilter{})
	if err != nil {
		t.Fatalf("QueryAudit: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d", resp.Total)
	}
}

func TestRESTQueryAudit_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.QueryAudit(context.Background(), transport.AuditFilter{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}
