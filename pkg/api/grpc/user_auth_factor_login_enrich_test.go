// Copyright 2026 Jeremy Hahn
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

package grpc

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestUserAuthFactorLogin_EnrichesFromUserService verifies the Login handler
// resolves the user record and populates username, tenant, and roles when a
// user servicer is wired.
func TestUserAuthFactorLogin_EnrichesFromUserService(t *testing.T) {
	factorSvc := newMockUserAuthFactorSvc() // lookup returns UserID 42
	userSvc := &mockUserService{
		getResult: &user.User{
			ID:       42,
			Username: "alice",
			TenantID: "acme",
			RoleIDs:  []string{"admin", "editor"},
			Enabled:  true,
		},
	}
	srv := &userAuthFactorServer{svc: factorSvc, userSvc: userSvc}

	resp, err := srv.Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if resp.UserId != 42 {
		t.Errorf("UserId: got %d, want 42", resp.UserId)
	}
	if resp.Username != "alice" {
		t.Errorf("Username: got %q, want %q", resp.Username, "alice")
	}
	if resp.TenantId != "acme" {
		t.Errorf("TenantId: got %q, want %q", resp.TenantId, "acme")
	}
	if len(resp.Roles) != 2 || resp.Roles[0] != "admin" {
		t.Errorf("Roles: got %v, want [admin editor]", resp.Roles)
	}
	if !resp.Active {
		t.Error("Active: got false, want true")
	}
	if userSvc.lastGetID != 42 {
		t.Errorf("lastGetID: got %d, want 42", userSvc.lastGetID)
	}
}

// TestUserAuthFactorLogin_NoUserServiceLeavesBaseFields verifies the handler
// returns the resolved user ID and factor without enrichment when no user
// servicer is wired (backward-compatible behavior).
func TestUserAuthFactorLogin_NoUserServiceLeavesBaseFields(t *testing.T) {
	srv := &userAuthFactorServer{svc: newMockUserAuthFactorSvc()}

	resp, err := srv.Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if resp.UserId != 42 {
		t.Errorf("UserId: got %d, want 42", resp.UserId)
	}
	if resp.Username != "" {
		t.Errorf("Username: got %q, want empty", resp.Username)
	}
	if !resp.Active {
		t.Error("Active: got false, want true (default when unenriched)")
	}
}
