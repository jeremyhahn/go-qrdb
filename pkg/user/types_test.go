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

package user

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUser_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	original := &User{
		ID:        9001,
		Username:  "alice",
		TenantID:  "acme",
		RoleIDs:   []string{"admin", "viewer"},
		Enabled:   true,
		System:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded User
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID: got %d, want %d", decoded.ID, original.ID)
	}
	if decoded.Username != original.Username {
		t.Errorf("Username: got %q, want %q", decoded.Username, original.Username)
	}
	if decoded.TenantID != original.TenantID {
		t.Errorf("TenantID: got %q, want %q", decoded.TenantID, original.TenantID)
	}
	if len(decoded.RoleIDs) != 2 {
		t.Fatalf("RoleIDs: got %d, want 2", len(decoded.RoleIDs))
	}
	if decoded.RoleIDs[0] != "admin" || decoded.RoleIDs[1] != "viewer" {
		t.Errorf("RoleIDs: got %v, want [admin viewer]", decoded.RoleIDs)
	}
	if decoded.Enabled != original.Enabled {
		t.Errorf("Enabled: got %v, want %v", decoded.Enabled, original.Enabled)
	}
	if decoded.System != original.System {
		t.Errorf("System: got %v, want %v", decoded.System, original.System)
	}
}

func TestUser_JSONEmptyRoleIDs(t *testing.T) {
	original := &User{
		ID:       9001,
		Username: "bob",
		TenantID: "system",
		RoleIDs:  []string{},
		Enabled:  true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded User
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.RoleIDs == nil {
		t.Fatal("expected non-nil empty RoleIDs")
	}
	if len(decoded.RoleIDs) != 0 {
		t.Errorf("RoleIDs length: got %d, want 0", len(decoded.RoleIDs))
	}
}

func TestListUsersResponse_JSON(t *testing.T) {
	resp := &ListUsersResponse{
		Users:   []*User{{ID: 1, Username: "alice"}},
		Total:   10,
		HasMore: true,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ListUsersResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Total != 10 {
		t.Errorf("Total: got %d, want 10", decoded.Total)
	}
	if !decoded.HasMore {
		t.Error("expected HasMore=true")
	}
	if len(decoded.Users) != 1 {
		t.Fatalf("Users: got %d, want 1", len(decoded.Users))
	}
	if decoded.Users[0].ID != 1 {
		t.Errorf("Users[0].ID: got %d, want %d", decoded.Users[0].ID, 1)
	}
}

func TestListUsersResponse_EmptyJSON(t *testing.T) {
	resp := &ListUsersResponse{
		Users:   []*User{},
		Total:   0,
		HasMore: false,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ListUsersResponse
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
