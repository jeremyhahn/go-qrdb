// Copyright (c) 2026 Jeremy Hahn
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

package role

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

func TestRole_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	original := &Role{
		ID:       "test-uuid",
		Name:     "admin",
		TenantID: "acme",
		Permissions: []permission.Permission{
			permission.KVPut, permission.KVGet,
		},
		System:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Role
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Name != original.Name {
		t.Errorf("Name: got %q, want %q", decoded.Name, original.Name)
	}
	if decoded.TenantID != original.TenantID {
		t.Errorf("TenantID: got %q, want %q", decoded.TenantID, original.TenantID)
	}
	if len(decoded.Permissions) != 2 {
		t.Fatalf("Permissions: got %d, want 2", len(decoded.Permissions))
	}
	if decoded.Permissions[0] != permission.KVPut || decoded.Permissions[1] != permission.KVGet {
		t.Errorf("Permissions: got %v, want [kv:put kv:get]", decoded.Permissions)
	}
	if decoded.System != original.System {
		t.Errorf("System: got %v, want %v", decoded.System, original.System)
	}
}

func TestRole_JSONEmptyPermissions(t *testing.T) {
	original := &Role{
		ID:          "test-uuid",
		Name:        "empty",
		TenantID:    "system",
		Permissions: []permission.Permission{},
		System:      false,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Role
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Permissions == nil {
		t.Fatal("expected non-nil empty Permissions")
	}
	if len(decoded.Permissions) != 0 {
		t.Errorf("Permissions length: got %d, want 0", len(decoded.Permissions))
	}
}

func TestListRolesResponse_JSON(t *testing.T) {
	resp := &ListRolesResponse{
		Roles:   []*Role{{ID: "r1", Name: "admin"}},
		Total:   10,
		HasMore: true,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ListRolesResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Total != 10 {
		t.Errorf("Total: got %d, want 10", decoded.Total)
	}
	if !decoded.HasMore {
		t.Error("expected HasMore=true")
	}
	if len(decoded.Roles) != 1 {
		t.Fatalf("Roles: got %d, want 1", len(decoded.Roles))
	}
	if decoded.Roles[0].ID != "r1" {
		t.Errorf("Roles[0].ID: got %q, want %q", decoded.Roles[0].ID, "r1")
	}
}

func TestListRolesResponse_EmptyJSON(t *testing.T) {
	resp := &ListRolesResponse{
		Roles:   []*Role{},
		Total:   0,
		HasMore: false,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ListRolesResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Total != 0 {
		t.Errorf("Total: got %d, want 0", decoded.Total)
	}
	if decoded.HasMore {
		t.Error("expected HasMore=false")
	}
}
