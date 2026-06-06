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

// pathAndQuery returns a request matcher for a method + exact path + required
// query parameter substring.
func pathAndQuery(method, path, querySubstr string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.Method == method &&
			r.URL.Path == path &&
			strings.Contains(r.URL.RawQuery, querySubstr)
	}
}

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestRESTGetRoleByName_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodGet, "/roles/by-name", "name=admin"),
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("tenant_id") != "t1" {
				http.Error(w, "missing tenant_id", http.StatusBadRequest)
				return
			}
			writeJSON(w, &transport.Role{
				ID:       "r-by-name",
				Name:     "admin",
				TenantID: "t1",
			})
		},
	)

	rl, err := rt.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if rl.ID != "r-by-name" {
		t.Errorf("ID = %q, want r-by-name", rl.ID)
	}
	if rl.Name != "admin" {
		t.Errorf("Name = %q, want admin", rl.Name)
	}
	if rl.TenantID != "t1" {
		t.Errorf("TenantID = %q, want t1", rl.TenantID)
	}
}

func TestRESTGetRoleByName_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		pathAndQuery(http.MethodGet, "/roles/by-name", "name="),
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		},
	)
	_, err := rt.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRESTGetRoleByName_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{
		TenantID: "t1",
		Name:     "admin",
	})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ResolvePermissions
// ---------------------------------------------------------------------------

func TestRESTResolvePermissions_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var gotRoleIDs []string
	rs.handle(
		exactPath(http.MethodPost, "/roles/resolve-permissions"),
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				RoleIDs []string `json:"role_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotRoleIDs = body.RoleIDs
			writeJSON(w, &transport.ResolvePermissionsResponse{
				Permissions: []string{"kv:read", "kv:write"},
			})
		},
	)

	resp, err := rt.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1", "r2"},
	})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(resp.Permissions) != 2 {
		t.Fatalf("Permissions len = %d, want 2: %v", len(resp.Permissions), resp.Permissions)
	}
	if len(gotRoleIDs) != 2 || gotRoleIDs[0] != "r1" || gotRoleIDs[1] != "r2" {
		t.Errorf("wire role_ids = %v, want [r1 r2]", gotRoleIDs)
	}
}

func TestRESTResolvePermissions_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(
		exactPath(http.MethodPost, "/roles/resolve-permissions"),
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		},
	)
	_, err := rt.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRESTResolvePermissions_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{
		RoleIDs: []string{"r1"},
	})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
